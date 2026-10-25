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

// summarizeLineWindow keeps the first headLines and last tailLines lines, with a
// count of the elided middle and any error/warn lines surfaced from it. Callers
// keep len(lines) > headLines+tailLines so the windows never overlap.
func summarizeLineWindow(lines []string, headLines, tailLines int) string {
	head := lines[:headLines]
	tail := lines[len(lines)-tailLines:]
	middle := lines[headLines : len(lines)-tailLines]
	var matches []string
	for _, l := range middle {
		if genericMatchRe.MatchString(l) {
			matches = append(matches, l)
			if len(matches) >= genericMaxMatch {
				break
			}
		}
	}

	var note string
	if len(matches) > 0 {
		note = fmt.Sprintf("…(%d middle line%s elided; %d error/warn shown)…", len(middle), genericPlural(len(middle)), len(matches))
	} else {
		note = fmt.Sprintf("…(%d middle line%s elided)…", len(middle), genericPlural(len(middle)))
	}

	out := make([]string, 0, len(head)+1+len(matches)+len(tail))
	out = append(out, head...)
	out = append(out, note)
	out = append(out, matches...)
	out = append(out, tail...)
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

	lines := strings.Split(raw, "\n")
	var summary string
	if len(lines) >= genericLineModeMin {
		summary = summarizeLines(lines)
	} else {
		summary = summarizeBlock(raw)
	}
	return Result{Summary: summary, OriginalSize: originalSize}
}
