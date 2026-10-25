// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/retention/retention.go

// Package retention decides whether an intercepted output keeps a retrievable
// verbatim body or is stored summary-only. Driven by store.retention:
//
//	full     — keep every intercepted body (max retrievability, max disk).
//	balanced — keep MCP/web/API results, network-fetch Bash (curl/wget/gh api)
//	           and content reads (cat, git diff/show/log -p, head/tail, sed -n
//	           ranges, grep with context), whose summaries drop what Claude
//	           asked for (upstream #332). Store other reproducible Bash (git
//	           log/status, tests, ls, plain grep, docker ps, build/lint…)
//	           summary-only: an old copy is either trivially reproducible or
//	           misleadingly stale, and the summary already delivered its value.
//	minimal  — drop every intercepted body (summary-only for all).
//
// Notes (recall__note) never pass through here — they are stored via the note
// tool, not the hook — so memory always keeps its body regardless of level.
// Ports src/retention.ts.
package retention

import (
	"regexp"
	"strings"

	"mcprecall/internal/handlers"
)

// networkBashRe matches Bash commands whose output is expensive or impossible to
// reproduce and stays valid later (network fetches / API calls) — worth keeping
// under "balanced".
var networkBashRe = regexp.MustCompile(`^(curl|wget|https?|xh)\b|^gh\s+api\b`)

// unwrapCommand delegates to the one normalizer handler routing uses. This
// package used to keep its own copy that understood only `&&`/`;`, so a fetch
// behind a newline-separated `cd` was classified reproducible and its body
// dropped under balanced (upstream #260).
var unwrapCommand = handlers.NormalizeCommand

// ShouldRetainFullBody reports whether the verbatim body should be persisted for
// retrieval. command is the Bash tool_input.command ("" for non-Bash tools).
func ShouldRetainFullBody(level, toolName, command string) bool {
	switch level {
	case "full":
		return true
	case "minimal":
		return false
	}

	// balanced:
	if strings.HasPrefix(toolName, "mcp__") {
		return true // web/API/scrape — durable, expensive
	}
	if toolName == "Bash" {
		// A content read's summary drops what Claude asked for (a diff kept as
		// stats, a file as head and tail), so its body is the only copy
		// (upstream #332).
		return networkBashRe.MatchString(unwrapCommand(command)) || handlers.IsContentRead(command)
	}
	// Unknown intercepted tool — keep the body (conservative: never silently
	// drop something we don't understand).
	return true
}
