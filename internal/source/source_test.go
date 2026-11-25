package source

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeLimit(t *testing.T) {
	tests := []struct {
		name  string
		limit int
		want  int
	}{
		// Zero and negative limits must return DefaultLimit rather than
		// zero: a zero limit reaching SQL is an unbounded query.
		{"zero returns the default, not zero", 0, DefaultLimit},
		{"negative returns the default", -7, DefaultLimit},
		// The boundaries belong in the table: the smallest and largest
		// values NormalizeLimit accepts pass through unchanged.
		{"one passes through", 1, 1},
		{"mid-range passes through", 42, 42},
		{"MaxLimit passes through", MaxLimit, MaxLimit},
		// Anything above MaxLimit clamps down.
		{"MaxLimit+1 clamps to MaxLimit", MaxLimit + 1, MaxLimit},
		{"a very large value clamps to MaxLimit", 1 << 30, MaxLimit},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, NormalizeLimit(tt.limit))
		})
	}
}

func TestFormatBucketInterval(t *testing.T) {
	tests := []struct {
		name string
		d    time.Duration
		want string
	}{
		// Zero and negative durations return "0s"
		{"zero duration returns 0s", 0, "0s"},
		{"negative duration returns 0s", -10 * time.Minute, "0s"},

		// Whole hours
		{"one hour", time.Hour, "1h"},
		{"multiple hours", 6 * time.Hour, "6h"},
		{"24 hours", 24 * time.Hour, "24h"},

		// Whole minutes
		{"one minute", time.Minute, "1m"},
		{"15 minutes", 15 * time.Minute, "15m"},
		{"45 minutes", 45 * time.Minute, "45m"},

		// Whole seconds
		{"one second", time.Second, "1s"},
		{"30 seconds", 30 * time.Second, "30s"},
		{"45 seconds", 45 * time.Second, "45s"},

		// Fallback / mixed duration (default branch)
		{"fractional seconds", 1500 * time.Millisecond, "1.5s"},
		{"whole seconds greater than 60", 90 * time.Second, "90s"},
		{"sub-second milliseconds", 500 * time.Millisecond, "500ms"},
		{"mixed minutes, seconds and millis", 90*time.Second + 500*time.Millisecond, "1m30.5s"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, FormatBucketInterval(tt.d))
		})
	}
}
