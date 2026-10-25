// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/handlers/bash_git.go

package handlers

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// diffBodyBytes is the bytes of hunk text a diff summary delivers, shared
// across files. A diff is read for its lines: a stats-only summary kept 5 of the
// 49 added lines Claude went on to quote (upstream #389). Diffs a little over
// this pass through whole, since the hook requires a real saving.
const diffBodyBytes = 4 * 1024

// maxPreambleLines is the lines of `git show`'s commit header (hash, author,
// message) kept before the diff.
const maxPreambleLines = 20

type diffHunk struct {
	lines []string
	bytes int
}

type fileDiff struct {
	path string
	// meta is the `diff --git` line plus mode, rename and binary lines; never
	// index or ---/+++.
	meta      []string
	hunks     []*diffHunk
	additions int
	deletions int
}

func diffLineBytes(line string) int { return len(line) + 1 }

var (
	diffGitPathRe = regexp.MustCompile(`diff --git a/.+ b/(.+)`)
	// git's extended header lines, between `diff --git` and the first hunk.
	diffExtendedHeaderRe = regexp.MustCompile(`^(old mode|new mode|deleted file mode|new file mode|copy (from|to)|rename (from|to)|(dis)?similarity index|Binary files|GIT binary patch)`)
	diffNamedLineRe      = regexp.MustCompile(`^(index |--- |\+\+\+ |\s*$)`)
)

func (f *fileDiff) count(line string) {
	if strings.HasPrefix(line, "+") {
		f.additions++
	} else if strings.HasPrefix(line, "-") {
		f.deletions++
	}
}

// parseGitDiff splits a diff into the text before its first file (git show's
// commit header) and per-file hunks.
func parseGitDiff(text string) (preamble []string, files []*fileDiff) {
	var file *fileDiff
	var hunk *diffHunk
	binary := false
	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			path := line[len("diff --git "):]
			if m := diffGitPathRe.FindStringSubmatch(line); m != nil {
				path = m[1]
			}
			file = &fileDiff{path: path, meta: []string{line}}
			files = append(files, file)
			hunk = nil
			binary = false
		case file == nil:
			preamble = append(preamble, line)
		case binary || (hunk == nil && diffNamedLineRe.MatchString(line)):
			// A --binary patch's encoded data, a line the diff --git line
			// already names, or a blank.
		case hunk == nil && diffExtendedHeaderRe.MatchString(line):
			file.meta = append(file.meta, line)
			binary = strings.HasPrefix(line, "GIT binary patch")
		case strings.HasPrefix(line, "@@") || hunk == nil:
			// Anything else before a hunk header opens one, so it stays inside
			// the budget.
			hunk = &diffHunk{lines: []string{line}, bytes: diffLineBytes(line)}
			file.hunks = append(file.hunks, hunk)
			if !strings.HasPrefix(line, "@@") {
				file.count(line)
			}
		default:
			hunk.lines = append(hunk.lines, line)
			hunk.bytes += diffLineBytes(line)
			file.count(line)
		}
	}
	return preamble, files
}

// fairShares splits budget so no file gets more than it needs and the rest
// share what is left.
func fairShares(sizes []int, budget int) []int {
	shares := make([]int, len(sizes))
	order := make([]int, len(sizes))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return sizes[order[a]] < sizes[order[b]] })
	left := budget
	for k, i := range order {
		shares[i] = min(sizes[i], left/(len(order)-k))
		left -= shares[i]
	}
	return shares
}

