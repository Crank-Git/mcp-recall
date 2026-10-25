// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/importer/importer_test.go

package importer

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcprecall/internal/db"
	"mcprecall/internal/tools"
)

const srcProject = "import_test_source_key"

func mkRow(id, tool, summary, content string) importRow {
	return importRow{
		ID: id, ProjectKey: srcProject, SessionID: "sess-abc", ToolName: tool,
		Summary: summary, FullContent: content, OriginalSize: 512,
		SummarySize: len(summary), CreatedAt: time.Now().Unix(), Pinned: 0, AccessCount: 0,
	}
}

func query(t *testing.T, path, sql string, args ...any) [][]any {
	t.Helper()
	database, err := db.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer database.Close()
	rows, err := database.Query(sql, args...)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var out [][]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		rows.Scan(ptrs...)
		out = append(out, vals)
	}
	return out
}

func TestImportRoundTripAndOrdering(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "t.db")
	a := mkRow("recall_aaaaaaaaaaaaaaa1", "mcp__github__list_issues", "Issue #1", `[{"number":1}]`)
	a.CreatedAt = 1000
	b := mkRow("recall_bbbbbbbbbbbbbbb2", "mcp__github__get_issue", "Issue #2", `[{"number":2}]`)
	b.CreatedAt = 2000
	res := importItems(dbPath, []importRow{a, b}, false, "current_project_key")
	if res.imported != 2 {
		t.Fatalf("imported = %d", res.imported)
	}
	rows := query(t, dbPath, "SELECT tool_name FROM stored_outputs ORDER BY created_at ASC")
	if len(rows) != 2 || rows[0][0] != "mcp__github__list_issues" || rows[1][0] != "mcp__github__get_issue" {
		t.Errorf("order/content wrong: %v", rows)
	}
}

// Every imported row is stamped with the current project's key, never the
// dump's — otherwise it lands in this project's database but is invisible to
// every project-scoped path (#226).
func TestImportAlwaysStampsCurrentProjectKey(t *testing.T) {
	p := filepath.Join(t.TempDir(), "t.db")
	importItems(p, []importRow{mkRow("recall_1111111111111111", "mcp__x", "s", "c")}, false, "current_project_key")
	got := query(t, p, "SELECT project_key FROM stored_outputs LIMIT 1")
	if got[0][0] != "current_project_key" {
		t.Errorf("project key = %v, want current_project_key", got[0][0])
	}
}

func TestKeepProjectKeyFlagRejected(t *testing.T) {
	msg := keepProjectKeyRejection([]string{"dump.json", "--keep-project-key"})
	if msg == "" {
		t.Fatal("--keep-project-key should be rejected")
	}
	if !strings.Contains(msg, "#226") {
		t.Errorf("rejection should cite the issue: %q", msg)
	}
	if keepProjectKeyRejection([]string{"dump.json", "--overwrite"}) != "" {
		t.Error("unrelated flags should not be rejected")
	}
}

func TestImportPreservesPinAndFTS(t *testing.T) {
	p := filepath.Join(t.TempDir(), "t.db")
	r := mkRow("recall_3333333333333333", "mcp__github__list_issues", "Issue", "The quick brown fox jumps over the lazy dog")
	r.Pinned = 1
	importItems(p, []importRow{r}, false, "current_project_key")
	if got := query(t, p, "SELECT pinned FROM stored_outputs WHERE id = ?", r.ID); got[0][0].(int64) != 1 {
		t.Errorf("pin not preserved: %v", got[0][0])
	}
	fts := query(t, p, `SELECT o.id FROM stored_outputs o JOIN outputs_fts f ON f.rowid=o.rowid WHERE outputs_fts MATCH ?`, `"fox"`)
	if len(fts) == 0 {
		t.Error("content not searchable after import")
	}
}

