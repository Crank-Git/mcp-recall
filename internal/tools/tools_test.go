// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/tools/tools_test.go

package tools

import (
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"mcprecall/internal/config"
	"mcprecall/internal/db"
)

const projectKey = "tooltest1234567"

var noteIDRe = regexp.MustCompile("recall_[0-9a-f]{16}")

func setup(t *testing.T) *sql.DB {
	t.Helper()
	// Isolate config from any real ~/.config file.
	os.Setenv("RECALL_CONFIG_PATH", filepath.Join(t.TempDir(), "nope.toml"))
	config.Reset()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() {
		database.Close()
		os.Unsetenv("RECALL_CONFIG_PATH")
		config.Reset()
	})
	return database
}

func baseInput() db.StoreInput {
	return db.StoreInput{
		ProjectKey:   projectKey,
		SessionID:    "test-session-abc",
		ToolName:     "mcp__github__list_issues",
		Summary:      `#1 "Fix bug" [open] · labels: bug · body: Something is broken`,
		FullContent:  `[{"number":1,"title":"Fix bug","state":"open","body":"Something is broken"}]`,
		OriginalSize: 2048,
	}
}

func store(t *testing.T, database *sql.DB, in db.StoreInput) db.StoredOutput {
	t.Helper()
	s, err := db.StoreOutput(database, in)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	return s
}

func hasT(t *testing.T, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Errorf("expected to contain %q\n--- got ---\n%s", want, got)
	}
}
func hasNotT(t *testing.T, got, want string) {
	t.Helper()
	if strings.Contains(got, want) {
		t.Errorf("expected NOT to contain %q\n--- got ---\n%s", want, got)
	}
}

func today() string { return time.Now().UTC().Format("2006-01-02") }
func daysAgoUnix(n int) int64 {
	return time.Now().Unix() - int64(n)*86400
}
func backdate(database *sql.DB, id string, createdAt int64) {
	database.Exec("UPDATE stored_outputs SET created_at = ? WHERE id = ?", createdAt, id)
}

// ── toolRetrieve ──────────────────────────────────────────────────────────────

func TestRetrieveBasics(t *testing.T) {
	d := setup(t)
	hasT(t, Retrieve(d, RetrieveArgs{ID: "recall_00000000"}), "no item found")

	s := store(t, d, baseInput())
	r := Retrieve(d, RetrieveArgs{ID: s.ID})
	hasT(t, r, s.ID)
	hasT(t, r, "mcp__github__list_issues")
	hasT(t, r, "Fix bug")
	hasT(t, r, "KB") // 2048 → 2.0KB in header

	hasT(t, Retrieve(d, RetrieveArgs{ID: s.ID, Query: "broken"}), "Something is broken")

	in := baseInput()
	in.FullContent = strings.Repeat("x", 2000)
	s2 := store(t, d, in)
	hasT(t, Retrieve(d, RetrieveArgs{ID: s2.ID, Query: "x", MaxBytes: 100}), "truncated")
}

// A summary-only row (store.retention) has no body or chunks: full/peek must
// say so rather than return an empty result.
func TestRetrieveSummaryOnlyRow(t *testing.T) {
	d := setup(t)
	in := baseInput()
	zero := 0
	in.FullRetained = &zero
	s := store(t, d, in)

	for _, mode := range []string{"full", "peek"} {
		r := Retrieve(d, RetrieveArgs{ID: s.ID, Mode: mode})
		hasT(t, r, "was not retained")
		hasT(t, r, in.Summary)
	}
	// summary mode is unaffected — that content is still there.
	sum := Retrieve(d, RetrieveArgs{ID: s.ID, Mode: "summary"})
	if strings.Contains(sum, "was not retained") {
		t.Errorf("summary mode should not warn:\n%s", sum)
	}
}

func TestRetrieveAccessAndFTS(t *testing.T) {
	d := setup(t)
	s := store(t, d, baseInput())
	Retrieve(d, RetrieveArgs{ID: s.ID})
	got, _ := db.RetrieveOutput(d, s.ID)
	if got.AccessCount != 1 {
		t.Errorf("access_count = %d", got.AccessCount)
	}

	in := baseInput()
	in.FullContent = "The deployment pipeline uses kubernetes and helm charts"
	s2 := store(t, d, in)
	hasT(t, Retrieve(d, RetrieveArgs{ID: s2.ID, Query: "kubernetes"}), "kubernetes")

	in3 := baseInput()
	in3.FullContent = "hello world content"
	s3 := store(t, d, in3)
	hasT(t, Retrieve(d, RetrieveArgs{ID: s3.ID, Query: "zzznomatch"}), "hello world content")
}

