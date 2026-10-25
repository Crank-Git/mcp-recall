// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/db/fts_test.go
// Covers the external-content FTS index and its one-time migration (upstream
// #263) and WAL truncation after a reclaim (upstream #292).

package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

const ftsProject = "fts_migration_test"

// convertToLegacyFTS rewrites a current store into the pre-#263 schema: a
// self-contained FTS table and a plain DELETE trigger.
func convertToLegacyFTS(t *testing.T, database *sql.DB) {
	t.Helper()
	for _, stmt := range []string{
		"DROP TRIGGER outputs_ad",
		"DROP TABLE outputs_fts",
		"CREATE VIRTUAL TABLE outputs_fts USING fts5(id UNINDEXED, tool_name, summary, full_content)",
		`CREATE TRIGGER outputs_ad AFTER DELETE ON stored_outputs BEGIN
		  DELETE FROM outputs_fts WHERE rowid = old.rowid;
		END`,
	} {
		if _, err := database.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
}

func ftsInput(summary, body string) StoreInput {
	return StoreInput{
		ProjectKey: ftsProject, SessionID: "2026-10-05", ToolName: "mcp__github__list_issues",
		Summary: summary, FullContent: body, OriginalSize: 1000,
	}
}

func rawOpen(t *testing.T, path string) *sql.DB {
	t.Helper()
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	if _, err := database.Exec("PRAGMA busy_timeout=5000"); err != nil {
		t.Fatal(err)
	}
	return database
}

// buildLegacyStore writes a legacy store on disk with full-body, summary-only,
// and deleted rows (rowid gaps).
func buildLegacyStore(t *testing.T, path string) {
	t.Helper()
	database := rawOpen(t, path)
	defer database.Close()
	if err := InitSchema(database); err != nil {
		t.Fatal(err)
	}
	convertToLegacyFTS(t, database)
	for i := 0; i < 30; i++ {
		mustStore(t, database, ftsInput(fmt.Sprintf("alpha summary %d", i),
			fmt.Sprintf("alpha body number%d kubernetes pod crashloop", i)))
	}
	zero := 0
	in := ftsInput("summaryonly zebra row", "never persisted")
	in.FullRetained = &zero
	mustStore(t, database, in)
	if _, err := database.Exec("DELETE FROM stored_outputs WHERE rowid % 4 = 0"); err != nil {
		t.Fatal(err)
	}
}

func shadowTables(t *testing.T, database *sql.DB) []string {
	t.Helper()
	rows, err := database.Query("SELECT name FROM sqlite_master WHERE name LIKE 'outputs_fts_%' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		rows.Scan(&n)
		names = append(names, n)
	}
	return names
}

func hasShadow(t *testing.T, database *sql.DB, name string) bool {
	for _, n := range shadowTables(t, database) {
		if n == name {
			return true
		}
	}
	return false
}

func ftsSnippets(t *testing.T, database *sql.DB, q string) []string {
	t.Helper()
	rows, err := database.Query("SELECT rowid, snippet(outputs_fts, 3, '', '', ' [...] ', 64) FROM outputs_fts WHERE outputs_fts MATCH ? ORDER BY rowid", q)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id int64
		var s string
		rows.Scan(&id, &s)
		out = append(out, fmt.Sprintf("%d:%s", id, s))
	}
	return out
}

func integrityPasses(database *sql.DB) bool {
	_, err := database.Exec("INSERT INTO outputs_fts(outputs_fts, rank) VALUES('integrity-check', 1)")
	return err == nil
}

func searchIDs(database *sql.DB, q string, limit int) []string {
	var ids []string
	for _, r := range SearchOutputs(database, q, SearchOptions{ProjectKey: ftsProject, Limit: limit}) {
		ids = append(ids, r.ID)
	}
	return ids
}

func TestFTSNewStoreIsExternalContent(t *testing.T) {
	database := newDB(t)
	if hasShadow(t, database, "outputs_fts_content") {
		t.Fatal("new store has a content shadow table")
	}
	var sqlText string
	database.QueryRow("SELECT sql FROM sqlite_master WHERE name = 'outputs_fts'").Scan(&sqlText)
	if !strings.Contains(sqlText, "content='stored_outputs'") {
		t.Fatalf("outputs_fts DDL = %q", sqlText)
	}
	if got := MigrateFTSToExternalContent(database); got != FTSCurrent {
		t.Fatalf("migrate on new store = %q", got)
	}
}

func TestFTSLegacyStoreMigratesOnOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	buildLegacyStore(t, path)

	legacy := rawOpen(t, path)
	if !hasShadow(t, legacy, "outputs_fts_content") {
		t.Fatal("legacy store lacks outputs_fts_content")
	}
	beforeSearch := searchIDs(legacy, "kubernetes", 100)
	beforeSnippets := ftsSnippets(t, legacy, "crashloop")
	beforeZebra := searchIDs(legacy, "zebra", 10)
	legacy.Close()
	if len(beforeSearch) <= 10 || len(beforeZebra) != 1 {
		t.Fatalf("legacy setup: %d kubernetes, %d zebra", len(beforeSearch), len(beforeZebra))
	}

	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if hasShadow(t, database, "outputs_fts_content") {
		t.Fatal("migration left the content shadow table")
	}
	if !integrityPasses(database) {
		t.Fatal("integrity-check failed after migration")
	}
	if got := searchIDs(database, "kubernetes", 100); !reflect.DeepEqual(got, beforeSearch) {
		t.Errorf("search changed:\n got %v\nwant %v", got, beforeSearch)
	}
	if got := ftsSnippets(t, database, "crashloop"); !reflect.DeepEqual(got, beforeSnippets) {
		t.Errorf("snippets changed:\n got %v\nwant %v", got, beforeSnippets)
	}
	// Summary-only rows (store.retention) stay findable by summary.
	if got := searchIDs(database, "zebra", 10); !reflect.DeepEqual(got, beforeZebra) {
		t.Errorf("zebra = %v, want %v", got, beforeZebra)
	}
}

func TestFTSFailedMigrationRollsBack(t *testing.T) {
	// Force a failure after DROP TABLE: rebuild reads stored_outputs.summary,
	// which this synthetic store lacks, while its legacy triggers work without it.
	database := rawOpen(t, filepath.Join(t.TempDir(), "broken.db"))
	defer database.Close()
	for _, stmt := range []string{
		"CREATE TABLE stored_outputs (id TEXT PRIMARY KEY, tool_name TEXT, title TEXT, full_content TEXT)",
		"CREATE VIRTUAL TABLE outputs_fts USING fts5(id UNINDEXED, tool_name, summary, full_content)",
		`CREATE TRIGGER outputs_ai AFTER INSERT ON stored_outputs BEGIN
		  INSERT INTO outputs_fts(rowid, id, tool_name, summary, full_content)
		  VALUES (new.rowid, new.id, new.tool_name, new.title, new.full_content);
		END`,
		`CREATE TRIGGER outputs_ad AFTER DELETE ON stored_outputs BEGIN
		  DELETE FROM outputs_fts WHERE rowid = old.rowid;
		END`,
		"INSERT INTO stored_outputs VALUES ('a', 't', 'title', 'walrus content')",
	} {
		if _, err := database.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}

	if got := MigrateFTSToExternalContent(database); got != FTSFailed {
		t.Fatalf("migrate = %q, want failed", got)
	}
	if !hasShadow(t, database, "outputs_fts_content") {
		t.Fatal("rollback lost the legacy index")
	}
	var id string
	if err := database.QueryRow("SELECT id FROM outputs_fts WHERE outputs_fts MATCH 'walrus'").Scan(&id); err != nil || id != "a" {
		t.Fatalf("legacy index unusable after rollback: id=%q err=%v", id, err)
	}
	var trigger string
	database.QueryRow("SELECT sql FROM sqlite_master WHERE name = 'outputs_ad'").Scan(&trigger)
	if !strings.Contains(trigger, "DELETE FROM outputs_fts") {
		t.Fatalf("trigger not restored: %q", trigger)
	}
}

