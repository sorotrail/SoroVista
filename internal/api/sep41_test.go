package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sorotrail/sorovista/internal/source"
)

func TestDetectTokenEvent(t *testing.T) {
	transfer := source.Event{
		ID:   "ev-1",
		Type: "contract",
		Topics: json.RawMessage(`[
			{"symbol": "transfer"},
			{"address": "GA...SENDER"},
			{"address": "GA...RECIPIENT"}]`),
		Value: json.RawMessage(`{"i128": "1000000"}`),
	}
	info := DetectTokenEvent(transfer)
	if info == nil {
		t.Fatal("transfer-shaped event should be detected")
	}
	if info.Event != "transfer" || info.From != "GA...SENDER" || info.To != "GA...RECIPIENT" || info.Amount != "1000000" {
		t.Fatalf("got %+v", info)
	}

	// burn: from is the holder (topic 2), to is the admin (topic 1).
	burn := source.Event{
		Topics: json.RawMessage(`[
			{"symbol": "burn"},
			{"address": "GA...ADMIN"},
			{"address": "GA...HOLDER"}]`),
		Value: json.RawMessage(`{"i128": "500"}`),
	}
	info = DetectTokenEvent(burn)
	if info == nil || info.From != "GA...HOLDER" || info.To != "GA...ADMIN" {
		t.Fatalf("burn slots wrong: %+v", info)
	}

	// set_admin: no amount.
	setAdmin := source.Event{
		Topics: json.RawMessage(`[
			{"symbol": "set_admin"},
			{"address": "GA...OLD"},
			{"address": "GA...NEW"}]`),
	}
	info = DetectTokenEvent(setAdmin)
	if info == nil || info.Event != "set_admin" || info.Amount != "" {
		t.Fatalf("set_admin wrong: %+v", info)
	}

	// Non-token events: wrong name, too few topics, non-address slots.
	for name, ev := range map[string]source.Event{
		"unknown event name": {Topics: json.RawMessage(`[{"symbol":"swap"},{"address":"A"},{"address":"B"}]`)},
		"too few topics":     {Topics: json.RawMessage(`[{"symbol":"transfer"}]`)},
		"non-address slots":  {Topics: json.RawMessage(`[{"symbol":"transfer"},{"symbol":"x"},{"address":"B"}]`)},
	} {
		if info := DetectTokenEvent(ev); info != nil {
			t.Fatalf("%s: should not be detected, got %+v", name, info)
		}
	}
}

func TestDetectTokenEventPreservesIntegerAmounts(t *testing.T) {
	tests := []struct {
		name   string
		value  string
		amount string
	}{
		{"i128 above float precision", `{"i128":9007199254740993}`, "9007199254740993"},
		{"large i128", `{"i128":123456789012345678901}`, "123456789012345678901"},
		{"u64", `{"u64":18446744073709551615}`, "18446744073709551615"},
		{"i64", `{"i64":-9223372036854775808}`, "-9223372036854775808"},
		{"u128", `{"u128":340282366920938463463374607431768211455}`, "340282366920938463463374607431768211455"},
		{"numeric string", `{"i128":"123456789012345678901"}`, "123456789012345678901"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := source.Event{
				Topics: json.RawMessage(`[{"symbol":"transfer"},{"address":"FROM"},{"address":"TO"}]`),
				Value:  json.RawMessage(tt.value),
			}
			info := DetectTokenEvent(ev)
			require.NotNil(t, info)
			assert.Equal(t, tt.amount, info.Amount)
		})
	}

	for name, value := range map[string]string{
		"decimal":   `{"i128":1.5}`,
		"exponent":  `{"i128":1e3}`,
		"null":      `{"i128":null}`,
		"not a map": `null`,
	} {
		t.Run(name+" amount is omitted", func(t *testing.T) {
			ev := source.Event{
				Topics: json.RawMessage(`[{"symbol":"transfer"},{"address":"FROM"},{"address":"TO"}]`),
				Value:  json.RawMessage(value),
			}
			info := DetectTokenEvent(ev)
			require.NotNil(t, info)
			assert.Empty(t, info.Amount)
		})
	}
}

func TestEventsResponseCarriesTokenAnnotation(t *testing.T) {
	events := []source.Event{
		{
			ID:     "ev-1",
			Topics: json.RawMessage(`[{"symbol":"transfer"},{"address":"GA...S"},{"address":"GA...R"}]`),
			Value:  json.RawMessage(`{"i128": "7"}`),
		},
		{
			ID:     "ev-2",
			Topics: json.RawMessage(`[{"symbol":"swap"},{"address":"GA...S"}]`),
		},
	}
	src := &fakeSource{events: source.EventPage{Events: events, NextCursor: "c1"}}
	srv := httptest.NewServer(New(src, discardLogger()).Routes())
	defer srv.Close()

	res, err := http.Get(srv.URL + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()

	var page struct {
		Events []struct {
			ID    string     `json:"id"`
			Token *TokenInfo `json:"token"`
		} `json:"events"`
		NextCursor string `json:"next_cursor"`
	}
	if err := json.NewDecoder(res.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 2 {
		t.Fatalf("got %d events", len(page.Events))
	}
	if page.Events[0].Token == nil || page.Events[0].Token.Event != "transfer" {
		t.Fatalf("token event not annotated: %+v", page.Events[0].Token)
	}
	if page.Events[1].Token != nil {
		t.Fatalf("non-token event annotated: %+v", page.Events[1].Token)
	}
	if page.NextCursor != "c1" {
		t.Fatalf("cursor lost: %q", page.NextCursor)
	}
}
