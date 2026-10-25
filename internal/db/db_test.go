// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/db/db_test.go

package db

import (
	"testing"
)

func ptr(s string) *string { return &s }

func mkInput(pk, tool, full string) StoreInput {
	return StoreInput{
		ProjectKey:   pk,
		SessionID:    "sess1",
		ToolName:     tool,
		Summary:      "[summary of " + tool + "]",
		FullContent:  full,
		OriginalSize: len(full),
	}
}

func TestStoreRetrieveSearchSnippet(t *testing.T) {
	database, _ := Open(":memory:")
	defer database.Close()

	in := mkInput("proj", "mcp__x__list", "the proxmox hypervisor runs plex and nextcloud")
	stored, err := StoreOutput(database, in)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if stored.ID == "" || stored.SummarySize != len(in.Summary) {
		t.Fatalf("bad stored: %+v", stored)
	}

	got, err := RetrieveOutput(database, stored.ID)
	if err != nil || got == nil {
		t.Fatalf("retrieve: %v %v", err, got)
	}
	if got.FullContent != in.FullContent {
		t.Fatalf("full content mismatch")
	}

	// second item so search must discriminate
	_, _ = StoreOutput(database, mkInput("proj", "mcp__x__get", "nginx reverse proxy configuration"))

	res := SearchOutputs(database, "proxmox", SearchOptions{ProjectKey: "proj"})
	if len(res) != 1 || res[0].ID != stored.ID {
		t.Fatalf("search proxmox: got %d results", len(res))
	}

	res2 := SearchOutputs(database, "nginx", SearchOptions{ProjectKey: "proj"})
	if len(res2) != 1 {
		t.Fatalf("search nginx: got %d", len(res2))
	}

	snip := RetrieveSnippet(database, stored.ID, "proxmox")
	if snip == "" {
		t.Fatalf("snippet empty")
	}
}

func TestEvictLFU(t *testing.T) {
	database, _ := Open(":memory:")
	defer database.Close()

	const now = int64(2_000_000_000)
	oneMB := 1024 * 1024
	var ids []string
	for i := 0; i < 3; i++ {
		in := mkInput("proj", "mcp__x__big", "x")
		in.OriginalSize = oneMB
		s, _ := StoreOutput(database, in)
		ids = append(ids, s.ID)
		// Backdate to distinct ages: ids[0] oldest (3d), ids[2] newest (1d).
		database.Exec("UPDATE stored_outputs SET created_at = ? WHERE id = ?", now-int64(3-i)*86400, s.ID)
	}
	// Never-accessed → value decays by age; oldest (lowest recency) evicted first.
	n, err := EvictIfNeeded(database, "proj", 2, 7, now)
	if err != nil {
		t.Fatalf("evict: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 evicted, got %d", n)
	}
	if got, _ := RetrieveOutput(database, ids[0]); got != nil {
		t.Fatalf("oldest (lowest-value) should have been evicted")
	}
}

func TestEvictPrefersRecencyOverRawCount(t *testing.T) {
	database, _ := Open(":memory:")
	defer database.Close()
	const now = int64(2_000_000_000)
	oneMB := 1024 * 1024

	// A: hammered long ago (high count, very old last access).
	a := mkInput("proj", "mcp__x__a", "x")
	a.OriginalSize = oneMB
	sa, _ := StoreOutput(database, a)
	database.Exec("UPDATE stored_outputs SET access_count=50, last_accessed=?, created_at=? WHERE id=?", now-60*86400, now-60*86400, sa.ID)
	// B: steadily used recently (low count, fresh).
	b := mkInput("proj", "mcp__x__b", "x")
	b.OriginalSize = oneMB
	sb, _ := StoreOutput(database, b)
	database.Exec("UPDATE stored_outputs SET access_count=3, last_accessed=?, created_at=? WHERE id=?", now-3600, now-3600, sb.ID)

	// cap 1MB → shed ~1MB → evict the lower-VALUE item. Decay ranks the
	// abandoned once-hammered A below the fresh B (pure LFU would keep A).
	n, _ := EvictIfNeeded(database, "proj", 1, 7, now)
	if n != 1 {
		t.Fatalf("expected 1 evicted, got %d", n)
	}
	if got, _ := RetrieveOutput(database, sa.ID); got != nil {
		t.Fatalf("stale-but-high-count item should be evicted, not the fresh one")
	}
	if got, _ := RetrieveOutput(database, sb.ID); got == nil {
		t.Fatalf("fresh item should survive")
	}
}

