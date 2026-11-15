package config

import (
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// validContract is a well-formed contract ID used across the tests.
const validContract = "CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC"

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
		check   func(t *testing.T, c Config)
	}{
		{
			name: "standalone defaults",
			env:  map[string]string{"DATABASE_URL": "postgres://localhost/sorovista"},
			check: func(t *testing.T, c Config) {
				assert.Equal(t, ModeRPC, c.SourceMode)
				assert.Equal(t, DefaultRPCURL, c.RPCURL)
				assert.Equal(t, DefaultPollInterval, c.PollInterval)
				assert.Equal(t, DefaultHTTPAddr, c.HTTPAddr)
				assert.Equal(t, slog.LevelInfo, c.LogLevel)
				assert.True(t, c.UsesDatabase())
			},
		},
		{
			name:    "standalone without a database fails fast",
			env:     map[string]string{"SOURCE_MODE": "rpc"},
			wantErr: "DATABASE_URL is required",
		},
		{
			name: "upstream mode needs no database",
			env: map[string]string{
				"SOURCE_MODE":   "sorotrail",
				"SOROTRAIL_URL": "http://sorotrail:8080",
			},
			check: func(t *testing.T, c Config) {
				assert.Equal(t, ModeSoroTrail, c.SourceMode)
				assert.Equal(t, "http://sorotrail:8080", c.SoroTrailURL)
				assert.False(t, c.UsesDatabase())
			},
		},
		{
			name:    "upstream mode without a URL fails fast",
			env:     map[string]string{"SOURCE_MODE": "sorotrail"},
			wantErr: "SOROTRAIL_URL is required",
		},
		{
			name: "upstream URL must carry a scheme",
			env: map[string]string{
				"SOURCE_MODE":   "sorotrail",
				"SOROTRAIL_URL": "sorotrail:8080",
			},
			wantErr: "must start with http:// or https://",
		},
		{
			name: "trailing slash is trimmed so paths do not double up",
			env: map[string]string{
				"SOURCE_MODE":   "sorotrail",
				"SOROTRAIL_URL": "https://sorotrail.example.com/",
			},
			check: func(t *testing.T, c Config) {
				assert.Equal(t, "https://sorotrail.example.com", c.SoroTrailURL)
			},
		},
		{
			name:    "unknown mode is rejected",
			env:     map[string]string{"SOURCE_MODE": "kafka"},
			wantErr: `invalid SOURCE_MODE "kafka"`,
		},
		{
			name: "mode is case-insensitive",
			env: map[string]string{
				"SOURCE_MODE":   "SoroTrail",
				"SOROTRAIL_URL": "http://localhost:8080",
			},
			check: func(t *testing.T, c Config) {
				assert.Equal(t, ModeSoroTrail, c.SourceMode)
			},
		},
		{
			name: "watched contracts are parsed and trimmed",
			env: map[string]string{
				"DATABASE_URL":      "postgres://localhost/sorovista",
				"WATCHED_CONTRACTS": " " + validContract + " , " + validContract + " ",
			},
			check: func(t *testing.T, c Config) {
				assert.Equal(t, []string{validContract, validContract}, c.WatchedContracts)
			},
		},
		{
			name: "a malformed watched contract is rejected",
			env: map[string]string{
				"DATABASE_URL":      "postgres://localhost/sorovista",
				"WATCHED_CONTRACTS": "not-a-contract",
			},
			wantErr: "invalid contract ID",
		},
		{
			name: "poll interval below the floor is rejected",
			env: map[string]string{
				"DATABASE_URL":  "postgres://localhost/sorovista",
				"POLL_INTERVAL": "100ms",
			},
			wantErr: "below the 1s minimum",
		},
		{
			name: "poll interval is parsed",
			env: map[string]string{
				"DATABASE_URL":  "postgres://localhost/sorovista",
				"POLL_INTERVAL": "30s",
			},
			check: func(t *testing.T, c Config) {
				assert.Equal(t, 30*time.Second, c.PollInterval)
			},
		},
		{
			name: "log level is parsed",
			env: map[string]string{
				"DATABASE_URL": "postgres://localhost/sorovista",
				"LOG_LEVEL":    "debug",
			},
			check: func(t *testing.T, c Config) {
				assert.Equal(t, slog.LevelDebug, c.LogLevel)
			},
		},
		{
			name: "invalid log level is rejected",
			env: map[string]string{
				"DATABASE_URL": "postgres://localhost/sorovista",
				"LOG_LEVEL":    "verbose",
			},
			wantErr: "invalid LOG_LEVEL",
		},
		{
			name: "start ledger must be positive",
			env: map[string]string{
				"DATABASE_URL": "postgres://localhost/sorovista",
				"START_LEDGER": "0",
			},
			wantErr: "invalid START_LEDGER",
		},
		{
			name: "start and retention ledgers are parsed",
			env: map[string]string{
				"DATABASE_URL":      "postgres://localhost/sorovista",
				"START_LEDGER":      "500",
				"RETENTION_LEDGERS": "1000",
			},
			check: func(t *testing.T, c Config) {
				assert.Equal(t, uint32(500), c.StartLedger)
				assert.Equal(t, uint32(1000), c.RetentionLedgers)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// t.Setenv isolates each case and restores the environment after.
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			cfg, err := Load()
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			if tt.check != nil {
				tt.check(t, cfg)
			}
		})
	}
}

