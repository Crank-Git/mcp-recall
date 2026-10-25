// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/handlers/parity_test.go

package handlers

// Parity tests: these mirror the original TypeScript suite's assertions
// (tests/handlers.test.ts) one-for-one, verifying the Go port produces the
// same summaries, byte sizes, and dispatch routing.

import (
	"reflect"
	"strings"
	"testing"

	"mcprecall/internal/jsonx"
)

func sumS(fn Handler, tool, input string) string { return fn(tool, input).Summary }
func sizeS(fn Handler, tool, input string) int   { return fn(tool, input).OriginalSize }

func sumO(t *testing.T, fn Handler, tool, jsonInput string) string {
	return fn(tool, mustParseT(t, jsonInput)).Summary
}

func has(t *testing.T, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Errorf("expected to contain %q\n--- got ---\n%s", want, got)
	}
}
func hasNot(t *testing.T, got, want string) {
	t.Helper()
	if strings.Contains(got, want) {
		t.Errorf("expected NOT to contain %q\n--- got ---\n%s", want, got)
	}
}
func eq(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}
func samePtr(a, b Handler) bool { return reflect.ValueOf(a).Pointer() == reflect.ValueOf(b).Pointer() }

func repeatS(s string, n int) string { return strings.Repeat(s, n) }

// ── extractText ───────────────────────────────────────────────────────────────

func TestParityExtractText(t *testing.T) {
	eq(t, ExtractText("hello"), "hello")
	eq(t, ExtractText(mustParseT(t, `{"content":[{"type":"text","text":"foo"},{"type":"text","text":"bar"}]}`)), "foo\nbar")
	eq(t, ExtractText(mustParseT(t, `{"content":[{"type":"image","data":"abc"},{"type":"text","text":"hi"}]}`)), "hi")
	// JSON.stringify fallback — order preserved, no HTML escaping
	eq(t, ExtractText(mustParseT(t, `{"foo":42}`)), `{"foo":42}`)
}

// ── playwright ────────────────────────────────────────────────────────────────

func TestParityPlaywright(t *testing.T) {
	snapshot := strings.Join([]string{
		`- document "Page"`, `  - heading "Welcome"`, `  - button "Submit"`,
		`  - textbox "Email"`, `  - link "Home"`, `  - statictext "Some visible text here"`,
	}, "\n")
	s := sumS(playwrightHandler, "mcp__playwright__browser_snapshot", snapshot)
	has(t, s, `[button "Submit"]`)
	has(t, s, `[textbox "Email"]`)
	has(t, s, `[link "Home"]`)
	has(t, s, "Welcome")
	has(t, s, "Some visible text here")
	if got := sizeS(playwrightHandler, "x", snapshot); got != len(snapshot) {
		t.Errorf("originalSize=%d", got)
	}
	has(t, sumS(playwrightHandler, "x", ""), "no interactive elements")
	// cap at 20
	var lines []string
	for i := 0; i < 25; i++ {
		lines = append(lines, `- button "Btn`+string(rune('0'+i%10))+`"`)
	}
	capped := sumS(playwrightHandler, "x", strings.Join(lines, "\n"))
	if n := strings.Count(capped, "[button"); n > 20 {
		t.Errorf("interactive count %d > 20", n)
	}
}

// ── github ────────────────────────────────────────────────────────────────────

func TestParityGithub(t *testing.T) {
	issue := `{"number":42,"title":"Fix the thing","state":"open","html_url":"https://github.com/org/repo/issues/42","labels":[{"name":"bug"},{"name":"P1: High"}],"body":"This is broken."}`
	s := sumS(githubHandler, "mcp__github__issue_read", issue)
	has(t, s, "#42")
	has(t, s, `"Fix the thing"`)
	has(t, s, "[open]")
	has(t, s, "bug")
	has(t, s, "This is broken.")

	arr := sumS(githubHandler, "mcp__github__list_issues", `[{"number":1,"title":"First","state":"open"},{"number":2,"title":"Second","state":"closed"}]`)
	has(t, arr, "#1")
	has(t, arr, "#2")

	long := sumS(githubHandler, "x", `[`+strings.TrimSuffix(strings.Repeat(`{"number":1,"title":"Item","state":"open"},`, 15), ",")+`]`)
	has(t, long, "5 more")

	has(t, sumS(githubHandler, "x", `{"number":1,"title":"T","state":"open","body":"`+repeatS("x", 300)+`"}`), "…")
	has(t, sumS(githubHandler, "mcp__github__get_file_contents", "plain text response"), "plain text response")
}

// ── filesystem ────────────────────────────────────────────────────────────────

