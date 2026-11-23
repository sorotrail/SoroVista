// Package config loads SoroVista configuration from environment variables and
// optionally a YAML configuration file. The environment always wins on any
// conflict, keeping container deployments working and secrets overridable
// without editing a file.
//
// The file path is configured via the CONFIG_FILE environment variable; if
// unset, the default is "./config.yaml". A file that is present but malformed
// is a startup failure naming the file and the problem; a missing file at the
// default path is not an error.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
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
)

// Config holds all runtime configuration. Every field maps to one environment
// variable; see .env.example for the full list. A YAML configuration file may
// also supply values; the environment always wins on any conflict, keeping
// container deployments working and secrets overridable without editing a file.
//
// The file path is configured via the CONFIG_FILE environment variable; if
// unset, the default is "./config.yaml". A file that is present but malformed
// is a startup failure naming the file and the problem; a missing file at the
// default path is not an error.
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
	RetentionLedgers uint32

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

	// ConfigFile is the path to the YAML configuration file loaded at
	// startup. Set via the CONFIG_FILE environment variable.
	ConfigFile string
}

// UsesDatabase reports whether the selected mode needs Postgres. Upstream mode
// reads everything from SoroTrail, so main skips migrations and the pool.
func (c Config) UsesDatabase() bool { return c.SourceMode == ModeRPC }

// configFileValues holds the values parsed from a YAML configuration file.
// Each field is omitted if empty (nil pointer) so that unset fields don't override
// their environment defaults.
type configFileValues struct {
	SourceMode       *string            `yaml:"source_mode"`
	Network          *networkFileValues `yaml:"network"`
	RPCURL           *string            `yaml:"rpc_url"`
	DatabaseURL      *string            `yaml:"database_url"`
	PollInterval     *string            `yaml:"poll_interval"`
	WatchedContracts *[]string          `yaml:"watched_contracts"`
	StartLedger      *uint32            `yaml:"start_ledger"`
	RetentionLedgers *uint32            `yaml:"retention_ledgers"`
	SoroTrailURL     *string            `yaml:"sorotrail_url"`
	CORSAllowedOrigins *[]string        `yaml:"cors_allowed_origins"`
	HTTPAddr         *string            `yaml:"http_addr"`
	LogLevel         *string            `yaml:"log_level"`
	ConfigFile       *string            `yaml:"-"` // ignored
}

// networkFileValues holds the network-related values from a YAML config file.
type networkFileValues struct {
	Network    *string `yaml:"network"`
	RPCURL     *string `yaml:"rpc_url"`
	NetworkPassphrase *string `yaml:"network_passphrase"`
}

// loadConfigFile parses the YAML configuration file at the given path.
// A missing file is not an error; it returns zero values.
func loadConfigFile(path string) (configFileValues, error) {
	// A missing file is not an error — return zero values.
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return configFileValues{}, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return configFileValues{}, fmt.Errorf("reading config file %q: %w", path, err)
	}

	var raw configFileValues
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return configFileValues{}, fmt.Errorf("parsing config file %q: %w", path, err)
	}

	return raw, nil
}

