// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/handlers/upstream117_test.go
// Port of the upstream v1.17.0 handler vectors: remote wrappers (#339),
// collapsed repeats (#340), the line-width cap (#349) and rtk (#353).

package handlers

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// ── #349: line-width cap ──────────────────────────────────────────────────────

var huge = strings.Repeat("x", 900_000)

func TestClipLine(t *testing.T) {
	t.Run("leaves a line at the cap unchanged", func(t *testing.T) {
		line := strings.Repeat("a", MaxLineChars)
		if clipLine(line, MaxLineChars) != line || HasClippedLine(line) {
			t.Error("a line at the cap was clipped")
		}
	})
	t.Run("cuts a wide line to the cap and states the elided char count", func(t *testing.T) {
		clipped := clipLine(huge, MaxLineChars)
		want := fmt.Sprintf("%s… (+%d chars)", strings.Repeat("x", MaxLineChars), 900_000-MaxLineChars)
		if clipped != want {
			t.Errorf("clipped to %d bytes", len(clipped))
		}
		if !HasClippedLine("header\n" + clipped + "\ntail") {
			t.Error("marker not detected")
		}
	})
	t.Run("never splits a surrogate pair at the cut", func(t *testing.T) {
		line := strings.Repeat("a", 9) + "😀" + strings.Repeat("b", 20)
		clipped := clipLine(line, 10)
		if !strings.HasPrefix(clipped, strings.Repeat("a", 9)+"…") {
			t.Errorf("clipped = %q", clipped)
		}
		// 31 UTF-16 units, as upstream counts them; 9 kept.
		if !strings.Contains(clipped, "(+22 chars)") {
			t.Errorf("clipped = %q", clipped)
		}
	})
	t.Run("clips a run of wide repeats before its count, so the count survives", func(t *testing.T) {
		run := collapseRepeats([]string{huge, huge, huge}, noLimit, MaxLineChars)[0]
		if run.count != 3 || !strings.HasSuffix(run.text, " chars)  (×3)") {
			t.Errorf("run = %d, %q", run.count, run.text[len(run.text)-30:])
		}
	})
}

func TestHugeLineSmallSummary(t *testing.T) {
	for _, tc := range []struct{ label, command, stdout string }{
		{"shell", "make bundle", huge},
		{"grep piped to head", "grep -n sourceMappingURL dist/app.min.js | head", "1:" + huge},
		{"git grep", "git grep foo", huge},
		{"compound", "ls dist; cat dist/app.min.js", "app.min.js\n" + huge},
	} {
		t.Run("clips it on "+tc.label, func(t *testing.T) {
			s := GetBashHandler(cmdInput(t, tc.command))("Bash", bashObj(tc.stdout, "")).Summary
			if len(s) >= MaxLineChars+200 || !HasClippedLine(s) {
				t.Errorf("summary %d bytes, clipped=%v", len(s), HasClippedLine(s))
			}
		})
	}
	t.Run("clips it in the generic fallback's line window", func(t *testing.T) {
		lines := []string{huge}
		for i := 0; i < 20; i++ {
			lines = append(lines, fmt.Sprintf("line %d", i))
		}
		s := GenericHandler("mcp__x__y", strings.Join(lines, "\n")).Summary
		if len(s) >= MaxLineChars+400 || !HasClippedLine(s) {
			t.Errorf("summary %d bytes", len(s))
		}
	})
	t.Run("leaves a summary with no wide line byte-identical (control)", func(t *testing.T) {
		rows := make([]string, 40)
		for i := range rows {
			rows[i] = fmt.Sprintf("row %d", i)
		}
		stdout := strings.Join(rows, "\n")
		if s := GetBashHandler(cmdInput(t, "ls dist; cat notes"))("Bash", bashObj(stdout, "")).Summary; s != stdout {
			t.Errorf("summary = %q", s)
		}
	})
}

// ── #340: collapse repeated lines ─────────────────────────────────────────────

