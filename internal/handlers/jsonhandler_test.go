// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/handlers/jsonhandler_test.go
// Covers the JSON fallback's character budget (upstream #403).

package handlers

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func envelope(result any) map[string]any {
	return map[string]any{"results": []any{map[string]any{"index": 0, "status": "success", "result": result}}}
}

func repo(i int) map[string]any {
	return map[string]any{
		"type": "repo", "path": fmt.Sprintf("owner%d/dataset-number-%d", i, i), "repo_type": "dataset",
		"private": false, "likes": i, "updated_at": fmt.Sprintf("2026-10-%02d", 1+i%28),
	}
}

func repos(n int) []any {
	out := make([]any, n)
	for i := range out {
		out[i] = repo(i)
	}
	return out
}

// jsonSummary runs the handler on v. Go's encoding/json sorts map keys, so
// fixtures built from maps arrive in sorted key order.
func jsonSummary(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return jsonHandler("mcp__some__tool", string(raw)).Summary
}

func decode(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("summary is not valid JSON: %v\n%s", err, s)
	}
	return v
}

func roundTrip(t *testing.T, v any) any {
	raw, _ := json.Marshal(v)
	return decode(t, string(raw))
}

func TestJSONKeepsEveryValueWithinBudget(t *testing.T) {
	obj := envelope(map[string]any{"uri": "hf://datasets/a/b", "op": "ls", "entries": []any{map[string]any{"a": map[string]any{"b": map[string]any{"c": map[string]any{"d": "deep"}}}}}})
	if got := decode(t, jsonSummary(t, obj)); !reflect.DeepEqual(got, roundTrip(t, obj)) {
		t.Errorf("document changed: %v", got)
	}
}

func TestJSONEmitsCompactValidJSON(t *testing.T) {
	s := jsonSummary(t, envelope(map[string]any{"entries": repos(300)}))
	if strings.Contains(s, "\n") {
		t.Error("summary is not compact")
	}
	decode(t, s)
}

func TestJSONStaysWithinBudget(t *testing.T) {
	s := jsonSummary(t, envelope(map[string]any{"entries": repos(300), "content": strings.Repeat("x", 50000)}))
	if n := utf16Len(s); n > jsonBudget {
		t.Errorf("summary is %d chars", n)
	}
}

func TestJSONCutsLongStringKeepingStartAndCount(t *testing.T) {
	var lines []string
	for i := 0; i < 2000; i++ {
		lines = append(lines, fmt.Sprintf("line %d of the dataset card", i))
	}
	content := strings.Join(lines, "\n")
	s := jsonSummary(t, envelope(map[string]any{"uri": "hf://x/README.md", "content": content}))
	kept := decode(t, s).(map[string]any)["results"].([]any)[0].(map[string]any)["result"].(map[string]any)["content"].(string)
	m := regexp.MustCompile(`…\[\+(\d+) chars\]$`).FindStringSubmatch(kept)
	if m == nil {
		t.Fatalf("no cut marker: %q", kept[len(kept)-40:])
	}
	if !strings.HasPrefix(kept, "line 0 of the dataset card\nline 1 of") {
		t.Error("start not kept")
	}
	var cut int
	fmt.Sscan(m[1], &cut)
	if prefix := utf16Len(kept[:strings.Index(kept, "…[+")]); prefix+cut != utf16Len(content) {
		t.Errorf("kept %d + cut %d != %d", prefix, cut, utf16Len(content))
	}
	if float64(utf16Len(s)) <= jsonBudget*0.9 {
		t.Errorf("summary only %d chars", utf16Len(s))
	}
}

func TestJSONShowsArrayItemsThatFitAndCountsRest(t *testing.T) {
	shown := decode(t, jsonSummary(t, map[string]any{"entries": repos(300)})).(map[string]any)["entries"].([]any)
	items := 0
	for _, e := range shown {
		if o, ok := e.(map[string]any); ok {
			if _, ok := o["path"]; ok {
				items++
			}
		}
	}
	if items <= 20 {
		t.Errorf("only %d items shown", items)
	}
	if more := shown[len(shown)-1]; more != fmt.Sprintf("…%d more", 300-items) {
		t.Errorf("tail = %v", more)
	}
}

