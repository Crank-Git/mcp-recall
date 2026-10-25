// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/db/db_port_test.go
//
// Full 1:1 port of tests/db.test.ts (upstream v1.9.0) granular assertions.

package db

import (
	"database/sql"
	"regexp"
	"strings"
	"testing"
	"time"
)

const projKey = "testproject1234"

func newDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func sp(s string) *string { return &s }

func input(over func(*StoreInput)) StoreInput {
	in := StoreInput{
		ProjectKey:   projKey,
		SessionID:    "2026-03-01",
		ToolName:     "mcp__github__list_issues",
		Summary:      "Summary of issues",
		FullContent:  "Full content of the GitHub issues response",
		OriginalSize: 1024,
	}
	if over != nil {
		over(&in)
	}
	return in
}

func mustStore(t *testing.T, database *sql.DB, in StoreInput) StoredOutput {
	t.Helper()
	s, err := StoreOutput(database, in)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	return s
}

func countRows(t *testing.T, database *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := database.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// -------------------------------------------------------------------------
// storeOutput / retrieveOutput
// -------------------------------------------------------------------------

func TestStoreOutputGranular(t *testing.T) {
	database := newDB(t)

	idRe := regexp.MustCompile(`^recall_[0-9a-f]{16}$`)
	r := mustStore(t, database, input(nil))
	if !idRe.MatchString(r.ID) {
		t.Errorf("id format: %q", r.ID)
	}

	r2 := mustStore(t, database, input(func(in *StoreInput) { in.Summary = "hello" }))
	if r2.SummarySize != len([]byte("hello")) {
		t.Errorf("summary_size = %d", r2.SummarySize)
	}

	before := time.Now().Unix()
	r3 := mustStore(t, database, input(nil))
	after := time.Now().Unix()
	if r3.CreatedAt < before || r3.CreatedAt > after {
		t.Errorf("created_at %d not in [%d,%d]", r3.CreatedAt, before, after)
	}

	a := mustStore(t, database, input(nil))
	b := mustStore(t, database, input(nil))
	if a.ID == b.ID {
		t.Error("ids should be unique")
	}
}

func TestRetrieveOutputGranular(t *testing.T) {
	database := newDB(t)
	stored := mustStore(t, database, input(nil))
	got, _ := RetrieveOutput(database, stored.ID)
	if got == nil || got.ID != stored.ID || got.ToolName != stored.ToolName || got.Summary != stored.Summary {
		t.Fatalf("retrieve mismatch: %+v", got)
	}
	if miss, _ := RetrieveOutput(database, "recall_00000000"); miss != nil {
		t.Error("unknown id should be nil")
	}
}

// -------------------------------------------------------------------------
// searchOutputs (FTS)
// -------------------------------------------------------------------------

func TestSearchOutputsGranular(t *testing.T) {
	t.Run("match in summary", func(t *testing.T) {
		database := newDB(t)
		mustStore(t, database, input(func(in *StoreInput) { in.Summary = "critical authentication bug" }))
		mustStore(t, database, input(func(in *StoreInput) { in.Summary = "update dependencies" }))
		res := SearchOutputs(database, "authentication", SearchOptions{ProjectKey: projKey})
		if len(res) != 1 || !strings.Contains(res[0].Summary, "authentication") {
			t.Fatalf("got %d", len(res))
		}
	})
	t.Run("match in full_content", func(t *testing.T) {
		database := newDB(t)
		mustStore(t, database, input(func(in *StoreInput) { in.FullContent = "deep content about oauth tokens" }))
		mustStore(t, database, input(func(in *StoreInput) { in.FullContent = "unrelated content" }))
		if res := SearchOutputs(database, "oauth", SearchOptions{ProjectKey: projKey}); len(res) != 1 {
			t.Fatalf("got %d", len(res))
		}
	})
	t.Run("empty when nothing matches", func(t *testing.T) {
		database := newDB(t)
		mustStore(t, database, input(func(in *StoreInput) { in.Summary = "something else" }))
		if res := SearchOutputs(database, "zzznomatch", SearchOptions{ProjectKey: projKey}); len(res) != 0 {
			t.Fatalf("got %d", len(res))
		}
	})
	t.Run("filter by tool", func(t *testing.T) {
		database := newDB(t)
		mustStore(t, database, input(func(in *StoreInput) { in.ToolName = "mcp__github__list_issues"; in.Summary = "search me" }))
		mustStore(t, database, input(func(in *StoreInput) { in.ToolName = "mcp__playwright__snapshot"; in.Summary = "search me" }))
		res := SearchOutputs(database, "search", SearchOptions{ProjectKey: projKey, Tool: "mcp__github__list_issues"})
		if len(res) != 1 || res[0].ToolName != "mcp__github__list_issues" {
			t.Fatalf("got %d", len(res))
		}
	})
	t.Run("respects limit", func(t *testing.T) {
		database := newDB(t)
		for i := 0; i < 5; i++ {
			mustStore(t, database, input(func(in *StoreInput) { in.Summary = "result item" }))
		}
		if res := SearchOutputs(database, "result", SearchOptions{ProjectKey: projKey, Limit: 3}); len(res) > 3 {
			t.Fatalf("got %d", len(res))
		}
	})
	t.Run("cross-project isolation", func(t *testing.T) {
		database := newDB(t)
		mustStore(t, database, input(func(in *StoreInput) { in.ProjectKey = "otherproject567"; in.Summary = "secret stuff" }))
		if res := SearchOutputs(database, "secret", SearchOptions{ProjectKey: projKey}); len(res) != 0 {
			t.Fatalf("got %d", len(res))
		}
	})
	t.Run("malformed FTS query returns array not throw", func(t *testing.T) {
		database := newDB(t)
		mustStore(t, database, input(nil))
		// Must not panic; a sanitized malformed query yields an empty result set.
		if res := SearchOutputs(database, "NOT *", SearchOptions{ProjectKey: projKey}); len(res) != 0 {
			t.Fatalf("got %d", len(res))
		}
	})
}

// -------------------------------------------------------------------------
// sanitizeFtsQuery
// -------------------------------------------------------------------------

func TestSanitizeFtsQueryGranular(t *testing.T) {
	cases := [][2]string{
		{"hello world", `"hello" "world"`},
		{`say "hi"`, `"say" """hi"""`},
		{"", `""`},
		{"   ", `""`},
		{"NOT something", `"NOT" "something"`},
		{"a OR b", `"a" "OR" "b"`},
	}
	for _, c := range cases {
		if got := SanitizeFtsQuery(c[0]); got != c[1] {
			t.Errorf("SanitizeFtsQuery(%q) = %q, want %q", c[0], got, c[1])
		}
	}
}

// -------------------------------------------------------------------------
// listOutputs
// -------------------------------------------------------------------------

func TestListOutputsGranular(t *testing.T) {
	insertOrd := func(database *sql.DB, id, summary string, createdAt int64) {
		database.Exec(`INSERT INTO stored_outputs (id,project_key,session_id,tool_name,summary,full_content,original_size,summary_size,created_at) VALUES (?,?,?,?,?,?,100,5,?)`,
			id, projKey, "2026-03-01", "mcp__tool", summary, summary+" content", createdAt)
	}

	t.Run("newest-first default", func(t *testing.T) {
		database := newDB(t)
		now := time.Now().Unix()
		insertOrd(database, "recall_ord00001", "first", now-10)
		insertOrd(database, "recall_ord00002", "second", now)
		res := ListOutputs(database, ListOptions{ProjectKey: projKey})
		if res[0].Summary != "second" || res[1].Summary != "first" {
			t.Fatalf("order: %v %v", res[0].Summary, res[1].Summary)
		}
	})
	t.Run("oldest-first when sort=oldest", func(t *testing.T) {
		database := newDB(t)
		now := time.Now().Unix()
		insertOrd(database, "recall_ord00003", "first", now-10)
		insertOrd(database, "recall_ord00004", "second", now)
		res := ListOutputs(database, ListOptions{ProjectKey: projKey, Sort: "oldest"})
		if res[0].Summary != "first" {
			t.Fatalf("oldest first = %v", res[0].Summary)
		}
	})
	t.Run("filter by tool", func(t *testing.T) {
		database := newDB(t)
		mustStore(t, database, input(func(in *StoreInput) { in.ToolName = "mcp__github__list_issues" }))
		mustStore(t, database, input(func(in *StoreInput) { in.ToolName = "mcp__playwright__snapshot" }))
		if res := ListOutputs(database, ListOptions{ProjectKey: projKey, Tool: "mcp__github__list_issues"}); len(res) != 1 {
			t.Fatalf("got %d", len(res))
		}
	})
	t.Run("paginate limit/offset", func(t *testing.T) {
		database := newDB(t)
		for i := 0; i < 5; i++ {
			mustStore(t, database, input(nil))
		}
		p1 := ListOutputs(database, ListOptions{ProjectKey: projKey, Limit: 2, Offset: 0})
		p2 := ListOutputs(database, ListOptions{ProjectKey: projKey, Limit: 2, Offset: 2})
		if len(p1) != 2 || len(p2) != 2 || p1[0].ID == p2[0].ID {
			t.Fatalf("pagination: %d %d", len(p1), len(p2))
		}
	})
	t.Run("cross-project isolation", func(t *testing.T) {
		database := newDB(t)
		mustStore(t, database, input(func(in *StoreInput) { in.ProjectKey = "otherproject567" }))
		if res := ListOutputs(database, ListOptions{ProjectKey: projKey}); len(res) != 0 {
			t.Fatalf("got %d", len(res))
		}
	})
}

// -------------------------------------------------------------------------
// forgetOutputs
// -------------------------------------------------------------------------

func TestForgetOutputsGranular(t *testing.T) {
	t.Run("by id returns change count", func(t *testing.T) {
		database := newDB(t)
		stored := mustStore(t, database, input(nil))
		if n, _ := ForgetOutputs(database, projKey, ForgetOptions{ID: stored.ID}); n != 1 {
			t.Fatalf("got %d", n)
		}
		if got, _ := RetrieveOutput(database, stored.ID); got != nil {
			t.Error("should be deleted")
		}
	})
	t.Run("by tool name", func(t *testing.T) {
		database := newDB(t)
		mustStore(t, database, input(func(in *StoreInput) { in.ToolName = "mcp__github__list_issues" }))
		mustStore(t, database, input(func(in *StoreInput) { in.ToolName = "mcp__github__list_issues" }))
		mustStore(t, database, input(func(in *StoreInput) { in.ToolName = "mcp__playwright__snapshot" }))
		if n, _ := ForgetOutputs(database, projKey, ForgetOptions{Tool: "mcp__github__list_issues"}); n != 2 {
			t.Fatalf("got %d", n)
		}
		if res := ListOutputs(database, ListOptions{ProjectKey: projKey}); len(res) != 1 {
			t.Fatalf("remaining %d", len(res))
		}
	})
	t.Run("by session_id", func(t *testing.T) {
		database := newDB(t)
		mustStore(t, database, input(func(in *StoreInput) { in.SessionID = "2026-03-01" }))
		mustStore(t, database, input(func(in *StoreInput) { in.SessionID = "2026-03-01" }))
		mustStore(t, database, input(func(in *StoreInput) { in.SessionID = "2026-02-28" }))
		if n, _ := ForgetOutputs(database, projKey, ForgetOptions{SessionID: "2026-03-01"}); n != 2 {
			t.Fatalf("got %d", n)
		}
	})
	t.Run("all=true", func(t *testing.T) {
		database := newDB(t)
		mustStore(t, database, input(nil))
		mustStore(t, database, input(nil))
		if n, _ := ForgetOutputs(database, projKey, ForgetOptions{All: true}); n != 2 {
			t.Fatalf("got %d", n)
		}
		if res := ListOutputs(database, ListOptions{ProjectKey: projKey}); len(res) != 0 {
			t.Fatalf("remaining %d", len(res))
		}
	})
	t.Run("cross-project isolation", func(t *testing.T) {
		database := newDB(t)
		stored := mustStore(t, database, input(func(in *StoreInput) { in.ProjectKey = "otherproject567" }))
		ForgetOutputs(database, projKey, ForgetOptions{All: true})
		if got, _ := RetrieveOutput(database, stored.ID); got == nil {
			t.Error("other project should survive")
		}
	})
	t.Run("0 when nothing matches", func(t *testing.T) {
		database := newDB(t)
		if n, _ := ForgetOutputs(database, projKey, ForgetOptions{}); n != 0 {
			t.Fatalf("got %d", n)
		}
	})
	t.Run("cleans up FTS index on delete", func(t *testing.T) {
		database := newDB(t)
		stored := mustStore(t, database, input(func(in *StoreInput) { in.Summary = "findme unique term" }))
		ForgetOutputs(database, projKey, ForgetOptions{ID: stored.ID})
		if res := SearchOutputs(database, "findme", SearchOptions{ProjectKey: projKey}); len(res) != 0 {
			t.Fatalf("stale search results: %d", len(res))
		}
	})
	t.Run("bulk delete of 50+ returns correct count", func(t *testing.T) {
		database := newDB(t)
		for i := 0; i < 55; i++ {
			mustStore(t, database, input(func(in *StoreInput) { in.Summary = "bulk item" }))
		}
		if n, err := ForgetOutputs(database, projKey, ForgetOptions{All: true}); err != nil || n != 55 {
			t.Fatalf("bulk delete n=%d err=%v", n, err)
		}
	})
	t.Run("integrity preserved after bulk delete w/ incremental_vacuum", func(t *testing.T) {
		database := newDB(t)
		survivor := mustStore(t, database, input(func(in *StoreInput) { in.ProjectKey = "other-project"; in.Summary = "keep me" }))
		for i := 0; i < 55; i++ {
			mustStore(t, database, input(func(in *StoreInput) { in.Summary = "vacuum-test item" }))
		}
		ForgetOutputs(database, projKey, ForgetOptions{All: true})
		got, _ := RetrieveOutput(database, survivor.ID)
		if got == nil || got.Summary != "keep me" {
			t.Fatalf("survivor lost: %+v", got)
		}
	})
}

// -------------------------------------------------------------------------
// getStats
// -------------------------------------------------------------------------

func TestGetStatsGranular(t *testing.T) {
	t.Run("zeros for empty project", func(t *testing.T) {
		database := newDB(t)
		s := GetStats(database, projKey)
		if s.TotalItems != 0 || s.TotalOriginalBytes != 0 || s.CompressionRatio != 0 {
			t.Fatalf("%+v", s)
		}
	})
	t.Run("accumulates totals", func(t *testing.T) {
		database := newDB(t)
		mustStore(t, database, input(func(in *StoreInput) { in.OriginalSize = 1000; in.Summary = strings.Repeat("x", 50) }))
		mustStore(t, database, input(func(in *StoreInput) { in.OriginalSize = 2000; in.Summary = strings.Repeat("y", 100) }))
		s := GetStats(database, projKey)
		if s.TotalItems != 2 || s.TotalOriginalBytes != 3000 || s.CompressionRatio >= 1 {
			t.Fatalf("%+v", s)
		}
	})
	t.Run("isolates other projects", func(t *testing.T) {
		database := newDB(t)
		mustStore(t, database, input(func(in *StoreInput) { in.ProjectKey = "otherproject567"; in.OriginalSize = 9999 }))
		if s := GetStats(database, projKey); s.TotalItems != 0 {
			t.Fatalf("%+v", s)
		}
	})
}

// -------------------------------------------------------------------------
// pruneExpired
// -------------------------------------------------------------------------

func TestPruneExpiredGranular(t *testing.T) {
	t.Run("removes items older than N days", func(t *testing.T) {
		database := newDB(t)
		oldTs := time.Now().Unix() - 10*86400
		database.Exec(`INSERT INTO stored_outputs (id,project_key,session_id,tool_name,summary,full_content,original_size,summary_size,created_at) VALUES ('recall_old00001',?,?,?,?,?,100,3,?)`,
			projKey, "2026-02-19", "mcp__tool", "old", "old content", oldTs)
		mustStore(t, database, input(func(in *StoreInput) { in.Summary = "recent" }))
		if n, _ := PruneExpired(database, projKey, 7); n != 1 {
			t.Fatalf("got %d", n)
		}
		if res := ListOutputs(database, ListOptions{ProjectKey: projKey}); len(res) != 1 {
			t.Fatalf("remaining %d", len(res))
		}
	})
	t.Run("0 when nothing expired", func(t *testing.T) {
		database := newDB(t)
		mustStore(t, database, input(nil))
		if n, _ := PruneExpired(database, projKey, 7); n != 0 {
			t.Fatalf("got %d", n)
		}
	})
}

// -------------------------------------------------------------------------
// recordSession / getSessionDays
// -------------------------------------------------------------------------

func TestSessionsGranular(t *testing.T) {
	database := newDB(t)
	RecordSession(database, "2026-03-01")
	days := GetSessionDays(database)
	if len(days) != 1 || days[0] != "2026-03-01" {
		t.Fatalf("record: %v", days)
	}
	RecordSession(database, "2026-03-01") // idempotent
	if len(GetSessionDays(database)) != 1 {
		t.Fatal("should be idempotent")
	}
	RecordSession(database, "2026-02-28")
	days = GetSessionDays(database)
	if days[0] != "2026-03-01" || days[1] != "2026-02-28" {
		t.Fatalf("descending order: %v", days)
	}
}

// -------------------------------------------------------------------------
// recordAccess
// -------------------------------------------------------------------------

func TestRecordAccessGranular(t *testing.T) {
	t.Run("increments and accumulates", func(t *testing.T) {
		database := newDB(t)
		stored := mustStore(t, database, input(nil))
		if stored.AccessCount != 0 {
			t.Fatal("initial 0")
		}
		RecordAccess(database, stored.ID)
		if got, _ := RetrieveOutput(database, stored.ID); got.AccessCount != 1 {
			t.Fatalf("after 1: %d", got.AccessCount)
		}
		RecordAccess(database, stored.ID)
		RecordAccess(database, stored.ID)
		if got, _ := RetrieveOutput(database, stored.ID); got.AccessCount != 3 {
			t.Fatalf("after 3: %d", got.AccessCount)
		}
	})
	t.Run("sets last_accessed recent", func(t *testing.T) {
		database := newDB(t)
		before := time.Now().Unix()
		stored := mustStore(t, database, input(nil))
		RecordAccess(database, stored.ID)
		after := time.Now().Unix()
		got, _ := RetrieveOutput(database, stored.ID)
		if got.LastAccessed == nil || *got.LastAccessed < before || *got.LastAccessed > after {
			t.Fatalf("last_accessed: %v", got.LastAccessed)
		}
	})
}

// -------------------------------------------------------------------------
// pinOutput
// -------------------------------------------------------------------------

func TestPinOutputGranular(t *testing.T) {
	t.Run("pin and unpin", func(t *testing.T) {
		database := newDB(t)
		stored := mustStore(t, database, input(nil))
		if stored.Pinned != 0 {
			t.Fatal("initial unpinned")
		}
		PinOutput(database, stored.ID, projKey, true)
		if got, _ := RetrieveOutput(database, stored.ID); got.Pinned != 1 {
			t.Fatal("should be pinned")
		}
		PinOutput(database, stored.ID, projKey, false)
		if got, _ := RetrieveOutput(database, stored.ID); got.Pinned != 0 {
			t.Fatal("should be unpinned")
		}
	})
	t.Run("returns true/false", func(t *testing.T) {
		database := newDB(t)
		stored := mustStore(t, database, input(nil))
		if ok, _ := PinOutput(database, stored.ID, projKey, true); !ok {
			t.Error("existing → true")
		}
		if ok, _ := PinOutput(database, "recall_00000000", projKey, true); ok {
			t.Error("unknown → false")
		}
	})
	t.Run("pruneExpired skips pinned", func(t *testing.T) {
		database := newDB(t)
		oldTs := time.Now().Unix() - 10*86400
		database.Exec(`INSERT INTO stored_outputs (id,project_key,session_id,tool_name,summary,full_content,original_size,summary_size,created_at,pinned) VALUES ('recall_pin00001',?,?,?,?,?,100,3,?,1)`,
			projKey, "2026-02-19", "mcp__tool", "pinned old", "content", oldTs)
		if n, _ := PruneExpired(database, projKey, 7); n != 0 {
			t.Fatalf("got %d", n)
		}
	})
	t.Run("forget all skips pinned by default", func(t *testing.T) {
		database := newDB(t)
		stored := mustStore(t, database, input(nil))
		PinOutput(database, stored.ID, projKey, true)
		mustStore(t, database, input(nil))
		if n, _ := ForgetOutputs(database, projKey, ForgetOptions{All: true}); n != 1 {
			t.Fatalf("got %d", n)
		}
		if got, _ := RetrieveOutput(database, stored.ID); got == nil {
			t.Error("pinned should survive")
		}
	})
	t.Run("forget all force deletes pinned", func(t *testing.T) {
		database := newDB(t)
		stored := mustStore(t, database, input(nil))
		PinOutput(database, stored.ID, projKey, true)
		ForgetOutputs(database, projKey, ForgetOptions{All: true, Force: true})
		if got, _ := RetrieveOutput(database, stored.ID); got != nil {
			t.Error("force should delete pinned")
		}
	})
}

// -------------------------------------------------------------------------
// schema migrations
// -------------------------------------------------------------------------

func TestSchemaMigrationsIdempotent(t *testing.T) {
	database := newDB(t)
	// Second init: duplicate-column ALTERs swallowed, indexes no-op — no error.
	if err := InitSchema(database); err != nil {
		t.Fatalf("re-init: %v", err)
	}
	rows, err := database.Query("PRAGMA table_info(stored_outputs)")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt sql.NullString
		rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk)
		if name == "output_hash" {
			found = true
		}
	}
	if !found {
		t.Error("output_hash column missing")
	}
}

