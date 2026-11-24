package source

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeLimit(t *testing.T) {
	tests := []struct {
		name  string
		limit int
		want  int
	}{
		{"in range", 42, 42},
		{"lower bound", 1, 1},
		{"upper bound", MaxLimit, MaxLimit},
		{"zero defaults", 0, DefaultLimit},
		{"negative defaults", -5, DefaultLimit},
		{"above maximum is clamped", MaxLimit + 1, MaxLimit},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, NormalizeLimit(tt.limit))
		})
	}
}

func TestBuildEmptyBuckets(t *testing.T) {
	from := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	hour := time.Hour

	t.Run("splits the range into contiguous half-open buckets", func(t *testing.T) {
		got := BuildEmptyBuckets(from, from.Add(3*hour), hour)
		require.Len(t, got, 3)
		for i, wantStart := range []time.Time{from, from.Add(hour), from.Add(2 * hour)} {
			assert.Equal(t, wantStart, got[i].Start)
			assert.Equal(t, wantStart.Add(hour), got[i].End)
			assert.Zero(t, got[i].Count)
		}
	})

	t.Run("clips the final partial bucket to the end time", func(t *testing.T) {
		to := from.Add(2*hour + 15*time.Minute)
		got := BuildEmptyBuckets(from, to, hour)
		require.Len(t, got, 3)
		assert.Equal(t, from.Add(2*hour), got[2].Start)
		assert.Equal(t, to, got[2].End)
	})

	t.Run("normalizes non-UTC bounds to UTC", func(t *testing.T) {
		local := time.Date(2026, 8, 2, 13, 0, 0, 0, time.FixedZone("CEST", 3600))
		got := BuildEmptyBuckets(local, local.Add(hour), hour)
		require.Len(t, got, 1)
		assert.Equal(t, from, got[0].Start)
		assert.Equal(t, from.Add(hour), got[0].End)
		assert.Equal(t, time.UTC, got[0].Start.Location())
	})

	// Invalid bounds are not reported as an error: callers get an empty,
	// non-nil series they can range over without a nil check.
	invalid := []struct {
		name     string
		from, to time.Time
		interval time.Duration
	}{
		{"empty range", from, from, hour},
		{"reversed range", from.Add(hour), from, hour},
		{"zero interval", from, from.Add(hour), 0},
		{"negative interval", from, from.Add(hour), -time.Minute},
	}
	for _, tt := range invalid {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildEmptyBuckets(tt.from, tt.to, tt.interval)
			assert.NotNil(t, got)
			assert.Empty(t, got)
		})
	}
}
