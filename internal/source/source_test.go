package source

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCountEventsIntoBuckets covers the pure tallying logic directly, without
// going through a backend. The half-open boundary rules are subtle enough that
// a regression here would otherwise only surface through an integration test.
func TestCountEventsIntoBuckets(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	interval := 15 * time.Minute
	to := base.Add(time.Hour) // four buckets: [00:00,00:15), [00:15,00:30), [00:30,00:45), [00:45,01:00)

	tests := []struct {
		name      string
		events    []Event
		wantCount []int64
		wantTotal int64
	}{
		{
			name:      "no events yields zero-count buckets",
			events:    nil,
			wantCount: []int64{0, 0, 0, 0},
			wantTotal: 0,
		},
		{
			name: "events are placed by timestamp with half-open boundaries",
			events: []Event{
				{ID: "at-from", LedgerClosedAt: base},
				{ID: "inside-first", LedgerClosedAt: base.Add(14*time.Minute + 59*time.Second)},
				{ID: "at-boundary", LedgerClosedAt: base.Add(15 * time.Minute)},
				{ID: "inside-last", LedgerClosedAt: base.Add(45 * time.Minute)},
			},
			wantCount: []int64{2, 1, 0, 1},
			wantTotal: 4,
		},
		{
			name: "events outside [from, to) are excluded",
			events: []Event{
				{ID: "before-from", LedgerClosedAt: base.Add(-time.Nanosecond)},
				{ID: "at-to", LedgerClosedAt: to},
				{ID: "after-to", LedgerClosedAt: to.Add(time.Second)},
			},
			wantCount: []int64{0, 0, 0, 0},
			wantTotal: 0,
		},
		{
			name: "zero LedgerClosedAt falls back to CreatedAt",
			events: []Event{
				{ID: "fallback", CreatedAt: base.Add(30 * time.Minute)},
			},
			wantCount: []int64{0, 0, 1, 0},
			wantTotal: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buckets, total := CountEventsIntoBuckets(base, to, interval, tt.events)

			require.Len(t, buckets, len(tt.wantCount))
			for i, want := range tt.wantCount {
				assert.Equal(t, want, buckets[i].Count, "bucket %d", i)
				assert.Equal(t, base.Add(time.Duration(i)*interval), buckets[i].Start)
				assert.Equal(t, base.Add(time.Duration(i+1)*interval), buckets[i].End)
			}
			assert.Equal(t, tt.wantTotal, total)
		})
	}
}

// TestCountEventsIntoBucketsInvalidRanges covers the inputs that cannot
// describe any bucket. The function reports them as an empty series rather
// than panicking on a division by a zero interval or a backwards range.
func TestCountEventsIntoBucketsInvalidRanges(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	events := []Event{{ID: "e1", LedgerClosedAt: base}}

	tests := []struct {
		name     string
		from     time.Time
		to       time.Time
		interval time.Duration
	}{
		{name: "empty range", from: base, to: base, interval: time.Minute},
		{name: "backwards range", from: base.Add(time.Hour), to: base, interval: time.Minute},
		{name: "zero interval", from: base, to: base.Add(time.Hour), interval: 0},
		{name: "negative interval", from: base, to: base.Add(time.Hour), interval: -time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buckets, total := CountEventsIntoBuckets(tt.from, tt.to, tt.interval, events)
			assert.Empty(t, buckets)
			assert.Zero(t, total)
		})
	}
}
