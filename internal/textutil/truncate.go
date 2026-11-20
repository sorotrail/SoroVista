package textutil

import (
	"strings"
	"unicode/utf8"
)

// TruncateRunes keeps at most maxRunes Unicode code points from the start of s
// and appends an ellipsis when truncation is needed.
func TruncateRunes(s string, maxRunes int) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	runes := []rune(s)
	if maxRunes < 0 {
		maxRunes = 0
	}
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "…"
}

// TruncateBytes keeps at most maxBytes bytes from the start of s, backs up to
// a UTF-8 boundary if needed, and appends an ellipsis when truncation is needed.
func TruncateBytes(s string, maxBytes int) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	if maxBytes < 0 {
		maxBytes = 0
	}
	if len(s) <= maxBytes {
		return s
	}

	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// ShortenRunes keeps keep Unicode code points from each end of s, separated
// by an ellipsis, when the string is long enough to abbreviate.
func ShortenRunes(s string, keep int) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	runes := []rune(s)
	if keep <= 0 || keep >= len(runes) || len(runes)-keep <= keep+1 {
		return s
	}
	return string(runes[:keep]) + "…" + string(runes[len(runes)-keep:])
}