func TestParityFilesystem(t *testing.T) {
	has(t, sumS(filesystemHandler, "x", "line1\nline2\nline3"), "3 lines")
	ten := ""
	for i := 1; i <= 10; i++ {
		if i > 1 {
			ten += "\n"
		}
		ten += "line " + string(rune('0'+i%10))
	}
	s := sumS(filesystemHandler, "x", ten)
	hasNot(t, s, "…")
	big := make([]string, 80)
	for i := range big {
		big[i] = "line" + string(rune('0'+i%10))
	}
	s2 := sumS(filesystemHandler, "x", strings.Join(big, "\n"))
	has(t, s2, "showing first 50")
	has(t, s2, "…")
	has(t, sumS(filesystemHandler, "x", "one line"), "1 line")
}

// ── json + generic ────────────────────────────────────────────────────────────

func TestParityJSON(t *testing.T) {
	// depth truncation: a.b.c.d == "…"
	depth := sumS(jsonHandler, "x", `{"a":{"b":{"c":{"d":"deep"}}}}`)
	pv, _ := jsonx.ParseString(depth)
	a, _ := pv.(*jsonx.Obj).Get("a")
	b, _ := a.(*jsonx.Obj).Get("b")
	c, _ := b.(*jsonx.Obj).Get("c")
	d, _ := c.(*jsonx.Obj).Get("d")
	if d != "…" {
		t.Errorf("depth: a.b.c.d = %v", d)
	}
	// v1.9.0: compact (no pretty-print indentation), keys verbatim.
	compact := sumS(jsonHandler, "x", `{"a":{"b":{"c":1}},"items":[1,2,3,4,5]}`)
	hasNot(t, compact, "\n  ")
	has(t, compact, `"items"`)
	if _, err := jsonx.ParseString(compact); err != nil {
		t.Errorf("compact summary not valid JSON: %q", compact)
	}
	// array cap: 3 items + "…2 more"
	arrOut := sumS(jsonHandler, "x", `{"items":[1,2,3,4,5]}`)
	has(t, arrOut, "2 more")
	// short array unchanged
	short := sumS(jsonHandler, "x", `{"items":[1,2]}`)
	has(t, short, "[1,2]")
	has(t, sumS(jsonHandler, "x", "not json at all"), "not json at all")
}

func TestParityGeneric(t *testing.T) {
	// small unchanged
	eq(t, sumS(genericHandler, "x", "short content"), "short content")
	// long single-block → head+tail window ≤ 502, contains "…"
	trunc := sumS(genericHandler, "x", repeatS("x", 600))
	if runeLen(trunc) > 502 {
		t.Errorf("generic length %d", runeLen(trunc))
	}
	has(t, trunc, "…")

	// line-mode: keeps head+tail lines, elides middle, actually compresses
	var lines []string
	for i := 0; i < 30; i++ {
		lines = append(lines, "line number "+itoaP(i)+" with some filler text here")
	}
	lm := sumS(genericHandler, "mcp__x__logs", strings.Join(lines, "\n"))
	has(t, lm, "line number 0 ")
	has(t, lm, "line number 29")
	has(t, lm, "elided")
	hasNot(t, lm, "line number 15")
	if len(lm) >= len(strings.Join(lines, "\n")) {
		t.Error("line-mode did not compress")
	}

	// surfaces error/warn from the middle
	lines2 := append([]string{}, lines...)
	lines2[15] = "ERROR: database connection refused"
	has(t, sumS(genericHandler, "mcp__x__logs", strings.Join(lines2, "\n")), "ERROR: database connection refused")

	// block-mode markers survive at both ends
	blk := sumS(genericHandler, "mcp__x__blob", "START-MARKER "+repeatS("a", 600)+" END-MARKER")
	if !strings.HasPrefix(blk, "START-MARKER") {
		t.Errorf("block head lost: %q", firstChars(blk, 30))
	}
	if !strings.HasSuffix(trimEnd(blk), "END-MARKER") {
		t.Errorf("block tail lost")
	}

	// mode boundary: 10 lines = block (no "elided"), 11 lines = line-mode
	mk := func(n int) string {
		var xs []string
		for i := 0; i < n; i++ {
			xs = append(xs, "line "+itoaP(i)+" "+repeatS("z", 55))
		}
		return strings.Join(xs, "\n")
	}
	ten := sumS(genericHandler, "x", mk(10))
	hasNot(t, ten, "elided")
	has(t, ten, "…")
	has(t, sumS(genericHandler, "x", mk(11)), "elided")

	// caps surfaced error/warn lines at 8
	var lines3 []string
	for i := 0; i < 30; i++ {
		if i >= 10 && i <= 25 {
			lines3 = append(lines3, "error at step "+itoaP(i)+" padded padded padded padded")
		} else {
			lines3 = append(lines3, "plain log line "+itoaP(i)+" padded padded padded")
		}
	}
	has(t, sumS(genericHandler, "mcp__x__logs", strings.Join(lines3, "\n")), "8 error/warn shown")
}

