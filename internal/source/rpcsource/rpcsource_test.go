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
}

func (s *stubStore) UpsertEvents(context.Context, []source.Event) (int64, error) { return 0, nil }

func (s *stubStore) GetEvent(context.Context, string) (source.Event, error) {
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
