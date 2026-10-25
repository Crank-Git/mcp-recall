// Package gc implements `mcprecall gc` — reclaim disk from the per-project
// database store. Ports src/gc/index.ts.
//
// Two independent problems this addresses:
//  1. Orphaned DBs: when a project is deleted, its DB is never reopened, so the
//     session-start prune never runs against it and it lingers forever. gc
//     classifies each DB by whether its recorded project_path still exists.
//  2. Free-page bloat: incremental_vacuum only reclaims on databases created
//     with auto_vacuum=INCREMENTAL. `gc --vacuum` runs a full VACUUM on
//     survivors, which reclaims free pages AND upgrades legacy
//     auto_vacuum=NONE DBs.
//
// Default is a dry run — nothing is deleted without --force. Deletion is
// conservative: only DBs whose project was definitively removed (the recorded
// path is gone but its parent still exists), or DBs we cannot attribute to a
// live project that are also untouched past the stale window (pathless legacy
// DBs, or an un-rootable relative recorded path), are candidates. A path missing
// because its whole volume is unmounted (absolute, so it may return intact), a
// non-mcp-recall .db, or a corrupt DB is never a candidate.
package gc

import (
	"database/sql"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"mcprecall/internal/db"
	"mcprecall/internal/format"
	"mcprecall/internal/logx"
)

// DefaultStaleDays is the age past which a pathless (legacy) database becomes a
// deletion candidate.
const DefaultStaleDays = 90

// Options controls a gc run.
type Options struct {
	// DryRun reports without deleting. Callers should default this to true.
	DryRun bool
	// StaleDays: legacy (no recorded path) DBs untouched longer than this are
	// candidates. Zero means DefaultStaleDays.
	StaleDays int
	// Vacuum runs a full VACUUM on surviving DBs to reclaim free pages.
	Vacuum bool
}

// Status is the classification of a single database file.
type Status string

const (
	// StatusCurrent is the active project's DB — never touched.
	StatusCurrent Status = "current"
	// StatusActive means the recorded path still exists on disk.
	StatusActive Status = "active"
	// StatusOrphaned means the recorded path is gone but its parent exists —
	// the project was deleted, so the DB is safe to remove.
	StatusOrphaned Status = "orphaned"
	// StatusUnverifiable means path AND parent gone (likely an unmounted volume,
	// which may return intact) — deletion can't be confirmed. Never deleted.
	StatusUnverifiable Status = "unverifiable"
	// StatusUnrootedFresh means an un-rootable recorded path (relative/empty),
	// recently modified — kept.
	StatusUnrootedFresh Status = "unrooted-fresh"
	// StatusUnrootedStale means an un-rootable recorded path (relative/empty),
	// untouched past the stale window — a candidate (upstream #214).
	StatusUnrootedStale Status = "unrooted-stale"
	// StatusLegacyFresh means no recorded path, recently modified — kept.
	StatusLegacyFresh Status = "legacy-fresh"
	// StatusLegacyStale means no recorded path, untouched past the stale window.
	StatusLegacyStale Status = "legacy-stale"
	// StatusUnreadable means not an mcp-recall DB, or could not be read.
	// Reported, never deleted.
	StatusUnreadable Status = "unreadable"
)

type policy struct{ deletable, vacuumable bool }

// statusPolicy is the single source of truth for both decisions. Every Status
// constant must appear here; statusPolicyFor panics on a missing entry so a new
// status can never be silently treated as deletable or vacuumable.
var statusPolicy = map[Status]policy{
	StatusCurrent:       {deletable: false, vacuumable: false},
	StatusActive:        {deletable: false, vacuumable: true},
	StatusOrphaned:      {deletable: true, vacuumable: false},
	StatusUnverifiable:  {deletable: false, vacuumable: false},
	StatusUnrootedFresh: {deletable: false, vacuumable: true},
	StatusUnrootedStale: {deletable: true, vacuumable: false},
	StatusLegacyFresh:   {deletable: false, vacuumable: true},
	StatusLegacyStale:   {deletable: true, vacuumable: false},
	StatusUnreadable:    {deletable: false, vacuumable: false},
}