func TestFTSMigrationTruncatesWALWithReaderOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal.db")
	setup := rawOpen(t, path)
	defer setup.Close()
	for _, p := range []string{"PRAGMA auto_vacuum=INCREMENTAL", "PRAGMA journal_mode=WAL"} {
		if _, err := setup.Exec(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := InitSchema(setup); err != nil {
		t.Fatal(err)
	}
	convertToLegacyFTS(t, setup)
	body := strings.Repeat("lorem ipsum dolor sit amet ", 400)
	for i := 0; i < 150; i++ {
		mustStore(t, setup, ftsInput("issue list summary", fmt.Sprintf("%s item%d", body, i)))
	}
	setup.Exec("PRAGMA wal_checkpoint(TRUNCATE)")

	server := rawOpen(t, path) // stands in for a session's MCP server
	defer server.Close()
	var n int
	server.QueryRow("SELECT count(*) FROM stored_outputs").Scan(&n)

	if got := MigrateFTSToExternalContent(setup); got != FTSMigrated {
		t.Fatalf("migrate = %q", got)
	}
	if fi, err := os.Stat(path + "-wal"); err == nil && fi.Size() != 0 {
		t.Fatalf("WAL is %d bytes after migration", fi.Size())
	}
}

func TestFTSDeletesStayConsistentAfterMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "del.db")
	buildLegacyStore(t, path)
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	victims := searchIDs(database, "number5", 10)
	if len(victims) == 0 {
		t.Fatal("no number5 row")
	}
	if n, _ := ForgetOutputs(database, ftsProject, ForgetOptions{ID: victims[0]}); n != 1 {
		t.Fatalf("forget = %d", n)
	}
	if got := searchIDs(database, "number5", 10); len(got) != 0 {
		t.Fatalf("deleted row still indexed: %v", got)
	}
	ForgetOutputs(database, ftsProject, ForgetOptions{All: true, Force: true})
	var c int
	database.QueryRow("SELECT count(*) FROM stored_outputs").Scan(&c)
	if c != 0 {
		t.Fatalf("%d rows left", c)
	}
	if !integrityPasses(database) {
		t.Fatal("integrity-check failed after deletes")
	}
	if got := searchIDs(database, "alpha", 10); len(got) != 0 {
		t.Fatalf("alpha still indexed: %v", got)
	}
}

func TestVerifyFTSIndexRebuildsDesyncedIndex(t *testing.T) {
	database := newDB(t)
	mustStore(t, database, ftsInput("s", "ocelot one"))
	mustStore(t, database, ftsInput("s", "ocelot two"))
	if VerifyFTSIndex(database) {
		t.Fatal("healthy index reported a rebuild")
	}
	// Desync: delete a row without telling the index.
	database.Exec("DROP TRIGGER outputs_ad")
	database.Exec("DELETE FROM stored_outputs WHERE rowid = 1")
	if integrityPasses(database) {
		t.Fatal("desync not detected by integrity-check")
	}
	if !VerifyFTSIndex(database) {
		t.Fatal("desynced index not rebuilt")
	}
	if !integrityPasses(database) {
		t.Fatal("integrity-check fails after rebuild")
	}
}

// TestNoWriterChangesIndexedColumn guards the external-content index: there is
// no AFTER UPDATE trigger, so an UPDATE of an indexed column, or a REPLACE
// (whose implicit delete skips triggers), would silently desync outputs_fts.
func TestNoWriterChangesIndexedColumn(t *testing.T) {
	root := filepath.Join("..", "..")
	updateRe := regexp.MustCompile(`(?is)UPDATE\s+stored_outputs\s+SET\s+(.*?)\s+WHERE`)
	indexedRe := regexp.MustCompile(`\b(id|tool_name|summary|full_content)\s*=`)
	replaceRe := regexp.MustCompile(`(?i)REPLACE\s+INTO\s+stored_outputs`)
	updates := 0
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		b, _ := os.ReadFile(p)
		for _, m := range updateRe.FindAllStringSubmatch(string(b), -1) {
			updates++
			if indexedRe.MatchString(m[1]) {
				t.Errorf("%s updates an indexed column: %q", p, m[1])
			}
		}
		if replaceRe.Match(b) {
			t.Errorf("%s uses REPLACE INTO stored_outputs", p)
		}
		return nil
	})
	if updates == 0 {
		t.Fatal("found no UPDATE stored_outputs statements; the scan is broken")
	}
}

// A new store must get incremental auto-vacuum: set after the WAL switch it
// silently stays NONE, and no reclaim ever shrinks the file.
func TestOpenNewStoreUsesIncrementalAutoVacuum(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "new.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var mode int
	if err := database.QueryRow("PRAGMA auto_vacuum").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != 2 {
		t.Fatalf("auto_vacuum = %d, want 2 (INCREMENTAL)", mode)
	}
}
