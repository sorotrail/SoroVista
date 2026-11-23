package api

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sorotrail/sorovista/internal/source"
)

// pagedSource scripts a sequence of pages keyed by the cursor that requests
// them, so export tests can prove the handler walks the whole result rather
// than returning the first page.
type pagedSource struct {
	fakeSource
	pages   map[string]source.EventPage
	cursors []string
}

func (p *pagedSource) ListEvents(_ context.Context, q source.EventQuery) (source.EventPage, error) {
	p.lastEventQuery = q
	p.cursors = append(p.cursors, q.Cursor)
	return p.pages[q.Cursor], p.err
}

func exportEvent(id string) source.Event {
	return source.Event{
		ID:               id,
		ContractID:       testContract,
		Ledger:           3947332,
		Type:             "contract",
		TxHash:           "abc123",
		TxIndex:          2,
		OpIndex:          1,
		InSuccessfulCall: true,
		Topics:           json.RawMessage(`[{"symbol":"transfer"}]`),
		Value:            json.RawMessage(`{"i128":"1000"}`),
		LedgerClosedAt:   time.Date(2026, 8, 2, 12, 43, 57, 0, time.UTC),
		CreatedAt:        time.Date(2026, 8, 3, 12, 28, 52, 0, time.UTC),
	}
}

// TestExportCSVHeaderAndQuoting covers the stable header and the awkward
// cells: a decoded topic can contain a comma, a quote or a newline, which a
// naive writer would corrupt.
func TestExportCSVHeaderAndQuoting(t *testing.T) {
	tricky := `[{"symbol":"a,b"},{"symbol":"say \"hi\""},{"symbol":"line\nbreak"}]`
	ev := exportEvent("0016880389704384512-0000000003")
	ev.Topics = json.RawMessage(tricky)
	ev.Value = json.RawMessage(`{"symbol":"value,with \"quotes\"\nand newline"}`)

	src := &fakeSource{events: source.EventPage{Events: []source.Event{ev}}}
	rec := do(t, testServer(src), "/api/events/export?format=csv")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "text/csv; charset=utf-8", rec.Header().Get("Content-Type"))

	disposition := rec.Header().Get("Content-Disposition")
	assert.Contains(t, disposition, "attachment")
	assert.Contains(t, disposition, ".csv")

	rows, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
	require.NoError(t, err)
	require.Len(t, rows, 2, "expected a header row and one data row")
	assert.Equal(t, csvColumns, rows[0])

	row := rows[1]
	require.Len(t, row, len(csvColumns))
	assert.Equal(t, ev.ID, row[0])
	assert.Equal(t, testContract, row[1])
	assert.Equal(t, "3947332", row[2])
	assert.Equal(t, "contract", row[3])
	assert.Equal(t, "true", row[7])
	assert.Equal(t, tricky, row[8], "topics must round-trip as JSON in one cell")
	assert.Equal(t, string(ev.Value), row[9])
	assert.Equal(t, "2026-08-02T12:43:57Z", row[10])
}

// TestExportNDJSONFraming checks there is exactly one complete JSON object per
// line and no array or other wrapper around the stream.
func TestExportNDJSONFraming(t *testing.T) {
	src := &fakeSource{events: source.EventPage{Events: []source.Event{
		exportEvent("0000000001-0000000001"),
		exportEvent("0000000001-0000000002"),
	}}}
	rec := do(t, testServer(src), "/api/events/export?format=ndjson")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/x-ndjson", rec.Header().Get("Content-Type"))

	body := rec.Body.String()
	lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	require.Len(t, lines, 2)
	assert.False(t, strings.HasPrefix(body, "["), "NDJSON must not be wrapped in an array")
	assert.False(t, strings.HasSuffix(strings.TrimSpace(body), ","))

	for i, line := range lines {
		var got source.Event
		require.NoError(t, json.Unmarshal([]byte(line), &got), "line %d is not standalone JSON", i)
		assert.Equal(t, []string{"0000000001-0000000001", "0000000001-0000000002"}[i], got.ID)
	}
}

// TestExportAppliesFilters proves exports reuse EventQueryFromRequest rather
// than a second, divergent query path.
func TestExportAppliesFilters(t *testing.T) {
	t.Run("global feed", func(t *testing.T) {
		src := &fakeSource{events: source.EventPage{Events: []source.Event{exportEvent("0000000001-0000000001")}}}
		rec := do(t, testServer(src),
			"/api/events/export?format=ndjson&contract_id="+testContract+"&type=contract&topic=transfer&from_ledger=100&to_ledger=200&limit=1")

		require.Equal(t, http.StatusOK, rec.Code)
		q := src.lastEventQuery
		assert.Equal(t, testContract, q.ContractID)
		assert.Equal(t, "contract", q.Type)
		assert.Equal(t, int64(100), q.FromLedger)
		assert.Equal(t, int64(200), q.ToLedger)
		assert.JSONEq(t, `{"symbol":"transfer"}`, string(q.Topic))
		assert.Equal(t, source.MaxLimit, q.Limit, "export should page at the largest size, not the caller's limit")
	})

	t.Run("contract feed", func(t *testing.T) {
		src := &fakeSource{events: source.EventPage{Events: []source.Event{exportEvent("0000000001-0000000001")}}}
		rec := do(t, testServer(src), "/api/contracts/"+testContract+"/events/export?format=csv&type=contract")

		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, testContract, src.lastEventQuery.ContractID)
		assert.Equal(t, "contract", src.lastEventQuery.Type)
	})
}

// TestExportWalksEveryPage guards the cursor loop: an export must not stop at
// the first page.
func TestExportWalksEveryPage(t *testing.T) {
	src := &pagedSource{pages: map[string]source.EventPage{
		"":   {Events: []source.Event{exportEvent("0000000001-0000000001")}, NextCursor: "c2"},
		"c2": {Events: []source.Event{exportEvent("0000000001-0000000002")}},
	}}

	rec := do(t, testServer(src), "/api/events/export?format=ndjson")
	require.Equal(t, http.StatusOK, rec.Code)

	lines := strings.Split(strings.TrimSuffix(rec.Body.String(), "\n"), "\n")
	require.Len(t, lines, 2)
	assert.Equal(t, []string{"", "c2"}, src.cursors)
}

func TestExportRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name   string
		target string
		want   string
	}{
		{"unknown format", "/api/events/export?format=xml", "invalid format"},
		{"bad filter", "/api/events/export?type=bogus", "invalid type"},
		{"bad contract on global feed", "/api/events/export?contract_id=nope", "invalid contract ID"},
		{"bad contract on contract feed", "/api/contracts/nope/events/export", "invalid contract ID"},
	}

	h := testServer(&fakeSource{})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, h, tt.target)
			require.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Contains(t, rec.Body.String(), tt.want)
		})
	}
}