func TestJSONStatesSharedFieldsOnce(t *testing.T) {
	shown := decode(t, jsonSummary(t, map[string]any{"entries": repos(300)})).(map[string]any)["entries"].([]any)
	want0 := map[string]any{"…each of 300 items has": map[string]any{"type": "repo", "repo_type": "dataset", "private": false}}
	if !reflect.DeepEqual(shown[0], want0) {
		t.Errorf("shared = %v", shown[0])
	}
	want1 := map[string]any{"path": "owner0/dataset-number-0", "likes": float64(0), "updated_at": "2026-10-01"}
	if !reflect.DeepEqual(shown[1], want1) {
		t.Errorf("first item = %v", shown[1])
	}
}

func TestJSONBudgetsLargeSharedFields(t *testing.T) {
	schema := strings.Repeat("column definition ", 2000)
	rows := make([]any, 300)
	for i := range rows {
		rows[i] = map[string]any{"id": i, "schema": schema}
	}
	s := jsonSummary(t, map[string]any{"rows": rows})
	if n := utf16Len(s); n > jsonBudget {
		t.Errorf("summary is %d chars", n)
	}
	shown := decode(t, s).(map[string]any)["rows"].([]any)
	shared := shown[0].(map[string]any)["…each of 300 items has"].(map[string]any)["schema"].(string)
	if !regexp.MustCompile(`^column definition .*…\[\+\d+ chars\]$`).MatchString(shared) {
		t.Errorf("shared schema = %q", shared)
	}
	if !reflect.DeepEqual(shown[1], map[string]any{"id": float64(0)}) {
		t.Errorf("first row = %v", shown[1])
	}
}

func TestJSONShowsFirstFieldsOfWideObject(t *testing.T) {
	// Built by hand to keep field order: name_0, name_1, …
	var b strings.Builder
	b.WriteByte('{')
	for i := 0; i < 2000; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `"name_%d":{"id":%d,"title":"item number %d"}`, i, i, i)
	}
	b.WriteByte('}')
	shown := decode(t, jsonHandler("mcp__some__tool", b.String()).Summary).(map[string]any)
	if !reflect.DeepEqual(shown["name_0"], map[string]any{"id": float64(0), "title": "item number 0"}) {
		t.Errorf("name_0 = %v", shown["name_0"])
	}
	fields := 0
	for k := range shown {
		if strings.HasPrefix(k, "name_") {
			fields++
		}
	}
	if fields <= 50 {
		t.Errorf("only %d fields", fields)
	}
	if shown["…more fields"] != float64(2000-fields) {
		t.Errorf("more fields = %v, want %d", shown["…more fields"], 2000-fields)
	}
}

func TestJSONPreservesSmallDocument(t *testing.T) {
	obj := map[string]any{"items": []any{1, 2}, "nested": map[string]any{"list": []any{map[string]any{"a": 1}, map[string]any{"a": 1}}}}
	if got := decode(t, jsonSummary(t, obj)); !reflect.DeepEqual(got, roundTrip(t, obj)) {
		t.Errorf("document changed: %v", got)
	}
}

func TestJSONOriginalSizeInBytes(t *testing.T) {
	if r := jsonHandler("x", `{"x":1}`); r.OriginalSize != 7 {
		t.Errorf("originalSize = %d", r.OriginalSize)
	}
	if s := jsonHandler("x", "not json at all").Summary; !strings.Contains(s, "not json at all") {
		t.Errorf("excerpt = %q", s)
	}
}

func TestFitStringCountsUTF16Units(t *testing.T) {
	s := strings.Repeat("😀", 100) // 200 UTF-16 units
	cut := fitString(s, 60)
	if n := utf16Len(`"` + cut + `"`); n > 60 {
		t.Errorf("cut is %d units", n)
	}
	if !strings.Contains(cut, "chars]") || strings.ContainsRune(cut, '�') {
		t.Errorf("cut = %q", cut)
	}
}
