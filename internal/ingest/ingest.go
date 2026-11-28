// Package ingest polls Stellar RPC for contract events and writes them to the
// store. It runs only in standalone mode.
package ingest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/codes"

	"github.com/sorotrail/sorovista/internal/decode"
	"github.com/sorotrail/sorovista/internal/metrics"
	"github.com/sorotrail/sorovista/internal/rpc"
	"github.com/sorotrail/sorovista/internal/source"
	"github.com/sorotrail/sorovista/internal/store"
	"github.com/sorotrail/sorovista/internal/tracing"
)

// MaxPagesPerBatch bounds how many cursor pages one batch may follow in a
// single poll. Without it, a node that keeps returning full pages could hold
// the loop indefinitely and starve the shutdown path.
const MaxPagesPerBatch = 1000

// Options configure an Ingester.
type Options struct {
	// WatchedContracts limits ingestion to these contract IDs. Empty ingests
	// every contract event.
	WatchedContracts []string
	// PollInterval is how long to sleep once caught up to the chain tip.
	PollInterval time.Duration
	// StartLedger forces a cold start from this ledger. Zero reaches back
	// RetentionLedgers from the tip instead.
	StartLedger uint32
	// RetentionLedgers is the cold-start reach-back when StartLedger is zero.
	RetentionLedgers uint32

	// PerContractResumers optionally provides per-contract ingestion state.
	// When set, the ingester resumes each contract from its own cursor rather
	// than from a single global position. When nil or empty, the global
	// ingest position is used (backward-compatible mode).
	PerContractResumers ContractIngestSaver
}

// ContractIngestSaver is the small interface needed from the Store for
// per-contract cursor support, so the ingester does not depend on the full
// Store interface when only these two methods are required.
type ContractIngestSaver interface {
	GetContractIngestState(ctx context.Context, contractID string) (store.ContractIngestState, error)
	SaveContractIngestState(ctx context.Context, s store.ContractIngestState) error
}

// EventPublisher receives newly ingested events after they have been persisted
// to the store. Implementations must not block the caller.
type EventPublisher interface {
	Publish(events []source.Event)
}

// Ingester polls the RPC and persists what it finds.
type Ingester struct {
	rpc     rpc.Client
	store   store.Store
	decoder decode.Decoder
	opts    Options
	log     *slog.Logger
	// metrics is optional instrumentation; nil-safe, see internal/metrics.
	metrics *metrics.Metrics
	// tracer is optional instrumentation; nil-safe, see internal/tracing.
	tracer    *tracing.Tracer
	publisher EventPublisher
}

// New builds an Ingester.
func New(client rpc.Client, st store.Store, dec decode.Decoder, opts Options, log *slog.Logger) *Ingester {
	if opts.PollInterval <= 0 {
		opts.PollInterval = 5 * time.Second
	}
	return &Ingester{rpc: client, store: st, decoder: dec, opts: opts, log: log}
}

// WithMetrics attaches Prometheus instrumentation to the ingest loop.
func (i *Ingester) WithMetrics(m *metrics.Metrics) *Ingester {
	i.metrics = m
	return i
}

// WithTracer attaches OpenTelemetry tracing, which wraps each poll cycle in a
// span. A nil tracer records nothing.
func (i *Ingester) WithTracer(t *tracing.Tracer) *Ingester {
	i.tracer = t
	return i
}

// WithPublisher attaches a non-blocking live event publisher (such as the SSE
// Broker) to be notified after each batch is stored.
func (i *Ingester) WithPublisher(p EventPublisher) *Ingester {
	i.publisher = p
	return i
}

