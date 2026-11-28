// Package config loads SoroVista configuration from environment variables.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// SourceMode selects where SoroVista reads contract events from.
type SourceMode string

const (
	// ModeRPC is standalone mode: SoroVista polls a Stellar RPC endpoint
	// itself and stores events in its own Postgres database.
	ModeRPC SourceMode = "rpc"
	// ModeSoroTrail is upstream mode: SoroVista reads from a SoroTrail
	// indexer's HTTP API and keeps no database of its own.
	ModeSoroTrail SourceMode = "sorotrail"
)

// Defaults used when the corresponding environment variable is unset.
const (
	DefaultSourceMode   = ModeRPC
	DefaultRPCURL       = "https://soroban-testnet.stellar.org"
	DefaultPollInterval = 5 * time.Second
	DefaultHTTPAddr     = ":8080"
	// Tracing defaults. A 10% head rate keeps a busy chain-tip instance from
	// flooding a collector while still producing enough traces to diagnose a
	// slow page view.
	DefaultOTelEndpoint    = "http://localhost:4318"
	DefaultOTelSampleRate  = 0.1
	DefaultOTelServiceName = "sorovista"
	// DefaultRetentionLedgers is how far back a cold start reaches when
	// START_LEDGER is unset — roughly 24h at ~5s per ledger. Stellar RPC
	// retains events for about 24h to 7d, so reaching further back than the
	// node's own window simply yields nothing.
	DefaultRetentionLedgers = 17280

	// DefaultPoolMaxOpenConns is the default maximum number of open connections
	// in the pgx connection pool. Matches pgxpool.DefaultMaxOpenConns.
	DefaultPoolMaxOpenConns = 0 // 0 means use pgxpool default
	// DefaultPoolMaxIdleConns is the default maximum number of idle connections
	// in the pgx connection pool.
	DefaultPoolMaxIdleConns = 0 // 0 means use pgxpool default
	// DefaultPoolMaxConnLifetime is the default maximum connection lifetime.
	DefaultPoolMaxConnLifetime = 0 // 0 means use pgxpool default
	// DefaultPoolMaxConnIdleTime is the default maximum connection idle time.
	DefaultPoolMaxConnIdleTime = 0 // 0 means use pgxpool default
)

