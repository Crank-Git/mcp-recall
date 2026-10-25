// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/db/matchwindow_test.go
// Covers search excerpts placed on the match (upstream #405).

package db

import (
	"strings"
	"testing"
	"time"
)

func mark(s string) string { return "" + s + "" }

func win(marked string, width int) MatchWindow {
	return windowAroundMatches(marked, width, defaultMarks)
}

func TestWindowUnmarkedTextCutAtEnd(t *testing.T) {
	if w := win(strings.Repeat("a", 20), 10); w != (MatchWindow{Text: strings.Repeat("a", 10), CutEnd: true}) {
		t.Errorf("window = %+v", w)
	}
}

func TestWindowClampsMatchNearEnd(t *testing.T) {
	if w := win(strings.Repeat("a", 40)+mark("hit"), 10); w != (MatchWindow{Text: strings.Repeat("a", 7) + "hit", CutStart: true}) {
		t.Errorf("window = %+v", w)
	}
}

func TestWindowMatchAtStart(t *testing.T) {
	w := win(mark("hit")+strings.Repeat("b", 40), 10)
	if !strings.HasPrefix(w.Text, "hit") || w.CutStart {
		t.Errorf("window = %+v", w)
	}
}

func TestWindowPrefersDistinctTerms(t *testing.T) {
	w := win(mark("fn")+" "+mark("fn")+" "+mark("fn")+strings.Repeat("-", 30)+mark("fn")+" "+mark("unit7"), 12)
	if !strings.Contains(w.Text, "fn unit7") {
		t.Errorf("window = %q", w.Text)
	}
}

func TestWindowBreaksTieWithTightestCluster(t *testing.T) {
	// The first line shares every term but the last; the window must not drift onto it.
	line := func(n string) string { return mark("export") + " " + mark("fn") + " " + n + "(" + mark("x") + ");" }
	w := win(line("a1")+" "+line(mark("a2"))+" "+strings.Repeat("-", 40), 22)
	if !strings.Contains(w.Text, "export fn a2(x);") {
		t.Errorf("window = %q", w.Text)
	}
}

func TestWindowSingleMatchWiderThanWindow(t *testing.T) {
	w := win(strings.Repeat("a", 20)+mark(strings.Repeat("b", 30))+strings.Repeat("c", 20), 10)
	if len([]rune(w.Text)) != 10 {
		t.Errorf("window = %q", w.Text)
	}
}

func TestWindowFastWithThousandsOfMatches(t *testing.T) {
	doc := strings.Repeat(mark("hit")+" ", 50000)
	start := time.Now()
	win(doc, 150)
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Errorf("took %v", d)
	}
}

func TestWindowCentersMatch(t *testing.T) {
	if w := win(strings.Repeat("a", 40)+mark("hit")+strings.Repeat("b", 40), 13); w.Text != "aaaaahitbbbbb" {
		t.Errorf("window = %q", w.Text)
	}
}

func TestWindowCountsRunes(t *testing.T) {
	w := win(strings.Repeat("é", 40)+mark("hit")+strings.Repeat("ü", 40), 13)
	if w.Text != "éééééhitüüüüü" {
		t.Errorf("window = %q", w.Text)
	}
}

func mwInput(body string) StoreInput {
	return StoreInput{ProjectKey: projKey, SessionID: "s", ToolName: "mcp__tool", Summary: "summary", FullContent: body, OriginalSize: 100}
}

func TestRetrieveMatchWindowDeepInChunk(t *testing.T) {
	database := newDB(t)
	s := mustStore(t, database, mwInput(strings.Repeat("x ", 150)+"deepword "+strings.Repeat("y ", 50)))
	w := RetrieveMatchWindow(database, s.ID, "deepword", 40)
	if w == nil || !strings.Contains(w.Text, "deepword") || !w.CutStart || !w.CutEnd {
		t.Errorf("window = %+v", w)
	}
}

func TestRetrieveMatchWindowNoMatch(t *testing.T) {
	database := newDB(t)
	s := mustStore(t, database, mwInput("completely different content"))
	if w := RetrieveMatchWindow(database, s.ID, "zzznomatch", 40); w != nil {
		t.Errorf("window = %+v", w)
	}
}

func TestRetrieveMatchWindowStraddlesChunkBoundary(t *testing.T) {
	database := newDB(t)
	// Chunks are [0, 512) and [448, 960): spanalpha sits only in the first, spanbeta only in the second.
	content := strings.Repeat("x", 430) + " spanalpha " + strings.Repeat("m", 70) + " spanbeta " + strings.Repeat("y", 500)
	s := mustStore(t, database, mwInput(content))
	w := RetrieveMatchWindow(database, s.ID, "spanalpha spanbeta", 120)
	if w == nil || !strings.Contains(w.Text, "spanalpha") || !strings.Contains(w.Text, "spanbeta") {
		t.Errorf("window = %+v", w)
	}
}

func TestRetrieveMatchWindowContentHoldingMarkers(t *testing.T) {
	database := newDB(t)
	// Nerd-font icons (e.g. `eza --icons`) live in the same private-use range.
	content := " folder  file " + strings.Repeat("z ", 100) + " iconhit"
	s := mustStore(t, database, mwInput(content))
	w := RetrieveMatchWindow(database, s.ID, "iconhit", 40)
	if w == nil || !strings.Contains(w.Text, " iconhit") || !w.CutStart {
		t.Errorf("window = %+v", w)
	}
}

func TestRetrieveMatchWindowItemBeforeChunking(t *testing.T) {
	database := newDB(t)
	if _, err := database.Exec(`
		INSERT INTO stored_outputs
			(id, project_key, session_id, tool_name, summary, full_content, original_size, summary_size, created_at)
		VALUES ('recall_legacy02', ?, 'sess', 'mcp__tool', 'summary', ?, 100, 7, ?)`,
		projKey, strings.Repeat("z ", 200)+"legacyhit tail", time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	w := RetrieveMatchWindow(database, "recall_legacy02", "legacyhit", 30)
	if w == nil || !strings.Contains(w.Text, "legacyhit") || !w.CutStart {
		t.Errorf("window = %+v", w)
	}
}
