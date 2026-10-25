// Package jsonx is an order-preserving JSON value model that reproduces
// JavaScript's JSON.parse + JSON.stringify semantics, so mcp-recall's Go
// handlers emit byte-identical output to the original TypeScript.
//
// Values are one of: nil, bool, float64, string, []any, *Obj.
// Objects preserve insertion order (Go maps do not), and Compact/Indent
// reproduce JSON.stringify's escaping and number formatting.
package jsonx

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// Obj is an insertion-ordered JSON object.
type Obj struct {
	keys   []string
	values map[string]any
}

// NewObj returns an empty ordered object.
func NewObj() *Obj { return &Obj{values: map[string]any{}} }

// Set inserts or updates a key, preserving first-insertion order.
func (o *Obj) Set(key string, v any) {
	if _, ok := o.values[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.values[key] = v
}

// Get returns the value for key and whether it was present.
func (o *Obj) Get(key string) (any, bool) {
	v, ok := o.values[key]
	return v, ok
}

// Keys returns the keys in insertion order.
func (o *Obj) Keys() []string { return o.keys }

// Str returns the string value for key, and whether it was present as a string.
func (o *Obj) Str(key string) (string, bool) {
	v, ok := o.Get(key)
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// Len returns the number of keys.
func (o *Obj) Len() int { return len(o.keys) }

// Delete removes a key, preserving the order of the remaining keys.
func (o *Obj) Delete(key string) {
	if _, ok := o.values[key]; !ok {
		return
	}
	delete(o.values, key)
	for i, k := range o.keys {
		if k == key {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

// Clone returns a shallow copy: keys and values are copied, nested values shared.
func (o *Obj) Clone() *Obj {
	c := &Obj{keys: append([]string(nil), o.keys...), values: make(map[string]any, len(o.values))}
	for k, v := range o.values {
		c.values[k] = v
	}
	return c
}

// MarshalJSON renders the object like JSON.stringify, keeping key order, so an
// Obj can be embedded in a struct encoded with encoding/json.
func (o *Obj) MarshalJSON() ([]byte, error) { return []byte(Compact(o)), nil }

// Parse decodes JSON into an order-preserving Value.
func Parse(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := parseValue(dec)
	if err != nil {
		return nil, err
	}
	return v, nil
}

// ParseString is Parse for a string input.
func ParseString(s string) (any, error) { return Parse([]byte(s)) }

func parseValue(dec *json.Decoder) (any, error) {
	t, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch tok := t.(type) {
	case json.Delim:
		switch tok {
		case '{':
			return parseObject(dec)
		case '[':
			return parseArray(dec)
		}
	case string:
		return tok, nil
	case json.Number:
		f, err := tok.Float64()
		if err != nil {
			return nil, err
		}
		return f, nil
	case bool:
		return tok, nil
	case nil:
		return nil, nil
	}
	return nil, nil
}

func parseObject(dec *json.Decoder) (any, error) {
	o := NewObj()
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key := keyTok.(string)
		val, err := parseValue(dec)
		if err != nil {
			return nil, err
		}
		o.Set(key, val)
	}
	// consume closing '}'
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	return o, nil
}

func parseArray(dec *json.Decoder) (any, error) {
	arr := []any{}
	for dec.More() {
		val, err := parseValue(dec)
		if err != nil {
			return nil, err
		}
		arr = append(arr, val)
	}
	// consume closing ']'
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	return arr, nil
}

// Compact renders a Value like JavaScript JSON.stringify(v).
func Compact(v any) string {
	var b strings.Builder
	writeValue(&b, v)
	return b.String()
}

// Indent renders a Value like JavaScript JSON.stringify(v, null, 2).
func Indent(v any) string {
	var b strings.Builder
	writeIndent(&b, v, "")
	return b.String()
}

func writeValue(b *strings.Builder, v any) {
	switch t := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if t {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case float64:
		b.WriteString(Number(t))
	case string:
		writeString(b, t)
	case []any:
		b.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			writeValue(b, e)
		}
		b.WriteByte(']')
	case *Obj:
		b.WriteByte('{')
		for i, k := range t.keys {
			if i > 0 {
				b.WriteByte(',')
			}
			writeString(b, k)
			b.WriteByte(':')
			writeValue(b, t.values[k])
		}
		b.WriteByte('}')
	}
}

func writeIndent(b *strings.Builder, v any, cur string) {
	switch t := v.(type) {
	case []any:
		if len(t) == 0 {
			b.WriteString("[]")
			return
		}
		next := cur + "  "
		b.WriteString("[\n")
		for i, e := range t {
			if i > 0 {
				b.WriteString(",\n")
			}
			b.WriteString(next)
			writeIndent(b, e, next)
		}
		b.WriteByte('\n')
		b.WriteString(cur)
		b.WriteByte(']')
	case *Obj:
		if len(t.keys) == 0 {
			b.WriteString("{}")
			return
		}
		next := cur + "  "
		b.WriteString("{\n")
		for i, k := range t.keys {
			if i > 0 {
				b.WriteString(",\n")
			}
			b.WriteString(next)
			writeString(b, k)
			b.WriteString(": ")
			writeIndent(b, t.values[k], next)
		}
		b.WriteByte('\n')
		b.WriteString(cur)
		b.WriteByte('}')
	default:
		writeValue(b, v)
	}
}

// writeString escapes a string like JSON.stringify: only ", \, and control
// characters are escaped. Notably < > & and non-ASCII are left as-is (unlike
// Go's encoding/json, which HTML-escapes them).
func writeString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			if r < 0x20 {
				b.WriteString(`\u`)
				const hex = "0123456789abcdef"
				b.WriteByte('0')
				b.WriteByte('0')
				b.WriteByte(hex[(r>>4)&0xf])
				b.WriteByte(hex[r&0xf])
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}

// Number formats a float like JavaScript's Number.toString / JSON.stringify.
// Integers within the safe range print without a decimal point; other values
// use the shortest round-tripping representation.
func Number(f float64) string {
	if f == float64(int64(f)) && f < 9.007199254740992e15 && f > -9.007199254740992e15 {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}