// -------------------------------------------------------------------------
// content-hash dedup
// -------------------------------------------------------------------------

func TestContentHashDedup(t *testing.T) {
	t.Run("records output_hash", func(t *testing.T) {
		database := newDB(t)
		stored := mustStore(t, database, input(func(in *StoreInput) { in.FullContent = "abc123 content body" }))
		if stored.OutputHash == nil || *stored.OutputHash != HashContent("abc123 content body") {
			t.Fatalf("output_hash: %v", stored.OutputHash)
		}
	})
	t.Run("finds identical content from the same tool and command family", func(t *testing.T) {
		database := newDB(t)
		fp := "git diff"
		a := mustStore(t, database, input(func(in *StoreInput) { in.ToolName = "Bash"; in.CommandFP = &fp; in.FullContent = "same body text" }))
		mustStore(t, database, input(func(in *StoreInput) { in.ToolName = "Bash"; in.CommandFP = &fp; in.FullContent = "unrelated body" }))
		found, _ := CheckOutputDedup(database, projKey, HashContent("same body text"), "Bash", &fp)
		if found == nil || found.ID != a.ID {
			t.Fatalf("found: %+v", found)
		}
	})
	t.Run("returns the most recent match when several exist", func(t *testing.T) {
		database := newDB(t)
		now := time.Now().Unix()
		for _, r := range []struct {
			id, summary string
			at          int64
		}{{"recall_dedup0001", "old", now - 10}, {"recall_dedup0002", "new", now}} {
			if _, err := database.Exec(`INSERT INTO stored_outputs (id,project_key,session_id,tool_name,summary,full_content,original_size,summary_size,created_at,output_hash)
				VALUES (?,?,?,?,?,?,100,3,?,?)`, r.id, projKey, "2026-03-01", "mcp__tool", r.summary, "content", r.at, HashContent("content")); err != nil {
				t.Fatal(err)
			}
		}
		found, _ := CheckOutputDedup(database, projKey, HashContent("content"), "mcp__tool", nil)
		if found == nil || found.Summary != "new" {
			t.Fatalf("found: %+v", found)
		}
	})
	t.Run("no match from another tool or command family (#324)", func(t *testing.T) {
		database := newDB(t)
		fp := "git diff"
		mustStore(t, database, input(func(in *StoreInput) { in.ToolName = "mcp__x__alpha"; in.FullContent = "same body text" }))
		mustStore(t, database, input(func(in *StoreInput) { in.ToolName = "Bash"; in.CommandFP = &fp; in.FullContent = "shared stderr" }))
		if found, _ := CheckOutputDedup(database, projKey, HashContent("same body text"), "mcp__y__beta", nil); found != nil {
			t.Error("matched another tool")
		}
		other := "git log"
		if found, _ := CheckOutputDedup(database, projKey, HashContent("shared stderr"), "Bash", &other); found != nil {
			t.Error("matched another command family")
		}
	})
	t.Run("null when no content matches", func(t *testing.T) {
		database := newDB(t)
		mustStore(t, database, input(func(in *StoreInput) { in.FullContent = "one thing" }))
		if found, _ := CheckOutputDedup(database, projKey, HashContent("another thing"), "mcp__github__list_issues", nil); found != nil {
			t.Error("should be nil")
		}
	})
	t.Run("project scoped", func(t *testing.T) {
		database := newDB(t)
		mustStore(t, database, input(func(in *StoreInput) { in.ProjectKey = "otherproject567"; in.FullContent = "shared body" }))
		if found, _ := CheckOutputDedup(database, projKey, HashContent("shared body"), "mcp__github__list_issues", nil); found != nil {
			t.Error("should not cross projects")
		}
	})
}

