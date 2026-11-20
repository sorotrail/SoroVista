package textutil

import (
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
)

func TestTruncateRunes(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"Japanese text", "日本語の文章", 4, "日本語の…"},
		{"four-byte character", "ab😀cd", 3, "ab😀…"},
		{"already within limit", "日本語", 3, "日本語"},
		{"zero limit", "日本語", 0, "…"},
		{"invalid UTF-8 is replaced", string([]byte{0xff, 'a'}), 2, "�a"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TruncateRunes(tt.in, tt.max)
			assert.Equal(t, tt.want, got)
			assert.True(t, utf8.ValidString(got))
		})
	}
}

func TestTruncateBytes(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"cuts before a partial rune", "日本語", 4, "日…"},
		{"four-byte character fits", "a😀b", 5, "a😀…"},
		{"already within limit", "日本語", 9, "日本語"},
		{"zero limit", "日本語", 0, "…"},
		{"invalid UTF-8 is replaced", string([]byte{0xff, 'a'}), 4, "�a"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TruncateBytes(tt.in, tt.max)
			assert.Equal(t, tt.want, got)
			assert.True(t, utf8.ValidString(got))
		})
	}
}

func TestShortenRunes(t *testing.T) {
	tests := []struct {
		name string
		in   string
		keep int
		want string
	}{
		{"keep four runes", "日本語の文章を安全に短く切り詰めます。追加文字列", 4, "日本語の…加文字列"},
		{"keep seven runes", "日本語の文章を安全に短く切り詰めます。追加文字列", 7, "日本語の文章を…す。追加文字列"},
		{"keep ten runes", "日本語の文章を安全に短く切り詰めます。追加文字列", 10, "日本語の文章を安全に…詰めます。追加文字列"},
		{"four-byte characters", "start😀middle🚀end", 5, "start…e🚀end"},
		{"invalid UTF-8 is replaced", string([]byte{'a', 0xff, 'b'}), 0, "a�b"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ShortenRunes(tt.in, tt.keep)
			assert.Equal(t, tt.want, got)
			assert.True(t, utf8.ValidString(got))
		})
	}
}
