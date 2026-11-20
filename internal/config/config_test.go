package config

import (
	"log/slog"
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
		{
			name: "tracing is off by default",
			env:  map[string]string{"DATABASE_URL": "postgres://localhost/sorovista"},
			check: func(t *testing.T, c Config) {
				assert.False(t, c.OTelEnabled, "tracing must be opt-in")
				assert.Equal(t, DefaultOTelSampleRate, c.OTelSampleRate)
				assert.Equal(t, DefaultOTelServiceName, c.OTelServiceName)
				assert.Equal(t, DefaultOTelEndpoint, c.OTelEndpoint)
			},
		},
		{
			name: "tracing configuration is parsed",
			env: map[string]string{
				"DATABASE_URL":                "postgres://localhost/sorovista",
				"OTEL_ENABLED":                "true",
				"OTEL_EXPORTER_OTLP_ENDPOINT": "collector:4318",
				"OTEL_SAMPLE_RATE":            "0.25",
				"OTEL_SERVICE_NAME":           "sorovista-edge",
			},
			check: func(t *testing.T, c Config) {
				assert.True(t, c.OTelEnabled)
				assert.Equal(t, "collector:4318", c.OTelEndpoint)
				assert.InDelta(t, 0.25, c.OTelSampleRate, 1e-9)
				assert.Equal(t, "sorovista-edge", c.OTelServiceName)
			},
		},
		{
			name: "an out-of-range sample rate is rejected",
			env: map[string]string{
				"DATABASE_URL":     "postgres://localhost/sorovista",
				"OTEL_SAMPLE_RATE": "1.5",
			},
			wantErr: "invalid OTEL_SAMPLE_RATE",
		},
		{
			name: "a non-boolean OTEL_ENABLED is rejected",
			env: map[string]string{
				"DATABASE_URL": "postgres://localhost/sorovista",
				"OTEL_ENABLED": "yes-please",
			},
			wantErr: "invalid OTEL_ENABLED",
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
