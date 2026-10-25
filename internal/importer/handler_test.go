// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/importer/handler_test.go

package importer

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"mcprecall/internal/db"
	"mcprecall/internal/tools"
)

// handler round-trips through export → import (upstream #341).
func TestImportRoundTripsHandler(t *testing.T) {
	src, _ := db.Open(":memory:")
	defer src.Close()
	handler := "shellHandler"
	db.StoreOutput(src, db.StoreInput{ProjectKey: srcProject, SessionID: "s", ToolName: "Bash",
		Summary: "d", FullContent: "body", OriginalSize: 100, Handler: &handler})
	var rows []importRow
	if err := json.Unmarshal([]byte(tools.Export(src, srcProject)), &rows); err != nil {
		t.Fatalf("export is not importable JSON: %v", err)
	}
	dbPath := filepath.Join(t.TempDir(), "t.db")
	if res := importItems(dbPath, rows, false, "k"); res.imported != 1 {
		t.Fatalf("imported = %d", res.imported)
	}
	got := query(t, dbPath, "SELECT handler FROM stored_outputs WHERE tool_name = 'Bash'")
	if len(got) != 1 || got[0][0] != "shellHandler" {
		t.Errorf("handler after import = %v", got)
	}
}
