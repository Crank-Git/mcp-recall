// Package tools implements the recall__* MCP tool logic: pure functions that
// take a DB + args and return a formatted text response. Ports src/tools.ts.
package tools

import (
	"database/sql"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"mcprecall/internal/config"
	"mcprecall/internal/db"
	"mcprecall/internal/format"
	"mcprecall/internal/jsonx"
	"mcprecall/internal/secrets"
)

// ContextEmptyResponse is returned when there is nothing to show / inject.
const ContextEmptyResponse = "[recall: no context available yet — use recall tools to build up your context store]"

const (
	searchExcerptLen  = 120
	noteExcerptLen    = 200
	contextExcerptLen = 100
	snippetMax        = 150
	listToolColWidth  = 40
	listIDColWidth    = 16
	// pinBudgetWarnPct is the share of store.max_pinned_mb at which recall__stats warns.
	pinBudgetWarnPct = 80
)

// ── display helpers ───────────────────────────────────────────────────────────

func formatDate(unixSecs int64) string {
	return time.Unix(unixSecs, 0).UTC().Format("2006-01-02")
}

func runeLen(s string) int { return len([]rune(s)) }

func firstChars(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func oneline(s string) string { return strings.ReplaceAll(s, "\n", " ") }

func padEnd(s string, n int) string {
	if l := runeLen(s); l < n {
		return s + strings.Repeat(" ", n-l)
	}
	return s
}

func padStart(s string, n int) string {
	if l := runeLen(s); l < n {
		return strings.Repeat(" ", n-l) + s
	}
	return s
}

// toFixed mimics JS Number.toFixed: round half away from zero, n decimals.
func toFixed(x float64, n int) string {
	p := math.Pow(10, float64(n))
	return strconv.FormatFloat(math.Round(x*p)/p, 'f', n, 64)
}

// groupInt renders an integer with en-US thousands separators (toLocaleString).
func groupInt(n int) string {
	s := strconv.Itoa(n)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	lead := len(s) % 3
	if lead > 0 {
		b.WriteString(s[:lead])
	}
	for i := lead; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	out := b.String()
	if neg {
		return "-" + out
	}
	return out
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// resolveScopeKey resolves an optional explicit project_key override for
// Forget / ListStored. An empty override targets the current project. A
// supplied-but-blank key is rejected rather than silently falling back, so an
// operator can never widen scope by accident. There is deliberately no
// "all projects" wildcard — the override always names exactly one key.
func resolveScopeKey(currentKey, override string) (key, errMsg string) {
	if override == "" {
		return currentKey, ""
	}
	trimmed := strings.TrimSpace(override)
	if trimmed == "" {
		return "", "[recall: project_key must be a non-empty key — omit it to target the current project]"
	}
	return trimmed, ""
}

// foreignKeyFooter builds a one-line footer naming rows stored under foreign
// project keys, so an operator can discover and target them. Empty when none exist.
func foreignKeyFooter(database *sql.DB, currentKey string) string {
	breakdown := db.ForeignKeyBreakdown(database, currentKey)
	if len(breakdown) == 0 {
		return ""
	}
	total := 0
	parts := make([]string, len(breakdown))
	for i, b := range breakdown {
		total += b.Count
		parts[i] = fmt.Sprintf("%s (%d)", b.ProjectKey, b.Count)
	}
	keyPlural := "s"
	if len(breakdown) == 1 {
		keyPlural = ""
	}
	// The delete example is intentionally complete (all + confirmed): Forget needs a
	// selector, so a project_key-only call would silently match nothing.
	return fmt.Sprintf("[recall: %d item%s under other project key%s: %s — inspect with "+
		"recall__list_stored project_key=…, delete with recall__forget project_key=… all=true confirmed=true]",
		total, plural(total), keyPlural, strings.Join(parts, ", "))
}

func reductionPct(original, summary int) string {
	if original == 0 {
		return "0%"
	}
	return fmt.Sprintf("%d%%", int(math.Round((1-float64(summary)/float64(original))*100)))
}

func itemHeader(item db.StoredOutput) string {
	return fmt.Sprintf("[recall:%s · %s · %s · %s→%s]", item.ID, item.ToolName, formatDate(item.CreatedAt),
		format.Bytes(item.OriginalSize), format.Bytes(item.SummarySize))
}

// ── recall__retrieve ──────────────────────────────────────────────────────────

type RetrieveArgs struct {
	ID       string
	Query    string
	MaxBytes int    // 0 = use config default
	Mode     string // "summary" | "peek" | "full"; "" = peek when query given, else summary
}

// Retrieve provides graduated retrieval across three tiers: summary (cheapest),
// peek (bounded context window), and full (verbatim, capped). With no explicit
// mode, a query defaults to peek and its absence to summary.
func Retrieve(database *sql.DB, args RetrieveArgs) string {
	cfg := config.Load()
	cap := cfg.Retrieve.DefaultMaxBytes
	if args.MaxBytes != 0 {
		cap = args.MaxBytes
	}

	item, _ := db.RetrieveOutput(database, args.ID)
	if item == nil {
		return fmt.Sprintf(`[recall: no item found with id "%s"]`, args.ID)
	}

	db.RecordAccess(database, args.ID)
	header := itemHeader(*item)

	mode := args.Mode
	if mode == "" {
		if args.Query != "" {
			mode = "peek"
		} else {
			mode = "summary"
		}
	}

	// Summary-only rows (store.retention) have no verbatim body or chunks. Return
	// the summary with an explicit note instead of an empty/misleading result, so
	// full/peek never silently yields nothing.
	if item.FullRetained == 0 && mode != "summary" {
		return header + "\n" + item.Summary + "\n" +
			"[recall: full body was not retained for this output (summary-only) — " +
			`re-run the command for current output, or set store.retention="full" to keep future bodies]`
	}

	fullCapped := func() string {
		content := firstChars(item.FullContent, cap)
		truncated := ""
		if runeLen(item.FullContent) > cap {
			truncated = fmt.Sprintf("\n…(truncated at %s)", format.Bytes(cap))
		}
		return header + "\n" + content + truncated
	}

	if mode == "summary" {
		return header + "\n" + item.Summary
	}
	if mode == "full" {
		return fullCapped()
	}

	// mode == "peek": bounded context window, with graceful fallbacks.
	if peek := db.RetrievePeek(database, args.ID, args.Query, 3); peek != "" {
		return header + "\n" + peek
	}
	if args.Query != "" {
		if snippet := db.RetrieveSnippet(database, args.ID, args.Query); snippet != "" {
			return header + "\n" + snippet
		}
	}
	return fullCapped()
}

// ── recall__search ────────────────────────────────────────────────────────────

type SearchArgs struct {
	Query string
	Tool  string
	Limit int // 0 = default 5
}

func Search(database *sql.DB, projectKey string, args SearchArgs) string {
	limit := args.Limit
	if limit == 0 {
		limit = 5
	}

	results := db.SearchOutputs(database, args.Query, db.SearchOptions{ProjectKey: projectKey, Limit: limit * 3})

	var filtered []db.StoredOutput
	if args.Tool != "" {
		low := strings.ToLower(args.Tool)
		for _, r := range results {
			if strings.Contains(strings.ToLower(r.ToolName), low) {
				filtered = append(filtered, r)
			}
		}
	} else {
		filtered = results
	}

	items := filtered
	if len(items) > limit {
		items = items[:limit]
	}
	if len(items) == 0 {
		return fmt.Sprintf(`[recall: no results for "%s"]`, args.Query)
	}

	var lines []string
	for i, item := range items {
		excerpt := oneline(firstChars(item.Summary, searchExcerptLen))
		ellipsis := ""
		if runeLen(item.Summary) > searchExcerptLen {
			ellipsis = "…"
		}
		summaryLine := fmt.Sprintf("%d. %s · %s · %s\n   %s%s", i+1, item.ID, item.ToolName, formatDate(item.CreatedAt), excerpt, ellipsis)

		snippet := db.RetrieveSnippet(database, item.ID, args.Query)
		if snippet == "" {
			lines = append(lines, summaryLine)
			continue
		}
		snipText := strings.TrimSpace(oneline(snippet))
		capped := firstChars(snipText, snippetMax)
		trailing := ""
		if runeLen(snipText) > snippetMax {
			trailing = "…"
		}
		lines = append(lines, fmt.Sprintf("%s\n   > …%s%s", summaryLine, capped, trailing))
	}

	return fmt.Sprintf("Found %d result%s for \"%s\":\n\n%s", len(items), plural(len(items)), args.Query, strings.Join(lines, "\n\n"))
}

// ── recall__pin ───────────────────────────────────────────────────────────────

type PinArgs struct {
	ID     string
	Pinned *bool // nil = default true
}

func Pin(database *sql.DB, projectKey string, args PinArgs) string {
	pinned := true
	if args.Pinned != nil {
		pinned = *args.Pinned
	}
	outcome, err := db.PinOutputBounded(database, args.ID, projectKey, pinned, config.Load().Store.MaxPinnedMB)
	if err != nil {
		return "[recall: error] " + err.Error()
	}
	if outcome.OK {
		verb := "pinned"
		if !pinned {
			verb = "unpinned"
		}
		return fmt.Sprintf("[recall: %s %s]", verb, args.ID)
	}
	if outcome.Reason == db.PinNotFound {
		return fmt.Sprintf(`[recall: no item found with id "%s"]`, args.ID)
	}
	return fmt.Sprintf("[recall: cannot pin %s — pinned data would reach %s, over the %s "+
		"store.max_pinned_mb cap. Pinned items are exempt from eviction, so this cap bounds "+
		"them separately from store.max_size_mb. Unpin an item, raise store.max_pinned_mb, "+
		"or recall__forget to reclaim space.]",
		args.ID,
		format.Bytes(int(outcome.PinnedBytes+outcome.ItemBytes)),
		format.Bytes(int(outcome.CapBytes)))
}

// ── recall__note ──────────────────────────────────────────────────────────────

type NoteArgs struct {
	Text  string
	Title string
}

func Note(database *sql.DB, projectKey string, args NoteArgs) string {
	// recall__note never passes through the PostToolUse hook (mcp__recall__* is
	// denylisted there), so the hook's secret scan never sees note text — scan
	// here or nothing does (upstream #271). Refuse rather than redact, and name
	// the patterns only, never the matched value.
	if names := secrets.Find(args.Title + "\n" + args.Text); len(names) > 0 {
		return fmt.Sprintf("[recall: note NOT stored — detected %s. Remove the credential and retry, "+
			"or store a reference to it instead of the value.]", strings.Join(names, ", "))
	}

	title := args.Title
	if title == "" {
		title = "(note)"
	}
	excerpt := firstChars(args.Text, noteExcerptLen)
	ellipsis := ""
	if runeLen(args.Text) > noteExcerptLen {
		ellipsis = "…"
	}
	summary := fmt.Sprintf("%s: %s%s", title, excerpt, ellipsis)
	sessionID := time.Now().UTC().Format("2006-01-02")

	stored, err := db.StoreOutput(database, db.StoreInput{
		ProjectKey:   projectKey,
		SessionID:    sessionID,
		ToolName:     "recall__note",
		Summary:      summary,
		FullContent:  args.Text,
		OriginalSize: len(args.Text),
	})
	if err != nil {
		return "[recall: error] " + err.Error()
	}
	return fmt.Sprintf("[recall: note stored as %s]", stored.ID)
}

// ── recall__export ────────────────────────────────────────────────────────────

func storedToObj(s db.StoredOutput) *jsonx.Obj {
	o := jsonx.NewObj()
	o.Set("id", s.ID)
	o.Set("project_key", s.ProjectKey)
	o.Set("session_id", s.SessionID)
	o.Set("tool_name", s.ToolName)
	o.Set("summary", s.Summary)
	o.Set("full_content", s.FullContent)
	o.Set("original_size", float64(s.OriginalSize))
	o.Set("summary_size", float64(s.SummarySize))
	o.Set("created_at", float64(s.CreatedAt))
	o.Set("pinned", float64(s.Pinned))
	o.Set("access_count", float64(s.AccessCount))
	if s.LastAccessed != nil {
		o.Set("last_accessed", float64(*s.LastAccessed))
	} else {
		o.Set("last_accessed", nil)
	}
	if s.InputHash != nil {
		o.Set("input_hash", *s.InputHash)
	} else {
		o.Set("input_hash", nil)
	}
	// Exported so a dump round-trips: import must not chunk a body that a
	// summary-only row never had.
	o.Set("full_retained", float64(s.FullRetained))
	if s.OutputHash != nil {
		o.Set("output_hash", *s.OutputHash)
	} else {
		o.Set("output_hash", nil)
	}
	if s.CommandFP != nil {
		o.Set("command_fp", *s.CommandFP)
	} else {
		o.Set("command_fp", nil)
	}
	if s.DeliveredSize != nil {
		o.Set("delivered_size", float64(*s.DeliveredSize))
	} else {
		o.Set("delivered_size", nil)
	}
	return o
}

func Export(database *sql.DB, projectKey string) string {
	items := db.ExportAll(database, projectKey)
	if len(items) == 0 {
		return "[recall: no items to export]"
	}
	arr := make([]any, len(items))
	for i, it := range items {
		arr[i] = storedToObj(it)
	}
	return jsonx.Indent(arr)
}

// ── recall__forget ────────────────────────────────────────────────────────────

type ForgetArgs struct {
	ID            string
	Tool          string
	SessionID     string
	OlderThanDays *int
	All           bool
	Confirmed     bool
	Force         bool
	ProjectKey    string // "" = current project
}

func Forget(database *sql.DB, projectKey string, args ForgetArgs) string {
	scopeKey, errMsg := resolveScopeKey(projectKey, args.ProjectKey)
	if errMsg != "" {
		return errMsg
	}
	usingOverride := scopeKey != projectKey

	hasSelector := args.All || args.ID != "" || args.Tool != "" ||
		args.SessionID != "" || args.OlderThanDays != nil

	// A project_key override with no selector matches no branch in ForgetOutputs and
	// would silently no-op, reading as "nothing there". Reject it explicitly so the
	// footer's suggested recovery command can't mislead.
	if args.ProjectKey != "" && !hasSelector {
		return "[recall: project_key needs a selector — add all: true (with confirmed: true), or id / tool / session_id / older_than_days]"
	}

	if args.All && !args.Confirmed {
		return "[recall: clearing all stored items requires confirmed: true]"
	}
	if args.OlderThanDays != nil && *args.OlderThanDays < 1 {
		return "[recall: older_than_days must be at least 1 — use all: true with confirmed: true to delete everything]"
	}

	deleted, _ := db.ForgetOutputs(database, scopeKey, db.ForgetOptions{
		ID: args.ID, Tool: args.Tool, SessionID: args.SessionID,
		OlderThanDays: args.OlderThanDays, All: args.All, Force: args.Force,
	})
	if deleted == 0 {
		// Under an override, a zero match may just be a mistyped key — name the keys
		// that do exist so a typo isn't indistinguishable from "nothing there".
		if usingOverride {
			if hint := foreignKeyFooter(database, projectKey); hint != "" {
				return fmt.Sprintf("[recall: no items matched under project key %s — nothing deleted]\n%s", scopeKey, hint)
			}
			return fmt.Sprintf("[recall: no items matched under project key %s — nothing deleted]", scopeKey)
		}
		return "[recall: no items matched — nothing deleted]"
	}
	scopeNote := ""
	if usingOverride {
		scopeNote = " under project key " + scopeKey
	}
	return fmt.Sprintf("[recall: deleted %d item%s%s]", deleted, plural(deleted), scopeNote)
}

// ── recall__list_stored ───────────────────────────────────────────────────────

type ListStoredArgs struct {
	Limit      int // 0 = default 10
	Offset     int
	Tool       string
	Sort       string // "recent" | "accessed" | "size"
	ProjectKey string // "" = current project
}

func ListStored(database *sql.DB, projectKey string, args ListStoredArgs) string {
	scopeKey, errMsg := resolveScopeKey(projectKey, args.ProjectKey)
	if errMsg != "" {
		return errMsg
	}
	usingOverride := scopeKey != projectKey

	limit := args.Limit
	if limit == 0 {
		limit = 10
	}
	items := db.ListStoredSorted(database, scopeKey, args.Tool, args.Sort, limit, args.Offset)

	// Discovery footer only on the default (current-project) scope, first page —
	// not when already listing an explicit foreign key.
	footerSuffix := ""
	if !usingOverride && args.Offset == 0 {
		if f := foreignKeyFooter(database, projectKey); f != "" {
			footerSuffix = "\n" + f
		}
	}

	if len(items) == 0 {
		if args.Offset > 0 {
			return "[recall: no more items]"
		}
		if usingOverride {
			// A zero-row override may be a mistyped key — name the keys that exist.
			base := fmt.Sprintf("[recall: no stored items under project key %s]", scopeKey)
			if hint := foreignKeyFooter(database, projectKey); hint != "" {
				return base + "\n" + hint
			}
			return base
		}
		return "[recall: no stored items]" + footerSuffix
	}

	var rows []string
	for _, item := range items {
		reduction := reductionPct(item.OriginalSize, item.SummarySize)
		pin := ""
		if item.Pinned != 0 {
			pin = " 📌"
		}
		rows = append(rows, fmt.Sprintf("%s  %s  %s  %s→%s  %s%s",
			item.ID, padEnd(item.ToolName, listToolColWidth), formatDate(item.CreatedAt),
			padStart(format.Bytes(item.OriginalSize), 7), padEnd(format.Bytes(item.SummarySize), 8), reduction, pin))
	}

	header := fmt.Sprintf("%s  %s  %s  %s %s  Red.",
		padEnd("ID", listIDColWidth), padEnd("Tool", listToolColWidth), padEnd("Date", 10),
		padStart("Size", 7), padEnd("→", 9))
	separator := strings.Repeat("-", runeLen(header))

	out := append([]string{header, separator}, rows...)
	return strings.Join(out, "\n") + footerSuffix
}

// ── recall__context ───────────────────────────────────────────────────────────

// overflowSessions is how many sessions a sample's overflow line names.
const overflowSessions = 3

// formatSampleCount is a sample's count for a section heading: "7", or
// "10 of 4,522" when rows were left out.
func formatSampleCount(sample db.SessionSample) string {
	if len(sample.Overflow) > 0 {
		return fmt.Sprintf("%d of %s", len(sample.Items), groupInt(sample.Total))
	}
	return strconv.Itoa(len(sample.Items))
}

// formatSampleOverflow is one line naming where a sample's left-out rows live,
// largest session first.
func formatSampleOverflow(overflow []db.SessionOverflow) string {
	var named []string
	for i, o := range overflow {
		if i == overflowSessions {
			break
		}
		named = append(named, fmt.Sprintf("+%s more from %s", groupInt(o.Count), o.SessionID))
	}
	if rest := overflow[min(len(overflow), overflowSessions):]; len(rest) > 0 {
		restCount := 0
		for _, o := range rest {
			restCount += o.Count
		}
		named = append(named, fmt.Sprintf("+%s more from %d other sessions", groupInt(restCount), len(rest)))
	}
	return "  " + strings.Join(named, " · ") + "\n" +
		"    (recall__list_stored to browse; recall__forget session_id clears a session, force: true for pinned)"
}

type ContextArgs struct {
	Days  int
	Limit int
}

func Context(database *sql.DB, projectKey string, args ContextArgs) string {
	data := db.GetContext(database, projectKey, args.Days, args.Limit)
	today := time.Now().UTC().Format("2006-01-02")

	isEmpty := len(data.Pinned.Items) == 0 && len(data.Notes) == 0 && len(data.Recent) == 0 &&
		len(data.Hot) == 0 && data.LastSession == nil
	if isEmpty {
		return ContextEmptyResponse
	}

	lines := []string{
		"Context — " + today,
		strings.Repeat("═", 36),
		"Generated " + format.RelativeTime(0),
	}

	if len(data.Pinned.Items) > 0 {
		lines = append(lines, "", fmt.Sprintf("Pinned (%s):", formatSampleCount(data.Pinned)))
		for _, item := range data.Pinned.Items {
			excerpt := oneline(firstChars(item.Summary, contextExcerptLen))
			ellipsis := ""
			if runeLen(item.Summary) > contextExcerptLen {
				ellipsis = "…"
			}
			lines = append(lines, fmt.Sprintf("  📌 %s  %s  %s", item.ID, padEnd(item.ToolName, listToolColWidth), formatDate(item.CreatedAt)))
			lines = append(lines, "    "+excerpt+ellipsis)
		}
		if len(data.Pinned.Overflow) > 0 {
			lines = append(lines, formatSampleOverflow(data.Pinned.Overflow))
		}
	}

	if len(data.Notes) > 0 {
		lines = append(lines, "", fmt.Sprintf("Notes (%d):", len(data.Notes)))
		for _, note := range data.Notes {
			excerpt := oneline(firstChars(note.Summary, 100))
			ellipsis := ""
			if runeLen(note.Summary) > 100 {
				ellipsis = "…"
			}
			lines = append(lines, fmt.Sprintf("  %s  %s", note.ID, formatDate(note.CreatedAt)))
			lines = append(lines, "    "+excerpt+ellipsis)
		}
	}

	if len(data.Recent) > 0 {
		days := args.Days
		if days == 0 {
			days = 7
		}
		lines = append(lines, "", fmt.Sprintf("Recently accessed (last %d day%s, %d item%s):", days, plural(days), len(data.Recent), plural(len(data.Recent))))
		for _, item := range data.Recent {
			excerpt := oneline(firstChars(item.Summary, contextExcerptLen))
			ellipsis := ""
			if runeLen(item.Summary) > contextExcerptLen {
				ellipsis = "…"
			}
			lines = append(lines, fmt.Sprintf("  %s  %s  %s  ×%d", item.ID, padEnd(item.ToolName, listToolColWidth), formatDate(item.CreatedAt), item.AccessCount))
			lines = append(lines, "    "+excerpt+ellipsis)
		}
	}

	if len(data.Hot) > 0 {
		date := ""
		if data.LastSession != nil {
			date = data.LastSession.Date
		}
		lines = append(lines, "", fmt.Sprintf("Hot from last session (%s, %d item%s):", date, len(data.Hot), plural(len(data.Hot))))
		for _, item := range data.Hot {
			excerpt := oneline(firstChars(item.Summary, contextExcerptLen))
			ellipsis := ""
			if runeLen(item.Summary) > contextExcerptLen {
				ellipsis = "…"
			}
			lines = append(lines, fmt.Sprintf("  %s  %s  %s  ×%d", item.ID, padEnd(item.ToolName, listToolColWidth), formatDate(item.CreatedAt), item.AccessCount))
			lines = append(lines, "    "+excerpt+ellipsis)
		}
	}

	if data.LastSession != nil {
		s := data.LastSession
		lines = append(lines, "", fmt.Sprintf("Last session (%s):", s.Date))
		lines = append(lines, fmt.Sprintf("  %d item%s stored · %s → %s (%s reduction)", s.StoredCount, plural(s.StoredCount),
			format.Bytes(s.TotalOriginalBytes), format.Bytes(s.TotalSummaryBytes), reductionPct(s.TotalOriginalBytes, s.TotalSummaryBytes)))
	}

	return strings.Join(lines, "\n")
}

// ── recall__session_summary ───────────────────────────────────────────────────

type SessionSummaryArgs struct {
	SessionID string
	Date      string
}

func SessionSummary(database *sql.DB, projectKey string, args SessionSummaryArgs) string {
	data := db.GetSessionSummary(database, projectKey, db.SessionSummaryOptions{SessionID: args.SessionID, Date: args.Date})
	if data.StoredCount == 0 {
		return fmt.Sprintf("[recall: no items stored for %s]", data.Label)
	}

	accessPlural := "es"
	if data.TotalAccesses == 1 {
		accessPlural = ""
	}
	lines := []string{
		"Session Summary — " + data.Label,
		strings.Repeat("─", 36),
		fmt.Sprintf("Stored: %d item%s · %s → %s (%s reduction)", data.StoredCount, plural(data.StoredCount),
			format.Bytes(data.TotalOriginalBytes), format.Bytes(data.TotalSummaryBytes), reductionPct(data.TotalOriginalBytes, data.TotalSummaryBytes)),
		fmt.Sprintf("Retrieved: %d item%s · %d total access%s", data.AccessedCount, plural(data.AccessedCount), data.TotalAccesses, accessPlural),
		"",
		"Tools stored:",
	}

	const topTools = 5
	for i, t := range data.ToolCounts {
		if i >= topTools {
			break
		}
		lines = append(lines, fmt.Sprintf("  %s×%d", padEnd(t.ToolName, 44), t.Count))
	}
	if len(data.ToolCounts) > topTools {
		lines = append(lines, fmt.Sprintf("  + %d more", len(data.ToolCounts)-topTools))
	}

	if len(data.TopAccessed) > 0 {
		lines = append(lines, "", "Most accessed:")
		for _, item := range data.TopAccessed {
			excerpt := oneline(firstChars(item.Summary, contextExcerptLen))
			ellipsis := ""
			if runeLen(item.Summary) > contextExcerptLen {
				ellipsis = "…"
			}
			lines = append(lines, fmt.Sprintf("  %s (×%d) %s", item.ID, item.AccessCount, item.ToolName))
			lines = append(lines, "    "+excerpt+ellipsis)
		}
	}

	if len(data.Pinned.Items) > 0 {
		lines = append(lines, "", "Pinned: "+formatSampleCount(data.Pinned))
		for _, item := range data.Pinned.Items {
			excerpt := oneline(firstChars(item.Summary, contextExcerptLen))
			ellipsis := ""
			if runeLen(item.Summary) > contextExcerptLen {
				ellipsis = "…"
			}
			lines = append(lines, fmt.Sprintf("  📌 %s  %s", item.ID, item.ToolName))
			lines = append(lines, "    "+excerpt+ellipsis)
		}
		if len(data.Pinned.Overflow) > 0 {
			lines = append(lines, formatSampleOverflow(data.Pinned.Overflow))
		}
	}

	if len(data.Notes.Items) > 0 {
		lines = append(lines, "", "Notes: "+formatSampleCount(data.Notes))
		for _, note := range data.Notes.Items {
			excerpt := oneline(firstChars(note.Summary, 100))
			ellipsis := ""
			if runeLen(note.Summary) > 100 {
				ellipsis = "…"
			}
			lines = append(lines, fmt.Sprintf("  %s — %s%s", note.ID, excerpt, ellipsis))
		}
		if len(data.Notes.Overflow) > 0 {
			lines = append(lines, formatSampleOverflow(data.Notes.Overflow))
		}
	}

	return strings.Join(lines, "\n")
}

// ── recall__stats ─────────────────────────────────────────────────────────────

type StatsArgs struct {
	PinThreshold int
	StaleDays    int
}

func Stats(database *sql.DB, projectKey string, args StatsArgs) string {
	stats := db.GetStats(database, projectKey)
	sessionDays := db.GetSessionDays(database)

	if stats.TotalItems == 0 && stats.NoteItems == 0 {
		return "[recall: no data stored for this project yet]"
	}

	// Savings figures cover intercepted tool output only; recall__note memory is
	// reported on its own line so a bulk note backend can't dilute them.
	lines := []string{"Session stats for current project:"}
	if stats.TotalItems > 0 {
		saved := stats.TotalOriginalBytes - stats.TotalSummaryBytes
		reductionPctVal := toFixed((1-stats.CompressionRatio)*100, 1)
		tokensSaved := saved / 4
		lines = append(lines,
			fmt.Sprintf("  Intercepted items: %d", stats.TotalItems),
			fmt.Sprintf("  Original size:     %s", format.Bytes(stats.TotalOriginalBytes)),
			fmt.Sprintf("  Compressed size:   %s", format.Bytes(stats.TotalSummaryBytes)),
			fmt.Sprintf("  Saved:             %s (%s%% reduction)", format.Bytes(saved), reductionPctVal),
			fmt.Sprintf("  ~Tokens saved:     ~%s", groupInt(tokensSaved)),
		)
	} else {
		lines = append(lines, "  Intercepted items: 0 (no tool output compressed yet)")
	}
	lines = append(lines, fmt.Sprintf("  Session days:      %d", len(sessionDays)))
	if stats.NoteItems > 0 {
		noteWord := "items"
		if stats.NoteItems == 1 {
			noteWord = "item"
		}
		lines = append(lines, fmt.Sprintf("  Notes/memory:      %d %s (%s) — stored memory, not interception",
			stats.NoteItems, noteWord, format.Bytes(stats.NoteBytes)))
	}

	// Pin-budget awareness: pinned items are exempt from eviction and bounded
	// separately by store.max_pinned_mb, which recall__pin enforces at pin time.
	// Report usage against that cap (an existing store may already be over it).
	if stats.PinnedItems > 0 {
		maxPinnedMB := config.Load().Store.MaxPinnedMB
		maxBytes := maxPinnedMB * 1024 * 1024
		capPct := 0.0
		if maxBytes > 0 {
			capPct = float64(stats.PinnedBytes) / maxBytes * 100
		}
		itemWord := "items"
		if stats.PinnedItems == 1 {
			itemWord = "item"
		}
		lines = append(lines, fmt.Sprintf("  Pinned:            %d %s (%s, %s%% of max_pinned_mb)",
			stats.PinnedItems, itemWord, format.Bytes(stats.PinnedBytes), toFixed(capPct, 0)))
		if capPct >= pinBudgetWarnPct {
			lines = append(lines, fmt.Sprintf("  ⚠ Pinned data is %s%% of the %g MB store.max_pinned_mb cap."+
				" New pins are refused once it is full — unpin or raise the cap to make room.",
				toFixed(capPct, 0), maxPinnedMB))
		}
	}

	breakdown := db.GetToolBreakdown(database, projectKey)
	if len(breakdown) > 0 {
		lines = append(lines, "", "By tool (sorted by original size):")
		maxLen := 0
		for _, r := range breakdown {
			if l := runeLen(r.ToolName); l > maxLen {
				maxLen = l
			}
		}
		colW := maxLen
		if colW > 40 {
			colW = 40
		}
		for _, row := range breakdown {
			reduction := " —"
			if row.OriginalBytes > 0 {
				reduction = fmt.Sprintf("%d%%", int(math.Round((1-float64(row.SummaryBytes)/float64(row.OriginalBytes))*100)))
			}
			itemWord := "s"
			if row.Items == 1 {
				itemWord = " "
			}
			lines = append(lines, fmt.Sprintf("  %s  %s item%s  %s → %s  %s",
				padEnd(row.ToolName, colW), padStart(strconv.Itoa(row.Items), 4), itemWord,
				padStart(format.Bytes(row.OriginalBytes), 8), padEnd(format.Bytes(row.SummaryBytes), 8), padStart(reduction, 4)))
		}
	}

	// Per-command breakdown for Bash — attributes the aggregate Bash figure to
	// command families so low-reduction rows (the compression leaks) are
	// visible (upstream #251).
	cmdBreakdown := db.GetBashCommandBreakdown(database, projectKey)
	if len(cmdBreakdown) > 0 {
		lines = append(lines, "", "By Bash command (sorted by original size):")
		colW := 0
		for _, r := range cmdBreakdown {
			if l := runeLen(r.CommandFP); l > colW {
				colW = l
			}
		}
		if colW > 40 {
			colW = 40
		}
		for _, row := range cmdBreakdown {
			reduction := " —"
			if row.OriginalBytes > 0 {
				reduction = fmt.Sprintf("%d%%", int(math.Round((1-float64(row.SummaryBytes)/float64(row.OriginalBytes))*100)))
			}
			itemWord := "s"
			if row.Items == 1 {
				itemWord = " "
			}
			lines = append(lines, fmt.Sprintf("  %s  %s item%s  %s → %s  %s",
				padEnd(row.CommandFP, colW), padStart(strconv.Itoa(row.Items), 4), itemWord,
				padStart(format.Bytes(row.OriginalBytes), 8), padEnd(format.Bytes(row.SummaryBytes), 8), padStart(reduction, 4)))
		}
	}

	sug := db.GetSuggestions(database, projectKey, db.SuggestionsOptions{PinThreshold: args.PinThreshold, StaleDays: args.StaleDays})
	if len(sug.PinCandidates) > 0 || len(sug.StaleCandidates) > 0 {
		lines = append(lines, "", "Suggestions:")
		if len(sug.PinCandidates) > 0 {
			lines = append(lines, "  📌 Consider pinning:")
			for _, item := range sug.PinCandidates {
				lines = append(lines, fmt.Sprintf("     %s  %s  accessed %d×", item.ID, padEnd(item.ToolName, listToolColWidth), item.AccessCount))
			}
		}
		if len(sug.StaleCandidates) > 0 {
			if len(sug.PinCandidates) > 0 {
				lines = append(lines, "")
			}
			lines = append(lines, "  🗑  Never accessed (consider forgetting):")
			now := time.Now().Unix()
			for _, item := range sug.StaleCandidates {
				ageDays := int((now - item.CreatedAt) / 86400)
				lines = append(lines, fmt.Sprintf("     %s  %s  created %d day%s ago", item.ID, padEnd(item.ToolName, listToolColWidth), ageDays, plural(ageDays)))
			}
		}
	}

	return strings.Join(lines, "\n")
}

// ── recall__suggest ───────────────────────────────────────────────────────────

type SuggestArgs struct {
	PinThreshold int
	StaleDays    int
	Limit        int
}

func Suggest(database *sql.DB, projectKey string, args SuggestArgs) string {
	cfg := config.Load()
	pinThreshold := args.PinThreshold
	if pinThreshold == 0 {
		pinThreshold = cfg.Store.PinRecommendationThreshold
	}
	staleDays := args.StaleDays
	if staleDays == 0 {
		staleDays = cfg.Store.StaleItemDays
	}
	sug := db.GetSuggestions(database, projectKey, db.SuggestionsOptions{PinThreshold: pinThreshold, StaleDays: staleDays, Limit: args.Limit})

	if len(sug.PinCandidates) == 0 && len(sug.StaleCandidates) == 0 {
		return "[recall: no suggestions — no frequently accessed unpinned items and no stale items]"
	}

	lines := []string{"Recall suggestions:"}
	if len(sug.PinCandidates) > 0 {
		lines = append(lines, "", "Pin candidates (frequently accessed, not yet pinned):")
		for _, item := range sug.PinCandidates {
			excerpt := oneline(firstChars(item.Summary, 80))
			ellipsis := ""
			if runeLen(item.Summary) > 80 {
				ellipsis = "…"
			}
			lines = append(lines, fmt.Sprintf("  %s  (accessed %d×)  %s", item.ID, item.AccessCount, item.ToolName))
			lines = append(lines, "    "+excerpt+ellipsis)
			lines = append(lines, fmt.Sprintf(`    → recall__pin id="%s"`, item.ID))
		}
	}
	if len(sug.StaleCandidates) > 0 {
		lines = append(lines, "", "Stale items (never accessed, consider forgetting):")
		now := time.Now().Unix()
		for _, item := range sug.StaleCandidates {
			ageDays := int((now - item.CreatedAt) / 86400)
			excerpt := oneline(firstChars(item.Summary, 80))
			ellipsis := ""
			if runeLen(item.Summary) > 80 {
				ellipsis = "…"
			}
			lines = append(lines, fmt.Sprintf("  %s  (%d day%s old)  %s", item.ID, ageDays, plural(ageDays), item.ToolName))
			lines = append(lines, "    "+excerpt+ellipsis)
			lines = append(lines, fmt.Sprintf(`    → recall__forget id="%s"`, item.ID))
		}
	}

	return strings.Join(lines, "\n")
}