func TestRetrieveGraduatedModes(t *testing.T) {
	d := setup(t)
	s := store(t, d, baseInput())
	sum := Retrieve(d, RetrieveArgs{ID: s.ID, Mode: "summary", Query: "broken"})
	hasT(t, sum, "labels: bug")
	hasNotT(t, sum, `"number":1`)

	hasT(t, Retrieve(d, RetrieveArgs{ID: s.ID, Mode: "full"}), `"number":1`)

	in := baseInput()
	in.FullContent = strings.Repeat("y", 2000)
	sf := store(t, d, in)
	hasT(t, Retrieve(d, RetrieveArgs{ID: sf.ID, Mode: "full", MaxBytes: 100}), "truncated")

	in2 := baseInput()
	in2.FullContent = "deployment uses kubernetes and helm across services"
	sp := store(t, d, in2)
	hasT(t, Retrieve(d, RetrieveArgs{ID: sp.ID, Mode: "peek", Query: "kubernetes"}), "kubernetes")

	in3 := baseInput()
	in3.FullContent = "alpha beta gamma delta epsilon"
	sh := store(t, d, in3)
	hasT(t, Retrieve(d, RetrieveArgs{ID: sh.ID, Mode: "peek"}), "alpha")

	// multi-chunk peek smaller than full
	var segs []string
	for i := 0; i < 60; i++ {
		segs = append(segs, "segment needle "+strings.Repeat("z", 100))
	}
	in4 := baseInput()
	in4.FullContent = strings.Join(segs, "\n")
	sm := store(t, d, in4)
	peek := Retrieve(d, RetrieveArgs{ID: sm.ID, Mode: "peek", Query: "needle"})
	whole := Retrieve(d, RetrieveArgs{ID: sm.ID, Mode: "full"})
	hasT(t, peek, "[…]")
	if len(peek) >= len(whole) {
		t.Errorf("peek not smaller than full")
	}

	// peek falls back to full when query matches no chunk
	in5 := baseInput()
	in5.FullContent = "hello world content here"
	sfb := store(t, d, in5)
	hasT(t, Retrieve(d, RetrieveArgs{ID: sfb.ID, Mode: "peek", Query: "zzznomatchxyz"}), "hello world content here")

	// peek not truncated by max_bytes
	var needles []string
	for i := 0; i < 40; i++ {
		needles = append(needles, "needle"+itoa(i)+" "+strings.Repeat("z", 100))
	}
	in6 := baseInput()
	in6.FullContent = strings.Join(needles, "\n")
	sn := store(t, d, in6)
	pk := Retrieve(d, RetrieveArgs{ID: sn.ID, Mode: "peek", Query: "needle5", MaxBytes: 10})
	hasNotT(t, pk, "truncated")
	if len(pk) <= 10 {
		t.Errorf("peek unexpectedly tiny: %q", pk)
	}

	// FTS operator chars tolerated (no panic/crash)
	in7 := baseInput()
	in7.FullContent = "alpha beta gamma delta"
	so := store(t, d, in7)
	_ = Retrieve(d, RetrieveArgs{ID: so.ID, Mode: "peek", Query: `alpha AND "beta`})
}

// ── toolSearch ────────────────────────────────────────────────────────────────

func TestSearch(t *testing.T) {
	d := setup(t)
	store(t, d, baseInput())
	hasT(t, Search(d, projectKey, SearchArgs{Query: "zzznomatch"}), "no results")

	in := baseInput()
	in.Summary = "critical authentication failure"
	store(t, d, in)
	r := Search(d, projectKey, SearchArgs{Query: "authentication"})
	hasT(t, r, "authentication")
	hasT(t, r, "Found 1 result")
}

