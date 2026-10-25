// Command mcprecall is the Go port of mcp-recall: a context-compression layer
// for Claude Code. It runs as hook subcommands (post-tool-use, session-start),
// the MCP server, and user-facing management commands (install/status/...).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"mcprecall/internal/config"
	"mcprecall/internal/db"
	"mcprecall/internal/discover"
	"mcprecall/internal/format"
	"mcprecall/internal/gc"
	"mcprecall/internal/hooks"
	"mcprecall/internal/importer"
	"mcprecall/internal/install"
	"mcprecall/internal/learn"
	"mcprecall/internal/profiles"
	"mcprecall/internal/projectkey"
	"mcprecall/internal/server"
)

func main() {
	// Activate declarative TOML profiles for compression, and wire retrain.
	profiles.Register()
	learn.Register()

	if len(os.Args) < 2 || hasArg("--help") || hasArg("-h") {
		printHelp()
		return
	}
	if hasArg("--version") || hasArg("-v") {
		fmt.Println(server.Version)
		return
	}

	switch os.Args[1] {
	case "post-tool-use":
		raw, _ := io.ReadAll(os.Stdin)
		writeJSON(hooks.HandlePostToolUse(string(raw)))
	case "session-start":
		raw, _ := io.ReadAll(os.Stdin)
		if snap := hooks.HandleSessionStart(string(raw)); snap != "" {
			fmt.Fprintln(os.Stdout, snap)
		}
		// Control line: suppress the hook's own echo in the transcript.
		_, _ = os.Stdout.WriteString(`{"suppressOutput":true}` + "\n")
	case "server":
		if err := server.Run(context.Background()); err != nil {
			fmt.Fprintf(os.Stderr, "mcprecall: server error: %v\n", err)
			os.Exit(1)
		}
	case "install":
		changes, err := install.Install(install.DefaultPaths())
		if err != nil {
			fmt.Fprintf(os.Stderr, "mcprecall: install failed: %v\n", err)
			os.Exit(1)
		}
		reportChanges(changes)
	case "uninstall":
		changes, err := install.Uninstall(install.DefaultPaths())
		if err != nil {
			fmt.Fprintf(os.Stderr, "mcprecall: uninstall failed: %v\n", err)
			os.Exit(1)
		}
		reportChanges(changes)
	case "status":
		r := install.Status(install.DefaultPaths())
		printStatus(r)
	case "gc":
		runGC(os.Args[2:])
	case "discover":
		runDiscover(os.Args[2:])
	case "profiles":
		profiles.HandleProfilesCommand(os.Args[2:])
	case "learn":
		learn.HandleLearn(os.Args[2:])
	case "import":
		importer.HandleImport(os.Args[2:])
	case "completions":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "Usage: mcprecall completions <bash|zsh|fish>")
			os.Exit(1)
		}
		script, err := completionScript(os.Args[2])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Print(script)
	default:
		fmt.Fprintf(os.Stderr, "mcprecall: unknown subcommand %q\n", os.Args[1])
		printHelp()
		os.Exit(1)
	}
}

func hasArg(flag string) bool {
	for _, a := range os.Args[1:] {
		if a == flag {
			return true
		}
	}
	return false
}

func printHelp() {
	fmt.Print(`mcp-recall — context compression for Claude Code

Usage: mcprecall <command> [options]

Commands:
  install              Register hooks + MCP server in Claude Code
  uninstall            Remove hooks + MCP server
  status               Show current configuration and health
  server               Run the recall MCP server (stdio)
  gc [--force]         Reclaim disk: list/delete orphaned project DBs
    --stale-days N     Legacy DBs (no recorded path) older than N days are
                       candidates (default 90)
    --vacuum           Full-VACUUM surviving DBs to reclaim free pages
  discover             Rank output routed to fallback handlers: where a new
                       handler would pay
    --all              Every project's store, not just this one
    --since YYYY-MM-DD Only output stored on or after this date
    --limit N          Rows to list (default 10)
  profiles <cmd>       Manage compression profiles (list/available/info/install/
                       update/remove/seed/feed/check/retrain/test)
  learn                Generate profile suggestions from your installed MCPs
  import <file>        Restore items from a recall__export JSON dump
  completions <shell>  Print shell completion script (bash, zsh, fish)

Options:
  --help, -h           Show this help
  --version, -v        Show version
`)
}

func reportChanges(changes []string) {
	if len(changes) == 0 {
		fmt.Println("mcp-recall: already up to date")
		return
	}
	for _, c := range changes {
		fmt.Println("✓ " + c)
	}
	fmt.Println("\nRestart Claude Code to activate.")
}

func tick(ok bool) string {
	if ok {
		return "✓"
	}
	return "✗"
}

