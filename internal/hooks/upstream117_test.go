// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/hooks/upstream117_test.go
// Port of the upstream v1.17.0 hook vectors: the recorded handler (#341), the
// remote-wrapper fingerprint (#339) and clipped-line retention (#349).

package hooks

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPostRecordsHandler(t *testing.T) {
	t.Run("records the handler that produced the stored summary", func(t *testing.T) {
		currentCWD, _ = hookEnv(t)
		bigDiff := "diff --git a/f b/f\n" + strings.Repeat("+added line\n", 1000)
		HandlePostToolUse(ptuResponse("Bash", bashEnvelope(bigDiff, ""), "git diff HEAD~1"))
		HandlePostToolUse(ptu("mcp__github__list_issues", largeGithub(), nil))
		got := map[string]string{}
		for _, o := range storedItems(t) {
			if o.Handler == nil {
				t.Fatalf("%s stored no handler", o.ToolName)
			}
			got[o.ToolName] = *o.Handler
		}
		if got["Bash"] != "gitDiffHandler" || got["mcp__github__list_issues"] != "githubHandler" {
			t.Errorf("handlers = %v", got)
		}
	})
	t.Run("attributes an empty-summary fallback to genericHandler", func(t *testing.T) {
		currentCWD, _ = hookEnv(t)
		unrelated, _ := json.Marshal(map[string]any{"report": strings.Repeat("quarterly-numbers ", 200)})
		HandlePostToolUse(ptu("mcp__sentry__get_report", string(unrelated), nil))
		items := storedItems(t)
		if len(items) != 1 || items[0].Handler == nil || *items[0].Handler != "genericHandler" {
			t.Errorf("stored %+v", items)
		}
	})
}

func TestPostRemoteFingerprint(t *testing.T) {
	currentCWD, _ = hookEnv(t)
	HandlePostToolUse(ptuResponse("Bash", bashEnvelope(numbered(400, "status line %d"), ""), "ssh -p 22 jupiter 'journalctl -n 400'"))
	items := storedItems(t)
	if len(items) != 1 || items[0].CommandFP == nil || *items[0].CommandFP != "ssh journalctl" {
		t.Fatalf("stored %d items", len(items))
	}
}

func TestPostClippedLineKeepsBody(t *testing.T) {
	// balanced is the default: `make bundle` keeps no body unless a line was
	// clipped, since the marker promises the rest is retrievable.
	currentCWD, _ = hookEnv(t)
	HandlePostToolUse(ptuResponse("Bash", bashEnvelope(strings.Repeat("x", 900_000), ""), "make bundle"))
	items := storedItems(t)
	if len(items) != 1 || items[0].FullRetained != 1 {
		t.Fatalf("stored %+v", len(items))
	}
	if len(items[0].Summary) > 4200 {
		t.Errorf("summary is %d bytes", len(items[0].Summary))
	}
}
