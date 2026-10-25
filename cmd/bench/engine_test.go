// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// cmd/bench/engine_test.go

package main

import (
	"sync"
	"testing"
)

var (
	corpusOnce    sync.Once
	corpusResults []Result
)

// runCorpus runs the full corpus once and shares the (deterministic) results
// across tests, so the timing loop isn't paid per test function.
func runCorpus(t *testing.T) []Result {
	t.Helper()
	corpusOnce.Do(func() {
		fixtures, err := LoadCorpus()
		if err != nil {
			t.Fatalf("load corpus: %v", err)
		}
		corpusResults = Run(fixtures, heuristicTokenizer{})
	})
	return corpusResults
}

// freshRun bypasses the cache for tests that need independent runs.
func freshRun(t *testing.T) []Result {
	t.Helper()
	fixtures, err := LoadCorpus()
	if err != nil {
		t.Fatalf("load corpus: %v", err)
	}
	return Run(fixtures, heuristicTokenizer{})
}

func byName(results []Result, name string) (Result, bool) {
	for _, r := range results {
		if r.Name == name {
			return r, true
		}
	}
	return Result{}, false
}

// Every fixture must behave correctly: stored items round-trip losslessly,
// secrets are blocked, pass-throughs are legitimate. No exceptions.
func TestCorpusAllOK(t *testing.T) {
	for _, r := range runCorpus(t) {
		if !r.OK() {
			t.Errorf("fixture %q (%s) not OK: stored=%v roundtrip=%v secret=%v note=%q",
				r.Name, r.Category, r.Stored, r.RoundTripOK, r.SecretBlocked, r.Note)
		}
	}
}

// The secret fixture must be detected and never stored.
func TestSecretFixtureBlocked(t *testing.T) {
	r, ok := byName(runCorpus(t), "secret (blocked)")
	if !ok {
		t.Fatal("secret fixture missing from corpus")
	}
	if !r.SecretBlocked {
		t.Error("secret fixture was not flagged as containing a secret")
	}
	if r.Stored {
		t.Error("secret fixture was STORED — must never happen")
	}
}

// The tiny fixture is below the compression threshold → legitimate pass-through,
// not stored, zero data loss.
func TestTinyFixturePassThrough(t *testing.T) {
	r, ok := byName(runCorpus(t), "tiny (passthrough)")
	if !ok {
		t.Fatal("tiny fixture missing")
	}
	if r.Stored {
		t.Errorf("tiny fixture should pass through, got stored (in=%d out=%d)", r.InputBytes, r.OutputBytes)
	}
}

// Structured API fixtures should compress hard (well past 50%) and round-trip.
func TestApiFixturesCompressWell(t *testing.T) {
	results := runCorpus(t)
	for _, name := range []string{"github issues", "stripe events", "jira search"} {
		r, ok := byName(results, name)
		if !ok {
			t.Fatalf("fixture %q missing", name)
		}
		if !r.Stored || !r.RoundTripOK {
			t.Errorf("%q: stored=%v roundtrip=%v", name, r.Stored, r.RoundTripOK)
		}
		if r.ReductionPct() < 50 {
			t.Errorf("%q reduced only %.1f%% (expected >50%%)", name, r.ReductionPct())
		}
	}
}

// Aggregate sanity: meaningful net savings, no integrity failures, byte totals
// only count stored items.
func TestSummaryAggregates(t *testing.T) {
	s := Summarize(runCorpus(t), "test")
	if s.Failures != 0 {
		t.Errorf("expected 0 integrity failures, got %d", s.Failures)
	}
	if s.StoredCount == 0 {
		t.Fatal("nothing was stored")
	}
	if s.TotalInputBytes <= s.TotalOutputBytes {
		t.Errorf("no net compression: in=%d out=%d", s.TotalInputBytes, s.TotalOutputBytes)
	}
	if s.GrossTokensSaved() <= 0 {
		t.Errorf("gross tokens saved = %d, want > 0", s.GrossTokensSaved())
	}
	// Net at 25% retrieval must be positive but below gross.
	if net := s.NetTokensSaved(0.25); net <= 0 || net >= s.GrossTokensSaved() {
		t.Errorf("net@25%% = %d out of range (gross=%d)", net, s.GrossTokensSaved())
	}
}

// Large synthetic fixtures represent real payload sizes and must compress hard.
func TestLargeFixturesCompressHard(t *testing.T) {
	for _, r := range runCorpus(t) {
		if r.Category != "large" {
			continue
		}
		if !r.Stored || !r.RoundTripOK {
			t.Errorf("large %q: stored=%v roundtrip=%v note=%q", r.Name, r.Stored, r.RoundTripOK, r.Note)
			continue
		}
		if r.InputBytes < 40_000 {
			t.Errorf("large %q only %d bytes — not large enough to be representative", r.Name, r.InputBytes)
		}
		if r.ReductionPct() < 80 {
			t.Errorf("large %q reduced only %.1f%% (expected >80%%)", r.Name, r.ReductionPct())
		}
	}
}

// The native Bash tool path (git/test-runner/docker sub-handlers) must be
// exercised and compress well.
func TestBashFixtures(t *testing.T) {
	results := runCorpus(t)
	for _, name := range []string{"Bash: git status ×500", "Bash: go test ×1200", "Bash: docker ps ×120"} {
		r, ok := byName(results, name)
		if !ok {
			t.Fatalf("bash fixture %q missing", name)
		}
		if !r.Stored || !r.RoundTripOK || r.ReductionPct() < 50 {
			t.Errorf("%q: stored=%v roundtrip=%v reduction=%.1f%%", name, r.Stored, r.RoundTripOK, r.ReductionPct())
		}
	}
}

// Throughput must be a real, positive measurement, and the typical headline must
// beat the all-fixtures number (edge cases drag the latter down).
func TestThroughputAndTypicalHeadline(t *testing.T) {
	s := Summarize(runCorpus(t), "test")
	if s.TotalComputeNS <= 0 || s.ThroughputMBps() <= 0 {
		t.Errorf("throughput not measured: computeNS=%d mbps=%.2f", s.TotalComputeNS, s.ThroughputMBps())
	}
	if s.TypicalReductionPct() < s.OverallReductionPct() {
		t.Errorf("typical (%.1f%%) should be >= overall (%.1f%%)", s.TypicalReductionPct(), s.OverallReductionPct())
	}
	if s.TotalInputBytes < 1_000_000 {
		t.Errorf("corpus only %d bytes — expected >1MB of real payload", s.TotalInputBytes)
	}
}

// Determinism: identical inputs → identical byte metrics across runs.
func TestDeterministic(t *testing.T) {
	a := Summarize(freshRun(t), "x")
	b := Summarize(freshRun(t), "x")
	if a.TotalInputBytes != b.TotalInputBytes || a.TotalOutputBytes != b.TotalOutputBytes || a.StoredCount != b.StoredCount {
		t.Errorf("non-deterministic: %+v vs %+v", a, b)
	}
}

// Facts kept (upstream scripts/benchmark.ts): fixtures that list facts report
// how many survive in the summary; the rest show "—".
func TestFactsKept(t *testing.T) {
	results := runCorpus(t)
	for name, want := range map[string]string{
		"Bash: git --no-pager diff ×18": "2/2",
		"Bash: rg ×240 (6 files)":       "2/2",
		"Bash: git status ×500":         "—",
	} {
		r, ok := byName(results, name)
		if !ok {
			t.Fatalf("fixture %q missing", name)
		}
		if got := r.FactsCell(); got != want {
			t.Errorf("%q facts = %s, want %s", name, got, want)
		}
	}
}