func printStatus(r install.StatusReport) {
	full := r.ServerRegistered && r.SessionStartHook && r.PostToolUseHook && r.ClaudeMD && r.FilesOK()
	state := "not installed"
	if full {
		state = "installed"
	} else if r.ServerRegistered || r.SessionStartHook || r.PostToolUseHook {
		state = "partial"
	}
	fmt.Printf("Installation: %s\n\n", state)
	fmt.Printf("  %s mcpServers.recall\n", tick(r.ServerRegistered))
	fmt.Printf("  %s SessionStart hook\n", tick(r.SessionStartHook))
	fmt.Printf("  %s PostToolUse hook\n", tick(r.PostToolUseHook))
	fmt.Printf("  %s CLAUDE.md instructions\n", tick(r.ClaudeMD))
	// What the registered entries run, which may be another install's
	// (upstream #364).
	if len(r.Files) > 0 {
		fmt.Println("\n  Registered launch")
		for _, f := range r.Files {
			fmt.Printf("  %s %s %s\n", tick(f.Exists), f.Label, f.Path)
		}
	}
	if r.RegisteredCommand != "" {
		fmt.Printf("\n  Registered: %s; this is %s.\n",
			install.DescribeLaunch(r.RegisteredCommand), install.DescribeLaunch(install.DefaultPaths().Binary))
		fmt.Println("  Run `mcprecall install` from this one to switch Claude Code to it.")
	}
	if !full && !r.FilesOK() {
		fmt.Println("\n  Run `mcprecall install` from the install Claude Code should use: what is marked ✗ is gone")
	}
	fmt.Println()
	printStoreFootprint(gc.StoreFootprint(db.DataDir()), config.Load().Store.GCReminderMB)
}

// printStoreFootprint nudges toward `gc` when the on-disk store is large.
func printStoreFootprint(fp gc.Footprint, reminderMB float64) {
	large := reminderMB > 0 && float64(fp.TotalBytes) >= reminderMB*1024*1024
	icon := "✓"
	if large {
		icon = "!"
	}
	plural := "s"
	if fp.DBCount == 1 {
		plural = ""
	}
	fmt.Printf("  %s Store: %s across %d project database%s\n", icon, format.Bytes(int(fp.TotalBytes)), fp.DBCount, plural)
	if large {
		fmt.Println("    → Reclaim space: mcprecall gc (review, then re-run with --force)")
	}
}

// runGC parses the gc subcommand's flags and runs it against the live store.
// Defaults to a dry run; only --force deletes.
func runGC(args []string) {
	opts := gc.Options{DryRun: true}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--force":
			opts.DryRun = false
		case "--vacuum":
			opts.Vacuum = true
		case "--stale-days":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "--stale-days requires a value")
				os.Exit(1)
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n < 1 {
				fmt.Fprintln(os.Stderr, "--stale-days must be a positive number")
				os.Exit(1)
			}
			opts.StaleDays = n
			i++
		default:
			fmt.Fprintf(os.Stderr, "unknown gc option: %s\n", args[i])
			os.Exit(1)
		}
	}

	cwd, _ := os.Getwd()
	currentFile := db.DefaultDBPath(projectkey.Key(cwd))
	gc.Run(os.Stdout, db.DataDir(), currentFile, opts, time.Now())
}

// runDiscover parses the discover subcommand's flags and prints the report.
func runDiscover(args []string) {
	var opts discover.Options
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--all":
			opts.All = true
		case "--since":
			value := ""
			if i+1 < len(args) {
				value = args[i+1]
			}
			since, ok := discover.ParseSince(value)
			if !ok {
				fmt.Fprintln(os.Stderr, "--since must be a date, YYYY-MM-DD")
				os.Exit(1)
			}
			opts.Since = since
			i++
		case "--limit":
			n := 0
			if i+1 < len(args) {
				n, _ = strconv.Atoi(args[i+1])
			}
			if n < 1 {
				fmt.Fprintln(os.Stderr, "--limit must be a positive integer")
				os.Exit(1)
			}
			opts.Limit = n
			i++
		default:
			fmt.Fprintf(os.Stderr, "unknown discover option: %s\n", args[i])
			os.Exit(1)
		}
	}
	cwd, _ := os.Getwd()
	discover.Run(os.Stdout, db.DataDir(), db.DefaultDBPath(projectkey.Key(cwd)), opts)
}

// writeJSON writes v as JSON followed by a newline, without HTML-escaping (to
// match JavaScript JSON.stringify, which leaves < > & untouched).
func writeJSON(v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v) // Encode appends a trailing newline
	_, _ = os.Stdout.Write(buf.Bytes())
}
