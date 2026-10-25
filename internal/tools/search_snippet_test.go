// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/tools/search_snippet_test.go
// Covers recall__search excerpts placed on the match (upstream #405).

package tools

import (
	"strings"
	"testing"
)

func snippetLine(t *testing.T, result string) string {
	t.Helper()
	for _, l := range strings.Split(result, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), ">") {
			return strings.TrimSpace(l)
		}
	}
	t.Fatalf("no snippet line in:\n%s", result)
	return ""
}

func searchWith(t *testing.T, summary, body, query string) string {
	t.Helper()
	database := setup(t)
	in := baseInput()
	in.Summary, in.FullContent = summary, body
	store(t, database, in)
	return snippetLine(t, Search(database, projectKey, SearchArgs{Query: query}))
}

func TestSearchShowsDeepMatch(t *testing.T) {
	line := searchWith(t, "item with a deep match", strings.Repeat("alpha ", 60)+"needleword"+strings.Repeat(" beta", 20), "needleword")
	hasT(t, line, "needleword")
	if !strings.HasPrefix(line, "> …") || !strings.HasSuffix(line, "…") {
		t.Errorf("line = %q", line)
	}
}

func TestSearchMarksNoCutForWholeChunk(t *testing.T) {
	if line := searchWith(t, "short item", "frobnication happens here", "frobnication"); line != "> frobnication happens here" {
		t.Errorf("line = %q", line)
	}
}

func TestSearchShowsEveryTermOfMultiTermMatch(t *testing.T) {
	body := strings.Repeat("pad ", 60) + "error in src/foo.ts line 42 unused import" + strings.Repeat(" pad", 60)
	hasT(t, searchWith(t, "lint output", body, "src/foo.ts line 42 unused import"), "src/foo.ts line 42 unused import")
}

func TestSearchPrefersWindowWithMostTerms(t *testing.T) {
	body := "alpha " + strings.Repeat("pad ", 60) + "alpha beta gamma delta" + strings.Repeat(" pad", 20)
	hasT(t, searchWith(t, "two hits", body, "alpha beta gamma delta"), "alpha beta gamma delta")
}
