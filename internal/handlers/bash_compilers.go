// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/handlers/bash_compilers.go
//
// Compiler & linter diagnostics handler — compresses verbose build / typecheck /
// lint output (cargo build|check|clippy, go build|vet, tsc, eslint, ruff) down to
// a headline count plus the individual error/warning diagnostics, errors first.
// Ports src/handlers/bash-compilers.ts.
//
// Safety contract (never hide a failure):
//   - NEVER reports success when the command failed. A non-zero exit code always
//     renders as ✗, and every parsed error is surfaced (capped, errors before
//     warnings).
//   - When it cannot recognise ANY diagnostics or count, it falls back to the
//     shell handler, so an unparsed failure is shown head/tail — never dropped.
//   - It never fabricates a "clean" summary for a command that exited non-zero.
//
// The full original output remains retrievable via recall__* regardless.

package handlers

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

type diagnostic struct {
	severity string // "error" | "warning"
	location string // "" when unknown
	message  string
	// labeled is true when the line carried an explicit severity word. An
	// unlabeled `file:line: message` (go-style) defaults to "error" but is only
	// trusted as a failure when the run didn't cleanly succeed — see the
	// clean-exit filter.
	labeled bool
}

const maxDiagnosticMessageLen = 100

var (
	// tsc: "src/foo.ts(42,10): error TS2345: message"
	tscRe = regexp.MustCompile(`^(\S+?)\((\d+),(\d+)\):\s+(error|warning)\s+TS\d+:\s+(.+)$`)
	// gcc / go / ruff: "path.ext:line[:col]: [severity:] message" — require a
	// file extension before :line so ordinary "note:" / "host:port" lines don't
	// match.
	fileLocRe = regexp.MustCompile(`^(\S*\.\w+):(\d+)(?::(\d+))?:\s+(?:(error|warning|note):\s+)?(.+)$`)
	// rustc / cargo bare severity: "error[E0308]: message" / "warning: message"
	severityRe = regexp.MustCompile(`^(error|warning)(?:\[[A-Za-z]?\d+\])?:\s+(.+)$`)
	// cargo location line following a bare-severity line: "  --> src/main.rs:10:5"
	cargoLocRe = regexp.MustCompile(`^\s*-->\s+(\S+:\d+(?::\d+)?)`)
	// eslint stylish file header (a bare path on its own line)
	eslintFileRe = regexp.MustCompile(`^(/?\S+\.\w+)$`)
	// eslint stylish diagnostic: "  10:5  error  message  rule-name"
	eslintRe = regexp.MustCompile(`^\s+(\d+):(\d+)\s+(error|warning)\s+(.+?)(?:\s{2,}[\w./-]+)?$`)

	// Authoritative summary lines (trusted for the headline count when present).
	cargoErrSumRe  = regexp.MustCompile(`(?i)could not compile .*? due to (\d+) previous error`)
	cargoWarnSumRe = regexp.MustCompile(`(?i)generated (\d+) warning`)
	eslintSumRe    = regexp.MustCompile(`(?i)[✖✗x]\s+\d+\s+problems?\s+\((\d+)\s+errors?,\s+(\d+)\s+warnings?\)`)
	ruffSumRe      = regexp.MustCompile(`(?i)Found (\d+) error`)
	tscSumRe       = regexp.MustCompile(`(?i)Found (\d+) errors?\b`)
	// cargo's boilerplate trailer — redundant with cargoErrSumRe's count, and
	// must not be parsed as another severityRe diagnostic ("error: aborting …").
	cargoAbortRe = regexp.MustCompile(`(?i)^error: aborting due to \d+ previous error`)
)

func clipDiagnostic(s string) string {
	return firstChars(strings.TrimSpace(s), maxDiagnosticMessageLen)
}

