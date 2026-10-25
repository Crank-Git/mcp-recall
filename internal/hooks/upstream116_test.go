// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/hooks/upstream116_test.go
// Port of the upstream v1.16.x hook vectors: header-inclusive sizing and the
// 25% minimum saving (#319), dedup that describes this call (#324), bounded
// reads (#327) and content reads (#332).

package hooks

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"mcprecall/internal/db"
	"mcprecall/internal/projectkey"
)

// runBash feeds one Bash call through the hook; the delivered stdout is
// UpdatedMCPToolOutput, which carries the same text as the replaced stdout.
func runBash(stdout, command string) HookOutput {
	return HandlePostToolUse(ptuResponse("Bash", bashEnvelope(stdout, ""), command))
}

func numbered(n int, format string) string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = fmt.Sprintf(format, i)
	}
	return strings.Join(lines, "\n")
}

// storeCached writes a row as an earlier run would have, so the next call can
// hit it in the dedup cache.
func storeCached(t *testing.T, summary, output, commandFP string) {
	t.Helper()
	database, err := db.Open(os.Getenv("RECALL_DB_PATH"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	hash := db.HashContent(output)
	one := 1
	if _, err := db.StoreOutput(database, db.StoreInput{
		ProjectKey: projectkey.Key(currentCWD), SessionID: sessionID, ToolName: "Bash",
		Summary: summary, FullContent: output, OriginalSize: len(output),
		OutputHash: &hash, FullRetained: &one, CommandFP: &commandFP,
	}); err != nil {
		t.Fatal(err)
	}
}

// ── #319: what Claude receives, header included ──────────────────────────────

const longTestName = "handlePostToolUse — Bash output cut at ~50 KB (#316) > sizes, stores and summarises the persisted output for the"

var grepped = strings.Join([]string{
	"- Expected  - 1", "+ Received  + 2", "(fail) " + longTestName + " object shape [0.11ms]",
	"- Expected  - 1", "+ Received  + 2", "(fail) " + longTestName + " JSON string shape [0.01ms]",
	" 4 pass", " 2 fail",
}, "\n")

func TestPostHeaderInclusiveSize(t *testing.T) {
	t.Run("passes output through when the summary plus its header is not smaller", func(t *testing.T) {
		currentCWD, _ = hookEnv(t)
		if r := runBash(grepped, "bun test | grep fail"); !isEmpty(r) {
			t.Errorf("replaced: %q", r.UpdatedMCPToolOutput)
		}
		if n := len(storedItems(t)); n != 0 {
			t.Errorf("stored %d rows", n)
		}
	})

	t.Run("passes output through when a cached summary plus its header is not smaller", func(t *testing.T) {
		// A row stored before #319 can hold a summary that only beat the
		// original without its header. Replaying it must not grow the output.
		currentCWD, _ = hookEnv(t)
		storeCached(t, grepped[:len(grepped)-40], grepped, "bun test")
		if r := runBash(grepped, "bun test | grep fail"); !isEmpty(r) {
			t.Errorf("replaced: %q", r.UpdatedMCPToolOutput)
		}
	})

	t.Run("records exactly the bytes it delivered", func(t *testing.T) {
		currentCWD, _ = hookEnv(t)
		r := runBash(numbered(600, "probe line %d"), "./scripts/probe.sh")
		items := storedItems(t)
		if len(items) != 1 || items[0].DeliveredSize == nil {
			t.Fatalf("stored %d rows", len(items))
		}
		if got, want := *items[0].DeliveredSize, int64(len(r.UpdatedMCPToolOutput)); got != want {
			t.Errorf("delivered_size = %d, delivered %d bytes", got, want)
		}
	})

	t.Run("never delivers more bytes than the original output", func(t *testing.T) {
		currentCWD, _ = hookEnv(t)
		for _, tc := range []struct{ command, stdout string }{
			{"bun test | grep fail", grepped},
			{"./scripts/probe.sh", numbered(600, "probe line %d")},
		} {
			r := runBash(tc.stdout, tc.command)
			if !isEmpty(r) && len(r.UpdatedMCPToolOutput) >= len(tc.stdout) {
				t.Errorf("%q: delivered %d of %d bytes", tc.command, len(r.UpdatedMCPToolOutput), len(tc.stdout))
			}
		}
		// Control: the long output is replaced, so the check above ran.
		if n := len(storedItems(t)); n != 1 {
			t.Errorf("stored %d rows, want 1", n)
		}
	})
}

// ── #319 part 2: a summary must save at least a quarter ──────────────────────

func TestPostMinimumSaving(t *testing.T) {
	t.Run("savesEnough accepts a 25% saving and rejects less", func(t *testing.T) {
		for _, tc := range []struct {
			delivered, original int
			want                bool
		}{{750, 1000, true}, {751, 1000, false}, {1000, 1000, false}, {0, 0, false}} {
			if got := savesEnough(tc.delivered, tc.original); got != tc.want {
				t.Errorf("savesEnough(%d, %d) = %v", tc.delivered, tc.original, got)
			}
		}
	})

	cachedRun := func(t *testing.T, summaryShare float64) HookOutput {
		currentCWD, _ = hookEnv(t)
		output := numbered(70, "(pass) suite > case number %d with a reasonably long name")
		storeCached(t, output[:int(float64(len(output))*summaryShare+0.5)], output, "bun test")
		return runBash(output, "bun test | grep pass")
	}

	t.Run("passes output through when a cached summary saves under 25%", func(t *testing.T) {
		if r := cachedRun(t, 0.85); !isEmpty(r) {
			t.Errorf("replaced: %q", firstLine(r.UpdatedMCPToolOutput))
		}
	})

	t.Run("still serves a cached summary that saves more", func(t *testing.T) {
		if r := cachedRun(t, 0.5); !strings.Contains(r.UpdatedMCPToolOutput, "· cached ·") {
			t.Errorf("got %q", firstLine(r.UpdatedMCPToolOutput))
		}
	})

	t.Run("never delivers a fresh summary that saves under 25%", func(t *testing.T) {
		for n := 26; n <= 70; n++ {
			currentCWD, _ = hookEnv(t)
			stdout := numbered(n, "issue %d\tP2: Medium,S,admin\tA title describing what this item asks for")
			r := runBash(stdout, "gh issue list --limit 70")
			if !isEmpty(r) && float64(len(r.UpdatedMCPToolOutput)) > float64(len(stdout))*0.75 {
				t.Errorf("n=%d: delivered %d of %d bytes", n, len(r.UpdatedMCPToolOutput), len(stdout))
			}
		}
	})
}

// ── #324: a cached summary must describe this call ───────────────────────────

var recallIDRe = regexp.MustCompile(`\[recall:(recall_[0-9a-f]{16})`)

func TestPostCacheDescribesThisCall(t *testing.T) {
	passing := numbered(300, "(pass) suite > case number %d [0.10ms]")

	t.Run("re-running a command after its output changed delivers the new result", func(t *testing.T) {
		currentCWD, _ = hookEnv(t)
		failing := passing + "\n(fail) suite > the broken one [0.2ms]\n\n 300 pass\n 1 fail\nRan 301 tests across 1 file. [1.00s]"
		fixed := passing + "\n(pass) suite > the broken one [0.2ms]\n\n 301 pass\n 0 fail\nRan 301 tests across 1 file. [1.00s]"
		if first := runBash(failing, "bun test").UpdatedMCPToolOutput; !strings.Contains(first, "1 failed") {
			t.Fatalf("first run: %q", first)
		}
		second := runBash(fixed, "bun test").UpdatedMCPToolOutput
		if strings.Contains(second, "· cached ·") || strings.Contains(second, "failed") {
			t.Errorf("second run: %q", second)
		}
	})

	t.Run("re-running with unchanged output still hits the cache", func(t *testing.T) {
		currentCWD, _ = hookEnv(t)
		output := passing + "\n\n 300 pass\n 0 fail"
		m := recallIDRe.FindStringSubmatch(runBash(output, "bun test").UpdatedMCPToolOutput)
		if m == nil {
			t.Fatal("first run not stored")
		}
		if second := runBash(output, "bun test").UpdatedMCPToolOutput; !strings.Contains(second, "[recall:"+m[1]+" · cached ·") {
			t.Errorf("second run: %q", firstLine(second))
		}
	})

	t.Run("identical output from another command family is summarised for this command", func(t *testing.T) {
		currentCWD, _ = hookEnv(t)
		fatal := "fatal: ambiguous argument 'a-long-enough-missing-ref-name-to-be-worth-summarising-here': unknown revision or path not in the working tree.\nUse '--' to separate paths from revisions, like this:\n'git <command> [<revision>...] -- [<file>...]'"
		run := func(command string) HookOutput {
			return HandlePostToolUse(ptuResponse("Bash", bashEnvelope("", fatal), command))
		}
		run("git diff missing | head -50")
		if text := run("git log missing --oneline | head").UpdatedMCPToolOutput; strings.Contains(text, "git diff") {
			t.Errorf("served another command's summary: %q", text)
		}
	})

	t.Run("identical output from another MCP tool is not served from its cache", func(t *testing.T) {
		currentCWD, _ = hookEnv(t)
		HandlePostToolUse(ptu("mcp__github__list_issues", largeGithub(), nil))
		if second := HandlePostToolUse(ptu("mcp__github__search_issues", largeGithub(), nil)); strings.Contains(second.UpdatedMCPToolOutput, "· cached · ") {
			t.Errorf("served another tool's cache: %q", firstLine(second.UpdatedMCPToolOutput))
		}
	})
}

// ── #327: a read the command bounded itself ──────────────────────────────────

func TestPostBoundedRead(t *testing.T) {
	// 60 lines of grep context (~3.5 KB): no `file:line:` shape, so the shell
	// handler would cut it.
	context := numbered(60, "    const field%[1]d = this.compute(input, %[1]d); // keep line %[1]d")

	t.Run("summarises the same output when the command set no bound (control)", func(t *testing.T) {
		currentCWD, _ = hookEnv(t)
		if isEmpty(runBash(context, "grep -rn compute src")) || len(storedItems(t)) != 1 {
			t.Error("control output was not summarised")
		}
	})

	t.Run("delivers bounded output whole and stores nothing", func(t *testing.T) {
		currentCWD, _ = hookEnv(t)
		for _, command := range []string{
			`grep -n "class X" -A 30 src/x.ts | head -40`,
			"grep -n foo src/x.ts | head -3; sed -n 900,1000p src/x.ts",
			"tail -n 60 build.log",
		} {
			if r := runBash(context, command); !isEmpty(r) {
				t.Errorf("%q was replaced", command)
			}
		}
		if n := len(storedItems(t)); n != 0 {
			t.Errorf("stored %d rows", n)
		}
	})

	t.Run("does not serve a bounded read from the cache", func(t *testing.T) {
		currentCWD, _ = hookEnv(t)
		runBash(context, "grep -rn compute src")
		if r := runBash(context, "grep -rn compute src | head -60"); !isEmpty(r) {
			t.Errorf("replaced: %q", firstLine(r.UpdatedMCPToolOutput))
		}
	})

	t.Run("still summarises bounded output above the size floor", func(t *testing.T) {
		currentCWD, _ = hookEnv(t)
		large := numbered(400, "    const field%[1]d = this.compute(input, %[1]d); // keep line %[1]d")
		if len(large) <= 8*1024 {
			t.Fatalf("fixture is %d bytes", len(large))
		}
		if isEmpty(runBash(large, "tail -n 400 build.log")) || len(storedItems(t)) != 1 {
			t.Error("large bounded output was not summarised")
		}
	})
}

// ── #332: reads of file and diff content ─────────────────────────────────────

func TestPostContentRead(t *testing.T) {
	doc := func(n int) string {
		return numbered(n, "Line %d of the document, with enough words to be worth reading.")
	}
	diff := func(n int) string {
		return strings.Join([]string{
			"diff --git a/src/x.sql b/src/x.sql", "index 1b17b9b..2e31c17 100644", "--- a/src/x.sql", "+++ b/src/x.sql",
			fmt.Sprintf("@@ -45,6 +45,%d @@", n),
			numbered(n, "+-- added comment line %d explaining why the column marks retired items"),
		}, "\n")
	}

	t.Run("delivers a small file or diff whole and stores nothing", func(t *testing.T) {
		currentCWD, _ = hookEnv(t)
		if !isEmpty(runBash(doc(54), "cat docs/notes.md")) || !isEmpty(runBash(diff(6), "git diff b03279c..b1548cd -- src/x.sql")) {
			t.Error("a small content read was replaced")
		}
		if n := len(storedItems(t)); n != 0 {
			t.Errorf("stored %d rows", n)
		}
	})

	t.Run("keeps the body of a large content read under balanced retention", func(t *testing.T) {
		currentCWD, _ = hookEnv(t)
		body := doc(400)
		if isEmpty(runBash(body, "cat docs/notes.md")) || isEmpty(runBash(diff(300), "git diff main")) {
			t.Fatal("a large content read was not summarised")
		}
		items := storedItems(t)
		if len(items) != 2 {
			t.Fatalf("stored %d rows", len(items))
		}
		for _, it := range items {
			if it.FullRetained != 1 {
				t.Errorf("%s: full_retained = 0", it.Summary[:20])
			}
			if strings.HasPrefix(it.FullContent, "Line 0") && it.FullContent != body {
				t.Error("cat body not kept verbatim")
			}
		}
	})

	t.Run("still stores other reproducible Bash summary-only (control)", func(t *testing.T) {
		currentCWD, _ = hookEnv(t)
		log := numbered(400, "abc%04[1]d fix: change number %[1]d to the parser")
		if isEmpty(runBash(log, "git log --oneline")) {
			t.Fatal("git log was not summarised")
		}
		if items := storedItems(t); len(items) != 1 || items[0].FullRetained != 0 {
			t.Errorf("git log should be summary-only: %+v", items)
		}
	})
}
