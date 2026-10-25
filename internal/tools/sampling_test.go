// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/tools/sampling_test.go
// Covers the bounded pinned/notes sections of recall__context and
// recall__session_summary (upstream #337, #338, #343).

package tools

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"mcprecall/internal/db"
)

func insertBulkPinnedNotes(t *testing.T, database *sql.DB, count int, base int64) {
	t.Helper()
	tx, _ := database.Begin()
	for i := 0; i < count; i++ {
		if _, err := tx.Exec(`
			INSERT INTO stored_outputs
				(id, project_key, session_id, tool_name, summary, full_content,
				 original_size, summary_size, created_at, pinned)
			VALUES (?, ?, 'bulk-sync', 'recall__note', 'bulk note', 'body', 1024, 64, ?, 1)`,
			fmt.Sprintf("recall_bulk_%d", i), projectKey, base+int64(i)); err != nil {
			t.Fatal(err)
		}
	}
	tx.Commit()
}

func TestSessionSummaryCapsBulkPinnedNotes(t *testing.T) {
	database := setup(t)
	dayStart, _ := time.Parse("2006-01-02", "2026-06-05")
	insertBulkPinnedNotes(t, database, 2000, dayStart.Unix())

	result := SessionSummary(database, projectKey, SessionSummaryArgs{Date: "2026-06-05"})

	hasT(t, result, "Pinned: 3 of 2,000")
	hasNotT(t, result, "Notes:")
	if n := strings.Count(result, "+1,997 more from bulk-sync"); n != 1 {
		t.Errorf("overflow line appears %d times", n)
	}
	if n := strings.Count(result, "📌"); n != 3 {
		t.Errorf("%d pins shown, want 3", n)
	}
}

func TestContextCapsPinnedSection(t *testing.T) {
	database := setup(t)
	insertBulkPinnedNotes(t, database, 2000, time.Now().Unix()-86400)
	mine := store(t, database, baseInput())
	db.PinOutput(database, mine.ID, projectKey, true)

	result := Context(database, projectKey, ContextArgs{})

	hasT(t, result, "Pinned (4 of 2,001):")
	hasT(t, result, mine.ID)
	hasT(t, result, "+1,997 more from bulk-sync")
	if n := strings.Count(result, "📌"); n != 4 {
		t.Errorf("%d pins shown, want 4", n)
	}
}