func atoiOr0(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func compilerDiagnosticsHandler(toolName string, output any) Result {
	stdout := extractStdout(output)
	stderr := extractStderr(output)
	combined := stdout + "\n" + stderr
	originalSize := byteLen(BashOutputText(output))
	exitCode, hasExit := extractExitCode(output)

	var diagnostics []diagnostic
	summaryErrors, hasSummaryErrors := 0, false
	summaryWarnings, hasSummaryWarnings := 0, false
	eslintFile := ""

	for _, raw := range strings.Split(combined, "\n") {
		t := strings.TrimRight(raw, " \t\r")
		if strings.TrimSpace(t) == "" {
			continue
		}

		// --- authoritative summary counts ---
		if m := cargoErrSumRe.FindStringSubmatch(t); m != nil {
			summaryErrors += atoiOr0(m[1])
			hasSummaryErrors = true
			continue
		}
		if m := cargoWarnSumRe.FindStringSubmatch(t); m != nil {
			summaryWarnings, hasSummaryWarnings = atoiOr0(m[1]), true
			continue
		}
		if m := eslintSumRe.FindStringSubmatch(t); m != nil {
			summaryErrors, hasSummaryErrors = atoiOr0(m[1]), true
			summaryWarnings, hasSummaryWarnings = atoiOr0(m[2]), true
			continue
		}
		if m := ruffSumRe.FindStringSubmatch(t); m != nil {
			summaryErrors, hasSummaryErrors = atoiOr0(m[1]), true
			continue
		}
		if m := tscSumRe.FindStringSubmatch(t); m != nil {
			summaryErrors, hasSummaryErrors = atoiOr0(m[1]), true
			continue
		}
		if cargoAbortRe.MatchString(t) {
			continue // redundant trailer, not a diagnostic
		}

		// --- tsc paren form (check before fileLocRe so TS codes are dropped) ---
		if m := tscRe.FindStringSubmatch(t); m != nil {
			diagnostics = append(diagnostics, diagnostic{
				severity: m[4], location: m[1] + ":" + m[2], message: clipDiagnostic(m[5]), labeled: true,
			})
			continue
		}
		// --- gcc / go / ruff file:line:col form ---
		if m := fileLocRe.FindStringSubmatch(t); m != nil {
			sev := m[4]
			if sev == "note" {
				continue
			}
			labeled := sev != ""
			if sev == "" {
				sev = "error"
			}
			diagnostics = append(diagnostics, diagnostic{
				severity: sev, location: m[1] + ":" + m[2], message: clipDiagnostic(m[5]), labeled: labeled,
			})
			continue
		}
		// --- rustc / cargo bare severity ---
		if m := severityRe.FindStringSubmatch(t); m != nil {
			diagnostics = append(diagnostics, diagnostic{
				severity: m[1], message: clipDiagnostic(m[2]), labeled: true,
			})
			continue
		}
		// --- cargo "  --> loc" attaches to the previous locationless diagnostic ---
		if m := cargoLocRe.FindStringSubmatch(t); m != nil {
			if n := len(diagnostics); n > 0 && diagnostics[n-1].location == "" {
				diagnostics[n-1].location = m[1]
			}
			continue
		}
		// --- eslint file header ---
		if eslintFileRe.MatchString(t) {
			eslintFile = strings.TrimSpace(t)
			continue
		}
		// --- eslint indented diagnostic ---
		if m := eslintRe.FindStringSubmatch(t); m != nil {
			loc := m[1] + ":" + m[2]
			if eslintFile != "" {
				loc = eslintFile + ":" + m[1]
			}
			diagnostics = append(diagnostics, diagnostic{
				severity: m[3], location: loc, message: clipDiagnostic(m[4]), labeled: true,
			})
			continue
		}
	}

	// Nothing recognisable — don't risk a wrong summary; show the raw output via
	// the shell handler so an unparsed failure is never hidden. (A non-zero exit
	// that DOES parse still renders ✗ below, so success is never fabricated.)
	if len(diagnostics) == 0 && !hasSummaryErrors && !hasSummaryWarnings {
		return shellHandler(toolName, output)
	}

	// On a KNOWN-clean exit, drop unlabeled "error" diagnostics: a go-style
	// `file:line: message` only appears on a real failure, so on a passing run an
	// incidental `host:port: message` line must not fabricate an error.
	visible := diagnostics
	if hasExit && exitCode == 0 {
		visible = nil
		for _, d := range diagnostics {
			if d.labeled {
				visible = append(visible, d)
			}
		}
	}

	var errs, warns []diagnostic
	for _, d := range visible {
		if d.severity == "error" {
			errs = append(errs, d)
		} else {
			warns = append(warns, d)
		}
	}
	errorCount, warnCount := len(errs), len(warns)
	if hasSummaryErrors {
		errorCount = summaryErrors
	}
	if hasSummaryWarnings {
		warnCount = summaryWarnings
	}

	// Pass/fail is authoritative from the exit code when we have it (never hide a
	// failure, never fabricate one on success); fall back to the parsed error
	// count only when the exit code is unknown.
	failed := errorCount > 0
	if hasExit {
		failed = exitCode != 0
	}
	status := "✓"
	if failed {
		status = "✗"
	}
	var parts []string
	if errorCount > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", errorCount, plural(errorCount, "error", "errors")))
	}
	if warnCount > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", warnCount, plural(warnCount, "warning", "warnings")))
	}
	tail := "clean"
	if failed {
		tail = "failed"
	}
	if len(parts) > 0 {
		tail = strings.Join(parts, ", ")
	}
	lines := []string{status + " " + tail}

	// Errors first, then warnings; cap the total shown.
	ordered := append(append([]diagnostic{}, errs...), warns...)
	for i, d := range ordered {
		if i >= maxBuildErrors {
			break
		}
		label := "error"
		if d.severity == "warning" {
			label = "warn"
		}
		loc := ""
		if d.location != "" {
			loc = d.location + " — "
		}
		lines = append(lines, fmt.Sprintf("  %s: %s%s", label, loc, d.message))
	}
	if len(ordered) > maxBuildErrors {
		lines = append(lines, fmt.Sprintf("  … (+%d more)", len(ordered)-maxBuildErrors))
	}

	return Result{Summary: strings.Join(lines, "\n"), OriginalSize: originalSize}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
