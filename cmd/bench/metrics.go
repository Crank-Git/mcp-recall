// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// cmd/bench/metrics.go
//
// Result and Summary types for the compression benchmark. All byte figures are
// exact; token figures come from the active Tokenizer (an estimate — see
// tokenizer.go).

package main

import "fmt"

// Result is the outcome of compressing one corpus fixture.
type Result struct {
	Category      string
	Name          string
	Tool          string
	Handler       string // handler/profile that actually fired
	InputBytes    int
	OutputBytes   int
	InputTokens   int
	OutputTokens  int
	Stored        bool  // compression was meaningful AND not secret-blocked
	SecretBlocked bool  // secrets detected → never stored (expected for secret fixtures)
	RoundTripOK   bool  // stored full content retrieved back byte-identical
	Expanded      bool  // summary was >= input (no compression win)
	LatencyNS     int64 // handler runtime per call (ns/op, median of a timed loop)
	Note          string
	// FactsKept of FactsListed: the fixture's Keep facts still in the summary.
	FactsKept   int
	FactsListed int
}

// FactsCell is "kept/listed" for a fixture's Keep facts, or "—" when it lists
// none.
func (r Result) FactsCell() string {
	if r.FactsListed == 0 {
		return "—"
	}
	return fmt.Sprintf("%d/%d", r.FactsKept, r.FactsListed)
}

// isEdge reports whether a fixture is an edge/robustness case (excluded from the
// "typical output" headline so tiny pings and malformed input don't skew it).
func (r Result) isEdge() bool { return r.Category == "edge" }

// ReductionPct is the byte reduction as a percentage (exact).
func (r Result) ReductionPct() float64 {
	if r.InputBytes == 0 {
		return 0
	}
	return (1 - float64(r.OutputBytes)/float64(r.InputBytes)) * 100
}

// TokensSaved is the estimated token delta (input − output).
func (r Result) TokensSaved() int { return r.InputTokens - r.OutputTokens }

// OK reports whether the fixture behaved as a healthy result: either it stored
// with a lossless round-trip, or it was correctly a pass-through / secret block.
func (r Result) OK() bool {
	if r.SecretBlocked {
		return !r.Stored // a detected secret must never be stored
	}
	if r.Stored {
		return r.RoundTripOK
	}
	return true // legitimate pass-through (below threshold / incompressible)
}

// Summary aggregates a run.
type Summary struct {
	Results          []Result
	TokenizerName    string
	TotalInputBytes  int
	TotalOutputBytes int
	TotalInputTokens int
	TotalOutTokens   int
	StoredCount      int
	PassThroughCount int
	SecretBlocked    int
	Failures         int   // round-trip failures or secrets that leaked
	TotalComputeNS   int64 // summed per-fixture compression time (ns)
	// Typical* cover stored, non-edge fixtures only — the real-world headline.
	TypicalInputBytes  int
	TypicalOutputBytes int
}

// Summarize folds per-fixture results into aggregate totals. Only stored items
// contribute to the compression totals — pass-throughs and secret blocks are
// counted separately so they don't dilute the ratio.
func Summarize(results []Result, tokenizerName string) Summary {
	s := Summary{Results: results, TokenizerName: tokenizerName}
	for _, r := range results {
		s.TotalComputeNS += r.LatencyNS
		if !r.OK() {
			s.Failures++
		}
		if r.SecretBlocked {
			s.SecretBlocked++
			continue
		}
		if !r.Stored {
			s.PassThroughCount++
			continue
		}
		s.StoredCount++
		s.TotalInputBytes += r.InputBytes
		s.TotalOutputBytes += r.OutputBytes
		s.TotalInputTokens += r.InputTokens
		s.TotalOutTokens += r.OutputTokens
		if !r.isEdge() {
			s.TypicalInputBytes += r.InputBytes
			s.TypicalOutputBytes += r.OutputBytes
		}
	}
	return s
}

// TypicalReductionPct is the byte reduction across stored, non-edge fixtures —
// the headline that reflects real tool outputs rather than robustness probes.
func (s Summary) TypicalReductionPct() float64 {
	if s.TypicalInputBytes == 0 {
		return 0
	}
	return (1 - float64(s.TypicalOutputBytes)/float64(s.TypicalInputBytes)) * 100
}

// ThroughputMBps is bytes compressed per second of compute time.
func (s Summary) ThroughputMBps() float64 {
	if s.TotalComputeNS == 0 {
		return 0
	}
	secs := float64(s.TotalComputeNS) / 1e9
	return (float64(s.TotalInputBytes) / (1024 * 1024)) / secs
}

// OverallReductionPct is the byte reduction across all stored items (exact).
func (s Summary) OverallReductionPct() float64 {
	if s.TotalInputBytes == 0 {
		return 0
	}
	return (1 - float64(s.TotalOutputBytes)/float64(s.TotalInputBytes)) * 100
}

// GrossTokensSaved is the estimated tokens saved assuming no retrieval.
func (s Summary) GrossTokensSaved() int { return s.TotalInputTokens - s.TotalOutTokens }

// TokenReductionPct is the estimated token reduction across stored items.
func (s Summary) TokenReductionPct() float64 {
	if s.TotalInputTokens == 0 {
		return 0
	}
	return (1 - float64(s.TotalOutTokens)/float64(s.TotalInputTokens)) * 100
}

// NetTokensSaved models a retrievalRate fraction of stored items being pulled
// back in full later — treated as yielding zero net saving for those items
// (Claude re-reads the content), so net = gross × (1 − retrievalRate). A
// deliberate simplification; retrievalRate in [0,1].
func (s Summary) NetTokensSaved(retrievalRate float64) int {
	return int(float64(s.GrossTokensSaved()) * (1 - retrievalRate))
}
