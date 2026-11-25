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
		name     string
		interval time.Duration
		want     string
	}{
		{name: "hours", interval: 2 * time.Hour, want: "2h"},
		{name: "minutes", interval: 15 * time.Minute, want: "15m"},
		{name: "seconds", interval: 30 * time.Second, want: "30s"},
		{name: "sub-second falls back to duration string", interval: 1500 * time.Millisecond, want: "1.5s"},
		{name: "zero", interval: 0, want: "0s"},
		{name: "negative", interval: -time.Minute, want: "0s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, FormatBucketInterval(tt.interval))
		})
	}
}
