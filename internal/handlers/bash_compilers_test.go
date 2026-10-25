// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/handlers/bash_compilers_test.go
//
// Covers command-aware compiler/linter compression (upstream PR #243).

package handlers

import (
	"strconv"
	"strings"
	"testing"

	"mcprecall/internal/jsonx"
)

// bashOut builds the JSON-string payload the Bash tool actually delivers.
func bashOut(t *testing.T, stdout, stderr string, exitCode int) any {
	t.Helper()
	return mustParseT(t, `{"stdout":`+jsonx.Compact(stdout)+`,"stderr":`+jsonx.Compact(stderr)+
		`,"exit_code":`+strconv.Itoa(exitCode)+`}`)
}

func TestCompilerTscDiagnostics(t *testing.T) {
	out := "src/a.ts(42,10): error TS2345: Argument of type 'string' is not assignable\n" +
		"src/b.ts(7,3): warning TS6133: 'x' is declared but never used\n" +
		"Found 1 error in src/a.ts\n"
	s := compilerDiagnosticsHandler("Bash", bashOut(t, out, "", 2)).Summary
	has(t, s, "✗")
	has(t, s, "1 error")
	has(t, s, "error: src/a.ts:42 — Argument of type")
	has(t, s, "warn: src/b.ts:7")
	// TS codes are dropped from the rendered message.
	hasNot(t, s, "TS2345")
}

func TestCompilerCargoDiagnostics(t *testing.T) {
	out := "error[E0308]: mismatched types\n" +
		"  --> src/main.rs:10:5\n" +
		"warning: unused variable: `y`\n" +
		"error: aborting due to 1 previous error\n" +
		"error: could not compile `demo` due to 1 previous error; 1 warning emitted\n"
	s := compilerDiagnosticsHandler("Bash", bashOut(t, "", out, 101)).Summary
	has(t, s, "✗ 1 error")
	has(t, s, "error: src/main.rs:10:5 — mismatched types")
	// The boilerplate trailer must not be counted as another diagnostic.
	hasNot(t, s, "aborting due to")
}

func TestCompilerEslintStylish(t *testing.T) {
	out := "/repo/src/app.js\n" +
		"  10:5  error  Unexpected console statement  no-console\n" +
		"  12:1  warning  Missing semicolon  semi\n" +
		"\n✖ 2 problems (1 error, 1 warning)\n"
	s := compilerDiagnosticsHandler("Bash", bashOut(t, out, "", 1)).Summary
	has(t, s, "✗ 1 error, 1 warning")
	has(t, s, "error: /repo/src/app.js:10 — Unexpected console statement")
}

// Errors sort before warnings regardless of input order.
func TestCompilerOrdersErrorsFirst(t *testing.T) {
	out := "a.go:1: warning: first\nb.go:2: error: second\n"
	s := compilerDiagnosticsHandler("Bash", bashOut(t, out, "", 1)).Summary
	ei := strings.Index(s, "error: b.go:2")
	wi := strings.Index(s, "warn: a.go:1")
	if ei < 0 || wi < 0 || ei > wi {
		t.Errorf("errors should precede warnings:\n%s", s)
	}
}

// Safety contract: a non-zero exit never renders as success.
func TestCompilerNeverReportsSuccessOnFailure(t *testing.T) {
	s := compilerDiagnosticsHandler("Bash", bashOut(t, "", "error: boom\n", 1)).Summary
	has(t, s, "✗")
	hasNot(t, s, "✓")
}

// On a clean exit, an unlabeled go-style "file:line: message" must not
// fabricate an error.
func TestCompilerCleanExitDropsUnlabeledDiagnostics(t *testing.T) {
	out := "config.yml:8: connecting to db.internal\nwarning: deprecated flag\n"
	s := compilerDiagnosticsHandler("Bash", bashOut(t, out, "", 0)).Summary
	has(t, s, "✓")
	has(t, s, "1 warning")
	hasNot(t, s, "1 error")
}

// Unrecognisable output must fall back to shell rather than risk a wrong
// summary — an unparsed failure is shown, never dropped.
func TestCompilerFallsBackToShell(t *testing.T) {
	body := strings.Repeat("some unstructured build chatter\n", 60)
	s := compilerDiagnosticsHandler("Bash", bashOut(t, body, "", 1)).Summary
	if strings.HasPrefix(s, "✓") || strings.HasPrefix(s, "✗") {
		t.Errorf("should have fallen back to shell:\n%s", s)
	}
	has(t, s, "some unstructured build chatter")
}

// The Bash payload is a JSON string; stderr-bound diagnostics must still be
// seen (upstream #243's extractStderr fix).
func TestExtractStderrFromJSONString(t *testing.T) {
	raw := `{"stdout":"","stderr":"error: boom","exit_code":1}`
	if got := extractStderr(raw); got != "error: boom" {
		t.Errorf("extractStderr = %q", got)
	}
	if ec, ok := extractExitCode(raw); !ok || ec != 1 {
		t.Errorf("extractExitCode = %d, %v", ec, ok)
	}
	if _, ok := extractExitCode("plain text"); ok {
		t.Error("exit code should be absent for unstructured output")
	}
}

func TestCompilerRuffFullFormat(t *testing.T) {
	out := strings.Join([]string{
		"F401 [*] `os` imported but unused",
		" --> svc/mod0.py:1:8",
		"  |",
		"1 | import os",
		"  |        ^^",
		"help: Remove unused import: `os`",
		"",
		"F821 Undefined name `fallback_0`",
		" --> svc/mod0.py:6:55",
		"  |",
		"6 |     return json.loads(open(path).read()) if path else fallback_0",
		"  |                                                       ^^^^^^^^^^",
		"",
		"Found 2 errors.",
		"[*] 1 fixable with the `--fix` option.",
	}, "\n")
	s := compilerDiagnosticsHandler("Bash", bashOut(t, out, "", 1)).Summary
	has(t, s, "2 errors")
	has(t, s, "svc/mod0.py:1:8 — F401 [*] `os` imported but unused")
	has(t, s, "svc/mod0.py:6:55 — F821 Undefined name `fallback_0`")
	hasNot(t, s, "help:")
	hasNot(t, s, "^^")
}

func TestCompilerRuffShapedLineOnPassingRun(t *testing.T) {
	s := compilerDiagnosticsHandler("Bash", bashOut(t, "D100 checks are advisory in this project\nBuild finished.", "", 0)).Summary
	hasNot(t, s, "D100")
	hasNot(t, s, "error")
}