func statusPolicyFor(s Status) policy {
	p, ok := statusPolicy[s]
	if !ok {
		panic("gc: no policy for status " + string(s))
	}
	return p
}

// Entry is one classified database file.
type Entry struct {
	File        string // absolute path to the .db file
	Status      Status
	ProjectPath string // recorded project_path, "" if none
	SizeBytes   int64  // .db + -wal + -shm
	ModTime     time.Time
	Items       int // stored_outputs row count (0 if unavailable)
}

// IsDeletionCandidate reports whether `gc --force` will delete this status.
func IsDeletionCandidate(s Status) bool { return statusPolicyFor(s).deletable }

// VacuumTargets returns the databases --vacuum should rewrite: the ones being
// kept. Excludes deletion candidates (pointless to rewrite a DB slated for
// removal — and in a dry run would otherwise VACUUM the entire store), the
// live-locked current DB, and anything unverifiable/unreadable.
func VacuumTargets(entries []Entry) []Entry {
	var out []Entry
	for _, e := range entries {
		if statusPolicyFor(e.Status).vacuumable {
			out = append(out, e)
		}
	}
	return out
}

func sumBytes(entries []Entry) int64 {
	var n int64
	for _, e := range entries {
		n += e.SizeBytes
	}
	return n
}

func fileSizeSafe(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.Size()
}

// dbFootprint is the total on-disk size of a DB including WAL and shared-memory
// sidecars.
func dbFootprint(file string) int64 {
	return fileSizeSafe(file) + fileSizeSafe(file+"-wal") + fileSizeSafe(file+"-shm")
}

// Footprint is a cheap size/count summary of the store.
type Footprint struct {
	TotalBytes int64
	DBCount    int
}

// StoreFootprint returns the total size and count of the DB store using stat
// only — no database opens — so it is safe to call on every session start. Used
// to decide whether to nudge the user to run gc. Orphan classification (which
// requires opening each DB) is deferred to the gc command itself.
func StoreFootprint(dir string) Footprint {
	names, err := os.ReadDir(dir)
	if err != nil {
		return Footprint{}
	}
	var fp Footprint
	for _, n := range names {
		if !strings.HasSuffix(n.Name(), ".db") {
			continue
		}
		fp.DBCount++
		fp.TotalBytes += dbFootprint(filepath.Join(dir, n.Name()))
	}
	return fp
}

// ReminderText is the one-line session-start nudge to run gc, or "" when the
// store is under the threshold or reminders are disabled (reminderMB <= 0).
// Pure — the caller supplies the footprint from the cheap StoreFootprint.
func ReminderText(fp Footprint, reminderMB float64) string {
	if reminderMB <= 0 {
		return ""
	}
	if float64(fp.TotalBytes) < reminderMB*1024*1024 {
		return ""
	}
	return fmt.Sprintf("💡 recall store is %s across %d project databases — "+
		"run `mcprecall gc` to review and reclaim disk space.",
		format.Bytes(int(fp.TotalBytes)), fp.DBCount)
}

// probe holds a database's classification inputs.
type probe struct {
	readable    bool // opened AND is an mcp-recall DB we could read
	hasPath     bool // a project_path row exists, even an empty one
	projectPath string
	items       int
}

