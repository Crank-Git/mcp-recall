// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/hooks/upstream115_test.go
// Port of the upstream v1.15.x hook vectors: hookSpecificOutput (#298), Bash's
// own output shape (#304), empty summaries (#296), the Bash envelope (#306),
// secrets in Bash output (#313) and the ~50 KB persisted output (#316).

package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"mcprecall/internal/config"
	"mcprecall/internal/db"
	"mcprecall/internal/jsonx"
	"mcprecall/internal/projectkey"
)

// ptuResponse builds a PostToolUse payload with an arbitrary tool_response.
func ptuResponse(toolName string, response any, command string) string {
	return ptu(toolName, "", map[string]any{
		"tool_input":    map[string]any{"command": command},
		"tool_response": response,
	})
}

// fullRetention points the config at retention = "full": the default
// "balanced" keeps no body for a local Bash command.
func fullRetention(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[store]\nretention = \"full\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	os.Setenv("RECALL_CONFIG_PATH", path)
	config.Reset()
}

func storedItems(t *testing.T) []db.StoredOutput {
	t.Helper()
	database, err := db.Open(os.Getenv("RECALL_DB_PATH"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	return db.ExportAll(database, projectkey.Key(currentCWD))
}

func bashEnvelope(stdout, stderr string) map[string]any {
	return map[string]any{"stdout": stdout, "stderr": stderr, "interrupted": false, "isImage": false, "noOutputExpected": false}
}

func TestPostHookSpecificOutput(t *testing.T) {
	currentCWD, _ = hookEnv(t)
	in := ptu("mcp__github__list_issues", largeGithub(), map[string]any{"tool_input": map[string]any{"owner": "org", "repo": "repo"}})
	for i, r := range []HookOutput{HandlePostToolUse(in), HandlePostToolUse(in)} {
		hs := r.HookSpecificOutput
		if hs == nil || hs.HookEventName != "PostToolUse" {
			t.Fatalf("call %d: hookSpecificOutput = %+v", i, hs)
		}
		s, ok := hs.UpdatedToolOutput.(string)
		if !ok || !recallHeaderRe.MatchString(s) || s != r.UpdatedMCPToolOutput {
			t.Errorf("call %d: updatedToolOutput = %#v", i, hs.UpdatedToolOutput)
		}
	}
}

func TestPostBashKeepsOutputShape(t *testing.T) {
	var lines []string
	for i := 1; i <= 600; i++ {
		lines = append(lines, fmt.Sprintf("probe line %d", i))
	}
	resp := bashEnvelope(strings.Join(lines, "\n"), "warning: probe")
	asString, _ := json.Marshal(resp)
	for _, shape := range []struct {
		label    string
		response any
	}{{"object", resp}, {"JSON string", string(asString)}} {
		t.Run(shape.label, func(t *testing.T) {
			currentCWD, _ = hookEnv(t)
			in := ptuResponse("Bash", shape.response, "seq 600 # "+shape.label)
			for i, r := range []HookOutput{HandlePostToolUse(in), HandlePostToolUse(in)} {
				if r.HookSpecificOutput == nil {
					t.Fatalf("call %d: no hookSpecificOutput", i)
				}
				obj, ok := r.HookSpecificOutput.UpdatedToolOutput.(*jsonx.Obj)
				if !ok {
					t.Fatalf("call %d: updatedToolOutput is %T, want object", i, r.HookSpecificOutput.UpdatedToolOutput)
				}
				if obj.Len() != len(resp) {
					t.Errorf("call %d: keys %v", i, obj.Keys())
				}
				stdout, _ := obj.Str("stdout")
				stderr, _ := obj.Str("stderr")
				interrupted, _ := obj.Get("interrupted")
				if !recallHeaderRe.MatchString(stdout) || stdout != r.UpdatedMCPToolOutput || stderr != "" || interrupted != false {
					t.Errorf("call %d: stdout=%q stderr=%q interrupted=%v", i, firstLine(stdout), stderr, interrupted)
				}
				// The serialised hook output carries the object, not a string.
				b, _ := json.Marshal(r)
				if !strings.Contains(string(b), `"updatedToolOutput":{"`) {
					t.Errorf("call %d: serialised %s", i, firstLine(string(b)))
				}
			}
		})
	}
}

// deliveredBody is the text after the `[recall:…]` header: what Claude reads.
func deliveredBody(r HookOutput) string {
	_, body, _ := strings.Cut(r.UpdatedMCPToolOutput, "\n")
	return body
}

func TestPostEmptySummaryFallsBack(t *testing.T) {
	t.Run("Sentry markdown is summarised, not emptied", func(t *testing.T) {
		currentCWD, _ = hookEnv(t)
		markdown := "# Issue PROJ-4F2 in **checkout-api**\n\n**Description**: TypeError reading 'id' of undefined\n" +
			"**First Seen**: 2026-09-30T10:00:00Z\n\n## Stack\n" + strings.Repeat("  at handler (src/routes/checkout.ts:88)\n", 60)
		r := HandlePostToolUse(ptu("mcp__claude_ai_Sentry__get_sentry_resource", "", map[string]any{
			"tool_response": []any{map[string]any{"type": "text", "text": markdown}},
		}))
		if !strings.Contains(deliveredBody(r), "# Issue PROJ-4F2") {
			t.Errorf("delivered %q", deliveredBody(r))
		}
	})
	t.Run("an empty handler summary falls back to generic", func(t *testing.T) {
		currentCWD, _ = hookEnv(t)
		unrelated, _ := json.Marshal(map[string]any{"report": strings.Repeat("quarterly-numbers ", 200)})
		r := HandlePostToolUse(ptu("mcp__sentry__get_report", string(unrelated), nil))
		if strings.TrimSpace(deliveredBody(r)) == "" {
			t.Error("delivered an empty body")
		}
		items := storedItems(t)
		if len(items) != 1 || strings.TrimSpace(items[0].Summary) == "" {
			t.Errorf("stored %d items, summary empty", len(items))
		}
	})
}

func TestPostBashEnvelopeIsNotOutput(t *testing.T) {
	var lines []string
	for i := 0; i < 300; i++ {
		lines = append(lines, fmt.Sprintf("shape value %d from the build log", i))
	}
	stdout := strings.Join(lines, "\n")
	stderr := "warning: deprecated flag"
	envelope := bashEnvelope(stdout, stderr)
	// Claude Code adds bashEditDiff when the command edited files: those
	// edits, not anything the command printed.
	envelope["bashEditDiff"] = "diff --git a/x.ts b/x.ts\n" + strings.Repeat("+zebrafish quokka axolotl\n", 400)
	text := stdout + "\n" + stderr
	asString, _ := json.Marshal(envelope)
	escapedHint := regexp.MustCompile(`"n(shape|warning)"`)

	for _, shape := range []struct {
		label    string
		response any
	}{{"object", envelope}, {"JSON string", string(asString)}} {
		t.Run(shape.label, func(t *testing.T) {
			currentCWD, _ = hookEnv(t)
			fullRetention(t)
			r := HandlePostToolUse(ptuResponse("Bash", shape.response, "cat build.log"))
			items := storedItems(t)
			if len(items) != 1 {
				t.Fatalf("stored %d items", len(items))
			}
			if items[0].OriginalSize != len(text) {
				t.Errorf("original_size %d, want %d", items[0].OriginalSize, len(text))
			}
			if items[0].FullContent != text {
				t.Errorf("full_content is not stdout + stderr")
			}
			header := firstLine(r.UpdatedMCPToolOutput)
			if !strings.Contains(header, "search:") || strings.Contains(header, "isImage") ||
				strings.Contains(header, "noOutputExpected") ||
				regexp.MustCompile(`zebrafish|quokka|axolotl`).MatchString(header) || escapedHint.MatchString(header) {
				t.Errorf("header %q", header)
			}
		})
	}
}

func TestPostBashSecretsAtLineStart(t *testing.T) {
	filler := make([]string, 300)
	for i := range filler {
		filler[i] = fmt.Sprintf("deploy step %d ok", i)
	}
	secretVals := map[string]string{
		"anthropic":  "sk-ant-" + strings.Repeat("a", 40),
		"openrouter": "sk-or-v1-" + strings.Repeat("b", 30),
		"stripe":     "sk_live_" + strings.Repeat("c", 30),
		"twilio":     "AC" + strings.Repeat("d", 32),
		"gcp":        "{\n  \"type\": \"service_account\",\n  \"project_id\": \"demo\"\n}",
	}
	for _, asString := range []bool{false, true} {
		respond := func(stdout string) HookOutput {
			var resp any = bashEnvelope(stdout, "")
			if asString {
				b, _ := json.Marshal(resp)
				resp = string(b)
			}
			return HandlePostToolUse(ptuResponse("Bash", resp, "./scripts/deploy.sh"))
		}
		t.Run(fmt.Sprintf("control/string=%v", asString), func(t *testing.T) {
			currentCWD, _ = hookEnv(t)
			if isEmpty(respond(strings.Join(filler, "\n"))) || len(storedItems(t)) != 1 {
				t.Error("control output was not stored")
			}
		})
		for name, secret := range secretVals {
			t.Run(fmt.Sprintf("%s/string=%v", name, asString), func(t *testing.T) {
				currentCWD, _ = hookEnv(t)
				lines := append(append(append([]string{}, filler[:150]...), secret), filler[150:]...)
				if r := respond(strings.Join(lines, "\n")); !isEmpty(r) {
					t.Errorf("not skipped: %q", firstLine(r.UpdatedMCPToolOutput))
				}
				if n := len(storedItems(t)); n != 0 {
					t.Errorf("stored %d items", n)
				}
			})
		}
	}
}

func TestPostBashPersistedOutput(t *testing.T) {
	var lines []string
	for i := 1; i <= 3000; i++ {
		lines = append(lines, fmt.Sprintf("persisted row %d of the build log", i))
	}
	full := strings.Join(lines, "\n") + "\nwarning: printed on stderr\n"
	cut := full[:50_000]

	setup := func(t *testing.T) string {
		currentCWD, _ = hookEnv(t)
		fullRetention(t)
		dir := filepath.Join(t.TempDir(), "tool-results")
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "b907gnz67.txt")
		if err := os.WriteFile(path, []byte(full), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	envelope := func(path string, size int) map[string]any {
		e := bashEnvelope(cut, "")
		e["persistedOutputPath"] = path
		e["persistedOutputSize"] = size
		return e
	}
	run := func(t *testing.T, response any) (HookOutput, *db.StoredOutput) {
		r := HandlePostToolUse(ptuResponse("Bash", response, "cat build.log"))
		items := storedItems(t)
		if len(items) == 0 {
			return r, nil
		}
		return r, &items[0]
	}

	for _, asString := range []bool{false, true} {
		t.Run(fmt.Sprintf("sizes, stores and summarises the persisted output/string=%v", asString), func(t *testing.T) {
			path := setup(t)
			var resp any = envelope(path, len(full))
			if asString {
				b, _ := json.Marshal(resp)
				resp = string(b)
			}
			r, row := run(t, resp)
			if row == nil || row.OriginalSize != len(full) || row.FullContent != full {
				t.Fatalf("row %+v", row)
			}
			if !strings.Contains(r.UpdatedMCPToolOutput, "[bash · 3001 lines stdout]") {
				t.Errorf("delivered %q", r.UpdatedMCPToolOutput[:200])
			}
		})
	}
	t.Run("scans the persisted output for secrets", func(t *testing.T) {
		path := setup(t)
		withKey := full + "sk-ant-api03-" + strings.Repeat("A", 95) + "\n"
		os.WriteFile(path, []byte(withKey), 0o644)
		if r, _ := run(t, envelope(path, len(withKey))); !isEmpty(r) {
			t.Error("stored a persisted output holding a key")
		}
	})
	t.Run("falls back to the cut when the file is missing", func(t *testing.T) {
		path := setup(t)
		os.Remove(path)
		if _, row := run(t, envelope(path, len(full))); row == nil || row.OriginalSize != len(cut) {
			t.Errorf("row %+v", row)
		}
	})
	t.Run("ignores a size mismatch", func(t *testing.T) {
		path := setup(t)
		if _, row := run(t, envelope(path, 123)); row == nil || row.OriginalSize != len(cut) {
			t.Errorf("row %+v", row)
		}
	})
	t.Run("ignores a path outside tool-results", func(t *testing.T) {
		setup(t)
		elsewhere := filepath.Join(t.TempDir(), "elsewhere.txt")
		os.WriteFile(elsewhere, []byte(full), 0o644)
		if _, row := run(t, envelope(elsewhere, len(full))); row == nil || row.OriginalSize != len(cut) {
			t.Errorf("row %+v", row)
		}
	})
}