// ── dispatch routing ──────────────────────────────────────────────────────────

func TestParityDispatch(t *testing.T) {
	cases := []struct {
		tool, out string
		want      Handler
	}{
		{"mcp__plugin_playwright_playwright__browser_snapshot", "", playwrightHandler},
		{"mcp__github__list_issues", "", githubHandler},
		{"mcp__gitlab__list_issues", "{}", gitlabHandler},
		{"mcp__filesystem__read_file", "", filesystemHandler},
		{"mcp__unknown__tool", `{"key":"value"}`, jsonHandler},
		{"mcp__unknown__tool", "plain text output", genericHandler},
		{"mcp__linear__get_issue", "", linearHandler},
		{"mcp__slack__get_messages", "", slackHandler},
		{"mcp__export__get_csv", "", csvHandler},
		{"mcp__unknown__tool", "col1,col2,col3\nv1,v2,v3\nv4,v5,v6\nv7,v8,v9", csvHandler},
		{"mcp__bash__execute", "output", shellHandler},
		{"mcp__shell__run", "output", shellHandler},
		{"mcp__terminal__execute", "output", shellHandler},
		{"mcp__mcp-remote-exec__ssh_exec_command", "output", shellHandler},
		{"mcp__docker__container_exec", "output", shellHandler},
		{"mcp__tavily__tavily_search", "{}", tavilyHandler},
		{"mcp__postgres__query", "{}", databaseHandler},
		{"mcp__mysql__execute", "{}", databaseHandler},
		{"mcp__sqlite__run_query", "{}", databaseHandler},
		{"mcp__sentry__get_issue", "{}", sentryHandler},
		{"mcp__stripe__list_customers", "{}", stripeHandler},
	}
	for _, c := range cases {
		if got := GetHandler(c.tool, c.out, nil); !samePtr(got, c.want) {
			t.Errorf("GetHandler(%q) routed wrong", c.tool)
		}
	}
}

func TestParityBashDispatch(t *testing.T) {
	cmd := func(c string) any { return mustParseT(t, `{"command":"`+c+`"}`) }
	cases := []struct {
		command string
		want    Handler
	}{
		{"git diff HEAD", gitDiffHandler},
		{"git show abc123", gitDiffHandler},
		{"git log --oneline -10", gitLogHandler},
		{"terraform plan -out=tfplan", terraformPlanHandler},
		{"git status", gitStatusHandler},
		{"git status --short", gitStatusHandler},
		{"npm install", packageInstallHandler},
		{"bun install", packageInstallHandler},
		{"pip install -r requirements.txt", packageInstallHandler},
		{"pip3 install requests", packageInstallHandler},
		{"pytest tests/", testRunnerHandler},
		{"jest --coverage", testRunnerHandler},
		{"bun test", testRunnerHandler},
		{"go test ./...", testRunnerHandler},
		{"npx jest", testRunnerHandler},
		{"docker ps", dockerPsHandler},
		{"docker compose ps", dockerPsHandler},
		{"make build", buildToolHandler},
		{"just test", buildToolHandler},
		{"npm test", shellHandler},
		{"gh pr list", ghHandler},
	}
	for _, c := range cases {
		if got := GetBashHandler(cmd(c.command)); !samePtr(got, c.want) {
			t.Errorf("GetBashHandler(%q) routed wrong", c.command)
		}
	}
	// getHandler("Bash", ..., {command}) routes through bash dispatcher
	if got := GetHandler("Bash", "output", cmd("git diff HEAD")); !samePtr(got, gitDiffHandler) {
		t.Error("Bash tool did not route to git diff")
	}
}

// ── csv + looksLikeCsv ────────────────────────────────────────────────────────

func TestParityCSV(t *testing.T) {
	csv := "name,age,city,country\nAlice,30,London,UK\nBob,25,Paris,France\nCarol,35,Berlin,Germany"
	s := sumS(csvHandler, "x", csv)
	has(t, s, "3 rows")
	has(t, s, "4 cols")
	has(t, s, "name")
	has(t, s, "Alice")
	has(t, s, "row 1")
	quoted := sumS(csvHandler, "x", "name,address\nAlice,\"123 Main St, Apt 4\"\nBob,456 Oak Ave")
	has(t, quoted, "Alice")
	has(t, quoted, "2 rows")
	has(t, sumS(csvHandler, "x", ""), "empty")

	if !looksLikeCsv("a,b,c\n1,2,3\n4,5,6") {
		t.Error("looksLikeCsv true case")
	}
	if looksLikeCsv("hello world\nno commas here\nstill no commas") {
		t.Error("looksLikeCsv false (no commas)")
	}
	if looksLikeCsv("a,b,c\n1,2,3") {
		t.Error("looksLikeCsv false (<3 lines)")
	}
}

