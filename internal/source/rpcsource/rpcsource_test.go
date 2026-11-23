package rpcsource

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sorotrail/sorovista/internal/rpc"
	"github.com/sorotrail/sorovista/internal/source"
	"github.com/sorotrail/sorovista/internal/store"
)

// stubStore is a canned store.Store.
type stubStore struct {
	contracts     []source.Contract
	contractNext  string
	events        []source.Event
	eventsNext    string
	event         source.Event
	stats         source.Stats
	contractStats source.ContractStats
	buckets       source.BucketSeries

	err     error
	pingErr error

	lastEventQuery    source.EventQuery
	lastContractQuery source.ContractQuery
	lastBucketQuery   source.BucketQuery
	lastEventID       string
}

func (s *stubStore) UpsertEvents(context.Context, []source.Event) (int64, error) { return 0, nil }

func (s *stubStore) GetEvent(_ context.Context, id string) (source.Event, error) {
	s.lastEventID = id
	return s.event, s.err
}

func (s *stubStore) QueryEvents(_ context.Context, q source.EventQuery) ([]source.Event, string, error) {
	s.lastEventQuery = q
	return s.events, s.eventsNext, s.err
}

func (s *stubStore) ListContracts(_ context.Context, q source.ContractQuery) ([]source.Contract, string, error) {
	s.lastContractQuery = q
	return s.contracts, s.contractNext, s.err
}

func (s *stubStore) ContractStats(context.Context, string) (source.ContractStats, error) {
	return s.contractStats, s.err
}

func (s *stubStore) EventBuckets(_ context.Context, q source.BucketQuery) (source.BucketSeries, error) {
	s.lastBucketQuery = q
	return s.buckets, s.err
}

func (s *stubStore) Stats(context.Context) (source.Stats, error) { return s.stats, s.err }

func (s *stubStore) GetIngestState(context.Context) (store.IngestState, error) {
	return store.IngestState{}, nil
}
func (s *stubStore) SaveIngestState(context.Context, store.IngestState) error { return nil }
func (s *stubStore) Ping(context.Context) error                               { return s.pingErr }
func (s *stubStore) Close()                                                   {}

// stubRPC is a canned rpc.Client used only for status reporting here.
type stubRPC struct {
	health rpc.Health
	err    error
}

func (s *stubRPC) GetEvents(context.Context, rpc.GetEventsRequest) (rpc.GetEventsResult, error) {
	return rpc.GetEventsResult{}, nil
}
func (s *stubRPC) GetLatestLedger(context.Context) (rpc.LatestLedger, error) {
	return rpc.LatestLedger{}, nil
}
func (s *stubRPC) GetHealth(context.Context) (rpc.Health, error) { return s.health, s.err }

func healthyRPC() *stubRPC { return &stubRPC{health: rpc.Health{Status: "healthy", LatestLedger: 900}} }

func TestListEventsForwardsQueryAndCursor(t *testing.T) {
	st := &stubStore{
		events:     []source.Event{{ID: "e1"}, {ID: "e2"}},
		eventsNext: "cursor-2",
	}
	src := New(st, healthyRPC())

	q := source.EventQuery{ContractID: "CABC", Type: "contract", FromLedger: 10, Limit: 25}
	page, err := src.ListEvents(context.Background(), q)
	require.NoError(t, err)

	assert.Len(t, page.Events, 2)
	assert.Equal(t, "cursor-2", page.NextCursor)
	// The query must reach the store unchanged.
	assert.Equal(t, q, st.lastEventQuery)
}

