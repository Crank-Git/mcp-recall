// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/handlers/upstream116_test.go
// Port of the upstream v1.16.x handler vectors: bounded and content reads
// (#327, #332, #344), the stderr-only error fallback (#326), test summaries
// from make/just recipes (#325) and failure detail lines (#320).

package handlers

import (
	"fmt"
	"strings"
	"testing"
)

// ── stderr-only errors (#326) ─────────────────────────────────────────────────

func TestStderrOnlyErrorKeepsShell(t *testing.T) {
	fatal := "fatal: ambiguous argument 'a-long-enough-missing-ref-name': unknown revision or path not in the working tree."
	failed := func(stderr string) any { return bashObj("", stderr) }

	t.Run("uses the shell handler instead of a stdout handler's empty result", func(t *testing.T) {
		for _, command := range []string{
			"git diff nosuchref | head -50", "git log nosuchref", "git status", "git branch -a",
			"grep -rn foo /missing/dir", "ls /missing/dir", "find /missing/dir -name x",
			"gh pr view 99999", "docker ps", "terraform plan",
		} {
			h := GetHandler("Bash", failed(fatal), cmdInput(t, command))
			if !samePtr(h, shellHandler) {
				t.Errorf("%q routed to %s", command, HandlerName(h))
				continue
			}
			if s := h("Bash", failed(fatal)).Summary; !strings.Contains(s, "fatal: ambiguous argument") {
				t.Errorf("%q: summary lost the error: %q", command, s)
			}
		}
	})

	t.Run("keeps handlers that report on stderr themselves", func(t *testing.T) {
		bunResults := "(pass) suite > a [0.1ms]\n(fail) suite > b [0.1ms]\n\n 1 pass\n 1 fail\nRan 2 tests across 1 file. [5.00ms]"
		for _, tc := range []struct {
			stderr, command string
			want            Handler
		}{
			{bunResults, "bun test", testRunnerHandler},
			{"make: *** [all] Error 1", "make", buildToolHandler},
			{"error[E0425]: cannot find value `x`", "cargo build", compilerDiagnosticsHandler},
			{"npm warn deprecated x@1", "npm install", packageInstallHandler},
		} {
			if h := GetHandler("Bash", failed(tc.stderr), cmdInput(t, tc.command)); !samePtr(h, tc.want) {
				t.Errorf("%q routed to %s", tc.command, HandlerName(h))
			}
		}
	})

	t.Run("keeps the stdout handler when stderr is empty or stdout has output", func(t *testing.T) {
		if h := GetHandler("Bash", failed(""), cmdInput(t, "grep -rn foo src")); !samePtr(h, grepHandler) {
			t.Errorf("empty stderr routed to %s", HandlerName(h))
		}
		withWarning := bashObj("src/a.ts:1:foo", "grep: src/b: Permission denied")
		if h := GetHandler("Bash", withWarning, cmdInput(t, "grep -rn foo src")); !samePtr(h, grepHandler) {
			t.Errorf("stdout with a warning routed to %s", HandlerName(h))
		}
	})
}

// ── IsBoundedRead (#327, #344) ────────────────────────────────────────────────

func TestIsBoundedRead(t *testing.T) {
	check := func(t *testing.T, want bool, commands ...string) {
		t.Helper()
		for _, c := range commands {
			if got := IsBoundedRead(c); got != want {
				t.Errorf("IsBoundedRead(%q) = %v, want %v", c, got, want)
			}
		}
	}
	t.Run("a command that limits its own output", func(t *testing.T) {
		check(t, true,
			`grep -n "class X" -A 30 src/x.ts | head -40`,
			"grep -B2 -A22 foo src/x.ts | head -50",
			"grep -n foo src/x.ts | head -3; sed -n 900,1000p src/x.ts",
			"sed -n '900,1000p' src/x.ts",
			`sed -n "5p" src/x.ts`,
			"git log --oneline | head",
			"bun test 2>&1 | tail -30",
			"head -n 80 README.md",
			"tail -20 build.log",
			"grep -rn -C3 foo src",
			"rg -n --context 5 foo src",
			"grep -rnA4 foo src",
			"grep -m 5 foo src/x.ts",
		)
	})
	t.Run("a sed -n range the shell computes, or an N,+M range", func(t *testing.T) {
		check(t, true,
			`sed -n "$((n-40)),$((n+40))p" src/x.ts`,
			`sed -n "${start},${end}p" src/x.ts`,
			"sed -n '120,+40p' src/x.ts",
			`sed -n "$start,+75p" src/x.ts`,
			"sed -n '30,$p' src/x.ts",
		)
	})
	t.Run("not a sed substitution or every-Nth-line step", func(t *testing.T) {
		check(t, false,
			"sed -n 's/^KEY=//p' .env.example",
			`sed -n "s/$pat//p" src/x.ts`,
			"sed -n '0~5p' src/x.ts",
		)
	})
	t.Run("not other commands", func(t *testing.T) {
		check(t, false,
			"grep -rn foo src",
			"cat header.txt",
			"ls -A 3",
			"ps -A",
			"sed 's/a/b/' src/x.ts",
			"sed -n '/start/,/end/p' src/x.ts",
			"git log --format=%h",
			"./scripts/headless-run.sh",
			"echo head -5",
		)
	})
	t.Run("sees through cd and wrapper prefixes", func(t *testing.T) {
		check(t, true, "timeout 30 grep -A 30 foo src", "cd /repo && sudo tail -n 50 /var/log/x")
	})
}