// Config holds all runtime configuration. Every field maps to one environment
// variable; see .env.example for the full list.
type Config struct {
	// SourceMode selects the EventSource implementation (rpc or sorotrail).
	SourceMode SourceMode
	// Network is the Stellar network in standalone mode — its name,
	// passphrase and RPC endpoint, resolved from NETWORK / RPC_URL /
	// NETWORK_PASSPHRASE by ParseNetwork. Verified against the node at
	// startup; unused in upstream mode, where the SoroTrail indexer's own
	// deployment owns its network.
	Network Network

	// RPCURL is the Stellar RPC endpoint (JSON-RPC 2.0 over HTTP). Standalone
	// mode only.
	RPCURL string
	// DatabaseURL is a Postgres connection string in pgx format. Standalone
	// mode only; upstream mode uses no database.
	DatabaseURL string
	// PollInterval is how long the ingester sleeps between polls once it has
	// caught up to the latest ledger. Standalone mode only.
	PollInterval time.Duration
	// WatchedContracts limits ingestion to these contract IDs. Empty means
	// ingest events from every contract. Standalone mode only.
	WatchedContracts []string
	// StartLedger forces a cold start from this ledger instead of reaching
	// back RetentionLedgers from the chain tip. Standalone mode only.
	StartLedger uint32
	// RetentionLedgers is the cold-start reach-back when StartLedger is unset.
	// It has nothing to do with pruning: it only decides where a fresh
	// ingester begins reading.
	RetentionLedgers uint32
	// EventRetention is how long stored events are kept, measured against
	// their on-chain ledger_closed_at timestamp. Zero keeps everything, which
	// is the default so an upgrade never starts deleting an operator's data.
	// Standalone mode only.
	EventRetention time.Duration

	// SoroTrailURL is the base URL of a SoroTrail indexer's HTTP API, e.g.
	// http://localhost:8080. Upstream mode only.
	SoroTrailURL string

	// CORSAllowedOrigins is the allow-list of browser Origins permitted to
	// call the API cross-origin (CORS_ALLOWED_ORIGINS, comma-separated).
	// Empty disables CORS: same-origin pages, including SoroVista's own UI,
	// never need it.
	CORSAllowedOrigins []string
	// HTTPAddr is the listen address for the web UI and JSON API.
	HTTPAddr string
	// LogLevel is the minimum slog level (debug, info, warn, error).
	LogLevel slog.Level

	// OTelEnabled turns OpenTelemetry tracing on. Off by default: an
	// operator who sets nothing gets no exporter and no background work.
	OTelEnabled bool
	// OTelEndpoint is the OTLP/HTTP collector endpoint, with or without a
	// scheme. Used only when tracing is enabled.
	OTelEndpoint string
	// OTelSampleRate is the head sampling ratio for new traces, in [0,1].
	// ParentBased sampling means an inbound sampling decision is honoured
	// regardless of this value.
	OTelSampleRate float64
	// OTelServiceName names this service in traces. Defaults to "sorovista".
	OTelServiceName string

	// RateLimitRPS is the sustained per-client request rate for /api, in
	// requests per second. Zero disables rate limiting, which is the default
	// so an existing deployment does not change behaviour on upgrade.
	RateLimitRPS float64
	// RateLimitBurst is the maximum instantaneous burst per client. It must
	// be set together with RateLimitRPS; a burst smaller than one request
	// would reject everything.
	RateLimitBurst int
	// RateLimitTrustedProxy makes the limiter key on the first
	// X-Forwarded-For entry instead of the socket's remote address. That is
	// only safe behind a proxy that overwrites the header, because a client
	// can otherwise choose its own key.
	RateLimitTrustedProxy bool

	// PoolMaxOpenConns is the maximum number of open connections to the
	// Postgres database. If 0, pgxpool's default is used. Set this (along
	// with PoolMaxIdleConns) to bound the pool size relative to your
	// Postgres max_connections setting.
	PoolMaxOpenConns uint32
	// PoolMaxIdleConns is the maximum number of idle connections in the
	// pgx connection pool. If 0, pgxpool's default is used.
	PoolMaxIdleConns uint32
	// PoolMaxConnLifetime is the maximum amount of time a connection may be
	// reused. If 0, pgxpool's default is used. Connections older than this
	// age will be closed and replaced.
	PoolMaxConnLifetime time.Duration
	// PoolMaxConnIdleTime is the maximum amount of time a connection may be
	// idle. If 0, pgxpool's default is used. Connections older than this
	// age will be closed.
	PoolMaxConnIdleTime time.Duration
}

// UsesDatabase reports whether the selected mode needs Postgres. Upstream mode
// reads everything from SoroTrail, so main skips migrations and the pool.
func (c Config) UsesDatabase() bool { return c.SourceMode == ModeRPC }

