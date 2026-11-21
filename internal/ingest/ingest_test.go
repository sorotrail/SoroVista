package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sorotrail/sorovista/internal/rpc"
	"github.com/sorotrail/sorovista/internal/source"
	"github.com/sorotrail/sorovista/internal/store"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTestIngester(client rpc.Client, st store.Store, opts Options) *Ingester {
	return New(client, st, passthroughDecoder{}, opts, testLogger())
}

// event builds an RPC event with a decodable payload.
func event(id string, ledger uint32, contract string) rpc.Event {
	return rpc.Event{
		ID:                       id,
		Type:                     "contract",
		Ledger:                   ledger,
		ContractID:               contract,
		PagingToken:              id,
		InSuccessfulContractCall: true,
		TxHash:                   "hash-" + id,
		Topic:                    []string{"transfer"},
		Value:                    "amount",
		LedgerClosedAt:           time.Unix(1700000000, 0).UTC(),
	}
}

func TestPollOnceStoresDecodedEvents(t *testing.T) {
	client := &mockRPC{
		latest: 100,
		responses: []rpc.GetEventsResult{
			{Events: []rpc.Event{event("0000000100-0000000001", 100, "CABC")}, LatestLedger: 100},
		},
	}
	st := newMockStore()

	ing := newTestIngester(client, st, Options{RetentionLedgers: 50})
	require.NoError(t, ing.pollOnce(context.Background()))

	require.Equal(t, 1, st.storedCount())

	stored, ok := st.stored("0000000100-0000000001")
	require.True(t, ok)
	assert.Equal(t, "CABC", stored.ContractID)
	assert.Equal(t, int64(100), stored.Ledger)
	assert.Equal(t, "contract", stored.Type)
	assert.Equal(t, "hash-0000000100-0000000001", stored.TxHash)
	assert.True(t, stored.InSuccessfulCall)
	// The decoder ran over the base64 fields, since the node sent no JSON.
	assert.JSONEq(t, `[{"symbol":"transfer"}]`, string(stored.Topics))
	assert.JSONEq(t, `{"symbol":"amount"}`, string(stored.Value))

	// Progress is recorded so a restart resumes rather than re-reading.
	assert.Equal(t, int64(100), st.savedState().LastLedger)
}

func TestPollOnceColdStartUsesRetentionWindow(t *testing.T) {
	client := &mockRPC{latest: 10000}
	st := newMockStore()

	ing := newTestIngester(client, st, Options{RetentionLedgers: 1000})
	require.NoError(t, ing.pollOnce(context.Background()))

	requests := client.capturedRequests()
	require.NotEmpty(t, requests)
	assert.Equal(t, uint32(9000), requests[0].StartLedger, "expected latest minus the retention window")
	assert.Equal(t, uint32(10001), requests[0].EndLedger, "endLedger is exclusive, so it must exceed the tip")
}

func TestPollOnceColdStartHonorsStartLedger(t *testing.T) {
	client := &mockRPC{latest: 10000}
	st := newMockStore()

	ing := newTestIngester(client, st, Options{StartLedger: 42, RetentionLedgers: 1000})
	require.NoError(t, ing.pollOnce(context.Background()))

	requests := client.capturedRequests()
	require.NotEmpty(t, requests)
	assert.Equal(t, uint32(42), requests[0].StartLedger)
}

func TestPollOnceColdStartClampsAtGenesis(t *testing.T) {
	// A retention window wider than the chain must not underflow past ledger 1.
	client := &mockRPC{latest: 50}
	st := newMockStore()

	ing := newTestIngester(client, st, Options{RetentionLedgers: 1000})
	require.NoError(t, ing.pollOnce(context.Background()))

	requests := client.capturedRequests()
	require.NotEmpty(t, requests)
	assert.Equal(t, uint32(1), requests[0].StartLedger)
}

func TestPollOnceResumesFromSavedState(t *testing.T) {
	client := &mockRPC{latest: 500}
	st := newMockStore()
	st.state = store.IngestState{LastLedger: 200}

	ing := newTestIngester(client, st, Options{RetentionLedgers: 1000})
	require.NoError(t, ing.pollOnce(context.Background()))

	requests := client.capturedRequests()
	require.NotEmpty(t, requests)
	assert.Equal(t, uint32(201), requests[0].StartLedger, "expected to resume just past the last ingested ledger")
}