func TestSearchFilterLimitSnippet(t *testing.T) {
	d := setup(t)
	a := baseInput()
	a.ToolName = "mcp__github__list_issues"
	a.Summary = "find me"
	store(t, d, a)
	b := baseInput()
	b.ToolName = "mcp__playwright__snapshot"
	b.Summary = "find me"
	store(t, d, b)
	r := Search(d, projectKey, SearchArgs{Query: "find", Tool: "github"})
	hasT(t, r, "mcp__github__list_issues")
	hasNotT(t, r, "playwright")

	d2 := setup(t)
	for i := 0; i < 5; i++ {
		in := baseInput()
		in.Summary = "result item number " + itoa(i)
		store(t, d2, in)
	}
	hasT(t, Search(d2, projectKey, SearchArgs{Query: "result", Limit: 2}), "Found 2 results")

	d3 := setup(t)
	in3 := baseInput()
	in3.Summary = "GitHub issues list"
	in3.FullContent = "Issue #42: implement the frobnication feature for power users"
	store(t, d3, in3)
	sr := Search(d3, projectKey, SearchArgs{Query: "frobnication"})
	hasT(t, sr, ">")
	hasT(t, sr, "frobnication")

	// graceful fallback (empty full_content)
	d4 := setup(t)
	in4 := baseInput()
	in4.Summary = "ghosttoken appears only in summary"
	in4.FullContent = ""
	store(t, d4, in4)
	gr := Search(d4, projectKey, SearchArgs{Query: "ghosttoken"})
	hasT(t, gr, "ghosttoken")
	hasT(t, gr, "Found 1 result")
}

// ── toolForget ────────────────────────────────────────────────────────────────

func TestForget(t *testing.T) {
	d := setup(t)
	hasT(t, Forget(d, projectKey, ForgetArgs{All: true}), "requires confirmed: true")

	store(t, d, baseInput())
	store(t, d, baseInput())
	hasT(t, Forget(d, projectKey, ForgetArgs{All: true, Confirmed: true}), "deleted 2 items")

	s := store(t, d, baseInput())
	hasT(t, Forget(d, projectKey, ForgetArgs{ID: s.ID}), "deleted 1 item")
	hasT(t, Forget(d, projectKey, ForgetArgs{ID: "recall_00000000"}), "nothing deleted")

	zero := 0
	neg := -5
	hasT(t, Forget(d, projectKey, ForgetArgs{OlderThanDays: &zero}), "older_than_days must be at least 1")
	hasT(t, Forget(d, projectKey, ForgetArgs{OlderThanDays: &neg}), "older_than_days must be at least 1")
}

func TestForgetByToolAndOlderThan(t *testing.T) {
	d := setup(t)
	g1 := baseInput()
	g1.ToolName = "mcp__github__list_issues"
	store(t, d, g1)
	store(t, d, g1)
	p := baseInput()
	p.ToolName = "mcp__playwright__snapshot"
	store(t, d, p)
	hasT(t, Forget(d, projectKey, ForgetArgs{Tool: "mcp__github__list_issues"}), "deleted 2 items")

	d2 := setup(t)
	old := store(t, d2, baseInput())
	fresh := store(t, d2, baseInput())
	backdate(d2, old.ID, daysAgoUnix(10))
	seven := 7
	hasT(t, Forget(d2, projectKey, ForgetArgs{OlderThanDays: &seven}), "deleted 1 item")
	if g, _ := db.RetrieveOutput(d2, old.ID); g != nil {
		t.Error("old should be deleted")
	}
	if g, _ := db.RetrieveOutput(d2, fresh.ID); g == nil {
		t.Error("fresh should remain")
	}

	d3 := setup(t)
	store(t, d3, baseInput())
	thirty := 30
	hasT(t, Forget(d3, projectKey, ForgetArgs{OlderThanDays: &thirty}), "nothing deleted")

	// pinned not deleted by older_than
	d4 := setup(t)
	pin := store(t, d4, baseInput())
	unp := store(t, d4, baseInput())
	backdate(d4, pin.ID, daysAgoUnix(10))
	backdate(d4, unp.ID, daysAgoUnix(10))
	db.PinOutput(d4, pin.ID, projectKey, true)
	hasT(t, Forget(d4, projectKey, ForgetArgs{OlderThanDays: &seven}), "deleted 1 item")
	if g, _ := db.RetrieveOutput(d4, pin.ID); g == nil {
		t.Error("pinned should survive older_than")
	}
}

func TestForgetPinAwareness(t *testing.T) {
	d := setup(t)
	s := store(t, d, baseInput())
	db.PinOutput(d, s.ID, projectKey, true)
	store(t, d, baseInput())
	hasT(t, Forget(d, projectKey, ForgetArgs{All: true, Confirmed: true}), "deleted 1 item")
	if g, _ := db.RetrieveOutput(d, s.ID); g == nil {
		t.Error("pinned should survive all")
	}

	d2 := setup(t)
	s2 := store(t, d2, baseInput())
	db.PinOutput(d2, s2.ID, projectKey, true)
	Forget(d2, projectKey, ForgetArgs{All: true, Confirmed: true, Force: true})
	if g, _ := db.RetrieveOutput(d2, s2.ID); g != nil {
		t.Error("force should delete pinned")
	}
}

