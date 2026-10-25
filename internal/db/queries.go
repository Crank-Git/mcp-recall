// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/db/queries.go

package db

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"math"
	"sort"
	"strings"
	"time"

	"mcprecall/internal/logx"
)

// storedColumns is the explicit column list matching StoredOutput scan order.
const storedColumns = `id, project_key, session_id, tool_name, summary, full_content,
	original_size, summary_size, created_at, pinned, access_count, last_accessed, input_hash, output_hash,
	full_retained, command_fp, delivered_size`

// EffectiveSizeExpr is a row's effective stored size — the bytes it actually
// occupies. A summary-only row (full_retained = 0, written under
// store.retention) drops its body and chunks, so it costs ~summary_size, not
// original_size. Used by the byte-cap accounting (max_size_mb eviction,
// max_pinned_mb pin budget) so lowering retention expands the item budget
// (upstream #247). NOT used for the savings figures, which report the context
// actually saved.
const EffectiveSizeExpr = "CASE WHEN full_retained = 1 THEN original_size ELSE summary_size END"

// VacuumThreshold is the minimum number of deleted rows that triggers
// incremental_vacuum to reclaim disk space.
const VacuumThreshold = 50

func scanOutput(s interface {
	Scan(dest ...any) error
}) (StoredOutput, error) {
	var o StoredOutput
	var lastAccessed, deliveredSize sql.NullInt64
	var inputHash, outputHash, commandFP sql.NullString
	err := s.Scan(
		&o.ID, &o.ProjectKey, &o.SessionID, &o.ToolName, &o.Summary, &o.FullContent,
		&o.OriginalSize, &o.SummarySize, &o.CreatedAt, &o.Pinned, &o.AccessCount,
		&lastAccessed, &inputHash, &outputHash, &o.FullRetained, &commandFP,
		&deliveredSize,
	)
	if err != nil {
		return o, err
	}
	if lastAccessed.Valid {
		v := lastAccessed.Int64
		o.LastAccessed = &v
	}
	if inputHash.Valid {
		v := inputHash.String
		o.InputHash = &v
	}
	if outputHash.Valid {
		v := outputHash.String
		o.OutputHash = &v
	}
	if commandFP.Valid {
		v := commandFP.String
		o.CommandFP = &v
	}
	if deliveredSize.Valid {
		v := deliveredSize.Int64
		o.DeliveredSize = &v
	}
	return o, nil
}

