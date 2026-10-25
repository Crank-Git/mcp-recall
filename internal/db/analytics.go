// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/db/analytics.go

package db

import (
	"database/sql"
	"sort"
	"time"
)

func todayUTC() string {
	return time.Now().UTC().Format("2006-01-02")
}

// startOfDayUnix parses YYYY-MM-DD as UTC midnight and returns the unix seconds.
func startOfDayUnix(date string) int64 {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return 0
	}
	return t.UTC().Unix()
}

// deliveredExpr is the bytes Claude received for a row: its delivered size
// (header included, upstream #319), or for rows written before that was
// recorded, the summary alone. The *SummaryBytes savings figures sum this.
const deliveredExpr = "COALESCE(delivered_size, summary_size)"

// GetStats returns aggregate storage stats for a project. The savings figures
// (Total*, CompressionRatio) cover intercepted tool output only — recall__note
// memory is excluded and reported separately as Note*, so a bulk note backend
// writing thousands of pinned notes can't dilute the compression figure.
// Pin-budget figures stay store-wide, since notes are pinned and do consume
// store.max_pinned_mb.
func GetStats(database *sql.DB, projectKey string) Stats {
	var s Stats
	_ = database.QueryRow(`
		SELECT
			COALESCE(SUM(CASE WHEN tool_name != 'recall__note' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN tool_name != 'recall__note' THEN original_size ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN tool_name != 'recall__note' THEN `+deliveredExpr+` ELSE 0 END), 0),
			COALESCE(SUM(pinned), 0),
			COALESCE(SUM(CASE WHEN pinned = 1 THEN `+EffectiveSizeExpr+` ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN tool_name = 'recall__note' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN tool_name = 'recall__note' THEN original_size ELSE 0 END), 0)
		FROM stored_outputs
		WHERE project_key = ?`, projectKey).Scan(
		&s.TotalItems, &s.TotalOriginalBytes, &s.TotalSummaryBytes, &s.PinnedItems, &s.PinnedBytes,
		&s.NoteItems, &s.NoteBytes)

	if s.TotalOriginalBytes > 0 {
		s.CompressionRatio = float64(s.TotalSummaryBytes) / float64(s.TotalOriginalBytes)
	}
	return s
}

// GetBashCommandBreakdown returns per-command-family storage stats for Bash
// rows only, sorted by original_bytes desc (upstream #251). Pre-migration /
// untagged Bash rows fold into an "unknown" bucket.
func GetBashCommandBreakdown(database *sql.DB, projectKey string) []CommandBreakdownRow {
	rows, err := database.Query(`
		SELECT
			COALESCE(command_fp, 'unknown'),
			COUNT(*),
			COALESCE(SUM(original_size),0),
			COALESCE(SUM(`+deliveredExpr+`),0)
		FROM stored_outputs
		WHERE project_key = ? AND tool_name = 'Bash'
		GROUP BY COALESCE(command_fp, 'unknown')
		ORDER BY 3 DESC`, projectKey)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []CommandBreakdownRow
	for rows.Next() {
		var r CommandBreakdownRow
		if err := rows.Scan(&r.CommandFP, &r.Items, &r.OriginalBytes, &r.SummaryBytes); err != nil {
			return out
		}
		out = append(out, r)
	}
	return out
}

// GetToolBreakdown returns per-tool storage stats, sorted by original_bytes desc.
func GetToolBreakdown(database *sql.DB, projectKey string) []ToolBreakdownRow {
	rows, err := database.Query(`
		SELECT
			tool_name,
			COUNT(*),
			COALESCE(SUM(original_size),0),
			COALESCE(SUM(`+deliveredExpr+`),0)
		FROM stored_outputs
		WHERE project_key = ?
		GROUP BY tool_name
		ORDER BY 3 DESC`, projectKey)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []ToolBreakdownRow
	for rows.Next() {
		var r ToolBreakdownRow
		if err := rows.Scan(&r.ToolName, &r.Items, &r.OriginalBytes, &r.SummaryBytes); err != nil {
			return out
		}
		out = append(out, r)
	}
	return out
}

// SampleOutputs returns the most recent `limit` stored outputs for an exact
// tool_name match. Used by profiles retrain to sample real output corpus.
func SampleOutputs(database *sql.DB, projectKey, toolName string, limit int) []StoredOutput {
	rows, err := database.Query(`
		SELECT `+storedColumns+` FROM stored_outputs
		WHERE project_key = ? AND tool_name = ?
		ORDER BY created_at DESC
		LIMIT ?`, projectKey, toolName, limit)
	if err != nil {
		return nil
	}
	out, err := scanOutputs(rows)
	if err != nil {
		return nil
	}
	return out
}

