package decode

import (
	"encoding/json"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRender(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"symbol", `{"symbol":"transfer"}`, "transfer"},
		{"string", `{"string":"hello world"}`, "hello world"},
		{"address", `{"address":"GABC"}`, "GABC"},
		{"bool", `{"bool":true}`, "true"},
		{"void", `{"void":null}`, "void"},
		{"u32 number", `{"u32":42}`, "42"},
		{"i128 decimal string", `{"i128":"1000000000000"}`, "1000000000000"},
		{"negative i128", `{"i128":"-1"}`, "-1"},
		{"bytes get a hex prefix", `{"bytes":"deadbeef"}`, "0xdeadbeef"},
		{"empty vec", `{"vec":[]}`, "[]"},
		{
			name: "vec of wrapped values",
			in:   `{"vec":[{"symbol":"a"},{"u32":1}]}`,
			want: "[a, 1]",
		},
		{
			name: "map renders as key: value",
			in:   `{"map":[{"key":{"symbol":"amount"},"val":{"i128":"5"}}]}`,
			want: "{amount: 5}",
		},
		{
			name: "nested vec inside map",
			in:   `{"map":[{"key":{"symbol":"ids"},"val":{"vec":[{"u32":1},{"u32":2}]}}]}`,
			want: "{ids: [1, 2]}",
		},
		{
			name: "unknown wrapper falls back to its content",
			in:   `{"unknown":"ScvError(...)"}`,
			want: "ScvError(...)",
		},
		{"json null", `null`, "null"},
		{"bare string", `"plain"`, "plain"},
		{"bare number", `7`, "7"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, Render(json.RawMessage(tt.in)))
		})
	}
}

func TestRenderDegradesGracefully(t *testing.T) {
	// Rendering is display-only, so malformed input must show something rather
	// than fail: the raw text is more useful to a reader than an error.
	t.Run("empty input", func(t *testing.T) {
		assert.Equal(t, "", Render(nil))
	})

	t.Run("invalid json returns the raw text", func(t *testing.T) {
		assert.Equal(t, "{not json", Render(json.RawMessage("{not json")))
	})

	t.Run("unrecognized object is rendered sorted", func(t *testing.T) {
		// Two keys, so it is not a wrapper; keys sort for stable output.
		assert.Equal(t, "{a: 1, b: 2}", Render(json.RawMessage(`{"b":2,"a":1}`)),
			"expected keys sorted regardless of input order")
	})
}

func TestRenderRespectsDepthLimit(t *testing.T) {
	// Build a value nested past MaxRenderDepth and confirm rendering
	// terminates rather than recursing without bound.
	nested := `{"u32":1}`
	for i := 0; i < MaxRenderDepth+5; i++ {
		nested = `{"vec":[` + nested + `]}`
	}

	got := Render(json.RawMessage(nested))
	assert.Contains(t, got, "…", "expected the depth limit to truncate")
}

func TestRenderTopics(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "typical transfer topics",
			in:   `[{"symbol":"transfer"},{"address":"GABC"},{"address":"GDEF"}]`,
			want: []string{"transfer", "GABC", "GDEF"},
		},
		{"empty array", `[]`, []string{}},
		{"nil input", ``, nil},
		{"not an array falls back to raw", `{"symbol":"x"}`, []string{`{"symbol":"x"}`}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RenderTopics(json.RawMessage(tt.in))
			if tt.want == nil {
				assert.Nil(t, got)
				return
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestEventName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "first topic symbol is the event name",
			in:   `[{"symbol":"transfer"},{"address":"GABC"}]`,
			want: "transfer",
		},
		{
			name: "a string first topic also counts",
			in:   `[{"string":"mint"}]`,
			want: "mint",
		},
		{
			name: "a non-symbol first topic has no name",
			in:   `[{"u32":1},{"symbol":"transfer"}]`,
			want: "",
		},
		{"empty topics", `[]`, ""},
		{"nil topics", ``, ""},
		{"malformed topics", `not json`, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, EventName(json.RawMessage(tt.in)))
		})
	}
}