func TestIsContentRead(t *testing.T) {
	for _, c := range []string{
		"cat README.md", "cat -n src/x.py", "git diff", "git diff main -- src/", "git show HEAD~1",
		"git --no-pager diff", "cd /repo && cat a.md", `echo "=== a"; cat a.md; echo "=== b"; cat b.md`,
		"timeout 30 cat big.log", "sed -n 5,20p x.ts", "grep -n foo -A 5 x.ts",
		"git log -p -3 src/x.ts", "git log --patch main..HEAD", "git stash show -p stash@{0}",
	} {
		if !IsContentRead(c) {
			t.Errorf("IsContentRead(%q) = false", c)
		}
	}
	for _, c := range []string{
		"git log --oneline", "git status", "ls -la", "grep -rn foo src", "./catalog --list",
		"concat a b", "echo cat", "git difftool", "bun test", "git log --oneline -p5x", "git stash show",
		"git log --pretty=%h",
	} {
		if IsContentRead(c) {
			t.Errorf("IsContentRead(%q) = true", c)
		}
	}
}

// ── test-runner failure detail (#320) ─────────────────────────────────────────

const bunTestFailFixture = "bun test v1.3.14 (0d9b296a)\n\ntests/hooks.test.ts:\n" +
	"851 |       expect(run(shape).row?.full_content).toBe(text);\n" +
	"                                                 ^\n" +
	"error: expect(received).toBe(expected)\n\n" +
	"Expected: \"shape value 0\"\nReceived: \"\"\n\n" +
	"      at <anonymous> (tests/hooks.test.ts:851:44)\n" +
	"(fail) Bash envelope > stores stdout + stderr for the object shape [2.26ms]\n\n" +
	" 4 pass\n 54 filtered out\n 1 fail\n 12 expect() calls\nRan 5 tests across 1 file. [68.00ms]"

func TestTestRunnerFailureDetail(t *testing.T) {
	longSuite := "handlePostToolUse — Bash output cut at ~50 KB (#316) > sizes, stores and summarises the persisted output for the"
	bunDiffFailure := func(shape string) []string {
		return []string{
			"5 |       expect(a).toBe(b);",
			"                ^",
			"error: expect(received).toBe(expected)",
			"",
			`  "persisted row 1`,
			`- persisted row 2"`,
			"+ persisted row 2",
			`+ "`,
			"",
			"- Expected  - 1",
			"+ Received  + 2",
			"",
			"      at <anonymous> (tests/hooks.test.ts:957:52)",
			fmt.Sprintf("(fail) %s %s shape [0.11ms]", longSuite, shape),
		}
	}
	twoDiffs := strings.Join(append(append(bunDiffFailure("object"), bunDiffFailure("JSON string")...), "", " 4 pass", " 2 fail"), "\n")
	run := func(stdout, stderr string) string { return testRunnerHandler("Bash", bashObj(stdout, stderr)).Summary }

	t.Run("keeps the end of a long failure name, where cases differ", func(t *testing.T) {
		s := run(twoDiffs, "")
		if !strings.Contains(s, "for the object shape") || !strings.Contains(s, "for the JSON string shape") {
			t.Errorf("got %q", s)
		}
		deep := "(fail) " + strings.Repeat("outer suite > ", 20) + "case for the JSON string shape [3ms]"
		var nameLine string
		for _, l := range strings.Split(run(deep+"\n 0 pass\n 1 fail", ""), "\n") {
			if strings.Contains(l, "(fail)") {
				nameLine = l
			}
		}
		if !strings.Contains(nameLine, " … ") || !strings.Contains(nameLine, "case for the JSON string shape") {
			t.Errorf("name line %q", nameLine)
		}
		if n := len([]rune(strings.TrimSpace(nameLine))); n > 160 {
			t.Errorf("name line is %d chars", n)
		}
	})

	t.Run("keeps a bun failure's diff lines, not the diff legend", func(t *testing.T) {
		s := run(twoDiffs, "")
		if !strings.Contains(s, `- persisted row 2"`) || !strings.Contains(s, "+ persisted row 2") {
			t.Errorf("diff lines missing: %q", s)
		}
		if strings.Contains(s, "- Expected  - 1") {
			t.Errorf("legend kept: %q", s)
		}
	})

	t.Run("keeps bun's Expected and Received lines", func(t *testing.T) {
		s := run("", bunTestFailFixture)
		if !strings.Contains(s, `Expected: "shape value 0"`) || !strings.Contains(s, `Received: ""`) {
			t.Errorf("got %q", s)
		}
	})

	t.Run("keeps jest's Expected and Received lines after the failure name", func(t *testing.T) {
		out := "  ● math › adds\n\n    expect(received).toBe(expected)\n\n    Expected: 4\n    Received: 3\n\nTests: 1 failed, 2 passed, 3 total"
		s := run(out, "")
		for _, want := range []string{"● math › adds", "Expected: 4", "Received: 3"} {
			if !strings.Contains(s, want) {
				t.Errorf("missing %q in %q", want, s)
			}
		}
	})

	t.Run("lists every failure name before spending the cap on detail", func(t *testing.T) {
		var parts []string
		for i := 0; i < 12; i++ {
			parts = append(parts, strings.Join([]string{
				"error: expect(received).toBe(expected)", "", fmt.Sprintf("Expected: %d", i), fmt.Sprintf("Received: %d", i+1), "",
				fmt.Sprintf("(fail) suite > case %d [1ms]", i),
			}, "\n"))
		}
		s := run(strings.Join(parts, "\n")+"\n 0 pass\n 12 fail", "")
		for i := 0; i < 12; i++ {
			if !strings.Contains(s, fmt.Sprintf("(fail) suite > case %d", i)) {
				t.Errorf("case %d missing", i)
			}
		}
		if !strings.Contains(s, "Expected: 0") {
			t.Error("first failure's detail missing")
		}
		if n := len(strings.Split(s, "\n")); n > 24 {
			t.Errorf("%d lines", n)
		}
	})
}

