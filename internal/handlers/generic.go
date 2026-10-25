// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/handlers/generic.go

package handlers

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

const (
	genericMaxChars    = 500 // return unchanged at or below this size
	genericHeadChars   = 380
	genericTailChars   = 100
	genericHeadLines   = 5
	genericTailLines   = 5
	genericLineModeMin = genericHeadLines + genericTailLines + 1
	genericMaxMatch    = 8
)

// genericMatchRe surfaces error/warn lines from an otherwise-elided middle.
var genericMatchRe = regexp.MustCompile(`(?i)\b(error|errors|warn|warning|fail|failed|failure|exception|fatal|panic|denied|refused|timeout)\b`)

func lastChars(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}

func trimStart(s string) string { return strings.TrimLeftFunc(s, unicode.IsSpace) }

func summarizeBlock(raw string) string {
	head := trimEnd(firstChars(raw, genericHeadChars))
	tail := trimStart(lastChars(raw, genericTailChars))
	return head + "\n…\n" + tail
}

func genericPlural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func summarizeLines(lines []string) string {
	return summarizeLineWindow(lines, genericHeadLines, genericTailLines)
}

// failureLines returns up to genericMaxMatch error/warn lines from lines a
// summary would cut.
func failureLines(lines []string) []string {
	var out []string
	for _, l := range lines {
		if genericMatchRe.MatchString(l) {
			out = append(out, l)
			if len(out) >= genericMaxMatch {
				break
			}
		}
	}
	return out
}

func containsLine(lines []string, l string) bool {
	for _, x := range lines {
		if x == l {
			return true
		}
	}
	return false
}

// summarizeLineWindow keeps the first headLines and last tailLines lines, with a
// count of the elided middle and any error/warn lines and rtk recovery hints
// (upstream #353) surfaced from it, each line clipped to MaxLineChars (#349).
// Callers keep len(lines) > headLines+tailLines so the windows never overlap.
func summarizeLineWindow(lines []string, headLines, tailLines int) string {
	head := lines[:headLines]
	tail := lines[len(lines)-tailLines:]
	middle := lines[headLines : len(lines)-tailLines]
	matches := failureLines(middle)
	var rest []string
	for _, l := range middle {
		if !containsLine(matches, l) {
			rest = append(rest, l)
		}
	}
	hints := recoveryHints(rest)

	var note string
	if len(matches) > 0 {
		note = fmt.Sprintf("…(%d middle line%s elided; %d error/warn shown)…", len(middle), genericPlural(len(middle)), len(matches))
	} else {
		note = fmt.Sprintf("…(%d middle line%s elided)…", len(middle), genericPlural(len(middle)))
	}

	out := make([]string, 0, len(head)+1+len(matches)+len(hints)+len(tail))
	out = append(out, head...)
	out = append(out, note)
	out = append(out, matches...)
	out = append(out, hints...)
	out = append(out, tail...)
	for i, l := range out {
		out[i] = clipLine(l, MaxLineChars)
	}
	return strings.Join(out, "\n")
}

// genericHandler is the last-resort fallback for output matching no dedicated
// handler and neither JSON nor CSV. Structure-aware: small output unchanged;
// long multi-line output keeps head+tail lines and surfaces error/warn lines
// from the elided middle; long single-block output keeps a head+tail window.
func genericHandler(_ string, output any) Result {
	return GenericHandler("", output)
}

// GenericHandler is genericHandler, exported for the hook's empty-summary
// fallback (upstream #296).
func GenericHandler(_ string, output any) Result {
	raw := ExtractText(output)
	originalSize := byteLen(raw)

	if runeLen(raw) <= genericMaxChars {
		return Result{Summary: raw, OriginalSize: originalSize}
	}

	// Repeats collapse first (upstream #340), so the head, tail and surfaced
	// error lines are distinct lines; the elided count is then in collapsed
	// lines.
	lines := collapsedTexts(strings.Split(raw, "\n"))
	text := strings.Join(lines, "\n")
	var summary string
	switch {
	case len(lines) >= genericLineModeMin:
		summary = summarizeLines(lines)
	case runeLen(text) <= genericMaxChars:
		summary = text
	default:
		summary = summarizeBlock(text)
	}
	return Result{Summary: summary, OriginalSize: originalSize}
}
