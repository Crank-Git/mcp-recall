// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/handlers/rtk.go

package handlers

import (
	"regexp"
	"strings"
)

// Output from rtk (upstream #353). rtk's PreToolUse hook rewrites a command
// before it runs (`git diff` → `rtk git diff`), and rtk then prints its own
// compact form, with a recovery hint wherever it elided something: `[+3
// hidden: rtk recall 18f9a13f3086]`, `[full output: rtk recall <hash>]`, or
// `[full diff: rtk git diff --no-compact]`. A summary that cuts the line
// holding a hint must keep it, or the elided content can no longer be found.

var recoveryHintRe = regexp.MustCompile(`\brtk recall [0-9a-f]{6,}\b|^\s*\[full [a-z ]+: rtk [^\]]+\]\s*$`)

// maxRecoveryHints bounds the hints appended, so a hint-dense output stays
// bounded.
const maxRecoveryHints = 5

// recoveryHints returns the rtk recovery hints among lines, in order, at most
// maxRecoveryHints.
func recoveryHints(lines []string) []string {
	var out []string
	for _, l := range lines {
		if recoveryHintRe.MatchString(l) {
			out = append(out, strings.TrimSpace(l))
			if len(out) == maxRecoveryHints {
				break
			}
		}
	}
	return out
}
