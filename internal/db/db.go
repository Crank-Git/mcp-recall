// Package db is the SQLite persistence layer for mcp-recall: schema,
// migrations, CRUD queries, and analytics. Uses pure-Go modernc SQLite with
// FTS5. Ports src/db/*.ts.
package db

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"

	"mcprecall/internal/logx"
	_ "modernc.org/sqlite"
)

// External-content FTS5 (upstream #263): the index reads text through from
// stored_outputs by rowid instead of keeping its own verbatim copy (formerly
// ~34% of store bytes). Not contentless (an empty content= option), which would break
// snippet() in SearchOutputs. Reading through by rowid makes stored_outputs'
// implicit rowid load-bearing; see VerifyFTSIndex for the guard after VACUUM.
const ftsTableDDL = `
  CREATE VIRTUAL TABLE IF NOT EXISTS outputs_fts USING fts5(
    id UNINDEXED,
    tool_name,
    summary,
    full_content,
    content='stored_outputs',
    content_rowid='rowid'
  )`

// An external-content index must be told the deleted row's old values; a plain
// DELETE would look them up in stored_outputs, where the row is already gone.
const ftsDeleteTriggerDDL = `
  CREATE TRIGGER IF NOT EXISTS outputs_ad AFTER DELETE ON stored_outputs BEGIN
    INSERT INTO outputs_fts(outputs_fts, rowid, id, tool_name, summary, full_content)
    VALUES ('delete', old.rowid, old.id, old.tool_name, old.summary, old.full_content);
  END`

const schema = `
  CREATE TABLE IF NOT EXISTS stored_outputs (
    id TEXT PRIMARY KEY,
    project_key TEXT NOT NULL,
    session_id TEXT NOT NULL,
    tool_name TEXT NOT NULL,
    summary TEXT NOT NULL,
    full_content TEXT NOT NULL,
    original_size INTEGER NOT NULL,
    summary_size INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    pinned INTEGER NOT NULL DEFAULT 0,
    access_count INTEGER NOT NULL DEFAULT 0,
    last_accessed INTEGER,
    input_hash TEXT
  );

  CREATE INDEX IF NOT EXISTS idx_so_project_key ON stored_outputs(project_key);
  CREATE INDEX IF NOT EXISTS idx_so_created_at  ON stored_outputs(created_at);
  CREATE INDEX IF NOT EXISTS idx_so_tool_name   ON stored_outputs(tool_name);
  CREATE INDEX IF NOT EXISTS idx_so_input_hash  ON stored_outputs(project_key, input_hash);

  ` + ftsTableDDL + `;

  CREATE TRIGGER IF NOT EXISTS outputs_ai AFTER INSERT ON stored_outputs BEGIN
    INSERT INTO outputs_fts(rowid, id, tool_name, summary, full_content)
    VALUES (new.rowid, new.id, new.tool_name, new.summary, new.full_content);
  END;

  ` + ftsDeleteTriggerDDL + `;

  CREATE VIRTUAL TABLE IF NOT EXISTS content_chunks USING fts5(
    output_id UNINDEXED,
    chunk_index UNINDEXED,
    content
  );

  CREATE TRIGGER IF NOT EXISTS outputs_ad_chunks AFTER DELETE ON stored_outputs BEGIN
    DELETE FROM content_chunks WHERE output_id = old.id;
  END;

  CREATE TABLE IF NOT EXISTS sessions (
    date TEXT PRIMARY KEY
  );

  CREATE TABLE IF NOT EXISTS meta (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
  );
`

