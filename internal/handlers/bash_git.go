// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/handlers/bash_git.go

package handlers

import (
	"fmt"
	"regexp"
	"strings"
)

type fileDiff struct {
	path      string
	additions int
	deletions int
	hunks     int
}

var diffGitPathRe = regexp.MustCompile(`diff --git a/.+ b/(.+)`)

func parseGitDiff(text string) []fileDiff {
	var files []fileDiff
	var current *fileDiff

	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			if current != nil {
				files = append(files, *current)
			}
			path := line[len("diff --git "):]
			if m := diffGitPathRe.FindStringSubmatch(line); m != nil {
				path = m[1]
			}
			current = &fileDiff{path: path}
		case current != nil:
			switch {
			case strings.HasPrefix(line, "@@ "):
				current.hunks++
			case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
				current.additions++
			case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
				current.deletions++
			}
		}
	}
	if current != nil {
		files = append(files, *current)
	}
	return files
}

func gitDiffHandler(toolName string, output any) Result {
	stdout := extractStdout(output)
	originalSize := byteLen(BashOutputText(output))

	if strings.TrimSpace(stdout) == "" {
		return Result{Summary: "[git diff — no changes]", OriginalSize: originalSize}
	}

	files := parseGitDiff(stdout)
	if len(files) == 0 {
		return shellHandler(toolName, output)
	}

	totalAdded, totalDeleted := 0, 0
	for _, f := range files {
		totalAdded += f.additions
		totalDeleted += f.deletions
	}
	filePlural := "s"
	if len(files) == 1 {
		filePlural = ""
	}
	header := fmt.Sprintf("git diff — %d file%s changed, +%d -%d", len(files), filePlural, totalAdded, totalDeleted)

	lines := []string{header}
	for _, f := range files {
		stats := fmt.Sprintf("+%d -%d", f.additions, f.deletions)
		hunkPlural := "s"
		if f.hunks == 1 {
			hunkPlural = ""
		}
		hunks := fmt.Sprintf("(%d hunk%s)", f.hunks, hunkPlural)
		lines = append(lines, fmt.Sprintf("  %s  %s  %s", padEnd(f.path, 48), padEnd(stats, 10), hunks))
	}
	return Result{Summary: strings.Join(lines, "\n"), OriginalSize: originalSize}
}

var gitLogOnelineRe = regexp.MustCompile(`^[0-9a-f]{6,40}\s`)

func gitLogHandler(_ string, output any) Result {
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

var (
	porcelainRe   = regexp.MustCompile(`^([MADRCU?!]{1,2})\s+(.+)$`)
	stagedLabelRe = regexp.MustCompile(`^(modified|new file|deleted|renamed):`)
	unstagLabelRe = regexp.MustCompile(`^(modified|deleted):`)
	stagedStripRe = regexp.MustCompile(`^(modified|new file|deleted|renamed):\s*`)
	unstagStripRe = regexp.MustCompile(`^(modified|deleted):\s*`)
)

func gitStatusHandler(toolName string, output any) Result {
	stdout := extractStdout(output)
	originalSize := byteLen(BashOutputText(output))

	if strings.TrimSpace(stdout) == "" {
		return Result{Summary: "[git status — clean working tree]", OriginalSize: originalSize}
	}

	var staged, unstaged, untracked, conflicts []string
	section := ""

	for _, line := range strings.Split(stdout, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "Changes to be committed"):
			section = "staged"
			continue
		case strings.HasPrefix(trimmed, "Changes not staged"):
			section = "unstaged"
			continue
		case strings.HasPrefix(trimmed, "Untracked files"):
			section = "untracked"
			continue
		case strings.HasPrefix(trimmed, "both modified") || strings.HasPrefix(trimmed, "both added"):
			conflicts = append(conflicts, trimmed)
			continue
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "(") || strings.HasPrefix(trimmed, "no changes") {
			continue
		}
		if strings.HasPrefix(trimmed, "On branch") || strings.HasPrefix(trimmed, "HEAD") ||
			strings.HasPrefix(trimmed, "Your branch") || strings.HasPrefix(trimmed, "nothing") {
			continue
		}

		if m := porcelainRe.FindStringSubmatch(line); m != nil {
			code, file := m[1], m[2]
			if strings.HasPrefix(code, "?") {
				untracked = append(untracked, file)
			} else if code[0] != ' ' && code[0] != '?' {
				staged = append(staged, string(code[0])+" "+file)
			}
			if len(code) >= 2 && code[1] != ' ' && code[1] != '?' {
				unstaged = append(unstaged, string(code[1])+" "+file)
			}
			continue
		}

		switch {
		case section == "staged" && stagedLabelRe.MatchString(trimmed):
			staged = append(staged, trimmed)
		case section == "unstaged" && unstagLabelRe.MatchString(trimmed):
			unstaged = append(unstaged, trimmed)
		case section == "untracked" && trimmed != "" && !strings.HasPrefix(trimmed, "("):
			untracked = append(untracked, trimmed)
		}
	}

	if len(staged) == 0 && len(unstaged) == 0 && len(untracked) == 0 && len(conflicts) == 0 {
		return shellHandler(toolName, output)
	}

	lines := []string{"git status"}
	if len(conflicts) > 0 {
		lines = append(lines, fmt.Sprintf("  conflicts (%d): %s", len(conflicts), strings.Join(firstN(conflicts, 5), ", ")))
	}
	if len(staged) > 0 {
		lines = append(lines, fmt.Sprintf("  staged (%d): %s%s", len(staged),
			strings.Join(stripEach(firstN(staged, 5), stagedStripRe), ", "), moreSuffix(len(staged), 5)))
	}
	if len(unstaged) > 0 {
		lines = append(lines, fmt.Sprintf("  unstaged (%d): %s%s", len(unstaged),
			strings.Join(stripEach(firstN(unstaged, 5), unstagStripRe), ", "), moreSuffix(len(unstaged), 5)))
	}
	if len(untracked) > 0 {
		lines = append(lines, fmt.Sprintf("  untracked (%d): %s%s", len(untracked),
			strings.Join(firstN(untracked, 5), ", "), moreSuffix(len(untracked), 5)))
	}
	return Result{Summary: strings.Join(lines, "\n"), OriginalSize: originalSize}
}

func firstN(xs []string, n int) []string {
	if len(xs) < n {
		return xs
	}
	return xs[:n]
}

func stripEach(xs []string, re *regexp.Regexp) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = re.ReplaceAllString(x, "")
	}
	return out
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
