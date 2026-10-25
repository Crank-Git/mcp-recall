// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/handlers/git_upstream_test.go
// Covers the git diff hunk budget (upstream #389), git log's fallback for a
// format it cannot parse (#350) and git status parsing (#387).

package handlers

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

const sampleDiff = "diff --git a/src/foo.ts b/src/foo.ts\nindex abc123..def456 100644\n--- a/src/foo.ts\n+++ b/src/foo.ts\n@@ -10,7 +10,7 @@ function doThing() {\n context line\n-old line\n+new line\n context line\n@@ -50,3 +50,5 @@\n more context\n+added line 1\n+added line 2\ndiff --git a/src/bar.ts b/src/bar.ts\nindex 111111..222222 100644\n--- a/src/bar.ts\n+++ b/src/bar.ts\n@@ -1,3 +1,2 @@\n keep\n-remove this\n"

func gitOut(t *testing.T, stdout string) any { return mustParseT(t, mkStdout(t, stdout)) }

func diffSummary(t *testing.T, stdout string) string {
	return gitDiffHandler("Bash", gitOut(t, stdout)).Summary
}

// bigFile is a file of hunks hunks, each width added lines naming the file and hunk.
func bigFile(path string, hunks, width int) string {
	name := regexp.MustCompile(`\W`).ReplaceAllString(path, "_")
	lines := []string{"diff --git a/" + path + " b/" + path, "index 1111111..2222222 100644", "--- a/" + path, "+++ b/" + path}
	for h := 0; h < hunks; h++ {
		lines = append(lines, fmt.Sprintf("@@ -%d,3 +%d,%d @@", h*100, h*100, width))
		for i := 0; i < width; i++ {
			lines = append(lines, fmt.Sprintf(`+const %s_h%d_l%d = "a line long enough to cost bytes";`, name, h, i))
		}
	}
	return strings.Join(lines, "\n")
}

func TestGitDiffListsHunkCountsOnlyWhenCut(t *testing.T) {
	hasNot(t, diffSummary(t, sampleDiff), "2 hunks")
	s := diffSummary(t, bigFile("src/a.ts", 40, 20)+"\n"+sampleDiff)
	has(t, s, "(40 hunks)")
	has(t, s, "(2 hunks)")
	has(t, s, "(1 hunk)")
}

func TestGitDiffKeepsHunkLines(t *testing.T) {
	s := diffSummary(t, sampleDiff)
	has(t, s, "@@ -10,7 +10,7 @@ function doThing() {\n context line\n-old line\n+new line")
	has(t, s, "-remove this")
	hasNot(t, s, "index abc123")
	hasNot(t, s, "+++ b/src/foo.ts")
}

func TestGitDiffStaysWithinBudget(t *testing.T) {
	s := diffSummary(t, bigFile("src/a.ts", 40, 20))
	if len(s) >= diffBodyBytes+1024 {
		t.Errorf("summary %d bytes", len(s))
	}
	// The share left after whole hunks goes to the next one's head, not to waste.
	if float64(len(s)) <= diffBodyBytes*0.95 {
		t.Errorf("summary only %d bytes", len(s))
	}
	has(t, s, "src_a_ts_h0_l0")
	if !regexp.MustCompile(`… \d+ more hunks? \(\+\d+ -0\) in src/a\.ts`).MatchString(s) {
		t.Errorf("no elision line:\n%s", s)
	}
}

func TestGitDiffSharesBudgetAcrossFiles(t *testing.T) {
	s := diffSummary(t, bigFile("src/a.ts", 40, 20)+"\n"+bigFile("src/b.ts", 1, 3))
	has(t, s, "src_b_ts_h0_l2")
	has(t, s, "src_a_ts_h0_l0")
}

func TestGitDiffManyFilesCostNoMoreThanOverview(t *testing.T) {
	var files []string
	for i := 0; i < 200; i++ {
		files = append(files, bigFile(fmt.Sprintf("src/f%d.ts", i), 2, 20))
	}
	s := diffSummary(t, strings.Join(files, "\n"))
	overviewRe := regexp.MustCompile(`\(\d+ hunks?\)$`)
	var overview []string
	for _, l := range strings.Split(s, "\n") {
		if overviewRe.MatchString(l) {
			overview = append(overview, l)
		}
	}
	if len(overview) != 200 {
		t.Fatalf("%d overview lines", len(overview))
	}
	if limit := len(strings.Join(overview, "\n")) + diffBodyBytes + 1024; len(s) >= limit {
		t.Errorf("summary %d bytes, limit %d", len(s), limit)
	}
}

func TestGitDiffBudgetsLinesWithoutHunkHeader(t *testing.T) {
	s := diffSummary(t, "diff --git a/f b/f\n"+strings.Repeat("+added line\n", 1000))
	if len(s) >= diffBodyBytes+1024 {
		t.Errorf("summary %d bytes", len(s))
	}
	has(t, s, "+1000 -0")
}

func TestGitDiffKeepsBinaryPatchDataOut(t *testing.T) {
	var data []string
	for i := 0; i < 300; i++ {
		data = append(data, fmt.Sprintf("zcmV+%d-Q8bXa+dk-P%s", i, strings.Repeat("x", 60)))
	}
	s := diffSummary(t, "diff --git a/logo.png b/logo.png\nindex 1111111..2222222 100644\nGIT binary patch\nliteral 9000\n"+strings.Join(data, "\n")+"\n\n"+sampleDiff)
	hasNot(t, s, "zcmV+")
	has(t, s, "GIT binary patch")
	has(t, s, "3 files changed, +3 -2")
}

