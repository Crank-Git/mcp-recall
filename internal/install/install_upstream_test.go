// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/install/install_upstream_test.go
// Covers upstream #364 (status checks the registered launch), #365 (install
// keeps user edits to recall's entries), #366 (renamed binary, duplicate
// hooks) and #373 (symlinked configs and file mode).

package install

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"mcprecall/internal/jsonx"
)

func mustParse(t *testing.T, s string) *jsonx.Obj {
	t.Helper()
	v, err := jsonx.ParseString(s)
	if err != nil {
		t.Fatal(err)
	}
	return v.(*jsonx.Obj)
}

func writeJSON(t *testing.T, path, s string) {
	t.Helper()
	if err := writeObj(path, mustParse(t, s)); err != nil {
		t.Fatal(err)
	}
}

func readCompact(t *testing.T, path string) string {
	t.Helper()
	o, err := loadObj(path)
	if err != nil {
		t.Fatal(err)
	}
	return jsonx.Compact(o)
}

func isSymlink(t *testing.T, p string) bool {
	t.Helper()
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode()&os.ModeSymlink != 0
}

// ── #373 symlinks and mode ────────────────────────────────────────────────────

func TestWriteThroughSymlinkKeepsLink(t *testing.T) {
	d := t.TempDir()
	real := filepath.Join(d, "dotfiles", "settings.json")
	writeJSON(t, real, `{"old":true}`)
	link := filepath.Join(d, "settings.json")
	os.Symlink(real, link)
	writeJSON(t, link, `{"updated":true}`)
	if !isSymlink(t, link) {
		t.Error("link replaced by a plain file")
	}
	if got := readCompact(t, real); got != `{"updated":true}` {
		t.Errorf("target = %s", got)
	}
}

func TestWriteThroughDanglingSymlink(t *testing.T) {
	d := t.TempDir()
	real := filepath.Join(d, "dotfiles", "missing.json")
	link := filepath.Join(d, "dangling.json")
	os.Symlink(real, link)
	writeJSON(t, link, `{"created":true}`)
	if !isSymlink(t, link) {
		t.Error("link replaced by a plain file")
	}
	if got := readCompact(t, real); got != `{"created":true}` {
		t.Errorf("target = %s", got)
	}
}

func TestWriteThroughDanglingSymlinkChain(t *testing.T) {
	d := t.TempDir()
	real := filepath.Join(d, "dotfiles", "end.json")
	middle := filepath.Join(d, "middle.json")
	link := filepath.Join(d, "chain.json")
	os.Symlink(real, middle)
	os.Symlink(middle, link)
	writeJSON(t, link, `{"created":true}`)
	if !isSymlink(t, middle) {
		t.Error("middle link replaced")
	}
	if got := readCompact(t, real); got != `{"created":true}` {
		t.Errorf("target = %s", got)
	}
}

func TestWriteKeepsModeWithStaleTemp(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")
	writeJSON(t, path, `{"a":1}`)
	os.Chmod(path, 0o600)
	os.WriteFile(path+".tmp", []byte("stale"), 0o666) // left by a crash
	writeJSON(t, path, `{"a":2}`)
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 600", fi.Mode().Perm())
	}
	if got := readCompact(t, path); got != `{"a":2}` {
		t.Errorf("content = %s", got)
	}
}

func TestWriteKeepsExistingMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")
	writeJSON(t, path, `{"a":1}`)
	os.Chmod(path, 0o600)
	writeJSON(t, path, `{"a":2}`)
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 600", fi.Mode().Perm())
	}
}

func TestSymlinkedClaudeMdStaysSymlink(t *testing.T) {
	d := t.TempDir()
	real := filepath.Join(d, "dotfiles", "CLAUDE.md")
	os.MkdirAll(filepath.Dir(real), 0o755)
	os.WriteFile(real, []byte("# mine\n"), 0o644)
	link := filepath.Join(d, "CLAUDE.md")
	os.Symlink(real, link)
	if _, err := injectClaudeMd(link); err != nil {
		t.Fatal(err)
	}
	if !isSymlink(t, link) {
		t.Error("CLAUDE.md link replaced")
	}
	b, _ := os.ReadFile(real)
	if !strings.Contains(string(b), claudeMdStart) {
		t.Error("block not written through the link")
	}
}

// ── shellWord / shellWords ────────────────────────────────────────────────────