// ── linear ────────────────────────────────────────────────────────────────────

func TestParityLinear(t *testing.T) {
	single := `{"identifier":"ENG-123","title":"Fix authentication bug","state":{"name":"In Progress"},"priority":2,"description":"The login form throws a 500 on invalid credentials.","url":"https://linear.app/org/issue/ENG-123"}`
	s := sumS(linearHandler, "x", single)
	has(t, s, "ENG-123")
	has(t, s, "Fix authentication bug")
	has(t, s, "[In Progress]")
	has(t, s, "Priority: High")
	has(t, s, "500 on invalid credentials")
	has(t, s, "https://linear.app")

	arr := sumS(linearHandler, "x", `[{"identifier":"ENG-1","title":"Issue one","state":{"name":"Todo"},"priority":3},{"identifier":"ENG-2","title":"Issue two","state":{"name":"Done"},"priority":4}]`)
	has(t, arr, "2 Linear issues")
	has(t, arr, "ENG-1")
	has(t, arr, "ENG-2")

	gql := sumS(linearHandler, "x", `{"data":{"issue":{"identifier":"ENG-99","title":"GraphQL issue","state":{"name":"Backlog"},"priority":4}}}`)
	has(t, gql, "ENG-99")
	relay := sumS(linearHandler, "x", `{"nodes":[{"identifier":"ENG-10","title":"Node issue","state":{"name":"Todo"},"priority":3}]}`)
	has(t, relay, "ENG-10")
	has(t, sumS(linearHandler, "x", "not json"), "not json")
}

// ── slack ─────────────────────────────────────────────────────────────────────

func TestParitySlack(t *testing.T) {
	wrapper := `{"ok":true,"messages":[{"ts":"1740825600.000000","user":"U12345","text":"Hello team, standup in 5 minutes"},{"ts":"1740825660.000000","user":"U67890","text":"On my way, be there shortly"}],"channel":"C_GENERAL"}`
	s := sumS(slackHandler, "x", wrapper)
	has(t, s, "2 messages")
	has(t, s, "U12345")
	has(t, s, "standup in 5 minutes")
	has(t, s, "C_GENERAL")
	// timestamp format [YYYY-MM-DD HH:MM]
	if !strings.Contains(s, "[2025-03-01 ") {
		t.Errorf("slack timestamp format missing: %s", s)
	}
	bare := sumS(slackHandler, "x", `[{"ts":"1740825600.000000","user":"alice","text":"bare array message"},{"ts":"1740825660.000000","user":"bob","text":"another"},{"ts":"1740825720.000000","user":"carol","text":"third"}]`)
	has(t, bare, "3 messages")
	has(t, bare, "bare array message")
	dn := sumS(slackHandler, "x", `{"messages":[{"ts":"1740825600.000000","user":"U12345","user_profile":{"display_name":"alice_display"},"text":"hello"}]}`)
	has(t, dn, "alice_display")
}

// ── shell / stripAnsi / stripSshNoise ─────────────────────────────────────────

func TestParityShell(t *testing.T) {
	eq(t, stripAnsi("\x1b[32mhello\x1b[0m"), "hello")
	eq(t, stripAnsi("\x1b[1mbold\x1b[0m text"), "bold text")
	eq(t, stripAnsi("plain output"), "plain output")

	has(t, sumS(shellHandler, "x", "line1\nline2\nline3"), "3 lines")
	ansi := sumS(shellHandler, "x", "\x1b[32mgreen text\x1b[0m\nnormal text")
	has(t, ansi, "green text")
	hasNot(t, ansi, "\x1b[")
	structured := sumO(t, shellHandler, "x", `{"stdout":"hello world","stderr":"","returncode":0}`)
	has(t, structured, "hello world")
	has(t, structured, "exit:0")
	has(t, sumO(t, shellHandler, "x", `{"stdout":"output","returncode":1}`), "exit:1")
	stderr := sumO(t, shellHandler, "x", `{"stdout":"ok","stderr":"warning: something","returncode":0}`)
	has(t, stderr, "stderr:")
	has(t, stderr, "warning: something")
	alt := sumO(t, shellHandler, "x", `{"output":"result text","exit_code":0}`)
	has(t, alt, "result text")
	has(t, alt, "exit:0")
	has(t, sumS(shellHandler, "x", ""), "0 lines")
	// JSON stdout routed to json handler
	jstruct := sumO(t, shellHandler, "x", `{"stdout":"{\"id\":1,\"name\":\"foo\",\"tags\":[\"a\",\"b\"]}","stderr":"","returncode":0}`)
	hasNot(t, jstruct, "[bash ·")
	has(t, jstruct, `"foo"`)
	// invalid JSON-looking output stays shell
	has(t, sumS(shellHandler, "x", "{ this is not json }"), "[bash ·")

	pq := strings.Join([]string{
		"** WARNING: connection is not using a post-quantum key exchange algorithm.",
		"** This session may be vulnerable to \"store now, decrypt later\" attacks.",
		"** The server may need to be upgraded. See https://openssh.com/pq.html for details.",
	}, "\n")
	eq(t, stripSshNoise(pq), "")
	eq(t, stripSshNoise(pq+"\nuid=0(root) gid=0(root) groups=0(root)"), "uid=0(root) gid=0(root) groups=0(root)")
	eq(t, stripSshNoise(pq+"\n\nreal output"), "real output")
	eq(t, stripSshNoise("**bold text** still present"), "**bold text** still present")
}