// Load reads configuration from the environment and validates that the
// variables required by the selected SOURCE_MODE are present.
func Load() (Config, error) {
	net, err := ParseNetwork(os.Getenv)
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		SourceMode:       DefaultSourceMode,
		Network:          net,
		RPCURL:           net.RPCURL,
		DatabaseURL:      os.Getenv("DATABASE_URL"),
		PollInterval:     DefaultPollInterval,
		SoroTrailURL:     strings.TrimRight(os.Getenv("SOROTRAIL_URL"), "/"),
		RetentionLedgers: DefaultRetentionLedgers,
		HTTPAddr:         getenv("HTTP_ADDR", DefaultHTTPAddr),
		LogLevel:         slog.LevelInfo,
		OTelEndpoint:     getenv("OTEL_EXPORTER_OTLP_ENDPOINT", DefaultOTelEndpoint),
		OTelSampleRate:   DefaultOTelSampleRate,
		OTelServiceName:  getenv("OTEL_SERVICE_NAME", DefaultOTelServiceName),
		// Pool settings default to 0 (use pgxpool defaults).
		PoolMaxOpenConns:    parseUintEnv("POOL_MAX_OPEN_CONNS", 0),
		PoolMaxIdleConns:    parseUintEnv("POOL_MAX_IDLE_CONNS", 0),
		PoolMaxConnLifetime: parseDurationEnv("POOL_MAX_CONN_LIFETIME", 0),
		PoolMaxConnIdleTime: parseDurationEnv("POOL_MAX_CONN_IDLE_TIME", 0),
	}

	if v := os.Getenv("SOURCE_MODE"); v != "" {
		switch SourceMode(strings.ToLower(v)) {
		case ModeRPC:
			cfg.SourceMode = ModeRPC
		case ModeSoroTrail:
			cfg.SourceMode = ModeSoroTrail
		default:
			return cfg, fmt.Errorf("invalid SOURCE_MODE %q (want %q or %q)", v, ModeRPC, ModeSoroTrail)
		}
	}

	if v := os.Getenv("POLL_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return cfg, fmt.Errorf("invalid POLL_INTERVAL %q: %w", v, err)
		}
		if d < time.Second {
			return cfg, fmt.Errorf("POLL_INTERVAL %q is below the 1s minimum", v)
		}
		cfg.PollInterval = d
	}

	if v := os.Getenv("WATCHED_CONTRACTS"); v != "" {
		for _, part := range strings.Split(v, ",") {
			id := strings.TrimSpace(part)
			if id == "" {
				continue
			}
			if !ValidContractID(id) {
				return cfg, fmt.Errorf("invalid contract ID %q in WATCHED_CONTRACTS", id)
			}
			cfg.WatchedContracts = append(cfg.WatchedContracts, id)
		}
	}

	if v := os.Getenv("START_LEDGER"); v != "" {
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil || n == 0 {
			return cfg, fmt.Errorf("invalid START_LEDGER %q: want a positive integer", v)
		}
		cfg.StartLedger = uint32(n)
	}

	if v := os.Getenv("RETENTION_LEDGERS"); v != "" {
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil || n == 0 {
			return cfg, fmt.Errorf("invalid RETENTION_LEDGERS %q: want a positive integer", v)
		}
		cfg.RetentionLedgers = uint32(n)
	}

	// EVENT_RETENTION is a duration, deliberately distinct from
	// RETENTION_LEDGERS so the two cannot be mistaken for each other: one
	// bounds storage, the other only picks a cold-start ledger.
	for _, name := range []string{"EVENT_RETENTION", "EVENT_RETENTION_PERIOD"} {
		v := os.Getenv(name)
		if v == "" {
			continue
		}
		d, err := time.ParseDuration(v)
		if err != nil {
			return cfg, fmt.Errorf("invalid %s %q: want a duration such as 720h", name, v)
		}
		if d <= 0 {
			return cfg, fmt.Errorf("invalid %s %q: want a positive duration", name, v)
		}
		cfg.EventRetention = d
		break
	}

	if v := os.Getenv("CORS_ALLOWED_ORIGINS"); v != "" {
		for _, o := range strings.Split(v, ",") {
			if o = strings.TrimSpace(o); o != "" {
				cfg.CORSAllowedOrigins = append(cfg.CORSAllowedOrigins, o)
			}
		}
	}

	if v := os.Getenv("LOG_LEVEL"); v != "" {
		lvl, err := parseLevel(v)
		if err != nil {
			return cfg, err
		}
		cfg.LogLevel = lvl
	}

	if v := os.Getenv("OTEL_ENABLED"); v != "" {
		enabled, err := strconv.ParseBool(v)
		if err != nil {
			return cfg, fmt.Errorf("invalid OTEL_ENABLED %q: want true or false", v)
		}
		cfg.OTelEnabled = enabled
	}

	if v := os.Getenv("OTEL_SAMPLE_RATE"); v != "" {
		rate, err := strconv.ParseFloat(v, 64)
		if err != nil || rate < 0 || rate > 1 {
			return cfg, fmt.Errorf("invalid OTEL_SAMPLE_RATE %q: want a number in [0,1]", v)
		}
		cfg.OTelSampleRate = rate
	}

	if v := os.Getenv("RATE_LIMIT_RPS"); v != "" {
		rps, err := strconv.ParseFloat(v, 64)
		if err != nil || rps <= 0 {
			return cfg, fmt.Errorf("invalid RATE_LIMIT_RPS %q: want a positive number", v)
		}
		cfg.RateLimitRPS = rps
	}

	if v := os.Getenv("RATE_LIMIT_BURST"); v != "" {
		burst, err := strconv.Atoi(v)
		if err != nil || burst <= 0 {
			return cfg, fmt.Errorf("invalid RATE_LIMIT_BURST %q: want a positive integer", v)
		}
		cfg.RateLimitBurst = burst
	}

	// Both halves are required together: a rate with no burst has no defined
	// instantaneous allowance, and a burst with no rate never refills.
	if (cfg.RateLimitRPS == 0) != (cfg.RateLimitBurst == 0) {
		return cfg, fmt.Errorf("RATE_LIMIT_RPS and RATE_LIMIT_BURST must be set together (leave both unset to disable rate limiting)")
	}

	if v := os.Getenv("RATE_LIMIT_TRUSTED_PROXY"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return cfg, fmt.Errorf("invalid RATE_LIMIT_TRUSTED_PROXY %q: want true or false", v)
		}
		cfg.RateLimitTrustedProxy = b
	}

	if err := cfg.validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// validate enforces the per-mode requirements, failing fast with a message
