// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/discover/discover_test.go
// Port of upstream tests/discover.test.ts (#341).

package discover

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"mcprecall/internal/db"
)

const key = "discovertest123"

func strp(s string) *string { return &s }

func input(handler *string, commandFP *string, originalSize int) db.StoreInput {
	return db.StoreInput{
		ProjectKey: key, SessionID: "s1", ToolName: "Bash", Summary: strings.Repeat("s", 100),
		FullContent: "body", OriginalSize: originalSize, Handler: handler, CommandFP: commandFP,
	}
}

func openDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	database, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return database
}

func store(t *testing.T, database *sql.DB, in db.StoreInput) db.StoredOutput {
	t.Helper()
	o, err := db.StoreOutput(database, in)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

// storeWithoutHandler drops the handler column and inserts a row, as a store
// that predates #341 holds.
func storeWithoutHandler(t *testing.T, database *sql.DB) {
	t.Helper()
	for _, q := range []string{
		"ALTER TABLE stored_outputs DROP COLUMN handler",
		`INSERT INTO stored_outputs (id, project_key, session_id, tool_name, summary, full_content,
			original_size, summary_size, created_at) VALUES ('recall_old', '` + key + `', 's0', 'Bash', 'x', 'y', 500, 1, 1)`,
	} {
		if _, err := database.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHandlerColumnMigration(t *testing.T) {
	t.Run("adds the column to a store that predates it, leaving existing rows NULL", func(t *testing.T) {
		database := openDB(t, ":memory:")
		defer database.Close()
		storeWithoutHandler(t, database)
		if err := db.InitSchema(database); err != nil {
			t.Fatal(err)
		}
		var h sql.NullString
		if err := database.QueryRow("SELECT handler FROM stored_outputs WHERE id = 'recall_old'").Scan(&h); err != nil || h.Valid {
			t.Errorf("handler = %v, err %v", h, err)
		}
	})
	t.Run("is idempotent", func(t *testing.T) {
		database := openDB(t, ":memory:")
		defer database.Close()
		_ = db.InitSchema(database)
		_ = db.InitSchema(database)
		var n int
		_ = database.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('stored_outputs') WHERE name = 'handler'`).Scan(&n)
		if n != 1 {
			t.Errorf("%d handler columns", n)
		}
	})
}

type routeKey struct {
	tool, fp, handler string
	items, original   int
}

func routeKeys(routes []db.FallbackRouteRow) []routeKey {
	out := make([]routeKey, len(routes))
	for i, r := range routes {
		fp := "<nil>"
		if r.CommandFP != nil {
			fp = *r.CommandFP
		}
		out[i] = routeKey{r.ToolName, fp, r.Handler, r.Items, r.OriginalBytes}
	}
	return out
}

func TestGetFallbackRoutes(t *testing.T) {
	t.Run("ranks fallback-routed tools and command families by original bytes", func(t *testing.T) {
		database := openDB(t, ":memory:")
		defer database.Close()
		store(t, database, input(strp("shellHandler"), strp("ssh"), 9000))
		store(t, database, input(strp("shellHandler"), strp("ssh"), 1000))
		store(t, database, input(strp("shellHandler"), strp("pct"), 4000))
		mcp := input(strp("jsonHandler"), nil, 6000)
		mcp.ToolName = "mcp__x__get"
		store(t, database, mcp)
		got := routeKeys(db.GetFallbackRoutes(database, key, db.HandlerQueryOptions{}))
		want := []routeKey{
			{"Bash", "ssh", "shellHandler", 2, 10000},
			{"mcp__x__get", "<nil>", "jsonHandler", 1, 6000},
			{"Bash", "pct", "shellHandler", 1, 4000},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("leaves out dedicated handlers, notes, and rows with no recorded handler", func(t *testing.T) {
		database := openDB(t, ":memory:")
		defer database.Close()
		store(t, database, input(strp("gitDiffHandler"), strp("git diff"), 50000))
		note := input(strp("genericHandler"), nil, 50000)
		note.ToolName = "recall__note"
		store(t, database, note)
		store(t, database, input(nil, strp("ssh"), 50000))
		store(t, database, input(strp("genericHandler"), strp("cat"), 10))
		routes := db.GetFallbackRoutes(database, key, db.HandlerQueryOptions{})
		if len(routes) != 1 || *routes[0].CommandFP != "cat" {
			t.Errorf("got %+v", routeKeys(routes))
		}
	})
	t.Run("counts an item retrieved once as retrieved, and only those", func(t *testing.T) {
		database := openDB(t, ":memory:")
		defer database.Close()
		a := store(t, database, input(strp("shellHandler"), strp("ssh"), 1000))
		store(t, database, input(strp("shellHandler"), strp("ssh"), 1000))
		_ = db.RecordAccess(database, a.ID)
		_ = db.RecordAccess(database, a.ID)
		r := db.GetFallbackRoutes(database, key, db.HandlerQueryOptions{})[0]
		if r.Items != 2 || r.Retrieved != 1 {
			t.Errorf("items %d retrieved %d", r.Items, r.Retrieved)
		}
	})
	t.Run("labels an untagged Bash row unknown", func(t *testing.T) {
		database := openDB(t, ":memory:")
		defer database.Close()
		store(t, database, input(strp("shellHandler"), nil, 1000))
		if fp := db.GetFallbackRoutes(database, key, db.HandlerQueryOptions{})[0].CommandFP; fp == nil || *fp != "unknown" {
			t.Errorf("fp = %v", fp)
		}
	})
	t.Run("filters by since", func(t *testing.T) {
		database := openDB(t, ":memory:")
		defer database.Close()
		store(t, database, input(strp("shellHandler"), strp("ssh"), 1000))
		_, _ = database.Exec("UPDATE stored_outputs SET created_at = 100")
		store(t, database, input(strp("shellHandler"), strp("pct"), 1000))
		routes := db.GetFallbackRoutes(database, key, db.HandlerQueryOptions{Since: 1000})
		if len(routes) != 1 || *routes[0].CommandFP != "pct" {
			t.Errorf("got %+v", routeKeys(routes))
		}
	})
}

func TestHandlerBreakdown(t *testing.T) {
	database := openDB(t, ":memory:")
	defer database.Close()
	store(t, database, input(strp("gitDiffHandler"), nil, 3000))
	store(t, database, input(strp("shellHandler"), nil, 2000))
	store(t, database, input(nil, nil, 1000))
	note := input(nil, nil, 1000)
	note.ToolName = "recall__note"
	store(t, database, note)
	var names []string
	for _, r := range db.GetHandlerBreakdown(database, key, db.HandlerQueryOptions{}) {
		names = append(names, r.Handler)
	}
	if strings.Join(names, ",") != "gitDiffHandler,shellHandler" {
		t.Errorf("handlers = %v", names)
	}
	// The note is not intercepted output.
	if n := db.CountUnrecordedHandler(database, key, db.HandlerQueryOptions{}); n != 1 {
		t.Errorf("unrecorded = %d", n)
	}
}

func TestReadStore(t *testing.T) {
	t.Run("reads a store that predates the column without migrating it", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "old.db")
		database := openDB(t, file)
		storeWithoutHandler(t, database)
		database.Close()

		r := ReadStore(file, db.HandlerQueryOptions{})
		if len(r.Routes) != 0 || len(r.Handlers) != 0 || r.Unrecorded != 1 || r.Stores != 1 {
			t.Errorf("report = %+v", r)
		}
		after, _ := sql.Open("sqlite", file)
		defer after.Close()
		var n int
		_ = after.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('stored_outputs') WHERE name = 'handler'`).Scan(&n)
		if n != 0 {
			t.Error("the read migrated the store")
		}
	})
	t.Run("covers every project key a store holds", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "multi.db")
		database := openDB(t, file)
		store(t, database, input(strp("shellHandler"), strp("ssh"), 1000))
		other := input(strp("shellHandler"), strp("ssh"), 1000)
		other.ProjectKey = "otherproject456"
		store(t, database, other)
		database.Close()
		if r := ReadStore(file, db.HandlerQueryOptions{}); len(r.Routes) != 1 || r.Routes[0].Items != 2 {
			t.Errorf("report = %+v", r)
		}
	})
	t.Run("returns an empty report for a file that is not a store", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "junk.db")
		junk, _ := sql.Open("sqlite", file)
		_, _ = junk.Exec("CREATE TABLE x (y)")
		junk.Close()
		if r := ReadStore(file, db.HandlerQueryOptions{}); len(r.Routes) != 0 || r.Unrecorded != 0 || r.Stores != 0 {
			t.Errorf("report = %+v", r)
		}
	})
}

