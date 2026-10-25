// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// cmd/bench/largegen.go
//
// Deterministic generators for large, realistic tool outputs. These stress the
// handlers with the payload sizes that make recall worth running (hundreds of
// KB), without bloating the repo with embedded fixtures. Everything is index-
// derived, so runs are fully reproducible.

package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"mcprecall/internal/jsonx"
)

// generatedFixtures returns the large synthetic fixtures appended to the corpus.
func generatedFixtures() []Fixture {
	bashInput := func(cmd string) *jsonx.Obj {
		o := jsonx.NewObj()
		o.Set("command", cmd)
		return o
	}
	return []Fixture{
		{"large", "github issues ×300", "mcp__github__list_issues", nil, genGithubIssues(300), nil},
		{"large", "jira search ×250", "mcp__jira__search_issues", nil, genJiraIssues(250), nil},
		{"large", "stripe events ×400", "mcp__stripe__list_events", nil, genStripeEvents(400), nil},
		{"large", "postgres rows ×1500", "mcp__postgres__query", nil, genPostgresRows(1500), nil},
		{"large", "playwright DOM (big)", "mcp__playwright__browser_snapshot", nil, genPlaywright(450), nil},
		{"large", "server log ×3000", "mcp__unknown__fetch_logs", nil, genServerLog(3000), nil},
		{"large", "read_file (big source)", "mcp__filesystem__read_file", nil, genReadFile(2600), nil},
		{"cli", "Bash: git status ×500", "Bash", bashInput("git status"), genGitStatus(500), nil},
		{"cli", "Bash: go test ×1200", "Bash", bashInput("go test ./..."), genTestRunner(1200), nil},
		{"cli", "Bash: docker ps ×120", "Bash", bashInput("docker ps -a"), genDockerPs(120), nil},
		// Upstream's command-aware Bash fixtures (scripts/benchmark.ts, #249).
		{"cli", "Bash: tsc --noEmit (60 errors)", "Bash", bashInput("tsc --noEmit"), genTscErrors(), []string{"60 errors", `file59\.ts`}},
		{"cli", "Bash: cargo build (failure)", "Bash", bashInput("cargo build"), genCargoBuild(), nil},
		{"cli", "Bash: git --no-pager diff ×18", "Bash", bashInput("git --no-pager diff"), genGitDiff(), []string{`f17\.ts`, `\+new 6`}},
		{"cli", "Bash: rg ×240 (6 files)", "Bash", bashInput("rg --no-heading doThing"), genRgMatches(), []string{"240 matches", "mod5/"}},
		{"cli", "Bash: ls -R (deep tree)", "Bash", bashInput("ls -R"), genLsR(), nil},
		{"cli", "Bash: find ×400", "Bash", bashInput("find . -name '*.ts'"), genFindPaths(), []string{"400", `file399\.ts`}},
	}
}

// ── pools (cycled deterministically by index) ─────────────────────────────────

var (
	poolUsers  = []string{"acidvegas", "contributor7", "maintainer", "qa-team", "winuser", "poweruser", "newuser"}
	poolStates = []string{"open", "closed"}
	poolTitles = []string{
		"Panic in eviction when half-life is zero", "FTS search misses hyphenated identifiers",
		"Add graduated retrieval tiers", "Compression regression on large snapshots",
		"Secret scanner false-positive on base64", "Per-tool breakdown in stats",
		"Windows path handling in project key", "Support NEAR queries in search",
		"Flaky WAL test under load", "Import command skips duplicates",
		"Decay eviction keeps fresh items", "Content-hash dedup across tools",
	}
	poolBodies = []string{
		"Reproduces on both in-memory and on-disk stores. Stack trace attached; expected a graceful fallback rather than a crash.",
		"The tokenizer splits on the separator so the term never matches. Quoting works but users won't discover that on their own.",
		"Implement summary, peek, and full modes so the model can pull a bounded window before committing to the whole payload.",
		"Ratio dropped after the accessibility-tree change; the new role annotations are probably not being elided. Needs a benchmark.",
		"Long high-entropy blobs from image tools trip the generic check and get blocked. We should exempt recognized data URIs.",
	}
)

func pick(pool []string, i int) string { return pool[i%len(pool)] }

// ── generators ────────────────────────────────────────────────────────────────

