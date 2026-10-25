// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/gc/wal_test.go
// Covers WAL truncation after a reclaim (upstream #292) and gc --vacuum
// migrating a legacy FTS index (upstream #263).

package gc

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcprecall/internal/db"
)

// A session's MCP server holds its store open, so the WAL is never deleted on
// close; without a TRUNCATE checkpoint the reclaimed pages sit in a WAL as
// large as the database and the disk is not freed.

func walSize(file string) int64 {
	fi, err := os.Stat(file + "-wal")
	if err != nil {
		return 0
	}
	return fi.Size()
}

func rawConn(t *testing.T, file string) *sql.DB {
	t.Helper()
	conn, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1)
	conn.Exec("PRAGMA busy_timeout=5000")
	t.Cleanup(func() { conn.Close() })
	return conn
}

func bloatedWALStore(t *testing.T, file string) *sql.DB {
	t.Helper()
	conn := rawConn(t, file)
	for _, p := range []string{"PRAGMA auto_vacuum=INCREMENTAL", "PRAGMA journal_mode=WAL"} {
		if _, err := conn.Exec(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.InitSchema(conn); err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("w", 4096)
	for i := 0; i < 120; i++ {
		if _, err := db.StoreOutput(conn, db.StoreInput{
			ProjectKey: "p", SessionID: "2026-01-01", ToolName: "t", Summary: "s",
			FullContent: big, OriginalSize: len(big),
		}); err != nil {
			t.Fatal(err)
		}
	}
	return conn
}

func TestVacuumFileTruncatesWALWithReaderOpen(t *testing.T) {
	file := filepath.Join(t.TempDir(), "store.db")
	conn := bloatedWALStore(t, file)
	conn.Exec("DELETE FROM stored_outputs")
	conn.Exec("PRAGMA wal_checkpoint(TRUNCATE)") // start from an empty WAL
	server := rawConn(t, file)                   // stands in for a session's MCP server
	var n int
	server.QueryRow("SELECT count(*) FROM stored_outputs").Scan(&n)

	result := VacuumFile(file)
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if got := walSize(file); got != 0 {
		t.Errorf("WAL is %d bytes after vacuum", got)
	}
	if result.After >= result.Before {
		t.Errorf("no reclaim reported: before=%d after=%d", result.Before, result.After)
	}
}

func TestReclaimPagesTruncatesWALWithReaderOpen(t *testing.T) {
	file := filepath.Join(t.TempDir(), "store.db")
	conn := bloatedWALStore(t, file)
	conn.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	server := rawConn(t, file)
	var n int
	server.QueryRow("SELECT count(*) FROM stored_outputs").Scan(&n)

	deleted, err := db.ForgetOutputs(conn, "p", db.ForgetOptions{All: true, Force: true})
	if err != nil || deleted != 120 {
		t.Fatalf("forget = %d, %v", deleted, err)
	}
	if got := walSize(file); got != 0 {
		t.Errorf("WAL is %d bytes after reclaim", got)
	}
}

func TestVacuumFileMigratesLegacyFTSAndShrinks(t *testing.T) {
	file := filepath.Join(t.TempDir(), "legacy.db")
	conn := rawConn(t, file)
	conn.Exec("PRAGMA auto_vacuum=NONE")
	if err := db.InitSchema(conn); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		"DROP TRIGGER outputs_ad",
		"DROP TABLE outputs_fts",
		"CREATE VIRTUAL TABLE outputs_fts USING fts5(id UNINDEXED, tool_name, summary, full_content)",
		"CREATE TRIGGER outputs_ad AFTER DELETE ON stored_outputs BEGIN DELETE FROM outputs_fts WHERE rowid = old.rowid; END",
	} {
		if _, err := conn.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	body := strings.Repeat("lorem ipsum dolor sit amet ", 400)
	for i := 0; i < 200; i++ {
		if _, err := db.StoreOutput(conn, db.StoreInput{
			ProjectKey: "p", SessionID: "2026-10-05", ToolName: "t", Summary: "s",
			FullContent: fmt.Sprintf("%s item%d", body, i), OriginalSize: 1000,
		}); err != nil {
			t.Fatal(err)
		}
	}
	conn.Close()
	fi, _ := os.Stat(file)
	before := fi.Size()

	if result := VacuumFile(file); result.Err != nil {
		t.Fatal(result.Err)
	}

	after := rawConn(t, file)
	var one int
	if after.QueryRow("SELECT 1 FROM sqlite_master WHERE name = 'outputs_fts_content'").Scan(&one) == nil {
		t.Error("legacy content shadow table survived gc --vacuum")
	}
	if _, err := after.Exec("INSERT INTO outputs_fts(outputs_fts, rank) VALUES('integrity-check', 1)"); err != nil {
		t.Errorf("integrity-check after vacuum: %v", err)
	}
	var c int
	after.QueryRow("SELECT count(*) FROM outputs_fts WHERE outputs_fts MATCH 'item7'").Scan(&c)
	if c != 1 {
		t.Errorf("item7 matches = %d, want 1", c)
	}
	fi, _ = os.Stat(file)
	if float64(fi.Size()) >= float64(before)*0.8 {
		t.Errorf("store did not shrink: before=%d after=%d", before, fi.Size())
	}
}