func TestMerge(t *testing.T) {
	route := db.FallbackRouteRow{ToolName: "Bash", CommandFP: strp("ssh"), HandlerBreakdownRow: db.HandlerBreakdownRow{
		Handler: "shellHandler", Items: 1, OriginalBytes: 100, SummaryBytes: 10, Retrieved: 1,
	}}
	bigger := route
	bigger.OriginalBytes = 300
	merged := Merge([]Report{
		{Routes: []db.FallbackRouteRow{route}, Handlers: []db.HandlerBreakdownRow{route.HandlerBreakdownRow}, Unrecorded: 2, Stores: 1},
		{Routes: []db.FallbackRouteRow{bigger}, Unrecorded: 3, Stores: 1},
	})
	want := db.HandlerBreakdownRow{Handler: "shellHandler", Items: 2, OriginalBytes: 400, SummaryBytes: 20, Retrieved: 2}
	if len(merged.Routes) != 1 || merged.Routes[0].HandlerBreakdownRow != want {
		t.Errorf("routes = %+v", merged.Routes)
	}
	if merged.Unrecorded != 5 || merged.Stores != 2 {
		t.Errorf("unrecorded %d stores %d", merged.Unrecorded, merged.Stores)
	}
}

func TestFormatReport(t *testing.T) {
	route := db.FallbackRouteRow{ToolName: "Bash", CommandFP: strp("ssh"), HandlerBreakdownRow: db.HandlerBreakdownRow{
		Handler: "shellHandler", Items: 4, OriginalBytes: 10240, SummaryBytes: 2048, Retrieved: 1,
	}}
	t.Run("shows each route with its reduction and retrieval rate", func(t *testing.T) {
		out := FormatReport(Report{Routes: []db.FallbackRouteRow{route}, Handlers: []db.HandlerBreakdownRow{route.HandlerBreakdownRow}, Stores: 1}, 0)
		if !regexp.MustCompile(`Bash: ssh\s+shellHandler\s+4\s+10\.0KB\s+2\.0KB\s+80%\s+25%`).MatchString(out) {
			t.Errorf("report:\n%s", out)
		}
		if strings.Contains(out, "stored before handlers were recorded") {
			t.Error("mentions unrecorded rows")
		}
	})
	t.Run("says so when nothing is recorded, and counts the older rows", func(t *testing.T) {
		out := FormatReport(Report{Unrecorded: 7, Stores: 1}, 0)
		if !strings.Contains(out, "none recorded") || !strings.Contains(out, "7 items stored before handlers were recorded") {
			t.Errorf("report:\n%s", out)
		}
	})
	t.Run("caps the list at the limit and says how many are hidden", func(t *testing.T) {
		var routes []db.FallbackRouteRow
		for _, fp := range []string{"cmd1", "cmd2", "cmd3"} {
			r := route
			r.CommandFP = strp(fp)
			routes = append(routes, r)
		}
		out := FormatReport(Report{Routes: routes, Stores: 1}, 2)
		if !strings.Contains(out, "cmd2") || strings.Contains(out, "cmd3") || !strings.Contains(out, "1 more") {
			t.Errorf("report:\n%s", out)
		}
	})
}

func TestParseSince(t *testing.T) {
	got, ok := ParseSince("2026-10-19")
	if want := time.Date(2026, 10, 19, 0, 0, 0, 0, time.Local).Unix(); !ok || got != want {
		t.Errorf("ParseSince = %d, %v; want %d", got, ok, want)
	}
	for _, bad := range []string{"", "2026-10", "2026-02-30"} {
		if _, ok := ParseSince(bad); ok {
			t.Errorf("ParseSince(%q) accepted", bad)
		}
	}
}
