// Package retention prunes stored events past a configurable horizon.
//
// It backs standalone mode only. Upstream mode has no local database, so no
// sweeper is ever constructed there. The horizon is deliberately a duration
// measured against ledger_closed_at: an operator reasons about "keep 30 days",
// not "keep 259200 ledgers", and on-chain time is what the event data carries.
package retention

import (
	"context"
	"log/slog"
	"time"

	"github.com/sorotrail/sorovista/internal/metrics"
)

// DefaultSweepInterval is how often a running sweeper checks for expired
// events. Pruning is cheap when there is nothing to remove and does not need
// to run as often as ingestion.
const DefaultSweepInterval = time.Hour

// Pruner is the slice of store.Store a sweeper needs. Keeping it narrow lets
// tests exercise the scheduling and cancellation logic without a database.
type Pruner interface {
	// PruneEvents deletes events older than olderThan and reports how many
	// rows it removed.
	PruneEvents(ctx context.Context, olderThan time.Time) (int64, error)
}

// Sweeper periodically deletes events past the configured horizon.
type Sweeper struct {
	pruner   Pruner
	horizon  time.Duration
	interval time.Duration
	log      *slog.Logger
	metrics  *metrics.Metrics
}

// New builds a sweeper. A non-positive horizon disables it entirely, so an
// unset EVENT_RETENTION keeps everything; Run also checks this defensively.
func New(p Pruner, horizon time.Duration, log *slog.Logger, m *metrics.Metrics) *Sweeper {
	return &Sweeper{
		pruner:   p,
		horizon:  horizon,
		interval: DefaultSweepInterval,
		log:      log,
		metrics:  m,
	}
}

// Run sweeps on a fixed schedule until ctx is cancelled. A cancelled context
// is a clean shutdown, not an error, so a deployment never waits for a sweep
// that will not finish.
func (s *Sweeper) Run(ctx context.Context) error {
	if s.horizon <= 0 {
		return nil
	}

	s.log.Info("retention sweeper starting",
		"horizon", s.horizon,
		"interval", s.interval)

	timer := time.NewTimer(0)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			s.log.Info("retention sweeper stopping")
			return nil
		case <-timer.C:
		}

		if err := ctx.Err(); err != nil {
			s.log.Info("retention sweeper stopping")
			return nil
		}

		removed, err := s.sweepOnce(ctx)
		if err != nil {
			if ctx.Err() != nil {
				s.log.Info("retention sweeper stopping")
				return nil
			}
			// A failed sweep is retried next interval rather than stopping the
			// process; the data simply stays a little longer.
			s.log.Error("retention sweep failed", "error", err)
		} else if removed > 0 {
			s.log.Info("pruned stored events",
				"count", removed,
				"horizon", s.horizon,
				"older_than", time.Now().UTC().Add(-s.horizon))
		}

		timer.Reset(s.interval)
	}
}

// sweepOnce deletes one horizon's worth of expired events and records the
// count. The store handles batching, so a first sweep over a large table does
// not hold one long transaction.
func (s *Sweeper) sweepOnce(ctx context.Context) (int64, error) {
	olderThan := time.Now().UTC().Add(-s.horizon)
	removed, err := s.pruner.PruneEvents(ctx, olderThan)
	if err != nil {
		return removed, err
	}
	s.metrics.RecordPruned(removed)
	return removed, nil
}
