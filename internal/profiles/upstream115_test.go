// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/profiles/upstream115_test.go
// Port of the upstream v1.15.1 profile vectors: text-block parsing and the
// json_extract excerpt fallback (#299), the bundled Context7 v2.0.0 profiles
// and the Cloudflare docs profile.

package profiles

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"mcprecall/internal/jsonx"
)

func textBlocks(t *testing.T, text string) any {
	t.Helper()
	b, _ := json.Marshal([]any{map[string]any{"type": "text", "text": text}})
	v, err := jsonx.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestJSONExtractTextBlocks(t *testing.T) {
	// Context7 sends markdown in a top-level text-block array; a `learn`
	// profile guessed JSON fields for it and every summary was "1 item:\n1. ".
	guessed := Strategy{
		Type:             "json_extract",
		ItemsPath:        []string{"items", "results", "data", "nodes"},
		Fields:           []string{"id", "title", "name", "libraryId"},
		MaxItems:         intptr(10),
		MaxCharsPerField: intptr(200),
		FallbackChars:    intptr(500),
	}
	t.Run("summarises markdown in a text-block array instead of empty items", func(t *testing.T) {
		markdown := "Available Libraries:\n\n- Title: SQLite\n- Context7-compatible library ID: /websites/sqlite_docs\n" +
			strings.Repeat("- Description: an embedded SQL engine\n", 40)
		s := applyJSONExtract(guessed, textBlocks(t, markdown)).Summary
		if !strings.Contains(s, "/websites/sqlite_docs") || regexp.MustCompile(`(?m)^\d+ items?:\n1\. $`).MatchString(s) {
			t.Errorf("got %q", s)
		}
	})
	t.Run("parses a JSON body carried in a text-block array", func(t *testing.T) {
		body := `{"results":[{"id":"lib-1","title":"First"},{"id":"lib-2","title":"Second"}]}`
		s := applyJSONExtract(guessed, textBlocks(t, body)).Summary
		for _, want := range []string{"2 items:", "id: lib-1", "title: Second"} {
			if !strings.Contains(s, want) {
				t.Errorf("missing %q in %q", want, s)
			}
		}
	})
	t.Run("falls back to an excerpt when no field matches any item", func(t *testing.T) {
		s := applyJSONExtract(guessed, `{"results":[{"other":"unmatched value one"},{"other":"unmatched value two"}]}`).Summary
		if !strings.Contains(s, "unmatched value one") || strings.Contains(s, "1. ") {
			t.Errorf("got %q", s)
		}
	})
}

func TestTextTruncateKeepsCompleteText(t *testing.T) {
	s := Strategy{Type: "text_truncate", MaxChars: intptr(100)}
	if got := applyTextTruncate(s, "short\n----------\n").Summary; got != "short\n----------\n" {
		t.Errorf("got %q", got)
	}
}

// bundledOnly loads the embedded profiles alone.
func bundledOnly(t *testing.T) {
	t.Helper()
	isolateDirs(t)
	os.Unsetenv("RECALL_BUNDLED_PROFILES_PATH")
}

var allTiers = []string{"user", "community", "bundled"}

func TestBundledContext7(t *testing.T) {
	bundledOnly(t)
	t.Run("resolve-library-id leaves a typical library list intact", func(t *testing.T) {
		var b strings.Builder
		b.WriteString("Available Libraries:\n\n")
		for i := 0; i < 5; i++ {
			fmt.Fprintf(&b, "- Title: Lib %d\n- Context7-compatible library ID: /org/lib-%d\n- Description: %s\n- Code Snippets: %d\n----------\n",
				i, i, strings.Repeat("An embedded library for testing. ", 6), i*10)
		}
		list := b.String()
		if len(list) <= 1200 {
			t.Fatalf("fixture too short: %d", len(list))
		}
		h := LookupHandler("mcp__context7__resolve-library-id", allTiers)
		if h == nil {
			t.Fatal("no bundled handler")
		}
		res := h("mcp__context7__resolve-library-id", textBlocks(t, list))
		for i := 0; i < 5; i++ {
			if !strings.Contains(res.Summary, fmt.Sprintf("/org/lib-%d", i)) {
				t.Errorf("dropped /org/lib-%d", i)
			}
		}
		if len(res.Summary) < res.OriginalSize {
			t.Errorf("summary %d < original %d: the hook would not pass it through", len(res.Summary), res.OriginalSize)
		}
	})
	t.Run("query-docs keeps the top of the documentation", func(t *testing.T) {
		docs := "### useEffect cleanup\n\nReturn a function from the effect.\n" + strings.Repeat("More reference text. ", 70) +
			"MID_MARKER " + strings.Repeat("More reference text. ", 400) + "BOTTOM_MARKER"
		h := LookupHandler("mcp__context7__query-docs", allTiers)
		if h == nil {
			t.Fatal("no bundled handler")
		}
		s := h("mcp__context7__query-docs", textBlocks(t, docs)).Summary
		if !strings.HasPrefix(s, "### useEffect cleanup") || !strings.Contains(s, "MID_MARKER") ||
			strings.Contains(s, "BOTTOM_MARKER") || len([]rune(s)) >= 2100 {
			t.Errorf("summary (%d runes) wrong", len([]rune(s)))
		}
	})
}

func TestBundledCloudflareDocs(t *testing.T) {
	bundledOnly(t)
	var results []any
	for i := 0; i < 7; i++ {
		results = append(results, map[string]any{
			"similarity": 0.9 - float64(i)/100,
			"id":         strings.Repeat("f", 64),
			"url":        fmt.Sprintf("https://developers.cloudflare.com/workers/page-%d/", i),
			"title":      fmt.Sprintf("Workers page %d", i),
			"text":       fmt.Sprintf("Section %d intro. ", i) + strings.Repeat("Durable Objects coordinate state across requests. ", 60) + fmt.Sprintf("TAIL_MARKER_%d", i),
		})
	}
	raw, _ := json.Marshal(map[string]any{"results": results})
	wrapped, _ := jsonx.ParseString(`{"content":[{"type":"text","text":` + jsonx.Compact(string(raw)) + `}]}`)
	for _, shape := range []struct {
		label  string
		output any
	}{{"content-wrapped", wrapped}, {"plain string", string(raw)}} {
		t.Run(shape.label, func(t *testing.T) {
			h := LookupHandler("mcp__cloudflare__docs", allTiers)
			if h == nil {
				t.Fatal("no bundled handler")
			}
			res := h("mcp__cloudflare__docs", shape.output)
			for i := 0; i < 5; i++ {
				if !strings.Contains(res.Summary, fmt.Sprintf("Workers page %d", i)) ||
					!strings.Contains(res.Summary, fmt.Sprintf("https://developers.cloudflare.com/workers/page-%d/", i)) {
					t.Errorf("result %d missing title or URL", i)
				}
			}
			if !strings.Contains(res.Summary, "Section 0 intro.") || strings.Contains(res.Summary, "TAIL_MARKER_0") {
				t.Errorf("excerpt wrong")
			}
			if r := 1 - float64(len(res.Summary))/float64(res.OriginalSize); r <= 0.8 {
				t.Errorf("reduction %.2f", r)
			}
		})
	}
	if h := LookupHandler("mcp__cloudflare__execute", allTiers); h != nil {
		t.Error("claimed another Cloudflare tool")
	}
}

// A profile handler is recorded as profile:<id>, as upstream names it (#341).
func TestProfileHandlerName(t *testing.T) {
	h, name := lookupNamedHandler("mcp__cloudflare__docs", []string{"user", "community", "bundled"})
	if h == nil || name != "profile:mcp__cloudflare__docs" {
		t.Errorf("name = %q", name)
	}
}
