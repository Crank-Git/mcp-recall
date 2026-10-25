// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/discover/discover.go

// Package discover reports which tools and Bash commands land on a fallback
// handler, by bytes, with their reduction and retrieval rate (upstream #341):
// the data to decide which handler to write next.
//
// Read-only: every store is opened mode=ro, so the report never migrates or
// writes one. A store opened by no session since #341 has no handler column;
// its rows count as "stored before handlers were recorded", like NULL rows.
// Ports src/discover/index.ts.
package discover

import (
	"database/sql"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"mcprecall/internal/db"
	"mcprecall/internal/format"
)

// DefaultLimit is how many fallback routes the report lists by default.
const DefaultLimit = 10

// Report is the discover result for one or more stores.
type Report struct {
	Routes   []db.FallbackRouteRow
	Handlers []db.HandlerBreakdownRow
	// Unrecorded counts intercepted rows with no recorded handler: stored
	// before upstream #341.
	Unrecorded int
	Stores     int
}

// Options configures a discover run.
type Options struct {
	All   bool
	Since int64 // Unix seconds; 0 for no lower bound
	Limit int   // 0 for DefaultLimit
}

func hasHandlerColumn(database *sql.DB) bool {
	var n int
	err := database.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('stored_outputs') WHERE name = 'handler'`).Scan(&n)
	return err == nil && n > 0
}

// ReadStore returns the report for one store file, across every project key
// it holds. An unreadable or locked store is left out rather than failing the
// report.
func ReadStore(file string, opts db.HandlerQueryOptions) Report {
	uri := (&url.URL{Scheme: "file", Path: file, RawQuery: "mode=ro"}).String()
	database, err := sql.Open("sqlite", uri)
	if err != nil {
		return Report{}
	}
	defer database.Close()
	database.SetMaxOpenConns(1)

	var one int
	if database.QueryRow(`SELECT 1 FROM sqlite_master WHERE type='table' AND name='stored_outputs'`).Scan(&one) != nil {
		return Report{}
	}
	if !hasHandlerColumn(database) {
		var n int
		if database.QueryRow(`SELECT COUNT(*) FROM stored_outputs WHERE tool_name != 'recall__note' AND created_at >= ?`,
			opts.Since).Scan(&n) != nil {
			return Report{}
		}
		return Report{Unrecorded: n, Stores: 1}
	}

	rows, err := database.Query(`SELECT DISTINCT project_key FROM stored_outputs`)
	if err != nil {
		return Report{}
	}
	var keys []string
	for rows.Next() {
		var k string
		if rows.Scan(&k) == nil {
			keys = append(keys, k)
		}
	}
	rows.Close()

	reports := make([]Report, 0, len(keys))
	for _, key := range keys {
		reports = append(reports, Report{
			Routes:     db.GetFallbackRoutes(database, key, opts),
			Handlers:   db.GetHandlerBreakdown(database, key, opts),
			Unrecorded: db.CountUnrecordedHandler(database, key, opts),
		})
	}
	merged := Merge(reports)
	merged.Stores = 1
	return merged
}

func addCounts(a, b db.HandlerBreakdownRow) db.HandlerBreakdownRow {
	a.Items += b.Items
	a.OriginalBytes += b.OriginalBytes
	a.SummaryBytes += b.SummaryBytes
	a.Retrieved += b.Retrieved
	return a
}

// Merge sums reports from several stores or project keys into one.
func Merge(reports []Report) Report {
	var out Report
	routeIdx := map[string]int{}
	handlerIdx := map[string]int{}
	for _, r := range reports {
		for _, row := range r.Routes {
			fp := ""
			if row.CommandFP != nil {
				fp = *row.CommandFP
			}
			k := row.ToolName + "\x00" + fp + "\x00" + row.Handler
			if i, ok := routeIdx[k]; ok {
				out.Routes[i].HandlerBreakdownRow = addCounts(out.Routes[i].HandlerBreakdownRow, row.HandlerBreakdownRow)
			} else {
				routeIdx[k] = len(out.Routes)
				out.Routes = append(out.Routes, row)
			}
		}
		for _, row := range r.Handlers {
			if i, ok := handlerIdx[row.Handler]; ok {
				out.Handlers[i] = addCounts(out.Handlers[i], row)
			} else {
				handlerIdx[row.Handler] = len(out.Handlers)
				out.Handlers = append(out.Handlers, row)
			}
		}
		out.Unrecorded += r.Unrecorded
		out.Stores += r.Stores
	}
	sort.SliceStable(out.Routes, func(i, j int) bool { return out.Routes[i].OriginalBytes > out.Routes[j].OriginalBytes })
	sort.SliceStable(out.Handlers, func(i, j int) bool { return out.Handlers[i].OriginalBytes > out.Handlers[j].OriginalBytes })
	return out
}

// StoreFiles returns the store files a run covers: the current project's, or
// every one in dataDir with all.
func StoreFiles(all bool, dataDir, currentFile string) []string {
	if !all {
		if _, err := os.Stat(currentFile); err != nil {
			return nil
		}
		return []string{currentFile}
	}
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".db") {
			out = append(out, filepath.Join(dataDir, e.Name()))
		}
	}
	return out
}

func pct(part, whole int) string {
	if whole <= 0 {
		return "—"
	}
	return fmt.Sprintf("%.0f%%", float64(part)/float64(whole)*100)
}

func reduction(row db.HandlerBreakdownRow) string {
	if row.OriginalBytes <= 0 {
		return "—"
	}
	return pct(row.OriginalBytes-row.SummaryBytes, row.OriginalBytes)
}

func routeLabel(row db.FallbackRouteRow) string {
	if row.CommandFP != nil {
		return row.ToolName + ": " + *row.CommandFP
	}
	return row.ToolName
}

// table renders a text table: the first textCols columns left-aligned, the
// figures after them right-aligned.
func table(header []string, rows [][]string, textCols int) []string {
	widths := make([]int, len(header))
	for i, h := range header {
		widths[i] = len([]rune(h))
		for _, r := range rows {
			if l := len([]rune(r[i])); l > widths[i] {
				widths[i] = l
			}
		}
	}
	fmtRow := func(cells []string) string {
		parts := make([]string, len(cells))
		for i, c := range cells {
			pad := strings.Repeat(" ", widths[i]-len([]rune(c)))
			if i < textCols {
				parts[i] = c + pad
			} else {
				parts[i] = pad + c
			}
		}
		return "  " + strings.Join(parts, "  ")
	}
	out := []string{fmtRow(header)}
	for _, r := range rows {
		out = append(out, fmtRow(r))
	}
	return out
}

// FormatReport renders a report, listing at most limit fallback routes.
func FormatReport(report Report, limit int) string {
	if limit <= 0 {
		limit = DefaultLimit
	}
	var lines []string
	shown := report.Routes
	if len(shown) > limit {
		shown = shown[:limit]
	}

	lines = append(lines, "Fallback-routed output, by original size (candidates for a new handler):")
	if len(shown) == 0 {
		lines = append(lines, "  none recorded")
	} else {
		rows := make([][]string, len(shown))
		for i, r := range shown {
			rows[i] = []string{
				routeLabel(r), r.Handler, strconv.Itoa(r.Items), format.Bytes(r.OriginalBytes),
				format.Bytes(r.SummaryBytes), reduction(r.HandlerBreakdownRow), pct(r.Retrieved, r.Items),
			}
		}
		lines = append(lines, table([]string{"tool / command", "handler", "items", "original", "delivered", "reduction", "retrieved"}, rows, 2)...)
		if hidden := len(report.Routes) - len(shown); hidden > 0 {
			lines = append(lines, fmt.Sprintf("  … %d more (--limit to show more)", hidden))
		}
	}

	lines = append(lines, "", "By handler:")
	if len(report.Handlers) == 0 {
		lines = append(lines, "  none recorded")
	} else {
		rows := make([][]string, len(report.Handlers))
		for i, r := range report.Handlers {
			rows[i] = []string{
				r.Handler, strconv.Itoa(r.Items), format.Bytes(r.OriginalBytes),
				format.Bytes(r.SummaryBytes), reduction(r), pct(r.Retrieved, r.Items),
			}
		}
		lines = append(lines, table([]string{"handler", "items", "original", "delivered", "reduction", "retrieved"}, rows, 1)...)
	}

	lines = append(lines, "",
		"retrieved = share fetched at least once with recall__retrieve; a high rate means the summary dropped something needed.")
	if n := report.Unrecorded; n > 0 {
		item, verb := "items", "are"
		if n == 1 {
			item, verb = "item", "is"
		}
		lines = append(lines, fmt.Sprintf("%d %s stored before handlers were recorded (1.17.0) %s not included.", n, item, verb))
	}
	return strings.Join(lines, "\n")
}

var sinceRe = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})$`)

