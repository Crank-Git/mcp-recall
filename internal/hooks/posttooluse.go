// Package hooks implements the SessionStart and PostToolUse hook handlers.
// Ports src/hooks/*.ts.
package hooks

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mcprecall/internal/config"
	"mcprecall/internal/db"
	"mcprecall/internal/denylist"
	"mcprecall/internal/format"
	"mcprecall/internal/handlers"
	"mcprecall/internal/hints"
	"mcprecall/internal/jsonx"
	"mcprecall/internal/logx"
	"mcprecall/internal/projectkey"
	"mcprecall/internal/retention"
	"mcprecall/internal/secrets"
)

// HookOutput is the JSON returned to Claude Code from PostToolUse. Empty fields
// are omitted so a skip serialises to "{}".
type HookOutput struct {
	HookSpecificOutput *HookSpecificOutput `json:"hookSpecificOutput,omitempty"`
	// Legacy field from before Claude Code documented updatedToolOutput;
	// current versions ignore it (upstream #298).
	UpdatedMCPToolOutput string `json:"updatedMCPToolOutput,omitempty"`
	SuppressOutput       bool   `json:"suppressOutput,omitempty"`
}

// HookSpecificOutput carries the replacement output. UpdatedToolOutput is a
// string for MCP tools, or the Bash response object (*jsonx.Obj) for Bash.
type HookSpecificOutput struct {
	HookEventName     string `json:"hookEventName"`
	UpdatedToolOutput any    `json:"updatedToolOutput"`
}

// asBashResponse returns the Bash tool's response object, which may arrive
// serialised as a JSON string; nil when it has no string stdout.
func asBashResponse(toolResponse any) *jsonx.Obj {
	value := toolResponse
	if s, ok := value.(string); ok {
		parsed, err := jsonx.ParseString(s)
		if err != nil {
			return nil
		}
		value = parsed
	}
	obj, ok := value.(*jsonx.Obj)
	if !ok {
		return nil
	}
	if _, ok := obj.Str("stdout"); !ok {
		return nil
	}
	return obj
}

// maxPersistedBytes bounds a persisted Bash output read back from disk; a
// larger one stays cut.
const maxPersistedBytes = 20 * 1024 * 1024

// withPersistedOutput returns the whole Bash output when Claude Code cut it.
// Claude Code cuts Bash stdout at about 50 KB before this hook sees it, with no
// marker in it, and saves the whole output (stdout, then stderr) to
// persistedOutputPath. Summarising the cut misstates every total (upstream
// #316), so the file is read back when it is plainly the one Claude Code
// wrote: absolute, in a tool-results directory, and exactly
// persistedOutputSize bytes. Anything else leaves the response as it is.
func withPersistedOutput(toolResponse any) any {
	bash := asBashResponse(toolResponse)
	if bash == nil {
		return toolResponse
	}
	path, ok := bash.Str("persistedOutputPath")
	sizeV, _ := bash.Get("persistedOutputSize")
	size, isNum := sizeV.(float64)
	if !ok || !isNum {
		return toolResponse
	}
	if !filepath.IsAbs(path) || filepath.Base(filepath.Dir(path)) != "tool-results" || size > maxPersistedBytes {
		return toolResponse
	}
	info, err := os.Stat(path)
	if err != nil || float64(info.Size()) != size {
		return toolResponse
	}
	data, err := os.ReadFile(path)
	if err != nil {
		logx.Debug(fmt.Sprintf("persisted output unreadable · %s · %v", path, err))
		return toolResponse
	}
	full := bash.Clone()
	full.Set("stdout", string(data))
	full.Set("stderr", "")
	return full
}