// newConfigFromFile creates a Config populated with defaults from a
// configFileValues struct. Fields with nil pointers in the file struct are
// left at their zero values, to be overridden by environment variables later.
func newConfigFromFile(file configFileValues) Config {
	cfg := Config{
		SourceMode:   DefaultSourceMode,
		Network:      Network{}, // zero value
		RPCURL:       DefaultRPCURL,
		DatabaseURL:  "",
		PollInterval: DefaultPollInterval,
		SoroTrailURL: "",
		RetentionLedgers: DefaultRetentionLedgers,
		HTTPAddr:     DefaultHTTPAddr,
		LogLevel:     slog.LevelInfo,
	}

	// Apply file values as defaults (will be overridden by env vars).
	if file.SourceMode != nil {
		cfg.SourceMode = SourceMode(*file.SourceMode)
	}
	if file.Network != nil {
		if file.Network.RPCURL != nil {
			cfg.RPCURL = *file.Network.RPCURL
		}
	}
	if file.RPCURL != nil {
		cfg.RPCURL = *file.RPCURL
	}
	if file.DatabaseURL != nil {
		cfg.DatabaseURL = *file.DatabaseURL
	}
	if file.PollInterval != nil {
		d, err := time.ParseDuration(*file.PollInterval)
		if err == nil && d >= time.Second {
			cfg.PollInterval = d
		}
	}
	if file.WatchedContracts != nil {
		cfg.WatchedContracts = *file.WatchedContracts
	}
	if file.StartLedger != nil {
		cfg.StartLedger = *file.StartLedger
	}
	if file.RetentionLedgers != nil {
		cfg.RetentionLedgers = *file.RetentionLedgers
	}
	if file.SoroTrailURL != nil {
		cfg.SoroTrailURL = *file.SoroTrailURL
	}
	if file.CORSAllowedOrigins != nil {
		cfg.CORSAllowedOrigins = *file.CORSAllowedOrigins
	}
	if file.HTTPAddr != nil {
		cfg.HTTPAddr = *file.HTTPAddr
	}
	if file.LogLevel != nil {
		lvl, err := parseLevel(*file.LogLevel)
		if err == nil {
			cfg.LogLevel = lvl
		}
	}

	return cfg
}

// Load reads configuration from the environment and optionally a YAML
// configuration file. The environment always wins on any conflict, keeping
// container deployments working and secrets overridable without editing a file.
//
// The file path is configured via the CONFIG_FILE environment variable; if
// unset, the default is "./config.yaml". A file that is present but malformed
// is a startup failure naming the file and the problem; a missing file at the
// default path is not an error.
func Load() (Config, error) {
	// --- Step 1: Load config file (if present) ---
	cfgPath := strings.TrimRight(os.Getenv("CONFIG_FILE"), "/")
	if cfgPath == "" {
		cfgPath = "./config.yaml"
	}

	file, err := loadConfigFile(cfgPath)
	if err != nil {
		return Config{}, fmt.Errorf("config file %q: %w", cfgPath, err)
	}

	// --- Step 2: Create cfg from file values as defaults ---
	cfg := newConfigFromFile(file)

	// --- Step 3: Override with environment variables (env wins over file) ---
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

	if v := os.Getenv("NETWORK"); v != "" {
		// ParseNetwork will handle the full network resolution, but we at
		// least set the name so the default doesn't clobber a custom setting
		// if ParseNetwork hasn't run yet. ParseNetwork will run later and
		// fully resolve it.
		cfg.Network.Name = strings.ToLower(v)
	}

	if v := os.Getenv("RPC_URL"); v != "" {
		cfg.RPCURL = v
	}

	if v := os.Getenv("DATABASE_URL"); v != "" {
		cfg.DatabaseURL = v
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

	if v := os.Getenv("SOROTRAIL_URL"); v != "" {
		cfg.SoroTrailURL = strings.TrimRight(v, "/")
	}

	if v := os.Getenv("HTTP_ADDR"); v != "" {
		cfg.HTTPAddr = v
	}

	// --- Step 4: ParseNetwork resolves NETWORK, RPC_URL, NETWORK_PASSPHRASE ---
	// We pass a custom getenv that checks the config file values first, then
	// falls back to os.Getenv. This way file defaults are visible to
	// ParseNetwork, but shell env vars still win (since we only use the file
	// as fallback when the env var is not set).
	net, err := ParseNetwork(func(key string) string {
		// Check os.Getenv first (shell/env var wins)
		if v := os.Getenv(key); v != "" {
			return v
		}
		// Fall back to config file values
		switch strings.ToLower(key) {
		case "rpc_url":
			if file.RPCURL != nil {
				return *file.RPCURL
			}
		case "network_passphrase":
			if file.Network != nil && file.Network.NetworkPassphrase != nil {
				return *file.Network.NetworkPassphrase
			}
		case "network":
			if file.Network != nil && file.Network.Network != nil {
				return *file.Network.Network
			}
		}
		return ""
	})
	if err != nil {
		return cfg, fmt.Errorf("parsing network: %w", err)
	}
	cfg.Network = net
	cfg.RPCURL = net.RPCURL

	// --- Step 5: Validate ---
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