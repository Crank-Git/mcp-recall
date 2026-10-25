// Package install registers/unregisters the mcp-recall Go binary with Claude
// Code: the MCP server entry in ~/.claude.json, the SessionStart/PostToolUse
// hooks in ~/.claude/settings.json, and an instruction block in ~/.claude/CLAUDE.md.
// Ports src/install/index.ts. Config JSON is read and rewritten order-preserving
// so unrelated user settings keep their position.
package install

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"mcprecall/internal/jsonx"
)

// PostToolUseMatcher excludes recall's own tools and matches all other MCP
// tools plus the native Bash tool. Evaluated by Claude Code (JS regex), so the
// negative lookahead is fine there.
const PostToolUseMatcher = "(mcp__(?!recall__).*|Bash)"

const (
	claudeMdStart = "<!-- BEGIN mcp-recall -->"
	claudeMdEnd   = "<!-- END mcp-recall -->"
)

const claudeMdSnippet = `## mcp-recall

Session context from previous sessions is automatically injected at startup (pinned items, notes, recent activity). If it was truncated, call ` + "`recall__context()`" + ` for the full view.

When a tool output was compressed by mcp-recall (you'll see a summary with a recall ID like ` + "`recall_abc123`" + `), call ` + "`recall__retrieve(\"recall_abc123\")`" + ` when you need the full content.

Proactively:
- ` + "`recall__note(\"…\")`" + ` — save important decisions or context worth keeping across sessions
- ` + "`recall__pin(\"recall_abc123\")`" + ` — protect frequently-needed items from expiry and eviction
- ` + "`recall__search(\"query\")`" + ` — find stored outputs by content when you don't have an ID`

func claudeMdBlock() string { return claudeMdStart + "\n" + claudeMdSnippet + "\n" + claudeMdEnd }

// Paths bundles the three config file locations (overridable for tests).
type Paths struct {
	ClaudeJSON string
	Settings   string
	ClaudeMD   string
	Binary     string // path to the mcprecall binary
}

// DefaultPaths returns the standard locations plus the running binary path.
func DefaultPaths() Paths {
	home, _ := os.UserHomeDir()
	bin, _ := os.Executable()
	if abs, err := filepath.Abs(bin); err == nil {
		bin = abs
	}
	return Paths{
		ClaudeJSON: filepath.Join(home, ".claude.json"),
		Settings:   filepath.Join(home, ".claude", "settings.json"),
		ClaudeMD:   filepath.Join(home, ".claude", "CLAUDE.md"),
		Binary:     bin,
	}
}

// ── order-preserving JSON file IO ─────────────────────────────────────────────

func loadObj(path string) (*jsonx.Obj, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return jsonx.NewObj(), nil
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return jsonx.NewObj(), nil
	}
	v, err := jsonx.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("cannot parse %s: %w", path, err)
	}
	o, ok := v.(*jsonx.Obj)
	if !ok {
		return jsonx.NewObj(), nil
	}
	return o, nil
}

func writeObj(path string, o *jsonx.Obj) error {
	return writeText(path, jsonx.Indent(o)+"\n")
}

// maxLinkHops is the kernel's own symlink-chain limit (Linux ELOOP), so a loop
// cannot spin.
const maxLinkHops = 40

// writeTarget is the file a write to path should replace: the end of a symlink
// chain, even a dangling one, so a dotfiles-managed link survives the rename
// (upstream #373).
func writeTarget(path string) (string, error) {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	target := path
	for hop := 0; hop < maxLinkHops; hop++ {
		link, err := os.Readlink(target)
		if err != nil {
			return target, nil // no file yet, or the end of the chain
		}
		if !filepath.IsAbs(link) {
			link = filepath.Join(filepath.Dir(target), link)
		}
		target = link
	}
	return target, nil
}

