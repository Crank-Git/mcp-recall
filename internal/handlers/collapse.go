// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/handlers/collapse.go

package handlers

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Collapses runs of repeated lines (upstream #340), so a fallback summary
// spends its line budget on distinct lines instead of a retry loop's
// hundredth copy.
//
// Two lines repeat when they are equal, or when both are log lines (see
// repeatKey) equal once volatile tokens are masked: timestamps, bracketed PIDs
// (`sshd[812]`), UUIDs, mixed hex ids of 8+ chars, and durations (`12ms`,
// `1.5s`). Nothing else is masked, so lines that differ in a path, a status
// code or a bare counter (`retry 1`, `retry 2`) stay apart. Only consecutive
// lines collapse: interleaved repeats keep their order.
//
// Under balanced retention a fallback row may keep no body, so the summary is
// the only copy. Masking is kept narrow for that reason.

var timestampRes = []*regexp.Regexp{
	regexp.MustCompile(`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:[.,]\d+)?(?:Z|[+-]\d{2}:?\d{2})?`),
	regexp.MustCompile(`\b(?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec) [ \d]\d \d{2}:\d{2}:\d{2}(?:\.\d+)?`),
	regexp.MustCompile(`\b\d{2}:\d{2}:\d{2}(?:[.,]\d+)?\b`),
}

var (
	pidRe  = regexp.MustCompile(`\[\d+\]`)
	uuidRe = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	// Upstream requires a digit and a letter by lookahead; RE2 has none, so
	// repeatKey checks each match instead.
	hexIDRe     = regexp.MustCompile(`\b[0-9a-f]{8,}\b`)
	hexDigitRe  = regexp.MustCompile(`\d`)
	hexLetterRe = regexp.MustCompile(`[a-f]`)
	durationRe  = regexp.MustCompile(`\b\d+(?:\.\d+)?(?:ms|µs|us|ns|s)\b`)
	hhmmssRe    = regexp.MustCompile(`\d\d:\d\d:\d\d`)
)

// A log line puts its timestamp first, after at most a level, host or count
// (`ERROR 2026-…`, `host 2026-…`, `1 time="…"`).
const maxWordsBeforeTimestamp = 2

// logPrefixChars is where a log line's timestamp must start; it also bounds
// the search per line.
const logPrefixChars = 64

// runePrefix is the first n characters of s, sliced in place so byte offsets
// into it are offsets into s; it never decodes past them, which matters on a
// 900 KB line.
func runePrefix(s string, n int) string {
	i := 0
	for k := 0; k < n && i < len(s); k++ {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	return s[:i]
}

// isLogLine reports whether line opens with a timestamp, as a log line does.
// A `|` before it marks a table row (`2026-09-26 | 11:00:00 | closed`), whose
// times are data.
func isLogLine(line string) bool {
	head := runePrefix(line, logPrefixChars)
	// Every timestamp form holds an HH:MM:SS: one cheap test rules most lines out.
	if !hhmmssRe.MatchString(head) {
		return false
	}
	// Leftmost-first: the match starts where the earliest timestamp form does.
	loc := anyTimestampRe.FindStringIndex(head)
	if loc == nil {
		return false
	}
	start := loc[0]
	prefix := strings.TrimSpace(line[:start])
	if strings.Contains(prefix, "|") {
		return false
	}
	return prefix == "" || len(strings.Fields(prefix)) <= maxWordsBeforeTimestamp
}

// anyTimestampRe matches any of timestampRes, starting at the earliest.
var anyTimestampRe = regexp.MustCompile(
	timestampRes[0].String() + `|` + timestampRes[1].String() + `|` + timestampRes[2].String())

// volatileRe matches every volatile token in one pass, in upstream's masking
// order: the timestamp forms, a bracketed PID, a UUID, a hex id, a duration.
// Go's alternation is leftmost-first, so where two match at one position the
// earlier wins, as when upstream masks them one after another; one pass is
// several times faster than seven on a large log.
var volatileRe = regexp.MustCompile(
	anyTimestampRe.String() + `|(?P<pid>` + pidRe.String() + `)|(?P<uuid>` + uuidRe.String() + `)|(?P<hex>` + hexIDRe.String() +
		`)|(?P<dur>` + durationRe.String() + `)`)

var (
	pidGroup  = volatileRe.SubexpIndex("pid")
	uuidGroup = volatileRe.SubexpIndex("uuid")
	hexGroup  = volatileRe.SubexpIndex("hex")
	durGroup  = volatileRe.SubexpIndex("dur")
)

// repeatKey returns line with its volatile tokens masked: the key two repeats
// share. Only a log line is masked; in any other line a timestamp or id may be
// the data itself (a list of booking slots, of commits), so it must match
// exactly.
func repeatKey(line string) string {
	if !isLogLine(line) {
		return line
	}
	var b strings.Builder
	last := 0
	for _, m := range volatileRe.FindAllStringSubmatchIndex(line, -1) {
		tok := line[m[0]:m[1]]
		var mask string
		switch {
		case m[2*pidGroup] >= 0:
			mask = "[<pid>]"
		case m[2*uuidGroup] >= 0:
			mask = "<uuid>"
		case m[2*hexGroup] >= 0:
			// Upstream requires a digit and a letter by lookahead, so a word
			// like `deadbeef` or a plain number is never masked.
			if !hexDigitRe.MatchString(tok) || !hexLetterRe.MatchString(tok) {
				continue
			}
			mask = "<hex>"
		case m[2*durGroup] >= 0:
			mask = "<dur>"
		default:
			mask = "<ts>"
		}
		b.WriteString(line[last:m[0]])
		b.WriteString(mask)
		last = m[1]
	}
	if last == 0 {
		return line
	}
	b.WriteString(line[last:])
	return b.String()
}

func lastTimestamp(line string) (string, bool) {
	for _, re := range timestampRes {
		if all := re.FindAllString(line, -1); len(all) > 0 {
			return all[len(all)-1], true
		}
	}
	return "", false
}

// collapsedLine is one summary line and how many input lines it stands for.
type collapsedLine struct {
	text  string
	count int
}

// noLimit stands for upstream's Infinity in collapseRepeats' limit and width.
const noLimit = math.MaxInt

// collapseRepeats returns lines with each run of 2+ repeats replaced by its
// first line and a count: `(×N)` when the run is identical, `(×N, until <ts>)`
// when only volatile tokens differ and the last line has a timestamp, `(×N,
// varying ids)` otherwise. Blank lines are never collapsed. Stops after limit
// summary lines, for a caller that keeps only the head. Each line is clipped
// to width before its count is appended, so a clipped run keeps its `(×N)`
// (upstream #349).
func collapseRepeats(lines []string, limit, width int) []collapsedLine {
	var out []collapsedLine
	i := 0
	for i < len(lines) && len(out) < limit {
		first := lines[i]
		end := i + 1
		if strings.TrimSpace(first) != "" {
			key := repeatKey(first)
			for end < len(lines) && (lines[end] == first || repeatKey(lines[end]) == key) {
				end++
			}
		}
		text := clipLine(first, width)
		if end-i > 1 {
			text += "  " + runNote(lines[i:end])
		}
		out = append(out, collapsedLine{text: text, count: end - i})
		i = end
	}
	return out
}

// collapsedTexts is collapseRepeats with no limit or width, as text only.
func collapsedTexts(lines []string) []string {
	shown := collapseRepeats(lines, noLimit, noLimit)
	out := make([]string, len(shown))
	for i, l := range shown {
		out[i] = l.text
	}
	return out
}

func runNote(run []string) string {
	identical := true
	for _, l := range run {
		if l != run[0] {
			identical = false
			break
		}
	}
	if identical {
		return fmt.Sprintf("(×%d)", len(run))
	}
	ts, ok := lastTimestamp(run[len(run)-1])
	firstTS, firstOK := lastTimestamp(run[0])
	if ok && (!firstOK || ts != firstTS) {
		return fmt.Sprintf("(×%d, until %s)", len(run), ts)
	}
	return fmt.Sprintf("(×%d, varying ids)", len(run))
}