func TestPollOnceSkipsWhenCaughtUp(t *testing.T) {
	client := &mockRPC{latest: 200}
	st := newMockStore()
	st.state = store.IngestState{LastLedger: 200}

	ing := newTestIngester(client, st, Options{RetentionLedgers: 1000})
	require.NoError(t, ing.pollOnce(context.Background()))

	assert.Empty(t, client.capturedRequests(), "expected no fetch when already at the tip")
}

func TestIngestBatchFollowsCursor(t *testing.T) {
	// A full page means more may remain, so the ingester follows the cursor;
	// the short page that follows ends the stream.
	full := make([]rpc.Event, rpc.DefaultEventsLimit)
	for i := range full {
		full[i] = event(fmt.Sprintf("page1-%04d", i), 100, "CABC")
	}

	client := &mockRPC{
		latest: 100,
		responses: []rpc.GetEventsResult{
			{Events: full, LatestLedger: 100, Cursor: "cursor-1"},
			{Events: []rpc.Event{event("page2-0001", 100, "CABC")}, LatestLedger: 100},
		},
	}
	st := newMockStore()

	ing := newTestIngester(client, st, Options{RetentionLedgers: 50})
	require.NoError(t, ing.pollOnce(context.Background()))

	assert.Equal(t, rpc.DefaultEventsLimit+1, st.storedCount())

	requests := client.capturedRequests()
	require.Len(t, requests, 2)
	// The first request opens the ledger range; the second continues by cursor
	// and must not also carry startLedger, which nodes reject.
	assert.Equal(t, uint32(50), requests[0].StartLedger, "cold start reaches back by the retention window")
	assert.Equal(t, "cursor-1", requests[1].Pagination.Cursor)
	assert.Zero(t, requests[1].StartLedger, "startLedger and cursor are mutually exclusive")
}

func TestIngestBatchStopsOnShortPage(t *testing.T) {
	client := &mockRPC{
		latest: 100,
		responses: []rpc.GetEventsResult{
			{Events: []rpc.Event{event("only-1", 100, "CABC")}, LatestLedger: 100, Cursor: "cursor-1"},
		},
	}
	st := newMockStore()

	ing := newTestIngester(client, st, Options{RetentionLedgers: 50})
	require.NoError(t, ing.pollOnce(context.Background()))

	assert.Len(t, client.capturedRequests(), 1, "a short page means the stream is drained")
}

func TestUpsertIsIdempotent(t *testing.T) {
	// Re-reading a ledger range after a restart must not duplicate rows.
	makeResponse := func() rpc.GetEventsResult {
		return rpc.GetEventsResult{
			Events:       []rpc.Event{event("0000000100-0000000001", 100, "CABC")},
			LatestLedger: 100,
		}
	}

	client := &mockRPC{latest: 100, responses: []rpc.GetEventsResult{makeResponse(), makeResponse()}}
	st := newMockStore()

	ing := newTestIngester(client, st, Options{RetentionLedgers: 50})
	require.NoError(t, ing.pollOnce(context.Background()))

	// Rewind so the second poll covers the same range again.
	st.state = store.IngestState{}
	require.NoError(t, ing.pollOnce(context.Background()))

	assert.Equal(t, 1, st.storedCount(), "expected the duplicate to be ignored")
}

func TestPollOncePrefersNodeDecodedJSON(t *testing.T) {
	// When the node sends decoded JSON there is nothing to decode locally, so
	// the values must pass through untouched rather than being re-wrapped.
	client := &mockRPC{
		latest: 100,
		responses: []rpc.GetEventsResult{{
			Events: []rpc.Event{{
				ID:         "0000000100-0000000001",
				Type:       "contract",
				Ledger:     100,
				ContractID: "CABC",
				TopicJSON:  []json.RawMessage{json.RawMessage(`{"symbol":"mint"}`)},
				ValueJSON:  json.RawMessage(`{"u32":5}`),
			}},
			LatestLedger: 100,
		}},
	}
	st := newMockStore()

	ing := newTestIngester(client, st, Options{RetentionLedgers: 50})
	require.NoError(t, ing.pollOnce(context.Background()))

	stored, ok := st.stored("0000000100-0000000001")
	require.True(t, ok)
	assert.JSONEq(t, `[{"symbol":"mint"}]`, string(stored.Topics))
	assert.JSONEq(t, `{"u32":5}`, string(stored.Value))
}

func TestPollOncePropagatesFetchErrors(t *testing.T) {
	client := &mockRPC{latest: 100, errs: []error{errors.New("node exploded")}}
	st := newMockStore()

	ing := newTestIngester(client, st, Options{RetentionLedgers: 50})
	err := ing.pollOnce(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "node exploded")
	assert.Zero(t, st.savedState().LastLedger, "progress must not advance past a failure")
}