// writeText is an atomic write (temp file + rename) through any symlink,
// keeping the replaced file's mode: a 0600 ~/.claude.json can hold MCP server
// tokens (upstream #373).
func writeText(path, content string) error {
	target, err := writeTarget(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	mode := fs.FileMode(0o644)
	keep := false
	if fi, err := os.Stat(target); err == nil {
		mode, keep = fi.Mode().Perm(), true
	}
	tmp := target + ".tmp"
	// A fresh temp file is created with the mode (narrowed by the umask), so it
	// is never wider than the file it replaces; the chmod restores bits the
	// umask cut.
	_ = os.Remove(tmp)
	if err := os.WriteFile(tmp, []byte(content), mode); err != nil {
		return err
	}
	if keep {
		if err := os.Chmod(tmp, mode); err != nil {
			os.Remove(tmp)
			return err
		}
	}
	return os.Rename(tmp, target)
}

// ── launch words ──────────────────────────────────────────────────────────────

// shellWord quotes p as one word of a hook command. Claude Code runs hook
// commands through a POSIX shell, so a path with a space or a quote goes in
// single quotes, where a backslash stays literal.
func shellWord(p string) string {
	if shellUnsafeRe.MatchString(p) {
		return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
	}
	return p
}

// shellUnsafeRe matches any character a shell does not leave alone.
var shellUnsafeRe = regexp.MustCompile(`[^\w@%+=:,./-]`)

// shellWords splits a hook command into words as a POSIX shell would: the
// reverse of shellWord, so Status can find the files a hook runs (upstream
// #364). Handles bare words, single quotes and backslash escapes; nothing else.
func shellWords(cmd string) []string {
	var words []string
	i := 0
	for i < len(cmd) {
		for i < len(cmd) && cmd[i] == ' ' {
			i++
		}
		if i >= len(cmd) {
			break
		}
		var word strings.Builder
		for i < len(cmd) && cmd[i] != ' ' {
			switch cmd[i] {
			case '\'':
				end := strings.IndexByte(cmd[i+1:], '\'')
				if end == -1 {
					return words // unterminated: not a command we wrote
				}
				word.WriteString(cmd[i+1 : i+1+end])
				i += end + 2
			case '\\':
				if i+1 < len(cmd) {
					word.WriteByte(cmd[i+1])
				}
				i += 2
			default:
				word.WriteByte(cmd[i])
				i++
			}
		}
		words = append(words, word.String())
	}
	return words
}

// ── hook entry builders / detectors ───────────────────────────────────────────

const (
	sessionStartMarker = "session-start"
	postToolUseMarker  = "post-tool-use"
)

func hookEntry(cli, marker, matcher string) *jsonx.Obj {
	hook := jsonx.NewObj()
	hook.Set("type", "command")
	hook.Set("command", cli+" "+marker)
	hook.Set("timeout", float64(10))
	e := jsonx.NewObj()
	if matcher != "" {
		e.Set("matcher", matcher)
	}
	e.Set("hooks", []any{hook})
	return e
}

func sessionStartEntry(cli string) *jsonx.Obj { return hookEntry(cli, sessionStartMarker, "") }

func postToolUseEntry(cli string) *jsonx.Obj {
	return hookEntry(cli, postToolUseMarker, PostToolUseMatcher)
}

func entryHooks(entry any) []any {
	o, ok := entry.(*jsonx.Obj)
	if !ok {
		return nil
	}
	return getArr(o, "hooks")
}

func hookCommand(h any) string {
	o, ok := h.(*jsonx.Obj)
	if !ok {
		return ""
	}
	cmd, _ := o.Str("command")
	return cmd
}

// entryCommand is the command of an entry's first hook.
func entryCommand(entry any) string {
	hooks := entryHooks(entry)
	if len(hooks) == 0 {
		return ""
	}
	return hookCommand(hooks[0])
}

// ourHooks recognises recall's hooks in one event: the entry that holds one,
// and the hook itself.
type ourHooks struct {
	marker  string
	matcher string // "" = any
	clis    []string
}

// hook reports whether h is recall's: its command names recall and the
// subcommand, or is exactly "<cli> <subcommand>" for a launch known to run
// recall, which covers a binary whose path has no "recall" in it (upstream #366).
func (r ourHooks) hook(h any) bool {
	cmd := hookCommand(h)
	if cmd == "" {
		return false
	}
	if strings.Contains(cmd, "recall") && strings.Contains(cmd, r.marker) {
		return true
	}
	for _, cli := range r.clis {
		if cmd == cli+" "+r.marker {
			return true
		}
	}
	return false
}

func (r ourHooks) entry(e any) bool {
	o, ok := e.(*jsonx.Obj)
	if !ok {
		return false
	}
	if r.matcher != "" {
		if m, _ := o.Str("matcher"); m != r.matcher {
			return false
		}
	}
	for _, h := range getArr(o, "hooks") {
		if r.hook(h) {
			return true
		}
	}
	return false
}

func sessionStartHooks(clis []string) ourHooks {
	return ourHooks{marker: sessionStartMarker, clis: clis}
}

func postToolUseHooks(clis []string) ourHooks {
	return ourHooks{marker: postToolUseMarker, matcher: PostToolUseMatcher, clis: clis}
}

func isOurSessionStart(entry any) bool { return sessionStartHooks(nil).entry(entry) }

func isOurPostToolUse(entry any) bool { return postToolUseHooks(nil).entry(entry) }

// knownCLIs are the launches known to run recall: this install's, and the
// program the registered server names, so a renamed binary's hooks are still
// found (upstream #366).
func knownCLIs(cli string, server *jsonx.Obj) []string {
	clis := []string{cli}
	if server != nil {
		if cmd, ok := server.Str("command"); ok && cmd != "" && !isBunProgram(cmd) {
			if w := shellWord(cmd); w != cli {
				clis = append(clis, w)
			}
		}
	}
	return clis
}

// withHooks returns a shallow copy of entry with its hooks replaced.
func withHooks(entry *jsonx.Obj, hooks []any) *jsonx.Obj {
	c := jsonx.NewObj()
	for _, k := range entry.Keys() {
		v, _ := entry.Get(k)
		c.Set(k, v)
	}
	c.Set("hooks", hooks)
	return c
}

// removeOurHooks removes recall's hooks from entries, dropping an entry only
// once it is empty, so a hook the user grouped beside recall's stays. Returns
// how many hooks went.
func removeOurHooks(entries []any, ours ourHooks) ([]any, int) {
	kept := make([]any, 0, len(entries))
	removed := 0
	for _, e := range entries {
		if !ours.entry(e) {
			kept = append(kept, e)
			continue
		}
		all := entryHooks(e)
		var hooks []any
		for _, h := range all {
			if !ours.hook(h) {
				hooks = append(hooks, h)
			}
		}
		removed += len(all) - len(hooks)
		if len(hooks) > 0 {
			kept = append(kept, withHooks(e.(*jsonx.Obj), hooks))
		}
	}
	return kept, removed
}

// upsertHook adds fresh to entries, or points recall's existing hook at
// fresh's command. An update changes that hook's command and nothing else: a
// timeout the user raised, and other hooks they grouped into the same entry,
// stay (upstream #365). Duplicates of recall's hook after the first are
// removed (upstream #366). result is "added", "updated" or "present".
func upsertHook(entries []any, ours ourHooks, fresh *jsonx.Obj) ([]any, string) {
	idx := -1
	for i, e := range entries {
		if ours.entry(e) {
			idx = i
			break
		}
	}
	if idx == -1 {
		return append(append([]any{}, entries...), fresh), "added"
	}
	rest, dupes := removeOurHooks(entries[idx+1:], ours)
	entry := entries[idx].(*jsonx.Obj)
	command := entryCommand(fresh)
	all := entryHooks(entry)
	first := -1
	var hooks []any
	stale := false
	for i, h := range all {
		if !ours.hook(h) {
			hooks = append(hooks, h)
			continue
		}
		if first != -1 {
			continue // a second copy inside the same entry
		}
		first = i
		if hookCommand(h) == command {
			hooks = append(hooks, h)
			continue
		}
		stale = true
		c := jsonx.NewObj()
		for _, k := range h.(*jsonx.Obj).Keys() {
			v, _ := h.(*jsonx.Obj).Get(k)
			c.Set(k, v)
		}
		c.Set("command", command)
		hooks = append(hooks, c)
	}
	if dupes == 0 && len(hooks) == len(all) && !stale {
		return entries, "present"
	}
	out := append(append([]any{}, entries[:idx]...), withHooks(entry, hooks))
	return append(out, rest...), "updated"
}

// getArr returns the array at key, or an empty slice.
func getArr(o *jsonx.Obj, key string) []any {
	if v, ok := o.Get(key); ok {
		if a, ok := v.([]any); ok {
			return a
		}
	}
	return nil
}

func getObj(o *jsonx.Obj, key string) *jsonx.Obj {
	if v, ok := o.Get(key); ok {
		if x, ok := v.(*jsonx.Obj); ok {
			return x
		}
	}
	return nil
}

// ── install ───────────────────────────────────────────────────────────────────

// Install registers the MCP server, hooks, and CLAUDE.md block. Idempotent.
// Returns a list of human-readable change lines.
func Install(p Paths) ([]string, error) {
	var changes []string

	// ~/.claude.json — mcpServers.recall
	root, err := loadObj(p.ClaudeJSON)
	if err != nil {
		return nil, err
	}
	servers := getObj(root, "mcpServers")
	if servers == nil {
		servers = jsonx.NewObj()
		root.Set("mcpServers", servers)
	}
	cli := shellWord(p.Binary)
	existing := getObj(servers, "recall")
	// From the server entry as it was before this run, which may name a
	// renamed binary.
	clis := knownCLIs(cli, existing)
	if existing == nil {
		newServer := jsonx.NewObj()
		newServer.Set("type", "stdio")
		newServer.Set("command", p.Binary)
		newServer.Set("args", []any{"server"})
		servers.Set("recall", newServer)
		if err := writeObj(p.ClaudeJSON, root); err != nil {
			return nil, err
		}
		changes = append(changes, "MCP server registered")
	} else if !runsServer(existing, p.Binary) {
		// Only the launch changes: an env or other field the user added stays
		// (upstream #365).
		existing.Set("type", "stdio")
		existing.Set("command", p.Binary)
		existing.Set("args", []any{"server"})
		if err := writeObj(p.ClaudeJSON, root); err != nil {
			return nil, err
		}
		changes = append(changes, "MCP server path updated")
	}

	// ~/.claude/settings.json — hooks
	settings, err := loadObj(p.Settings)
	if err != nil {
		return nil, err
	}
	hooks := getObj(settings, "hooks")
	if hooks == nil {
		hooks = jsonx.NewObj()
		settings.Set("hooks", hooks)
	}
	settingsChanged := false

	for _, ev := range []struct {
		name  string
		ours  ourHooks
		fresh *jsonx.Obj
	}{
		{"SessionStart", sessionStartHooks(clis), sessionStartEntry(cli)},
		{"PostToolUse", postToolUseHooks(clis), postToolUseEntry(cli)},
	} {
		entries, result := upsertHook(getArr(hooks, ev.name), ev.ours, ev.fresh)
		if result == "present" {
			continue
		}
		hooks.Set(ev.name, entries)
		settingsChanged = true
		changes = append(changes, ev.name+" hook "+result)
	}

	if settingsChanged {
		if err := writeObj(p.Settings, settings); err != nil {
			return nil, err
		}
	}

	// ~/.claude/CLAUDE.md
	res, err := injectClaudeMd(p.ClaudeMD)
	if err != nil {
		return nil, err
	}
	if res != "present" {
		changes = append(changes, "CLAUDE.md instructions "+res)
	}

	return changes, nil
}

func injectClaudeMd(path string) (string, error) {
	existing := ""
	if data, err := os.ReadFile(path); err == nil {
		existing = string(data)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}

	block := claudeMdBlock()
	start := strings.Index(existing, claudeMdStart)
	end := strings.Index(existing, claudeMdEnd)
	if start != -1 && end != -1 {
		cur := existing[start : end+len(claudeMdEnd)]
		if cur == block {
			return "present", nil
		}
		updated := existing[:start] + block + existing[end+len(claudeMdEnd):]
		return "updated", writeText(path, updated)
	}
	var content string
	if existing != "" {
		content = strings.TrimRight(existing, "\n\t ") + "\n\n" + block + "\n"
	} else {
		content = block + "\n"
	}
	return "added", writeText(path, content)
}

// ── uninstall ─────────────────────────────────────────────────────────────────

// Uninstall removes the MCP server, hooks, and CLAUDE.md block.
func Uninstall(p Paths) ([]string, error) {
	var changes []string

	clis := []string{shellWord(p.Binary)}
	if root, err := loadObj(p.ClaudeJSON); err == nil {
		if servers := getObj(root, "mcpServers"); servers != nil {
			if server := getObj(servers, "recall"); server != nil {
				// Read before the entry goes: it may name a renamed binary whose
				// hooks follow (upstream #366).
				clis = knownCLIs(clis[0], server)
			}
			if _, ok := servers.Get("recall"); ok {
				servers.Delete("recall")
				if err := writeObj(p.ClaudeJSON, root); err != nil {
					return nil, err
				}
				changes = append(changes, "Removed mcpServers.recall")
			}
		}
	}

	if settings, err := loadObj(p.Settings); err == nil {
		if hooks := getObj(settings, "hooks"); hooks != nil {
			changed := false
			// Every copy, not only the first: earlier versions could leave
			// duplicates (upstream #366).
			for _, ev := range []struct {
				name string
				ours ourHooks
			}{
				{"SessionStart", sessionStartHooks(clis)},
				{"PostToolUse", postToolUseHooks(clis)},
			} {
				entries := getArr(hooks, ev.name)
				if entries == nil {
					continue
				}
				kept, removed := removeOurHooks(entries, ev.ours)
				if removed > 0 {
					hooks.Set(ev.name, kept)
					changed = true
					changes = append(changes, "Removed "+ev.name+" hook")
				}
			}
			if changed {
				if err := writeObj(p.Settings, settings); err != nil {
					return nil, err
				}
			}
		}
	}

	if removed, err := removeClaudeMd(p.ClaudeMD); err == nil && removed {
		changes = append(changes, "Removed CLAUDE.md instructions")
	}
	return changes, nil
}

func removeClaudeMd(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	existing := string(data)
	start := strings.Index(existing, claudeMdStart)
	end := strings.Index(existing, claudeMdEnd)
	if start == -1 || end == -1 {
		return false, nil
	}
	before := strings.TrimRight(existing[:start], "\n\t ")
	after := strings.TrimLeft(existing[end+len(claudeMdEnd):], "\n")
	result := strings.TrimRight(before+"\n"+after, "\n\t ")
	if result != "" {
		result += "\n"
	}
	return true, writeText(path, result)
}

// ── status ────────────────────────────────────────────────────────────────────

// runsServer reports whether a registered mcpServers.recall entry runs bin:
// same command and args.
func runsServer(server *jsonx.Obj, bin string) bool {
	cmd, _ := server.Str("command")
	if cmd != bin {
		return false
	}
	args, _ := server.Get("args")
	return jsonx.Compact(args) == `["server"]`
}

var bunProgramRe = regexp.MustCompile(`(^|[\\/])bun(\.exe)?$`)

func isBunProgram(program string) bool { return bunProgramRe.MatchString(program) }

// DescribeLaunch names a launch for status, from the program its server entry
// runs: the upstream npm install runs bun.
func DescribeLaunch(command string) string {
	if isBunProgram(command) {
		return "the npm install"
	}
	return "the binary at " + command
}

// RegisteredFile is one file the registered server or hooks run.
type RegisteredFile struct {
	Label  string
	Path   string
	Exists bool
}

// locate finds a program as a shell would: an absolute path as is, a bare
// name on PATH.
func locate(program string) (string, bool) {
	if filepath.IsAbs(program) {
		_, err := os.Stat(program)
		return program, err == nil
	}
	if found, err := exec.LookPath(program); err == nil {
		return found, true
	}
	return program, false
}

// registeredFiles are the files the registered server and hooks run, from each
// launch's words: its program and, for bun, the script after it. Status checks
// these, not its own binary, which says nothing when another install is the
// one registered (upstream #364).
func registeredFiles(launches [][]string) []RegisteredFile {
	var files []RegisteredFile
	seen := map[string]bool{}
	add := func(label, p string) {
		if seen[p] {
			return
		}
		seen[p] = true
		path, ok := locate(p)
		files = append(files, RegisteredFile{Label: label, Path: path, Exists: ok})
	}
	for _, words := range launches {
		if len(words) == 0 || words[0] == "" {
			continue
		}
		if isBunProgram(words[0]) {
			add("bun", words[0])
			if len(words) > 1 && words[1] != "" {
				add(filepath.Base(words[1]), words[1])
			}
		} else {
			add("binary", words[0])
		}
	}
	return files
}

// ourHookCommands are the commands of recall's own hooks among an event's
// entries, not their neighbours'.
func ourHookCommands(entries []any, ours ourHooks) []string {
	var cmds []string
	for _, e := range entries {
		if !ours.entry(e) {
			continue
		}
		for _, h := range entryHooks(e) {
			if ours.hook(h) {
				cmds = append(cmds, hookCommand(h))
			}
		}
	}
	return cmds
}

// StatusReport says whether each piece is registered, and whether the files
// the registered launch runs exist.
type StatusReport struct {
	ServerRegistered bool
	SessionStartHook bool
	PostToolUseHook  bool
	ClaudeMD         bool
	// Files are what the registered server and hooks run (upstream #364).
	Files []RegisteredFile
	// RegisteredCommand is the program mcpServers.recall names, when it is not
	// this binary; "" otherwise.
	RegisteredCommand string
}

// FilesOK reports whether every registered file exists.
func (r StatusReport) FilesOK() bool {
	for _, f := range r.Files {
		if !f.Exists {
			return false
		}
	}
	return true
}

// Status inspects the config files and returns a StatusReport.
func Status(p Paths) StatusReport {
	var r StatusReport
	var server *jsonx.Obj
	if root, err := loadObj(p.ClaudeJSON); err == nil {
		if servers := getObj(root, "mcpServers"); servers != nil {
			_, r.ServerRegistered = servers.Get("recall")
			server = getObj(servers, "recall")
		}
	}
	clis := knownCLIs(shellWord(p.Binary), server)
	ss, ptu := sessionStartHooks(clis), postToolUseHooks(clis)

	var launches [][]string
	if server != nil {
		if cmd, ok := server.Str("command"); ok {
			words := []string{cmd}
			for _, a := range getArr(server, "args") {
				if s, ok := a.(string); ok {
					words = append(words, s)
				}
			}
			launches = append(launches, words)
			if !runsServer(server, p.Binary) {
				r.RegisteredCommand = cmd
			}
		}
	}
	if settings, err := loadObj(p.Settings); err == nil {
		if hooks := getObj(settings, "hooks"); hooks != nil {
			for _, e := range getArr(hooks, "SessionStart") {
				r.SessionStartHook = r.SessionStartHook || ss.entry(e)
			}
			for _, e := range getArr(hooks, "PostToolUse") {
				r.PostToolUseHook = r.PostToolUseHook || ptu.entry(e)
			}
			for _, cmd := range ourHookCommands(getArr(hooks, "SessionStart"), ss) {
				launches = append(launches, shellWords(cmd))
			}
			for _, cmd := range ourHookCommands(getArr(hooks, "PostToolUse"), ptu) {
				launches = append(launches, shellWords(cmd))
			}
		}
	}
	r.Files = registeredFiles(launches)
	if data, err := os.ReadFile(p.ClaudeMD); err == nil {
		r.ClaudeMD = strings.Contains(string(data), claudeMdStart)
	}
	return r
}
