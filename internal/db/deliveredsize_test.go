// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/db/deliveredsize_test.go
// Port of the upstream v1.16 delivered_size vectors (#319).

package db

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func i64(n int64) *int64 { return &n }

func TestDeliveredSizePersists(t *testing.T) {
	database := newDB(t)
	s := mustStore(t, database, input(func(in *StoreInput) { in.DeliveredSize = i64(140) }))
	if s.DeliveredSize == nil || *s.DeliveredSize != 140 {
		t.Errorf("stored = %v", s.DeliveredSize)
	}
	if got, _ := RetrieveOutput(database, s.ID); got == nil || got.DeliveredSize == nil || *got.DeliveredSize != 140 {
		t.Errorf("retrieved = %+v", got)
	}
	n := mustStore(t, database, input(func(in *StoreInput) { in.FullContent = "other body" }))
	if got, _ := RetrieveOutput(database, n.ID); got.DeliveredSize != nil {
		t.Errorf("omitted delivered_size = %d, want NULL", *got.DeliveredSize)
	}
}

// Every savings figure counts delivered bytes, falling back to summary_size
// for rows written before the column existed.
func TestDeliveredSizeInSavingsFigures(t *testing.T) {
	database := newDB(t)
	fp := "ls"
	mustStore(t, database, input(func(in *StoreInput) {
		in.ToolName, in.CommandFP, in.OriginalSize, in.Summary, in.FullContent = "Bash", &fp, 1000, "abcd", "a"
		in.DeliveredSize = i64(104)
	}))
	mustStore(t, database, input(func(in *StoreInput) {
		in.ToolName, in.CommandFP, in.OriginalSize, in.Summary, in.FullContent = "Bash", &fp, 1000, "efgh", "b"
	}))
	const delivered = 104 + 4
	if got := GetStats(database, projKey).TotalSummaryBytes; got != delivered {
		t.Errorf("stats = %d", got)
	}
	for _, r := range GetToolBreakdown(database, projKey) {
		if r.ToolName == "Bash" && r.SummaryBytes != delivered {
			t.Errorf("tool breakdown = %d", r.SummaryBytes)
		}
	}
	if rows := GetBashCommandBreakdown(database, projKey); len(rows) != 1 || rows[0].SummaryBytes != delivered {
		t.Errorf("command breakdown = %+v", rows)
	}
	if got := GetSessionSummary(database, projKey, SessionSummaryOptions{}).TotalSummaryBytes; got != delivered {
		t.Errorf("session summary = %d", got)
	}
}

func TestDeliveredSizeKeepsSummarySize(t *testing.T) {
	database := newDB(t)
	zero := 0
	s := mustStore(t, database, input(func(in *StoreInput) {
		in.Summary, in.DeliveredSize, in.FullRetained = "abcd", i64(104), &zero
	}))
	if got, _ := RetrieveOutput(database, s.ID); got.SummarySize != 4 {
		t.Errorf("summary_size = %d", got.SummarySize)
	}
}

// A store created before the column gains it on open, old rows NULL.
func TestDeliveredSizeMigratesExistingDB(t *testing.T) {
	file := filepath.Join(t.TempDir(), "old.db")
	database, _ := Open(file)
	old := mustStore(t, database, input(nil))
	database.Close()
	raw, _ := sql.Open("sqlite", file)
	if _, err := raw.Exec("ALTER TABLE stored_outputs DROP COLUMN delivered_size"); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	database, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if got, _ := RetrieveOutput(database, old.ID); got == nil || got.DeliveredSize != nil {
		t.Errorf("old row = %+v", got)
	}
}
