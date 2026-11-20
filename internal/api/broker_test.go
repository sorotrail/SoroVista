package api

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sorotrail/sorovista/internal/source"
)

func TestNewBroker(t *testing.T) {
	tests := []struct {
		name        string
		newBroker   func() *Broker
		wantBufSize int
	}{
		{
			name:        "default buffer size",
			newBroker:   NewBroker,
			wantBufSize: DefaultSubscriberBufferSize,
		},
		{
			name:        "explicit buffer size",
			newBroker:   func() *Broker { return NewBrokerWithBuffer(8) },
			wantBufSize: 8,
		},
		{
			name:        "zero buffer falls back to default",
			newBroker:   func() *Broker { return NewBrokerWithBuffer(0) },
			wantBufSize: DefaultSubscriberBufferSize,
		},
		{
			name:        "negative buffer falls back to default",
			newBroker:   func() *Broker { return NewBrokerWithBuffer(-1) },
			wantBufSize: DefaultSubscriberBufferSize,
		},
		{
			name:        "minimum buffer size of one",
			newBroker:   func() *Broker { return NewBrokerWithBuffer(1) },
			wantBufSize: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := tt.newBroker()
			require.NotNil(t, b)
			require.NotNil(t, b.subscribers)
			require.Equal(t, tt.wantBufSize, b.bufSize)
			require.Zero(t, b.SubscriberCount())

			// A freshly built broker must accept subscriptions regardless of
			// how its buffer was configured, and the channel uses that buffer.
			ch, cancel := b.Subscribe(source.EventQuery{})
			require.NotNil(t, ch)
			require.Equal(t, tt.wantBufSize, cap(ch))
			require.Equal(t, 1, b.SubscriberCount())
			cancel()
			require.Zero(t, b.SubscriberCount())
		})
	}
}

func TestEventMatchesQuerySuccessfulCall(t *testing.T) {
	yes, no := true, false
	tests := []struct {
		name  string
		want  *bool
		calls []bool
		pass  []bool
	}{
		{
			name:  "unset matches successful and failed events",
			want:  nil,
			calls: []bool{true, false},
			pass:  []bool{true, true},
		},
		{
			name:  "successful only",
			want:  &yes,
			calls: []bool{true, false},
			pass:  []bool{true, false},
		},
		{
			name:  "failed only",
			want:  &no,
			calls: []bool{true, false},
			pass:  []bool{false, true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for i, successful := range tt.calls {
				got := eventMatchesQuery(source.EventQuery{InSuccessfulCall: tt.want}, source.Event{InSuccessfulCall: successful})
				require.Equal(t, tt.pass[i], got)
			}
		})
	}
}

func TestSubscriberCount(t *testing.T) {
	tests := []struct {
		name  string
		setup func() (*Broker, func())
		want  int
	}{
		{
			name: "new broker has no subscribers",
			setup: func() (*Broker, func()) {
				return NewBroker(), func() {}
			},
			want: 0,
		},
		{
			name: "nil broker reports zero",
			setup: func() (*Broker, func()) {
				var b *Broker
				return b, func() {}
			},
			want: 0,
		},
		{
			name: "one subscription",
			setup: func() (*Broker, func()) {
				b := NewBroker()
				_, cancel := b.Subscribe(source.EventQuery{})
				return b, cancel
			},
			want: 1,
		},
		{
			name: "several subscriptions",
			setup: func() (*Broker, func()) {
				b := NewBroker()
				var cancels []func()
				for range 3 {
					_, cancel := b.Subscribe(source.EventQuery{})
					cancels = append(cancels, cancel)
				}
				return b, func() {
					for _, cancel := range cancels {
						cancel()
					}
				}
			},
			want: 3,
		},
		{
			name: "cancelled subscription is removed",
			setup: func() (*Broker, func()) {
				b := NewBroker()
				_, cancel := b.Subscribe(source.EventQuery{})
				cancel()
				return b, func() {}
			},
			want: 0,
		},
		{
			name: "shutdown clears subscribers",
			setup: func() (*Broker, func()) {
				b := NewBroker()
				_, cancel := b.Subscribe(source.EventQuery{})
				b.Shutdown()
				return b, cancel
			},
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, cleanup := tt.setup()
			defer cleanup()
			require.Equal(t, tt.want, b.SubscriberCount())
		})
	}
}
