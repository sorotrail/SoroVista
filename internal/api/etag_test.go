package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sorotrail/sorovista/internal/source"
)

func eventAt(id string, ledger int64) source.Event {
	return source.Event{
		ID:               id,
		ContractID:       testContract,
		Ledger:           ledger,
		Type:             "contract",
		Topics:           json.RawMessage(`[{"symbol":"transfer"}]`),
		Value:            json.RawMessage(`{"i32":1}`),
		LedgerClosedAt:   time.Unix(1700000000+ledger, 0).UTC(),
		CreatedAt:        time.Unix(1700000000+ledger, 0).UTC(),
		InSuccessfulCall: true,
	}
}

// getWith issues a GET carrying an If-None-Match header.
func getWith(t *testing.T, h http.Handler, target, ifNoneMatch string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestEventListETagConditionalGet(t *testing.T) {
	src := &fakeSource{events: source.EventPage{
		Events: []source.Event{
			eventAt("0000000102-0000000001", 102),
			eventAt("0000000101-0000000001", 101),
		},
		NextCursor: "0000000101-0000000001",
	}}
	h := testServer(src)
	target := "/api/events?limit=2"

	first := getWith(t, h, target, "")
	require.Equal(t, http.StatusOK, first.Code)
	etag := first.Header().Get("ETag")
	require.NotEmpty(t, etag, "list endpoints must set an ETag")

	t.Run("matching If-None-Match returns 304 with no body", func(t *testing.T) {
		res := getWith(t, h, target, etag)
		require.Equal(t, http.StatusNotModified, res.Code)
		assert.Empty(t, res.Body.String())
		assert.Equal(t, etag, res.Header().Get("ETag"), "the ETag must be repeated on a 304")
		assert.Empty(t, res.Header().Get("Content-Type"), "a 304 has no body, so no Content-Type")
	})

	t.Run("non-matching If-None-Match returns the full response", func(t *testing.T) {
		res := getWith(t, h, target, `"some-other-version"`)
		require.Equal(t, http.StatusOK, res.Code)
		assert.Equal(t, first.Body.String(), res.Body.String())
	})

	t.Run("absent header returns the full response", func(t *testing.T) {
		res := getWith(t, h, target, "")
		require.Equal(t, http.StatusOK, res.Code)
		assert.NotEmpty(t, res.Body.String())
	})

	t.Run("a new event invalidates the previous ETag", func(t *testing.T) {
		src.events = source.EventPage{
			Events: []source.Event{
				eventAt("0000000103-0000000001", 103),
				eventAt("0000000102-0000000001", 102),
			},
			NextCursor: "0000000102-0000000001",
		}
		res := getWith(t, h, target, etag)
		require.Equal(t, http.StatusOK, res.Code, "a stale 304 must not hide a new event")
		assert.NotEqual(t, etag, res.Header().Get("ETag"))
	})

	t.Run("the same tag is stable for unchanged data", func(t *testing.T) {
		a := getWith(t, h, target, "")
		b := getWith(t, h, target, "")
		require.Equal(t, http.StatusOK, a.Code)
		assert.Equal(t, a.Header().Get("ETag"), b.Header().Get("ETag"))
	})

	t.Run("different filters do not collide", func(t *testing.T) {
		// The fake source returns the same page for any filter, so only the
		// query itself can distinguish these tags.
		a := getWith(t, h, "/api/events?type=contract", "")
		b := getWith(t, h, "/api/events?type=system", "")
		require.Equal(t, http.StatusOK, a.Code)
		require.Equal(t, http.StatusOK, b.Code)
		assert.NotEqual(t, a.Header().Get("ETag"), b.Header().Get("ETag"))
	})
}

func TestContractListETagChangesWithAggregates(t *testing.T) {
	src := &fakeSource{contracts: source.ContractPage{Contracts: []source.Contract{{
		ID:           testContract,
		EventCount:   10,
		FirstLedger:  100,
		LastLedger:   110,
		LastActivity: time.Unix(1700000110, 0).UTC(),
	}}}}
	h := testServer(src)

	first := getWith(t, h, "/api/contracts", "")
	require.Equal(t, http.StatusOK, first.Code)
	etag := first.Header().Get("ETag")
	require.NotEmpty(t, etag)

	res := getWith(t, h, "/api/contracts", etag)
	require.Equal(t, http.StatusNotModified, res.Code)

	// The same contract still on the page, but with another event: this must
	// change the tag even though the contract IDs are identical.
	src.contracts.Contracts[0].EventCount = 11
	src.contracts.Contracts[0].LastLedger = 111
	res = getWith(t, h, "/api/contracts", etag)
	require.Equal(t, http.StatusOK, res.Code)
	assert.NotEqual(t, etag, res.Header().Get("ETag"))
}

func TestContractEventsETag(t *testing.T) {
	src := &fakeSource{events: source.EventPage{Events: []source.Event{eventAt("0000000100-0000000001", 100)}}}
	h := testServer(src)
	target := "/api/contracts/" + testContract + "/events"

	first := getWith(t, h, target, "")
	require.Equal(t, http.StatusOK, first.Code)
	etag := first.Header().Get("ETag")
	require.NotEmpty(t, etag)

	res := getWith(t, h, target, etag)
	require.Equal(t, http.StatusNotModified, res.Code)
}

func TestSingleEventETag(t *testing.T) {
	src := &fakeSource{event: eventAt("0000000100-0000000001", 100)}
	h := testServer(src)
	target := "/api/events/0000000100-0000000001"

	first := getWith(t, h, target, "")
	require.Equal(t, http.StatusOK, first.Code)
	etag := first.Header().Get("ETag")
	require.NotEmpty(t, etag)

	res := getWith(t, h, target, etag)
	require.Equal(t, http.StatusNotModified, res.Code)
	assert.Empty(t, res.Body.String())

	// "*" matches any current representation.
	res = getWith(t, h, target, "*")
	require.Equal(t, http.StatusNotModified, res.Code)
}