func genGithubIssues(n int) string {
	var b strings.Builder
	b.WriteByte('[')
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		num := 500 - i
		id := 2451000000 + i
		user := pick(poolUsers, i)
		state := pick(poolStates, i)
		fmt.Fprintf(&b, `{"id":%d,"node_id":"I_kwDO%08d","number":%d,"title":%q,`+
			`"url":"https://api.github.com/repos/acidvegas/mcprecall/issues/%d",`+
			`"repository_url":"https://api.github.com/repos/acidvegas/mcprecall",`+
			`"labels_url":"https://api.github.com/repos/acidvegas/mcprecall/issues/%d/labels{/name}",`+
			`"comments_url":"https://api.github.com/repos/acidvegas/mcprecall/issues/%d/comments",`+
			`"html_url":"https://github.com/acidvegas/mcprecall/issues/%d","state":%q,"locked":false,`+
			`"user":{"login":%q,"id":%d,"node_id":"MDQ6VXNlc%08d","avatar_url":"https://avatars.githubusercontent.com/u/%d?v=4",`+
			`"url":"https://api.github.com/users/%s","html_url":"https://github.com/%s","type":"User","site_admin":false},`+
			`"labels":[{"id":%d,"node_id":"MDU6TGFiZWw%08d","url":"https://api.github.com/repos/acidvegas/mcprecall/labels/bug","name":"bug","color":"d73a4a","default":true,"description":"Something isn't working"}],`+
			`"assignee":null,"milestone":null,"comments":%d,"created_at":"2026-05-%02dT10:00:00Z","updated_at":"2026-05-%02dT12:00:00Z","closed_at":null,`+
			`"author_association":"CONTRIBUTOR",`+
			`"reactions":{"url":"https://api.github.com/repos/acidvegas/mcprecall/issues/%d/reactions","total_count":%d,"+1":%d,"-1":0,"laugh":0,"hooray":0,"confused":0,"heart":0,"rocket":0,"eyes":0},`+
			`"timeline_url":"https://api.github.com/repos/acidvegas/mcprecall/issues/%d/timeline","body":%q}`,
			id, id, num, fmt.Sprintf("%s (#%d)", pick(poolTitles, i), num),
			num, num, num, num, state,
			user, 10000000+i, id, 10000000+i, user, user,
			208045000+i, id,
			i%12, (i%28)+1, (i%28)+1,
			num, i%9, i%9,
			num, pick(poolBodies, i))
	}
	b.WriteByte(']')
	return b.String()
}

func genJiraIssues(n int) string {
	var b strings.Builder
	b.WriteString(`{"expand":"schema,names","startAt":0,"maxResults":`)
	fmt.Fprintf(&b, `%d,"total":%d,"issues":[`, n, n*3)
	statuses := []string{"To Do", "In Progress", "In Review", "Done", "Blocked"}
	types := []string{"Bug", "Story", "Task", "Epic"}
	prios := []string{"Critical", "High", "Medium", "Low"}
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		key := 600 - i
		fmt.Fprintf(&b, `{"expand":"operations,versionedRepresentations,editmeta,changelog,renderedFields","id":"%d","self":"https://acme.atlassian.net/rest/api/2/issue/%d","key":"PROJ-%d","fields":{`+
			`"summary":%q,`+
			`"status":{"self":"https://acme.atlassian.net/rest/api/2/status/%d","description":"status","iconUrl":"https://acme.atlassian.net/images/icons/statuses/x.png","name":%q,"id":"%d","statusCategory":{"self":"https://acme.atlassian.net/rest/api/2/statuscategory/4","id":4,"key":"indeterminate","colorName":"yellow","name":"In Progress"}},`+
			`"assignee":{"self":"https://acme.atlassian.net/rest/api/2/user?accountId=a%d","accountId":"a%d","emailAddress":"user%d@acme.com","displayName":%q,"active":true,"timeZone":"America/New_York","accountType":"atlassian"},`+
			`"priority":{"self":"https://acme.atlassian.net/rest/api/2/priority/%d","iconUrl":"https://acme.atlassian.net/images/icons/priorities/x.svg","name":%q,"id":"%d"},`+
			`"issuetype":{"self":"https://acme.atlassian.net/rest/api/2/issuetype/1000%d","id":"1000%d","description":"an issue type","iconUrl":"https://acme.atlassian.net/x.png","name":%q,"subtask":false,"avatarId":10303},`+
			`"created":"2026-05-%02dT10:00:00.000+0000","updated":"2026-05-%02dT12:00:00.000+0000","reporter":{"accountId":"r%d","displayName":"Reporter %d","active":true},`+
			`"watches":{"self":"https://acme.atlassian.net/x","watchCount":%d,"isWatching":false},"project":{"self":"https://acme.atlassian.net/rest/api/2/project/10000","id":"10000","key":"PROJ","name":"Platform","projectTypeKey":"software"},"labels":["a","b"],"components":[{"id":"10100","name":"core"}],"worklog":{"startAt":0,"maxResults":20,"total":0,"worklogs":[]}}}`,
			100000+i, 100000+i, key,
			fmt.Sprintf("%s [PROJ-%d]", pick(poolTitles, i), key),
			i%5, pick(statuses, i), i%5,
			i, i, i, fmt.Sprintf("User %d", i),
			(i%4)+1, pick(prios, i), (i%4)+1,
			i%4, i%4, pick(types, i),
			(i%28)+1, (i%28)+1, i, i,
			i%9)
	}
	b.WriteString(`]}`)
	return b.String()
}