// renderFile is the file's whole hunks, in order, that fit share, then as much
// of the next hunk's head as still fits. A file whose share holds no hunk line
// is left to the per-file overview, so a many-file diff costs no more than that
// list.
func renderFile(file *fileDiff, share int) []string {
	out := append([]string{}, file.meta...)
	used, whole := 0, 0
	for whole < len(file.hunks) && used+file.hunks[whole].bytes <= share {
		out = append(out, file.hunks[whole].lines...)
		used += file.hunks[whole].bytes
		whole++
	}
	if whole == len(file.hunks) {
		return out
	}
	var fits []string
	for _, line := range file.hunks[whole].lines {
		if used+diffLineBytes(line) > share {
			break
		}
		fits = append(fits, line)
		used += diffLineBytes(line)
	}
	// A hunk header with none of its lines shows nothing.
	var partial []string
	if len(fits) > 1 {
		partial = fits
	}
	if whole == 0 && len(partial) == 0 {
		return nil
	}
	// A cut hunk counts as elided, with only its unshown lines in the stats.
	var hidden []string
	for _, h := range file.hunks[whole:] {
		hidden = append(hidden, h.lines...)
	}
	hidden = hidden[len(partial):]
	add, del := 0, 0
	for _, l := range hidden {
		if strings.HasPrefix(l, "+") {
			add++
		} else if strings.HasPrefix(l, "-") {
			del++
		}
	}
	n := len(file.hunks) - whole
	out = append(out, partial...)
	return append(out, fmt.Sprintf("  … %d more %s (+%d -%d) in %s", n, plural(n, "hunk", "hunks"), add, del, file.path))
}

func gitDiffHandler(toolName string, output any) Result {
	stdout := extractStdout(output)
	originalSize := byteLen(BashOutputText(output))

	if strings.TrimSpace(stdout) == "" {
		return Result{Summary: "[git diff — no changes]", OriginalSize: originalSize}
	}

	preamble, files := parseGitDiff(stdout)
	if len(files) == 0 {
		// Binary diff, submodule change, or unusual output — fall back to shell
		return shellHandler(toolName, output)
	}

	totalAdded, totalDeleted := 0, 0
	for _, f := range files {
		totalAdded += f.additions
		totalDeleted += f.deletions
	}
	header := fmt.Sprintf("git diff — %d %s changed, +%d -%d", len(files), plural(len(files), "file", "files"), totalAdded, totalDeleted)

	sizes := make([]int, len(files))
	for i, f := range files {
		for _, h := range f.hunks {
			sizes[i] += h.bytes
		}
	}
	shares := fairShares(sizes, diffBodyBytes)

	lines := []string{header}
	// The per-file list only adds what the body left out; on a whole body it
	// is overhead.
	cutAny := false
	for i := range files {
		cutAny = cutAny || shares[i] < sizes[i]
	}
	if cutAny {
		for _, f := range files {
			stats := fmt.Sprintf("+%d -%d", f.additions, f.deletions)
			hunks := fmt.Sprintf("(%d %s)", len(f.hunks), plural(len(f.hunks), "hunk", "hunks"))
			lines = append(lines, fmt.Sprintf("  %s  %s  %s", padEnd(f.path, 48), padEnd(stats, 10), hunks))
		}
		lines = append(lines, "")
	}
	if head := strings.TrimSpace(strings.Join(preamble, "\n")); head != "" {
		headLines := strings.Split(head, "\n")
		if cut := len(headLines) - maxPreambleLines; cut > 0 {
			headLines = append(headLines[:maxPreambleLines:maxPreambleLines], fmt.Sprintf("  … %d more commit header lines", cut))
		}
		lines = append(append(lines, headLines...), "")
	}
	for i, f := range files {
		lines = append(lines, renderFile(f, shares[i])...)
	}
	return Result{Summary: strings.Join(lines, "\n"), OriginalSize: originalSize}
}

var gitLogOnelineRe = regexp.MustCompile(`^[0-9a-f]{6,40}\s`)