// ParseSince parses a YYYY-MM-DD date (local midnight) to Unix seconds; false
// when malformed or not a real date.
func ParseSince(value string) (int64, bool) {
	if !sinceRe.MatchString(value) {
		return 0, false
	}
	t, err := time.ParseInLocation("2006-01-02", value, time.Local)
	if err != nil {
		return 0, false
	}
	return t.Unix(), true
}

// Run prints the discover report for the current project's store
// (currentFile), or every store in dataDir with opts.All.
func Run(w io.Writer, dataDir, currentFile string, opts Options) {
	files := StoreFiles(opts.All, dataDir, currentFile)
	if len(files) == 0 {
		if opts.All {
			fmt.Fprintln(w, "No stores found.")
		} else {
			fmt.Fprintln(w, "No store for this project yet.")
		}
		return
	}
	reports := make([]Report, len(files))
	for i, f := range files {
		reports[i] = ReadStore(f, db.HandlerQueryOptions{Since: opts.Since})
	}
	report := Merge(reports)
	scope := "this project"
	if opts.All {
		plural := "s"
		if report.Stores == 1 {
			plural = ""
		}
		scope = fmt.Sprintf("%d store%s", report.Stores, plural)
	}
	since := ""
	if opts.Since != 0 {
		since = " since " + time.Unix(opts.Since, 0).Local().Format("2006-01-02")
	}
	fmt.Fprintf(w, "mcp-recall discover · %s%s\n\n", scope, since)
	fmt.Fprintln(w, FormatReport(report, opts.Limit))
}