// ── toolListStored ────────────────────────────────────────────────────────────

func TestListStored(t *testing.T) {
	d := setup(t)
	hasT(t, ListStored(d, projectKey, ListStoredArgs{}), "no stored items")
	store(t, d, baseInput())
	r := ListStored(d, projectKey, ListStoredArgs{})
	hasT(t, r, "recall_")
	hasT(t, r, "mcp__github__list_issues")

	d2 := setup(t)
	store(t, d2, baseInput())
	hasT(t, ListStored(d2, projectKey, ListStoredArgs{Limit: 10, Offset: 100}), "no more items")

	// filter
	d3 := setup(t)
	g := baseInput()
	g.ToolName = "mcp__github__list_issues"
	store(t, d3, g)
	p := baseInput()
	p.ToolName = "mcp__playwright__snapshot"
	store(t, d3, p)
	fr := ListStored(d3, projectKey, ListStoredArgs{Tool: "github"})
	hasT(t, fr, "mcp__github__list_issues")
	hasNotT(t, fr, "playwright")

	// sort=size
	d4 := setup(t)
	small := baseInput()
	small.OriginalSize = 100
	store(t, d4, small)
	big := baseInput()
	big.OriginalSize = 5000
	store(t, d4, big)
	sz := ListStored(d4, projectKey, ListStoredArgs{Sort: "size"})
	if strings.Index(sz, "5.0KB") > strings.Index(sz, "100B") {
		t.Error("size sort: 5.0KB should come first")
	}

	// sort=accessed + pin indicator
	d5 := setup(t)
	a := store(t, d5, baseInput())
	b := store(t, d5, baseInput())
	d5.Exec("UPDATE stored_outputs SET access_count = 5 WHERE id = ?", b.ID)
	acc := ListStored(d5, projectKey, ListStoredArgs{Sort: "accessed"})
	if strings.Index(acc, b.ID) > strings.Index(acc, a.ID) {
		t.Error("accessed sort: b should come first")
	}
	db.PinOutput(d5, a.ID, projectKey, true)
	hasT(t, ListStored(d5, projectKey, ListStoredArgs{}), "📌")
}

// ── toolStats ─────────────────────────────────────────────────────────────────

func TestStats(t *testing.T) {
	d := setup(t)
	hasT(t, Stats(d, projectKey, StatsArgs{}), "no data stored")

	in1 := baseInput()
	in1.OriginalSize = 10000
	in1.Summary = strings.Repeat("x", 100)
	store(t, d, in1)
	in2 := baseInput()
	in2.OriginalSize = 5000
	in2.Summary = strings.Repeat("y", 50)
	store(t, d, in2)
	r := Stats(d, projectKey, StatsArgs{})
	hasT(t, r, "Intercepted items: 2")
	hasT(t, r, "reduction")
	hasT(t, r, "Tokens saved")

	db.RecordSession(d, "2026-03-01")
	db.RecordSession(d, "2026-02-28")
	hasT(t, Stats(d, projectKey, StatsArgs{}), "Session days:      2")
}

// The per-command section attributes the aggregate Bash figure, keeping a
// low-reduction family visible (upstream #251).
func TestStatsBashCommandBreakdown(t *testing.T) {
	d := setup(t)
	for _, c := range []struct {
		fp      string
		orig    int
		summary string
	}{{"git diff", 8000, strings.Repeat("d", 80)}, {"cat", 6000, strings.Repeat("c", 5000)}} {
		in := baseInput()
		in.ToolName = "Bash"
		in.OriginalSize = c.orig
		in.Summary = c.summary
		fp := c.fp
		in.CommandFP = &fp
		store(t, d, in)
	}
	r := Stats(d, projectKey, StatsArgs{})
	hasT(t, r, "By Bash command (sorted by original size):")
	if !regexp.MustCompile(`git diff\s+1 item`).MatchString(r) || !regexp.MustCompile(`cat\s+1 item`).MatchString(r) {
		t.Errorf("missing per-command rows:\n%s", r)
	}
	hasT(t, r, "  git diff     1 item      7.8KB → 80B        99%")
}

