// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/handlers/bash_test.go

package handlers

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	failLineRe1 = regexp.MustCompile(`^(FAILED|FAIL)\s+`)
	failLineRe2 = regexp.MustCompile(`^[✕✗×●]\s`)
	failLineRe3 = regexp.MustCompile(`^\(fail\)\s`)
	failLineRe4 = regexp.MustCompile(`^--- FAIL:`)
	passMarkRe  = regexp.MustCompile(`^\(pass\)\s`)
	// bun's `error: …` precedes its (fail) line; pytest's `E   assert …` follows
	// FAILED. The pytest marker is tested on the untrimmed line: it is always
	// flush-left, and requiring that keeps indented prose starting "E  " out.
	bunErrorRe  = regexp.MustCompile(`^error:\s`)
	pytestErrRe = regexp.MustCompile(`^E\s{2,}\S`)

	bunPassRe = regexp.MustCompile(`^(\d+)\s+pass$`)
	bunFailRe = regexp.MustCompile(`^(\d+)\s+fail$`)
	bunRanRe  = regexp.MustCompile(`^Ran\s+(\d+)\s+tests?\s+across\b`)
	// pytest: "1 failed, 5 passed, 1 skipped, 2 warnings in 1.23s". The order
	// varies (pytest puts failed first), so each count is read on its own.
	pytestRe        = regexp.MustCompile(`\b\d+\s+(?:passed|failed)\b.*\bin\s+\d+(?:\.\d+)?s\b`)
	pytestPassedRe  = regexp.MustCompile(`(\d+)\s+passed\b`)
	pytestFailedRe  = regexp.MustCompile(`(\d+)\s+failed\b`)
	pytestSkippedRe = regexp.MustCompile(`(\d+)\s+skipped\b`)
	jestRe          = regexp.MustCompile(`Tests:\s+(?:(\d+)\s+failed,\s+)?(\d+)\s+passed(?:,\s+(\d+)\s+skipped)?`)
	goOkRe          = regexp.MustCompile(`^ok\s+\S+`)
	goFailRe        = regexp.MustCompile(`^FAIL\s+\S+`)
)

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// testRunnerHandler summarises pytest/jest/bun test/vitest/go test output into
// pass/fail counts plus failure names.
func testRunnerHandler(toolName string, output any) Result {
	if r, ok := summarizeTestRun(output); ok {
		return r
	}
	return shellHandler(toolName, output)
}