// SeedContractCursors reads the global ingest state and seeds per-contract
// cursors for all watched contracts. When WATCHED_CONTRACTS is empty, this
// is a no-op since the global position is used as before. This is called once
// at startup so that upgrades seed per-contract cursors from the global
// position without re-reading the retention window.
func SeedContractCursors(ctx context.Context, st store.Store, contracts []string) error {
	if len(contracts) == 0 {
		// No watched contracts: global position is used as before.
		return nil
	}

	// Read global ingest state.
	globalState, err := st.GetIngestState(ctx)
	if err != nil {
		return fmt.Errorf("reading global ingest state for seeding: %w", err)
	}

	// If the global position is empty, there's nothing to seed from.
	if globalState.LastLedger == 0 {
		// Fall back to StartLedger / retention window seeding logic if needed.
		// For now, leave per-contract cursors empty; they will be populated
		// on first poll from the retention window / StartLedger.
		return nil
	}

	// Seed each watched contract's per-contract state from the global position.
	for _, cid := range contracts {
		// Check if per-contract state already exists.
		existing, err := st.GetContractIngestState(ctx, cid)
		if err != nil {
			return fmt.Errorf("checking existing per-contract state for %s: %w", cid, err)
		}
		// Only seed if no per-contract state exists yet (upgrade case).
		if existing.LastLedger == 0 {
			err := st.SaveContractIngestState(ctx, store.ContractIngestState{
				ContractID: cid,
				LastLedger: globalState.LastLedger,
				LastCursor: globalState.LastCursor,
			})
			if err != nil {
				return fmt.Errorf("seeding per-contract state for %s: %w", cid, err)
			}
		}
	}
	return nil
}

// Run polls until ctx is cancelled. A failed poll is logged and retried on the
// next tick rather than stopping ingestion, since RPC endpoints routinely
// return transient errors.
func (i *Ingester) Run(ctx context.Context) error {
	i.log.Info("ingester starting",
		"watched_contracts", len(i.opts.WatchedContracts),
		"poll_interval", i.opts.PollInterval)

	if len(i.opts.WatchedContracts) > rpc.MaxWatchedContracts {
		i.log.Warn("watching many contracts requires several requests per poll",
			"count", len(i.opts.WatchedContracts),
			"per_request", rpc.MaxWatchedContracts)
	}

	timer := time.NewTimer(0)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			i.log.Info("ingester stopping")
			return nil
		case <-timer.C:
		}

		start := time.Now()
		// One span per poll cycle ties the RPC calls and store writes of a poll
		// to the lag and duration metrics recorded just below.
		pollCtx, span := i.tracer.Start(ctx, "ingest.poll")
		err := i.pollOnce(pollCtx)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
		i.metrics.RecordPoll(err == nil, time.Since(start))
		if err != nil {
			if ctx.Err() != nil {
				i.log.Info("ingester stopping")
				return nil
			}
			i.log.Error("poll failed", "error", err)
		}
		timer.Reset(i.opts.PollInterval)
	}
}

// pollOnce ingests everything from the resume point up to the current tip.
func (i *Ingester) pollOnce(ctx context.Context) error {
	latest, err := i.rpc.GetLatestLedger(ctx)
	if err != nil {
		return fmt.Errorf("fetching latest ledger: %w", err)
	}

	resume, err := i.resumeLedger(ctx, latest.Sequence)
	if err != nil {
		return err
	}

	// start is the overall ledger from which the RPC query begins.
	var start uint32

	// When per-contract resumers are configured, the resume map keys are
	// contract IDs; otherwise it has the single "" key for backward compat.
	if i.opts.PerContractResumers != nil && len(i.opts.WatchedContracts) > 0 {
		// Determine the overall start ledger as the minimum across all
		// contracts' resume ledgers, so the RPC query covers enough range for
		// every contract.
		minLedger := latest.Sequence
		for _, ledger := range resume {
			if ledger < minLedger {
				minLedger = ledger
			}
		}
		start = minLedger
	} else {
		// Backward-compatible: resume is a single "{"ledger": ...} map.
		start = resume[""]
	}

	if start > latest.Sequence {
		// Already caught up; nothing has closed since the last poll.
		i.metrics.SetLag(0)
		return nil
	}
	i.metrics.SetLag(int64(latest.Sequence) - int64(start))

	// endLedger is exclusive, so +1 includes the tip itself.
	endExclusive := latest.Sequence + 1

	var total int64
	for _, batch := range filterBatches(i.opts.WatchedContracts) {
		n, err := i.ingestBatch(ctx, batch, start, endExclusive)
		total += n
		if err != nil {
			return err
		}
	}

	// Save per-contract ingestion state when resumers are configured.
	// Each contract gets its own resume ledger from the map returned by
	// resumeLedger. The overall "start" for the RPC query is the minimum
	// across all contracts, but per-contract state records each contract's
	// own position so restarts resume correctly.
	if i.opts.PerContractResumers != nil && len(i.opts.WatchedContracts) > 0 {
		for cid, ledger := range resume {
			// Only save state for contracts we're actually watching.
			// The resume map may contain contracts not in WatchedContracts
			// (e.g. the global "" key), so filter accordingly.
			if len(i.opts.WatchedContracts) > 0 {
				found := false
				for _, wc := range i.opts.WatchedContracts {
					if wc == cid {
						found = true
						break
					}
				}
				if !found {
					continue
				}
			}
			err := i.opts.PerContractResumers.SaveContractIngestState(ctx, store.ContractIngestState{
				ContractID: cid,
				LastLedger: int64(ledger),
				LastCursor: "",
			})
			if err != nil {
				i.log.Warn("failed to save per-contract ingest state", "contract", cid, "error", err)
			}
		}
	} else {
		if err := i.store.SaveIngestState(ctx, store.IngestState{LastLedger: int64(latest.Sequence)}); err != nil {
			return err
		}
	}

	i.metrics.RecordEvents(total)
	i.metrics.SetLag(0) // caught up: the resume point now equals the tip

	if total > 0 {
		i.log.Info("ingested events", "count", total, "from_ledger", start, "to_ledger", latest.Sequence)
	} else {
		i.log.Debug("no new events", "from_ledger", start, "to_ledger", latest.Sequence)
	}
	return nil
}

