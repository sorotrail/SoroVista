package rpc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestJSONXDREdgeCases covers the states JSONXDR must report: the optimistic
// default, unchanged after an error unrelated to xdrFormat, and latched off
// permanently once the node rejects the field.
func TestJSONXDREdgeCases(t *testing.T) {
	tests := []struct {
		name        string
		handler     func(req capturedRequest) string
		calls       int
		wantJSONXDR bool
	}{
		{
			name: "new client reports json format as enabled",
			handler: func(capturedRequest) string {
				return `{"jsonrpc":"2.0","id":1,"result":{"events":[],"latestLedger":1}}`
			},
			calls:       1,
			wantJSONXDR: true,
		},
		{
			name: "unrelated rpc error leaves json format enabled",
			handler: func(capturedRequest) string {
				return `{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"startLedger exceeds latest ledger"}}`
			},
			calls:       1,
			wantJSONXDR: true,
		},
		{
			name: "xdrFormat rejection latches json format off",
			handler: func(req capturedRequest) string {
				var params GetEventsRequest
				require.NoError(t, json.Unmarshal(req.Params, &params))
				if params.XDRFormat != "" {
					return `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"unknown field xdrFormat"}}`
				}
				return `{"jsonrpc":"2.0","id":1,"result":{"events":[],"latestLedger":1}}`
			},
			calls:       2, // a later call must not re-enable json format
			wantJSONXDR: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, _ := fakeNode(t, tt.handler)

			for i := 0; i < tt.calls; i++ {
				// A rejected first attempt is retried and succeeds; an
				// unrelated error is expected to surface.
				_, _ = client.GetEvents(context.Background(), GetEventsRequest{StartLedger: uint32(i + 1)})
			}

			if got := client.JSONXDR(); got != tt.wantJSONXDR {
				t.Fatalf("JSONXDR() = %v, want %v", got, tt.wantJSONXDR)
			}
			// Repeated reads must be stable, not consume state.
			if got := client.JSONXDR(); got != tt.wantJSONXDR {
				t.Fatalf("second JSONXDR() = %v, want %v", got, tt.wantJSONXDR)
			}
		})
	}
}