// TestListEventsEdgeCases covers the edges the happy-path test above does not:
// an unconstrained query, a single-ledger boundary window, and a store failure.
func TestListEventsEdgeCases(t *testing.T) {
	tests := []struct {
		name       string
		query      source.EventQuery
		stubEvents []source.Event
		stubNext   string
		stubErr    error
		wantEvents int
		wantNext   string
		wantErr    bool
	}{
		{
			name:       "empty query and empty store yield an empty page",
			query:      source.EventQuery{},
			stubEvents: nil,
			wantEvents: 0,
			wantNext:   "",
		},
		{
			name:       "from and to on the same ledger is a one-ledger window",
			query:      source.EventQuery{FromLedger: 42, ToLedger: 42, Limit: 1},
			stubEvents: []source.Event{{ID: "e42", Ledger: 42}},
			wantEvents: 1,
		},
		{
			name:       "last page carries no cursor",
			query:      source.EventQuery{Limit: source.MaxLimit},
			stubEvents: []source.Event{{ID: "e1"}, {ID: "e2"}},
			wantEvents: 2,
			wantNext:   "",
		},
		{
			name:    "store failure propagates without a partial page",
			query:   source.EventQuery{Limit: 25},
			stubErr: errors.New("query failed"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := &stubStore{events: tt.stubEvents, eventsNext: tt.stubNext, err: tt.stubErr}
			src := New(st, healthyRPC())

			page, err := src.ListEvents(context.Background(), tt.query)
			if tt.wantErr {
				require.Error(t, err)
				assert.Empty(t, page.Events, "a failed query must not return a partial page")
				return
			}

			require.NoError(t, err)
			assert.Len(t, page.Events, tt.wantEvents)
			assert.Equal(t, tt.wantNext, page.NextCursor)
			// Even an all-zero query must reach the store untouched: the store
			// owns defaulting and clamping.
			assert.Equal(t, tt.query, st.lastEventQuery)
		})
	}
}

func TestListContractsForwardsQuery(t *testing.T) {
	st := &stubStore{
		contracts:    []source.Contract{{ID: "CABC", EventCount: 3}},
		contractNext: "next",
	}
	src := New(st, healthyRPC())

	page, err := src.ListContracts(context.Background(), source.ContractQuery{Search: "CA", Limit: 10})
	require.NoError(t, err)

	require.Len(t, page.Contracts, 1)
	assert.Equal(t, "next", page.NextCursor)
	assert.Equal(t, "CA", st.lastContractQuery.Search)
}

func TestContractStats(t *testing.T) {
	st := &stubStore{contractStats: source.ContractStats{
		ContractID:  "CAAA",
		TotalEvents: 3,
		FirstLedger: 100,
		LastLedger:  120,
		TypeBreakdown: []source.TypeCount{
			{Type: "contract", Count: 2},
			{Type: "diagnostic", Count: 1},
		},
	}}
	src := New(st, healthyRPC())

	got, err := src.ContractStats(context.Background(), "CAAA")
	require.NoError(t, err)

	assert.Equal(t, "CAAA", got.ContractID)
	assert.Equal(t, int64(3), got.TotalEvents)
	assert.Equal(t, int64(100), got.FirstLedger)
	assert.Equal(t, int64(120), got.LastLedger)
	assert.Equal(t, []source.TypeCount{
		{Type: "contract", Count: 2},
		{Type: "diagnostic", Count: 1},
	}, got.TypeBreakdown)
	// Approximate is part of the interface rather than an incidental field:
	// the UI labels approximate figures. Standalone counts in SQL, so it is
	// always exact.
	assert.False(t, got.Approximate, "standalone mode counts exactly")
}

func TestContractStatsUnknownContractIsEmpty(t *testing.T) {
	// The Postgres implementation counts with COALESCE(MIN(ledger), 0) and
	// COALESCE(MAX(ledger), 0), so a contract with no stored events comes back
	// as zeros rather than source.ErrNotFound. The wrapper must pass that
	// through unchanged instead of inventing an error.
	st := &stubStore{contractStats: source.ContractStats{ContractID: "CZZZ"}}
	src := New(st, healthyRPC())

	got, err := src.ContractStats(context.Background(), "CZZZ")
	require.NoError(t, err)

	assert.Equal(t, "CZZZ", got.ContractID)
	assert.Zero(t, got.TotalEvents)
	assert.Zero(t, got.FirstLedger)
	assert.Zero(t, got.LastLedger)
	assert.Empty(t, got.TypeBreakdown)
	assert.False(t, got.Approximate)
}