func TestPollOnceDoesNotAdvanceOnStoreFailure(t *testing.T) {
	client := &mockRPC{
		latest:    100,
		responses: []rpc.GetEventsResult{{Events: []rpc.Event{event("e1", 100, "CABC")}, LatestLedger: 100}},
	}
	st := newMockStore()
	st.upsertErr = errors.New("disk full")

	ing := newTestIngester(client, st, Options{RetentionLedgers: 50})
	require.Error(t, ing.pollOnce(context.Background()))
	assert.Zero(t, st.savedState().LastLedger)
}

// TestWithPublisherEdgeCases pins down the attachment semantics: attaching a
// nil publisher is safe, re-attaching replaces rather than stacks, and a later
// nil detaches whatever was attached before.
func TestWithPublisherEdgeCases(t *testing.T) {
	const eventID = "0000000100-0000000001"

	tests := []struct {
		name       string
		attach     func(a, b *recordingPublisher) []EventPublisher
		wantFirst  []string
		wantSecond []string
	}{
		{
			name:   "no publisher attached is safe",
			attach: func(a, b *recordingPublisher) []EventPublisher { return nil },
		},
		{
			name:   "nil publisher is accepted",
			attach: func(a, b *recordingPublisher) []EventPublisher { return []EventPublisher{nil} },
		},
		{
			name:       "one publisher receives each batch",
			attach:     func(a, b *recordingPublisher) []EventPublisher { return []EventPublisher{a} },
			wantFirst:  []string{eventID},
			wantSecond: nil,
		},
		{
			name:       "the last attached publisher wins",
			attach:     func(a, b *recordingPublisher) []EventPublisher { return []EventPublisher{a, b} },
			wantFirst:  nil,
			wantSecond: []string{eventID},
		},
		{
			name:   "nil detaches an existing publisher",
			attach: func(a, b *recordingPublisher) []EventPublisher { return []EventPublisher{a, nil} },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &mockRPC{
				latest: 100,
				responses: []rpc.GetEventsResult{
					{Events: []rpc.Event{event(eventID, 100, "CABC")}, LatestLedger: 100},
				},
			}
			st := newMockStore()
			first, second := &recordingPublisher{}, &recordingPublisher{}

			ing := newTestIngester(client, st, Options{RetentionLedgers: 50})
			for _, p := range tt.attach(first, second) {
				assert.Same(t, ing, ing.WithPublisher(p), "WithPublisher should return the receiver for chaining")
			}

			require.NoError(t, ing.pollOnce(context.Background()))
			assert.Equal(t, 1, st.storedCount(), "ingestion must proceed regardless of publisher attachment")
			assert.Equal(t, tt.wantFirst, first.events)
			assert.Equal(t, tt.wantSecond, second.events)
		})
	}
}

func TestRunStopsOnContextCancel(t *testing.T) {
	client := &mockRPC{latest: 100}
	st := newMockStore()

	ing := newTestIngester(client, st, Options{PollInterval: time.Second, RetentionLedgers: 50})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ing.Run(ctx) }()

	cancel()

	select {
	case err := <-done:
		require.NoError(t, err, "a cancelled context is a clean shutdown, not a failure")
	case <-time.After(5 * time.Second):
		t.Fatal("ingester did not stop after its context was cancelled")
	}
}

// TestRunEdgeCases exercises the loop's shutdown and retry behaviour: an
// already-cancelled context must not poll at all, and a failed poll is retried
// on the next tick instead of ending the run.
func TestRunEdgeCases(t *testing.T) {
	tests := []struct {
		name            string
		ctx             func() (context.Context, context.CancelFunc)
		opts            Options
		client          *mockRPC
		wantStored      int
		wantRetriedPoll bool
	}{
		{
			name: "pre-cancelled context returns without polling",
			ctx: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, cancel
			},
			opts:   Options{RetentionLedgers: 50},
			client: &mockRPC{latest: 100},
		},
		{
			name: "transient poll errors are retried until events arrive",
			ctx:  func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) },
			opts: Options{PollInterval: time.Millisecond, RetentionLedgers: 50},
			client: &mockRPC{
				latest: 100,
				errs:   []error{errors.New("transient node error")},
				responses: []rpc.GetEventsResult{
					{Events: []rpc.Event{event("0000000100-0000000001", 100, "CABC")}, LatestLedger: 100},
				},
			},
			wantStored:      1,
			wantRetriedPoll: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := newMockStore()
			ing := newTestIngester(tt.client, st, tt.opts)

			ctx, cancel := tt.ctx()
			defer cancel()

			done := make(chan error, 1)
			go func() { done <- ing.Run(ctx) }()

			if tt.wantStored > 0 {
				require.Eventually(t, func() bool { return st.storedCount() >= tt.wantStored },
					5*time.Second, time.Millisecond, "ingester never stored the event")
			}
			cancel()

			select {
			case err := <-done:
				require.NoError(t, err, "a cancelled context is a clean shutdown, not a failure")
			case <-time.After(5 * time.Second):
				t.Fatal("Run did not stop after its context was cancelled")
			}

			assert.Equal(t, tt.wantStored, st.storedCount())
			requests := tt.client.capturedRequests()
			if tt.wantRetriedPoll {
				assert.GreaterOrEqual(t, len(requests), 2, "the failed poll should be retried on the next tick")
			} else {
				assert.Empty(t, requests, "a pre-cancelled context must not reach the RPC")
			}
		})
	}
}