func TestImportSkipAndOverwrite(t *testing.T) {
	p := filepath.Join(t.TempDir(), "t.db")
	r := mkRow("recall_4444444444444444", "mcp__x", "Issue #1", "original content")
	importItems(p, []importRow{r}, false, "current_project_key")

	// skip existing by default (mutated summary should NOT apply)
	r.Summary = "UPDATED"
	res := importItems(p, []importRow{r}, false, "current_project_key")
	if res.skipped != 1 {
		t.Errorf("expected skip, got %+v", res)
	}
	if got := query(t, p, "SELECT summary FROM stored_outputs WHERE id = ?", r.ID); got[0][0] != "Issue #1" {
		t.Errorf("skip failed, summary = %v", got[0][0])
	}

	// overwrite replaces + refreshes chunks (no stale "original")
	r.Summary = "OVERWRITTEN"
	r.FullContent = "replacement content"
	res2 := importItems(p, []importRow{r}, true, "current_project_key")
	if res2.overwritten != 1 {
		t.Errorf("expected overwrite, got %+v", res2)
	}
	if got := query(t, p, "SELECT summary FROM stored_outputs WHERE id = ?", r.ID); got[0][0] != "OVERWRITTEN" {
		t.Errorf("overwrite failed: %v", got[0][0])
	}
	chunks := query(t, p, "SELECT content FROM content_chunks WHERE output_id = ?", r.ID)
	if len(chunks) == 0 {
		t.Fatal("no chunks after overwrite")
	}
	for _, c := range chunks {
		if strings.Contains(c[0].(string), "original") {
			t.Error("stale chunk content survived overwrite")
		}
	}
}

func TestImportDryRunCounts(t *testing.T) {
	p := filepath.Join(t.TempDir(), "t.db")
	items := []importRow{mkRow("recall_5555555555555555", "mcp__x", "s1", "c1"), mkRow("recall_6666666666666666", "mcp__y", "s2", "c2")}
	// non-existent DB path → all imported
	if r := dryRunCount(filepath.Join(t.TempDir(), "missing.db"), items, false); r.imported != 2 {
		t.Errorf("dry-run on missing db: %+v", r)
	}
	// after a real import, dry-run counts skips
	importItems(p, items, false, "current_project_key")
	if r := dryRunCount(p, items, false); r.skipped != 2 {
		t.Errorf("dry-run skip count: %+v", r)
	}
}

// CLI validation (exit codes) via the built binary, mirroring the original's subprocess tests.
func TestImportCLIValidation(t *testing.T) {
	bin, _ := filepath.Abs("../../mcprecall")
	if _, err := os.Stat(bin); err != nil {
		t.Skip("binary not built")
	}
	run := func(content string) (int, string) {
		f := filepath.Join(t.TempDir(), "dump.json")
		os.WriteFile(f, []byte(content), 0o644)
		cmd := exec.Command(bin, "import", f)
		cmd.Env = append(os.Environ(), "RECALL_DB_PATH="+filepath.Join(t.TempDir(), "x.db"))
		out, _ := cmd.CombinedOutput()
		return cmd.ProcessState.ExitCode(), string(out)
	}
	if code, out := run("not json at all"); code != 1 || !strings.Contains(out, "Invalid JSON") {
		t.Errorf("invalid json: code=%d out=%q", code, out)
	}
	if code, out := run(`[{"foo":"bar"}]`); code != 1 || !strings.Contains(out, "recall__export") {
		t.Errorf("bad schema: code=%d out=%q", code, out)
	}
	if code, out := run("[]"); code != 0 || !strings.Contains(out, "Nothing to import") {
		t.Errorf("empty export should be graceful: code=%d out=%q", code, out)
	}
}

// Upstream's schema bounds full_retained to 0|1; any other integer would be
// stored as-is and read as "retained" by every != 0 check.
func TestValidateFullRetainedRange(t *testing.T) {
	for _, v := range []int{-1, 2} {
		r := mkRow("recall_a", "Bash", "[s]", "")
		r.FullRetained = &v
		if issues := r.validate(0); len(issues) != 1 || !strings.Contains(issues[0], "full_retained") {
			t.Errorf("full_retained=%d: issues = %v", v, issues)
		}
	}
	for _, v := range []int{0, 1} {
		r := mkRow("recall_a", "Bash", "[s]", "")
		r.FullRetained = &v
		if issues := r.validate(0); len(issues) != 0 {
			t.Errorf("full_retained=%d should be valid: %v", v, issues)
		}
	}
	if issues := mkRow("recall_a", "Bash", "[s]", "").validate(0); len(issues) != 0 {
		t.Errorf("missing full_retained should be valid: %v", issues)
	}
}

