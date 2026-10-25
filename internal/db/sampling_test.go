// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/db/sampling_test.go
// Covers session-fair sampling of pinned items and notes in GetContext and
// GetSessionSummary (upstream #269, #336, #337, #338, #343).

package db

import (
	"database/sql"
	"fmt"
	"reflect"
	"testing"
	"time"
)

// pinMany writes count notes into one session, created ageSeconds ago and
// spaced 1s apart, pinned unless pinned is 0: the shape a bulk note backend
// leaves behind (upstream #269).
func pinMany(t *testing.T, database *sql.DB, sessionID string, count int, ageSeconds int64, pinned int) {
	t.Helper()
	tx, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().Unix() - ageSeconds
	for i := 0; i < count; i++ {
		if _, err := tx.Exec(`
			INSERT INTO stored_outputs
				(id, project_key, session_id, tool_name, summary, full_content,
				 original_size, summary_size, created_at, pinned)
			VALUES (?, ?, ?, 'recall__note', 'a note', 'full note body', 1024, 64, ?, ?)`,
			fmt.Sprintf("recall_%s_%d", sessionID, i), projKey, sessionID, base+int64(i), pinned); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func shownPerSession(items []SampledItem) map[string]int {
	counts := map[string]int{}
	for _, it := range items {
		counts[it.SessionID]++
	}
	return counts
}

func sampleIDs(items []SampledItem) map[string]bool {
	ids := map[string]bool{}
	for _, it := range items {
		ids[it.ID] = true
	}
	return ids
}

func TestSampleBulkSessionCannotCrowdOutLonePin(t *testing.T) {
	database := newDB(t)
	pinMany(t, database, "mine", 1, 90*86400, 1)
	pinMany(t, database, "other-sync", 20, 30*86400, 1)
	pinMany(t, database, "bulk-sync", 500, 86400, 1)

	data := GetContext(database, projKey, 0, 0)

	if !sampleIDs(data.Pinned.Items)["recall_mine_0"] {
		t.Error("lone pin crowded out")
	}
	if len(data.Pinned.Items) > SampleLimit {
		t.Errorf("%d items, cap %d", len(data.Pinned.Items), SampleLimit)
	}
	for s, c := range shownPerSession(data.Pinned.Items) {
		if c > SamplePerSession {
			t.Errorf("session %s shows %d", s, c)
		}
	}
	if data.Pinned.Total != 521 {
		t.Errorf("total = %d, want 521", data.Pinned.Total)
	}
	want := []SessionOverflow{{"bulk-sync", 497}, {"other-sync", 17}}
	if !reflect.DeepEqual(data.Pinned.Overflow, want) {
		t.Errorf("overflow = %v, want %v", data.Pinned.Overflow, want)
	}
}

func TestSampleDealsSlotsRoundRobin(t *testing.T) {
	database := newDB(t)
	// Staggered ages: by recency alone s1-s3 would take three slots each and
	// s4 one, so this fails unless slots are dealt round-robin.
	for i, s := range []string{"s1", "s2", "s3", "s4"} {
		pinMany(t, database, s, 5, int64(i+1)*86400, 1)
	}
	data := GetContext(database, projKey, 0, 0)
	if len(data.Pinned.Items) != 10 {
		t.Fatalf("%d items, want 10", len(data.Pinned.Items))
	}
	for s, c := range shownPerSession(data.Pinned.Items) {
		if c < 2 {
			t.Errorf("session %s shows only %d", s, c)
		}
	}
}

func TestSampleUnderCapHasNoOverflow(t *testing.T) {
	database := newDB(t)
	pinMany(t, database, "mine", 2, 86400, 1)
	data := GetContext(database, projKey, 0, 0)
	if len(data.Pinned.Items) != 2 || data.Pinned.Total != 2 || len(data.Pinned.Overflow) != 0 {
		t.Errorf("sample = %+v", data.Pinned)
	}
}

// summaryDay is a fixed past day that keeps every fixture row inside the
// summarised date.
const summaryDay = "2026-06-05"

func ageInDay() int64 {
	return time.Now().Unix() - (startOfDayUnix(summaryDay) + 3600)
}

func TestSessionSummaryBoundsBulkSessions(t *testing.T) {
	database := newDB(t)
	pinMany(t, database, "mine", 1, ageInDay(), 1)
	pinMany(t, database, "bulk-sync", 2000, ageInDay()-60, 1)
	pinMany(t, database, "mine-notes", 1, ageInDay(), 0)
	pinMany(t, database, "bulk-notes", 2000, ageInDay()-60, 0)

	s := GetSessionSummary(database, projKey, SessionSummaryOptions{Date: summaryDay})

	if !sampleIDs(s.Pinned.Items)["recall_mine_0"] || len(s.Pinned.Items) != 4 || s.Pinned.Total != 2001 {
		t.Errorf("pinned = %d items, total %d", len(s.Pinned.Items), s.Pinned.Total)
	}
	if want := []SessionOverflow{{"bulk-sync", 1997}}; !reflect.DeepEqual(s.Pinned.Overflow, want) {
		t.Errorf("pinned overflow = %v", s.Pinned.Overflow)
	}
	if !sampleIDs(s.Notes.Items)["recall_mine-notes_0"] || len(s.Notes.Items) != 4 || s.Notes.Total != 2001 {
		t.Errorf("notes = %d items, total %d", len(s.Notes.Items), s.Notes.Total)
	}
	if want := []SessionOverflow{{"bulk-notes", 1997}}; !reflect.DeepEqual(s.Notes.Overflow, want) {
		t.Errorf("notes overflow = %v", s.Notes.Overflow)
	}
}

func TestSessionSummaryListsPinnedNoteOnce(t *testing.T) {
	database := newDB(t)
	pinMany(t, database, "mine", 1, ageInDay(), 1)
	s := GetSessionSummary(database, projKey, SessionSummaryOptions{Date: summaryDay})
	if s.Pinned.Total != 1 || s.Notes.Total != 0 {
		t.Errorf("pinned total %d, notes total %d", s.Pinned.Total, s.Notes.Total)
	}
}

func TestSessionSummaryOneSessionGetsEverySlot(t *testing.T) {
	database := newDB(t)
	pinMany(t, database, "bulk-sync", 500, ageInDay(), 1)
	s := GetSessionSummary(database, projKey, SessionSummaryOptions{SessionID: "bulk-sync"})
	if len(s.Pinned.Items) != 10 {
		t.Errorf("%d items, want 10", len(s.Pinned.Items))
	}
	if want := []SessionOverflow{{"bulk-sync", 490}}; !reflect.DeepEqual(s.Pinned.Overflow, want) {
		t.Errorf("overflow = %v", s.Pinned.Overflow)
	}
}
