// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/handlers/jsonhandler.go
//
// JSON handler — the fallback for unrecognised JSON tool outputs. Spends a
// character budget over the document instead of cutting at a fixed depth
// (upstream #403): wrapper levels cost only their keys, a string is cut rather
// than dropped and says how much it lost, an array shows as many items as fit
// and counts the rest, and fields every item of an array shares are stated
// once. Numbers, booleans and null are always kept. The summary is compact
// JSON. Sizes are UTF-16 code units, as JSON.stringify(v).length counts them.

package handlers

import (
	"fmt"
	"math"
	"sort"
	"unicode/utf16"

	"mcprecall/internal/jsonx"
)

// jsonBudget is the summary's size, in characters of compact JSON.
const jsonBudget = 4096

const (
	// jsonShareFloor: each array item or field gets at least this much of what
	// is left, so short items show whole instead of every item being starved
	// to a stub.
	jsonShareFloor = 512
	// jsonMinPart: below this an item or field is not started; the rest are
	// counted instead.
	jsonMinPart = 24
)

// utf16Len is the length of s in UTF-16 code units (JavaScript's .length).
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

func jsonSize(v any) float64 { return float64(utf16Len(jsonx.Compact(v))) }

func fitString(s string, budget float64) string {
	if jsonSize(s) <= budget {
		return s
	}
	units := utf16.Encode([]rune(s))
	// Escapes make the JSON longer than the string, so shrink until it fits.
	// Shares of a budget are fractional; a character count is not.
	keep := max(0, int(math.Floor(budget-24)))
	for {
		k := min(keep, len(units))
		if k > 0 && utf16.IsSurrogate(rune(units[k-1])) && units[k-1] < 0xdc00 {
			k-- // never split a surrogate pair
		}
		cut := fmt.Sprintf("%s…[+%d chars]", string(utf16.Decode(units[:k])), len(units)-k)
		if jsonSize(cut) <= budget || keep == 0 {
			return cut
		}
		keep = max(0, keep-max(1, int(math.Ceil(jsonSize(cut)-budget))))
	}
}

// jsonShare is the share of remaining the next of rest parts may use.
func jsonShare(remaining float64, rest int) float64 {
	return math.Max(remaining/float64(rest), math.Min(remaining, jsonShareFloor))
}

// sharedFields are the fields with the same value in every item of an array
// of objects, or nil when there are none.
func sharedFields(items []any) *jsonx.Obj {
	if len(items) < 2 {
		return nil
	}
	objs := make([]*jsonx.Obj, len(items))
	for i, it := range items {
		o, ok := it.(*jsonx.Obj)
		if !ok {
			return nil
		}
		objs[i] = o
	}
	shared := jsonx.NewObj()
	for _, k := range objs[0].Keys() {
		v, _ := objs[0].Get(k)
		want := jsonx.Compact(v)
		same := true
		for _, o := range objs[1:] {
			ov, ok := o.Get(k)
			if !ok || jsonx.Compact(ov) != want {
				same = false
				break
			}
		}
		if same {
			shared.Set(k, v)
		}
	}
	if shared.Len() == 0 {
		return nil
	}
	return shared
}

func fitArray(items []any, budget float64) []any {
	shared := sharedFields(items)
	out := []any{}
	parts := items
	if shared != nil {
		// Budgeted like any value, and to a quarter of the array's, so a large
		// shared field leaves room for the items.
		note := jsonx.NewObj()
		note.Set(fmt.Sprintf("…each of %d items has", len(items)), shared)
		out = append(out, fitValue(note, budget/4))
		parts = make([]any, len(items))
		for i, it := range items {
			o := it.(*jsonx.Obj)
			rest := jsonx.NewObj()
			for _, k := range o.Keys() {
				if _, isShared := shared.Get(k); !isShared {
					v, _ := o.Get(k)
					rest.Set(k, v)
				}
			}
			parts[i] = rest
		}
	}
	// Brackets, plus room for the "…N more" note.
	remaining := budget - jsonSize(out) - 16
	shown := 0
	for ; shown < len(parts) && remaining >= jsonMinPart; shown++ {
		itemBudget := jsonShare(remaining, len(parts)-shown) - 1
		// Near the end of the budget an item that does not fit whole would be
		// a stub; count it instead.
		if jsonSize(parts[shown]) > itemBudget && itemBudget < jsonShareFloor {
			break
		}
		fitted := fitValue(parts[shown], itemBudget)
		used := jsonSize(fitted) + 1
		if used > remaining {
			break
		}
		out = append(out, fitted)
		remaining -= used
	}
	if shown < len(parts) {
		out = append(out, fmt.Sprintf("…%d more", len(parts)-shown))
	}
	return out
}

