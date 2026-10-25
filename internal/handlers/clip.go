// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/handlers/clip.go

package handlers

import (
	"fmt"
	"regexp"
	"unicode/utf8"
)

// MaxLineChars caps the width of a summary line (upstream #349). Handlers cap
// how many lines a summary keeps, never how wide they are, so one huge line (a
// minified bundle hit by grep, a serialized blob) was delivered whole. Wide
// enough that prose and JSON lines a summary already showed survive.
const MaxLineChars = 4000

var clipMarkerRe = regexp.MustCompile(`(?m)… \(\+\d+ chars\)$`)

// utf16Len is the length of s in UTF-16 code units, as JavaScript counts it, so
// the cap and the elided count match upstream.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// clipLine cuts line to max UTF-16 units plus `… (+N chars)`, or returns it
// unchanged when it fits. A character is never split, so the cut may fall one
// unit short of max.
func clipLine(line string, max int) string {
	if len(line) <= max { // bytes >= UTF-16 units
		return line
	}
	total := utf16Len(line)
	if total <= max {
		return line
	}
	units, end := 0, 0
	for end < len(line) {
		r, size := utf8.DecodeRuneInString(line[end:])
		w := 1
		if r >= 0x10000 {
			w = 2
		}
		if units+w > max {
			break
		}
		units += w
		end += size
	}
	return fmt.Sprintf("%s… (+%d chars)", line[:end], total-units)
}

// HasClippedLine reports whether some line of summary was cut by clipLine.
// Balanced retention keeps such a summary's body, so the marker never points
// at content that is gone.
func HasClippedLine(summary string) bool {
	return clipMarkerRe.MatchString(summary)
}