// migrations are columns added after the initial schema — applied once,
// idempotent via duplicate-column error suppression.
var migrations = []string{
	"ALTER TABLE stored_outputs ADD COLUMN pinned INTEGER NOT NULL DEFAULT 0",
	"ALTER TABLE stored_outputs ADD COLUMN access_count INTEGER NOT NULL DEFAULT 0",
	"ALTER TABLE stored_outputs ADD COLUMN last_accessed INTEGER",
	"ALTER TABLE stored_outputs ADD COLUMN input_hash TEXT",
	"ALTER TABLE stored_outputs ADD COLUMN output_hash TEXT",
	"CREATE INDEX IF NOT EXISTS idx_so_output_hash ON stored_outputs(project_key, output_hash)",
	// Whether the verbatim body is persisted (1) or the row is summary-only (0).
	// Existing rows all have bodies, so default 1. See store.retention.
	"ALTER TABLE stored_outputs ADD COLUMN full_retained INTEGER NOT NULL DEFAULT 1",
	// Privacy-safe command family fingerprint for per-command savings
	// attribution (upstream #251); NULL for non-Bash rows and rows written before
	// this migration (reported as "unknown"). See handlers.CommandFingerprint.
	"ALTER TABLE stored_outputs ADD COLUMN command_fp TEXT",
	"CREATE INDEX IF NOT EXISTS idx_so_command_fp ON stored_outputs(project_key, command_fp)",
	// Bytes Claude received: header, newline, summary (upstream #319). NULL for
	// notes and for rows written before this migration; savings figures then
	// fall back to summary_size. summary_size stays the stored size of a
	// summary-only row.
	"ALTER TABLE stored_outputs ADD COLUMN delivered_size INTEGER",
}

// DefaultDBPath returns the SQLite path for a project. Respects RECALL_DB_PATH;
// otherwise ~/.local/share/mcp-recall/<projectKey>.db.
func DefaultDBPath(projectKey string) string {
	if p := os.Getenv("RECALL_DB_PATH"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "mcp-recall", projectKey+".db")
}

// DataDir returns the directory holding per-project databases. When
// RECALL_DB_PATH overrides to a single file, returns its parent directory so
// callers that scan the store (e.g. gc) operate on the right location.
func DataDir() string {
	if p := os.Getenv("RECALL_DB_PATH"); p != "" {
		return filepath.Dir(p)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "mcp-recall")
}

// Open opens (creating if needed) the SQLite database at path, applies pragmas,
// schema, and migrations. Use ":memory:" for tests. The connection pool is
// pinned to a single connection so that pragmas and (for :memory:) the database
// itself persist for the process lifetime — matching the original's single
// bun:sqlite handle.
func Open(path string) (*sql.DB, error) {
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
	}
	database, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	database.SetMaxOpenConns(1)

	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
		"PRAGMA auto_vacuum=INCREMENTAL",
	}
	for _, p := range pragmas {
		if _, err := database.Exec(p); err != nil {
			database.Close()
			return nil, err
		}
	}

	if err := InitSchema(database); err != nil {
		database.Close()
		return nil, err
	}
	return database, nil
}

// InitSchema applies the full schema and migrations to a connection. All DDL is
// idempotent (IF NOT EXISTS / duplicate-column guards).
func InitSchema(database *sql.DB) error {
	if _, err := database.Exec(schema); err != nil {
		return err
	}
	for _, m := range migrations {
		if _, err := database.Exec(m); err != nil {
			if !strings.Contains(err.Error(), "duplicate column") {
				return err
			}
		}
	}
	MigrateFTSToExternalContent(database)
	return nil
}

// TruncateWAL folds the WAL into the database and truncates it to zero bytes.
// Call after any page reclaim: stores run in WAL mode, a WAL is reused rather
// than shrunk, and it is deleted only when the last connection closes, which a
// session's MCP server never does. Without this, reclaimed pages sit in a WAL
// as large as the database (upstream #292). Works with other connections open;
// a busy reader just leaves the WAL in place until the next call. Never fails.
func TruncateWAL(database *sql.DB) {
	if _, err := database.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		logx.Warn("wal_checkpoint(TRUNCATE) failed — " + err.Error())
	}
}

// hasLegacyFTS reports whether outputs_fts is the pre-#263 form that keeps its
// own copy of the text.
func hasLegacyFTS(q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) bool {
	var one int
	err := q.QueryRowContext(context.Background(),
		"SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'outputs_fts_content'").Scan(&one)
	return err == nil
}