// ── make/just recipes that run tests (#325) ───────────────────────────────────

func TestRecipeRunningTests(t *testing.T) {
	var failedNames []string
	for i := 0; i < 6; i++ {
		failedNames = append(failedNames, fmt.Sprintf("FAILED app/tests/test_api.py::test_case_%d - AssertionError: expected %d", i, i))
	}
	var lines []string
	for i := 0; i < 69; i++ {
		lines = append(lines, fmt.Sprintf("app/tests/test_api.py::test_ok_%d PASSED", i))
	}
	lines = append(lines, failedNames...)
	lines = append(lines,
		"=================== 6 failed, 69 passed in 3.21s ===================",
		"error: Recipe `test` failed on line 3 with exit code 1")
	pytestViaJust := strings.Join(lines, "\n")

	t.Run("pytest from just is a test run with every failing name", func(t *testing.T) {
		s := buildToolHandler("Bash", bashObj(pytestViaJust, "")).Summary
		if !strings.Contains(s, "FAIL: 69 passed, 6 failed") {
			t.Errorf("got %q", s)
		}
		for _, n := range failedNames {
			if !strings.Contains(s, firstChars(n, 60)) {
				t.Errorf("missing %q", n)
			}
		}
		if c := strings.Count(s, "error: Recipe `test` failed"); c != 1 {
			t.Errorf("recipe error shown %d times", c)
		}
	})

	passing := func(n int) []string {
		var out []string
		for i := 0; i < n; i++ {
			out = append(out, fmt.Sprintf("(pass) suite > case %d [0.10ms]", i))
		}
		return out
	}

	t.Run("bun from make keeps make's error from stderr", func(t *testing.T) {
		stdout := strings.Join(append(passing(40), "(fail) suite > the broken one [0.20ms]", "", " 40 pass", " 1 fail", "Ran 41 tests across 1 file. [1.00s]"), "\n")
		s := buildToolHandler("Bash", bashObj(stdout, "make: *** [Makefile:3: test] Error 1")).Summary
		for _, want := range []string{"FAIL: 40 passed, 1 failed", "(fail) suite > the broken one", "make: *** [Makefile:3: test] Error 1"} {
			if !strings.Contains(s, want) {
				t.Errorf("missing %q in %q", want, s)
			}
		}
	})

	t.Run("keeps a later step's error when the tests passed", func(t *testing.T) {
		stdout := strings.Join(append(passing(40), "", " 40 pass", " 0 fail", "Ran 40 tests across 1 file. [1.00s]",
			"src/app.ts:3:5: error: 'unused' is declared but never read",
			"error: Recipe `ci` failed on line 7 with exit code 1"), "\n")
		s := buildToolHandler("Bash", bashObj(stdout, "")).Summary
		for _, want := range []string{"pass: 40 passed", "src/app.ts:3:5: error: 'unused'", "error: Recipe `ci` failed"} {
			if !strings.Contains(s, want) {
				t.Errorf("missing %q in %q", want, s)
			}
		}
	})
}