func TestFilterBatches(t *testing.T) {
	contract := func(i int) string { return fmt.Sprintf("C%055d", i) }

	t.Run("no watched contracts matches every contract event", func(t *testing.T) {
		batches := filterBatches(nil)
		require.Len(t, batches, 1)
		require.Len(t, batches[0], 1)
		assert.Equal(t, "contract", batches[0][0].Type)
		assert.Empty(t, batches[0][0].ContractIDs)
	})

	t.Run("contracts are chunked to the per-filter cap", func(t *testing.T) {
		var contracts []string
		for i := 0; i < rpc.MaxContractIDsPerFilter+1; i++ {
			contracts = append(contracts, contract(i))
		}

		batches := filterBatches(contracts)
		require.Len(t, batches, 1)
		require.Len(t, batches[0], 2, "expected a second filter once the first is full")
		assert.Len(t, batches[0][0].ContractIDs, rpc.MaxContractIDsPerFilter)
		assert.Len(t, batches[0][1].ContractIDs, 1)
	})

	t.Run("filters are chunked into separate requests", func(t *testing.T) {
		// One more contract than a single request can carry forces a second.
		var contracts []string
		for i := 0; i < rpc.MaxWatchedContracts+1; i++ {
			contracts = append(contracts, contract(i))
		}

		batches := filterBatches(contracts)
		require.Len(t, batches, 2)
		assert.Len(t, batches[0], rpc.MaxFiltersPerRequest)
		assert.Len(t, batches[1], 1)

		// Every contract must appear exactly once across all batches.
		seen := map[string]int{}
		for _, batch := range batches {
			for _, f := range batch {
				assert.LessOrEqual(t, len(f.ContractIDs), rpc.MaxContractIDsPerFilter)
				for _, id := range f.ContractIDs {
					seen[id]++
				}
			}
		}
		assert.Len(t, seen, len(contracts))
		for id, n := range seen {
			assert.Equal(t, 1, n, "contract %s appeared %d times", id, n)
		}
	})
}

func TestNextCursorFallsBackToPagingToken(t *testing.T) {
	t.Run("top-level cursor wins", func(t *testing.T) {
		got := nextCursor(rpc.GetEventsResult{
			Cursor: "explicit",
			Events: []rpc.Event{{PagingToken: "token"}},
		})
		assert.Equal(t, "explicit", got)
	})

	t.Run("falls back to the last paging token", func(t *testing.T) {
		got := nextCursor(rpc.GetEventsResult{
			Events: []rpc.Event{{PagingToken: "first"}, {PagingToken: "last"}},
		})
		assert.Equal(t, "last", got)
	})

	t.Run("no events yields no cursor", func(t *testing.T) {
		assert.Empty(t, nextCursor(rpc.GetEventsResult{}))
	})
}

type recordingPublisher struct {
	events []string
}

func (r *recordingPublisher) Publish(e []source.Event) {
	for _, ev := range e {
		r.events = append(r.events, ev.ID)
	}
}

func TestPollOncePublishesDecodedEventsToBroker(t *testing.T) {
	client := &mockRPC{
		latest: 100,
		responses: []rpc.GetEventsResult{
			{Events: []rpc.Event{event("0000000100-0000000001", 100, "CABC")}, LatestLedger: 100},
		},
	}
	st := newMockStore()
	pub := &recordingPublisher{}

	ing := newTestIngester(client, st, Options{RetentionLedgers: 50}).WithPublisher(pub)
	require.NoError(t, ing.pollOnce(context.Background()))

	assert.Equal(t, []string{"0000000100-0000000001"}, pub.events)
}