func genStripeEvents(n int) string {
	var b strings.Builder
	b.WriteString(`{"object":"list","url":"/v1/events","has_more":true,"data":[`)
	types := []string{"charge.succeeded", "customer.subscription.updated", "invoice.payment_failed", "charge.refunded", "payment_intent.succeeded", "checkout.session.completed"}
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"id":"evt_1Px%06d","object":"event","api_version":"2026-01-01","type":%q,"created":%d,"livemode":true,`+
			`"pending_webhooks":1,"request":{"id":"req_%08d","idempotency_key":null},`+
			`"data":{"object":{"id":"ch_3Px%06d","object":"charge","amount":%d,"amount_captured":%d,"currency":"usd","customer":"cus_Qe%06d","description":"Pro plan monthly invoice","status":"succeeded","payment_method":"pm_1Px%06d","receipt_url":"https://pay.stripe.com/receipts/x/%08d","billing_details":{"email":"user%d@example.com","name":"Customer %d","address":{"city":"NYC","country":"US","line1":"1 Main St","postal_code":"10001"}}}}}`,
			i, pick(types, i), 1717243811-i*100,
			i, i, 4999+(i%50)*100, 4999+(i%50)*100, i, i, i, i, i)
	}
	b.WriteString(`]}`)
	return b.String()
}

func genPostgresRows(n int) string {
	var b strings.Builder
	fmt.Fprintf(&b, `{"command":"SELECT","rowCount":%d,"fields":["id","tool_name","project_key","items","original_bytes","summary_bytes","ratio","created_at"],"rows":[`, n)
	tools := []string{"mcp__github__list_issues", "mcp__playwright__browser_snapshot", "mcp__stripe__list_events", "mcp__slack__conversations_history", "mcp__filesystem__read_file"}
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		orig := 100000 + i*37
		summ := orig / (10 + i%15)
		fmt.Fprintf(&b, `{"id":"recall_%016x","tool_name":%q,"project_key":"proj_%08x","items":%d,"original_bytes":%d,"summary_bytes":%d,"ratio":%.3f,"created_at":"2026-05-%02dT%02d:00:00Z"}`,
			i*2654435761, pick(tools, i), i*40503, i%50, orig, summ, float64(summ)/float64(orig), (i%28)+1, i%24)
	}
	b.WriteString(`]}`)
	return b.String()
}

func genPlaywright(rows int) string {
	var b strings.Builder
	b.WriteString("- document \"Dashboard — Acme Analytics\"\n  - banner\n    - link \"Acme Analytics\" [ref=s1e2]\n    - navigation \"Primary\"\n      - list\n")
	for i := 0; i < 6; i++ {
		fmt.Fprintf(&b, "        - listitem: link %q [ref=s1e%d]\n", fmt.Sprintf("Nav %d", i), 10+i)
	}
	b.WriteString("  - main\n    - heading \"Overview\" [level=1] [ref=s1e40]\n    - region \"Data\"\n      - table\n")
	for i := 0; i < rows; i++ {
		fmt.Fprintf(&b, "        - row [ref=s2e%d]: cell %q; cell %q; cell %q; cell %q; cell %q\n",
			1000+i,
			fmt.Sprintf("event-%05d", i),
			fmt.Sprintf("user-%04d@acme.com", i%400),
			fmt.Sprintf("%d.%02d.%02d", 2026, (i%12)+1, (i%28)+1),
			fmt.Sprintf("%d ms", 20+i%900),
			pick([]string{"success", "warning", "error", "pending"}, i))
	}
	b.WriteString("  - contentinfo\n    - text \"© 2026 Acme Analytics\"\n    - link \"Privacy\" [ref=s9e1]\n    - link \"Terms\" [ref=s9e2]\n")
	return b.String()
}

func genServerLog(lines int) string {
	var b strings.Builder
	levels := []string{"INFO", "INFO", "INFO", "INFO", "WARN", "INFO", "INFO", "ERROR"}
	tools := []string{"mcp__github__list_issues", "mcp__playwright__browser_snapshot", "mcp__stripe__list_events", "mcp__slack__conversations_history"}
	for i := 0; i < lines; i++ {
		lvl := pick(levels, i)
		ts := fmt.Sprintf("2026-06-01T%02d:%02d:%02dZ", i/3600%24, i/60%60, i%60)
		switch lvl {
		case "WARN":
			fmt.Fprintf(&b, "%s WARN  slow query %dms: SELECT * FROM stored_outputs WHERE project_key=$1 ORDER BY created_at\n", ts, 500+i%800)
		case "ERROR":
			fmt.Fprintf(&b, "%s ERROR store failed: database is locked (attempt %d/3) tool=%s\n", ts, (i%3)+1, pick(tools, i))
		default:
			fmt.Fprintf(&b, "%s INFO  request POST /v1/compress 200 %dms tool=%s in=%d out=%d\n", ts, 4+i%40, pick(tools, i), 10000+i*13, 400+i*3)
		}
	}
	return b.String()
}

func genReadFile(lines int) string {
	var b strings.Builder
	for i := 0; i < lines; i++ {
		switch i % 8 {
		case 0:
			fmt.Fprintf(&b, "func handler%04d(toolName string, output any) Result {\n", i)
		case 1:
			b.WriteString("\traw := ExtractText(output)\n")
		case 2:
			b.WriteString("\toriginalSize := byteLen(raw)\n")
		case 3:
			fmt.Fprintf(&b, "\tparsed, err := jsonx.ParseString(raw) // line %d of the read file\n", i)
		case 4:
			b.WriteString("\tif err != nil {\n\t\treturn Result{Summary: firstChars(raw, 500), OriginalSize: originalSize}\n\t}\n")
		case 5:
			fmt.Fprintf(&b, "\titems, ok := resolveItems(parsed, []string{\"data\", \"nodes\", \"results\"}) // idx %d\n", i)
		case 6:
			b.WriteString("\t_ = items\n\t_ = ok\n")
		default:
			b.WriteString("}\n\n")
		}
	}
	return b.String()
}

func genGitStatus(nFiles int) string {
	var b strings.Builder
	b.WriteString("On branch main\nYour branch is up to date with 'origin/main'.\n\nChanges not staged for commit:\n")
	codes := []string{" M", "??", "A ", " D", "M ", "R "}
	dirs := []string{"internal/db", "internal/handlers", "internal/tools", "cmd/bench/corpus", "internal/profiles/bundled"}
	for i := 0; i < nFiles; i++ {
		fmt.Fprintf(&b, "\t%s %s/file_%04d.go\n", pick(codes, i), pick(dirs, i), i)
	}
	fmt.Fprintf(&b, "\n%d files changed\n", nFiles)
	return b.String()
}

func genTestRunner(nTests int) string {
	var b strings.Builder
	b.WriteString("=== RUN test suite\n")
	pass, fail := 0, 0
	pkgs := []string{"mcprecall/internal/db", "mcprecall/internal/handlers", "mcprecall/internal/tools", "mcprecall/internal/profiles"}
	for i := 0; i < nTests; i++ {
		if i%50 == 49 {
			fail++
			fmt.Fprintf(&b, "--- FAIL: Test%04d (%d.%02ds)\n    x_test.go:%d: expected %d, got %d\n", i, i%3, i%99, 100+i, i, i+1)
		} else {
			pass++
			fmt.Fprintf(&b, "--- PASS: Test%04d (0.%02ds)\n", i, i%9)
		}
		if i%300 == 299 {
			fmt.Fprintf(&b, "ok  \t%s\t%d.%03ds\n", pick(pkgs, i), i%5, i%999)
		}
	}
	fmt.Fprintf(&b, "\n%d pass, %d fail\nFAIL\n", pass, fail)
	return b.String()
}

func genDockerPs(n int) string {
	var b strings.Builder
	b.WriteString("CONTAINER ID   IMAGE                       COMMAND                  CREATED         STATUS                     PORTS                    NAMES\n")
	images := []string{"postgres:16", "redis:7", "nginx:1.27", "acme/api:3.2.1", "acme/worker:2.9.0", "grafana/grafana:11"}
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "%012x   %-26s  \"/entrypoint.sh run\"     %2d days ago     Up %2d hours (healthy)      0.0.0.0:%d->%d/tcp     svc_%04d\n",
			i*2654435761, pick(images, i), i%30, i%24, 20000+i, 8080, i)
	}
	return b.String()
}

// ── upstream benchmark fixtures (scripts/benchmark.ts) ────────────────────────

// bashResult wraps output the way a native Bash tool response arrives.
func bashResult(stdout, stderr string, exitCode int) string {
	b, _ := json.Marshal(map[string]any{"stdout": stdout, "stderr": stderr, "exit_code": exitCode})
	return string(b)
}

// rep repeats line n times, replacing every %i with the index.
func rep(line string, n int) string {
	out := make([]string, n)
	for i := range out {
		out[i] = strings.ReplaceAll(line, "%i", strconv.Itoa(i))
	}
	return strings.Join(out, "\n")
}

func genTscErrors() string {
	stdout := rep("src/module%i/file%i.ts(%i,10): error TS2345: Argument of type 'Foo%i' is not assignable to parameter of type 'Bar%i'.", 60) +
		"\n\nFound 60 errors in 60 files.\n" +
		rep("  at Object.<anonymous> (/repo/node_modules/typescript/lib/tsc.js:%i:40)", 300)
	return bashResult(stdout, "", 2)
}

func genCargoBuild() string {
	stderr := "   Compiling demo v0.1.0 (/home/u/demo)\n" +
		rep("error[E0308]: mismatched types\n  --> src/mod%i.rs:%i:20\n   |\n%i |     let x: u32 = \"hi\";\n   |            ---   ^^^^ expected `u32`, found `&str`\n   |", 12) +
		"\nerror: aborting due to 12 previous errors\nerror: could not compile `demo` (bin \"demo\") due to 12 previous errors"
	return bashResult("", stderr, 101)
}

func genGitDiff() string {
	files := make([]string, 18)
	for i := range files {
		files[i] = fmt.Sprintf("diff --git a/src/f%d.ts b/src/f%d.ts\nindex abc..def 100644\n--- a/src/f%d.ts\n+++ b/src/f%d.ts\n@@ -1,6 +1,8 @@\n", i, i, i, i) +
			rep(" context %i", 4) + "\n" + rep("-old %i", 5) + "\n" + rep("+new %i", 7)
	}
	return bashResult(strings.Join(files, "\n"), "", 0)
}

var modNumRe = regexp.MustCompile(`mod(\d+)`)

func genRgMatches() string {
	stdout := modNumRe.ReplaceAllStringFunc(rep("src/mod%i/orchestrator.ts:%i:  const r = doThing(ctx, %i)", 240), func(m string) string {
		n, _ := strconv.Atoi(m[3:])
		return "mod" + strconv.Itoa(n%6)
	})
	return bashResult(stdout, "", 0)
}

func genLsR() string {
	dirs := make([]string, 25)
	for i := range dirs {
		dirs[i] = fmt.Sprintf("./src/pkg%d:\n", i) + rep("a%i.ts", 8) + "\n"
	}
	return bashResult(strings.Join(dirs, "\n"), "", 0)
}

func genFindPaths() string {
	return bashResult(rep("./src/very/deeply/nested/path/segment/file%i.ts", 400), "", 0)
}
