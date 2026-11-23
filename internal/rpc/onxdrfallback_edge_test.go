package rpc

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestOnXDRFallbackEdgeCases covers the edges of callback registration: a nil
// callback, the once-only guarantee, and registration after the client has
// already latched the fallback off.
func TestOnXDRFallbackEdgeCases(t *testing.T) {
	// A node that rejects xdrFormat:"json", like one predating the field.
	rejectingNode := func(req capturedRequest) string {
		var params GetEventsRequest
		require.NoError(t, json.Unmarshal(req.Params, &params))
		if params.XDRFormat != "" {
			return `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"unknown field xdrFormat"}}`
		}
		return `{"jsonrpc":"2.0","id":1,"result":{"events":[],"latestLedger":1}}`
	}

	tests := []struct {
		name        string
		run         func(t *testing.T, c *HTTPClient, hits *atomic.Int64)
		wantHits    int64
		wantJSONXDR bool
	}{
		{
			name: "nil callback is safe and fallback still works",
			run: func(t *testing.T, c *HTTPClient, _ *atomic.Int64) {
				c.OnXDRFallback(nil)
				_, err := c.GetEvents(context.Background(), GetEventsRequest{StartLedger: 1})
				require.NoError(t, err)
			},
			wantHits:    0,
			wantJSONXDR: false,
		},
		{
			name: "callback fires exactly once across requests",
			run: func(t *testing.T, c *HTTPClient, hits *atomic.Int64) {
				c.OnXDRFallback(func() { hits.Add(1) })
				for i := 0; i < 3; i++ {
					_, err := c.GetEvents(context.Background(), GetEventsRequest{StartLedger: uint32(i + 1)})
					require.NoError(t, err)
				}
			},
			wantHits:    1,
			wantJSONXDR: false,
		},
		{
			name: "callback registered after fallback is not called",
			run: func(t *testing.T, c *HTTPClient, hits *atomic.Int64) {
				_, err := c.GetEvents(context.Background(), GetEventsRequest{StartLedger: 1})
				require.NoError(t, err)

				c.OnXDRFallback(func() { hits.Add(1) })
				_, err = c.GetEvents(context.Background(), GetEventsRequest{StartLedger: 2})
				require.NoError(t, err)
			},
			wantHits:    0,
			wantJSONXDR: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, captured := fakeNode(t, rejectingNode)
			var hits atomic.Int64

			tt.run(t, client, &hits)

			if got := hits.Load(); got != tt.wantHits {
				t.Fatalf("callback fired %d times, want %d", got, tt.wantHits)
			}
			if got := client.JSONXDR(); got != tt.wantJSONXDR {
				t.Fatalf("JSONXDR() = %v, want %v", got, tt.wantJSONXDR)
			}
			if len(*captured) == 0 {
				t.Fatal("expected at least one request to reach the node")
			}
		})
	}
}