// probeDB opens a DB read-only and reads what classification needs. A file that
// is not an mcp-recall database (no stored_outputs table) or that errors on read
// (corrupt) is reported readable:false so it is never a deletion candidate.
func probeDB(file string) probe {
	// mode=ro so a probe can never create or modify a file; immutable is NOT set
	// because a live DB may have a WAL that must still be read. The path is
	// URL-escaped so `#` or `%` in it isn't parsed as URI syntax.
	uri := (&url.URL{Scheme: "file", Path: file, RawQuery: "mode=ro"}).String()
	database, err := sql.Open("sqlite", uri)
	if err != nil {
		return probe{}
	}
	defer database.Close()
	database.SetMaxOpenConns(1)

	var one int
	err = database.QueryRow(
		`SELECT 1 FROM sqlite_master WHERE type='table' AND name='stored_outputs'`).Scan(&one)
	if err != nil {
		return probe{} // not an mcp-recall DB, or unreadable
	}

	// A missing meta table is a legitimate legacy database, not corruption, so
	// its error is swallowed into "no recorded path" rather than failing the
	// probe. Read directly rather than via db.GetMeta so a recorded "" stays
	// distinct from no row, as upstream's null vs "" does.
	var projectPath string
	hasPath := database.QueryRow(`SELECT value FROM meta WHERE key = 'project_path'`).Scan(&projectPath) == nil

	var items int
	if err := database.QueryRow(`SELECT COUNT(*) FROM stored_outputs`).Scan(&items); err != nil {
		// A failure here means real corruption, not just a missing table.
		return probe{}
	}
	return probe{readable: true, hasPath: hasPath, projectPath: projectPath, items: items}
}

// classify determines one database's status. Split out for exhaustive, testable
// status logic.
func classify(p probe, modTime, staleCutoff time.Time) Status {
	if !p.readable {
		return StatusUnreadable
	}
	if p.hasPath {
		// Checked before any existence test: a relative path is resolved against
		// whatever cwd gc happens to run from, so it would otherwise classify
		// differently per invocation — and "." or a bare name would read as
		// "parent survived, project deleted" (its dir is ".", which always
		// exists) and be destroyed. We cannot know where a relative path was
		// rooted, so it is never deleted on a deleted-project inference — but it
		// should not stay pinned forever either (upstream #214): it falls through
		// to the untouched-past-the-stale-window rule used for pathless DBs.
		if !filepath.IsAbs(p.projectPath) {
			if modTime.Before(staleCutoff) {
				return StatusUnrootedStale
			}
			return StatusUnrootedFresh
		}
		if pathExists(p.projectPath) {
			return StatusActive
		}
		// Path gone: only orphaned if the PARENT still exists (the project dir
		// was really deleted). If the parent is also gone, the volume is likely
		// just unmounted — never delete on that basis.
		if pathExists(filepath.Dir(p.projectPath)) {
			return StatusOrphaned
		}
		return StatusUnverifiable
	}
	if modTime.Before(staleCutoff) {
		return StatusLegacyStale
	}
	return StatusLegacyFresh
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// ScanDatabases inspects every *.db in dir and classifies it. Read-only: no file
// is modified or deleted. currentFile is the active project's DB path (always
// classified current); now is injectable for deterministic tests.
func ScanDatabases(dir, currentFile string, staleDays int, now time.Time) []Entry {
	names, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	staleCutoff := now.AddDate(0, 0, -staleDays)

	// Resolve both sides so a non-normalized RECALL_DB_PATH (e.g. a "/./"
	// segment) still matches the scanned, Join-normalized path — protecting the
	// live DB.
	currentResolved := resolvePath(currentFile)

	var entries []Entry
	for _, n := range names {
		if !strings.HasSuffix(n.Name(), ".db") {
			continue
		}
		file := filepath.Join(dir, n.Name())
		fi, err := os.Stat(file)
		if err != nil {
			continue // vanished between ReadDir and Stat
		}
		size := dbFootprint(file)

		// The active project's DB is always current — never deletable, never
		// vacuumed — whatever the probe returns. It is still probed so the report
		// shows its real path and item count (upstream #265): a live WAL DB reads
		// fine through a second read-only connection.
		p := probeDB(file)
		status := classify(p, fi.ModTime(), staleCutoff)
		if resolvePath(file) == currentResolved {
			status = StatusCurrent
		}
		entries = append(entries, Entry{
			File:        file,
			Status:      status,
			ProjectPath: p.projectPath,
			SizeBytes:   size,
			ModTime:     fi.ModTime(),
			Items:       p.items,
		})
	}

	// Largest first — the biggest reclaimable wins surface at the top.
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].SizeBytes > entries[j].SizeBytes })
	return entries
}