func TestValidContractID(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want bool
	}{
		{"well-formed", validContract, true},
		{"empty", "", false},
		{"wrong prefix", "G" + validContract[1:], false},
		{"too short", validContract[:len(validContract)-1], false},
		{"too long", validContract + "A", false},
		{"lowercase is not strkey", "c" + validContract[1:], false},
		{"digits outside base32", validContract[:54] + "01", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ValidContractID(tt.id))
		})
	}
}

// parseLevel maps LOG_LEVEL onto a slog.Level. Half of it was unreached: the
// "warn" spelling, the case folding, and the default branch. The default branch
// matters beyond its message, because it returns a usable level along with the
// error: Load propagates the error, but a caller that ignored it would log at
// info rather than at whatever was asked for, so the pairing is pinned here.
func TestParseLevel(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    slog.Level
		wantErr string
	}{
		{"debug", "debug", slog.LevelDebug, ""},
		{"info", "info", slog.LevelInfo, ""},
		{"warn", "warn", slog.LevelWarn, ""},
		{"warning is accepted as warn", "warning", slog.LevelWarn, ""},
		{"error", "error", slog.LevelError, ""},
		{"upper case", "DEBUG", slog.LevelDebug, ""},
		{"mixed case", "WaRnInG", slog.LevelWarn, ""},
		{"empty is invalid", "", slog.LevelInfo, `invalid LOG_LEVEL ""`},
		{"unknown word", "verbose", slog.LevelInfo, `invalid LOG_LEVEL "verbose"`},
		{"numeric text is invalid", "3", slog.LevelInfo, `invalid LOG_LEVEL "3"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseLevel(tt.in)
			assert.Equal(t, tt.want, got)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.Contains(t, err.Error(), "want debug, info, warn or error")
		})
	}
}

// getenv returns the fallback for an unset or empty variable and the value
// otherwise. The empty case is the one that was unreached, and it is the one
// that matters: `LOG_LEVEL=` in a .env file must behave like leaving it out.
func TestGetenv(t *testing.T) {
	t.Run("set", func(t *testing.T) {
		t.Setenv("SOROVISTA_TEST_GETENV", "value")
		assert.Equal(t, "value", getenv("SOROVISTA_TEST_GETENV", "fallback"))
	})
	t.Run("empty falls back", func(t *testing.T) {
		t.Setenv("SOROVISTA_TEST_GETENV", "")
		assert.Equal(t, "fallback", getenv("SOROVISTA_TEST_GETENV", "fallback"))
	})
	t.Run("unset falls back", func(t *testing.T) {
		t.Setenv("SOROVISTA_TEST_GETENV", "x") // registered for restore, then cleared
		os.Unsetenv("SOROVISTA_TEST_GETENV")
		assert.Equal(t, "fallback", getenv("SOROVISTA_TEST_GETENV", "fallback"))
	})
	t.Run("no fallback", func(t *testing.T) {
		t.Setenv("SOROVISTA_TEST_GETENV", "")
		assert.Equal(t, "", getenv("SOROVISTA_TEST_GETENV", ""))
	})
}