// GetSuggestions returns pin candidates (frequently accessed, not yet pinned)
// and stale candidates (never accessed, older than staleDays).
func GetSuggestions(database *sql.DB, projectKey string, opts SuggestionsOptions) SuggestionsData {
	threshold := opts.PinThreshold
	if threshold == 0 {
		threshold = 5
	}
	staleDays := opts.StaleDays
	if staleDays == 0 {
		staleDays = 3
	}
	limit := opts.Limit
	if limit == 0 {
		limit = 3
	}
	staleCutoff := nowUnix() - int64(staleDays)*86400

	pinRows, err := database.Query(`
		SELECT `+storedColumns+` FROM stored_outputs
		WHERE project_key = ? AND pinned = 0 AND access_count >= ?
		ORDER BY access_count DESC
		LIMIT ?`, projectKey, threshold, limit)
	var pin []StoredOutput
	if err == nil {
		pin, _ = scanOutputs(pinRows)
	}

	staleRows, err := database.Query(`
		SELECT `+storedColumns+` FROM stored_outputs
		WHERE project_key = ? AND pinned = 0 AND access_count = 0 AND created_at < ?
		ORDER BY created_at ASC
		LIMIT ?`, projectKey, staleCutoff, limit)
	var stale []StoredOutput
	if err == nil {
		stale, _ = scanOutputs(staleRows)
	}

	return SuggestionsData{PinCandidates: pin, StaleCandidates: stale}
}

// SampleLimit is the most rows a sampleBySession sample returns, across all
// sessions.
const SampleLimit = 10

// SamplePerSession is the most rows any one session contributes to a sample
// spanning sessions.
const SamplePerSession = 3

// sampleBySession samples the rows matching where without letting one session
// fill the sample: each session contributes at most perSession, and slots are
// dealt round-robin (every session's best row before any session's second),
// best meaning most recently accessed, then newest. A bulk writer can put
// thousands of rows in one session (upstream #269), so callers that list rows
// to Claude use this rather than an unbounded SELECT. full_content is never read.
func sampleBySession(database *sql.DB, where string, params []any, perSession int) SessionSample {
	sample := SessionSample{Items: []SampledItem{}, Overflow: []SessionOverflow{}}
	rows, err := database.Query(`
		WITH ranked AS (
			SELECT id, session_id, tool_name, summary, created_at, last_accessed,
				ROW_NUMBER() OVER (
					PARTITION BY session_id
					ORDER BY last_accessed DESC NULLS LAST, created_at DESC
				) AS rn
			FROM stored_outputs
			`+where+`
		)
		SELECT id, session_id, tool_name, summary, created_at, last_accessed
		FROM ranked
		WHERE rn <= ?
		ORDER BY rn, last_accessed DESC NULLS LAST, created_at DESC
		LIMIT ?`, append(append([]any{}, params...), perSession, SampleLimit)...)
	if err != nil {
		return sample
	}
	shown := map[string]int{}
	for rows.Next() {
		var it SampledItem
		var la sql.NullInt64
		if rows.Scan(&it.ID, &it.SessionID, &it.ToolName, &it.Summary, &it.CreatedAt, &la) == nil {
			if la.Valid {
				v := la.Int64
				it.LastAccessed = &v
			}
			sample.Items = append(sample.Items, it)
			shown[it.SessionID]++
		}
	}
	rows.Close()

	counts, err := database.Query(`
		SELECT session_id, COUNT(*) FROM stored_outputs `+where+`
		GROUP BY session_id`, params...)
	if err != nil {
		return sample
	}
	for counts.Next() {
		var o SessionOverflow
		if counts.Scan(&o.SessionID, &o.Count) == nil {
			sample.Total += o.Count
			o.Count -= shown[o.SessionID]
			if o.Count > 0 {
				sample.Overflow = append(sample.Overflow, o)
			}
		}
	}
	counts.Close()
	sort.Slice(sample.Overflow, func(i, j int) bool {
		a, b := sample.Overflow[i], sample.Overflow[j]
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		return a.SessionID < b.SessionID
	})
	return sample
}

