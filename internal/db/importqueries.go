// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/db/importqueries.go

package db

import "database/sql"

// HasID reports whether a row with the given id exists.
func HasID(database *sql.DB, id string) bool {
	var x string
	return database.QueryRow(`SELECT id FROM stored_outputs WHERE id = ? LIMIT 1`, id).Scan(&x) == nil
}

// DeleteByID removes a row by id (triggers clean up FTS + chunks).
func DeleteByID(database *sql.DB, id string) error {
	_, err := database.Exec(`DELETE FROM stored_outputs WHERE id = ?`, id)
	return err
}

// HasStoredTable reports whether the stored_outputs table exists.
func HasStoredTable(database *sql.DB) bool {
	var name string
	return database.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='stored_outputs' LIMIT 1`).Scan(&name) == nil
}

func nullInt(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

// InsertFull inserts a fully-specified stored output (preserving id, created_at,
// pinned, etc.) plus its content chunks, in one transaction. Used by import.
func InsertFull(database *sql.DB, o StoredOutput) error {
	tx, err := database.Begin()
	if err != nil {
		return err
	}
	_, err = tx.Exec(`
		INSERT INTO stored_outputs
			(id, project_key, session_id, tool_name, summary, full_content,
			 original_size, summary_size, created_at, pinned, access_count,
			 last_accessed, input_hash, full_retained, command_fp, delivered_size)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		o.ID, o.ProjectKey, o.SessionID, o.ToolName, o.Summary, o.FullContent,
		o.OriginalSize, o.SummarySize, o.CreatedAt, o.Pinned, o.AccessCount,
		nullInt(o.LastAccessed), nullString(o.InputHash), o.FullRetained,
		nullString(o.CommandFP), nullInt(o.DeliveredSize),
	)
	if err != nil {
		tx.Rollback()
		return err
	}
	// Summary-only rows have no body, so skip chunking — matches StoreOutput.
	if o.FullRetained != 0 {
		if err := storeChunks(tx, o.ID, o.FullContent); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}
