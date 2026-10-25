// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/handlers/upstream115_test.go
// Port of the upstream v1.15.x handler vectors: wrapper unwrapping (#302),
// compound commands and test-runner summaries (#308), and Bash sizing (#306).

package handlers

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"mcprecall/internal/jsonx"
)

func TestWrapperUnwrapRouting(t *testing.T) {
	prefixes := []string{
		"timeout 30 ",
		"timeout 2.5m ",
		"timeout -s KILL 30 ",
		"timeout --kill-after=5 --preserve-status 30s ",
		"time ",
		"time -p ",
		"nice ",
		"nice -n 10 ",
		"env ",
		"env FOO=1 BAR=x ",
		"FOO=1 ",
		`FOO="a b" BAR='c d' `,
		"sudo ",
		"doas ",
		"cd /repo && timeout 60 ",
		"timeout 60 env CI=1 ",
	}
	targets := []struct {
		command string
		want    Handler
	}{
		{"grep -rn foo src/", grepHandler},
		{"git diff HEAD", gitDiffHandler},
		{"git log --stat", gitLogHandler},
	}
	for _, p := range prefixes {
		for _, tc := range targets {
			if got := GetBashHandler(cmdInput(t, p+tc.command)); !samePtr(got, tc.want) {
				t.Errorf("GetBashHandler(%q) routed to %s", p+tc.command, HandlerName(got))
			}
			if got := NormalizeCommand(p + tc.command); got != tc.command {
				t.Errorf("NormalizeCommand(%q) = %q", p+tc.command, got)
			}
		}
	}
}

func TestWrapperUnwrapFingerprint(t *testing.T) {
	if got := CommandFingerprint(NormalizeCommand("timeout 30 git diff")); got != "git diff" {
		t.Errorf("got %q", got)
	}
	if got := CommandFingerprint(NormalizeCommand("env CI=1 bun test")); got != "bun test" {
		t.Errorf("got %q", got)
	}
}

func TestWrapperLeftAlone(t *testing.T) {
	for _, c := range []string{"timeout 30", "env", "env | grep PATH", "time", "nice", "FOO=1", "sudo -u www git diff"} {
		if got := NormalizeCommand(c); got != c {
			t.Errorf("NormalizeCommand(%q) = %q, want unchanged", c, got)
		}
	}
}

func TestWrapperUnterminatedAssignmentFast(t *testing.T) {
	input := strings.Repeat("A=1 ", 200) + `B="unterminated`
	t0 := time.Now()
	NormalizeCommand(input)
	if d := time.Since(t0); d > 250*time.Millisecond {
		t.Errorf("took %v", d)
	}
}

func TestCompoundRouting(t *testing.T) {
	for _, c := range []string{
		"ls -la .claude/; git check-ignore -v x; git diff | wc -c",
		"grep -n a f && grep -n b g",
		"bun test x | tail -4; bun run typecheck | tail -3",
		"git diff\ngit status",
		"make || echo failed",
		"cd /repo && git diff; git status",
	} {
		if got := GetBashHandler(cmdInput(t, c)); !samePtr(got, compoundHandler) {
			t.Errorf("GetBashHandler(%q) routed to %s, want compoundHandler", c, HandlerName(got))
		}
	}
	notCompound := []struct {
		command string
		want    Handler
	}{
		{"cd /repo && git diff", gitDiffHandler},
		{"git log --oneline | head -5", gitLogHandler},
		{"grep 'a; b' file", grepHandler},
		{`grep "a && b" file`, grepHandler},
		{"grep -rn $(echo a; echo b) src", grepHandler},
		{`grep -rn a\;b src`, grepHandler},
		{"git diff HEAD;", gitDiffHandler},
		{"git diff 2>&1 | tail -20", gitDiffHandler},
	}
	for _, tc := range notCompound {
		if got := GetBashHandler(cmdInput(t, tc.command)); !samePtr(got, tc.want) {
			t.Errorf("GetBashHandler(%q) routed to %s", tc.command, HandlerName(got))
		}
	}
}

func bashObj(stdout, stderr string) *jsonx.Obj {
	o := jsonx.NewObj()
	o.Set("stdout", stdout)
	o.Set("stderr", stderr)
	return o
}

func TestCompoundHandler(t *testing.T) {
	t.Run("passes a modest multi-command output through whole", func(t *testing.T) {
		var parts []string
		for i := 0; i < 20; i++ {
			parts = append(parts, fmt.Sprintf("== section %d\nresult line for section %d", i, i))
		}
		text := strings.Join(parts, "\n")
		res := compoundHandler("Bash", bashObj(text, ""))
		if res.Summary != text || res.OriginalSize != len(text) {
			t.Errorf("summary changed or size %d != %d", res.OriginalSize, len(text))
		}
	})
	t.Run("keeps head, tail and middle errors of a long one", func(t *testing.T) {
		lines := make([]string, 300)
		for i := range lines {
			lines[i] = fmt.Sprintf("line %d", i)
		}
		lines[150] = "error: build failed in the middle"
		s := compoundHandler("Bash", bashObj(strings.Join(lines, "\n"), "")).Summary
		for _, want := range []string{"line 0", "line 29", "line 299", "line 285", "error: build failed in the middle"} {
			if !strings.Contains(s, want) {
				t.Errorf("missing %q", want)
			}
		}
		if strings.Contains(s, "line 100\n") {
			t.Error("kept an elided middle line")
		}
	})
}

