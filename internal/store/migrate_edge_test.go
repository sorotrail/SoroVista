package store

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestMigrateEdgeCases covers inputs at the edges of what Migrate accepts:
// empty/whitespace input, malformed URLs, and an unreachable host. These must
// fail fast with an error rather than panic or hang.
func TestMigrateEdgeCases(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{name: "empty input", url: "", wantErr: true},
		{name: "whitespace-only input", url: "   \t\n", wantErr: true},
		{name: "malformed url", url: "not-a-url", wantErr: true},
		{name: "unreachable host", url: "postgres://sorovista:sorovista@127.0.0.1:1/sorovista?sslmode=disable", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Migrate(tt.url)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}

	t.Run("already-migrated database is a no-op", func(t *testing.T) {
		databaseURL := os.Getenv("TEST_DATABASE_URL")
		if databaseURL == "" {
			t.Skip("TEST_DATABASE_URL is not set; skipping database-backed case")
		}
		require.NoError(t, Migrate(databaseURL))
		require.NoError(t, Migrate(databaseURL))
	})
}