func gitLogHandler(toolName string, output any) Result {
	stdout := extractStdout(output)
	originalSize := byteLen(BashOutputText(output))

	trimmed := strings.TrimSpace(stdout)
	var lines []string
	for _, l := range strings.Split(trimmed, "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		return Result{Summary: "[git log — no commits]", OriginalSize: originalSize}
	}

	isOneline := true
	for _, l := range lines {
		if !gitLogOnelineRe.MatchString(strings.TrimSpace(l)) {
			isOneline = false
			break
		}
	}

	if isOneline {
		total := len(lines)
		n := total
		if n > maxLogCommits {
			n = maxLogCommits
		}
		var shown []string
		for _, l := range lines[:n] {
			shown = append(shown, "  "+l)
		}
		overflow := ""
		if total > maxLogCommits {
			overflow = fmt.Sprintf("\n… (+%d more commits)", total-maxLogCommits)
		}
		plural := "s"
		if total == 1 {
			plural = ""
		}
		summary := fmt.Sprintf("git log — %d commit%s\n%s%s", total, plural, strings.Join(shown, "\n"), overflow)
		return Result{Summary: summary, OriginalSize: originalSize}
	}

	// full format
	var commits []string
	hash := ""
	seenBlank := false
	for _, line := range strings.Split(stdout, "\n") {
		switch {
		case strings.HasPrefix(line, "commit "):
			hash = firstChars(line[len("commit "):], 7)
			seenBlank = false
		case hash != "" && !seenBlank && (strings.HasPrefix(line, "Author:") ||
			strings.HasPrefix(line, "Date:") ||
			strings.HasPrefix(line, "Merge:") ||
			strings.HasPrefix(line, "gpgsig ") ||
			(strings.HasPrefix(line, " ") && strings.TrimSpace(line) != "")):
			// skip git metadata headers before the blank separator
		case hash != "" && strings.TrimSpace(line) == "" && !seenBlank:
			seenBlank = true
		case hash != "" && seenBlank && strings.HasPrefix(line, "    ") && strings.TrimSpace(line) != "":
			subject := firstChars(strings.TrimSpace(line), 72)
			commits = append(commits, fmt.Sprintf("  %s  %s", hash, subject))
			hash = ""
			seenBlank = false
		}
	}

	// A custom --format/--pretty parses to nothing; a count is only stated
	// when parsed (upstream #350).
	if len(commits) == 0 {
		return shellHandler(toolName, output)
	}

	total := len(commits)
	n := total
	if n > maxLogCommits {
		n = maxLogCommits
	}
	shown := commits[:n]
	overflow := ""
	if total > maxLogCommits {
		overflow = fmt.Sprintf("\n… (+%d more commits)", total-maxLogCommits)
	}
	plural := "s"
	if total == 1 {
		plural = ""
	}
	header := fmt.Sprintf("git log — %d commit%s", total, plural)
	out := append([]string{header}, shown...)
	return Result{Summary: strings.Join(out, "\n") + overflow, OriginalSize: originalSize}
}

// maxStatusPaths is the paths listed per category before the rest are
// counted. git status is usually short, and the paths are what Claude acts on
// (a 5-path cap dropped them, upstream #387).
const maxStatusPaths = 50

var (
	// Unmerged pairs in porcelain v1 (git-status(1), "Short Format").
	conflictCodes  = map[string]bool{"DD": true, "AU": true, "UD": true, "UA": true, "DU": true, "AA": true, "UU": true}
	longConflictRe = regexp.MustCompile(`^(both modified|both added|both deleted|added by us|added by them|deleted by us|deleted by them):\s*`)
	longEntryRe    = regexp.MustCompile(`^(modified|new file|deleted|renamed|copied|typechange):\s*`)
	// Kept verbatim: the branch, its upstream and ahead/behind are often why
	// status was run.
	branchLineRe = regexp.MustCompile(`^(On branch |HEAD detached |Your branch |## )`)
	porcelainRe  = regexp.MustCompile(`^([ MTADRCU?!])([ MTADRCU?!]) (.+)$`)
)

type statusParts struct {
	branch, staged, unstaged, untracked, conflicts []string
}