// summarizeTestRun summarises test-runner output; ok is false when it holds no
// test result (no count line and no failing test). Also used for make/just
// recipes (upstream #325).
func summarizeTestRun(output any) (Result, bool) {
	stdout := extractStdout(output)
	stderr := extractStderr(output)
	combined := strings.TrimSpace(stdout + "\n" + stderr)
	originalSize := byteLen(BashOutputText(output))

	// Collect failure names plus the message lines that say why (upstream
	// #308): a name alone does not tell Claude what to fix. Message lines alone
	// never decide the status: a passing run can print `error: …` from the
	// code under test.
	var failureLines []failureLine
	failureNames, passLines, failLines := 0, 0, 0
	// Detail lines still to take after a bun `error:` or a jest `●` line
	// (upstream #320).
	detailBudget := 0
	for _, line := range strings.Split(combined, "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		if passMarkRe.MatchString(t) {
			passLines++
		}
		if failLineRe3.MatchString(t) {
			failLines++
		}
		isName := failLineRe1.MatchString(t) || failLineRe2.MatchString(t) || failLineRe3.MatchString(t) || failLineRe4.MatchString(t)
		isMessage := bunErrorRe.MatchString(t) || pytestErrRe.MatchString(line)
		switch {
		case isName:
			failureNames++
			failureLines = append(failureLines, failureLine{text: clipName(t), isName: true})
			detailBudget = 0
			if strings.HasPrefix(t, "●") {
				detailBudget = maxDetailLines
			}
		case isMessage:
			failureLines = append(failureLines, failureLine{text: firstChars(t, maxTestLine)})
			if bunErrorRe.MatchString(t) {
				detailBudget = maxDetailLines
			}
		case detailBudget > 0 && isDetail(t):
			failureLines = append(failureLines, failureLine{text: firstChars(t, maxTestLine)})
			detailBudget--
		}
	}

	passed, failed, skipped := 0, 0, 0
	ranTotal := -1
	foundSummary := false

	for _, line := range strings.Split(combined, "\n") {
		t := strings.TrimSpace(line)

		if m := bunPassRe.FindStringSubmatch(t); m != nil {
			passed = atoi(m[1])
			foundSummary = true
		}
		if m := bunFailRe.FindStringSubmatch(t); m != nil {
			failed = atoi(m[1])
			foundSummary = true
		}
		if m := bunRanRe.FindStringSubmatch(t); m != nil {
			ranTotal = atoi(m[1])
			foundSummary = true
		}
		if pytestRe.MatchString(t) {
			count := func(re *regexp.Regexp) int {
				if m := re.FindStringSubmatch(t); m != nil {
					return atoi(m[1])
				}
				return 0
			}
			passed = count(pytestPassedRe)
			failed = count(pytestFailedRe)
			skipped = count(pytestSkippedRe)
			foundSummary = true
		}
		if m := jestRe.FindStringSubmatch(t); m != nil {
			if m[1] != "" {
				failed = atoi(m[1])
			}
			passed = atoi(m[2])
			if m[3] != "" {
				skipped = atoi(m[3])
			}
			foundSummary = true
		}
		if goOkRe.MatchString(t) {
			passed++
			foundSummary = true
		}
		if goFailRe.MatchString(t) {
			failed++
			foundSummary = true
		}
	}

	if !foundSummary && failureNames == 0 {
		return Result{}, false
	}

	// Without count lines (output cut by `tail`, or grepped), count bun's
	// per-test lines rather than report nothing.
	if passed == 0 {
		passed = passLines
	}
	if failed == 0 {
		failed = failLines
	}
	// A partial view must not invent a total: prefer the runner's own.
	total := passed + failed + skipped
	if ranTotal >= 0 {
		total = ranTotal
	}
	// A failing test's name always means FAIL, even when no count line survived.
	isFail := failed > 0 || failureNames > 0
	status := "pass"
	if isFail {
		status = "FAIL"
	}
	var parts []string
	if passed > 0 {
		parts = append(parts, fmt.Sprintf("%d passed", passed))
	}
	if failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", failed))
	}
	if skipped > 0 {
		parts = append(parts, fmt.Sprintf("%d skipped", skipped))
	}
	// No count survived (e.g. a cut-off jest run): say so, rather than claim
	// the run produced no results.
	summaryStr := "no counts in output"
	if len(parts) > 0 {
		summaryStr = strings.Join(parts, ", ")
	}

	totalStr := ""
	if total > 0 {
		totalStr = fmt.Sprintf(" (%d total)", total)
	}
	lines := []string{fmt.Sprintf("test runner — %s: %s%s", status, summaryStr, totalStr)}
	if isFail && len(failureLines) > 0 {
		kept := selectFailureLines(failureLines, maxBuildErrors)
		lines = append(lines, "  failures:")
		for _, l := range kept {
			lines = append(lines, "    "+l.text)
		}
		if len(failureLines) > len(kept) {
			lines = append(lines, fmt.Sprintf("    … (+%d more)", len(failureLines)-len(kept)))
		}
	}
	return Result{Summary: strings.Join(lines, "\n"), OriginalSize: originalSize}, true
}

type failureLine struct {
	text   string
	isName bool
}

// Detail lines kept after one bun `error:` or jest `●` line (upstream #320).
const (
	maxDetailLines = 4
	maxTestLine    = 160
	nameHead       = 60
)

var (
	diffLegendRe = regexp.MustCompile(`^[-+]\s+(Expected|Received)\s+[-+]\s*\d+$`)
	expRecvRe    = regexp.MustCompile(`^(Expected|Received)\b`)
	diffLineRe   = regexp.MustCompile(`^[-+]\s`)
	testTimingRe = regexp.MustCompile(`\s+\[\d+(?:\.\d+)?m?s\]$`)
)

// isDetail reports Expected/Received values and `-`/`+` diff lines; not the
// `- Expected  - 1` diff legend.
func isDetail(t string) bool {
	if diffLegendRe.MatchString(t) {
		return false
	}
	return expRecvRe.MatchString(t) || diffLineRe.MatchString(t)
}

// clipName shortens a failing test's name. bun names are `describe > it`, so
// parameterised cases differ at the end: drop the timing, then clip from the
// middle so the end survives (upstream #320).
func clipName(t string) string {
	name := []rune(testTimingRe.ReplaceAllString(t, ""))
	if len(name) <= maxTestLine {
		return string(name)
	}
	return string(name[:nameHead]) + " … " + string(name[len(name)-(maxTestLine-nameHead-3):])
}

// selectFailureLines keeps up to max lines in their original order. Every
// failure name comes first in priority, so detail never pushes a later
// failure out of the list; the room left goes to the earliest failures'
// messages.
func selectFailureLines(entries []failureLine, max int) []failureLine {
	if len(entries) <= max {
		return entries
	}
	names := 0
	for _, e := range entries {
		if e.isName {
			names++
		}
	}
	detailRoom := max - names
	if detailRoom < 0 {
		detailRoom = 0
	}
	nameRoom := max
	var out []failureLine
	for _, e := range entries {
		if e.isName {
			if nameRoom > 0 {
				out = append(out, e)
			}
			nameRoom--
			continue
		}
		if detailRoom > 0 && nameRoom > 0 {
			detailRoom--
			nameRoom--
			out = append(out, e)
		}
	}
	return out
}