func resolvePath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	return abs
}

var statusLabel = map[Status]string{
	StatusCurrent:       "current",
	StatusActive:        "active",
	StatusOrphaned:      "ORPHANED",
	StatusUnverifiable:  "unverifiable",
	StatusUnrootedFresh: "unrooted",
	StatusUnrootedStale: "UNROOTED-STALE",
	StatusLegacyFresh:   "legacy",
	StatusLegacyStale:   "LEGACY-STALE",
	StatusUnreadable:    "unreadable",
}

func padEnd(s string, n int) string {
	if l := len([]rune(s)); l < n {
		return s + strings.Repeat(" ", n-l)
	}
	return s
}

func padStart(s string, n int) string {
	if l := len([]rune(s)); l < n {
		return strings.Repeat(" ", n-l) + s
	}
	return s
}

func reportLine(e Entry, now time.Time) string {
	flag := " "
	if IsDeletionCandidate(e.Status) {
		flag = "✗"
	}
	age := format.RelativeTime(now.Sub(e.ModTime).Milliseconds())
	where := e.ProjectPath
	if where == "" {
		where = "(no recorded path)"
	}
	return fmt.Sprintf("  %s %s %s  %s items  %s  %s\n      %s",
		flag, padEnd(statusLabel[e.Status], 14), padStart(format.Bytes(int(e.SizeBytes)), 9),
		padStart(fmt.Sprintf("%d", e.Items), 6), padEnd(age, 14), filepath.Base(e.File), where)
}

// VacuumResult reports the outcome of one VACUUM. Err is set on failure, in
// which case Before/After are meaningless.
type VacuumResult struct {
	Before, After int64
	Err           error
}

// VacuumFile runs a full VACUUM: reclaims free pages and upgrades legacy DBs to
// incremental auto-vacuum. Also migrates a legacy FTS index first (upstream
// #263), so stores that are rarely opened still shed their duplicate copy, and
// checks the index afterwards (db.VerifyFTSIndex).
func VacuumFile(file string) VacuumResult {
	before := dbFootprint(file)
	database, err := sql.Open("sqlite", file)
	if err != nil {
		return VacuumResult{Err: err}
	}
	database.SetMaxOpenConns(1)

	if _, err := database.Exec("PRAGMA busy_timeout=5000"); err != nil {
		database.Close()
		logx.Warn(fmt.Sprintf("vacuum failed for %s — %v", filepath.Base(file), err))
		return VacuumResult{Err: err}
	}
	db.MigrateFTSToExternalContent(database)
	for _, stmt := range []string{"PRAGMA auto_vacuum=INCREMENTAL", "VACUUM"} {
		if _, err := database.Exec(stmt); err != nil {
			database.Close()
			// VACUUM is atomic — a failure leaves the DB intact. Log the real
			// cause; the caller derives a user-facing reason from it.
			logx.Warn(fmt.Sprintf("vacuum failed for %s — %v", filepath.Base(file), err))
			return VacuumResult{Err: err}
		}
	}
	db.VerifyFTSIndex(database)
	// A session's MCP server may hold the store open, so the close below would
	// not remove the WAL; truncate it explicitly (upstream #292).
	db.TruncateWAL(database)
	database.Close()
	return VacuumResult{Before: before, After: dbFootprint(file)}
}

// vacuumSkipReason is a best-effort human-readable cause from the raw error.
func vacuumSkipReason(err error) string {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "lock"), strings.Contains(msg, "busy"):
		return "locked by another session"
	case strings.Contains(msg, "disk"), strings.Contains(msg, "full"), strings.Contains(msg, "space"):
		return "disk full"
	default:
		return "unreadable or errored (see RECALL_DEBUG logs)"
	}
}