// ── git diff / log / status ───────────────────────────────────────────────────

func TestParityGitDiff(t *testing.T) {
	diff := "diff --git a/src/foo.ts b/src/foo.ts\nindex abc123..def456 100644\n--- a/src/foo.ts\n+++ b/src/foo.ts\n@@ -10,7 +10,7 @@ function doThing() {\n context line\n-old line\n+new line\n context line\n@@ -50,3 +50,5 @@\n more context\n+added line 1\n+added line 2\ndiff --git a/src/bar.ts b/src/bar.ts\nindex 111111..222222 100644\n--- a/src/bar.ts\n+++ b/src/bar.ts\n@@ -1,3 +1,2 @@\n keep\n-remove this\n"
	s := gitDiffHandler("Bash", mustParseT(t, mkStdout(t, diff))).Summary
	has(t, s, "2 files changed")
	has(t, s, "+3")
	has(t, s, "-2")
	has(t, s, "src/foo.ts")
	has(t, s, "src/bar.ts")
	hasNot(t, s, "2 hunks") // the whole body fits, so no per-file overview
	has(t, gitDiffHandler("Bash", mustParseT(t, `{"stdout":"","stderr":"","exit_code":0}`)).Summary, "no changes")
}

func TestParityGitLog(t *testing.T) {
	oneline := "abc1234 Fix authentication bug in login flow\ndef5678 Add new user profile feature\n7890abc Update dependencies to latest versions\n"
	s := gitLogHandler("Bash", mustParseT(t, mkStdout(t, oneline))).Summary
	has(t, s, "3 commits")
	has(t, s, "Fix authentication bug")
	full := "commit abc1234567890abcdef\nAuthor: Jane Dev <jane@example.com>\nDate:   Sun Mar 2 10:00:00 2026 -0800\n\n    Fix authentication bug in login flow\n\n    More details about the fix.\n\ncommit def5678901234abcde\nAuthor: John Dev <john@example.com>\nDate:   Sat Mar 1 09:00:00 2026 -0800\n\n    Add new user profile feature\n"
	s2 := gitLogHandler("Bash", mustParseT(t, mkStdout(t, full))).Summary
	has(t, s2, "2 commits")
	has(t, s2, "Fix authentication bug in login flow")
	has(t, s2, "Add new user profile feature")
	has(t, gitLogHandler("Bash", mustParseT(t, `{"stdout":"","stderr":"","exit_code":0}`)).Summary, "no commits")
}

func TestParityGitStatus(t *testing.T) {
	long := "On branch feat/foo\nYour branch is up to date with 'origin/feat/foo'.\n\nChanges to be committed:\n  (use \"git restore --staged <file>...\" to unstage)\n\tmodified:   src/tools.ts\n\tnew file:   src/format.ts\n\nChanges not staged for commit:\n  (use \"git add <file>...\" to update an unstaged file)\n\tmodified:   README.md\n\nUntracked files:\n  (use \"git add <file>...\" to include in what will be committed)\n\tdocs/new-guide.md\n\ttmp/scratch.ts\n"
	s := gitStatusHandler("Bash", mustParseT(t, mkStdout(t, long))).Summary
	has(t, s, "staged")
	has(t, s, "src/tools.ts")
	has(t, s, "unstaged")
	has(t, s, "README.md")
	has(t, s, "untracked")
	has(t, s, "docs/new-guide.md")
	short := "M  src/tools.ts\nA  src/format.ts\n M README.md\n?? docs/new-guide.md\n"
	s2 := gitStatusHandler("Bash", mustParseT(t, mkStdout(t, short))).Summary
	has(t, s2, "staged")
	has(t, s2, "untracked")
	has(t, gitStatusHandler("Bash", mustParseT(t, `{"stdout":"","stderr":"","exit_code":0}`)).Summary, "clean working tree")
}

// ── terraform / package / test / docker / build / gh ──────────────────────────