// -------------------------------------------------------------------------
// evictIfNeeded
// -------------------------------------------------------------------------

func TestEvictIfNeededGranular(t *testing.T) {
	t.Run("0 when under limit", func(t *testing.T) {
		database := newDB(t)
		mustStore(t, database, input(func(in *StoreInput) { in.OriginalSize = 100 }))
		if n, _ := EvictIfNeeded(database, projKey, 500, 7, time.Now().Unix()); n != 0 {
			t.Fatalf("got %d", n)
		}
	})
	t.Run("evicts least-accessed when over limit", func(t *testing.T) {
		database := newDB(t)
		mustStore(t, database, input(func(in *StoreInput) { in.OriginalSize = 300; in.Summary = "item a" }))
		b := mustStore(t, database, input(func(in *StoreInput) { in.OriginalSize = 300; in.Summary = "item b" }))
		RecordAccess(database, b.ID)
		RecordAccess(database, b.ID)
		n, _ := EvictIfNeeded(database, projKey, 0.0005, 7, time.Now().Unix())
		if n <= 0 {
			t.Fatalf("expected eviction, got %d", n)
		}
		if got, _ := RetrieveOutput(database, b.ID); got == nil {
			t.Error("more-accessed b should survive")
		}
	})
	t.Run("does not evict pinned", func(t *testing.T) {
		database := newDB(t)
		stored := mustStore(t, database, input(func(in *StoreInput) { in.OriginalSize = 1000000 }))
		PinOutput(database, stored.ID, projKey, true)
		EvictIfNeeded(database, projKey, 0, 7, time.Now().Unix())
		if got, _ := RetrieveOutput(database, stored.ID); got == nil {
			t.Error("pinned should survive")
		}
	})
	t.Run("0 when all pinned", func(t *testing.T) {
		database := newDB(t)
		a := mustStore(t, database, input(func(in *StoreInput) { in.OriginalSize = 500000 }))
		b := mustStore(t, database, input(func(in *StoreInput) { in.OriginalSize = 500000 }))
		PinOutput(database, a.ID, projKey, true)
		PinOutput(database, b.ID, projKey, true)
		if n, _ := EvictIfNeeded(database, projKey, 0, 7, time.Now().Unix()); n != 0 {
			t.Fatalf("got %d", n)
		}
	})
	t.Run("decay keeps fresh over old heavily-accessed", func(t *testing.T) {
		database := newDB(t)
		now := int64(1_000_000_000)
		day := int64(86400)
		old := mustStore(t, database, input(func(in *StoreInput) { in.OriginalSize = 300; in.Summary = "old heavy" }))
		fresh := mustStore(t, database, input(func(in *StoreInput) { in.OriginalSize = 300; in.Summary = "fresh light" }))
		database.Exec("UPDATE stored_outputs SET access_count = 50, last_accessed = ? WHERE id = ?", now-60*day, old.ID)
		database.Exec("UPDATE stored_outputs SET access_count = 2, last_accessed = ? WHERE id = ?", now-1, fresh.ID)
		n, _ := EvictIfNeeded(database, projKey, 0.0005, 7, now)
		if n <= 0 {
			t.Fatalf("expected eviction, got %d", n)
		}
		if got, _ := RetrieveOutput(database, fresh.ID); got == nil {
			t.Error("fresh should survive")
		}
		if got, _ := RetrieveOutput(database, old.ID); got != nil {
			t.Error("stale old should be evicted")
		}
	})
	t.Run("uses creation time when never accessed", func(t *testing.T) {
		database := newDB(t)
		now := int64(1_000_000_000)
		day := int64(86400)
		older := mustStore(t, database, input(func(in *StoreInput) { in.OriginalSize = 300; in.Summary = "older" }))
		newer := mustStore(t, database, input(func(in *StoreInput) { in.OriginalSize = 300; in.Summary = "newer" }))
		database.Exec("UPDATE stored_outputs SET created_at = ?, last_accessed = NULL WHERE id = ?", now-90*day, older.ID)
		database.Exec("UPDATE stored_outputs SET created_at = ?, last_accessed = NULL WHERE id = ?", now-1, newer.ID)
		EvictIfNeeded(database, projKey, 0.0005, 7, now)
		if got, _ := RetrieveOutput(database, newer.ID); got == nil {
			t.Error("newer should survive")
		}
		if got, _ := RetrieveOutput(database, older.ID); got != nil {
			t.Error("older should be evicted")
		}
	})
	t.Run("halves weight at one half-life", func(t *testing.T) {
		database := newDB(t)
		now := int64(1_000_000_000)
		day := int64(86400)
		halfLife := 4
		fresh := mustStore(t, database, input(func(in *StoreInput) { in.OriginalSize = 300; in.Summary = "fresh" }))
		aged := mustStore(t, database, input(func(in *StoreInput) { in.OriginalSize = 300; in.Summary = "aged" }))
		database.Exec("UPDATE stored_outputs SET access_count = 1, last_accessed = ? WHERE id = ?", now, fresh.ID)
		database.Exec("UPDATE stored_outputs SET access_count = 1, last_accessed = ? WHERE id = ?", now-int64(halfLife)*day, aged.ID)
		EvictIfNeeded(database, projKey, 0.0005, halfLife, now)
		if got, _ := RetrieveOutput(database, fresh.ID); got == nil {
			t.Error("fresh should survive")
		}
		if got, _ := RetrieveOutput(database, aged.ID); got != nil {
			t.Error("aged should be evicted")
		}
	})
	t.Run("breaks ties deterministically by id", func(t *testing.T) {
		database := newDB(t)
		now := int64(1_000_000_000)
		x := mustStore(t, database, input(func(in *StoreInput) { in.OriginalSize = 300; in.Summary = "x" }))
		y := mustStore(t, database, input(func(in *StoreInput) { in.OriginalSize = 300; in.Summary = "y" }))
		for _, id := range []string{x.ID, y.ID} {
			database.Exec("UPDATE stored_outputs SET access_count = 1, last_accessed = ?, created_at = ? WHERE id = ?", now-10, now-100, id)
		}
		smaller, larger := x.ID, y.ID
		if larger < smaller {
			smaller, larger = larger, smaller
		}
		EvictIfNeeded(database, projKey, 0.0005, 7, now)
		if got, _ := RetrieveOutput(database, smaller); got != nil {
			t.Error("smaller id should be evicted")
		}
		if got, _ := RetrieveOutput(database, larger); got == nil {
			t.Error("larger id should survive")
		}
	})
	t.Run("no crash/NaN when half_life non-positive", func(t *testing.T) {
		database := newDB(t)
		now := int64(1_000_000_000)
		a := mustStore(t, database, input(func(in *StoreInput) { in.OriginalSize = 300; in.Summary = "a" }))
		b := mustStore(t, database, input(func(in *StoreInput) { in.OriginalSize = 300; in.Summary = "b" }))
		database.Exec("UPDATE stored_outputs SET access_count = 0, last_accessed = ? WHERE id = ?", now-100, a.ID)
		database.Exec("UPDATE stored_outputs SET access_count = 5, last_accessed = ? WHERE id = ?", now-100, b.ID)
		n, err := EvictIfNeeded(database, projKey, 0.0005, 0, now)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if n <= 0 {
			t.Fatalf("expected eviction, got %d", n)
		}
	})
}