// allocate gives each field its budget, filling from the smallest: a field
// that needs less than an even split takes only what it needs and leaves the
// rest to larger ones, so a wrapper's one big field gets nearly all of it.
func allocate(needs []float64, budget float64) []float64 {
	out := make([]float64, len(needs))
	order := make([]int, len(needs))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return needs[order[a]] < needs[order[b]] })
	remaining := budget
	for n, i := range order {
		out[i] = math.Min(needs[i], remaining/float64(len(order)-n))
		remaining -= out[i]
	}
	return out
}

func fitObject(obj *jsonx.Obj, budget float64) *jsonx.Obj {
	keys := obj.Keys()
	keyCosts := make([]float64, len(keys))
	needs := make([]float64, len(keys))
	for i, k := range keys {
		v, _ := obj.Get(k)
		keyCosts[i] = jsonSize(k) + 2 // key, colon and comma
		needs[i] = keyCosts[i] + jsonSize(v)
	}
	// Braces, plus room for the "…more fields" note.
	available := budget - 2 - 24
	budgets := allocate(needs, available)
	for i := range keys {
		// Too many fields to give each a usable share: show them in order, as
		// an array does.
		if budgets[i] < needs[i] && budgets[i]-keyCosts[i] < jsonMinPart {
			return fitFieldsInOrder(obj, available)
		}
	}
	out := jsonx.NewObj()
	for i, k := range keys {
		v, _ := obj.Get(k)
		out.Set(k, fitValue(v, budgets[i]-keyCosts[i]))
	}
	return out
}

func fitFieldsInOrder(obj *jsonx.Obj, budget float64) *jsonx.Obj {
	keys := obj.Keys()
	out := jsonx.NewObj()
	remaining := budget
	shown := 0
	for ; shown < len(keys) && remaining >= jsonMinPart; shown++ {
		k := keys[shown]
		v, _ := obj.Get(k)
		keyCost := jsonSize(k) + 2
		valueBudget := jsonShare(remaining, len(keys)-shown) - keyCost
		if jsonSize(v) > valueBudget && valueBudget < jsonShareFloor {
			break
		}
		fitted := fitValue(v, valueBudget)
		used := keyCost + jsonSize(fitted)
		if used > remaining {
			break
		}
		out.Set(k, fitted)
		remaining -= used
	}
	if shown < len(keys) {
		out.Set("…more fields", float64(len(keys)-shown))
	}
	return out
}

// fitValue returns value within budget characters of compact JSON, or as
// close as its scalars allow.
func fitValue(value any, budget float64) any {
	if jsonSize(value) <= budget {
		return value
	}
	switch t := value.(type) {
	case string:
		return fitString(t, budget)
	case []any:
		return fitArray(t, budget)
	case *jsonx.Obj:
		return fitObject(t, budget)
	}
	return value
}

// jsonHandler summarises unrecognised JSON within jsonBudget characters.
func jsonHandler(_ string, output any) Result {
	raw := ExtractText(output)
	originalSize := byteLen(raw)

	parsed, err := jsonx.ParseString(raw)
	if err != nil {
		excerpt := trimEnd(firstChars(raw, 500))
		if runeLen(excerpt) < runeLen(raw) {
			return Result{Summary: excerpt + "\n…", OriginalSize: originalSize}
		}
		return Result{Summary: excerpt, OriginalSize: originalSize}
	}

	// Compact (no indentation) — smaller inline summary, keys kept verbatim.
	return Result{Summary: jsonx.Compact(fitValue(parsed, jsonBudget)), OriginalSize: originalSize}
}