func TestParityTerraform(t *testing.T) {
	plan := "\nTerraform will perform the following actions:\n\n  # aws_instance.web will be created\n  + resource \"aws_instance\" \"web\" {\n    }\n\n  # aws_security_group.main will be updated in-place\n  ~ resource \"aws_security_group\" \"main\" {\n    }\n\n  # aws_s3_bucket.old will be destroyed\n  - resource \"aws_s3_bucket\" \"old\" {}\n\nPlan: 2 to add, 1 to change, 1 to destroy.\n"
	s := terraformPlanHandler("Bash", mustParseT(t, mkStdout(t, plan))).Summary
	has(t, s, "Plan: 2 to add, 1 to change, 1 to destroy")
	has(t, s, "+ aws_instance.web")
	has(t, s, "~ aws_security_group.main")
	has(t, s, "- aws_s3_bucket.old")
	has(t, terraformPlanHandler("Bash", mustParseT(t, mkStdout(t, "Initializing the backend...\nSuccess!"))).Summary, "lines")
}

func TestParityPackageInstall(t *testing.T) {
	bun := "bun install v1.1.0\n  + express@4.19.2\n  120 packages installed [1.23s]\n"
	has(t, packageInstallHandler("Bash", mustParseT(t, mkStdout(t, bun))).Summary, "120 packages installed")
	npm := "\nadded 42 packages, and audited 43 packages in 3s\n\nfound 0 vulnerabilities\n"
	has(t, packageInstallHandler("Bash", mustParseT(t, mkStdout(t, npm))).Summary, "added 42 packages")
	pip := "\nCollecting requests>=2.28\nSuccessfully installed certifi-2024.2.2 charset-normalizer-3.3.2 requests-2.31.0\n"
	has(t, packageInstallHandler("Bash", mustParseT(t, mkStdout(t, pip))).Summary, "pip: 3 packages installed")
}

func TestParityTestRunner(t *testing.T) {
	bun := "\n tests/tools.test.ts:\n   ✓ forget guard rejects\n\n 543 pass\n 0 fail\n Ran 543 tests across 15 files.\n"
	s := testRunnerHandler("Bash", mustParseT(t, mkStdout(t, bun))).Summary
	has(t, s, "pass")
	has(t, s, "543 passed")
	pyt := "\ncollected 47 items\n\n=============================== 3 passed, 1 warning in 0.45s ===============\n"
	has(t, testRunnerHandler("Bash", mustParseT(t, mkStdout(t, pyt))).Summary, "3 passed")
	jest := "\n  ● AuthService › login fails\n\nTests: 1 failed, 4 passed, 5 total\n"
	sj := testRunnerHandler("Bash", mustParseT(t, mkStdout(t, jest))).Summary
	has(t, sj, "FAIL")
	has(t, sj, "1 failed")
	has(t, sj, "4 passed")
	gotest := "\nok  \tgithub.com/user/repo/pkg/auth\t0.043s\nFAIL\tgithub.com/user/repo/pkg/api\t0.034s\n--- FAIL: TestHandleRequest (0.00s)\n"
	sg := testRunnerHandler("Bash", mustParseT(t, mkStdout(t, gotest))).Summary
	has(t, sg, "FAIL")
	has(t, sg, "--- FAIL: TestHandleRequest")
}

func TestParityDockerBuild(t *testing.T) {
	ps := "CONTAINER ID   IMAGE              COMMAND                  CREATED        STATUS          PORTS                    NAMES\na1b2c3d4e5f6   nginx:latest       \"/docker-entrypoint.…\"   2 hours ago    Up 2 hours      0.0.0.0:80->80/tcp       web\nb2c3d4e5f6a1   postgres:16        \"docker-entrypoint.s…\"   2 hours ago    Up 2 hours      0.0.0.0:5432->5432/tcp   db\nc3d4e5f6a1b2   redis:7-alpine     \"docker-entrypoint.s…\"   2 hours ago    Up 2 hours      6379/tcp                 cache\n"
	s := dockerPsHandler("Bash", mustParseT(t, mkStdout(t, ps))).Summary
	has(t, s, "3 containers")
	has(t, s, "web")
	has(t, s, "db")
	has(t, s, "cache")
	has(t, s, "Up")
	has(t, dockerPsHandler("Bash", mustParseT(t, `{"stdout":"","stderr":"","exit_code":0}`)).Summary, "no containers")

	mkErr := "make[1]: Entering directory '/home/user/project'\ncc -Wall -o main main.c\nmain.c:42:10: error: 'undefined_var' undeclared (first use in this function)\nmake[1]: *** [Makefile:15: main] Error 1\nmake[1]: Leaving directory '/home/user/project'\n"
	se := buildToolHandler("Bash", mustParseT(t, `{"stdout":`+jsonx.Compact(mkErr)+`,"stderr":"","exit_code":2}`)).Summary
	has(t, se, "error")
	has(t, se, "undefined_var")
	has(t, se, "Error 1")
	ok := buildToolHandler("Bash", mustParseT(t, `{"stdout":"make[1]: Nothing to be done for 'all'.\n","stderr":"","exit_code":0}`)).Summary
	has(t, ok, "completed successfully")
}

