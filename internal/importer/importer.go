// Package importer implements `mcp-recall import`: restore items from a
// recall__export JSON dump into the current project's database. Ports
// src/import/index.ts.
//
// Imported rows are always stamped with the current project's key so they are
// reachable through the project-scoped tool layer. The former
// `--keep-project-key` flag is rejected (upstream #226) — see HandleImport.
//
// Rows carrying a credential are withheld and reported rather than written —
// see partitionSecrets (upstream #273). This is the only secret scan on the
// import path.
package importer

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mcprecall/internal/db"
	"mcprecall/internal/projectkey"
	"mcprecall/internal/secrets"
)

const (
	largeFileBytes      = 50 * 1024 * 1024
	emptyExportSentinel = "[recall: no items to export]"
)

type importRow struct {
	ID           string  `json:"id"`
	ProjectKey   string  `json:"project_key"`
	SessionID    string  `json:"session_id"`
	ToolName     string  `json:"tool_name"`
	Summary      string  `json:"summary"`
	FullContent  string  `json:"full_content"`
	OriginalSize int     `json:"original_size"`
	SummarySize  int     `json:"summary_size"`
	CreatedAt    int64   `json:"created_at"`
	Pinned       int     `json:"pinned"`
	AccessCount  int     `json:"access_count"`
	LastAccessed *int64  `json:"last_accessed"`
	InputHash    *string `json:"input_hash"`
	// FullRetained is optional for backward-compat with dumps predating
	// store.retention: an older dump has no flag and its rows all carry bodies,
	// so a missing value defaults to retained (1).
	FullRetained *int `json:"full_retained"`
	// CommandFP is optional for dumps predating per-command attribution
	// (upstream #251): missing → NULL, reported as "unknown".
	CommandFP *string `json:"command_fp"`
	// DeliveredSize is optional for dumps predating upstream #319: missing →
	// NULL, and savings figures fall back to summary_size for that row.
	DeliveredSize *int64 `json:"delivered_size"`
}

func (r importRow) validate(i int) []string {
	var issues []string
	req := func(name, val string) {
		if val == "" {
			issues = append(issues, fmt.Sprintf("[%d.%s] required", i, name))
		}
	}
	req("id", r.ID)
	req("project_key", r.ProjectKey)
	req("session_id", r.SessionID)
	req("tool_name", r.ToolName)
	if r.OriginalSize < 0 || r.SummarySize < 0 || r.AccessCount < 0 {
		issues = append(issues, fmt.Sprintf("[%d] sizes/counts must be non-negative", i))
	}
	if r.CreatedAt <= 0 {
		issues = append(issues, fmt.Sprintf("[%d.created_at] must be positive", i))
	}
	if r.Pinned < 0 || r.Pinned > 1 {
		issues = append(issues, fmt.Sprintf("[%d.pinned] must be 0 or 1", i))
	}
	if r.DeliveredSize != nil && *r.DeliveredSize < 0 {
		issues = append(issues, fmt.Sprintf("[%d.delivered_size] must be non-negative", i))
	}
	if r.FullRetained != nil && (*r.FullRetained < 0 || *r.FullRetained > 1) {
		issues = append(issues, fmt.Sprintf("[%d.full_retained] must be 0 or 1", i))
	}
	return issues
}

// toStored stamps the row with projectKey — always the current project's key,
// never the dump's, so the row is reachable and deletable through the
// project-scoped tool layer (upstream #226).
func (r importRow) toStored(projectKey string) db.StoredOutput {
	fullRetained := 1
	if r.FullRetained != nil {
		fullRetained = *r.FullRetained
	}
	// Enforce StoreOutput's invariant: a summary-only row carries no body. A
	// legitimate export already has "" here, but a malformed dump may not, and
	// effective-size accounting would then under-count it (upstream #247).
	fullContent := r.FullContent
	if fullRetained == 0 {
		fullContent = ""
	}
	return db.StoredOutput{
		ID: r.ID, ProjectKey: projectKey, SessionID: r.SessionID, ToolName: r.ToolName,
		Summary: r.Summary, FullContent: fullContent, OriginalSize: r.OriginalSize,
		SummarySize: r.SummarySize, CreatedAt: r.CreatedAt, Pinned: r.Pinned,
		AccessCount: r.AccessCount, LastAccessed: r.LastAccessed, InputHash: r.InputHash,
		FullRetained: fullRetained, CommandFP: r.CommandFP, DeliveredSize: r.DeliveredSize,
	}
}