func TestRepeatKey(t *testing.T) {
	t.Run("masks timestamps, pids, uuids, hex ids and durations", func(t *testing.T) {
		for in, want := range map[string]string{
			"Oct 19 12:00:01 jupiter sshd[812]: retry in 1.5s":             "<ts> jupiter sshd[<pid>]: retry in <dur>",
			"2026-10-19T12:00:01.123Z job 3f9a2c1e7b failed":               "<ts> job <hex> failed",
			"ERROR 12:00:01 req 123e4567-e89b-12d3-a456-426614174000 done": "ERROR <ts> req <uuid> done",
		} {
			if got := repeatKey(in); got != want {
				t.Errorf("repeatKey(%q) = %q, want %q", in, got, want)
			}
		}
	})
	t.Run("keeps log lines apart that differ in a path, status code, counter or word", func(t *testing.T) {
		for _, p := range [][2]string{
			{"GET /api/a 200", "GET /api/b 200"},
			{"GET /api/a 200", "GET /api/a 500"},
			{"retry 1 failed", "retry 2 failed"},
			{"open /var/log/a.log: denied", "open /var/log/b.log: denied"},
			{"key deadbeef", "key cafebabe"},
			{"exit 1", "exit 2"},
		} {
			if repeatKey("12:00:01 "+p[0]) == repeatKey("12:00:02 "+p[1]) {
				t.Errorf("%q and %q collapsed", p[0], p[1])
			}
		}
	})
	t.Run("does not mask a line that does not open with a timestamp", func(t *testing.T) {
		slot := func(ts string) string {
			return `  "s": "2026-09-26 | 2026-09-26T` + ts + `:00.000Z | status=open | cap=10 booked=0"`
		}
		row := func(from, to string) string {
			return "2026-09-26 | " + from + ":00 | " + to + ":00 | closed | 10 | 0 | 0"
		}
		for _, p := range [][2]string{
			{slot("14:00"), slot("15:30")},
			{row("11:00", "11:30"), row("11:30", "12:00")},
			{"3f9a2c1e7b wip", "8d0e4b6a2f wip"},
		} {
			if repeatKey(p[0]) == repeatKey(p[1]) {
				t.Errorf("%q and %q collapsed", p[0], p[1])
			}
		}
	})
}

func TestCollapseRepeats(t *testing.T) {
	for _, tc := range []struct {
		name      string
		in, wants []string
	}{
		{"collapses an identical run with a count", []string{"a", "b", "b", "b", "c"}, []string{"a", "b  (×3)", "c"}},
		{"collapses a run that differs only in timestamps and shows the last",
			[]string{"12:00:01 retry failed", "12:00:02 retry failed", "12:03:20 retry failed"},
			[]string{"12:00:01 retry failed  (×3, until 12:03:20)"}},
		{"says ids vary when a run has no timestamp to show",
			[]string{"12:00:01 worker[1] up", "12:00:01 worker[2] up"}, []string{"12:00:01 worker[1] up  (×2, varying ids)"}},
		{"leaves interleaved repeats and blank lines in order",
			[]string{"a", "b", "a", "b", "", "", "c"}, []string{"a", "b", "a", "b", "", "", "c"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := collapsedTexts(tc.in); strings.Join(got, "\n") != strings.Join(tc.wants, "\n") {
				t.Errorf("got %q", got)
			}
		})
	}
	t.Run("records how many input lines each summary line stands for", func(t *testing.T) {
		got := collapseRepeats([]string{"x", "x", "y"}, noLimit, noLimit)
		if len(got) != 2 || got[0].count != 2 || got[1].count != 1 {
			t.Errorf("got %+v", got)
		}
	})
}

func TestFallbacksCollapseRepeats(t *testing.T) {
	var journal []string
	for i := 0; i < 200; i++ {
		journal = append(journal, fmt.Sprintf("Oct 19 12:%02d:%02d jupiter app[812]: connection reset, retrying", i/60, i%60))
	}
	for i := 0; i < 299; i++ {
		journal = append(journal, "Oct 19 12:05:00 jupiter app[812]: queue depth 0")
	}
	journal = append(journal, "Oct 19 12:05:01 jupiter app[812]: FATAL: disk full on /var")

	t.Run("shows all three distinct messages of a 500-line log with counts", func(t *testing.T) {
		s := shellHandler("Bash", bashObj(strings.Join(journal, "\n"), "")).Summary
		for _, want := range []string{
			"[bash · 500 lines stdout]", "connection reset, retrying  (×200, until Oct 19 12:03:19)",
			"queue depth 0  (×299)", "FATAL: disk full on /var",
		} {
			if !strings.Contains(s, want) {
				t.Errorf("missing %q in %q", want, s)
			}
		}
		if strings.Contains(s, "more lines") {
			t.Errorf("overflow in %q", s)
		}
	})
	t.Run("counts the overflow in input lines, not summary lines", func(t *testing.T) {
		var lines []string
		for i := 0; i < 10; i++ {
			lines = append(lines, "same")
		}
		for i := 0; i < 40; i++ {
			lines = append(lines, fmt.Sprintf("line %d", i))
		}
		s := shellHandler("Bash", strings.Join(lines, "\n")).Summary
		for _, want := range []string{"same  (×10)", "line 23", "… (+16 more lines)"} {
			if !strings.Contains(s, want) {
				t.Errorf("missing %q", want)
			}
		}
		if strings.Contains(s, "line 24") {
			t.Error("kept line 24")
		}
	})
	t.Run("summarizes a collapsed repetitive output in the generic handler, not a raw window", func(t *testing.T) {
		lines := make([]string, 500)
		for i := range lines {
			lines[i] = fmt.Sprintf("2026-10-19T12:00:%02dZ poll ok", i%60)
		}
		want := "2026-10-19T12:00:00Z poll ok  (×500, until 2026-10-19T12:00:19Z)"
		if s := GenericHandler("mcp__x__logs", strings.Join(lines, "\n")).Summary; s != want {
			t.Errorf("summary = %q", s)
		}
	})
}