// -------------------------------------------------------------------------
// retrieveSnippet
// -------------------------------------------------------------------------

func TestRetrieveSnippetGranular(t *testing.T) {
	t.Run("null for unknown id", func(t *testing.T) {
		database := newDB(t)
		if s := RetrieveSnippet(database, "recall_00000000", "query"); s != "" {
			t.Errorf("got %q", s)
		}
	})
	t.Run("excerpt when query matches", func(t *testing.T) {
		database := newDB(t)
		stored := mustStore(t, database, input(func(in *StoreInput) {
			in.FullContent = "The quick brown fox jumps over the lazy authentication dog"
		}))
		s := RetrieveSnippet(database, stored.ID, "authentication")
		if !strings.Contains(s, "authentication") {
			t.Errorf("got %q", s)
		}
	})
	t.Run("null when no match", func(t *testing.T) {
		database := newDB(t)
		stored := mustStore(t, database, input(func(in *StoreInput) { in.FullContent = "hello world" }))
		if s := RetrieveSnippet(database, stored.ID, "zzznomatch"); s != "" {
			t.Errorf("got %q", s)
		}
	})
}

// -------------------------------------------------------------------------
// chunkText
// -------------------------------------------------------------------------

func TestChunkTextGranular(t *testing.T) {
	if got := ChunkText(""); len(got) != 0 {
		t.Errorf("empty → %d chunks", len(got))
	}
	if got := ChunkText("short text"); len(got) != 1 || got[0] != "short text" {
		t.Errorf("short → %v", got)
	}
	if got := ChunkText(strings.Repeat("x", ChunkSize)); len(got) != 1 {
		t.Errorf("exactly ChunkSize → %d", len(got))
	}
	if got := ChunkText(strings.Repeat("x", ChunkSize*2)); len(got) <= 1 {
		t.Errorf("2x → %d", len(got))
	}
	for _, c := range ChunkText(strings.Repeat("a", ChunkSize*3+100)) {
		if len([]rune(c)) > ChunkSize {
			t.Errorf("chunk too long: %d", len([]rune(c)))
		}
	}
	// consecutive chunks overlap by ChunkOverlap
	chunks := ChunkText(strings.Repeat("abcdefghij", 60))
	if len(chunks) <= 1 {
		t.Fatalf("expected multiple chunks")
	}
	step := ChunkSize - ChunkOverlap
	r0, r1 := []rune(chunks[0]), []rune(chunks[1])
	if string(r1[:ChunkOverlap]) != string(r0[step:step+ChunkOverlap]) {
		t.Error("chunks should overlap by ChunkOverlap")
	}
	// last chunk contains the end of the text
	text := strings.Repeat("x", ChunkSize+100)
	lc := ChunkText(text)
	last := lc[len(lc)-1]
	if !strings.HasSuffix(text, last) {
		t.Error("last chunk should contain end of text")
	}
}

