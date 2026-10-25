// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/handlers/handlers_test.go

package handlers

import (
	"strings"
	"testing"
)

// Handlers receive `output` which in the real pipeline is often a raw JSON
// string (from an MCP content block). Passing a string exercises the same path.

func TestExtractTextFromContent(t *testing.T) {
	// {content:[{type:"text",text:"hi"},{type:"text",text:"there"}]}
	out, err := parseOut(`{"content":[{"type":"text","text":"hi"},{"type":"text","text":"there"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := ExtractText(out); got != "hi\nthere" {
		t.Errorf("ExtractText = %q", got)
	}
}

func TestGenericTruncation(t *testing.T) {
	raw := strings.Repeat("a", 600)
	r := genericHandler("x", raw)
	if r.OriginalSize != 600 {
		t.Errorf("originalSize = %d", r.OriginalSize)
	}
	// v1.9.0: single-block long output → head + tail window (≤ 502), contains "…".
	if runeLen(r.Summary) > 502 || !strings.Contains(r.Summary, "…") {
		t.Errorf("block-mode summary wrong len=%d: %q", runeLen(r.Summary), r.Summary)
	}
}

func TestJSONHandlerDepthAndArrayCap(t *testing.T) {
	// v1.9.0: compact output. Depth-truncated value at depth>3 is "…".
	r := jsonHandler("x", `{"a":{"b":{"c":{"d":1}}}}`)
	if r.Summary != `{"a":{"b":{"c":{"d":"…"}}}}` {
		t.Errorf("depth truncation = %q", r.Summary)
	}
	r2 := jsonHandler("x", `[1,2,3,4,5]`)
	if r2.Summary != `[1,2,3,"…2 more"]` {
		t.Errorf("array cap = %q", r2.Summary)
	}
}

func TestFilesystemHeader(t *testing.T) {
	r := filesystemHandler("x", "line1\nline2\nline3")
	if r.Summary != "[3 lines]\nline1\nline2\nline3" {
		t.Errorf("fs summary = %q", r.Summary)
	}
}

func TestGithubSingleAndArray(t *testing.T) {
	single := githubHandler("mcp__github__get_issue",
		`{"number":42,"title":"Fix bug","state":"open","html_url":"http://x","labels":[{"name":"bug"}],"body":"a body"}`)
	want := `#42 · "Fix bug" · [open] · http://x · labels: bug · body: a body`
	if single.Summary != want {
		t.Errorf("github single = %q\nwant %q", single.Summary, want)
	}

	arr := githubHandler("mcp__github__list_issues", `[{"number":1,"title":"a"},{"number":2,"title":"b"}]`)
	if arr.Summary != "#1 · \"a\"\n#2 · \"b\"" {
		t.Errorf("github array = %q", arr.Summary)
	}
}

func TestShellStructured(t *testing.T) {
	r := shellHandler("mcp__x__bash", `{"stdout":"a\nb\nc","stderr":"","exit_code":0}`)
	if r.Summary != "[bash · exit:0 · 3 lines stdout]\na\nb\nc" {
		t.Errorf("shell = %q", r.Summary)
	}
}

func TestGitDiff(t *testing.T) {
	out := `{"stdout":"diff --git a/f.txt b/f.txt\n@@ -1 +1 @@\n-old\n+new\n","stderr":"","exit_code":0}`
	r := gitDiffHandler("Bash", mustParseT(t, out))
	if !strings.Contains(r.Summary, "git diff — 1 file changed, +1 -1") {
		t.Errorf("git diff header missing: %q", r.Summary)
	}
	if !strings.Contains(r.Summary, "@@ -1 +1 @@\n-old\n+new") || !strings.Contains(r.Summary, "f.txt") {
		t.Errorf("git diff file line wrong: %q", r.Summary)
	}
}

func TestDispatchRouting(t *testing.T) {
	cases := map[string]string{
		"mcp__github__list_issues":   "github",
		"mcp__gitlab__list":          "gitlab",
		"mcp__filesystem__read_file": "filesystem",
		"mcp__x__run_command":        "shell",
		"mcp__linear__issue":         "linear",
		"mcp__slack__history":        "slack",
		"mcp__postgres__query":       "database",
		"mcp__acme__list_things":     "generic-or-json",
	}
	for tool := range cases {
		h := GetHandler(tool, `{"a":1}`, nil)
		if h == nil {
			t.Errorf("no handler for %s", tool)
		}
	}
	// content-based JSON fallback
	if GetHandler("mcp__unknown__x", `{"k":1}`, nil) == nil {
		t.Error("json fallback nil")
	}
}