type result struct{ imported, skipped, overwritten int }

// partitionSecrets splits dump rows into those safe to write and those carrying
// a credential. Import reaches storage through its own INSERT, bypassing the
// PostToolUse hook, so this is the only secret scan on the path (upstream
// #273). It runs before both the dry-run counter and the real insert so a
// dry run cannot disagree with the run it predicts. One bad row must not fail a
// large restore, so rows are skipped and reported — pattern names only, never
// the matched value.
func partitionSecrets(items []importRow) (clean []importRow, withheld int, patterns []string) {
	seen := map[string]bool{}
	for _, item := range items {
		// Scan the body even for full_retained=0, whose body the insert drops: a
		// tampered dump can pair that flag with a body, and a credential only in
		// the summary is just as unsafe to store.
		found := secrets.Find(item.Summary + "\n" + item.FullContent)
		if len(found) == 0 {
			clean = append(clean, item)
			continue
		}
		withheld++
		for _, name := range found {
			if !seen[name] {
				seen[name] = true
				patterns = append(patterns, name)
			}
		}
	}
	sort.Strings(patterns)
	return clean, withheld, patterns
}

// HandleImport implements the import CLI command.
func HandleImport(args []string) {
	overwrite := has(args, "--overwrite")
	dryRun := has(args, "--dry-run")

	if msg := keepProjectKeyRejection(args); msg != "" {
		fmt.Fprintln(os.Stderr, msg)
		os.Exit(1)
	}

	var rawPath string
	for _, a := range args {
		if !strings.HasPrefix(a, "--") {
			rawPath = a
			break
		}
	}

	var raw string
	if rawPath != "" {
		fp, _ := filepath.Abs(rawPath)
		if fi, err := os.Stat(fp); err == nil && fi.Size() > largeFileBytes {
			fmt.Fprintf(os.Stderr, "Warning: file is %d MB — this may take a while.\n", fi.Size()/1024/1024)
		}
		data, err := os.ReadFile(fp)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Cannot read file: %s\n", fp)
			os.Exit(1)
		}
		raw = string(data)
	} else {
		data, err := io.ReadAll(os.Stdin)
		if err != nil || len(data) == 0 {
			fmt.Fprintln(os.Stderr, "No file specified and stdin is not readable.")
			fmt.Fprintln(os.Stderr, "Usage: mcprecall import <file.json> [--overwrite] [--dry-run]")
			os.Exit(1)
		}
		raw = string(data)
	}

	if strings.HasPrefix(strings.TrimLeft(raw, " \t\r\n"), emptyExportSentinel) {
		fmt.Println("Nothing to import (empty export).")
		return
	}

	var items []importRow
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		fmt.Fprintln(os.Stderr, "Invalid JSON input.")
		os.Exit(1)
	}
	var allIssues []string
	for i, it := range items {
		allIssues = append(allIssues, it.validate(i)...)
	}
	if len(allIssues) > 0 {
		fmt.Fprintln(os.Stderr, "Input does not look like a recall__export dump:")
		for i, issue := range allIssues {
			if i >= 5 {
				break
			}
			fmt.Fprintf(os.Stderr, "  %s\n", issue)
		}
		os.Exit(1)
	}
	if len(items) == 0 {
		fmt.Println("Nothing to import (empty export).")
		return
	}

	projectKey := projectkey.Key(mustGetwd())
	dbPath := db.DefaultDBPath(projectKey)

	clean, withheld, patterns := partitionSecrets(items)

	fmt.Printf("\nImporting %d item(s) into %s\n", len(clean), dbPath)
	if dryRun {
		fmt.Print("(dry run — nothing will be written)\n\n")
	}

	// On stderr so the warning survives a redirected stdout. Worded apart from
	// the "skipped (already exist)" count — both can occur in one run.
	if withheld > 0 {
		if dryRun {
			fmt.Fprintf(os.Stderr, "Would withhold %d row(s) containing secrets (%s). They would not be imported.\n",
				withheld, strings.Join(patterns, ", "))
		} else {
			fmt.Fprintf(os.Stderr, "Withheld %d row(s) containing secrets (%s). They were NOT imported.\n",
				withheld, strings.Join(patterns, ", "))
		}
	}

	var res result
	if dryRun {
		res = dryRunCount(dbPath, clean, overwrite)
	} else {
		res = importItems(dbPath, clean, overwrite, projectKey)
	}

	var parts []string
	if res.imported > 0 {
		parts = append(parts, fmt.Sprintf("%d imported", res.imported))
	}
	if res.overwritten > 0 {
		parts = append(parts, fmt.Sprintf("%d overwritten", res.overwritten))
	}
	if res.skipped > 0 {
		parts = append(parts, fmt.Sprintf("%d skipped (already exist — use --overwrite to replace)", res.skipped))
	}
	if len(parts) > 0 {
		fmt.Println(strings.Join(parts, ", ") + ".")
	} else {
		fmt.Println("Nothing imported.")
	}

	if !dryRun && res.imported+res.overwritten > 0 {
		fmt.Println("\nNext steps:")
		fmt.Println("  recall__search <query>   — verify content is searchable")
		fmt.Println("  recall__list_stored      — browse imported items")
	}
}