func TestStatsAreExact(t *testing.T) {
	st := &stubStore{stats: source.Stats{TotalEvents: 10, ContractCount: 2}}
	src := New(st, healthyRPC())

	got, err := src.Stats(context.Background())
	require.NoError(t, err)

	assert.Equal(t, int64(10), got.TotalEvents)
	assert.False(t, got.Approximate, "standalone mode counts exactly")
}

func TestErrorsPropagate(t *testing.T) {
	st := &stubStore{err: errors.New("database exploded")}
	src := New(st, healthyRPC())
	ctx := context.Background()

	_, err := src.ListEvents(ctx, source.EventQuery{})
	assert.Error(t, err)

	_, err = src.ListContracts(ctx, source.ContractQuery{})
	assert.Error(t, err)

	_, err = src.Stats(ctx)
	assert.Error(t, err)

	// A store failure on a stats read must surface, not collapse into empty
	// stats that look like a contract with no activity.
	_, err = src.ContractStats(ctx, "CAAA")
	assert.Error(t, err)
}

func TestGetEventNotFoundPassesThrough(t *testing.T) {
	st := &stubStore{err: source.ErrNotFound}
	src := New(st, healthyRPC())

	_, err := src.GetEvent(context.Background(), "missing")
	assert.ErrorIs(t, err, source.ErrNotFound)
}

// dependencyByName returns a status's dependency by its stable name.
func dependencyByName(t *testing.T, st source.Status, name string) source.Dependency {
	t.Helper()
	for _, d := range st.Dependencies {
		if d.Name == name {
			return d
		}
	}
	t.Fatalf("dependency %q missing from %+v", name, st.Dependencies)
	return source.Dependency{}
}

func TestStatusReportsEachDependency(t *testing.T) {
	tests := []struct {
		name          string
		pingErr       error
		rpcErr        error
		rpcStatus     string
		wantHealthy   bool
		wantDBHealthy bool
		wantRPCh      bool
	}{
		{"both healthy", nil, nil, "healthy", true, true, true},
		{"database failing alone", errors.New("connection refused"), nil, "healthy", false, false, true},
		{"rpc failing alone", nil, errors.New("timeout"), "", false, true, false},
		{"both failing", errors.New("connection refused"), errors.New("timeout"), "", false, false, false},
		{"rpc degraded", nil, nil, "syncing", false, true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := New(&stubStore{pingErr: tt.pingErr}, &stubRPC{
				err:    tt.rpcErr,
				health: rpc.Health{Status: tt.rpcStatus, LatestLedger: 900},
			})
			st := src.Status(context.Background())

			if st.Healthy != tt.wantHealthy {
				t.Fatalf("overall Healthy = %v, want %v", st.Healthy, tt.wantHealthy)
			}
			if got := len(st.Dependencies); got != 2 {
				t.Fatalf("dependencies = %d, want 2 (postgres and stellar_rpc)", got)
			}

			db := dependencyByName(t, st, "postgres")
			if db.Healthy != tt.wantDBHealthy {
				t.Errorf("postgres Healthy = %v, want %v", db.Healthy, tt.wantDBHealthy)
			}
			if db.Severity != source.DependencyFatal {
				t.Errorf("postgres severity = %q, want %q", db.Severity, source.DependencyFatal)
			}

			rpcDep := dependencyByName(t, st, "stellar_rpc")
			if rpcDep.Healthy != tt.wantRPCh {
				t.Errorf("stellar_rpc Healthy = %v, want %v", rpcDep.Healthy, tt.wantRPCh)
			}
			if rpcDep.Severity != source.DependencyDegraded {
				t.Errorf("stellar_rpc severity = %q, want %q", rpcDep.Severity, source.DependencyDegraded)
			}
		})
	}
}

