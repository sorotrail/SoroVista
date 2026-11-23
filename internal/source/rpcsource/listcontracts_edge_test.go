package rpcsource

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sorotrail/sorovista/internal/source"
)

// TestListContractsEdgeCases covers the edges of the read path: a store with
// no matching contracts, a store failure, and a query at the shared maximum
// limit, which must be forwarded to the store untouched.
func TestListContractsEdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		store    *stubStore
		query    source.ContractQuery
		wantPage source.ContractPage
		wantErr  bool
	}{
		{
			name:     "no contracts stored returns an empty page",
			store:    &stubStore{},
			query:    source.ContractQuery{Search: "missing"},
			wantPage: source.ContractPage{},
		},
		{
			name:     "store failure returns a zero page and the error",
			store:    &stubStore{err: errors.New("database exploded")},
			query:    source.ContractQuery{},
			wantErr:  true,
			wantPage: source.ContractPage{},
		},
		{
			name: "maximum limit and cursor are forwarded unchanged",
			store: &stubStore{
				contracts:    []source.Contract{{ID: "CABC", EventCount: 1}},
				contractNext: "next",
			},
			query: source.ContractQuery{Search: "CA", Cursor: "cursor-1", Limit: source.MaxLimit},
			wantPage: source.ContractPage{
				Contracts:  []source.Contract{{ID: "CABC", EventCount: 1}},
				NextCursor: "next",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := New(tt.store, healthyRPC())

			page, err := src.ListContracts(context.Background(), tt.query)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			assert.Equal(t, tt.wantPage, page)
			// Even for an empty result the query must reach the store exactly
			// as the caller supplied it.
			assert.Equal(t, tt.query, tt.store.lastContractQuery)
		})
	}
}