func deleteDBFiles(file string) {
	for _, f := range []string{file, file + "-wal", file + "-shm"} {
		_ = os.Remove(f)
	}
}

// ExecuteDeletions deletes each candidate's .db plus WAL/SHM sidecars and
// returns the bytes freed.
func ExecuteDeletions(candidates []Entry) int64 {
	for _, e := range candidates {
		deleteDBFiles(e.File)
	}
	return sumBytes(candidates)
}

// Run is the entry point for the gc subcommand: prints a report to w and, unless
// DryRun, reclaims. dir is the store directory and currentFile the live DB.
func Run(w io.Writer, dir, currentFile string, opts Options, now time.Time) {
	staleDays := opts.StaleDays
	if staleDays == 0 {
		staleDays = DefaultStaleDays
	}

	entries := ScanDatabases(dir, currentFile, staleDays, now)
	if len(entries) == 0 {
		fmt.Fprintf(w, "No databases found in %s\n", dir)
		return
	}

	var candidates []Entry
	for _, e := range entries {
		if IsDeletionCandidate(e.Status) {
			candidates = append(candidates, e)
		}
	}

	fmt.Fprintf(w, "Project databases in %s:\n\n", dir)
	for _, e := range entries {
		fmt.Fprintln(w, reportLine(e, now))
	}
	fmt.Fprintf(w, "\n%d databases · %s total · %d reclaimable (%s)\n",
		len(entries), format.Bytes(int(sumBytes(entries))),
		len(candidates), format.Bytes(int(sumBytes(candidates))))
	fmt.Fprintf(w, "  ORPHANED = project path deleted · LEGACY-STALE = no recorded path · "+
		"UNROOTED-STALE = un-rootable relative path — both untouched > %dd (--stale-days N) · "+
		"unverifiable (unmounted volume) / unreadable are never deleted\n", staleDays)

	if opts.DryRun {
		if len(candidates) > 0 {
			fmt.Fprintf(w, "\nDry run — pass --force to delete the %d marked database(s).\n", len(candidates))
		}
	} else {
		freed := ExecuteDeletions(candidates)
		fmt.Fprintf(w, "\nDeleted %d database(s), freed %s.\n", len(candidates), format.Bytes(int(freed)))
	}

	if opts.Vacuum {
		// Vacuum the databases we are KEEPING only (see VacuumTargets). Acts
		// regardless of dry-run: --vacuum is an explicit reclaim action,
		// orthogonal to --force, and VACUUM is non-destructive to data.
		survivors := VacuumTargets(entries)
		fmt.Fprintf(w, "\nVacuuming %d database(s) to keep (%s) — rewrites each file, may take a while…\n",
			len(survivors), format.Bytes(int(sumBytes(survivors))))
		var reclaimed int64
		vacuumed, skipped := 0, 0
		for i, e := range survivors {
			fmt.Fprintf(w, "  [%d/%d] %s (%s)… ", i+1, len(survivors),
				filepath.Base(e.File), format.Bytes(int(e.SizeBytes)))
			result := VacuumFile(e.File)
			if result.Err != nil {
				skipped++
				fmt.Fprintf(w, "skipped — %s\n", vacuumSkipReason(result.Err))
				continue
			}
			freed := result.Before - result.After
			if freed < 0 {
				freed = 0
			}
			reclaimed += freed
			vacuumed++
			fmt.Fprintf(w, "reclaimed %s\n", format.Bytes(int(freed)))
		}
		skipNote := ""
		if skipped > 0 {
			skipNote = fmt.Sprintf(" (%d skipped)", skipped)
		}
		fmt.Fprintf(w, "Vacuumed %d database(s), reclaimed %s of free pages.%s\n",
			vacuumed, format.Bytes(int(reclaimed)), skipNote)
	}
}