func TestParityGh(t *testing.T) {
	rows := ""
	for i := 1; i <= 12; i++ {
		rows += "#" + itoaP(i) + "\tIssue title " + itoaP(i) + "\tbug\t2026-03-01\n"
	}
	s := ghHandler("Bash", mustParseT(t, `{"stdout":`+jsonx.Compact(rows)+`,"stderr":"","exit_code":0}`)).Summary
	has(t, s, "12 items")
	has(t, s, "#1")
	has(t, s, "… (+2 more)")
	checks := "Test\tpass\t14s\nBundle\tpass\t6s\nLint\tfail\t2s\nSecurity\tpass\t30s\nDeploy\tpass\t45s\nE2E\tpass\t90s"
	sc := ghHandler("Bash", mustParseT(t, `{"stdout":`+jsonx.Compact(checks)+`,"stderr":"","exit_code":0}`)).Summary
	has(t, sc, "5 pass")
	has(t, sc, "1 fail")
	has(t, sc, "fail: Lint")
}

// ── tavily / gitlab / database / sentry / stripe ──────────────────────────────

func TestParityTavily(t *testing.T) {
	search := `{"query":"bun runtime benchmarks","answer":"Bun is a fast all-in-one JavaScript runtime. Benchmarks show it is 3x faster than Node.js for many workloads.","results":[{"title":"Bun vs Node.js Performance","url":"https://example.com/bun-benchmarks","content":"Bun achieves significant speed improvements over Node.js in HTTP throughput and startup time.","raw_content":"` + repeatS("FULL PAGE CONTENT ", 500) + `","score":0.98}],"response_time":1.23}`
	r := tavilyHandler("mcp__tavily__tavily_search", search)
	has(t, r.Summary, "bun runtime benchmarks")
	has(t, r.Summary, "3x faster than Node.js")
	has(t, r.Summary, "Bun vs Node.js Performance")
	has(t, r.Summary, "https://example.com/bun-benchmarks")
	has(t, r.Summary, "significant speed improvements")
	if len(r.Summary) >= r.OriginalSize/10 {
		t.Errorf("tavily not compressed enough: %d vs %d", len(r.Summary), r.OriginalSize)
	}
}

func TestParityGitlab(t *testing.T) {
	single := `{"iid":12,"title":"Fix the pipeline","state":"opened","web_url":"https://gitlab.com/org/repo/-/issues/12","labels":["bug","P1"],"description":"The pipeline fails on merge requests targeting the main branch."}`
	s := sumS(gitlabHandler, "x", single)
	has(t, s, "!12")
	has(t, s, `"Fix the pipeline"`)
	has(t, s, "[opened]")
	has(t, s, "https://gitlab.com/org/repo/-/issues/12")
	has(t, s, "bug")
	has(t, s, "P1")
	has(t, s, "pipeline fails on merge requests")
	arr := sumS(gitlabHandler, "x", `[{"iid":1,"title":"First MR","state":"merged"},{"iid":2,"title":"Second MR","state":"opened"}]`)
	has(t, arr, "!1")
	has(t, arr, "!2")
}

func TestParityDatabase(t *testing.T) {
	nodepg := `{"rows":[{"id":1,"name":"Alice","email":"alice@example.com"},{"id":2,"name":"Bob","email":"bob@example.com"}],"fields":[{"name":"id"},{"name":"name"},{"name":"email"}],"rowCount":2}`
	s := sumS(databaseHandler, "x", nodepg)
	has(t, s, "2 rows")
	has(t, s, "3 cols")
	has(t, s, "Alice")
	has(t, s, "headers:")
	bare := sumS(databaseHandler, "x", `[{"id":1,"status":"active","amount":100},{"id":2,"status":"closed","amount":200},{"id":3,"status":"active","amount":300}]`)
	has(t, bare, "3 rows")
	has(t, bare, "active")
	wrap := sumS(databaseHandler, "x", `{"results":[{"user_id":10,"score":99},{"user_id":11,"score":88}]}`)
	has(t, wrap, "2 rows")
	has(t, wrap, "user_id")
	has(t, wrap, "99")
	empty := sumS(databaseHandler, "x", `{"rows":[],"fields":[{"name":"id"}]}`)
	has(t, empty, "0 rows")
	has(t, empty, "empty result set")
	has(t, sumS(databaseHandler, "x", "ERROR: relation not found"), "ERROR: relation not found")
}