func TestGitDiffCutsLongCommitHeader(t *testing.T) {
	var msg []string
	for i := 0; i < 30; i++ {
		msg = append(msg, fmt.Sprintf("    body line %d", i))
	}
	s := diffSummary(t, "commit 9540379abcdef\nAuthor: A <a@b.c>\n\n"+strings.Join(msg, "\n")+"\n\n"+sampleDiff)
	has(t, s, "body line 0")
	hasNot(t, s, "body line 29")
	if !regexp.MustCompile(`… \d+ more commit header lines`).MatchString(s) {
		t.Errorf("no header cut note:\n%s", s)
	}
}

func TestGitDiffKeepsShowCommitHeader(t *testing.T) {
	s := diffSummary(t, "commit 9540379abcdef\nAuthor: A <a@b.c>\nDate:   Thu Oct 23 2026\n\n    fix: store the saved result\n\n"+sampleDiff)
	has(t, s, "commit 9540379abcdef")
	has(t, s, "fix: store the saved result")
}

func TestFairShares(t *testing.T) {
	got := fairShares([]int{100, 10000, 50}, 4096)
	if got[0] != 100 || got[2] != 50 || got[1] != 4096-150 {
		t.Errorf("shares = %v", got)
	}
}

func TestGitLogFallsBackForUnparsedFormat(t *testing.T) {
	custom := strings.Join([]string{
		"3af300f0c1d2e3f4a5b6c7d8e9f0a1b2c3d4e5f6", "feat: record handler per output", "", "---",
		"ea341eb0c1d2e3f4a5b6c7d8e9f0a1b2c3d4e5f6", "chore: release v1.16.1", "", "---",
		"c0cce410c1d2e3f4a5b6c7d8e9f0a1b2c3d4e5f6", "fix(bash): recognise a sed -n range", "", "---",
	}, "\n")
	out := gitOut(t, custom)
	s := gitLogHandler("Bash", out).Summary
	hasNot(t, s, "0 commits")
	has(t, s, "chore: release v1.16.1")
	eq(t, s, shellHandler("Bash", out).Summary)
}

const gitStatusLong = "On branch feat/foo\nYour branch is up to date with 'origin/feat/foo'.\n\nChanges to be committed:\n  (use \"git restore --staged <file>...\" to unstage)\n\tmodified:   src/tools.ts\n\tnew file:   src/format.ts\n\nChanges not staged for commit:\n  (use \"git add <file>...\" to update an unstaged file)\n\tmodified:   README.md\n\nUntracked files:\n  (use \"git add <file>...\" to include in what will be committed)\n\tdocs/new-guide.md\n"

const gitStatusShort = "M  src/tools.ts\nA  src/format.ts\n M README.md\n?? docs/new-guide.md\n"

func status(t *testing.T, stdout string) string {
	return gitStatusHandler("Bash", gitOut(t, stdout)).Summary
}

func TestGitStatusUpstream(t *testing.T) {
	has(t, status(t, gitStatusShort), "unstaged (1): M README.md")

	s := status(t, " M apps/admin/src/api.ts\n M apps/api/src/db/schema.ts\n D apps/old.ts\n?? tasks/\n")
	has(t, s, "unstaged (3): M apps/admin/src/api.ts, M apps/api/src/db/schema.ts, D apps/old.ts")
	has(t, s, "untracked (1): tasks/")

	has(t, status(t, "## feat/x...origin/feat/x [ahead 2]\n M a.ts\n"), "## feat/x...origin/feat/x [ahead 2]")

	long := status(t, gitStatusLong)
	has(t, long, "On branch feat/foo")
	has(t, long, "Your branch is up to date with 'origin/feat/foo'.")
	has(t, long, "staged (2): src/tools.ts, src/format.ts")
	has(t, long, "unstaged (1): README.md")
	has(t, long, "untracked (1): docs/new-guide.md")

	has(t, status(t, "HEAD detached at 1a2b3c4\nChanges not staged for commit:\n\tmodified:   a.ts\n"), "HEAD detached at 1a2b3c4")

	var files []string
	for i := 0; i < 60; i++ {
		files = append(files, fmt.Sprintf("?? src/file-%d.ts", i))
	}
	many := status(t, strings.Join(files, "\n"))
	has(t, many, "src/file-49.ts")
	hasNot(t, many, "src/file-50.ts")
	has(t, many, "+10 more")

	has(t, status(t, "UU src/a.ts\nAA src/b.ts\nDU src/c.ts\n M src/d.ts\n"), "conflicts (3): UU src/a.ts, AA src/b.ts, DU src/c.ts")
	conflicts := status(t, "Unmerged paths:\n\tboth modified:   src/a.ts\n\tdeleted by them: src/b.ts\n\tadded by us:     src/c.ts\n")
	has(t, conflicts, "conflicts (3)")
	has(t, conflicts, "src/b.ts")
	has(t, conflicts, "src/c.ts")

	renamed := status(t, "Changes not staged for commit:\n\trenamed:    old.ts -> new.ts\n\ttypechange: link.ts\n")
	has(t, renamed, "old.ts -> new.ts")
	has(t, renamed, "link.ts")

	has(t, status(t, "R  src/old.ts -> src/new.ts\n"), "src/old.ts -> src/new.ts")
}
