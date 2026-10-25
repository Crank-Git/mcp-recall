// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// cmd/bench/engine.go
//
// The headless benchmark engine: run each corpus fixture through the real
// compression pipeline (the same handlers.GetHandler dispatch the PostToolUse
// hook uses), verify losslessness against a live SQLite store, and record exact
// byte metrics plus estimated token savings. Deterministic and dependency-free
// aside from the compression packages themselves.

package main

import (
	"regexp"
	"time"

	"mcprecall/internal/db"
	"mcprecall/internal/handlers"
	"mcprecall/internal/profiles"
	"mcprecall/internal/secrets"
)

// Fixture is one benchmark input: a raw tool output plus the tool name that
// drives handler dispatch.
type Fixture struct {
	Category string
	Name     string
	Tool     string
	Input    any // optional tool_input (mostly nil)
	Content  string
	// Keep lists facts (regexes) a summary must still carry for Claude, as in
	// upstream's scripts/benchmark.ts; nil when the fixture lists none.
	Keep []string
}

var registered bool

// Run compresses every fixture and returns per-fixture results. It registers
// the profile resolver once so bundled/user profiles participate in dispatch,
// exactly as the real binary does at startup.
func Run(fixtures []Fixture, tok Tokenizer) []Result {
	if !registered {
		profiles.Register()
		registered = true
	}
	results := make([]Result, 0, len(fixtures))
	for _, f := range fixtures {
		results = append(results, runOne(f, tok))
	}
	return results
}

func runOne(f Fixture, tok Tokenizer) Result {
	full := handlers.ExtractText(f.Content)
	secretsFound := secrets.Find(full)

	handler := handlers.GetHandler(f.Tool, f.Content, f.Input)
	res := handler(f.Tool, f.Content)
	latency := timeHandler(handler, f.Tool, f.Content)

	r := Result{
		Category:      f.Category,
		Name:          f.Name,
		Tool:          f.Tool,
		Handler:       handlers.HandlerName(handler),
		InputBytes:    res.OriginalSize,
		OutputBytes:   len(res.Summary),
		InputTokens:   tok.Count(full),
		OutputTokens:  tok.Count(res.Summary),
		SecretBlocked: len(secretsFound) > 0,
		Expanded:      len(res.Summary) >= res.OriginalSize,
		LatencyNS:     latency,
		FactsListed:   len(f.Keep),
	}
	for _, re := range f.Keep {
		if regexp.MustCompile(re).MatchString(res.Summary) {
			r.FactsKept++
		}
	}

	// Mirror the hook: store only when compression is meaningful and no secret
	// was detected. Secret fixtures must never be stored.
	if r.SecretBlocked {
		r.Note = "secret detected → blocked"
		return r
	}
	if len(res.Summary) >= res.OriginalSize {
		r.Note = "pass-through (no compression win)"
		return r
	}

	r.Stored = true
	r.RoundTripOK = verifyRoundTrip(f, full, res)
	if !r.RoundTripOK {
		r.Note = "ROUND-TRIP MISMATCH"
	}
	return r
}

// timeHandler measures the handler's compression time as ns/op by running it in
// a loop for a small fixed budget and dividing — a real throughput measurement,
// not a single noisy sample. Large fixtures that exceed the budget in one call
// still report an honest single-op time.
func timeHandler(handler handlers.Handler, tool, content string) int64 {
	const budget = 8 * time.Millisecond
	start := time.Now()
	n := 0
	for {
		handler(tool, content)
		n++
		if elapsed := time.Since(start); elapsed >= budget {
			return elapsed.Nanoseconds() / int64(n)
		}
	}
}

// verifyRoundTrip stores the full content in a real in-memory SQLite store and
// asserts it comes back byte-identical — the compression is worthless if the
// original can't be recovered exactly.
func verifyRoundTrip(f Fixture, full string, res handlers.Result) bool {
	database, err := db.Open(":memory:")
	if err != nil {
		return false
	}
	defer database.Close()

	stored, err := db.StoreOutput(database, db.StoreInput{
		ProjectKey:   "bench",
		SessionID:    "bench",
		ToolName:     f.Tool,
		Summary:      res.Summary,
		FullContent:  full,
		OriginalSize: res.OriginalSize,
	})
	if err != nil {
		return false
	}
	got, err := db.RetrieveOutput(database, stored.ID)
	if err != nil || got == nil {
		return false
	}
	return got.FullContent == full
}