func TestParitySentry(t *testing.T) {
	event := `{"event_id":"abc123def456","level":"error","environment":"production","release":"app@2.1.0","exception":{"values":[{"type":"TypeError","value":"Cannot read properties of undefined (reading 'id')","stacktrace":{"frames":[{"filename":"node_modules/express/lib/router.js","function":"next","lineno":136},{"filename":"src/middleware/auth.ts","function":"verifyToken","lineno":42},{"filename":"src/routes/users.ts","function":"getUser","lineno":88},{"filename":"src/controllers/user.ts","function":"fetchById","lineno":23}]}}]}}`
	r := sentryHandler("x", event)
	has(t, r.Summary, "TypeError")
	has(t, r.Summary, "Cannot read properties of undefined")
	has(t, r.Summary, "[error]")
	has(t, r.Summary, "env:production")
	has(t, r.Summary, "release:app@2.1.0")
	has(t, r.Summary, "id:abc123de")
	has(t, r.Summary, "src/routes/users.ts")
	has(t, r.Summary, ":88")
	has(t, r.Summary, "getUser")
	// frame cap: build 20 frames, expect last 8 (12..19)
	var frames []string
	for i := 0; i < 20; i++ {
		frames = append(frames, `{"filename":"src/file`+itoaP(i)+`.ts","function":"fn`+itoaP(i)+`","lineno":`+itoaP(i*10+1)+`}`)
	}
	capped := sentryHandler("x", `{"level":"error","exception":{"values":[{"type":"Error","value":"boom","stacktrace":{"frames":[`+strings.Join(frames, ",")+`]}}]}}`).Summary
	has(t, capped, "src/file19.ts")
	has(t, capped, "src/file12.ts")
	hasNot(t, capped, "src/file11.ts")
}

func TestParityStripe(t *testing.T) {
	customers := `{"object":"list","data":[{"id":"cus_A1B2C3D4E5F6G7","name":"Ada Example","email":"ada@example.com","phone":"+15550001111"},{"id":"cus_H8I9J0K1L2M3N4","name":"Acme Widgets","email":"billing@example.org","phone":null}]}`
	s := sumS(stripeHandler, "mcp__stripe__list_customers", customers)
	has(t, s, "cus_A1B2C3D4E5F6G7")
	has(t, s, "Ada Example")
	has(t, s, "ada@example.com")

	invoices := `{"object":"list","data":[{"id":"in_1","status":"open","amount_due":250000,"amount_paid":0,"currency":"usd","customer_name":"Ada Example","billing_reason":"manual"}]}`
	si := sumS(stripeHandler, "mcp__stripe__list_invoices", invoices)
	has(t, si, "$2,500.00")
	hasNot(t, si, "250000")
	has(t, si, "[open]")
	has(t, si, "due: $2,500.00")

	pi := `{"object":"list","data":[{"id":"pi_1","amount":250000,"currency":"usd","status":"requires_payment_method","customer":"cus_x"},{"id":"pi_2","amount":40000,"currency":"usd","status":"succeeded","customer":null}]}`
	sp := sumS(stripeHandler, "mcp__stripe__list_payment_intents", pi)
	has(t, sp, "$2,500.00")
	has(t, sp, "$400.00")

	bal := sumS(stripeHandler, "mcp__stripe__retrieve_balance", `{"object":"balance","available":[{"amount":50000,"currency":"usd"}],"pending":[{"amount":10000,"currency":"usd"}]}`)
	has(t, bal, "available: $500.00")
	has(t, bal, "pending: $100.00")

	eq(t, sumS(stripeHandler, "mcp__stripe__list_subscriptions", `{"object":"list","data":[]}`), "No items.")
	eq(t, sumS(stripeHandler, "mcp__stripe__list_subscriptions", "[]"), "No items.")

	jpy := sumS(stripeHandler, "mcp__stripe__list_payment_intents", `{"object":"list","data":[{"id":"pi_jpy","amount":1500,"currency":"jpy","status":"succeeded"}]}`)
	has(t, jpy, "¥1,500")
	hasNot(t, jpy, "¥15.00")

	var items []string
	for i := 0; i < 13; i++ {
		items = append(items, `{"id":"cus_`+itoaP(i)+`","name":"Customer `+itoaP(i)+`","email":"c@x.com"}`)
	}
	over := sumS(stripeHandler, "mcp__stripe__list_customers", `{"object":"list","data":[`+strings.Join(items, ",")+`]}`)
	has(t, over, "…and 3 more")
}

// helpers

func mkStdout(t *testing.T, s string) string {
	return `{"stdout":` + jsonx.Compact(s) + `,"stderr":"","exit_code":0}`
}

func itoaP(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}