func TestTestRunnerKeepsWhatAFailingRunNeeds(t *testing.T) {
	bunTestFail := strings.Join([]string{
		"bun test v1.3.14 (0d9b296a)",
		"",
		"tests/hooks.test.ts:",
		"851 |       expect(run(shape).row?.full_content).toBe(text);",
		"                                                 ^",
		"error: expect(received).toBe(expected)",
		"",
		`Expected: "shape value 0"`,
		`Received: ""`,
		"",
		"      at <anonymous> (tests/hooks.test.ts:851:44)",
		"(fail) Bash envelope > stores stdout + stderr for the object shape [2.26ms]",
		"",
		" 4 pass",
		" 54 filtered out",
		" 1 fail",
		" 12 expect() calls",
		"Ran 5 tests across 1 file. [68.00ms]",
	}, "\n")
	run := func(stdout, stderr string) string { return testRunnerHandler("Bash", bashObj(stdout, stderr)).Summary }

	t.Run("keeps a bun failure's error message next to its name", func(t *testing.T) {
		s := run("", bunTestFail)
		if !strings.Contains(s, "error: expect(received).toBe(expected)") ||
			!strings.Contains(s, "(fail) Bash envelope > stores stdout + stderr for the object shape") {
			t.Errorf("got %q", s)
		}
	})
	t.Run("takes bun's total from its Ran line", func(t *testing.T) {
		s := run(" 1 fail\n 12 expect() calls\nRan 5 tests across 1 file. [68.00ms]", "")
		if !strings.Contains(s, "(5 total)") || strings.Contains(s, "(1 total)") {
			t.Errorf("got %q", s)
		}
	})
	t.Run("never reports pass while it lists failures", func(t *testing.T) {
		s := run("(fail) a > one [1ms]\n(fail) a > two [1ms]", "")
		if !strings.HasPrefix(s, "test runner — FAIL") || !strings.Contains(s, "2 failed") {
			t.Errorf("got %q", s)
		}
		if s := run("  ✕ adds numbers (3 ms)", ""); !strings.HasPrefix(s, "test runner — FAIL: no counts in output") {
			t.Errorf("jest: got %q", s)
		}
	})
	t.Run("reads pytest's failed-first summary line", func(t *testing.T) {
		s := run("1 failed, 2 passed, 1 skipped in 0.12s", "")
		if !strings.HasPrefix(s, "test runner — FAIL") || !strings.Contains(s, "2 passed, 1 failed, 1 skipped") {
			t.Errorf("got %q", s)
		}
	})
	t.Run("counts (pass) lines when no count line is present", func(t *testing.T) {
		s := run("(pass) a > one [1ms]\n(pass) a > two [1ms]\n(fail) a > three [1ms]", "")
		if !strings.Contains(s, "2 passed") || !strings.Contains(s, "1 failed") {
			t.Errorf("got %q", s)
		}
	})
	t.Run("does not turn console noise in a passing run into failures", func(t *testing.T) {
		s := run("error: retrying connection (expected in this test)\n\n 12 pass\n 0 fail\nRan 12 tests across 1 file.", "")
		if !strings.HasPrefix(s, "test runner — pass") || strings.Contains(s, "failures:") {
			t.Errorf("got %q", s)
		}
	})
	t.Run("keeps pytest assertion lines", func(t *testing.T) {
		s := run("FAILED tests/test_x.py::test_add - assert 3 == 4\nE       assert 3 == 4\n1 failed, 2 passed in 0.12s", "")
		if !strings.Contains(s, "E       assert 3 == 4") {
			t.Errorf("got %q", s)
		}
	})
}

func TestBashHandlersSizeOutputNotEnvelope(t *testing.T) {
	var lines []string
	for i := 0; i < 200; i++ {
		lines = append(lines, fmt.Sprintf("src/file%d.ts:%d: shape value %d", i, i, i))
	}
	stdout := strings.Join(lines, "\n")
	stderr := "warning: something went sideways"
	envelope := jsonx.NewObj()
	envelope.Set("stdout", stdout)
	envelope.Set("stderr", stderr)
	envelope.Set("interrupted", false)
	envelope.Set("isImage", false)
	envelope.Set("noOutputExpected", false)
	want := len(stdout + "\n" + stderr)

	commands := []string{
		"git grep shape", "git diff HEAD", "git log -20", "git status", "git branch -a",
		"terraform plan", "bun install", "bun test", "docker ps", "make build", "gh pr list",
		"tsc --noEmit", "grep -rn shape src", "ls -la", "find . -name x", "echo unrouted",
	}
	for _, shape := range []struct {
		name     string
		response any
	}{{"object", envelope}, {"JSON string", jsonx.Compact(envelope)}} {
		for _, c := range commands {
			res := GetHandler("Bash", shape.response, cmdInput(t, c))("Bash", shape.response)
			if res.OriginalSize != want {
				t.Errorf("%s (%s): originalSize %d, want %d", c, shape.name, res.OriginalSize, want)
			}
		}
	}
}