// ingestBatch follows the cursor for one filter batch until the stream drains.
func (i *Ingester) ingestBatch(ctx context.Context, filters []rpc.EventFilter, start, endExclusive uint32) (int64, error) {
	var (
		cursor   string
		inserted int64
	)

	for page := 0; page < MaxPagesPerBatch; page++ {
		if err := ctx.Err(); err != nil {
			return inserted, err
		}

		req := rpc.GetEventsRequest{
			Filters:    filters,
			Pagination: &rpc.Pagination{Limit: rpc.DefaultEventsLimit},
		}
		// startLedger and a cursor are mutually exclusive: the cursor already
		// encodes the position, and nodes reject requests carrying both.
		if cursor == "" {
			req.StartLedger = start
			req.EndLedger = endExclusive
		} else {
			req.Pagination.Cursor = cursor
		}

		res, err := i.rpc.GetEvents(ctx, req)
		if err != nil {
			return inserted, fmt.Errorf("fetching events: %w", err)
		}
		if len(res.Events) == 0 {
			return inserted, nil
		}

		events, err := i.convert(res.Events)
		if err != nil {
			return inserted, err
		}
		n, err := i.store.UpsertEvents(ctx, events)
		if err != nil {
			return inserted, err
		}
		inserted += n
		if i.publisher != nil && len(events) > 0 {
			i.publisher.Publish(events)
		}

		// A short page means the stream is drained for now.
		if len(res.Events) < rpc.DefaultEventsLimit {
			return inserted, nil
		}
		next := nextCursor(res)
		if next == "" || next == cursor {
			return inserted, nil
		}
		cursor = next
	}

	i.log.Warn("stopped following cursor at the page cap; will resume next poll",
		"max_pages", MaxPagesPerBatch)
	return inserted, nil
}

// convert decodes RPC events into the storage shape.
func (i *Ingester) convert(events []rpc.Event) ([]source.Event, error) {
	out := make([]source.Event, 0, len(events))
	for _, e := range events {
		topics, value, err := decode.TopicsValue(i.decoder, decode.RawEvent{
			Topic:     e.Topic,
			Value:     e.Value,
			TopicJSON: e.TopicJSON,
			ValueJSON: e.ValueJSON,
		})
		if err != nil {
			return nil, fmt.Errorf("decoding event %s: %w", e.ID, err)
		}
		out = append(out, source.Event{
			ID:               e.ID,
			ContractID:       e.ContractID,
			Ledger:           int64(e.Ledger),
			Type:             e.Type,
			TxHash:           e.TxHash,
			TxIndex:          e.TxIndex,
			OpIndex:          e.OpIndex,
			InSuccessfulCall: e.InSuccessfulContractCall,
			Topics:           topics,
			Value:            value,
			LedgerClosedAt:   e.LedgerClosedAt,
		})
	}
	return out, nil
}

