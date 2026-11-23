package reqid

import (
	"context"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestFromEdgeCases covers inputs at the edges of what From must tolerate:
// no ID in the context, an ID that is present but empty, a value of the wrong
// type, and the maximum ID length the middleware will store. Every case must
// yield a usable ID rather than an empty string or a panic.
func TestFromEdgeCases(t *testing.T) {
	tests := []struct {
		name string
		ctx  context.Context
		// wantID is the exact ID expected, or empty when From must generate a
		// fresh one.
		wantID string
	}{
		{
			name: "missing context value generates an ID",
			ctx:  context.Background(),
		},
		{
			name: "empty context value generates an ID",
			ctx:  context.WithValue(context.Background(), ctxKey{}, ""),
		},
		{
			name: "non-string context value generates an ID",
			ctx:  context.WithValue(context.Background(), ctxKey{}, 42),
		},
		{
			name:   "maximum length ID is returned unchanged",
			ctx:    context.WithValue(context.Background(), ctxKey{}, strings.Repeat("a", 128)),
			wantID: strings.Repeat("a", 128),
		},
	}

	seen := map[string]bool{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := From(httptest.NewRequest(http.MethodGet, "/", nil).WithContext(tt.ctx))

			if tt.wantID != "" {
				if got != tt.wantID {
					t.Fatalf("From() = %q, want %q", got, tt.wantID)
				}
				return
			}

			if len(got) != 16 {
				t.Fatalf("From() = %q: want a generated 16-hex-char ID", got)
			}
			if _, err := hex.DecodeString(got); err != nil {
				t.Fatalf("From() = %q: not valid hex: %v", got, err)
			}
			if seen[got] {
				t.Fatalf("From() generated duplicate ID %q", got)
			}
			seen[got] = true
		})
	}
}