// A tampered dump can pair full_retained=0 with a body; import must drop it to
// match StoreOutput, or effective-size accounting under-counts the row.
func TestImportDropsBodyOfSummaryOnlyRow(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "t.db")
	r := mkRow("recall_deadbeefcafe0001", "Bash", "short summary", strings.Repeat("x", 10000))
	zero := 0
	r.FullRetained = &zero
	if res := importItems(dbPath, []importRow{r}, false, "k"); res.imported != 1 {
		t.Fatalf("imported = %d", res.imported)
	}
	rows := query(t, dbPath, "SELECT full_content, full_retained FROM stored_outputs")
	if len(rows) != 1 || rows[0][0] != "" || rows[0][1] != int64(0) {
		t.Errorf("want 1 row with empty body and full_retained 0, got %d rows", len(rows))
	}
	if n := query(t, dbPath, "SELECT COUNT(*) FROM outputs_fts WHERE outputs_fts MATCH 'xxxxxxxxxx*'"); n[0][0] != int64(0) {
		t.Errorf("dropped body still indexed: %v", n)
	}
}

// command_fp round-trips through export → import, not reset to unknown.
func TestImportRoundTripsCommandFP(t *testing.T) {
	src, _ := db.Open(":memory:")
	defer src.Close()
	fp := "git diff"
	db.StoreOutput(src, db.StoreInput{ProjectKey: srcProject, SessionID: "s", ToolName: "Bash",
		Summary: "d", FullContent: "body", OriginalSize: 100, CommandFP: &fp})
	dump := tools.Export(src, srcProject)

	var rows []importRow
	if err := json.Unmarshal([]byte(dump), &rows); err != nil {
		t.Fatalf("export is not importable JSON: %v", err)
	}
	dbPath := filepath.Join(t.TempDir(), "t.db")
	if res := importItems(dbPath, rows, false, "k"); res.imported != 1 {
		t.Fatalf("imported = %d", res.imported)
	}
	got := query(t, dbPath, "SELECT command_fp FROM stored_outputs WHERE tool_name = 'Bash'")
	if len(got) != 1 || got[0][0] != "git diff" {
		t.Errorf("command_fp after import = %v", got)
	}
}

// delivered_size round-trips through export → import (upstream #319).
func TestImportRoundTripsDeliveredSize(t *testing.T) {
	src, _ := db.Open(":memory:")
	defer src.Close()
	delivered := int64(140)
	db.StoreOutput(src, db.StoreInput{ProjectKey: srcProject, SessionID: "s", ToolName: "Bash",
		Summary: "d", FullContent: "body", OriginalSize: 100, DeliveredSize: &delivered})
	var rows []importRow
	if err := json.Unmarshal([]byte(tools.Export(src, srcProject)), &rows); err != nil {
		t.Fatalf("export is not importable JSON: %v", err)
	}
	dbPath := filepath.Join(t.TempDir(), "t.db")
	if res := importItems(dbPath, rows, false, "k"); res.imported != 1 {
		t.Fatalf("imported = %d", res.imported)
	}
	got := query(t, dbPath, "SELECT delivered_size FROM stored_outputs WHERE tool_name = 'Bash'")
	if len(got) != 1 || got[0][0] != int64(140) {
		t.Errorf("delivered_size after import = %v", got)
	}
}

// ── secret scan (upstream #273) ─────────────────────────────────────────────

const (
	awsKeySample = "AKIAIOSFODNN7EXAMPLE" // AWS's documented non-secret sample
	ghPATSample  = "ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
)

func dumpRow(id string, mod func(*importRow)) importRow {
	r := importRow{ID: id, ProjectKey: srcProject, SessionID: "sess-abc", ToolName: "mcp__github__list_issues",
		Summary: "a clean summary", FullContent: "a clean body", OriginalSize: 12, SummarySize: 15, CreatedAt: 1_700_000_000}
	if mod != nil {
		mod(&r)
	}
	return r
}

