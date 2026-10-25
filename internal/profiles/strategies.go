// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/profiles/strategies.go

package profiles

import (
	"fmt"
	"strings"
	"unicode"

	"mcprecall/internal/handlers"
	"mcprecall/internal/jsonx"
)

// ── string helpers (match handlers' rune-based slicing) ───────────────────────

func firstChars(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
func runeLen(s string) int { return len([]rune(s)) }
func trimEnd(s string) string {
	return strings.TrimRightFunc(s, unicode.IsSpace)
}

// excerpt returns raw unchanged when it fits, so the hook's size check passes
// it through; trimming it would turn a complete answer into a 1-byte
// "summary". Otherwise the first maxChars runes, trimmed, then "\n…".
func excerpt(raw string, maxChars int) string {
	if runeLen(raw) <= maxChars {
		return raw
	}
	return trimEnd(firstChars(raw, maxChars)) + "\n…"
}

func deref(p *int, def int) int {
	if p != nil {
		return *p
	}
	return def
}

// resolvePath walks a dotted path through jsonx objects. Returns nil for a
// missing key or a JSON null (both treated as "absent", matching the TS).
func resolvePath(obj any, path string) any {
	if path == "" || path == "." {
		return obj
	}
	cur := obj
	for _, key := range strings.Split(path, ".") {
		o, ok := cur.(*jsonx.Obj)
		if !ok {
			return nil
		}
		v, ok := o.Get(key)
		if !ok {
			return nil
		}
		cur = v
	}
	return cur
}

func getLabel(fieldPath string, labels map[string]string) string {
	if labels != nil {
		if l, ok := labels[fieldPath]; ok && l != "" {
			return l
		}
	}
	parts := strings.Split(fieldPath, ".")
	return parts[len(parts)-1]
}

func fieldValue(obj any, fieldPath string, maxChars int) string {
	val := resolvePath(obj, fieldPath)
	if val == nil {
		return ""
	}
	var str string
	switch val.(type) {
	case *jsonx.Obj, []any:
		str = jsonx.Compact(val)
	default:
		str = handlers.JSToString(val)
	}
	if runeLen(str) > maxChars {
		return firstChars(str, maxChars) + "…"
	}
	return str
}

// ── json_extract ──────────────────────────────────────────────────────────────

func resolveItems(parsed any, itemsPaths []string) ([]any, bool) {
	paths := itemsPaths
	if len(paths) == 0 {
		paths = []string{""}
	}
	for _, p := range paths {
		val := resolvePath(parsed, p)
		if arr, ok := val.([]any); ok {
			return arr, true
		}
		if o, ok := val.(*jsonx.Obj); ok {
			return []any{o}, true
		}
	}
	if arr, ok := parsed.([]any); ok {
		return arr, true
	}
	if o, ok := parsed.(*jsonx.Obj); ok {
		return []any{o}, true
	}
	return nil, false
}

func applyJSONExtract(s Strategy, output any) handlers.Result {
	raw := handlers.ProfileText(output)
	originalSize := len(raw)
	fallbackChars := deref(s.FallbackChars, 500)

	parsed, err := jsonx.ParseString(raw)
	if err != nil {
		return handlers.Result{Summary: excerpt(raw, fallbackChars), OriginalSize: originalSize}
	}
	items, ok := resolveItems(parsed, s.ItemsPath)
	if !ok || len(items) == 0 {
		return handlers.Result{Summary: excerpt(raw, fallbackChars), OriginalSize: originalSize}
	}

	maxItems := deref(s.MaxItems, 10)
	maxCharsPerField := deref(s.MaxCharsPerField, 200)
	count := len(items)

	n := count
	if n > maxItems {
		n = maxItems
	}
	extracted := make([][]string, n)
	matched := false
	for i, item := range items[:n] {
		for _, f := range s.Fields {
			val := fieldValue(item, f, maxCharsPerField)
			if val != "" {
				extracted[i] = append(extracted[i], getLabel(f, s.Labels)+": "+val)
				matched = true
			}
		}
	}
	// No configured field matched anything: the profile does not fit this
	// output (a guessed `learn` profile, or an API that changed shape). An
	// excerpt beats "1. " lines that tell Claude nothing (upstream #299).
	if !matched {
		return handlers.Result{Summary: excerpt(raw, fallbackChars), OriginalSize: originalSize}
	}
	lines := make([]string, n)
	for i, parts := range extracted {
		lines[i] = fmt.Sprintf("%d. %s", i+1, strings.Join(parts, " · "))
	}
	more := ""
	if count > maxItems {
		more = fmt.Sprintf("\n…and %d more", count-maxItems)
	}
	plural := "s"
	if count == 1 {
		plural = ""
	}
	summary := fmt.Sprintf("%d item%s:\n%s%s", count, plural, strings.Join(lines, "\n"), more)
	return handlers.Result{Summary: summary, OriginalSize: originalSize}
}

// ── json_truncate ─────────────────────────────────────────────────────────────

func truncateJSON(value any, depth, maxDepth, maxArrayItems int) any {
	if depth > maxDepth {
		return "…"
	}
	switch t := value.(type) {
	case []any:
		n := len(t)
		if n > maxArrayItems {
			n = maxArrayItems
		}
		items := make([]any, 0, n+1)
		for _, v := range t[:n] {
			items = append(items, truncateJSON(v, depth+1, maxDepth, maxArrayItems))
		}
		if len(t) > maxArrayItems {
			items = append(items, fmt.Sprintf("…%d more", len(t)-maxArrayItems))
		}
		return items
	case *jsonx.Obj:
		res := jsonx.NewObj()
		for _, k := range t.Keys() {
			v, _ := t.Get(k)
			res.Set(k, truncateJSON(v, depth+1, maxDepth, maxArrayItems))
		}
		return res
	default:
		return value
	}
}

func applyJSONTruncate(s Strategy, output any) handlers.Result {
	raw := handlers.ProfileText(output)
	originalSize := len(raw)
	fallbackChars := deref(s.FallbackChars, 500)
	maxDepth := deref(s.MaxDepth, 3)
	maxArrayItems := deref(s.MaxArrayItems, 3)

	parsed, err := jsonx.ParseString(raw)
	if err != nil {
		return handlers.Result{Summary: excerpt(raw, fallbackChars), OriginalSize: originalSize}
	}
	return handlers.Result{Summary: jsonx.Indent(truncateJSON(parsed, 0, maxDepth, maxArrayItems)), OriginalSize: originalSize}
}

// ── text_truncate ─────────────────────────────────────────────────────────────

func applyTextTruncate(s Strategy, output any) handlers.Result {
	raw := handlers.ProfileText(output)
	return handlers.Result{Summary: excerpt(raw, deref(s.MaxChars, 500)), OriginalSize: len(raw)}
}

func apply(s Strategy, output any) handlers.Result {
	switch s.Type {
	case "json_extract":
		return applyJSONExtract(s, output)
	case "json_truncate":
		return applyJSONTruncate(s, output)
	default:
		return applyTextTruncate(s, output)
	}
}