// parsePorcelain parses one porcelain v1 line: two status columns, a space,
// the path. Matched on the untrimmed line, since a blank first column
// (" M file", changed but unstaged) is the most common entry.
func parsePorcelain(line string, parts *statusParts) bool {
	m := porcelainRe.FindStringSubmatch(line)
	if m == nil {
		return false
	}
	x, y, path := m[1], m[2], m[3]
	switch {
	case conflictCodes[x+y]:
		parts.conflicts = append(parts.conflicts, x+y+" "+path)
	case x == "?":
		parts.untracked = append(parts.untracked, path)
	case x != "!":
		if x != " " {
			parts.staged = append(parts.staged, x+" "+path)
		}
		if y != " " {
			parts.unstaged = append(parts.unstaged, y+" "+path)
		}
	}
	return true
}

func parseStatus(stdout string) statusParts {
	var parts statusParts
	section := ""
	for _, line := range strings.Split(stdout, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case branchLineRe.MatchString(trimmed):
			parts.branch = append(parts.branch, trimmed)
			continue
		case strings.HasPrefix(trimmed, "Changes to be committed"):
			section = "staged"
			continue
		case strings.HasPrefix(trimmed, "Changes not staged"):
			section = "unstaged"
			continue
		case strings.HasPrefix(trimmed, "Untracked files"):
			section = "untracked"
			continue
		case strings.HasPrefix(trimmed, "Unmerged paths"):
			section = "unmerged"
			continue
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "(") || strings.HasPrefix(trimmed, "no changes") || strings.HasPrefix(trimmed, "nothing") {
			continue
		}
		// Long-format entries are tab-indented, so a porcelain line never
		// starts with a tab.
		if !strings.HasPrefix(line, "\t") && parsePorcelain(line, &parts) {
			continue
		}
		switch {
		case longConflictRe.MatchString(trimmed):
			parts.conflicts = append(parts.conflicts, longConflictRe.ReplaceAllString(trimmed, ""))
		case section == "staged" && longEntryRe.MatchString(trimmed):
			parts.staged = append(parts.staged, longEntryRe.ReplaceAllString(trimmed, ""))
		case section == "unstaged" && longEntryRe.MatchString(trimmed):
			parts.unstaged = append(parts.unstaged, longEntryRe.ReplaceAllString(trimmed, ""))
		case section == "untracked":
			parts.untracked = append(parts.untracked, trimmed)
		}
	}
	return parts
}

func pathList(label string, paths []string) string {
	return fmt.Sprintf("  %s (%d): %s%s", label, len(paths),
		strings.Join(firstN(paths, maxStatusPaths), ", "), moreSuffix(len(paths), maxStatusPaths))
}

func gitStatusHandler(toolName string, output any) Result {
	stdout := extractStdout(output)
	originalSize := byteLen(BashOutputText(output))

	if strings.TrimSpace(stdout) == "" {
		return Result{Summary: "[git status — clean working tree]", OriginalSize: originalSize}
	}

	p := parseStatus(stdout)
	if len(p.staged) == 0 && len(p.unstaged) == 0 && len(p.untracked) == 0 && len(p.conflicts) == 0 {
		// Couldn't parse — fall back to shell
		return shellHandler(toolName, output)
	}

	lines := []string{"git status"}
	for _, b := range p.branch {
		lines = append(lines, "  "+b)
	}
	for _, c := range []struct {
		label string
		paths []string
	}{{"conflicts", p.conflicts}, {"staged", p.staged}, {"unstaged", p.unstaged}, {"untracked", p.untracked}} {
		if len(c.paths) > 0 {
			lines = append(lines, pathList(c.label, c.paths))
		}
	}
	return Result{Summary: strings.Join(lines, "\n"), OriginalSize: originalSize}
}

func firstN(xs []string, n int) []string {
	if len(xs) < n {
		return xs
	}
	return xs[:n]
}

func moreSuffix(total, shown int) string {
	if total > shown {
		return fmt.Sprintf(" +%d more", total-shown)
	}
	return ""
}

// ── git branch / stash list / remote -v — long ref listings ───────────────────

const maxRefs = 25