// recall__note memory is stored memory, not interception: it must not count
// toward the savings figures, and is reported on its own line instead.
func TestStatsExcludesNotesFromSavings(t *testing.T) {
	d := setup(t)
	in := baseInput()
	in.OriginalSize = 10000
	in.Summary = strings.Repeat("x", 100)
	store(t, d, in)

	note := baseInput()
	note.ToolName = "recall__note"
	note.OriginalSize = 900000
	note.Summary = strings.Repeat("n", 900000)
	note.FullContent = strings.Repeat("n", 900000)
	store(t, d, note)

	r := Stats(d, projectKey, StatsArgs{})
	hasT(t, r, "Intercepted items: 1")
	hasT(t, r, "Notes/memory:      1 item")
	hasT(t, r, "99.0% reduction")
}

// A store holding only notes still reports, rather than claiming no data.
func TestStatsNotesOnly(t *testing.T) {
	d := setup(t)
	note := baseInput()
	note.ToolName = "recall__note"
	store(t, d, note)

	r := Stats(d, projectKey, StatsArgs{})
	hasT(t, r, "Intercepted items: 0 (no tool output compressed yet)")
	hasT(t, r, "Notes/memory:      1 item")
}

func TestStatsSuggestions(t *testing.T) {
	d := setup(t)
	store(t, d, baseInput())
	hasNotT(t, Stats(d, projectKey, StatsArgs{PinThreshold: 5, StaleDays: 3}), "Suggestions")

	d2 := setup(t)
	s := store(t, d2, baseInput())
	db.RecordAccess(d2, s.ID)
	r := Stats(d2, projectKey, StatsArgs{PinThreshold: 1})
	hasT(t, r, "Suggestions")
	hasT(t, r, "Consider pinning")
	hasT(t, r, s.ID)
	hasT(t, r, "accessed 1×")

	d3 := setup(t)
	s3 := store(t, d3, baseInput())
	backdate(d3, s3.ID, daysAgoUnix(5))
	r3 := Stats(d3, projectKey, StatsArgs{StaleDays: 3})
	hasT(t, r3, "Never accessed")
	hasT(t, r3, "days ago")

	// pinned not suggested
	d4 := setup(t)
	s4 := store(t, d4, baseInput())
	db.PinOutput(d4, s4.ID, projectKey, true)
	db.RecordAccess(d4, s4.ID)
	hasNotT(t, Stats(d4, projectKey, StatsArgs{PinThreshold: 1}), "Consider pinning")
}

func TestStatsBreakdown(t *testing.T) {
	d := setup(t)
	g := baseInput()
	g.ToolName = "mcp__github__list_issues"
	g.OriginalSize = 8000
	store(t, d, g)
	store(t, d, g)
	p := baseInput()
	p.ToolName = "mcp__playwright__snapshot"
	p.OriginalSize = 20000
	store(t, d, p)
	r := Stats(d, projectKey, StatsArgs{})
	hasT(t, r, "By tool")
	if strings.Index(r, "mcp__playwright__snapshot") > strings.Index(r, "mcp__github__list_issues") {
		t.Error("playwright (larger) should come first")
	}
	if strings.Count(r, "mcp__playwright__snapshot") != 1 {
		t.Error("each tool once")
	}
}

// ── toolPin ───────────────────────────────────────────────────────────────────

func TestPin(t *testing.T) {
	d := setup(t)
	s := store(t, d, baseInput())
	r := Pin(d, projectKey, PinArgs{ID: s.ID})
	hasT(t, r, "pinned")
	hasT(t, r, s.ID)
	got, _ := db.RetrieveOutput(d, s.ID)
	if got.Pinned != 1 {
		t.Error("should be pinned")
	}
	f := false
	hasT(t, Pin(d, projectKey, PinArgs{ID: s.ID, Pinned: &f}), "unpinned")
	hasT(t, Pin(d, projectKey, PinArgs{ID: "recall_00000000"}), "no item found")
}

// A DB failure must surface as an error, not masquerade as an over-budget pin.
func TestPinSurfacesDBError(t *testing.T) {
	d := setup(t)
	s := store(t, d, baseInput())
	d.Close()
	r := Pin(d, projectKey, PinArgs{ID: s.ID})
	hasT(t, r, "[recall: error]")
	hasNotT(t, r, "max_pinned_mb")
}

// ── toolNote ──────────────────────────────────────────────────────────────────

// onlyNote returns the single stored note (each sub-case uses a fresh db, so
// there is exactly one — mirroring the original's per-`it` isolation).
func onlyNote(t *testing.T, d *sql.DB) db.StoredOutput {
	t.Helper()
	items := db.ListStoredSorted(d, projectKey, "recall__note", "", 10, 0)
	if len(items) != 1 {
		t.Fatalf("expected exactly one note, got %d", len(items))
	}
	return items[0]
}

