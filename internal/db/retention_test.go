// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/db/retention_test.go
// Covers summary-only storage (upstream PR #246 / store.retention).

package db

import "testing"

func mkRetentionInput(fullRetained *int) StoreInput {
	return StoreInput{
		ProjectKey:   "proj",
		SessionID:    "sess1",
		ToolName:     "Bash",
		Summary:      "[s]",
		FullContent:  "the quick brown fox jumps over the lazy dog",
		OriginalSize: 43,
		FullRetained: fullRetained,
	}
}

func TestStoreOutputDefaultsToRetained(t *testing.T) {
	database, _ := Open(":memory:")
	defer database.Close()

	s, err := StoreOutput(database, mkRetentionInput(nil))
	if err != nil {
		t.Fatal(err)
	}
	if s.FullRetained != 1 {
		t.Errorf("FullRetained = %d, want 1", s.FullRetained)
	}
	got, _ := RetrieveOutput(database, s.ID)
	if got.FullContent == "" || got.FullRetained != 1 {
		t.Errorf("body should be persisted: %+v", got)
	}
}

// A summary-only row drops the body and its chunks, but still hashes the REAL
// content so dedup keeps working.
func TestStoreOutputSummaryOnly(t *testing.T) {
	database, _ := Open(":memory:")
	defer database.Close()

	zero := 0
	in := mkRetentionInput(&zero)
	s, err := StoreOutput(database, in)
	if err != nil {
		t.Fatal(err)
	}
	if s.FullRetained != 0 || s.FullContent != "" {
		t.Errorf("row should be summary-only: %+v", s)
	}

	got, _ := RetrieveOutput(database, s.ID)
	if got.FullContent != "" {
		t.Errorf("body should not be persisted: %q", got.FullContent)
	}
	if got.Summary != "[s]" {
		t.Errorf("summary should survive: %q", got.Summary)
	}

	var chunks int
	database.QueryRow(`SELECT COUNT(*) FROM content_chunks WHERE output_id = ?`, s.ID).Scan(&chunks)
	if chunks != 0 {
		t.Errorf("summary-only row wrote %d chunks", chunks)
	}

	// Dedup must still match on the real content, not on the empty body.
	if *s.OutputHash != HashContent(in.FullContent) {
		t.Error("output_hash should hash the real content")
	}
	hit, _ := CheckOutputDedup(database, "proj", HashContent(in.FullContent), in.ToolName, in.CommandFP)
	if hit == nil {
		t.Error("summary-only row should still be dedup-matchable")
	}
}