var (
	stashEntryRe   = regexp.MustCompile(`^stash@\{\d+\}:`)
	remoteKindRe   = regexp.MustCompile(`\((fetch|push)\)\s*$`)
	remoteFetchRe  = regexp.MustCompile(`^(\S+)\s+(\S+)\s+\(fetch\)`)
	branchMarkerRe = regexp.MustCompile(`^[*+]?\s*`)
	whitespaceRe   = regexp.MustCompile(`\s`)
)

func gitRefsHandler(toolName string, output any) Result {
	stdout := extractStdout(output)
	originalSize := byteLen(BashOutputText(output))
	var lines []string
	for _, l := range strings.Split(stdout, "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		return shellHandler(toolName, output)
	}

	refLines := func(header string, items []string, width int) Result {
		out := []string{header}
		for i, l := range items {
			if i >= maxRefs {
				break
			}
			out = append(out, "  "+firstChars(strings.TrimSpace(l), width))
		}
		if len(items) > maxRefs {
			out = append(out, fmt.Sprintf("  … (+%d more)", len(items)-maxRefs))
		}
		return Result{Summary: strings.Join(out, "\n"), OriginalSize: originalSize}
	}

	// git stash list: "stash@{0}: WIP on ..."
	stashes := 0
	for _, l := range lines {
		if stashEntryRe.MatchString(strings.TrimSpace(l)) {
			stashes++
		}
	}
	if stashes >= ceilHalf(len(lines)) {
		word := "entries"
		if len(lines) == 1 {
			word = "entry"
		}
		return refLines(fmt.Sprintf("git stash — %d %s", len(lines), word), lines, 100)
	}

	// git remote -v: "origin\tgit@...\t(fetch)"
	kinds := 0
	for _, l := range lines {
		if remoteKindRe.MatchString(l) {
			kinds++
		}
	}
	if kinds >= ceilHalf(len(lines)) {
		var names []string
		urls := map[string]string{}
		for _, l := range lines {
			if m := remoteFetchRe.FindStringSubmatch(l); m != nil {
				if _, seen := urls[m[1]]; !seen {
					names = append(names, m[1])
				}
				urls[m[1]] = m[2]
			}
		}
		out := []string{fmt.Sprintf("git remote — %d %s", len(names), plural(len(names), "remote", "remotes"))}
		for i, n := range names {
			if i >= maxRefs {
				break
			}
			out = append(out, fmt.Sprintf("  %s → %s", n, firstChars(urls[n], 80)))
		}
		if len(names) > maxRefs {
			out = append(out, fmt.Sprintf("  … (+%d more)", len(names)-maxRefs))
		}
		return Result{Summary: strings.Join(out, "\n"), OriginalSize: originalSize}
	}

	// git branch: "* main" / "  feat/x" / "  remotes/origin/y". A branch line is
	// the leading marker (`* `/`+ `/spaces) followed by a single whitespace-free ref.
	var branchLines []string
	remotes := 0
	for _, l := range lines {
		ref := strings.TrimSpace(branchMarkerRe.ReplaceAllString(l, ""))
		if ref == "" || whitespaceRe.MatchString(ref) {
			continue
		}
		branchLines = append(branchLines, l)
		if strings.Contains(l, "remotes/") {
			remotes++
		}
	}
	if len(branchLines) >= ceilHalf(len(lines)) {
		current := ""
		for _, l := range lines {
			if strings.HasPrefix(strings.TrimLeft(l, " \t"), "* ") {
				current = strings.TrimSpace(strings.TrimPrefix(strings.TrimLeft(l, " \t"), "*"))
				break
			}
		}
		header := fmt.Sprintf("git branch — %d local", len(branchLines)-remotes)
		if remotes > 0 {
			header += fmt.Sprintf(", %d remote", remotes)
		}
		if current != "" {
			header += fmt.Sprintf(" (on %s)", current)
		}
		return refLines(header, branchLines, 80)
	}

	return shellHandler(toolName, output)
}
