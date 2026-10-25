// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/handlers/bash_search.go
//
// Search / listing handlers — grep|rg, ls, find|fd. These commands emit a list
// of items (matches, entries, paths) whose bulk is length, not structure.
// Ports src/handlers/bash-search.ts.
//
// Safety: the user usually wants the items, so these NEVER drop items silently —
// they always report the total count and show a generous capped sample with an
// explicit overflow line, and fall back to the shell handler when the output
// doesn't match the expected shape. The full output stays retrievable via
// recall__*.
//
// Budget: summaries are sized to the generic shell cap — the sample shrinks
// until it fits, and if shell already kept every line the handler defers to it.
// These never compress worse than the fallback they replace (upstream #262).

package handlers

import (
	"fmt"
	"regexp"
	"strings"
)

const maxSearchSample = 40

// clipSearch truncates with an ellipsis, unlike firstChars which truncates bare.
func clipSearch(s string, n int) string {
	if runeLen(s) > n {
		return firstChars(s, n) + "…"
	}
	return s
}

func overflowLine(total, shown int, noun string) []string {
	if total > shown {
		return []string{fmt.Sprintf("  … (+%d more %s)", total-shown, noun)}
	}
	return nil
}

func stdoutLineCount(output any) int {
	lines := strings.Split(extractStdout(output), "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return len(lines)
}

// fitUnderFallback keeps a specialised summary only when it is no larger than
// the shell handler's. If it overshoots and shell already kept every line, it
// defers so cheap-to-show items aren't hidden; otherwise it binary-searches the
// largest sample that fits.
func fitUnderFallback(toolName string, output any, total int, build func(shown int) Result) Result {
	fallback := shellHandler(toolName, output)
	budget := len(fallback.Summary)
	maxShown := min(maxSearchSample, total)
	fits := func(n int) bool { return len(build(n).Summary) <= budget }

	if fits(maxShown) {
		return build(maxShown)
	}
	if stdoutLineCount(output) <= headStdout || !fits(0) {
		return fallback
	}
	best, lo, hi := 0, 0, maxShown
	for lo <= hi {
		mid := (lo + hi) / 2
		if fits(mid) {
			best = mid
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	if candidate := build(best); len(candidate.Summary) <= budget {
		return candidate
	}
	return fallback
}

// sampleSummary is a header, the first shown items (each passed through line),
// and an overflow line for the rest.
func sampleSummary(header string, items []string, shown int, noun string, line func(string) string, originalSize int) Result {
	out := []string{header}
	for _, it := range items[:min(shown, len(items))] {
		out = append(out, line(it))
	}
	out = append(out, overflowLine(len(items), shown, noun)...)
	return Result{Summary: strings.Join(out, "\n"), OriginalSize: originalSize}
}

// ── grep / ripgrep — inline "file:line:content" form (piped, --no-heading) ─────

var grepLineRe = regexp.MustCompile(`^(.+?):(\d+):(.*)$`)

func grepHandler(toolName string, output any) Result {
	stdout := extractStdout(output)
	originalSize := byteLen(BashOutputText(output))

	var lines []string
	for _, l := range strings.Split(stdout, "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		return Result{Summary: "[grep — no matches]", OriginalSize: originalSize}
	}

	type match struct{ file, line, text string }
	var matches []match
	files := map[string]bool{}
	for _, l := range lines {
		if m := grepLineRe.FindStringSubmatch(l); m != nil {
			matches = append(matches, match{m[1], m[2], m[3]})
			files[m[1]] = true
		}
	}

	// Not predominantly file:line:content (grouped rg, plain grep, binary) — the
	// shell handler's 25-line cap is safer than a wrong parse.
	if len(matches) < ceilHalf(len(lines)) {
		return shellHandler(toolName, output)
	}

	header := fmt.Sprintf("grep — %d %s in %d %s",
		len(matches), plural(len(matches), "match", "matches"),
		len(files), plural(len(files), "file", "files"))
	formatted := make([]string, len(matches))
	for i, m := range matches {
		formatted[i] = fmt.Sprintf("  %s:%s: %s", m.file, m.line, clipSearch(strings.TrimSpace(m.text), 100))
	}
	return fitUnderFallback(toolName, output, len(matches), func(shown int) Result {
		return sampleSummary(header, formatted, shown, "matches", func(s string) string { return s }, originalSize)
	})
}

// ── ls — long (-l), recursive (-R), and plain forms ───────────────────────────

var (
	lsLongRe             = regexp.MustCompile(`^([-dlbcps])[rwxsStT-]{9}[+@.]?\s+\d+\s`)
	lsRecursiveHeaderRe  = regexp.MustCompile(`^(\.?[^\s:]*):$`)
	lsTotalRe            = regexp.MustCompile(`^total\s+\d+$`)
	lsColumnSeparatorRe  = regexp.MustCompile(`\s{2,}|\t`)
	findDiagnosticLineRe = regexp.MustCompile(`^find: `)
)

func lsHandler(toolName string, output any) Result {
	stdout := extractStdout(output)
	originalSize := byteLen(BashOutputText(output))

	raw := strings.Split(stdout, "\n")
	var nonEmpty []string
	for _, l := range raw {
		if strings.TrimSpace(l) != "" {
			nonEmpty = append(nonEmpty, l)
		}
	}
	if len(nonEmpty) == 0 {
		return Result{Summary: "[ls — empty]", OriginalSize: originalSize}
	}

	// Recursive: directory-header lines like "./src:" grouping entries. `ls -R`
	// always blank-line-separates directory blocks; a plain listing that merely
	// happens to contain colon-suffixed names has no such separator, so require
	// one to avoid misreading a plain listing as recursive (and hiding files).
	var dirHeaders []string
	totals := 0
	for _, l := range nonEmpty {
		trimmed := strings.TrimSpace(l)
		if lsRecursiveHeaderRe.MatchString(trimmed) {
			dirHeaders = append(dirHeaders, l)
		}
		if lsTotalRe.MatchString(trimmed) {
			totals++
		}
	}
	hasBlankSeparator := false
	for _, l := range raw {
		if strings.TrimSpace(l) == "" {
			hasBlankSeparator = true
			break
		}
	}
	if len(dirHeaders) >= 2 && hasBlankSeparator {
		entries := len(nonEmpty) - len(dirHeaders) - totals
		header := fmt.Sprintf("ls -R — %d directories, ~%d entries", len(dirHeaders), entries)
		return fitUnderFallback(toolName, output, len(dirHeaders), func(shown int) Result {
			return sampleSummary(header, dirHeaders, shown, "directories",
				func(d string) string { return "  " + strings.TrimSpace(d) }, originalSize)
		})
	}

	// Long format: perm-string lines. Count dirs vs files.
	var longLines []string
	for _, l := range nonEmpty {
		if lsLongRe.MatchString(l) {
			longLines = append(longLines, l)
		}
	}
	if len(longLines) >= ceilHalf(len(nonEmpty)) {
		dirs := 0
		var names []string
		for _, l := range longLines {
			if m := lsLongRe.FindStringSubmatch(l); m != nil && m[1] == "d" {
				dirs++
			}
			fields := strings.Fields(l)
			if len(fields) > 8 {
				if name := strings.Join(fields[8:], " "); name != "" {
					names = append(names, name)
				}
			}
		}
		files := len(longLines) - dirs
		header := fmt.Sprintf("ls — %d entries (%d %s, %d %s)",
			len(longLines), dirs, plural(dirs, "dir", "dirs"), files, plural(files, "file", "files"))
		return fitUnderFallback(toolName, output, len(names), func(shown int) Result {
			return sampleSummary(header, names, shown, "entries",
				func(n string) string { return "  " + clipSearch(n, 100) }, originalSize)
		})
	}

	// Plain listing: names one-per-line or column-wrapped. Flatten to tokens.
	var tokens []string
	for _, l := range nonEmpty {
		for _, tok := range lsColumnSeparatorRe.Split(l, -1) {
			if tok = strings.TrimSpace(tok); tok != "" {
				tokens = append(tokens, tok)
			}
		}
	}
	if len(tokens) < 2 {
		return shellHandler(toolName, output)
	}
	header := fmt.Sprintf("ls — %d entries", len(tokens))
	return fitUnderFallback(toolName, output, len(tokens), func(shown int) Result {
		return sampleSummary(header, tokens, shown, "entries",
			func(n string) string { return "  " + clipSearch(n, 100) }, originalSize)
	})
}

// ── find / fd — one path per line ─────────────────────────────────────────────

func findHandler(toolName string, output any) Result {
	stdout := extractStdout(output)
	originalSize := byteLen(BashOutputText(output))

	var paths []string
	for _, l := range strings.Split(stdout, "\n") {
		if l = strings.TrimRight(l, " \t\r"); l != "" {
			paths = append(paths, l)
		}
	}
	if len(paths) == 0 {
		return Result{Summary: "[find — no results]", OriginalSize: originalSize}
	}
	// Guard against non-find output (e.g. find errors / prompts): require most
	// lines to look like paths (no leading whitespace, no "find: " marker).
	pathLike := 0
	for _, p := range paths {
		if !strings.HasPrefix(p, " ") && !strings.HasPrefix(p, "\t") && !findDiagnosticLineRe.MatchString(p) {
			pathLike++
		}
	}
	if pathLike < ceilFraction(len(paths), 0.7) {
		return shellHandler(toolName, output)
	}

	header := fmt.Sprintf("find — %d %s", len(paths), plural(len(paths), "path", "paths"))
	return fitUnderFallback(toolName, output, len(paths), func(shown int) Result {
		return sampleSummary(header, paths, shown, "paths",
			func(p string) string { return "  " + clipSearch(p, 120) }, originalSize)
	})
}

// ceilHalf is Math.ceil(n * 0.5).
func ceilHalf(n int) int { return (n + 1) / 2 }

// ceilFraction is Math.ceil(n * f) for the fractions used above.
func ceilFraction(n int, f float64) int {
	v := float64(n) * f
	i := int(v)
	if float64(i) < v {
		i++
	}
	return i
}