// FTSMigrationResult is the outcome of MigrateFTSToExternalContent.
type FTSMigrationResult string

const (
	FTSMigrated FTSMigrationResult = "migrated"
	FTSCurrent  FTSMigrationResult = "current"
	FTSFailed   FTSMigrationResult = "failed"
)

// MigrateFTSToExternalContent is the one-time conversion of a legacy
// self-contained outputs_fts to external content (upstream #263). Runs in a
// single IMMEDIATE transaction that ends with a content-aware integrity-check,
// so an interruption or a failed check rolls back to the old, working index —
// never a half-built or silently-wrong one. The legacy check is repeated inside
// the transaction because two processes (hook + server) can open the same store
// at once. Never fails: a store that cannot migrate keeps working on the old
// schema and retries on its next open.
func MigrateFTSToExternalContent(database *sql.DB) FTSMigrationResult {
	if !hasLegacyFTS(database) {
		return FTSCurrent
	}
	ctx := context.Background()
	// Pin one connection: BEGIN IMMEDIATE and its statements must share it.
	conn, err := database.Conn(ctx)
	if err != nil {
		logx.Warn("FTS migration failed, keeping the existing index — " + err.Error())
		return FTSFailed
	}
	migrated, err := func() (bool, error) {
		defer conn.Close()
		if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
			return false, err
		}
		if !hasLegacyFTS(conn) {
			_, err := conn.ExecContext(ctx, "COMMIT")
			return false, err
		}
		for _, stmt := range []string{
			"DROP TRIGGER IF EXISTS outputs_ad",
			"DROP TABLE outputs_fts",
			ftsTableDDL,
			ftsDeleteTriggerDDL,
			"INSERT INTO outputs_fts(outputs_fts) VALUES('rebuild')",
			"INSERT INTO outputs_fts(outputs_fts, rank) VALUES('integrity-check', 1)",
			"COMMIT",
		} {
			if _, err := conn.ExecContext(ctx, stmt); err != nil {
				_, _ = conn.ExecContext(ctx, "ROLLBACK")
				return false, err
			}
		}
		return true, nil
	}()
	if err != nil {
		logx.Warn("FTS migration failed, keeping the existing index — " + err.Error())
		return FTSFailed
	}
	if !migrated {
		return FTSCurrent
	}
	// The dropped copy's pages are free but still in the file. A no-op on
	// stores created without auto_vacuum=INCREMENTAL; `gc --vacuum` reclaims those.
	if _, err := database.Exec("PRAGMA incremental_vacuum"); err != nil {
		logx.Warn("incremental_vacuum after FTS migration failed — " + err.Error())
	}
	TruncateWAL(database)
	logx.Debug("FTS index migrated to external content (#263)")
	return FTSMigrated
}

// VerifyFTSIndex checks that outputs_fts agrees with stored_outputs and
// rebuilds it if not. stored_outputs has no INTEGER PRIMARY KEY, and SQLite
// documents that VACUUM may renumber implicit rowids; current SQLite preserves
// them, but an external-content index keyed on rowid would silently return
// wrong rows if one ever did. Call after VACUUM. Returns true when a rebuild
// was needed. Never fails.
func VerifyFTSIndex(database *sql.DB) bool {
	var one int
	if database.QueryRow("SELECT 1 FROM sqlite_master WHERE name = 'outputs_fts'").Scan(&one) != nil {
		return false
	}
	if _, err := database.Exec("INSERT INTO outputs_fts(outputs_fts, rank) VALUES('integrity-check', 1)"); err == nil {
		return false
	}
	if _, err := database.Exec("INSERT INTO outputs_fts(outputs_fts) VALUES('rebuild')"); err != nil {
		logx.Warn("FTS rebuild failed — " + err.Error())
	} else {
		logx.Warn("FTS index disagreed with stored_outputs; rebuilt")
	}
	return true
}
