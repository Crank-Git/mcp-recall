// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/handlers/content_blocks.go

package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"mcprecall/internal/jsonx"
)

// mcpBinaryTypes are the non-text MCP content block types.
var mcpBinaryTypes = map[string]bool{"image": true, "image_url": true, "audio": true, "resource": true, "resource_link": true}

// contentBlock is an MCP content block: {type:"text", text} or {type:"image", …}.
type contentBlock struct {
	typ     string
	text    string
	hasText bool // text is a string
}

// blocksOf returns value as content blocks when it is a non-empty array of
// objects that all carry a string type, at least one being a text block with
// string text or a known binary type. Unrelated arrays (GitHub issues, …)
// return nil.
func blocksOf(value any) []contentBlock {
	arr, ok := value.([]any)
	if !ok || len(arr) == 0 {
		return nil
	}
	blocks := make([]contentBlock, 0, len(arr))
	recognised := false
	for _, item := range arr {
		o, ok := item.(*jsonx.Obj)
		if !ok {
			return nil
		}
		typ, ok := objStr(o, "type")
		if !ok {
			return nil
		}
		text, hasText := objStr(o, "text")
		if (typ == "text" && hasText) || mcpBinaryTypes[typ] {
			recognised = true
		}
		blocks = append(blocks, contentBlock{typ, text, hasText})
	}
	if !recognised {
		return nil
	}
	return blocks
}

func blocksFromValue(value any) []contentBlock {
	if b := blocksOf(value); b != nil {
		return b
	}
	if o, ok := value.(*jsonx.Obj); ok {
		if c, ok := o.Get("content"); ok {
			return blocksOf(c)
		}
	}
	return nil
}

// asContentBlocks returns MCP content blocks, top-level ([{type, text}, …]) or
// wrapped ({content: […]}), parsing a JSON string of either shape. nil when the
// output is neither.
func asContentBlocks(output any) []contentBlock {
	if b := blocksFromValue(output); b != nil {
		return b
	}
	if s, ok := output.(string); ok && mayBeContentBlocks(s) {
		if v, err := jsonx.ParseString(s); err == nil {
			return blocksFromValue(v)
		}
	}
	return nil
}

// mayBeContentBlocks cheaply rules out JSON strings that cannot hold content
// blocks before the order-preserving jsonx parse, which is far slower than
// encoding/json on large payloads. It only checks the shape blocksOf requires
// (a non-empty array of objects that all carry a string "type", top-level or
// under "content"); anything that passes is still parsed and judged by
// blocksFromValue, so the result is unchanged.
func mayBeContentBlocks(s string) bool {
	t := strings.TrimLeft(s, " \t\r\n")
	if t == "" || (t[0] != '[' && t[0] != '{') {
		return false
	}
	raw := []byte(t)
	if t[0] == '{' {
		var obj map[string]json.RawMessage
		if json.Unmarshal(raw, &obj) != nil {
			return false
		}
		if raw = obj["content"]; raw == nil {
			return false
		}
	}
	// Stream the array so the first element without a string "type" ends the
	// check without decoding the rest (a 300-issue list stops at issue one).
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('[') {
		return false
	}
	n := 0
	for ; dec.More(); n++ {
		var el map[string]json.RawMessage
		if dec.Decode(&el) != nil {
			return false
		}
		if typ := el["type"]; len(typ) == 0 || typ[0] != '"' {
			return false
		}
	}
	return n > 0
}

func hasNonText(blocks []contentBlock) bool {
	for _, b := range blocks {
		if b.typ != "text" {
			return true
		}
	}
	return false
}

func hasNonTextContentBlocks(output any) bool {
	b := asContentBlocks(output)
	return b != nil && hasNonText(b)
}

func joinTextBlocks(blocks []contentBlock) string {
	var texts []string
	for _, b := range blocks {
		if b.typ == "text" && b.hasText {
			texts = append(texts, b.text)
		}
	}
	return strings.Join(texts, "\n")
}

// payloadByteLength is the byte size of the raw payload, image bytes included.
func payloadByteLength(output any) int {
	if s, ok := output.(string); ok {
		return len(s)
	}
	return len(jsonx.Compact(output))
}

func isTopLevelArrayPayload(output any) bool {
	switch v := output.(type) {
	case []any:
		return true
	case string:
		return strings.HasPrefix(strings.TrimLeftFunc(v, unicode.IsSpace), "[")
	}
	return false
}

// contentBlockHandler strips incompressible non-text MCP blocks (screenshots,
// audio, embedded resources) and keeps the text. OriginalSize is the pre-strip
// payload: measuring the extracted text instead would make the summary no
// smaller than the original, so PostToolUse would skip and the screenshot would
// still land in the model window (upstream #270).
func contentBlockHandler(_ string, output any) Result {
	originalSize := payloadByteLength(output)
	blocks := asContentBlocks(output)
	text := ExtractText(output)
	if blocks != nil {
		text = joinTextBlocks(blocks)
	}
	var types []string
	seen := map[string]bool{}
	stripped := 0
	for _, b := range blocks {
		if b.typ == "text" {
			continue
		}
		stripped++
		if !seen[b.typ] {
			seen[b.typ] = true
			types = append(types, b.typ)
		}
	}
	if stripped == 0 {
		return Result{Summary: text, OriginalSize: originalSize}
	}
	label := "non-text"
	if len(types) == 1 {
		label = types[0]
	}
	plural := "s"
	if stripped == 1 {
		plural = ""
	}
	note := fmt.Sprintf("[stripped %d %s content block%s]", stripped, label, plural)
	if text != "" {
		return Result{Summary: text + "\n" + note, OriginalSize: originalSize}
	}
	return Result{Summary: note, OriginalSize: originalSize}
}

// ProfileText is the text a compression profile works on. ExtractText keeps a
// text-only top-level block array as JSON (for jsonHandler routing), so a
// profile would parse the blocks themselves and find none of its fields.
// Context7 and Sentry send that shape: unwrap it so a JSON body parses and
// markdown takes the fallback path (upstream #299).
func ProfileText(output any) string {
	if blocks := asContentBlocks(output); blocks != nil {
		for _, b := range blocks {
			if b.typ == "text" {
				return joinTextBlocks(blocks)
			}
		}
	}
	return ExtractText(output)
}