// -------------------------------------------------------------------------
// content_chunks storage / deletion
// -------------------------------------------------------------------------

func TestContentChunksStorage(t *testing.T) {
	t.Run("stores multiple chunks for long content", func(t *testing.T) {
		database := newDB(t)
		mustStore(t, database, input(func(in *StoreInput) { in.FullContent = strings.Repeat("word ", 200) }))
		if n := countRows(t, database, "SELECT COUNT(*) FROM content_chunks"); n <= 1 {
			t.Errorf("got %d chunks", n)
		}
	})
	t.Run("single chunk for short content", func(t *testing.T) {
		database := newDB(t)
		stored := mustStore(t, database, input(func(in *StoreInput) { in.FullContent = "short content" }))
		if n := countRows(t, database, "SELECT COUNT(*) FROM content_chunks WHERE output_id = ?", stored.ID); n != 1 {
			t.Errorf("got %d", n)
		}
	})
	t.Run("chunk count matches ChunkText", func(t *testing.T) {
		database := newDB(t)
		content := strings.Repeat("z", ChunkSize*2+50)
		stored := mustStore(t, database, input(func(in *StoreInput) { in.FullContent = content }))
		expected := len(ChunkText(content))
		if n := countRows(t, database, "SELECT COUNT(*) FROM content_chunks WHERE output_id = ?", stored.ID); n != expected {
			t.Errorf("got %d, want %d", n, expected)
		}
	})
	t.Run("deletes chunks when item deleted", func(t *testing.T) {
		database := newDB(t)
		stored := mustStore(t, database, input(func(in *StoreInput) { in.FullContent = "some content to chunk" }))
		database.Exec("DELETE FROM stored_outputs WHERE id = ?", stored.ID)
		if n := countRows(t, database, "SELECT COUNT(*) FROM content_chunks WHERE output_id = ?", stored.ID); n != 0 {
			t.Errorf("chunks not cascaded: %d", n)
		}
	})
}

