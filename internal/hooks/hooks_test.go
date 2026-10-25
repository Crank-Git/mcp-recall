// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/hooks/hooks_test.go

package hooks

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"mcprecall/internal/config"
	"mcprecall/internal/db"
	"mcprecall/internal/projectkey"
)

const sessionID = "test-session-abc123"

// hookEnv sets up a temp-file DB (so state persists across hook calls, unlike
// a fresh :memory: per Open) and returns the cwd + resolved project key.
func hookEnv(t *testing.T) (cwd, pk string) {
	t.Helper()
	dir := t.TempDir()
	cwd = t.TempDir() // non-git dir → project key is a stable hash of the path
	os.Setenv("RECALL_DB_PATH", filepath.Join(dir, "h.db"))
	os.Setenv("RECALL_CONFIG_PATH", filepath.Join(dir, "nope.toml"))
	config.Reset()
	t.Cleanup(func() {
		os.Unsetenv("RECALL_DB_PATH")
		os.Unsetenv("RECALL_CONFIG_PATH")
		config.Reset()
	})
	return cwd, projectkey.Key(cwd)
}

func ptu(toolName string, text string, extra map[string]any) string {
	p := map[string]any{
		"session_id":    sessionID,
		"cwd":           currentCWD,
		"tool_name":     toolName,
		"tool_input":    map[string]any{},
		"tool_response": map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}},
	}
	for k, v := range extra {
		p[k] = v
	}
	b, _ := json.Marshal(p)
	return string(b)
}

func sessionStart(extra map[string]any) string {
	p := map[string]any{"session_id": sessionID, "cwd": currentCWD, "hook_event_name": "SessionStart"}
	for k, v := range extra {
		p[k] = v
	}
	b, _ := json.Marshal(p)
	return string(b)
}

var currentCWD string

func captureStderr(fn func()) string {
	old := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	fn()
	w.Close()
	os.Stderr = old
	var buf bytes.Buffer
	io.Copy(&buf, r)
	return buf.String()
}