// GetSessionSummary returns a digest of stored activity for a session or day.
func GetSessionSummary(database *sql.DB, projectKey string, opts SessionSummaryOptions) SessionSummaryData {
	var filter, label string
	var filterParams []any

	if opts.SessionID != "" {
		filter = "session_id = ?"
		filterParams = []any{opts.SessionID}
		label = opts.SessionID
	} else {
		date := opts.Date
		if date == "" {
			date = todayUTC()
		}
		startOfDay := startOfDayUnix(date)
		endOfDay := startOfDay + 86400
		filter = "created_at >= ? AND created_at < ?"
		filterParams = []any{startOfDay, endOfDay}
		label = date
	}

	base := "WHERE project_key = ? AND " + filter
	bp := append([]any{projectKey}, filterParams...)

	var d SessionSummaryData
	d.Label = label
	_ = database.QueryRow(`
		SELECT
			COUNT(*),
			COALESCE(SUM(original_size), 0),
			COALESCE(SUM(`+deliveredExpr+`), 0),
			COUNT(CASE WHEN access_count > 0 THEN 1 END),
			COALESCE(SUM(access_count), 0)
		FROM stored_outputs `+base, bp...).Scan(
		&d.StoredCount, &d.TotalOriginalBytes, &d.TotalSummaryBytes, &d.AccessedCount, &d.TotalAccesses)

	if rows, err := database.Query(`
		SELECT tool_name, COUNT(*) FROM stored_outputs `+base+`
		GROUP BY tool_name ORDER BY 2 DESC`, bp...); err == nil {
		for rows.Next() {
			var tc ToolCount
			if rows.Scan(&tc.ToolName, &tc.Count) == nil {
				d.ToolCounts = append(d.ToolCounts, tc)
			}
		}
		rows.Close()
	}

	if rows, err := database.Query(`
		SELECT id, tool_name, summary, access_count FROM stored_outputs `+base+` AND access_count > 0
		ORDER BY access_count DESC LIMIT 5`, bp...); err == nil {
		for rows.Next() {
			var t TopAccessed
			if rows.Scan(&t.ID, &t.ToolName, &t.Summary, &t.AccessCount) == nil {
				d.TopAccessed = append(d.TopAccessed, t)
			}
		}
		rows.Close()
	}

	// A single-session summary has one session to sample, so it gets every
	// slot. Notes are unpinned ones only, as in GetContext: a pinned note is
	// listed under pinned, not twice (upstream #343).
	perSession := SamplePerSession
	if opts.SessionID != "" {
		perSession = SampleLimit
	}
	d.Pinned = sampleBySession(database, base+" AND pinned = 1", bp, perSession)
	d.Notes = sampleBySession(database, base+" AND pinned = 0 AND tool_name = 'recall__note'", bp, perSession)

	return d
}

// GetContext returns a session-orientation snapshot in five isolated sections.
func GetContext(database *sql.DB, projectKey string, days, limit int) ContextData {
	if days == 0 {
		days = 7
	}
	if limit == 0 {
		limit = 5
	}
	cutoff := nowUnix() - int64(days)*86400
	today := todayUTC()

	var cd ContextData

	// Pinned items, bounded and session-fair (upstream #269).
	cd.Pinned = sampleBySession(database, "WHERE project_key = ? AND pinned = 1", []any{projectKey}, SamplePerSession)

	if rows, err := database.Query(`
		SELECT `+storedColumns+` FROM stored_outputs
		WHERE project_key = ? AND pinned = 0 AND tool_name = 'recall__note'
		ORDER BY created_at DESC
		LIMIT 10`, projectKey); err == nil {
		cd.Notes, _ = scanOutputs(rows)
	}

	if rows, err := database.Query(`
		SELECT `+storedColumns+` FROM stored_outputs
		WHERE project_key = ? AND pinned = 0 AND tool_name != 'recall__note'
		  AND last_accessed >= ?
		ORDER BY last_accessed DESC
		LIMIT ?`, projectKey, cutoff, limit); err == nil {
		cd.Recent, _ = scanOutputs(rows)
	}

	sessionDays := GetSessionDays(database)
	var lastDate string
	for _, day := range sessionDays {
		if day < today {
			lastDate = day
			break
		}
	}
	if lastDate != "" {
		summary := GetSessionSummary(database, projectKey, SessionSummaryOptions{Date: lastDate})
		if summary.StoredCount > 0 {
			cd.LastSession = &LastSession{
				Date:               lastDate,
				StoredCount:        summary.StoredCount,
				TotalOriginalBytes: summary.TotalOriginalBytes,
				TotalSummaryBytes:  summary.TotalSummaryBytes,
			}
		}
	}

	cd.Hot = []StoredOutput{}
	if lastDate != "" {
		startOfDay := startOfDayUnix(lastDate)
		endOfDay := startOfDay + 86400
		exclude := map[string]bool{}
		for _, i := range cd.Pinned.Items {
			exclude[i.ID] = true
		}
		for _, i := range cd.Notes {
			exclude[i.ID] = true
		}
		for _, i := range cd.Recent {
			exclude[i.ID] = true
		}
		if rows, err := database.Query(`
			SELECT `+storedColumns+` FROM stored_outputs
			WHERE project_key = ?
			  AND pinned = 0
			  AND tool_name != 'recall__note'
			  AND created_at >= ? AND created_at < ?
			  AND access_count > 0
			ORDER BY access_count DESC
			LIMIT ?`, projectKey, startOfDay, endOfDay, 5+len(exclude)); err == nil {
			all, _ := scanOutputs(rows)
			for _, r := range all {
				if !exclude[r.ID] && len(cd.Hot) < 5 {
					cd.Hot = append(cd.Hot, r)
				}
			}
		}
	}

	return cd
}