func TestNote(t *testing.T) {
	// id in response + stored under recall__note
	d := setup(t)
	r := Note(d, projectKey, NoteArgs{Text: "some note text"})
	if !noteIDRe.MatchString(r) {
		t.Errorf("no id in note response: %q", r)
	}
	if onlyNote(t, d).ToolName != "recall__note" {
		t.Fatalf("note not stored under recall__note")
	}

	// title in summary
	d2 := setup(t)
	Note(d2, projectKey, NoteArgs{Text: "content here", Title: "My Finding"})
	hasT(t, onlyNote(t, d2).Summary, "My Finding")

	// default (note) title
	d3 := setup(t)
	Note(d3, projectKey, NoteArgs{Text: "untitled note"})
	hasT(t, onlyNote(t, d3).Summary, "(note)")

	// full text preserved as full_content
	d4 := setup(t)
	text := "The full text of this important note"
	Note(d4, projectKey, NoteArgs{Text: text})
	got, _ := db.RetrieveOutput(d4, onlyNote(t, d4).ID)
	if got.FullContent != text {
		t.Errorf("full_content = %q", got.FullContent)
	}

	// truncate summary at 200 chars with ellipsis
	d5 := setup(t)
	Note(d5, projectKey, NoteArgs{Text: strings.Repeat("x", 300)})
	hasT(t, onlyNote(t, d5).Summary, "…")
}

// recall__note never passes through the PostToolUse hook, so Note scans its own
// title and text (upstream #271).
func TestNoteRefusesSecrets(t *testing.T) {
	secret := "sk-" + strings.Repeat("a1b2c3d4e5f6g7h8", 3)
	countNotes := func(d *sql.DB) int {
		var n int
		d.QueryRow("SELECT COUNT(*) FROM stored_outputs WHERE tool_name = 'recall__note'").Scan(&n)
		return n
	}

	d := setup(t)
	r := Note(d, projectKey, NoteArgs{Text: "the key is " + secret})
	hasT(t, r, "NOT stored")
	hasT(t, r, "OpenAI API key")
	hasNotT(t, r, secret)
	if n := countNotes(d); n != 0 {
		t.Errorf("secret note stored: %d rows", n)
	}

	d2 := setup(t)
	hasT(t, Note(d2, projectKey, NoteArgs{Text: "harmless body", Title: secret}), "NOT stored")
	if n := countNotes(d2); n != 0 {
		t.Errorf("secret title stored: %d rows", n)
	}

	d3 := setup(t)
	text := "a perfectly ordinary note about the auth flow"
	if r := Note(d3, projectKey, NoteArgs{Text: text}); !noteIDRe.MatchString(r) {
		t.Errorf("clean note refused: %q", r)
	}
	if got := onlyNote(t, d3).FullContent; got != text {
		t.Errorf("full_content = %q", got)
	}
}

// ── toolExport ────────────────────────────────────────────────────────────────

func TestExport(t *testing.T) {
	d := setup(t)
	hasT(t, Export(d, projectKey), "no items to export")

	in := baseInput()
	in.Summary = "exported summary"
	store(t, d, in)
	r := Export(d, projectKey)
	hasT(t, r, `"id"`)
	hasT(t, r, `"tool_name"`)
	hasT(t, r, `"summary": "exported summary"`)
	hasT(t, r, `"full_content"`)

	// oldest first
	d2 := setup(t)
	now := time.Now().Unix()
	d2.Exec(`INSERT INTO stored_outputs (id,project_key,session_id,tool_name,summary,full_content,original_size,summary_size,created_at) VALUES ('recall_exp00001',?,?,?,?,?,100,3,?)`, projectKey, "s", "mcp__tool", "older", "c", now-10)
	d2.Exec(`INSERT INTO stored_outputs (id,project_key,session_id,tool_name,summary,full_content,original_size,summary_size,created_at) VALUES ('recall_exp00002',?,?,?,?,?,100,3,?)`, projectKey, "s", "mcp__tool", "newer", "c", now)
	ex := Export(d2, projectKey)
	if strings.Index(ex, "older") > strings.Index(ex, "newer") {
		t.Error("export should be oldest-first")
	}
}

// ── toolSessionSummary ────────────────────────────────────────────────────────

