// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/db/matchwindow.go
// Search-result excerpts placed on the match (upstream #405). Positions are
// runes throughout: the marked text, the spans and the window.

package db

import (
	"database/sql"
	"errors"
	"strings"
)

// MatchWindow is a bounded excerpt of a search match, and which sides of it
// were cut.
type MatchWindow struct {
	Text     string
	CutStart bool
	CutEnd   bool
}

// marks is the pair FTS5 highlight() wraps each matched token in.
type marks struct{ open, close string }

// defaultMarks are private-use code points, rare in tool output but not
// absent: nerd-font icons share the range, so content holding these gets a
// pair from markerPairFor.
var defaultMarks = marks{"", ""}

// markerPairFor returns the first private-use pair absent from raw, or false
// if every one occurs.
func markerPairFor(raw string) (marks, bool) {
	var free []string
	for cp := rune(0xe000); cp <= 0xf8ff && len(free) < 2; cp++ {
		if ch := string(cp); !strings.Contains(raw, ch) {
			free = append(free, ch)
		}
	}
	if len(free) < 2 {
		return marks{}, false
	}
	return marks{free[0], free[1]}, true
}

type matchSpan struct {
	start, end int
	term       string
}

// parseMarks strips highlight markers, returning the plain text as runes and
// each match's span and lowercased term.
func parseMarks(marked string, m marks) ([]rune, []matchSpan) {
	var spans []matchSpan
	var text []rune
	for i, piece := range strings.Split(marked, m.open) {
		closeAt := -1
		if i > 0 {
			closeAt = strings.Index(piece, m.close)
		}
		if closeAt >= 0 {
			term := []rune(piece[:closeAt])
			spans = append(spans, matchSpan{start: len(text), end: len(text) + len(term), term: strings.ToLower(string(term))})
			piece = piece[:closeAt] + piece[closeAt+len(m.close):]
		}
		text = append(text, []rune(piece)...)
	}
	return text, spans
}

// windowAroundMatches cuts marked (text with highlight markers) to width runes
// around the best run of matches, centered on it. Best covers the most
// distinct matched terms, then spans the fewest characters: near-duplicate
// lines share every term but the one that tells them apart, so a looser run
// drifts onto a sibling line. Text without markers yields its first width runes.
func windowAroundMatches(marked string, width int, m marks) MatchWindow {
	text, spans := parseMarks(marked, m)
	if len(text) <= width {
		return MatchWindow{Text: string(text)}
	}

	// Sliding window over the matches: for each right end, drop from the left
	// while the run is too wide or the leftmost term repeats later in the run.
	counts := map[string]int{}
	bestTerms, bestStart, bestEnd := 0, 0, 0
	left := 0
	for right := range spans {
		counts[spans[right].term]++
		for left <= right && (spans[right].end-spans[left].start > width || counts[spans[left].term] > 1) {
			if counts[spans[left].term]--; counts[spans[left].term] == 0 {
				delete(counts, spans[left].term)
			}
			left++
		}
		if left > right {
			continue // one match wider than the window
		}
		start, end := spans[left].start, spans[right].end
		if len(counts) > bestTerms || (len(counts) == bestTerms && end-start < bestEnd-bestStart) {
			bestTerms, bestStart, bestEnd = len(counts), start, end
		}
	}
	start := 0
	if bestTerms > 0 {
		slack := width - (bestEnd - bestStart)
		start = max(0, min(bestStart-slack/2, len(text)-width))
	}
	return MatchWindow{
		Text:     string(text[start : start+width]),
		CutStart: start > 0,
		CutEnd:   start+width < len(text),
	}
}

// RetrieveMatchWindow is the search-result excerpt for one stored item: width
// runes of its best matching chunk, placed on the match. Match positions come
// from FTS5 itself, so they agree with the tokenizer that found the item. When
// no one chunk holds every term (the match straddles a chunk boundary, or the
// item predates chunking), the window is placed over the whole document
// instead. Returns nil when nothing matches or the query fails to parse.
func RetrieveMatchWindow(database *sql.DB, id, query string, width int) *MatchWindow {
	safe := SanitizeFtsQuery(query)
	const chunkSQL = `
		SELECT content, highlight(content_chunks, 2, ?, ?) FROM content_chunks
		WHERE content_chunks MATCH ? AND output_id = ?
		ORDER BY rank
		LIMIT 1`
	const docSQL = `
		SELECT full_content, highlight(outputs_fts, 3, ?, ?) FROM outputs_fts
		WHERE outputs_fts MATCH ? AND rowid = (SELECT rowid FROM stored_outputs WHERE id = ?)`

	run := func(q string, m marks) (raw, marked string, err error) {
		err = database.QueryRow(q, m.open, m.close, safe, id).Scan(&raw, &marked)
		return
	}
	// Content already holding a default marker is highlighted again with a
	// pair it lacks.
	place := func(q string) (*MatchWindow, error) {
		raw, marked, err := run(q, defaultMarks)
		if err != nil {
			return nil, err
		}
		if !strings.Contains(raw, defaultMarks.open) && !strings.Contains(raw, defaultMarks.close) {
			w := windowAroundMatches(marked, width, defaultMarks)
			return &w, nil
		}
		m, ok := markerPairFor(raw)
		if !ok {
			r := []rune(raw)
			return &MatchWindow{Text: string(r[:min(width, len(r))]), CutEnd: len(r) > width}, nil
		}
		if _, marked, err = run(q, m); err != nil {
			return nil, err
		}
		w := windowAroundMatches(marked, width, m)
		return &w, nil
	}
	w, err := place(chunkSQL)
	if errors.Is(err, sql.ErrNoRows) {
		w, err = place(docSQL)
	}
	if err != nil {
		return nil // no match, or an FTS parse error
	}
	return w
}
