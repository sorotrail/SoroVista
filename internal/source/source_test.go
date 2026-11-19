package source

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

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