// ── #339: commands run elsewhere ──────────────────────────────────────────────

func TestRemoteWrappers(t *testing.T) {
	for _, tc := range []struct {
		command, inner string
		handler        Handler
	}{
		{"ssh h 'git status'", "git status", gitStatusHandler},
		{`ssh -p 22 h "docker ps"`, "docker ps", dockerPsHandler},
		{"ssh -p22 h git status", "git status", gitStatusHandler},
		{"ssh -p22 -tt -o StrictHostKeyChecking=no root@10.0.0.5 git log --stat", "git log --stat", gitLogHandler},
		{"ssh -i ~/.ssh/k -J bastion h 'rg foo src'", "rg foo src", grepHandler},
		{"ssh h 'cd /x && git diff'", "git diff", gitDiffHandler},
		{"pct exec 108 -- git status", "git status", gitStatusHandler},
		{"pct exec 108 git log", "git log", gitLogHandler},
		{"pct exec 108 -- bash -c 'git diff HEAD'", "git diff HEAD", gitDiffHandler},
		{"ssh jupiter pct exec 108 -- ls -la /etc", "ls -la /etc", lsHandler},
		{"docker exec -it -u root -w /app web find . -name x", "find . -name x", findHandler},
		{"docker exec --user=root web git status", "git status", gitStatusHandler},
		{"bash -lc 'grep -rn foo src'", "grep -rn foo src", grepHandler},
		{`sh -c "git log \"--format=%h\""`, `git log "--format=%h"`, gitLogHandler},
		{"timeout 30 ssh h 'git status'", "git status", gitStatusHandler},
	} {
		if got := NormalizeCommand(tc.command); got != tc.inner {
			t.Errorf("NormalizeCommand(%q) = %q, want %q", tc.command, got, tc.inner)
		}
		if h := GetBashHandler(cmdInput(t, tc.command)); !samePtr(h, tc.handler) {
			t.Errorf("%q routed to %s", tc.command, HandlerName(h))
		}
	}

	t.Run("routes a remote compound command to the compound handler", func(t *testing.T) {
		if h := GetBashHandler(cmdInput(t, "ssh h 'git status; uptime'")); !samePtr(h, compoundHandler) {
			t.Errorf("routed to %s", HandlerName(h))
		}
	})
	t.Run("keeps a local pipeline after the quoted remote command", func(t *testing.T) {
		if got := NormalizeCommand("ssh h 'journalctl -n 500' | grep err"); got != "journalctl -n 500 | grep err" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("leaves ambiguous or commandless shapes as they are", func(t *testing.T) {
		for _, command := range []string{
			"ssh h", "ssh -N -L 8080:x:80 h", "ssh -N h sleep 10", "ssh -Z h ls", `ssh -o "A B" h ls`, "ssh h 'unterminated",
			`ssh h 'it'\''s'`, "ssh h 'a' b", "ssh -p", "pct exec", "pct exec abc -- ls", "pct list",
			"docker exec web", "docker exec --rm web ls", "bash script.sh", "bash -c", "sh -c rg",
		} {
			if got := NormalizeCommand(command); got != command {
				t.Errorf("NormalizeCommand(%q) = %q", command, got)
			}
		}
	})
	t.Run("fingerprints the remote wrapper and the command, never the host or id", func(t *testing.T) {
		for command, want := range map[string]string{
			"ssh -p 22 jupiter 'git status'":        "ssh git status",
			"pct exec 108 -- journalctl -n 200":     "pct exec journalctl",
			"ssh jupiter pct exec 108 -- docker ps": "ssh pct exec docker ps",
			"docker exec web ls":                    "docker exec ls",
			"bash -c 'rg foo'":                      "rg", // runs locally
			"timeout 30 git diff":                   "git diff",
			"ssh jupiter":                           "ssh",
		} {
			if got := BashFingerprint(command); got != want {
				t.Errorf("BashFingerprint(%q) = %q, want %q", command, got, want)
			}
		}
	})
	t.Run("sees a content read inside a remote command", func(t *testing.T) {
		for command, want := range map[string]bool{
			"ssh h 'cat /etc/hosts'":                   true,
			"ssh h 'journalctl -u x | tail -50'":       true,
			"pct exec 108 -- sed -n 1,40p /etc/x.conf": true,
			"ssh h 'docker ps'":                        false,
		} {
			if got := IsContentRead(command); got != want {
				t.Errorf("IsContentRead(%q) = %v", command, got)
			}
		}
	})
	t.Run("still sees a read that no unwrap reaches", func(t *testing.T) {
		for _, command := range []string{
			"sed -n '70,115p;255,300p' src/router.py",
			"cd /x && grep -n '' f.ts | sed -n '85,175p;285,345p'",
			"for h in a b; do ssh $h 'journalctl -n 500 | tail -20'; done",
			`ssh h 'pct exec 165 -- runuser -u r -- bash -c "free -g | head -2"'`,
		} {
			if !IsBoundedRead(command) {
				t.Errorf("IsBoundedRead(%q) = false", command)
			}
		}
	})
	t.Run("accepts reading a quoted pipe as a read, capped at the passthrough size", func(t *testing.T) {
		if !IsContentRead(`echo "pipe it | head -5" > notes.txt`) {
			t.Error("not a read")
		}
	})
}

// ── #353: commands rewritten by rtk ───────────────────────────────────────────

var rtkMatches = func() []string {
	out := make([]string, 200)
	for i := range out {
		out[i] = fmt.Sprintf("src/m%d.ts:%d:export function f%d() {}", i%20, i+1, i)
	}
	return out
}()

func TestRecoveryHints(t *testing.T) {
	got := recoveryHints([]string{
		"plain line",
		"+21 more files [+135 hidden: rtk recall 207c077f54e0]",
		"[full output: rtk recall 3f9a1c2b]",
		"[full diff: rtk git diff --no-compact]",
		"run rtk recall later", // no hash: not a hint
	})
	want := []string{
		"+21 more files [+135 hidden: rtk recall 207c077f54e0]",
		"[full output: rtk recall 3f9a1c2b]",
		"[full diff: rtk git diff --no-compact]",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got %q", got)
	}
}

func TestRtkSummariesKeepHints(t *testing.T) {
	rtkGrep := strings.Join(append(append([]string{}, rtkMatches...),
		"  +3 more in src/import/index.ts [+3 hidden: rtk recall 18f9a13f3086]",
		"+21 more files [+135 hidden: rtk recall 207c077f54e0]"), "\n")

	t.Run("keeps hints the shell fallback's head would cut", func(t *testing.T) {
		s := GetBashHandler(cmdInput(t, "rtk grep -rn export src"))("Bash", bashObj(rtkGrep, "")).Summary
		if !strings.Contains(s, "… (+177 more lines)") || !strings.Contains(s, "[+3 hidden: rtk recall 18f9a13f3086]") {
			t.Errorf("summary = %q", s)
		}
		if !strings.HasSuffix(strings.TrimRight(s, " \n"), "+21 more files [+135 hidden: rtk recall 207c077f54e0]") {
			t.Errorf("summary ends %q", s[len(s)-80:])
		}
	})
	t.Run("keeps a hint from the middle of a compound command's window", func(t *testing.T) {
		lines := append(append(append([]string{}, rtkMatches[:40]...), "[full output: rtk recall 3f9a1c2b]"), rtkMatches[40:80]...)
		s := GetBashHandler(cmdInput(t, "rtk git status; rtk grep -rn x src"))("Bash", bashObj(strings.Join(lines, "\n"), "")).Summary
		if !strings.Contains(s, "middle lines elided") || !strings.Contains(s, "[full output: rtk recall 3f9a1c2b]") {
			t.Errorf("summary = %q", s)
		}
	})
	t.Run("adds nothing to a summary with no hints (control)", func(t *testing.T) {
		s := GetBashHandler(cmdInput(t, "rtk grep -rn export src"))("Bash", bashObj(strings.Join(rtkMatches, "\n"), "")).Summary
		if strings.Contains(s, "rtk recall") || !strings.HasSuffix(strings.TrimRight(s, " \n"), "… (+175 more lines)") {
			t.Errorf("summary = %q", s)
		}
	})
}

func TestRtkCommands(t *testing.T) {
	rtkDiff := strings.Join([]string{
		" src/handlers/bash.ts | 12 +++++++-----", " src/handlers/shell.ts |  4 ++--", " 2 files changed, 10 insertions(+), 6 deletions(-)",
		"", "Changes:", "", "src/handlers/bash.ts", "@@ -740,6 +740,9 @@ export function getBashHandler", "-  const command = normalizeCommand(rawCommand);",
		"+  const { command, wrappers } = unwrapCommand(rawCommand);", "  +7 -5", "", "src/handlers/shell.ts", "@@ -64,6 +64,7 @@", "+  const hints = 1;", "  +3 -1",
	}, "\n")

	t.Run("fingerprints rtk and the command it runs", func(t *testing.T) {
		for command, want := range map[string]string{
			"rtk git diff HEAD~3":                 "rtk git diff",
			"cd src && rtk git diff":              "rtk git diff",
			"timeout 30 rtk git log -5":           "rtk git log",
			"rtk ssh anubis 'git status'":         "rtk ssh git status",
			"rtk read src/cli.ts --head-lines 40": "rtk read",
			"rtk recall 18f9a13f3086":             "rtk recall",
			"rtk -v cargo test":                   "rtk cargo test",
		} {
			if got := BashFingerprint(command); got != want {
				t.Errorf("BashFingerprint(%q) = %q, want %q", command, got, want)
			}
		}
	})
	t.Run("treats rtk's reads and the reads it runs as content reads", func(t *testing.T) {
		for command, want := range map[string]bool{
			"rtk read src/handlers/collapse.ts":   true,
			"rtk read src/cli.ts --tail-lines 20": true,
			"rtk recall 18f9a13f3086 --tail 50":   true,
			"rtk git diff HEAD~3":                 true,
			"rtk git status":                      false,
		} {
			if got := IsContentRead(command); got != want {
				t.Errorf("IsContentRead(%q) = %v", command, got)
			}
		}
	})
	t.Run("routes rtk output to the shell fallback, never a parser for the raw command", func(t *testing.T) {
		for _, command := range []string{
			"rtk git diff HEAD~3", "rtk git log -5", "rtk git status", "rtk grep -rn foo src", "rtk ls -la",
			"rtk ssh h 'docker ps'", "cd src && rtk git diff",
		} {
			if h := GetBashHandler(cmdInput(t, command)); !samePtr(h, shellHandler) {
				t.Errorf("%q routed to %s", command, HandlerName(h))
			}
		}
		if h := GetBashHandler(cmdInput(t, "rtk git status; rtk git diff")); !samePtr(h, compoundHandler) {
			t.Errorf("compound routed to %s", HandlerName(h))
		}
	})
	t.Run("claims no count for rtk's header-stripped diff", func(t *testing.T) {
		out := bashObj(rtkDiff, "")
		s := GetBashHandler(cmdInput(t, "rtk git diff HEAD~3"))("Bash", out).Summary
		if s != shellHandler("Bash", out).Summary || strings.HasPrefix(s, "git diff —") {
			t.Errorf("summary = %q", s)
		}
	})
	t.Run("claims no count for rtk's elided grep, which grepHandler would miscount", func(t *testing.T) {
		shown := make([]string, 200)
		for i := range shown {
			shown[i] = fmt.Sprintf("src/m%d.ts:%d:export const f%d = 1;", i%43, i+1, i)
		}
		stdout := strings.Join(append(append([]string{"338 matches in 63 files:", ""}, shown...),
			"+20 more files [+138 hidden: rtk recall 207c077f54e0]"), "\n")
		out := bashObj(stdout, "")
		if !regexp.MustCompile(`^grep — 200 matches in 43 files`).MatchString(grepHandler("Bash", out).Summary) {
			t.Fatal("grepHandler no longer miscounts; the control is stale")
		}
		s := GetBashHandler(cmdInput(t, "rtk grep -rn export src"))("Bash", out).Summary
		if strings.HasPrefix(s, "grep —") || !strings.Contains(s, "338 matches in 63 files:") {
			t.Errorf("summary = %q", s[:200])
		}
	})
	t.Run("leaves a command that only mentions rtk alone", func(t *testing.T) {
		if NormalizeCommand("rtk") != "rtk" || NormalizeCommand("rtkx git diff") != "rtkx git diff" {
			t.Error("unwrapped a non-rtk command")
		}
		if h := GetBashHandler(cmdInput(t, "git diff")); !samePtr(h, gitDiffHandler) {
			t.Errorf("routed to %s", HandlerName(h))
		}
	})
}
