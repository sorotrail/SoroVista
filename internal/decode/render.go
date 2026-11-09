package decode

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// MaxRenderDepth bounds nesting when rendering, so a pathological deeply
// nested value cannot blow the stack while formatting a page.
const MaxRenderDepth = 12

// Render turns one decoded ScVal — in the RPC's single-key wrapper form, e.g.
// {"symbol":"transfer"} or {"i128":"1000"} — into a short human-readable
// string for display. It never fails: anything unrecognized falls back to its
// compact JSON, because showing raw JSON is more useful than showing an error.
//
// contributors: this is the decoded-event renderer extension point. To format
// a known event standard (say SEP-41 amounts scaled by a token's decimals),
// add a renderer that wraps this one rather than special-casing here.
func Render(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	return renderValue(v, 0)
}

// RenderTopics renders a topics array into one string per topic. A nil or
// malformed array yields nil rather than an error.
func RenderTopics(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return []string{string(raw)}
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, Render(item))
	}
	return out
}

// EventName returns the conventional Soroban event name: the first topic, when
// it is a symbol or string. It returns "" when the topics do not follow the
// convention, which callers should display as an unnamed event rather than
// treating as an error.
func EventName(topics json.RawMessage) string {
	if len(topics) == 0 {
		return ""
	}
	var items []json.RawMessage
	if err := json.Unmarshal(topics, &items); err != nil || len(items) == 0 {
		return ""
	}
	var wrapper map[string]any
	if err := json.Unmarshal(items[0], &wrapper); err != nil || len(wrapper) != 1 {
		return ""
	}
	for _, key := range []string{"symbol", "string"} {
		if s, ok := wrapper[key].(string); ok {
			return s
		}
	}
	return ""
}

func renderValue(v any, depth int) string {
	if depth > MaxRenderDepth {
		return "…"
	}

	switch t := v.(type) {
	case nil:
		return "null"
	case bool:
		return fmt.Sprint(t)
	case string:
		return t
	case float64:
		// Encoding/json numbers: render integers without a decimal point.
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t))
		}
		return fmt.Sprint(t)
	case []any:
		parts := make([]string, 0, len(t))
		for _, item := range t {
			parts = append(parts, renderValue(item, depth+1))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case map[string]any:
		return renderObject(t, depth)
	default:
		return fmt.Sprint(t)
	}
}

// renderObject handles the single-key ScVal wrappers plus the {key,val} pairs
// used inside a map.
func renderObject(m map[string]any, depth int) string {
	if len(m) == 1 {
		for key, inner := range m {
			switch key {
			case "void":
				return "void"
			case "bool", "u32", "i32", "u64", "i64", "timepoint", "duration",
				"u128", "i128", "u256", "i256", "string", "symbol", "address":
				return renderValue(inner, depth+1)
			case "bytes":
				if s, ok := inner.(string); ok {
					return "0x" + s
				}
				return renderValue(inner, depth+1)
			case "vec":
				return renderValue(inner, depth+1)
			case "map":
				return renderMap(inner, depth)
			case "unknown":
				return renderValue(inner, depth+1)
			}
		}
	}

	// A {key, val} pair, or any shape the wrappers do not cover: render it as
	// sorted k: v so output stays stable across runs.
	if k, ok := m["key"]; ok && len(m) == 2 {
		if val, ok := m["val"]; ok {
			return renderValue(k, depth+1) + ": " + renderValue(val, depth+1)
		}
	}

	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+": "+renderValue(m[k], depth+1))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func renderMap(inner any, depth int) string {
	entries, ok := inner.([]any)
	if !ok {
		return renderValue(inner, depth+1)
	}
	parts := make([]string, 0, len(entries))
	for _, e := range entries {
		parts = append(parts, renderValue(e, depth+1))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// runePrefixEnd walks up to maxRunes UTF-8 code points from the start of s
// without allocating a []rune slice, returning the byte offset where that
// prefix ends and whether the entire string fit within maxRunes.
func runePrefixEnd(s string, maxRunes int) (int, bool) {
	if maxRunes <= 0 {
		return 0, len(s) == 0
	}
	pos := 0
	for i := 0; i < maxRunes && pos < len(s); i++ {
		_, width := utf8.DecodeRuneInString(s[pos:])
		pos += width
	}
	return pos, pos >= len(s)
}

// Truncate caps s at maxRunes UTF-8 code points (runes) for single-cell table
// display, appending "…" when runes are dropped. The limit is in runes rather
// than bytes so multi-byte characters do not shrink a table cell unpredictably
// or get split mid-character into invalid UTF-8. Grapheme clusters (such as a
// combining accent on a base character) are not kept together.
func Truncate(s string, maxRunes int) string {
	end, allFit := runePrefixEnd(s, maxRunes)
	if allFit {
		return s
	}
	return s[:end] + "…"
}

// Shorten abbreviates a long identifier for table display, keeping keepRunes
// UTF-8 code points (runes) at the head and tail so it remains recognizable:
// CABCDEF…UVWXYZ. Strings with at most keepRunes*2+1 runes (or non-positive
// keepRunes) are returned unchanged. Both ends cut on rune boundaries so
// multi-byte characters are never split into invalid UTF-8; grapheme clusters
// are not kept together.
func Shorten(s string, keepRunes int) string {
	if keepRunes <= 0 || len(s) <= keepRunes*2+1 {
		return s
	}
	headEnd, allFit := runePrefixEnd(s, keepRunes)
	if allFit {
		return s
	}
	tailStart := len(s)
	for i := 0; i < keepRunes; i++ {
		_, width := utf8.DecodeLastRuneInString(s[:tailStart])
		tailStart -= width
		if tailStart <= headEnd {
			return s
		}
	}
	// If only a single rune sits between the head and tail, replacing it with
	// "…" would not make the string any shorter.
	if _, width := utf8.DecodeRuneInString(s[headEnd:tailStart]); headEnd+width >= tailStart {
		return s
	}
	return s[:headEnd] + "…" + s[tailStart:]
}
