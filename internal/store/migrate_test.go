package store

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigrationFilesArePairedAndVersionsAreUnique(t *testing.T) {
	files, err := migrationsFS.ReadDir("migrations")
	require.NoError(t, err)
	require.NotEmpty(t, files)

	upFiles := make(map[string]bool)
	downFiles := make(map[string]bool)
	versions := make(map[string]string)

	for _, file := range files {
		name := file.Name()
		switch {
		case strings.HasSuffix(name, ".up.sql"):
			base := strings.TrimSuffix(name, ".up.sql")
			version, ok := migrationVersion(base)
			require.True(t, ok, "invalid up migration filename %q", name)
			if previous, exists := versions[version]; exists {
				t.Errorf("migrations %q and %q share version %s", previous, name, version)
			} else {
				versions[version] = name
			}
			upFiles[base] = true
		case strings.HasSuffix(name, ".down.sql"):
			base := strings.TrimSuffix(name, ".down.sql")
			_, ok := migrationVersion(base)
			require.True(t, ok, "invalid down migration filename %q", name)
			downFiles[base] = true
		}
	}

	for base := range upFiles {
		assert.True(t, downFiles[base], "up migration %q has no matching down migration", base)
	}
	for base := range downFiles {
		assert.True(t, upFiles[base], "down migration %q has no matching up migration", base)
	}
}

func TestMigrateUpDownRoundTrip(t *testing.T) {
	databaseURL := migrationTestURL(t)

	require.NoError(t, Migrate(databaseURL), "up from an empty schema")
	require.NoError(t, Migrate(databaseURL), "a second up must be idempotent")

	migrateAllDown(t, databaseURL)
	require.NoError(t, Migrate(databaseURL), "up after a full down")
}

func migrationVersion(base string) (string, bool) {
	parts := strings.SplitN(base, "_", 2)
	if len(parts) != 2 || parts[1] == "" {
		return "", false
	}
	version, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil {
		return "", false
	}
	return strconv.FormatUint(version, 10), true
}

func migrationTestURL(t *testing.T) string {
	t.Helper()

	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set; skipping database-backed migration test")
	}

	parsed, err := url.Parse(databaseURL)
	require.NoError(t, err)
	require.NotEmpty(t, parsed.Scheme, "TEST_DATABASE_URL must be a PostgreSQL URL")

	schema := fmt.Sprintf("sorovista_migrate_test_%d", time.Now().UnixNano())
	db, err := sql.Open("pgx", databaseURL)
	require.NoError(t, err)
	_, err = db.Exec(fmt.Sprintf(`CREATE SCHEMA "%s"`, schema))
	require.NoError(t, err)
	require.NoError(t, db.Close())

	t.Cleanup(func() {
		cleanupDB, err := sql.Open("pgx", databaseURL)
		require.NoError(t, err)
		defer func() { require.NoError(t, cleanupDB.Close()) }()
		_, err = cleanupDB.Exec(fmt.Sprintf(`DROP SCHEMA "%s" CASCADE`, schema))
		require.NoError(t, err)
	})

	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func migrateAllDown(t *testing.T, databaseURL string) {
	t.Helper()

	db, err := sql.Open("pgx", databaseURL)
	require.NoError(t, err)
	src, err := iofs.New(migrationsFS, "migrations")
	require.NoError(t, err)
	driver, err := postgres.WithInstance(db, &postgres.Config{})
	require.NoError(t, err)
	m, err := migrate.NewWithInstance("iofs", src, "postgres", driver)
	require.NoError(t, err)

	closed := false
	t.Cleanup(func() {
		if closed {
			return
		}
		sourceErr, databaseErr := m.Close()
		assert.NoError(t, errors.Join(sourceErr, databaseErr))
	})

	require.NoError(t, m.Down())
	sourceErr, databaseErr := m.Close()
	closed = true
	require.NoError(t, errors.Join(sourceErr, databaseErr))
}