func TestShorten(t *testing.T) {
	const (
		contract = "CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC"
		japanese = "日本語でスマートコントラクトのイベントを発行しています"
		emojis   = "🚀🌟🎉🔥💎⚡🎯🏆🎸🎺"
	)

	tests := []struct {
		name string
		in   string
		keep int
		want string
	}{
		{"long id is abbreviated", contract, 6, "CDLZFC…HGCYSC"},
		{"short strings pass through", "abc", 6, "abc"},
		{"exact 2*keep+1 runes passes through", "1234567", 3, "1234567"},
		{"zero keep passes through", contract, 0, contract},
		{"negative keep passes through", contract, -1, contract},
		{"japanese keep=4 cuts on rune boundaries", japanese, 4, "日本語で…ています"},
		{"japanese keep=7 cuts on rune boundaries", japanese, 7, "日本語でスマー…発行しています"},
		{"japanese keep=10 cuts on rune boundaries", japanese, 10, "日本語でスマートコン…ントを発行しています"},
		{"4-byte emojis cut on rune boundaries", emojis, 2, "🚀🌟…🎸🎺"},
		{"4-byte emojis short enough pass through", emojis, 5, emojis},
		{"multi-byte 2*keep runes passes through", "αβγδεζ", 3, "αβγδεζ"},
		{"multi-byte 2*keep+1 runes passes through", "αβγδεζη", 3, "αβγδεζη"},
		{"multi-byte 2*keep+2 runes abbreviates middle two runes", "αβγδεζηθ", 3, "αβγ…ζηθ"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Shorten(tt.in, tt.keep)
			assert.True(t, utf8.ValidString(got), "Shorten(%q, %d) produced invalid UTF-8: %q", tt.in, tt.keep, got)
			assert.Equal(t, tt.want, got)
		})
	}

	// Sweep every keep value across multi-byte strings to guarantee no cut
	// ever splits a rune.
	for _, sample := range []string{japanese, emojis, "αβγδεζηθικλμνξοπ"} {
		for keep := -1; keep <= len(sample); keep++ {
			got := Shorten(sample, keep)
			assert.True(t, utf8.ValidString(got), "invalid UTF-8 for keep=%d on %q: %q", keep, sample, got)
		}
	}
}

func TestTruncate(t *testing.T) {
	const (
		japanese = "日本語でイベントを発行しています"
		emojis   = "🚀🌟🎉🔥💎"
	)

	tests := []struct {
		name     string
		in       string
		maxRunes int
		want     string
	}{
		{"ascii within limit", "transfer", 28, "transfer"},
		{"ascii at exact limit", "transfer", 8, "transfer"},
		{"ascii truncated", "transfer", 5, "trans…"},
		{"japanese maxRunes=4", japanese, 4, "日本語で…"},
		{"japanese maxRunes=7", japanese, 7, "日本語でイベン…"},
		{"japanese maxRunes=10", japanese, 10, "日本語でイベントを発…"},
		{"4-byte emoji truncated", emojis, 3, "🚀🌟🎉…"},
		{"4-byte emoji within limit", emojis, 5, emojis},
		{"zero limit on non-empty", japanese, 0, "…"},
		{"empty string at zero limit", "", 0, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Truncate(tt.in, tt.maxRunes)
			assert.True(t, utf8.ValidString(got), "Truncate(%q, %d) produced invalid UTF-8: %q", tt.in, tt.maxRunes, got)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestTopicsValuePrefersNodeDecodedJSON(t *testing.T) {
	// When the node supports xdrFormat "json" there is nothing to decode
	// locally, and the decoder must not be consulted at all.
	topics, value, err := TopicsValue(failingDecoder{}, RawEvent{
		TopicJSON: []json.RawMessage{json.RawMessage(`{"symbol":"transfer"}`)},
		ValueJSON: json.RawMessage(`{"i128":"5"}`),
	})
	require.NoError(t, err)
	assert.JSONEq(t, `[{"symbol":"transfer"}]`, string(topics))
	assert.JSONEq(t, `{"i128":"5"}`, string(value))
}

func TestTopicsValueFallsBackToXDR(t *testing.T) {
	topics, value, err := TopicsValue(stubDecoder{}, RawEvent{
		Topic: []string{"topic-a", "topic-b"},
		Value: "the-value",
	})
	require.NoError(t, err)
	assert.JSONEq(t, `[{"symbol":"topic-a"},{"symbol":"topic-b"}]`, string(topics))
	assert.JSONEq(t, `{"symbol":"the-value"}`, string(value))
}

func TestTopicsValueWithNoValue(t *testing.T) {
	topics, value, err := TopicsValue(stubDecoder{}, RawEvent{})
	require.NoError(t, err)
	assert.JSONEq(t, `[]`, string(topics))
	assert.Equal(t, "null", string(value))
}

func TestTopicsValuePropagatesDecoderErrors(t *testing.T) {
	_, _, err := TopicsValue(failingDecoder{}, RawEvent{Topic: []string{"bad"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decoding topic 0")
}

// stubDecoder wraps whatever it is given as a symbol, so tests can assert on
// which path produced a value without depending on real XDR.
type stubDecoder struct{}

func (stubDecoder) DecodeScVal(s string) (json.RawMessage, error) {
	return json.Marshal(map[string]string{"symbol": s})
}

// failingDecoder fails on every call, proving a path never reaches it.
type failingDecoder struct{}

func (failingDecoder) DecodeScVal(string) (json.RawMessage, error) {
	return nil, assert.AnError
}