// that names both the missing variable and the mode that requires it.
func (c Config) validate() error {
	switch c.SourceMode {
	case ModeRPC:
		if c.DatabaseURL == "" {
			return fmt.Errorf("DATABASE_URL is required when SOURCE_MODE=%s (standalone mode stores events itself)", ModeRPC)
		}
		if c.RPCURL == "" {
			return fmt.Errorf("RPC_URL is required when SOURCE_MODE=%s", ModeRPC)
		}
		// Validate pool settings when using standalone mode.
		if err := c.ValidatePoolSettings(); err != nil {
			return err
		}
	case ModeSoroTrail:
		if c.SoroTrailURL == "" {
			return fmt.Errorf("SOROTRAIL_URL is required when SOURCE_MODE=%s (upstream mode reads from a SoroTrail indexer)", ModeSoroTrail)
		}
		if !strings.HasPrefix(c.SoroTrailURL, "http://") && !strings.HasPrefix(c.SoroTrailURL, "https://") {
			return fmt.Errorf("SOROTRAIL_URL %q must start with http:// or https://", c.SoroTrailURL)
		}
	default:
		return fmt.Errorf("unknown SOURCE_MODE %q", c.SourceMode)
	}

	if c.HTTPAddr == "" {
		return fmt.Errorf("HTTP_ADDR must not be empty")
	}
	if c.OTelEnabled && c.OTelEndpoint == "" {
		return fmt.Errorf("OTEL_EXPORTER_OTLP_ENDPOINT must not be empty when OTEL_ENABLED=true")
	}
	return nil
}

// ValidatePoolSettings checks that pool configuration values are consistent.
// A maximum below the minimum, or a zero maximum when a minimum is set, is a
// configuration error.
func (c Config) ValidatePoolSettings() error {
	// When PoolMaxOpenConns is set, it must be positive.
	if c.PoolMaxOpenConns > 0 && c.PoolMaxOpenConns < 1 {
		return fmt.Errorf("POOL_MAX_OPEN_CONNS %d must be positive when set", c.PoolMaxOpenConns)
	}
	// When both PoolMaxOpenConns and PoolMaxIdleConns are set, idle must not
	// exceed the maximum.
	if c.PoolMaxOpenConns > 0 && c.PoolMaxIdleConns > 0 && c.PoolMaxIdleConns > c.PoolMaxOpenConns {
		return fmt.Errorf("POOL_MAX_IDLE_CONNS %d exceeds POOL_MAX_OPEN_CONNS %d", c.PoolMaxIdleConns, c.PoolMaxOpenConns)
	}
	// When PoolMaxConnLifetime is set, it must be positive.
	if c.PoolMaxConnLifetime > 0 && c.PoolMaxConnLifetime <= 0 {
		return fmt.Errorf("POOL_MAX_CONN_LIFETIME must be positive when set")
	}
	// When PoolMaxConnIdleTime is set, it must be positive.
	if c.PoolMaxConnIdleTime > 0 && c.PoolMaxConnIdleTime <= 0 {
		return fmt.Errorf("POOL_MAX_CONN_IDLE_TIME must be positive when set")
	}
	return nil
}

// contractIDPattern matches a strkey-encoded contract ID: 'C' followed by 55
// base32 characters.
var contractIDPattern = regexp.MustCompile(`^C[A-Z2-7]{55}$`)

// ValidContractID reports whether s looks like a Soroban contract ID. This is
// a shape check, not a checksum verification.
func ValidContractID(s string) bool { return contractIDPattern.MatchString(s) }

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("invalid LOG_LEVEL %q (want debug, info, warn or error)", s)
	}
}

// parseUintEnv reads an environment variable as uint32, returning the fallback
// when the variable is unset or unparseable.
func parseUintEnv(key string, fallback uint32) uint32 {
	if v := os.Getenv(key); v != "" {
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			return fallback
		}
		return uint32(n)
	}
	return fallback
}

// parseDurationEnv reads an environment variable as time.Duration, returning the
// fallback when the variable is unset or unparseable.
func parseDurationEnv(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fallback
		}
		return d
	}
	return fallback
}