// replaceOutput replaces the tool's output with text via
// hookSpecificOutput.updatedToolOutput; the top-level field alone is silently
// ignored (upstream #298). For a built-in tool the replacement must match that
// tool's output shape, or Claude Code delivers the original: a plain string
// never matches Bash's object, so Bash gets its response back with stdout
// replaced (upstream #304). The summary already carries stderr, so it is
// cleared rather than shown twice.
func replaceOutput(text string, toolResponse any) HookOutput {
	var updated any = text
	if bash := asBashResponse(toolResponse); bash != nil {
		replaced := bash.Clone()
		replaced.Set("stdout", text)
		replaced.Set("stderr", "")
		updated = replaced
	}
	return HookOutput{
		HookSpecificOutput:   &HookSpecificOutput{HookEventName: "PostToolUse", UpdatedToolOutput: updated},
		UpdatedMCPToolOutput: text,
		SuppressOutput:       true,
	}
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// HandlePostToolUse intercepts a tool output: denylist + secret checks, dedup,
// compress via the matching handler, store, evict, and return the summary.
// It never returns an error — any failure degrades to a pass-through ("{}").
func HandlePostToolUse(raw string) HookOutput {
	parsed, err := jsonx.ParseString(raw)
	if err != nil {
		logx.Error("post-tool-use received invalid JSON — skipping")
		return HookOutput{}
	}
	obj, ok := parsed.(*jsonx.Obj)
	if !ok {
		logx.Error("post-tool-use received unexpected input shape — skipping")
		return HookOutput{}
	}

	toolName, _ := obj.Str("tool_name")
	cwd, _ := obj.Str("cwd")
	sessionID, _ := obj.Str("session_id")
	toolInput, hasInput := obj.Get("tool_input")
	toolResponse, _ := obj.Get("tool_response")

	cfg := config.Load()
	// The whole output, not the cut Claude Code hands over above ~50 KB (#316).
	response := toolResponse
	if toolName == "Bash" {
		response = withPersistedOutput(toolResponse)
	}

	// 1. Denylist check
	if denylist.IsDenied(toolName, cfg) {
		logx.Debug("SKIP denylist · " + toolName)
		return HookOutput{}
	}

	// 2. Extract text and check for secrets. For Bash that is stdout + stderr:
	//    the envelope's field names and escaped newlines are not output, so they
	//    must not reach the store, the hints or FTS (upstream #306).
	var fullContent string
	if toolName == "Bash" {
		fullContent = handlers.BashOutputText(response)
	} else {
		fullContent = handlers.ExtractText(response)
	}
	logx.Debug(fmt.Sprintf("intercepted %s · %s", toolName, format.Bytes(len(fullContent))))
	if names := secrets.Find(fullContent); len(names) > 0 {
		logx.Warn(fmt.Sprintf("skipped %s: detected %s", toolName, joinComma(names)))
		return HookOutput{}
	}

	// 3. Setup DB
	projectKey := projectkey.Key(cwd)
	database, err := db.Open(db.DefaultDBPath(projectKey))
	if err != nil {
		logx.Error("post-tool-use db open failed: " + err.Error())
		return HookOutput{}
	}
	defer database.Close()

	// 4. Dedup. input_hash catches an identical call (skipped when tool_input is
	//    absent); output_hash catches identical content from any call, so re-runs
	//    and different calls yielding the same output store once.
	var inputHash *string
	if hasInput {
		h := sha256Hex(toolName + jsonx.Compact(toolInput))
		inputHash = &h
	}
	outputHash := db.HashContent(fullContent)

	cachedResponse := func(c *db.StoredOutput) HookOutput {
		cachedDate := time.Unix(c.CreatedAt, 0).UTC().Format("2006-01-02")
		logx.Debug(fmt.Sprintf("CACHE HIT · %s · id=%s · cached %s", toolName, c.ID, cachedDate))
		return replaceOutput(fmt.Sprintf("[recall:%s · cached · %s]\n%s", c.ID, cachedDate, c.Summary), toolResponse)
	}

	if inputHash != nil {
		if byInput, _ := db.CheckDedup(database, projectKey, *inputHash); byInput != nil {
			return cachedResponse(byInput)
		}
	}
	// A content match may have been stored by a different tool; attribution
	// follows the first storer — acceptable since the full content is identical.
	if byOutput, _ := db.CheckOutputDedup(database, projectKey, outputHash); byOutput != nil {
		return cachedResponse(byOutput)
	}

	// 5. Compress
	handler := handlers.GetHandler(toolName, response, toolInput)
	logx.Debug(fmt.Sprintf("handler: %s · %s", handlers.HandlerName(handler), toolName))
	res := handler(toolName, response)
	// An empty summary always passes the size check below, yet delivers
	// nothing: the handler did not recognise this shape. Fall back rather than
	// store it (upstream #296).
	if strings.TrimSpace(res.Summary) == "" && res.OriginalSize > 0 {
		logx.Debug(fmt.Sprintf("empty summary from %s · %s · falling back to genericHandler", handlers.HandlerName(handler), toolName))
		res = handlers.GenericHandler(toolName, response)
	}
	summarySize := len(res.Summary)

	// 6. Only store when compression is meaningful
	if summarySize >= res.OriginalSize {
		logx.Debug(fmt.Sprintf("SKIP no-compression · %s · %s ≥ %s", toolName, format.Bytes(summarySize), format.Bytes(res.OriginalSize)))
		return HookOutput{}
	}

	// 7. Store. Retention policy decides whether to keep the verbatim body or
	//    store the row summary-only (store.retention). The command drives the
	//    balanced-tier classification for Bash; outputHash (computed above from
	//    the real content) is passed through so dedup works even when the body is
	//    dropped.
	command := ""
	if hasInput {
		if in, ok := toolInput.(*jsonx.Obj); ok {
			command, _ = in.Str("command")
		}
	}
	fullRetained := 1
	if !retention.ShouldRetainFullBody(cfg.Store.Retention, toolName, command) {
		fullRetained = 0
		logx.Debug(fmt.Sprintf("summary-only · %s · retention=%s", toolName, cfg.Store.Retention))
	}

	// Privacy-safe command family fingerprint for per-command savings
	// attribution (upstream #251). Bash only; "" (no bare verb) stores NULL.
	var commandFP *string
	if toolName == "Bash" && command != "" {
		if fp := handlers.CommandFingerprint(handlers.NormalizeCommand(command)); fp != "" {
			commandFP = &fp
		}
	}

	stored, err := db.StoreOutput(database, db.StoreInput{
		ProjectKey:   projectKey,
		SessionID:    sessionID,
		ToolName:     toolName,
		Summary:      res.Summary,
		FullContent:  fullContent,
		OriginalSize: res.OriginalSize,
		InputHash:    inputHash,
		OutputHash:   &outputHash, // reuse the hash computed above
		FullRetained: &fullRetained,
		CommandFP:    commandFP,
	})
	if err != nil {
		logx.Error("post-tool-use store failed: " + err.Error())
		return HookOutput{}
	}

	// 8. Evict if store exceeds size limit
	db.EvictIfNeeded(database, projectKey, cfg.Store.MaxSizeMB, cfg.Store.EvictionHalfLifeDays, time.Now().Unix())

	// 9. Return compressed output to Claude, with retrieval hints so Claude's
	//    first recall__search lands on a real term.
	reduction := int(math.Round((1 - float64(summarySize)/float64(res.OriginalSize)) * 100))
	logx.Debug(fmt.Sprintf("STORED · %s · id=%s · %s→%s (%d%% reduction)", toolName, stored.ID, format.Bytes(res.OriginalSize), format.Bytes(summarySize), reduction))
	hintStr := ""
	if hs := hints.Extract(fullContent); len(hs) > 0 {
		quoted := make([]string, len(hs))
		for i, h := range hs {
			quoted[i] = `"` + h + `"`
		}
		hintStr = " · search: " + strings.Join(quoted, ", ")
	}
	header := fmt.Sprintf("[recall:%s · %s→%s (%d%% reduction)%s]", stored.ID, format.Bytes(res.OriginalSize), format.Bytes(summarySize), reduction, hintStr)
	return replaceOutput(header+"\n"+res.Summary, toolResponse)
}

func joinComma(xs []string) string {
	out := ""
	for i, x := range xs {
		if i > 0 {
			out += ", "
		}
		out += x
	}
	return out
}