func TestSessionSummary(t *testing.T) {
	d := setup(t)
	r := SessionSummary(d, projectKey, SessionSummaryArgs{Date: "2000-01-01"})
	hasT(t, r, "no items stored")
	hasT(t, r, "2000-01-01")

	i1 := baseInput()
	i1.OriginalSize = 4096
	store(t, d, i1)
	i2 := baseInput()
	i2.OriginalSize = 8192
	store(t, d, i2)
	s := SessionSummary(d, projectKey, SessionSummaryArgs{Date: today()})
	hasT(t, s, "2 items")
	hasT(t, s, "→")
	hasT(t, s, "reduction")

	d2 := setup(t)
	p := baseInput()
	p.ToolName = "mcp__playwright__browser_snapshot"
	store(t, d2, p)
	store(t, d2, p)
	g := baseInput()
	g.ToolName = "mcp__github__list_issues"
	store(t, d2, g)
	tb := SessionSummary(d2, projectKey, SessionSummaryArgs{Date: today()})
	hasT(t, tb, "×2")
	if strings.Index(tb, "mcp__playwright__browser_snapshot") > strings.Index(tb, "mcp__github__list_issues") {
		t.Error("playwright (higher count) first")
	}

	d3 := setup(t)
	s3 := store(t, d3, baseInput())
	d3.Exec("UPDATE stored_outputs SET access_count = 3 WHERE id = ?", s3.ID)
	ma := SessionSummary(d3, projectKey, SessionSummaryArgs{Date: today()})
	hasT(t, ma, "Most accessed")
	hasT(t, ma, "×3")

	d4 := setup(t)
	s4 := store(t, d4, baseInput())
	db.PinOutput(d4, s4.ID, projectKey, true)
	pn := SessionSummary(d4, projectKey, SessionSummaryArgs{Date: today()})
	hasT(t, pn, "Pinned: 1")
	hasT(t, pn, "📌")

	d5 := setup(t)
	note := baseInput()
	note.ToolName = "recall__note"
	note.Summary = "(note): Auth findings"
	store(t, d5, note)
	nt := SessionSummary(d5, projectKey, SessionSummaryArgs{Date: today()})
	hasT(t, nt, "Notes: 1")
	hasT(t, nt, "Auth findings")

	d6 := setup(t)
	a := baseInput()
	a.SessionID = "sess-aaa"
	store(t, d6, a)
	b := baseInput()
	b.SessionID = "sess-bbb"
	store(t, d6, b)
	fs := SessionSummary(d6, projectKey, SessionSummaryArgs{SessionID: "sess-aaa"})
	hasT(t, fs, "sess-aaa")
	hasT(t, fs, "1 item")
}

// ── toolContext ───────────────────────────────────────────────────────────────

func TestContext(t *testing.T) {
	d := setup(t)
	empty := Context(d, projectKey, ContextArgs{})
	hasT(t, empty, "no context available")
	hasNotT(t, empty, "Generated ")

	d2 := setup(t)
	s := store(t, d2, baseInput())
	db.PinOutput(d2, s.ID, projectKey, true)
	pc := Context(d2, projectKey, ContextArgs{})
	hasT(t, pc, "Pinned (1)")
	hasT(t, pc, "📌")
	hasT(t, pc, "Generated ")

	d3 := setup(t)
	note := baseInput()
	note.ToolName = "recall__note"
	note.Summary = "(note): Auth findings"
	store(t, d3, note)
	hasT(t, Context(d3, projectKey, ContextArgs{}), "Notes (1)")

	d4 := setup(t)
	s4 := store(t, d4, baseInput())
	db.RecordAccess(d4, s4.ID)
	hasT(t, Context(d4, projectKey, ContextArgs{}), "Recently accessed")

	// excluded outside window
	d5 := setup(t)
	s5 := store(t, d5, baseInput())
	d5.Exec("UPDATE stored_outputs SET access_count = 1, last_accessed = ? WHERE id = ?", daysAgoUnix(8), s5.ID)
	hasNotT(t, Context(d5, projectKey, ContextArgs{Days: 7}), s5.ID)

	// pinned appears in Pinned, not Recently accessed
	d6 := setup(t)
	s6 := store(t, d6, baseInput())
	db.PinOutput(d6, s6.ID, projectKey, true)
	db.RecordAccess(d6, s6.ID)
	pcx := Context(d6, projectKey, ContextArgs{})
	hasT(t, pcx, "Pinned (1)")
	hasNotT(t, pcx, "Recently accessed")
}