// -------------------------------------------------------------------------
// retrieveSnippet — chunk-based retrieval
// -------------------------------------------------------------------------

func TestRetrieveSnippetChunked(t *testing.T) {
	t.Run("matching chunk when query matches", func(t *testing.T) {
		database := newDB(t)
		stored := mustStore(t, database, input(func(in *StoreInput) {
			in.FullContent = "The deployment pipeline uses kubernetes and helm charts for orchestration"
		}))
		if s := RetrieveSnippet(database, stored.ID, "kubernetes"); !strings.Contains(s, "kubernetes") {
			t.Errorf("got %q", s)
		}
	})
	t.Run("returns full chunk not short excerpt", func(t *testing.T) {
		database := newDB(t)
		content := strings.Repeat("alpha ", 50) + "targetword " + strings.Repeat("beta ", 50)
		stored := mustStore(t, database, input(func(in *StoreInput) { in.FullContent = content }))
		s := RetrieveSnippet(database, stored.ID, "targetword")
		if len(s) <= 100 {
			t.Errorf("expected full chunk, got len %d", len(s))
		}
	})
	t.Run("chunk containing match in multi-chunk doc", func(t *testing.T) {
		database := newDB(t)
		content := strings.Repeat("x ", 300) + "uniquekeyword " + strings.Repeat("y ", 300)
		stored := mustStore(t, database, input(func(in *StoreInput) { in.FullContent = content }))
		if s := RetrieveSnippet(database, stored.ID, "uniquekeyword"); !strings.Contains(s, "uniquekeyword") {
			t.Errorf("got %q", s)
		}
	})
	t.Run("falls back to legacy FTS snippet without chunks", func(t *testing.T) {
		database := newDB(t)
		now := time.Now().Unix()
		database.Exec(`INSERT INTO stored_outputs (id,project_key,session_id,tool_name,summary,full_content,original_size,summary_size,created_at) VALUES ('recall_legacy01',?,?,?,?,?,100,7,?)`,
			projKey, "sess", "mcp__tool", "summary", "legacy content with matchword", now)
		if s := RetrieveSnippet(database, "recall_legacy01", "matchword"); !strings.Contains(s, "matchword") {
			t.Errorf("got %q", s)
		}
	})
	t.Run("null when no chunk and no legacy match", func(t *testing.T) {
		database := newDB(t)
		stored := mustStore(t, database, input(func(in *StoreInput) { in.FullContent = "completely different content" }))
		if s := RetrieveSnippet(database, stored.ID, "zzznomatch"); s != "" {
			t.Errorf("got %q", s)
		}
	})
}