var quotedPaths = []string{
	"/usr/local/bin/mcp-recall",
	"/Users/a b/Application Support/mcp-recall",
	`C:\Program Files\mcp-recall\mcp-recall.exe`,
	`C:\Users\me\bin\mcp-recall.exe`, // no space: the backslashes alone need quoting
	"/opt/it's/mcp-recall",
}

func TestShellWordRoundTripsThroughShell(t *testing.T) {
	if got := shellWord("/usr/local/bin/mcp-recall"); got != "/usr/local/bin/mcp-recall" {
		t.Errorf("plain path quoted: %q", got)
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	for _, p := range quotedPaths {
		out, err := exec.Command(sh, "-c", "printf %s "+shellWord(p)).Output()
		if err != nil || string(out) != p {
			t.Errorf("shell read %q back as %q (%v)", p, out, err)
		}
	}
}

func TestShellWordsReadsBackShellWord(t *testing.T) {
	for _, p := range quotedPaths {
		if got := shellWords(shellWord(p) + " post-tool-use"); !reflect.DeepEqual(got, []string{p, "post-tool-use"}) {
			t.Errorf("shellWords = %q", got)
		}
		if got := shellWords("bun " + shellWord(p) + " session-start"); !reflect.DeepEqual(got, []string{"bun", p, "session-start"}) {
			t.Errorf("shellWords = %q", got)
		}
	}
}

// ── #365 install keeps the user's edits ───────────────────────────────────────

// editedInstall installs as p, then makes the edits a user makes to recall's
// entries.
func editedInstall(t *testing.T, p Paths) {
	t.Helper()
	if _, err := Install(p); err != nil {
		t.Fatal(err)
	}
	root, _ := loadObj(p.ClaudeJSON)
	recall := getObj(getObj(root, "mcpServers"), "recall")
	recall.Set("env", mustParse(t, `{"RECALL_DB_PATH":"/custom/db"}`))
	recall.Set("alwaysAllow", []any{"recall__search"})
	writeObj(p.ClaudeJSON, root)

	settings, _ := loadObj(p.Settings)
	hooks := getObj(settings, "hooks")
	ssHook := entryHooks(getArr(hooks, "SessionStart")[0])[0].(*jsonx.Obj)
	ssHook.Set("timeout", float64(20))
	// Recall's PostToolUse hook is no longer first in its entry, and shares it.
	ptuEntry := getArr(hooks, "PostToolUse")[0].(*jsonx.Obj)
	ptuHook := entryHooks(ptuEntry)[0].(*jsonx.Obj)
	ptuHook.Set("timeout", float64(30))
	ptuEntry.Set("hooks", []any{mustParse(t, `{"type":"command","command":"audit-log"}`), ptuHook})
	writeObj(p.Settings, settings)
}

func TestInstallKeepsServerEditsOnNewLaunch(t *testing.T) {
	p := tempPaths(t)
	editedInstall(t, p)
	p.Binary = "/new/place/mcprecall"
	if _, err := Install(p); err != nil {
		t.Fatal(err)
	}
	root, _ := loadObj(p.ClaudeJSON)
	got := jsonx.Compact(getObj(getObj(root, "mcpServers"), "recall"))
	want := `{"type":"stdio","command":"/new/place/mcprecall","args":["server"],"env":{"RECALL_DB_PATH":"/custom/db"},"alwaysAllow":["recall__search"]}`
	if got != want {
		t.Errorf("server =\n %s\nwant\n %s", got, want)
	}
}

func TestInstallKeepsHookTimeoutAndNeighbours(t *testing.T) {
	p := tempPaths(t)
	editedInstall(t, p)
	p.Binary = "/new/place/mcprecall"
	if _, err := Install(p); err != nil {
		t.Fatal(err)
	}
	settings, _ := loadObj(p.Settings)
	hooks := getObj(settings, "hooks")
	ss, ptu := getArr(hooks, "SessionStart"), getArr(hooks, "PostToolUse")
	if len(ss) != 1 || len(ptu) != 1 {
		t.Fatalf("entries: %d SessionStart, %d PostToolUse", len(ss), len(ptu))
	}
	if got := jsonx.Compact(entryHooks(ss[0])); got != `[{"type":"command","command":"/new/place/mcprecall session-start","timeout":20}]` {
		t.Errorf("SessionStart hooks = %s", got)
	}
	want := `[{"type":"command","command":"audit-log"},{"type":"command","command":"/new/place/mcprecall post-tool-use","timeout":30}]`
	if got := jsonx.Compact(entryHooks(ptu[0])); got != want {
		t.Errorf("PostToolUse hooks = %s", got)
	}
}

func TestInstallLeavesHookAloneWhenNotFirst(t *testing.T) {
	p := tempPaths(t)
	editedInstall(t, p)
	before, _ := os.ReadFile(p.Settings)
	changes, err := Install(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range changes {
		if strings.Contains(c, "hook") {
			t.Errorf("unexpected change: %s", c)
		}
	}
	if after, _ := os.ReadFile(p.Settings); string(after) != string(before) {
		t.Error("settings.json rewritten")
	}
}

// ── #366 renamed binary ───────────────────────────────────────────────────────

func renamedPaths(t *testing.T) Paths {
	t.Helper()
	p := tempPaths(t)
	p.Binary = filepath.Join(t.TempDir(), "mr")
	// Precondition: with "recall" anywhere in the path, these tests prove nothing.
	if strings.Contains(p.Binary, "recall") {
		t.Skipf("renamed-binary path contains \"recall\": %s", p.Binary)
	}
	return p
}

func hookCommands(t *testing.T, p Paths, event string) [][]string {
	t.Helper()
	settings, _ := loadObj(p.Settings)
	out := [][]string{}
	for _, e := range getArr(getObj(settings, "hooks"), event) {
		var cmds []string
		for _, h := range entryHooks(e) {
			cmds = append(cmds, hookCommand(h))
		}
		out = append(out, cmds)
	}
	return out
}

func TestRenamedBinaryInstallTwiceOneHookPerEvent(t *testing.T) {
	p := renamedPaths(t)
	Install(p)
	Install(p)
	if got := hookCommands(t, p, "SessionStart"); !reflect.DeepEqual(got, [][]string{{p.Binary + " session-start"}}) {
		t.Errorf("SessionStart = %q", got)
	}
	if got := hookCommands(t, p, "PostToolUse"); !reflect.DeepEqual(got, [][]string{{p.Binary + " post-tool-use"}}) {
		t.Errorf("PostToolUse = %q", got)
	}
}

func TestInstallRemovesDuplicatesKeepingNeighbour(t *testing.T) {
	p := renamedPaths(t)
	bin := p.Binary
	writeJSON(t, p.ClaudeJSON, `{"mcpServers":{"recall":{"type":"stdio","command":"`+bin+`","args":["server"]}}}`)
	ptuHook := entryHooks(postToolUseEntry(bin))[0]
	shared := jsonx.NewObj()
	shared.Set("matcher", PostToolUseMatcher)
	shared.Set("hooks", []any{mustParse(t, `{"type":"command","command":"audit-log"}`), ptuHook})
	hooks := jsonx.NewObj()
	hooks.Set("SessionStart", []any{sessionStartEntry(bin), sessionStartEntry(bin)})
	hooks.Set("PostToolUse", []any{postToolUseEntry(bin), shared})
	settings := jsonx.NewObj()
	settings.Set("hooks", hooks)
	writeObj(p.Settings, settings)

	if _, err := Install(p); err != nil {
		t.Fatal(err)
	}
	if got := hookCommands(t, p, "SessionStart"); !reflect.DeepEqual(got, [][]string{{bin + " session-start"}}) {
		t.Errorf("SessionStart = %q", got)
	}
	if got := hookCommands(t, p, "PostToolUse"); !reflect.DeepEqual(got, [][]string{{bin + " post-tool-use"}, {"audit-log"}}) {
		t.Errorf("PostToolUse = %q", got)
	}
}

func TestUninstallRenamedBinaryRemovesAllCopies(t *testing.T) {
	p := renamedPaths(t)
	Install(p)
	settings, _ := loadObj(p.Settings)
	hooks := getObj(settings, "hooks")
	ptuEntry := getArr(hooks, "PostToolUse")[0].(*jsonx.Obj)
	ptuEntry.Set("hooks", append(entryHooks(ptuEntry), mustParse(t, `{"type":"command","command":"audit-log"}`)))
	hooks.Set("SessionStart", append(getArr(hooks, "SessionStart"), sessionStartEntry(p.Binary))) // a duplicate from before the fix
	writeObj(p.Settings, settings)

	if _, err := Uninstall(p); err != nil {
		t.Fatal(err)
	}
	if got := hookCommands(t, p, "SessionStart"); len(got) != 0 {
		t.Errorf("SessionStart = %q", got)
	}
	if got := hookCommands(t, p, "PostToolUse"); !reflect.DeepEqual(got, [][]string{{"audit-log"}}) {
		t.Errorf("PostToolUse = %q", got)
	}
}

func TestUninstallFromAnotherBinaryFindsRegisteredRenamedHooks(t *testing.T) {
	p := renamedPaths(t)
	Install(p)
	other := p
	other.Binary = "/opt/other/mcprecall"
	if _, err := Uninstall(other); err != nil {
		t.Fatal(err)
	}
	root, _ := loadObj(p.ClaudeJSON)
	if _, ok := getObj(root, "mcpServers").Get("recall"); ok {
		t.Error("server not removed")
	}
	if got := hookCommands(t, p, "SessionStart"); len(got) != 0 {
		t.Errorf("SessionStart = %q", got)
	}
	if got := hookCommands(t, p, "PostToolUse"); len(got) != 0 {
		t.Errorf("PostToolUse = %q", got)
	}
}

// ── #364 status checks the registered launch ──────────────────────────────────

func binPaths(t *testing.T) Paths {
	t.Helper()
	p := tempPaths(t)
	p.Binary = filepath.Join(t.TempDir(), "bin", "mcprecall")
	os.MkdirAll(filepath.Dir(p.Binary), 0o755)
	os.WriteFile(p.Binary, nil, 0o755)
	return p
}

func missingFiles(r StatusReport) []string {
	var out []string
	for _, f := range r.Files {
		if !f.Exists {
			out = append(out, f.Path)
		}
	}
	return out
}

func TestStatusInstalledWhenRegisteredLaunchExists(t *testing.T) {
	p := binPaths(t)
	Install(p)
	r := Status(p)
	if !r.FilesOK() || r.RegisteredCommand != "" {
		t.Errorf("report = %+v", r)
	}
	if len(r.Files) != 1 || r.Files[0].Path != p.Binary {
		t.Errorf("files = %+v", r.Files)
	}
}

func TestStatusStaleWhenRegisteredBinaryGone(t *testing.T) {
	p := binPaths(t)
	Install(p)
	os.Remove(p.Binary)
	other := p // an intact install runs status
	other.Binary = binPaths(t).Binary
	r := Status(other)
	if r.FilesOK() || !reflect.DeepEqual(missingFiles(r), []string{p.Binary}) {
		t.Errorf("missing = %v", missingFiles(r))
	}
	if r.RegisteredCommand != p.Binary {
		t.Errorf("registered = %q", r.RegisteredCommand)
	}
	if got := DescribeLaunch(r.RegisteredCommand); got != "the binary at "+p.Binary {
		t.Errorf("describe = %q", got)
	}
}

func TestStatusStaleWhenHookNamesGoneInstall(t *testing.T) {
	p := binPaths(t)
	Install(p)
	settings, _ := loadObj(p.Settings)
	h := entryHooks(getArr(getObj(settings, "hooks"), "PostToolUse")[0])[0].(*jsonx.Obj)
	h.Set("command", "bun /gone/mcp-recall/dist/cli.js post-tool-use")
	writeObj(p.Settings, settings)
	r := Status(p)
	found := false
	for _, f := range missingFiles(r) {
		found = found || f == "/gone/mcp-recall/dist/cli.js"
	}
	if !found {
		t.Errorf("missing = %v", missingFiles(r))
	}
}

func TestStatusChecksOnlyRecallsHookInSharedEntry(t *testing.T) {
	p := binPaths(t)
	Install(p)
	settings, _ := loadObj(p.Settings)
	e := getArr(getObj(settings, "hooks"), "PostToolUse")[0].(*jsonx.Obj)
	e.Set("hooks", append(entryHooks(e), mustParse(t, `{"type":"command","command":"/gone/notify post-tool-use"}`)))
	writeObj(p.Settings, settings)
	r := Status(p)
	if !r.FilesOK() {
		t.Errorf("neighbour hook checked: missing = %v", missingFiles(r))
	}
}

func TestStatusStaleWhenRegisteredBunGone(t *testing.T) {
	p := binPaths(t)
	Install(p)
	writeJSON(t, p.ClaudeJSON, `{"mcpServers":{"recall":{"type":"stdio","command":"/gone/.bun/bin/bun","args":["/x/dist/server.js"]}}}`)
	r := Status(p)
	missing := strings.Join(missingFiles(r), " ")
	if !strings.Contains(missing, "/gone/.bun/bin/bun") {
		t.Errorf("missing = %v", missing)
	}
	if got := DescribeLaunch(r.RegisteredCommand); got != "the npm install" {
		t.Errorf("describe = %q", got)
	}
}
