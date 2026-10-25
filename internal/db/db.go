// Package db is the SQLite persistence layer for mcp-recall: schema,
// migrations, CRUD queries, and analytics. Uses pure-Go modernc SQLite with
// FTS5. Ports src/db/*.ts.
package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

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

  CREATE VIRTUAL TABLE IF NOT EXISTS outputs_fts USING fts5(
    id UNINDEXED,
    tool_name,
    summary,
    full_content
  );

  CREATE TRIGGER IF NOT EXISTS outputs_ai AFTER INSERT ON stored_outputs BEGIN
    INSERT INTO outputs_fts(rowid, id, tool_name, summary, full_content)
    VALUES (new.rowid, new.id, new.tool_name, new.summary, new.full_content);
  END;

  CREATE TRIGGER IF NOT EXISTS outputs_ad AFTER DELETE ON stored_outputs BEGIN
    DELETE FROM outputs_fts WHERE rowid = old.rowid;
  END;

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
	return nil
}