// -------------------------------------------------------------------------
// getContext hot section
// -------------------------------------------------------------------------

func TestGetContextHot(t *testing.T) {
	insertRow := func(database *sql.DB, id string, createdAt int64, accessCount int, lastAccessed *int64, toolName string, pinned int) {
		if toolName == "" {
			toolName = "mcp__github__list_issues"
		}
		database.Exec(`INSERT INTO stored_outputs (id,project_key,session_id,tool_name,summary,full_content,original_size,summary_size,created_at,access_count,last_accessed,pinned) VALUES (?,?,'sess',?,'a summary','full content',1024,64,?,?,?,?)`,
			id, projKey, toolName, createdAt, accessCount, nullInt(lastAccessed), pinned)
	}
	oldDate := func(daysAgo int) (string, int64) {
		d := time.Now().UTC().Add(-time.Duration(daysAgo) * 24 * time.Hour)
		date := d.Format("2006-01-02")
		return date, startOfDayUnix(date)
	}

	t.Run("empty when no last session", func(t *testing.T) {
		database := newDB(t)
		if got := GetContext(database, projKey, 0, 0).Hot; len(got) != 0 {
			t.Errorf("got %d", len(got))
		}
	})
	t.Run("empty when last-session items have access_count 0", func(t *testing.T) {
		database := newDB(t)
		date, start := oldDate(14)
		insertRow(database, "recall_hot_t1", start+3600, 0, nil, "", 0)
		RecordSession(database, date)
		if got := GetContext(database, projKey, 0, 0).Hot; len(got) != 0 {
			t.Errorf("got %d", len(got))
		}
	})
	t.Run("ordered by access_count desc", func(t *testing.T) {
		database := newDB(t)
		date, start := oldDate(14)
		insertRow(database, "recall_hot_t2a", start+3600, 5, nil, "", 0)
		insertRow(database, "recall_hot_t2b", start+3601, 1, nil, "", 0)
		insertRow(database, "recall_hot_t2c", start+3602, 3, nil, "", 0)
		RecordSession(database, date)
		hot := GetContext(database, projKey, 0, 0).Hot
		if len(hot) != 3 || hot[0].ID != "recall_hot_t2a" || hot[1].ID != "recall_hot_t2c" || hot[2].ID != "recall_hot_t2b" {
			t.Fatalf("order: %+v", hot)
		}
	})
	t.Run("excludes items already in recent", func(t *testing.T) {
		database := newDB(t)
		date, start := oldDate(14)
		recentAccess := time.Now().Unix() - 3600
		insertRow(database, "recall_hot_t3", start+3600, 3, &recentAccess, "", 0)
		RecordSession(database, date)
		data := GetContext(database, projKey, 0, 0)
		inRecent := false
		for _, i := range data.Recent {
			if i.ID == "recall_hot_t3" {
				inRecent = true
			}
		}
		inHot := false
		for _, i := range data.Hot {
			if i.ID == "recall_hot_t3" {
				inHot = true
			}
		}
		if !inRecent || inHot {
			t.Errorf("recent=%v hot=%v", inRecent, inHot)
		}
	})
	t.Run("excludes notes", func(t *testing.T) {
		database := newDB(t)
		date, start := oldDate(14)
		insertRow(database, "recall_hot_t4", start+3600, 2, nil, "recall__note", 0)
		RecordSession(database, date)
		if got := GetContext(database, projKey, 0, 0).Hot; len(got) != 0 {
			t.Errorf("got %d", len(got))
		}
	})
	t.Run("excludes pinned", func(t *testing.T) {
		database := newDB(t)
		date, start := oldDate(14)
		insertRow(database, "recall_hot_t5", start+3600, 2, nil, "mcp__tool", 1)
		RecordSession(database, date)
		if got := GetContext(database, projKey, 0, 0).Hot; len(got) != 0 {
			t.Errorf("got %d", len(got))
		}
	})
}