func dryRunCount(dbPath string, items []importRow, overwrite bool) result {
	if _, err := os.Stat(dbPath); err != nil {
		return result{imported: len(items)}
	}
	database, err := db.Open(dbPath)
	if err != nil || !db.HasStoredTable(database) {
		if database != nil {
			database.Close()
		}
		return result{imported: len(items)}
	}
	defer database.Close()
	var r result
	for _, item := range items {
		if db.HasID(database, item.ID) {
			if overwrite {
				r.overwritten++
			} else {
				r.skipped++
			}
		} else {
			r.imported++
		}
	}
	return r
}

// keepProjectKeyRejection returns the error text for a removed
// `--keep-project-key` flag, or "" when it is absent.
//
// The flag stamped rows with the dump's original project key while still
// writing them to the *current* project's database — where every project-scoped
// path (search, list_stored, forget, size accounting) filters on the current
// key. The rows were therefore unreachable and undeletable through the tool
// layer. Reject it loudly rather than silently re-stamping, so a caller who
// relied on it learns why (upstream #226).
func keepProjectKeyRejection(args []string) string {
	if !has(args, "--keep-project-key") {
		return ""
	}
	return "The --keep-project-key flag was removed (#226): it wrote rows into the current\n" +
		"project's database while stamping them with the dump's original key, leaving them\n" +
		"unreachable by search/list_stored/forget and invisible to the size cap.\n" +
		"Run `mcprecall import <file>` without it — items land in the current project and\n" +
		"behave normally. To reach rows already stranded by the old flag, pass an explicit\n" +
		"project_key to recall__list_stored / recall__forget."
}

func importItems(dbPath string, items []importRow, overwrite bool, targetKey string) result {
	database, err := db.Open(dbPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer database.Close()
	var r result
	for _, item := range items {
		exists := db.HasID(database, item.ID)
		if exists {
			if !overwrite {
				r.skipped++
				continue
			}
			db.DeleteByID(database, item.ID)
		}
		if err := db.InsertFull(database, item.toStored(targetKey)); err != nil {
			continue
		}
		if exists {
			r.overwritten++
		} else {
			r.imported++
		}
	}
	return r
}

func has(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

func mustGetwd() string {
	cwd, _ := os.Getwd()
	return cwd
}