// largeGithub returns a compressible github list response.
func largeGithub() string {
	var issues []map[string]any
	for i := 0; i < 5; i++ {
		issues = append(issues, map[string]any{
			"number": i + 1, "title": "Issue number " + itoa(i+1) + " with a descriptive title",
			"state": "open", "html_url": "https://github.com/org/repo/issues/" + itoa(i+1),
			"labels": []any{map[string]any{"name": "bug"}},
			// Long enough that a summary saves the 25% minimum (upstream #319).
			"body": strings.Repeat("x", 900),
		})
	}
	b, _ := json.Marshal(issues)
	return string(b)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func isEmpty(o HookOutput) bool {
	return o.UpdatedMCPToolOutput == "" && !o.SuppressOutput && o.HookSpecificOutput == nil
}

// ── handleSessionStart ────────────────────────────────────────────────────────

func TestSessionStartRecordsAndIdempotent(t *testing.T) {
	currentCWD, _ = hookEnv(t)
	HandleSessionStart(sessionStart(nil))
	HandleSessionStart(sessionStart(nil)) // idempotent
	database, _ := db.Open(os.Getenv("RECALL_DB_PATH"))
	defer database.Close()
	days := db.GetSessionDays(database)
	today := time.Now().UTC().Format("2006-01-02")
	n := 0
	for _, d := range days {
		if d == today {
			n++
		}
	}
	if n != 1 {
		t.Errorf("today recorded %d times, want 1", n)
	}
}

func recordedPath(t *testing.T) string {
	t.Helper()
	database, _ := db.Open(os.Getenv("RECALL_DB_PATH"))
	defer database.Close()
	p, _ := db.GetMeta(database, "project_path")
	return p
}

func TestSessionStartRecordsProjectPath(t *testing.T) {
	currentCWD, _ = hookEnv(t)
	HandleSessionStart(sessionStart(nil))
	if got := recordedPath(t); got != projectkey.Path(currentCWD) {
		t.Errorf("project_path = %q, want %q", got, projectkey.Path(currentCWD))
	}
}

// A relative payload cwd is rooted against this process's cwd; recording a path
// that doesn't exist would let gc read the live project as orphaned.
func TestSessionStartSkipsNonexistentPath(t *testing.T) {
	hookEnv(t)
	currentCWD = "definitely-not-a-real-dir-xyz"
	HandleSessionStart(sessionStart(nil))
	if got := recordedPath(t); got != "" {
		t.Errorf("project_path = %q, want unrecorded", got)
	}
}

func TestSessionStartSkipsFilePath(t *testing.T) {
	hookEnv(t)
	f := filepath.Join(t.TempDir(), "not-a-dir")
	os.WriteFile(f, []byte("x"), 0o644)
	currentCWD = f
	HandleSessionStart(sessionStart(nil))
	if got := recordedPath(t); got != "" {
		t.Errorf("project_path = %q, want unrecorded for a file", got)
	}
}

// SetMeta upserts, so a session that can't verify its path leaves an earlier
// verified one in place.
func TestSessionStartKeepsVerifiedPath(t *testing.T) {
	currentCWD, _ = hookEnv(t)
	HandleSessionStart(sessionStart(nil))
	first := recordedPath(t)
	if first == "" {
		t.Fatal("first session recorded nothing")
	}
	currentCWD = "definitely-not-a-real-dir-xyz"
	HandleSessionStart(sessionStart(nil))
	if got := recordedPath(t); got != first {
		t.Errorf("project_path = %q, want kept %q", got, first)
	}
}

func TestSessionStartInjection(t *testing.T) {
	var pk string
	currentCWD, pk = hookEnv(t)
	// empty store → no snapshot
	if snap := HandleSessionStart(sessionStart(nil)); snap != "" {
		t.Errorf("expected empty snapshot, got %q", snap)
	}

	// pinned item → snapshot contains its summary
	database, _ := db.Open(os.Getenv("RECALL_DB_PATH"))
	s, _ := db.StoreOutput(database, db.StoreInput{ProjectKey: pk, SessionID: "sess-inject-001",
		ToolName: "mcp__github__list_issues", Summary: "pinned item summary for injection test",
		FullContent: "full content here", OriginalSize: 100})
	db.PinOutput(database, s.ID, pk, true)
	database.Close()
	if snap := HandleSessionStart(sessionStart(nil)); !strings.Contains(snap, "pinned item summary for injection test") {
		t.Errorf("snapshot missing pinned summary: %q", snap)
	}
}

func TestSessionStartTruncatesLarge(t *testing.T) {
	var pk string
	currentCWD, pk = hookEnv(t)
	database, _ := db.Open(os.Getenv("RECALL_DB_PATH"))
	for i := 0; i < 15; i++ {
		s, _ := db.StoreOutput(database, db.StoreInput{ProjectKey: pk, SessionID: "sess-inject-002",
			ToolName: "mcp__github__list_issues", Summary: "pinned item " + itoa(i) + " — " + strings.Repeat("x", 80),
			FullContent: "full", OriginalSize: 100})
		db.PinOutput(database, s.ID, pk, true)
	}
	database.Close()
	snap := HandleSessionStart(sessionStart(nil))
	if len([]rune(snap)) >= 2100 {
		t.Errorf("snapshot not truncated: %d runes", len([]rune(snap)))
	}
	if !strings.Contains(snap, "truncated") {
		t.Errorf("missing truncation note")
	}
}

func TestSessionStartUnknownProject(t *testing.T) {
	currentCWD, _ = hookEnv(t)
	database, _ := db.Open(os.Getenv("RECALL_DB_PATH"))
	db.StoreOutput(database, db.StoreInput{ProjectKey: "completely-different-project-key",
		SessionID: "sess-other", ToolName: "mcp__github__list_issues", Summary: "should not appear",
		FullContent: "full", OriginalSize: 100})
	database.Close()
	if snap := HandleSessionStart(sessionStart(nil)); snap != "" {
		t.Errorf("should not inject unknown project: %q", snap)
	}
}

// ── handlePostToolUse ─────────────────────────────────────────────────────────

func TestPostDeniedSecretsAndSmall(t *testing.T) {
	currentCWD, _ = hookEnv(t)
	if !isEmpty(HandlePostToolUse(ptu("mcp__recall__search", "x", nil))) {
		t.Error("recall tool should be denied → {}")
	}
	if !isEmpty(HandlePostToolUse(ptu("mcp__1password__item_lookup", "secret=abc", nil))) {
		t.Error("1password should be denied → {}")
	}
	if !isEmpty(HandlePostToolUse(ptu("mcp__github__get_file_contents", "-----BEGIN RSA PRIVATE KEY-----\nMIIE...", nil))) {
		t.Error("secret content → {}")
	}
	if !isEmpty(HandlePostToolUse(ptu("mcp__github__list_issues", "tiny", nil))) {
		t.Error("too-small output → {}")
	}
}

var recallHeaderRe = regexp.MustCompile(`^\[recall:recall_[0-9a-f]{16}`)
var sizeReductionRe = regexp.MustCompile(`→\d+(\.\d+)?(B|KB|MB)`)
var hintRe = regexp.MustCompile(`· search: "[^"]+"`)

func TestPostCompressesWithHeaderAndHints(t *testing.T) {
	currentCWD, _ = hookEnv(t)
	r := HandlePostToolUse(ptu("mcp__github__list_issues", largeGithub(), nil))
	if r.UpdatedMCPToolOutput == "" || !r.SuppressOutput {
		t.Fatal("expected compressed output + suppressOutput")
	}
	if !recallHeaderRe.MatchString(r.UpdatedMCPToolOutput) {
		t.Errorf("missing recall header: %q", r.UpdatedMCPToolOutput)
	}
	if !strings.Contains(r.UpdatedMCPToolOutput, "% reduction") || !sizeReductionRe.MatchString(r.UpdatedMCPToolOutput) {
		t.Errorf("missing size/reduction: %q", r.UpdatedMCPToolOutput)
	}
	if !hintRe.MatchString(r.UpdatedMCPToolOutput) {
		t.Errorf("missing hints: %q", r.UpdatedMCPToolOutput)
	}
}

func TestPostOmitsHintsForStopwords(t *testing.T) {
	currentCWD, _ = hookEnv(t)
	r := HandlePostToolUse(ptu("mcp__notes__dump", strings.Repeat("the\n", 4000), nil))
	if r.UpdatedMCPToolOutput == "" {
		t.Fatal("should compress")
	}
	if strings.Contains(r.UpdatedMCPToolOutput, "search:") {
		t.Errorf("should omit hints for stopword-only content: %q", firstLine(r.UpdatedMCPToolOutput))
	}
}

func TestPostStoresExtractedContent(t *testing.T) {
	currentCWD, _ = hookEnv(t)
	HandlePostToolUse(ptu("mcp__github__list_issues", largeGithub(), nil))
	database, _ := db.Open(os.Getenv("RECALL_DB_PATH"))
	defer database.Close()
	items := db.ExportAll(database, projectkey.Key(currentCWD))
	if len(items) == 0 {
		t.Fatal("nothing stored")
	}
	it := items[0]
	if it.SessionID != sessionID {
		t.Errorf("session_id = %q", it.SessionID)
	}
	// full_content is extracted text, not the MCP wrapper
	if !strings.Contains(it.FullContent, `"number"`) || strings.Contains(it.FullContent, `"content":[{`) {
		t.Errorf("full_content is the wrapper, not extracted text")
	}
}

func TestPostDedupInputHash(t *testing.T) {
	currentCWD, _ = hookEnv(t)
	in := ptu("mcp__github__list_issues", largeGithub(), map[string]any{"tool_input": map[string]any{"owner": "org", "repo": "repo"}})
	first := HandlePostToolUse(in)
	firstID := recallHeaderRe.FindString(first.UpdatedMCPToolOutput)
	second := HandlePostToolUse(in)
	if !strings.Contains(second.UpdatedMCPToolOutput, "· cached · ") || !second.SuppressOutput {
		t.Errorf("second call should be cached: %q", firstLine(second.UpdatedMCPToolOutput))
	}
	if !strings.Contains(second.UpdatedMCPToolOutput, strings.TrimPrefix(firstID, "[")) {
		t.Errorf("cached header should reuse original id")
	}
	database, _ := db.Open(os.Getenv("RECALL_DB_PATH"))
	defer database.Close()
	if got := db.ExportAll(database, projectkey.Key(currentCWD)); len(got) != 1 {
		t.Errorf("cache hit should not store a 2nd item, got %d", len(got))
	}
}

func TestPostDedupOutputHash(t *testing.T) {
	currentCWD, _ = hookEnv(t)
	resp := largeGithub()
	first := HandlePostToolUse(ptu("mcp__github__list_issues", resp, map[string]any{"tool_input": map[string]any{"page": 1}}))
	if first.UpdatedMCPToolOutput == "" {
		t.Fatal("first should store")
	}
	second := HandlePostToolUse(ptu("mcp__github__list_issues", resp, map[string]any{"tool_input": map[string]any{"page": 2}}))
	if !strings.Contains(second.UpdatedMCPToolOutput, "· cached · ") {
		t.Errorf("identical content, different input → content-hash cache: %q", firstLine(second.UpdatedMCPToolOutput))
	}
	database, _ := db.Open(os.Getenv("RECALL_DB_PATH"))
	defer database.Close()
	if got := db.ExportAll(database, projectkey.Key(currentCWD)); len(got) != 1 {
		t.Errorf("output-dedup should reuse first item, got %d", len(got))
	}
}

func TestPostEviction(t *testing.T) {
	dir := t.TempDir()
	cwd := t.TempDir()
	currentCWD = cwd
	dbPath := filepath.Join(dir, "h.db")
	cfgPath := filepath.Join(dir, "config.toml")
	os.WriteFile(cfgPath, []byte("[store]\nmax_size_mb = 0.003\n"), 0o644)
	os.Setenv("RECALL_DB_PATH", dbPath)
	os.Setenv("RECALL_CONFIG_PATH", cfgPath)
	config.Reset()
	defer func() {
		os.Unsetenv("RECALL_DB_PATH")
		os.Unsetenv("RECALL_CONFIG_PATH")
		config.Reset()
	}()

	pk := projectkey.Key(cwd)
	database, _ := db.Open(dbPath)
	oldTs := time.Now().Unix() - 60
	database.Exec(`INSERT INTO stored_outputs (id,project_key,session_id,tool_name,summary,full_content,original_size,summary_size,created_at) VALUES ('recall_evict0001',?,'session','mcp__old__tool','old','old content',2000,3,?)`, pk, oldTs)
	database.Close()

	HandlePostToolUse(ptu("mcp__github__list_issues", largeGithub(), nil))

	database2, _ := db.Open(dbPath)
	defer database2.Close()
	if db.HasID(database2, "recall_evict0001") {
		t.Error("old low-value item should have been evicted")
	}
}

func TestPostMalformed(t *testing.T) {
	currentCWD, _ = hookEnv(t)
	for _, in := range []string{"", "not json at all", `{"session_id":"x","cwd":"/tmp"`, "[]", `"just a string"`} {
		if !isEmpty(HandlePostToolUse(in)) {
			t.Errorf("malformed %q should return {}", in)
		}
	}
	if !strings.Contains(captureStderr(func() { HandlePostToolUse("bad input") }), "invalid JSON") {
		t.Error("missing invalid JSON log")
	}
	if !strings.Contains(captureStderr(func() { HandlePostToolUse("[]") }), "unexpected input shape") {
		t.Error("missing shape error log")
	}
}

func TestSessionStartMalformed(t *testing.T) {
	currentCWD, _ = hookEnv(t)
	for _, in := range []string{"", "not json", `{"session_id":"x"`, "[]"} {
		HandleSessionStart(in) // must not panic
	}
	if !strings.Contains(captureStderr(func() { HandleSessionStart("bad input") }), "invalid JSON") {
		t.Error("missing invalid JSON log")
	}
	if !strings.Contains(captureStderr(func() { HandleSessionStart("[]") }), "unexpected input shape") {
		t.Error("missing shape error log")
	}
}

func TestPostDebugLogging(t *testing.T) {
	currentCWD, _ = hookEnv(t)
	os.Setenv("RECALL_DEBUG", "1")
	defer os.Unsetenv("RECALL_DEBUG")

	deny := captureStderr(func() { HandlePostToolUse(ptu("mcp__1password__item_lookup", "x", nil)) })
	if !strings.Contains(deny, "SKIP denylist") || !strings.Contains(deny, "mcp__1password__item_lookup") {
		t.Errorf("denylist debug missing: %q", deny)
	}
	small := captureStderr(func() { HandlePostToolUse(ptu("mcp__github__list_issues", "tiny", nil)) })
	if !strings.Contains(small, "SKIP no-compression") {
		t.Errorf("no-compression debug missing")
	}
	stored := captureStderr(func() { HandlePostToolUse(ptu("mcp__github__list_issues", largeGithub(), nil)) })
	if !strings.Contains(stored, "STORED") || !strings.Contains(stored, "reduction") {
		t.Errorf("STORED debug missing: %q", stored)
	}
	if !strings.Contains(stored, "handler:") || !strings.Contains(stored, "githubHandler") {
		t.Errorf("handler-name debug missing: %q", stored)
	}
	if !strings.Contains(stored, "intercepted mcp__github__list_issues") {
		t.Errorf("intercepted debug missing")
	}
	// CACHE HIT (same input twice)
	in := ptu("mcp__github__list_issues", largeGithub(), map[string]any{"tool_input": map[string]any{"o": "r"}})
	HandlePostToolUse(in)
	hit := captureStderr(func() { HandlePostToolUse(in) })
	if !strings.Contains(hit, "CACHE HIT") {
		t.Errorf("CACHE HIT debug missing: %q", hit)
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// Default retention is balanced: reproducible Bash is stored summary-only while
// a network fetch keeps its body. (A `cat` is a content read and keeps its body
// since upstream #332, so the reproducible case is a script.)
func TestPostRetentionWiring(t *testing.T) {
	var lines []string
	for i := 0; i < 300; i++ {
		lines = append(lines, "line "+itoa(i)+" of some reasonably long output text here")
	}
	stdout := strings.Join(lines, "\n")
	for _, tc := range []struct {
		command  string
		retained int
	}{{"./scripts/build.sh", 0}, {"curl https://example.com/data", 1}} {
		currentCWD, _ = hookEnv(t)
		payload := map[string]any{
			"session_id": sessionID, "cwd": currentCWD, "tool_name": "Bash",
			"tool_input":    map[string]any{"command": tc.command},
			"tool_response": map[string]any{"stdout": stdout, "stderr": "", "interrupted": false},
		}
		b, _ := json.Marshal(payload)
		if out := HandlePostToolUse(string(b)); isEmpty(out) {
			t.Fatalf("%s: not intercepted", tc.command)
		}
		database, _ := db.Open(os.Getenv("RECALL_DB_PATH"))
		items := db.ExportAll(database, projectkey.Key(currentCWD))
		database.Close()
		if len(items) != 1 {
			t.Fatalf("%s: stored %d rows, want 1", tc.command, len(items))
		}
		if items[0].FullRetained != tc.retained {
			t.Errorf("%s: full_retained = %d, want %d", tc.command, items[0].FullRetained, tc.retained)
		}
		if hasBody := items[0].FullContent != ""; hasBody != (tc.retained == 1) {
			t.Errorf("%s: body present = %v, want %v", tc.command, hasBody, tc.retained == 1)
		}
	}
}

// A Bash row stores its command family; NormalizeCommand strips --no-pager and
// only the bare subcommand is kept, never the argument.
func TestPostStoresCommandFingerprint(t *testing.T) {
	currentCWD, _ = hookEnv(t)
	// Above the 8 KB content-read floor, or the diff passes through unstored.
	bigDiff := "diff --git a/f b/f\n" + strings.Repeat("+added line\n", 1000)
	resp, _ := json.Marshal(map[string]any{"stdout": bigDiff, "stderr": "", "exit_code": 0})
	payload := map[string]any{
		"session_id": sessionID, "cwd": currentCWD, "tool_name": "Bash",
		"tool_input":    map[string]any{"command": "git --no-pager diff HEAD~1"},
		"tool_response": string(resp),
	}
	b, _ := json.Marshal(payload)
	if isEmpty(HandlePostToolUse(string(b))) {
		t.Fatal("not intercepted")
	}
	database, _ := db.Open(os.Getenv("RECALL_DB_PATH"))
	defer database.Close()
	items := db.ExportAll(database, projectkey.Key(currentCWD))
	if len(items) != 1 || items[0].CommandFP == nil || *items[0].CommandFP != "git diff" {
		t.Fatalf("stored rows = %d, command_fp = %v", len(items), items[0].CommandFP)
	}
}

// ── image content blocks (upstream #270) ────────────────────────────────────

func chromeScreenshot() (payload []any, imageData string) {
	raw := make([]byte, 32*1024)
	raw[0], raw[1], raw[2], raw[3] = 0xff, 0xd8, 0xff, 0xe2
	copy(raw[4:], "ICC_PROFILE")
	for i := 16; i < len(raw); i++ {
		raw[i] = byte((i*17 + 31) & 0xff)
	}
	imageData = base64.StdEncoding.EncodeToString(raw)
	return []any{
		map[string]any{"type": "text", "text": "Successfully captured screenshot (1419x840, jpeg) - ID: ss_135241jnk"},
		map[string]any{"type": "text", "text": "\n\nTab Context:\n- https://example.com/dashboard\n- Title: Dashboard"},
		map[string]any{"type": "image", "mimeType": "image/jpeg", "data": imageData},
	}, imageData
}

func ptuRaw(toolName string, response any) string {
	b, _ := json.Marshal(map[string]any{
		"session_id": sessionID, "cwd": currentCWD, "tool_name": toolName,
		"tool_input": map[string]any{}, "tool_response": response,
	})
	return string(b)
}

var reductionRe = regexp.MustCompile(`\((\d+)% reduction\)`)

func TestPostReplacesScreenshot(t *testing.T) {
	currentCWD, _ = hookEnv(t)
	payload, imageData := chromeScreenshot()
	out := HandlePostToolUse(ptuRaw("mcp__claude-in-chrome__computer", payload))
	if out.UpdatedMCPToolOutput == "" || !out.SuppressOutput {
		t.Fatal("screenshot passed through instead of being replaced")
	}
	body := out.UpdatedMCPToolOutput
	for _, want := range []string{"ss_135241jnk", "1419x840", "jpeg", "Tab Context"} {
		if !strings.Contains(body, want) {
			t.Errorf("summary missing %q", want)
		}
	}
	if strings.Contains(body, imageData) {
		t.Error("summary contains the image bytes")
	}
	m := reductionRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no reduction in header: %q", body[:min(len(body), 200)])
	}
	if pct, _ := strconv.Atoi(m[1]); pct <= 90 {
		t.Errorf("reduction %d%%, want > 90", pct)
	}

	database, _ := db.Open(os.Getenv("RECALL_DB_PATH"))
	defer database.Close()
	items := db.ExportAll(database, projectkey.Key(currentCWD))
	if len(items) != 1 || !strings.Contains(items[0].FullContent, "ss_135241jnk") || strings.Contains(items[0].FullContent, imageData) {
		t.Errorf("stored full_content should be the stripped text")
	}
}

func TestPostTextOnlyBlocksKeepText(t *testing.T) {
	currentCWD, _ = hookEnv(t)
	payload := []any{
		map[string]any{"type": "text", "text": "Scrolled down 400 pixels"},
		map[string]any{"type": "text", "text": "\n\nTab Context:\n- https://example.com/page\n- Title: Page"},
	}
	out := HandlePostToolUse(ptuRaw("mcp__claude-in-chrome__computer", payload))
	if regexp.MustCompile(`(?i)secret|denied|refus`).MatchString(out.UpdatedMCPToolOutput) {
		t.Errorf("text-only payload refused: %q", out.UpdatedMCPToolOutput)
	}
	if out.UpdatedMCPToolOutput != "" && (!strings.Contains(out.UpdatedMCPToolOutput, "Scrolled down 400 pixels") ||
		!strings.Contains(out.UpdatedMCPToolOutput, "Tab Context")) {
		t.Errorf("text lost: %q", out.UpdatedMCPToolOutput)
	}
}