func TestContextLastSessionAndHot(t *testing.T) {
	d := setup(t)
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	startOfY := parseDayUnix(yesterday)
	d.Exec(`INSERT INTO stored_outputs (id,project_key,session_id,tool_name,summary,full_content,original_size,summary_size,created_at) VALUES ('recall_ctx_prev1',?,?,?,?,?,4096,64,?)`,
		projectKey, "sess-prev", "mcp__tool", "prev summary", "prev content", startOfY+3600)
	db.RecordSession(d, yesterday)
	r := Context(d, projectKey, ContextArgs{})
	hasT(t, r, "Last session ("+yesterday+")")
	hasT(t, r, "1 item")

	d2 := setup(t)
	old := time.Now().UTC().AddDate(0, 0, -14).Format("2006-01-02")
	startOfOld := parseDayUnix(old)
	d2.Exec(`INSERT INTO stored_outputs (id,project_key,session_id,tool_name,summary,full_content,original_size,summary_size,created_at,access_count) VALUES ('recall_ctx_hot1',?,?,?,?,?,4096,64,?,3)`,
		projectKey, "sess-old", "mcp__github__list_issues", "hot item summary", "full content", startOfOld+3600)
	db.RecordSession(d2, old)
	hr := Context(d2, projectKey, ContextArgs{})
	hasT(t, hr, "Hot from last session ("+old)
	hasT(t, hr, "recall_ctx_hot1")
	hasT(t, hr, "×3")
}

// ── toolSuggest ───────────────────────────────────────────────────────────────

func TestSuggest(t *testing.T) {
	d := setup(t)
	hasT(t, Suggest(d, projectKey, SuggestArgs{}), "no suggestions")
	store(t, d, baseInput())
	hasT(t, Suggest(d, projectKey, SuggestArgs{PinThreshold: 5, StaleDays: 3}), "no suggestions")

	d2 := setup(t)
	s := store(t, d2, baseInput())
	db.RecordAccess(d2, s.ID)
	r := Suggest(d2, projectKey, SuggestArgs{PinThreshold: 1})
	hasT(t, r, "Pin candidates")
	hasT(t, r, "accessed 1×")
	hasT(t, r, `recall__pin id="`+s.ID+`"`)

	d3 := setup(t)
	s3 := store(t, d3, baseInput())
	backdate(d3, s3.ID, daysAgoUnix(5))
	sr := Suggest(d3, projectKey, SuggestArgs{StaleDays: 3})
	hasT(t, sr, "Stale items")
	hasT(t, sr, "days old")
	hasT(t, sr, `recall__forget id="`+s3.ID+`"`)

	d4 := setup(t)
	s4 := store(t, d4, baseInput())
	db.PinOutput(d4, s4.ID, projectKey, true)
	db.RecordAccess(d4, s4.ID)
	hasNotT(t, Suggest(d4, projectKey, SuggestArgs{PinThreshold: 1}), "Pin candidates")

	d5 := setup(t)
	for i := 0; i < 5; i++ {
		si := store(t, d5, baseInput())
		db.RecordAccess(d5, si.ID)
	}
	lr := Suggest(d5, projectKey, SuggestArgs{PinThreshold: 1, Limit: 2})
	if strings.Count(lr, "recall__pin") != 2 {
		t.Errorf("limit: got %d pin lines", strings.Count(lr, "recall__pin"))
	}
}

// helpers

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func parseDayUnix(date string) int64 {
	tm, _ := time.Parse("2006-01-02", date)
	return tm.UTC().Unix()
}

// The per-handler section shows each handler's retrieval rate and counts rows
// stored before handlers were recorded apart (upstream #341).
func TestStatsHandlerBreakdown(t *testing.T) {
	d := setup(t)
	for _, h := range []string{"gitDiffHandler", "shellHandler", ""} {
		in := baseInput()
		in.ToolName = "Bash"
		in.OriginalSize = 8000
		in.Summary = strings.Repeat("d", 80)
		if h != "" {
			name := h
			in.Handler = &name
		}
		store(t, d, in)
	}
	r := Stats(d, projectKey, StatsArgs{})
	hasT(t, r, "By handler (sorted by original size):")
	if !regexp.MustCompile(`gitDiffHandler\s+1 item\s+7\.8KB → 80B\s+99%\s+0% retrieved`).MatchString(r) {
		t.Errorf("missing handler row:\n%s", r)
	}
	hasT(t, r, "(1 older item stored before handlers were recorded are not included)")
}