func TestEvictSkipsPinned(t *testing.T) {
	database, _ := Open(":memory:")
	defer database.Close()

	oneMB := 1024 * 1024
	in := mkInput("proj", "mcp__x__big", "x")
	in.OriginalSize = oneMB * 3
	s, _ := StoreOutput(database, in)
	PinOutput(database, s.ID, "proj", true)

	n, _ := EvictIfNeeded(database, "proj", 1, 7, 2_000_000_000) // way over, but only item is pinned
	if n != 0 {
		t.Fatalf("pinned item must not be evicted, got %d", n)
	}
}

func TestForget(t *testing.T) {
	database, _ := Open(":memory:")
	defer database.Close()

	a, _ := StoreOutput(database, mkInput("proj", "mcp__x__a", "aaa"))
	b, _ := StoreOutput(database, mkInput("proj", "mcp__x__b", "bbb"))
	PinOutput(database, b.ID, "proj", true)

	// forget by id
	n, _ := ForgetOutputs(database, "proj", ForgetOptions{ID: a.ID})
	if n != 1 {
		t.Fatalf("forget by id: %d", n)
	}
	// forget all skips pinned
	n, _ = ForgetOutputs(database, "proj", ForgetOptions{All: true})
	if n != 0 {
		t.Fatalf("forget all should skip pinned: %d", n)
	}
	// single-id delete bypasses pin protection
	n, _ = ForgetOutputs(database, "proj", ForgetOptions{ID: b.ID})
	if n != 1 {
		t.Fatalf("single-id delete should bypass pin: %d", n)
	}
}

func TestPruneExpired(t *testing.T) {
	database, _ := Open(":memory:")
	defer database.Close()

	old, _ := StoreOutput(database, mkInput("proj", "mcp__x__old", "old"))
	pinnedOld, _ := StoreOutput(database, mkInput("proj", "mcp__x__pin", "pin"))
	// backdate both by 100 days
	past := nowUnix() - 100*86400
	database.Exec("UPDATE stored_outputs SET created_at = ?", past)
	PinOutput(database, pinnedOld.ID, "proj", true)

	n, err := PruneExpired(database, "proj", 30)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 pruned (unpinned), got %d", n)
	}
	if got, _ := RetrieveOutput(database, old.ID); got != nil {
		t.Fatalf("old unpinned should be pruned")
	}
	if got, _ := RetrieveOutput(database, pinnedOld.ID); got == nil {
		t.Fatalf("pinned should survive prune")
	}
}

func TestStatsAndSessions(t *testing.T) {
	database, _ := Open(":memory:")
	defer database.Close()

	in := mkInput("proj", "mcp__x__a", "aaaa")
	in.OriginalSize = 1000
	StoreOutput(database, in)

	s := GetStats(database, "proj")
	if s.TotalItems != 1 || s.TotalOriginalBytes != 1000 {
		t.Fatalf("stats: %+v", s)
	}
	if s.CompressionRatio <= 0 || s.CompressionRatio >= 1 {
		t.Fatalf("ratio out of range: %v", s.CompressionRatio)
	}

	RecordSession(database, "2026-07-20")
	RecordSession(database, "2026-07-20") // idempotent
	RecordSession(database, "2026-07-22")
	days := GetSessionDays(database)
	if len(days) != 2 || days[0] != "2026-07-22" {
		t.Fatalf("session days: %v", days)
	}
}

func TestRecordAccess(t *testing.T) {
	database, _ := Open(":memory:")
	defer database.Close()
	s, _ := StoreOutput(database, mkInput("proj", "mcp__x__a", "aaaa"))
	RecordAccess(database, s.ID)
	RecordAccess(database, s.ID)
	got, _ := RetrieveOutput(database, s.ID)
	if got.AccessCount != 2 || got.LastAccessed == nil {
		t.Fatalf("access tracking: %+v", got)
	}
}