// TestGetEventEdgeCases pins the pass-through behaviour for the inputs the
// happy path does not exercise: an empty ID, a store error other than
// ErrNotFound, and a zero-value event returned without error.
func TestGetEventEdgeCases(t *testing.T) {
	tests := []struct {
		name      string
		id        string
		event     source.Event
		err       error
		wantID    string
		wantErr   error
		wantNoErr bool
	}{
		{
			name:    "empty id is forwarded, not short-circuited",
			id:      "",
			err:     source.ErrNotFound,
			wantID:  "",
			wantErr: source.ErrNotFound,
		},
		{
			name:      "not-found maps to ErrNotFound",
			id:        "0000000000000000001-0000000001",
			err:       source.ErrNotFound,
			wantID:    "0000000000000000001-0000000001",
			wantErr:   source.ErrNotFound,
			wantNoErr: false,
		},
		{
			name:      "an unexpected store error propagates unchanged",
			id:        "e1",
			err:       errors.New("database exploded"),
			wantID:    "e1",
			wantNoErr: false,
		},
		{
			name:      "zero-value event without error is returned as-is",
			id:        "e1",
			event:     source.Event{},
			wantID:    "e1",
			wantNoErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := &stubStore{event: tt.event, err: tt.err}
			src := New(st, healthyRPC())

			got, err := src.GetEvent(context.Background(), tt.id)
			assert.Equal(t, tt.wantID, st.lastEventID, "the ID must be forwarded to the store verbatim")
			if tt.wantNoErr {
				require.NoError(t, err)
				assert.Equal(t, tt.event, got)
				return
			}
			require.Error(t, err)
			assert.Equal(t, tt.event, got, "no event is returned alongside an error")
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
			}
		})
	}
}

func TestStatusDetailNeverIncludesCredentials(t *testing.T) {
	// A connection error may quote the connection string. The health response
	// is unauthenticated, so it must never carry the password through.
	src := New(&stubStore{
		pingErr: errors.New("postgres://sorovista:hunter2@db:5432/sorovista: connection refused"),
	}, healthyRPC())

	st := src.Status(context.Background())

	if strings.Contains(st.Detail, "hunter2") {
		t.Fatalf("overall detail leaked the password: %q", st.Detail)
	}
	db := dependencyByName(t, st, "postgres")
	if strings.Contains(db.Detail, "hunter2") {
		t.Fatalf("dependency detail leaked the password: %q", db.Detail)
	}
	if !strings.Contains(db.Detail, "[redacted]") {
		t.Fatalf("expected a redaction marker, got %q", db.Detail)
	}
}
func TestStatus(t *testing.T) {
	t.Run("healthy reports the retention caveat", func(t *testing.T) {
		src := New(&stubStore{}, healthyRPC())
		st := src.Status(context.Background())

		assert.Equal(t, "rpc", st.Mode)
		assert.True(t, st.Healthy)
		assert.Equal(t, int64(900), st.LatestLedger)
		assert.Contains(t, st.RetentionNote, "only while SoroVista is running",
			"the capture-window caveat must always be surfaced")
	})

	t.Run("a database failure is unhealthy, not an error", func(t *testing.T) {
		src := New(&stubStore{pingErr: errors.New("connection refused")}, healthyRPC())
		st := src.Status(context.Background())

		assert.False(t, st.Healthy)
		assert.Contains(t, st.Detail, "database unreachable")
	})

	t.Run("an unreachable node is unhealthy", func(t *testing.T) {
		src := New(&stubStore{}, &stubRPC{err: errors.New("timeout")})
		st := src.Status(context.Background())

		assert.False(t, st.Healthy)
		assert.Contains(t, st.Detail, "stellar rpc unreachable")
	})

	t.Run("a degraded node is unhealthy", func(t *testing.T) {
		src := New(&stubStore{}, &stubRPC{health: rpc.Health{Status: "syncing"}})
		st := src.Status(context.Background())

		assert.False(t, st.Healthy)
		assert.Contains(t, st.Detail, "syncing")
	})
}

// TestSourceSatisfiesInterface fails at compile time if the standalone source
// ever drifts from source.EventSource.
func TestSourceSatisfiesInterface(t *testing.T) {
	var _ source.EventSource = New(&stubStore{}, healthyRPC())
}