// resumeLedger decides where this poll should start: just past whatever was
// ingested last, considering per-contract resumption when per-contract
// resumers are configured. When no per-contract resumers are available, the
// global ingest position is used (backward-compatible mode).
func (i *Ingester) resumeLedger(ctx context.Context, latest uint32) (map[string]uint32, error) {
	// When per-contract resumers are configured, determine each contract's
	// resume ledger. Otherwise fall back to the global position.
	if i.opts.PerContractResumers != nil {
		contracts := i.opts.WatchedContracts
		if len(contracts) == 0 {
			// No watched contracts: use the global position as before.
			state, err := i.store.GetIngestState(ctx)
			if err != nil {
				return nil, err
			}
			if state.LastLedger > 0 {
				return map[string]uint32{
					"": uint32(state.LastLedger) + 1,
				}, nil
			}
			return map[string]uint32{"": 1}, nil
		}

		resume := make(map[string]uint32)
		for _, cid := range contracts {
			cs, err := i.opts.PerContractResumers.GetContractIngestState(ctx, cid)
			if err != nil {
				return nil, err
			}
			switch {
			case cs.LastLedger > 0:
				// Resume from the per-contract cursor + 1.
				resume[cid] = uint32(cs.LastLedger) + 1
			case i.opts.StartLedger > 0:
				// No per-contract state yet, but StartLedger is set:
				// start from the explicit start ledger.
				resume[cid] = i.opts.StartLedger
			default:
				// No per-contract state and no StartLedger: fall back to
				// the retention window / cold-start position.
				if latest > i.opts.RetentionLedgers {
					resume[cid] = latest - i.opts.RetentionLedgers
				} else {
					resume[cid] = 1
				}
			}
		}
		// Return the minimum resume ledger so the overall poll starts early
		// enough for all contracts.
		minLedger := latest
		for _, ledger := range resume {
			if ledger < minLedger {
				minLedger = ledger
			}
		}
		return resume, nil
	}

	// Backward-compatible: single global position.
	state, err := i.store.GetIngestState(ctx)
	if err != nil {
		return nil, err
	}
	if state.LastLedger > 0 {
		return map[string]uint32{
			"": uint32(state.LastLedger) + 1,
		}, nil
	}

	if i.opts.StartLedger > 0 {
		return map[string]uint32{"": i.opts.StartLedger}, nil
	}

	// Cold start: reach back over the retention window, clamped at ledger 1.
	if latest > i.opts.RetentionLedgers {
		return map[string]uint32{"": latest - i.opts.RetentionLedgers}, nil
	}
	return map[string]uint32{"": 1}, nil
}

// nextCursor prefers the response's own cursor and falls back to the last
// event's paging token, which is what older nodes provide.
func nextCursor(res rpc.GetEventsResult) string {
	if res.Cursor != "" {
		return res.Cursor
	}
	if len(res.Events) > 0 {
		return res.Events[len(res.Events)-1].PagingToken
	}
	return ""
}

// filterBatches splits watched contracts into request-sized groups, honoring
// both the per-filter contract cap and the per-request filter cap. With no
// watched contracts it returns a single batch matching every contract event.
func filterBatches(contracts []string) [][]rpc.EventFilter {
	if len(contracts) == 0 {
		return [][]rpc.EventFilter{{{Type: "contract"}}}
	}

	var filters []rpc.EventFilter
	for start := 0; start < len(contracts); start += rpc.MaxContractIDsPerFilter {
		end := min(start+rpc.MaxContractIDsPerFilter, len(contracts))
		filters = append(filters, rpc.EventFilter{
			Type:        "contract",
			ContractIDs: contracts[start:end],
		})
	}

	var batches [][]rpc.EventFilter
	for start := 0; start < len(filters); start += rpc.MaxFiltersPerRequest {
		end := min(start+rpc.MaxFiltersPerRequest, len(filters))
		batches = append(batches, filters[start:end])
	}
	return batches
}

// ErrStopped signals a clean shutdown to callers that distinguish it from a
// genuine failure.
var ErrStopped = errors.New("ingester stopped")
