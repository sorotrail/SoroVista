package retention

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakePruner struct {
	mu       sync.Mutex
	calls    []time.Time
	removed  int64
	err      error
	blocking chan struct{}
}

func (f *fakePruner) PruneEvents(ctx context.Context, olderThan time.Time) (int64, error) {
	f.mu.Lock()
	f.calls = append(f.calls, olderThan)
	f.mu.Unlock()

	if f.blocking != nil {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-f.blocking:
		}
	}
	return f.removed, f.err
}

func (f *fakePruner) called() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestSweepOnceUsesTheHorizon(t *testing.T) {
	pruner := &fakePruner{removed: 42}
	s := New(pruner, 30*24*time.Hour, testLogger(), nil)

	before := time.Now().UTC()
	removed, err := s.sweepOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(42), removed)

	pruner.mu.Lock()
	defer pruner.mu.Unlock()
	require.Len(t, pruner.calls, 1)
	assert.WithinDuration(t, before.Add(-30*24*time.Hour), pruner.calls[0], time.Minute,
		"the cutoff should be the horizon measured back from now")
}

func TestRunDeletesNothingWithoutAHorizon(t *testing.T) {
	pruner := &fakePruner{}
	s := New(pruner, 0, testLogger(), nil)

	require.NoError(t, s.Run(context.Background()))
	assert.Zero(t, pruner.called(), "an unset horizon must never prune")
}

func TestRunStopsCleanlyOnCancel(t *testing.T) {
	// A sweep in progress must observe cancellation and return nil rather
	// than making shutdown wait for it.
	pruner := &fakePruner{blocking: make(chan struct{})}
	s := New(pruner, time.Hour, testLogger(), nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	require.Eventually(t, func() bool { return pruner.called() > 0 }, 2*time.Second, 10*time.Millisecond,
		"sweeper should start a sweep immediately")

	cancel()

	select {
	case err := <-done:
		require.NoError(t, err, "cancellation is a clean shutdown, not a failure")
	case <-time.After(2 * time.Second):
		t.Fatal("sweeper did not stop after its context was cancelled")
	}
}