// runImport writes rows as a dump, runs HandleImport against a fresh DB, and
// returns combined stdout+stderr plus the DB path.
func runImport(t *testing.T, rows []importRow, extra ...string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	dump := filepath.Join(dir, "dump.json")
	b, _ := json.Marshal(rows)
	os.WriteFile(dump, b, 0o644)
	dbPath := filepath.Join(dir, "target.db")
	t.Setenv("RECALL_DB_PATH", dbPath)

	r, w, _ := os.Pipe()
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = w, w
	HandleImport(append([]string{dump}, extra...))
	w.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	out, _ := io.ReadAll(r)
	return string(out), dbPath
}

func ids(t *testing.T, dbPath string) []string {
	var out []string
	for _, row := range query(t, dbPath, "SELECT id FROM stored_outputs ORDER BY id") {
		out = append(out, row[0].(string))
	}
	return out
}

func TestImportWithholdsSecretRows(t *testing.T) {
	_, dbPath := runImport(t, []importRow{
		dumpRow("clean_row", func(r *importRow) { r.FullContent = "nothing sensitive here" }),
		dumpRow("dirty_row", func(r *importRow) { r.FullContent = "export AWS_ACCESS_KEY_ID=" + awsKeySample }),
	})
	if got := ids(t, dbPath); len(got) != 1 || got[0] != "clean_row" {
		t.Errorf("stored = %v, want [clean_row]", got)
	}
}

// A summary-only row can only carry the secret in its summary; still withheld.
func TestImportWithholdsSecretInSummary(t *testing.T) {
	zero := 0
	_, dbPath := runImport(t, []importRow{dumpRow("dirty_summary", func(r *importRow) {
		r.Summary, r.FullContent, r.FullRetained = "token "+ghPATSample, "", &zero
	})})
	if got := ids(t, dbPath); len(got) != 0 {
		t.Errorf("stored = %v, want none", got)
	}
}

func TestImportReportsWithheldNotValues(t *testing.T) {
	out, _ := runImport(t, []importRow{
		dumpRow("clean_row", nil),
		dumpRow("dirty_aws", func(r *importRow) { r.FullContent = "key=" + awsKeySample }),
		dumpRow("dirty_gh", func(r *importRow) { r.FullContent = "token=" + ghPATSample }),
	})
	for _, want := range []string{"Withheld 2 row(s)", "AWS access key ID", "GitHub PAT (classic)", "1 imported"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, awsKeySample) || strings.Contains(out, ghPATSample) {
		t.Errorf("output echoes a secret value:\n%s", out)
	}
}

func TestImportDryRunPredictsWithheld(t *testing.T) {
	out, dbPath := runImport(t, []importRow{
		dumpRow("clean_row", nil),
		dumpRow("dirty_aws", func(r *importRow) { r.FullContent = "key=" + awsKeySample }),
	}, "--dry-run")
	for _, want := range []string{"Would withhold 1 row(s)", "AWS access key ID", "1 imported"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, awsKeySample) {
		t.Error("dry run echoes the secret")
	}
	if _, err := os.Stat(dbPath); err == nil {
		t.Error("dry run created the database")
	}
}

func TestImportAllWithheldLeavesStoreEmpty(t *testing.T) {
	out, dbPath := runImport(t, []importRow{
		dumpRow("dirty_a", func(r *importRow) { r.FullContent = "key=" + awsKeySample }),
		dumpRow("dirty_b", func(r *importRow) { r.FullContent = "token=" + ghPATSample }),
	})
	if !strings.Contains(out, "Withheld 2 row(s)") || !strings.Contains(out, "Nothing imported.") {
		t.Errorf("output:\n%s", out)
	}
	if got := ids(t, dbPath); len(got) != 0 {
		t.Errorf("stored = %v", got)
	}
}

func TestImportCleanDumpReportsNoWithheld(t *testing.T) {
	out, _ := runImport(t, []importRow{dumpRow("clean_row", nil)})
	if strings.Contains(out, "containing secrets") || !strings.Contains(out, "1 imported") {
		t.Errorf("output:\n%s", out)
	}
}