func scanOutputs(rows *sql.Rows) ([]StoredOutput, error) {
	defer rows.Close()
	var out []StoredOutput
	for rows.Next() {
		o, err := scanOutput(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func generateID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "recall_" + hex.EncodeToString(b)
}

func nowUnix() int64 { return time.Now().Unix() }

func storeChunks(tx *sql.Tx, id, fullContent string) error {
	chunks := ChunkText(fullContent)
	stmt, err := tx.Prepare(`INSERT INTO content_chunks (output_id, chunk_index, content) VALUES (?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for i, c := range chunks {
		if _, err := stmt.Exec(id, i, c); err != nil {
			return err
		}
	}
	return nil
}

func countAndDelete(database *sql.DB, where string, params ...any) (int, error) {
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM stored_outputs WHERE `+where, params...).Scan(&count); err != nil {
		return 0, err
	}
	if count > 0 {
		if _, err := database.Exec(`DELETE FROM stored_outputs WHERE `+where, params...); err != nil {
			return 0, err
		}
	}
	return count, nil
}

// StoreOutput persists a compressed tool output and populates the FTS index and
// chunk table. Returns the fully-hydrated row including the generated id.
func StoreOutput(database *sql.DB, in StoreInput) (StoredOutput, error) {
	id := generateID()
	summarySize := len([]byte(in.Summary))
	createdAt := nowUnix()
	// Content hash enables dedup of identical output across different calls.
	// Reuse the caller's hash when provided (the hook already computed it).
	// Hash the REAL content even for summary-only rows, so dedup still works.
	outputHash := in.OutputHash
	if outputHash == nil {
		h := HashContent(in.FullContent)
		outputHash = &h
	}

	// Summary-only rows (store.retention) drop the verbatim body and its chunks.
	// full_content is NOT NULL, so store an empty string rather than NULL.
	fullRetained := 1
	if in.FullRetained != nil {
		fullRetained = *in.FullRetained
	}
	bodyToStore := in.FullContent
	if fullRetained == 0 {
		bodyToStore = ""
	}

	tx, err := database.Begin()
	if err != nil {
		return StoredOutput{}, err
	}
	_, err = tx.Exec(`
		INSERT INTO stored_outputs
			(id, project_key, session_id, tool_name, summary, full_content,
			 original_size, summary_size, created_at, input_hash, output_hash, full_retained, command_fp,
			 delivered_size)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, in.ProjectKey, in.SessionID, in.ToolName,
		in.Summary, bodyToStore, in.OriginalSize,
		summarySize, createdAt, nullString(in.InputHash), nullString(outputHash), fullRetained,
		nullString(in.CommandFP), nullInt(in.DeliveredSize),
	)
	if err != nil {
		tx.Rollback()
		return StoredOutput{}, err
	}
	if fullRetained != 0 {
		if err := storeChunks(tx, id, in.FullContent); err != nil {
			tx.Rollback()
			return StoredOutput{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return StoredOutput{}, err
	}

	return StoredOutput{
		ID: id, ProjectKey: in.ProjectKey, SessionID: in.SessionID,
		ToolName: in.ToolName, Summary: in.Summary, FullContent: bodyToStore,
		OriginalSize: in.OriginalSize, SummarySize: summarySize, CreatedAt: createdAt,
		Pinned: 0, AccessCount: 0, LastAccessed: nil, InputHash: in.InputHash, OutputHash: outputHash,
		FullRetained: fullRetained, CommandFP: in.CommandFP, DeliveredSize: in.DeliveredSize,
	}, nil
}

// HashContent returns the sha256 hex of tool output content — a path-independent
// dedup key.
func HashContent(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func nullString(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// RetrieveOutput fetches a single stored output by id, or nil if not found.
func RetrieveOutput(database *sql.DB, id string) (*StoredOutput, error) {
	row := database.QueryRow(`SELECT `+storedColumns+` FROM stored_outputs WHERE id = ?`, id)
	o, err := scanOutput(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &o, nil
}

// RecordAccess increments access_count and updates last_accessed for an item.
func RecordAccess(database *sql.DB, id string) error {
	_, err := database.Exec(`
		UPDATE stored_outputs
		SET access_count = access_count + 1, last_accessed = ?
		WHERE id = ?`, nowUnix(), id)
	return err
}

// PinReason explains why a PinOutcome was not OK.
type PinReason string

const (
	// PinNotFound means no item matched the id within the project.
	PinNotFound PinReason = "not_found"
	// PinOverBudget means pinning would push total pinned bytes past the cap.
	PinOverBudget PinReason = "over_budget"
)

// PinOutcome is the result of a pin attempt. On PinOverBudget the byte figures
// are populated so the caller can build an actionable message without re-querying.
type PinOutcome struct {
	OK          bool
	Reason      PinReason
	PinnedBytes int64
	ItemBytes   int64
	CapBytes    int64
}

// PinOutput pins or unpins an item without a budget check. Returns true if the
// item was found/updated. Used by internal callers that pin unconditionally;
// user-facing pins go through PinOutputBounded.
func PinOutput(database *sql.DB, id, projectKey string, pinned bool) (bool, error) {
	outcome, err := PinOutputBounded(database, id, projectKey, pinned, 0)
	return outcome.OK, err
}

// PinOutputBounded pins or unpins an item, enforcing the pinned-data cap at the
// write. Pinned items are exempt from expiry and eviction, so an unbounded number
// of pins would silently void store.max_size_mb. When pinning a not-yet-pinned
// item and maxPinnedMB is positive, if the item's effective size (summary_size
// for summary-only rows, else original_size) would push total pinned bytes over
// the cap the row is left unpinned and PinOverBudget is returned — so the bound holds even if the caller ignores the result and keeps
// pinning. Unpinning, and re-pinning an already-pinned item, are never checked.
// A non-positive maxPinnedMB disables the check.
func PinOutputBounded(database *sql.DB, id, projectKey string, pinned bool, maxPinnedMB float64) (PinOutcome, error) {
	var itemSize int64
	var alreadyPinned int
	err := database.QueryRow(
		`SELECT `+EffectiveSizeExpr+`, pinned FROM stored_outputs WHERE id = ? AND project_key = ?`,
		id, projectKey).Scan(&itemSize, &alreadyPinned)
	if errors.Is(err, sql.ErrNoRows) {
		return PinOutcome{Reason: PinNotFound}, nil
	}
	if err != nil {
		return PinOutcome{}, err
	}

	if pinned && alreadyPinned == 0 && maxPinnedMB > 0 {
		capBytes := int64(maxPinnedMB * 1024 * 1024)
		var pinnedBytes int64
		if err := database.QueryRow(
			`SELECT COALESCE(SUM(`+EffectiveSizeExpr+`), 0) FROM stored_outputs WHERE project_key = ? AND pinned = 1`,
			projectKey).Scan(&pinnedBytes); err != nil {
			return PinOutcome{}, err
		}
		if pinnedBytes+itemSize > capBytes {
			return PinOutcome{
				Reason:      PinOverBudget,
				PinnedBytes: pinnedBytes,
				ItemBytes:   itemSize,
				CapBytes:    capBytes,
			}, nil
		}
	}

	p := 0
	if pinned {
		p = 1
	}
	if _, err := database.Exec(`UPDATE stored_outputs SET pinned = ? WHERE id = ? AND project_key = ?`, p, id, projectKey); err != nil {
		return PinOutcome{}, err
	}
	return PinOutcome{OK: true}, nil
}

// CheckOutputDedup looks up the most recent stored output whose summary can
// stand in for this call: identical content (output_hash) from the same tool
// and, for Bash, the same command family (commandFP; nil matches NULL).
// Matching on input instead served a re-run its first run's result after the
// output changed, and matching content across commands delivered one
// command's summary for another (upstream #324). Returns nil on a miss.
func CheckOutputDedup(database *sql.DB, projectKey, outputHash, toolName string, commandFP *string) (*StoredOutput, error) {
	row := database.QueryRow(`
		SELECT `+storedColumns+` FROM stored_outputs
		WHERE project_key = ? AND output_hash = ? AND tool_name = ? AND command_fp IS ?
		ORDER BY created_at DESC
		LIMIT 1`, projectKey, outputHash, toolName, nullString(commandFP))
	o, err := scanOutput(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &o, nil
}

const secondsPerDay = 86400

// EvictIfNeeded enforces the project store size cap by evicting the
// lowest-value non-pinned items until total effective size is within maxSizeMB.
// Effective size is original_size for full-body rows and summary_size for
// summary-only rows, so lowering retention raises the number of items the store
// holds before eviction (upstream #247).
// Value is a recency-weighted frequency score: (access_count + 1) decayed by an
// exponential half-life on the time since last access (falling back to creation
// time for never-accessed items). nowSecs is injectable for deterministic tests.
func EvictIfNeeded(database *sql.DB, projectKey string, maxSizeMB float64, halfLifeDays int, nowSecs int64) (int, error) {
	maxBytes := int64(maxSizeMB * 1024 * 1024)

	var total int64
	if err := database.QueryRow(`
		SELECT COALESCE(SUM(`+EffectiveSizeExpr+`), 0) FROM stored_outputs WHERE project_key = ?`, projectKey).Scan(&total); err != nil {
		return 0, err
	}
	if total <= maxBytes {
		return 0, nil
	}
	bytesToShed := total - maxBytes

	rows, err := database.Query(`
		SELECT id, `+EffectiveSizeExpr+`, access_count, last_accessed, created_at
		FROM stored_outputs
		WHERE project_key = ? AND pinned = 0`, projectKey)
	if err != nil {
		return 0, err
	}
	type cand struct {
		id        string
		size      int64
		score     float64
		createdAt int64
	}
	// Math.max(1, …) guards a non-positive half-life.
	halfLifeSecs := math.Max(1, float64(halfLifeDays)*secondsPerDay)
	var candidates []cand
	for rows.Next() {
		var id string
		var size, createdAt int64
		var accessCount int
		var lastAccessed sql.NullInt64
		if err := rows.Scan(&id, &size, &accessCount, &lastAccessed, &createdAt); err != nil {
			rows.Close()
			return 0, err
		}
		lastActive := createdAt
		if lastAccessed.Valid {
			lastActive = lastAccessed.Int64
		}
		age := float64(nowSecs - lastActive)
		if age < 0 {
			age = 0
		}
		recency := math.Pow(0.5, age/halfLifeSecs)
		candidates = append(candidates, cand{id, size, (float64(accessCount) + 1) * recency, createdAt})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(candidates) == 0 {
		return 0, nil
	}

	// Evict lowest value first; tiebreak oldest creation, then id.
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.score != b.score {
			return a.score < b.score
		}
		if a.createdAt != b.createdAt {
			return a.createdAt < b.createdAt
		}
		return a.id < b.id
	})

	var toEvict []any
	var shed int64
	for _, c := range candidates {
		if shed >= bytesToShed {
			break
		}
		toEvict = append(toEvict, c.id)
		shed += c.size
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(toEvict)), ",")
	if _, err := database.Exec(`DELETE FROM stored_outputs WHERE id IN (`+placeholders+`)`, toEvict...); err != nil {
		return 0, err
	}
	ReclaimPages(database, len(toEvict))
	return len(toEvict), nil
}

// RetrieveSnippet returns a relevant excerpt from a stored item's full content
// using FTS. Prefers chunk-based retrieval (precise, verbatim) over the legacy
// snippet function. Returns "" if the item doesn't exist or has no match.
func RetrieveSnippet(database *sql.DB, id, query string) string {
	var rowid int64
	err := database.QueryRow(`SELECT rowid FROM stored_outputs WHERE id = ?`, id).Scan(&rowid)
	if err != nil {
		return ""
	}
	safe := SanitizeFtsQuery(query)

	var content string
	err = database.QueryRow(`
		SELECT content FROM content_chunks
		WHERE content_chunks MATCH ? AND output_id = ?
		ORDER BY rank
		LIMIT 1`, safe, id).Scan(&content)
	if err == nil {
		return content
	}

	var excerpt string
	err = database.QueryRow(`
		SELECT snippet(outputs_fts, 3, '', '', ' [...] ', 64)
		FROM outputs_fts
		WHERE outputs_fts MATCH ?
		AND rowid = ?`, safe, rowid).Scan(&excerpt)
	if err != nil {
		return ""
	}
	return excerpt
}

const (
	peekMaxChunks   = 3
	peekChunkJoiner = "\n […] \n"
)

// RetrievePeek returns a bounded, multi-chunk context window into a stored item
// — the middle tier between SearchOutputs and full content. With a query, the
// top matching chunks (in document order); without one, the first chunks as a
// head preview. Returns "" when the item has no stored chunks (caller falls back).
func RetrievePeek(database *sql.DB, id, query string, maxChunks int) string {
	var one int
	if database.QueryRow(`SELECT 1 FROM stored_outputs WHERE id = ?`, id).Scan(&one) != nil {
		return ""
	}

	if query != "" {
		safe := SanitizeFtsQuery(query)
		rows, err := database.Query(`
			SELECT content, chunk_index FROM content_chunks
			WHERE content_chunks MATCH ? AND output_id = ?
			ORDER BY rank
			LIMIT ?`, safe, id, maxChunks)
		if err != nil {
			return ""
		}
		type chunk struct {
			content string
			index   int
		}
		var chunks []chunk
		for rows.Next() {
			var c chunk
			if rows.Scan(&c.content, &c.index) == nil {
				chunks = append(chunks, c)
			}
		}
		rows.Close()
		if len(chunks) == 0 {
			return ""
		}
		// Present in document order so the joiner reflects real gaps.
		sort.SliceStable(chunks, func(i, j int) bool { return chunks[i].index < chunks[j].index })
		parts := make([]string, len(chunks))
		for i, c := range chunks {
			parts[i] = c.content
		}
		return strings.Join(parts, peekChunkJoiner)
	}

	rows, err := database.Query(`
		SELECT content FROM content_chunks
		WHERE output_id = ?
		ORDER BY chunk_index
		LIMIT ?`, id, maxChunks)
	if err != nil {
		return ""
	}
	defer rows.Close()
	var parts []string
	for rows.Next() {
		var c string
		if rows.Scan(&c) == nil {
			parts = append(parts, c)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, peekChunkJoiner)
}

// SearchOutputs full-text searches stored outputs for a project, ordered by FTS
// rank (best match first), capped at options.Limit (default 10).
func SearchOutputs(database *sql.DB, query string, options SearchOptions) []StoredOutput {
	limit := options.Limit
	if limit == 0 {
		limit = 10
	}
	safe := SanitizeFtsQuery(query)
	sqlStr := `
		SELECT ` + prefixCols("s.") + ` FROM outputs_fts f
		JOIN stored_outputs s ON s.rowid = f.rowid
		WHERE outputs_fts MATCH ?
		AND s.project_key = ?`
	params := []any{safe, options.ProjectKey}
	if options.Tool != "" {
		sqlStr += ` AND s.tool_name = ?`
		params = append(params, options.Tool)
	}
	sqlStr += ` ORDER BY rank LIMIT ?`
	params = append(params, limit)

	rows, err := database.Query(sqlStr, params...)
	if err != nil {
		return []StoredOutput{}
	}
	out, err := scanOutputs(rows)
	if err != nil {
		return []StoredOutput{}
	}
	return out
}

// prefixCols returns storedColumns with each column prefixed (e.g. "s.").
func prefixCols(prefix string) string {
	parts := strings.Split(storedColumns, ",")
	for i, p := range parts {
		parts[i] = prefix + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}

// ListOutputs returns a paginated list of stored outputs, optionally filtered by tool.
func ListOutputs(database *sql.DB, options ListOptions) []StoredOutput {
	limit := options.Limit
	if limit == 0 {
		limit = 20
	}
	order := "DESC"
	if options.Sort == "oldest" {
		order = "ASC"
	}
	sqlStr := `SELECT ` + storedColumns + ` FROM stored_outputs WHERE project_key = ?`
	params := []any{options.ProjectKey}
	if options.Tool != "" {
		sqlStr += ` AND tool_name = ?`
		params = append(params, options.Tool)
	}
	sqlStr += ` ORDER BY created_at ` + order + ` LIMIT ? OFFSET ?`
	params = append(params, limit, options.Offset)

	rows, err := database.Query(sqlStr, params...)
	if err != nil {
		return []StoredOutput{}
	}
	out, err := scanOutputs(rows)
	if err != nil {
		return []StoredOutput{}
	}
	return out
}

// ForgetOutputs deletes stored outputs matching the given criteria. Pinned items
// are skipped unless Force is true — except single-ID deletes, which always
// bypass pin protection. Returns the number of items deleted.
func ForgetOutputs(database *sql.DB, projectKey string, options ForgetOptions) (int, error) {
	pinGuard := " AND pinned = 0"
	if options.Force {
		pinGuard = ""
	}
	var deleted int
	var err error

	switch {
	case options.All:
		deleted, err = countAndDelete(database, "project_key = ?"+pinGuard, projectKey)
	case options.ID != "":
		deleted, err = countAndDelete(database, "id = ? AND project_key = ?", options.ID, projectKey)
	case options.Tool != "":
		deleted, err = countAndDelete(database, "tool_name = ? AND project_key = ?"+pinGuard, options.Tool, projectKey)
	case options.SessionID != "":
		deleted, err = countAndDelete(database, "session_id = ? AND project_key = ?"+pinGuard, options.SessionID, projectKey)
	case options.OlderThanDays != nil:
		cutoff := nowUnix() - int64(*options.OlderThanDays)*86400
		deleted, err = countAndDelete(database, "created_at < ? AND project_key = ?"+pinGuard, cutoff, projectKey)
	}
	if err != nil {
		return 0, err
	}

	ReclaimPages(database, deleted)
	return deleted, nil
}

// ReclaimPages returns free pages to the OS after a bulk delete, when enough
// rows were removed to be worth the work. A no-op on databases created without
// auto_vacuum=INCREMENTAL (e.g. legacy stores) — those must be reclaimed with a
// full VACUUM, which `mcprecall gc --vacuum` performs. Never returns an error.
func ReclaimPages(database *sql.DB, deleted int) {
	if deleted < VacuumThreshold {
		return
	}
	if _, err := database.Exec("PRAGMA incremental_vacuum"); err != nil {
		logx.Warn("incremental_vacuum failed — " + err.Error())
	}
}

// SetMeta upserts a key/value pair into the per-project meta table.
func SetMeta(database *sql.DB, key, value string) error {
	_, err := database.Exec(
		`INSERT INTO meta (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// GetMeta reads a value from the meta table. Returns "" if the key is absent.
// The error is non-nil only for a real read failure (e.g. no meta table at all,
// which is a legitimate legacy database), letting callers distinguish "absent"
// from "cannot be read".
func GetMeta(database *sql.DB, key string) (string, error) {
	var v string
	err := database.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return v, nil
}

// PruneExpired deletes non-pinned items created more than calendarDays ago.
func PruneExpired(database *sql.DB, projectKey string, calendarDays int) (int, error) {
	cutoff := nowUnix() - int64(calendarDays)*86400
	deleted, err := countAndDelete(database, "created_at < ? AND project_key = ? AND pinned = 0", cutoff, projectKey)
	if err != nil {
		return 0, err
	}
	ReclaimPages(database, deleted)
	return deleted, nil
}

// ForeignCount is a row count under a project key other than the current one.
type ForeignCount struct {
	ProjectKey string
	Count      int
}

// ForeignKeyBreakdown returns the count of stored outputs grouped by every
// project key OTHER than the current one, most rows first. Used to surface rows
// stranded under a foreign key so they can be enumerated and deleted through the
// tool layer.
func ForeignKeyBreakdown(database *sql.DB, currentKey string) []ForeignCount {
	rows, err := database.Query(`
		SELECT project_key, COUNT(*) AS n
		FROM stored_outputs
		WHERE project_key != ?
		GROUP BY project_key
		ORDER BY n DESC, project_key ASC`, currentKey)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []ForeignCount
	for rows.Next() {
		var fc ForeignCount
		if err := rows.Scan(&fc.ProjectKey, &fc.Count); err != nil {
			return nil
		}
		out = append(out, fc)
	}
	if rows.Err() != nil {
		return nil
	}
	return out
}

// RecordSession records a session date (YYYY-MM-DD). No-op if already present.
func RecordSession(database *sql.DB, date string) error {
	_, err := database.Exec(`INSERT OR IGNORE INTO sessions (date) VALUES (?)`, date)
	return err
}

// GetSessionDays returns all recorded session dates in descending order.
func GetSessionDays(database *sql.DB) []string {
	rows, err := database.Query(`SELECT date FROM sessions ORDER BY date DESC`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return out
		}
		out = append(out, d)
	}
	return out
}
