// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/handlers/index.go

package handlers

import (
	"reflect"
	"runtime"
	"strings"
	"unicode"
)

// HandlerName returns the short function name of a handler (e.g. "githubHandler"),
// as upstream's handler.name gives it. A profile handler is a closure, so its
// name comes from GetNamedHandler instead.
func HandlerName(h Handler) string {
	name := runtime.FuncForPC(reflect.ValueOf(h).Pointer()).Name()
	if i := strings.LastIndex(name, "."); i >= 0 {
		return name[i+1:]
	}
	return name
}

// ProfileLookup is an optional hook, set by the profiles package (phase 2), that
// returns a declarative-profile handler for a tool at the given tiers and its
// name (`profile:<id>`), or nil. Kept as a variable to avoid an import cycle
// between handlers and profiles.
var ProfileLookup func(toolName string, tiers []string) (Handler, string)

func profileHandler(toolName string, tiers []string) (Handler, string) {
	if ProfileLookup == nil {
		return nil, ""
	}
	return ProfileLookup(toolName, tiers)
}

type handlerMatcher struct {
	match   func(string) bool
	handler Handler
}

func contains(sub string) func(string) bool {
	return func(t string) bool { return strings.Contains(t, sub) }
}
func prefix(p string) func(string) bool {
	return func(t string) bool { return strings.HasPrefix(t, p) }
}

// handlerRegistry is ordered by specificity; first match wins. Mirrors
// HANDLER_REGISTRY in the original.
var handlerRegistry = []handlerMatcher{
	{func(t string) bool { return strings.Contains(t, "playwright") && strings.Contains(t, "snapshot") }, playwrightHandler},
	{prefix("mcp__github__"), githubHandler},
	{prefix("mcp__gitlab__"), gitlabHandler},
	{prefix("mcp__stripe__"), stripeHandler},
	{func(t string) bool {
		return strings.HasPrefix(t, "mcp__filesystem__") || strings.Contains(t, "read_file") || strings.Contains(t, "get_file")
	}, filesystemHandler},
	{func(t string) bool {
		for _, s := range []string{"bash", "shell", "terminal", "run_command", "ssh_exec", "exec_command", "remote_exec", "container_exec"} {
			if strings.Contains(t, s) {
				return true
			}
		}
		return false
	}, shellHandler},
	{contains("linear"), linearHandler},
	{contains("slack"), slackHandler},
	{contains("tavily"), tavilyHandler},
	{func(t string) bool {
		for _, s := range []string{"postgres", "mysql", "sqlite", "database"} {
			if strings.Contains(t, s) {
				return true
			}
		}
		return false
	}, databaseHandler},
	{contains("sentry"), sentryHandler},
	{contains("csv"), csvHandler},
}

// GetHandler returns the compression handler for a tool. Dispatch order:
//  1. native Bash tool → CLI-aware bash handler (an error only on stderr goes
//     to shell, upstream #326)
//  2. user/community profiles
//  3. typed-handler registry (first match wins)
//  4. bundled profiles
//  5. content blocks with non-text → content-block handler (strip images)
//  6. JSON content fallback
//  7. CSV content fallback
//  8. generic handler
func GetHandler(toolName string, output any, input any) Handler {
	h, _ := GetNamedHandler(toolName, output, input)
	return h
}

// GetNamedHandler is GetHandler plus the handler's name, which the hook
// records for `discover` (upstream #341): the function name, or
// `profile:<id>` for a profile.
func GetNamedHandler(toolName string, output any, input any) (Handler, string) {
	named := func(h Handler) (Handler, string) { return h, HandlerName(h) }
	if toolName == "Bash" {
		return named(keepStderrError(GetBashHandler(input), output))
	}
	if h, name := profileHandler(toolName, []string{"user", "community"}); h != nil {
		return h, name
	}
	for _, m := range handlerRegistry {
		if m.match(toolName) {
			return named(m.handler)
		}
	}
	if h, name := profileHandler(toolName, []string{"bundled"}); h != nil {
		return h, name
	}

	// MCP content blocks that include images/audio: strip them before the JSON
	// fallback, which does not truncate strings and would store the JPEG
	// almost verbatim (upstream #270).
	if hasNonTextContentBlocks(output) {
		return named(contentBlockHandler)
	}

	text := ExtractText(output)
	trimmed := strings.TrimLeftFunc(text, unicode.IsSpace)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		return named(jsonHandler)
	}
	if looksLikeCsv(text) {
		return named(csvHandler)
	}
	return named(genericHandler)
}
