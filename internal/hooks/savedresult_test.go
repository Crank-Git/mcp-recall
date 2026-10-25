// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/hooks/savedresult_test.go
// Port of the upstream saved-MCP-result vectors (#391, #401).

package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// savedFull is a large MCP result, with multibyte text so the character count
// differs from the byte count.
var savedFull = func() string {
	results := make([]map[string]any, 900)
	for i := range results {
		results[i] = map[string]any{"index": i, "status": "success", "uri": fmt.Sprintf("hf://models/org/model-%d", i), "note": strings.Repeat("é ", 20)}
	}
	b, _ := json.Marshal(map[string]any{"results": results})
	return string(b)
}()

func commas(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// savedNotice is Claude Code's notice; lines < 0 leaves out the line count
// added in 2.1.305 (#401).
func savedNotice(path string, chars, lines int) string {
	across := ""
	if lines >= 0 {
		across = " across " + commas(lines) + " lines"
	}
	return "Error: result (" + commas(chars) + " characters" + across + ") exceeds maximum allowed tokens. Output has been saved to " + path + ".\n" +
		"Format: JSON with schema: {results: [{...}]}\n" +
		"- You MUST read the content from the file at " + path + " in sequential chunks until 100% of the content has been read.\n"
}

func runeCount(s string) int { return len([]rune(s)) }

func TestPostSavedMcpResult(t *testing.T) {
	setup := func(t *testing.T, content string) string {
		currentCWD, _ = hookEnv(t)
		fullRetention(t)
		dir := filepath.Join(t.TempDir(), "tool-results")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "mcp-hf-hf_fs-1792768643441.txt")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	run := func(response any) HookOutput {
		return HandlePostToolUse(ptu("mcp__hf__hf_fs", "", map[string]any{"tool_response": response}))
	}
	wantStored := func(t *testing.T, r HookOutput, content string) {
		t.Helper()
		items := storedItems(t)
		if len(items) != 1 || items[0].FullContent != content || items[0].OriginalSize != len(content) {
			t.Fatalf("stored %d items", len(items))
		}
		if strings.Contains(r.UpdatedMCPToolOutput, "exceeds maximum allowed tokens") {
			t.Error("delivered the notice")
		}
	}

	for _, shape := range []struct {
		name string
		wrap func(string) any
	}{
		{"string", func(s string) any { return s }},
		{"content object", func(s string) any { return map[string]any{"content": []any{map[string]any{"type": "text", "text": s}}} }},
		{"bare block array", func(s string) any { return []any{map[string]any{"type": "text", "text": s}} }},
	} {
		t.Run("stores and summarises the saved result, not the notice, for the "+shape.name+" shape", func(t *testing.T) {
			path := setup(t, savedFull)
			wantStored(t, run(shape.wrap(savedNotice(path, runeCount(savedFull), -1))), savedFull)
		})
	}
	t.Run("scans the saved result for secrets", func(t *testing.T) {
		withKey := savedFull[:len(savedFull)-2] + `,"k":"sk-ant-api03-` + strings.Repeat("A", 95) + `"}`
		path := setup(t, withKey)
		if r := run(savedNotice(path, runeCount(withKey), -1)); !isEmpty(r) {
			t.Error("stored a result holding a secret")
		}
	})
	t.Run("passes the notice through whole when the saved file is missing", func(t *testing.T) {
		path := setup(t, savedFull)
		os.Remove(path)
		if r := run(savedNotice(path, runeCount(savedFull), -1)); !isEmpty(r) {
			t.Error("replaced the notice")
		}
	})
	t.Run("passes the notice through whole when the character count does not match", func(t *testing.T) {
		path := setup(t, savedFull)
		if r := run(savedNotice(path, runeCount(savedFull)-1, -1)); !isEmpty(r) {
			t.Error("replaced the notice")
		}
	})
	t.Run("stores the saved result when the notice also gives its line count", func(t *testing.T) {
		multi := savedFull + "\n" + savedFull + "\n" + savedFull
		path := setup(t, multi)
		wantStored(t, run(savedNotice(path, runeCount(multi), 3)), multi)
	})
	t.Run("accepts either line count for a file that ends in a newline", func(t *testing.T) {
		ended := savedFull + "\n" + savedFull + "\n"
		for _, lines := range []int{2, 3} {
			path := setup(t, ended)
			wantStored(t, run(savedNotice(path, runeCount(ended), lines)), ended)
		}
	})
	t.Run("passes the notice through whole when its line count does not match the file", func(t *testing.T) {
		path := setup(t, savedFull)
		if r := run(savedNotice(path, runeCount(savedFull), 2)); !isEmpty(r) {
			t.Error("replaced the notice")
		}
	})
	t.Run("passes the notice through whole when the path is outside a tool-results directory", func(t *testing.T) {
		path := setup(t, savedFull)
		elsewhere := filepath.Join(filepath.Dir(filepath.Dir(path)), "elsewhere.txt")
		_ = os.WriteFile(elsewhere, []byte(savedFull), 0o644)
		if r := run(savedNotice(elsewhere, runeCount(savedFull), -1)); !isEmpty(r) {
			t.Error("replaced the notice")
		}
	})
	t.Run("treats the same notice text from Bash as output, not a saved MCP result", func(t *testing.T) {
		path := setup(t, savedFull)
		r := HandlePostToolUse(ptuResponse("Bash", bashEnvelope(savedNotice(path, runeCount(savedFull), -1), ""), "cat notice.txt"))
		b, _ := json.Marshal(r)
		if strings.Contains(string(b), `"index":899`) {
			t.Error("read the saved file for Bash")
		}
	})
}
