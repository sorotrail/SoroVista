// Command sorovista serves the contract explorer: a web UI and a read-only
// JSON API over decoded Soroban contract events.
//
// It runs in one of two modes, selected by SOURCE_MODE:
//
//	rpc        standalone — poll Stellar RPC and store events in Postgres
//	sorotrail  upstream   — read from a SoroTrail indexer's HTTP API
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/sorotrail/sorovista/internal/api"
	"github.com/sorotrail/sorovista/internal/config"
	"github.com/sorotrail/sorovista/internal/decode"
	"github.com/sorotrail/sorovista/internal/ingest"
	"github.com/sorotrail/sorovista/internal/metrics"
	"github.com/sorotrail/sorovista/internal/reqid"
	"github.com/sorotrail/sorovista/internal/retention"
	"github.com/sorotrail/sorovista/internal/rpc"
	"github.com/sorotrail/sorovista/internal/source"
	"github.com/sorotrail/sorovista/internal/source/rpcsource"
	"github.com/sorotrail/sorovista/internal/source/sorotrailsource"
	"github.com/sorotrail/sorovista/internal/store"
	"github.com/sorotrail/sorovista/internal/tracing"
	"github.com/sorotrail/sorovista/internal/web"
)

// shutdownTimeout bounds how long in-flight requests may finish after a signal.
const shutdownTimeout = 15 * time.Second

func main() {
	// Handle the "validate" subcommand before anything else.
	if len(os.Args) > 1 && os.Args[1] == "validate" {
		if err := runValidate(); err != nil {
			fmt.Fprintf(os.Stderr, "sorovista validate: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		// The logger may not exist yet if configuration failed, so report the
		// startup failure on stderr unconditionally.
		fmt.Fprintf(os.Stderr, "sorovista: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(log)

	// Cancelled on SIGINT/SIGTERM, which unwinds the ingester and the server.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	m := metrics.New()

	tracer, err := tracing.Setup(ctx, tracing.Config{
		Enabled:     cfg.OTelEnabled,
		Endpoint:    cfg.OTelEndpoint,
		SampleRate:  cfg.OTelSampleRate,
		ServiceName: cfg.OTelServiceName,
	}, log)
	if err != nil {
		return err
	}
	// Flushing is bounded by the same shutdown budget as the HTTP server, so
	// a slow or dead collector can never be the reason a pod fails to exit.
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := tracer.Shutdown(shutdownCtx); err != nil {
			log.Warn("tracing shutdown incomplete", "error", err)
		}
	}()

	var broker *api.Broker
	if cfg.SourceMode == config.ModeRPC {
		broker = api.NewBroker()
	}

	src, cleanup, startIngest, startSweep, err := buildSource(ctx, cfg, log, m, tracer, broker)
	if err != nil {
		return err
	}
	defer cleanup()

	log.Info("starting sorovista",
		"mode", cfg.SourceMode,
		"http_addr", cfg.HTTPAddr)

	var wg sync.WaitGroup
	if startIngest != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := startIngest(ctx); err != nil {
				log.Error("ingester exited", "error", err)
			}
		}()
	}
	if startSweep != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := startSweep(ctx); err != nil {
				log.Error("retention sweeper exited", "error", err)
			}
		}()
	}

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router(src, log, m, cfg, tracer, broker),
		ReadHeaderTimeout: 10 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() {
		log.Info("http server listening", "addr", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case err := <-serveErr:
		stop()
		broker.Shutdown()
		wg.Wait()
		if err != nil {
			return fmt.Errorf("http server: %w", err)
		}
		return nil
	case <-ctx.Done():
		log.Info("shutdown signal received")
	}

	// Close active SSE streams before waiting on srv.Shutdown so long-lived
	// subscribers exit promptly within shutdownTimeout.
	broker.Shutdown()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("graceful shutdown failed", "error", err)
	}

	wg.Wait()
	log.Info("sorovista stopped")
	return nil
}

// buildSource wires the EventSource for the configured mode. It returns the
// source, a cleanup function, and — in standalone mode — the ingester's run
// function, which is nil in upstream mode.
//
// contributors: adding a new SOURCE_MODE means adding a case here and an
// implementation under internal/source. Nothing else in the program needs to
// know which backend is in use.
func buildSource(ctx context.Context, cfg config.Config, log *slog.Logger, m *metrics.Metrics, tracer *tracing.Tracer, broker *api.Broker) (
	src source.EventSource,
	cleanup func(),
	startIngest func(context.Context) error,
	startSweep func(context.Context) error,
	err error,
) {
	switch cfg.SourceMode {
	case config.ModeSoroTrail:
		log.Info("using upstream sorotrail source", "url", cfg.SoroTrailURL)
		// Upstream mode has no database, so there is nothing to prune and no
		// sweeper to construct.
		return sorotrailsource.New(cfg.SoroTrailURL, nil), func() {}, nil, nil, nil

	case config.ModeRPC:
		log.Info("using standalone rpc source", "rpc_url", cfg.RPCURL)

		if err := store.Migrate(cfg.DatabaseURL); err != nil {
			return nil, nil, nil, nil, err
		}
		log.Info("migrations applied")

		pg, err := store.NewPostgres(ctx, cfg.DatabaseURL, &cfg)
		if err != nil {
			return nil, nil, nil, nil, err
		}

		client := rpc.NewHTTPClient(cfg.RPCURL, tracer.HTTPClient(nil))

		// Verify the endpoint really is the configured network before
		// ingesting anything: a mainnet endpoint behind testnet config makes
		// the explorer quietly show the wrong chain's activity.
		if err := config.StartupNetworkCheck(ctx, client, cfg.Network.Passphrase); err != nil {
			if errors.Is(err, config.ErrNetworkUnverifiable) {
				log.Warn("could not verify network passphrase", "error", err)
			} else {
				return nil, nil, nil, nil, err
			}
		}
		log.Info("network verified", "network", cfg.Network.Name, "rpc_url", cfg.RPCURL)

		client.OnXDRFallback(func() {
			log.Warn("stellar rpc rejected xdrFormat=json; decoding base64 XDR locally instead")
		})

		ingester := ingest.New(client, pg, decode.NewXDRDecoder(), ingest.Options{
			WatchedContracts:    cfg.WatchedContracts,
			PollInterval:        cfg.PollInterval,
			StartLedger:         cfg.StartLedger,
			RetentionLedgers:    cfg.RetentionLedgers,
			PerContractResumers: ingest.ContractIngestSaver(pg),
		}, log)

		ingester.WithMetrics(m).WithTracer(tracer).WithPublisher(broker)

		// Seed per-contract cursors from the global position so that upgrades
		// do not re-read the retention window.
		if err := ingest.SeedContractCursors(ctx, pg, cfg.WatchedContracts); err != nil {
			log.Warn("failed to seed per-contract cursors from global state", "error", err)
		}

		var startSweep func(context.Context) error
		if cfg.EventRetention > 0 {
			log.Info("event retention enabled", "horizon", cfg.EventRetention)
			startSweep = retention.New(pg, cfg.EventRetention, log, m).Run
		}

		return rpcsource.New(pg, client), pg.Close, ingester.Run, startSweep, nil

	default:
		return nil, nil, nil, nil, fmt.Errorf("unsupported SOURCE_MODE %q", cfg.SourceMode)
	}
}

func runValidate() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// Collect all validation errors (not just the first).
	var errors []string

	// Validate per-mode requirements.
	switch cfg.SourceMode {
	case config.ModeRPC:
		if cfg.DatabaseURL == "" {
			errors = append(errors, "DATABASE_URL is required when SOURCE_MODE=rpc")
		}
		if cfg.RPCURL == "" {
			errors = append(errors, "RPC_URL is required when SOURCE_MODE=rpc")
		}
		// Validate pool settings.
		if err := cfg.ValidatePoolSettings(); err != nil {
			errors = append(errors, err.Error())
		}
	case config.ModeSoroTrail:
		if cfg.SoroTrailURL == "" {
			errors = append(errors, "SOROTRAIL_URL is required when SOURCE_MODE=sorotrail")
		}
		if !strings.HasPrefix(cfg.SoroTrailURL, "http://") && !strings.HasPrefix(cfg.SoroTrailURL, "https://") {
			errors = append(errors, fmt.Sprintf("SOROTRAIL_URL %q must start with http:// or https://", cfg.SoroTrailURL))
		}
	default:
		errors = append(errors, fmt.Sprintf("unknown SOURCE_MODE %q", cfg.SourceMode))
	}

	// Report every validation error, not just the first.
	if len(errors) > 0 {
		return fmt.Errorf("configuration errors:\n  %s", strings.Join(errors, "\n  "))
	}

	// Redact DATABASE_URL password before printing.
	redactedDBURL := cfg.DatabaseURL
	if idx := strings.LastIndex(redactedDBURL, "/"); idx > 0 {
		// Find the password part in postgres://user:pass@host/db
		if strings.HasPrefix(redactedDBURL, "postgres://") || strings.HasPrefix(redactedDBURL, "postgresql://") {
			// Replace password with ****
			// Format: postgres://user:password@host/db
			afterAt := strings.Index(redactedDBURL, "@")
			if afterAt > 0 {
				// Find the password between // and @
				afterSlash := strings.Index(redactedDBURL, "//")
				if afterSlash >= 0 && afterSlash < afterAt {
					passwordSection := redactedDBURL[afterSlash+2 : afterAt]
					colonIdx := strings.Index(passwordSection, ":")
					if colonIdx > 0 && colonIdx < len(passwordSection)-1 {
						// Replace everything after the colon with ****
						redactedDBURL = redactedDBURL[:afterSlash+2] + ":***@" + redactedDBURL[afterAt+1:]
					}
				}
			}
		}
	}

	// Print the resolved configuration.
	fmt.Println("mode:", cfg.SourceMode)
	fmt.Println("network:", cfg.Network.Name)
	fmt.Println("http_addr", cfg.HTTPAddr)
	fmt.Println("log_level:", cfg.LogLevel)
	fmt.Println("database_url", redactedDBURL)
	fmt.Println("source_mode", cfg.SourceMode)
	if cfg.SourceMode == config.ModeRPC {
		fmt.Println("rpc_url", cfg.RPCURL)
		fmt.Println("pool_max_open_conns", cfg.PoolMaxOpenConns)
		fmt.Println("pool_max_idle_conns", cfg.PoolMaxIdleConns)
		fmt.Println("pool_max_conn_lifetime", cfg.PoolMaxConnLifetime)
		fmt.Println("pool_max_conn_idle_time", cfg.PoolMaxConnIdleTime)
	}
	if cfg.SourceMode == config.ModeSoroTrail {
		fmt.Println("sorotrail_url", cfg.SoroTrailURL)
	}

	// Optional connectivity check is not implemented in this release.

	return nil
}

// router mounts the web UI at the root and the JSON API under /api.
func router(src source.EventSource, log *slog.Logger, m *metrics.Metrics, cfg config.Config, tracer *tracing.Tracer, broker ...*api.Broker) http.Handler {
	apiServer := api.New(src, log)
	if len(broker) > 0 && broker[0] != nil {
		apiServer.WithBroker(broker[0])
	}

	webServer, err := web.New(src, log)
	if err != nil {
		// Templates are embedded, so a parse failure is a build-time mistake
		// that must surface loudly rather than degrade at runtime.
		panic(fmt.Sprintf("parsing web templates: %v", err))
	}

	r := chi.NewRouter()
	// Tracing runs after reqid so the inbound span can carry the request ID,
	// which is what lets a trace and a log line be joined.
	r.Use(middleware.Recoverer, reqid.Middleware, tracer.Middleware, requestLogger(log))
	r.Use(api.CORSMiddleware(api.CORSConfig{Origins: cfg.CORSAllowedOrigins}))
	// WithRoutePattern resolves the matched chi pattern (e.g. "/api/events/{id}"),
	// not the raw path, keeping metric label cardinality bounded.
	m.WithRoutePattern(func(req *http.Request) string {
		if rc := chi.RouteContext(req.Context()); rc != nil {
			return rc.RoutePattern()
		}
		return ""
	})
	r.Use(m.Middleware)

	// Mounted after request-ID and metrics so a rejected request still has a
	// correlation ID and is counted. Disabled by default (both settings zero).
	if rl := api.NewRateLimiter(api.RateLimitConfig{
		RPS:          cfg.RateLimitRPS,
		Burst:        cfg.RateLimitBurst,
		TrustedProxy: cfg.RateLimitTrustedProxy,
		Metrics:      m,
	}); rl != nil {
		r.Use(rl.Middleware)
		log.Info("api rate limiting enabled",
			"rps", cfg.RateLimitRPS,
			"burst", cfg.RateLimitBurst,
			"trusted_proxy", cfg.RateLimitTrustedProxy)
	}

	// /metrics is unauthenticated like the rest of SoroVista's read surface:
	// it exposes route timings and ingest counters, no event data.
	r.Handle("/metrics", m.Handler())
	r.Get("/health", apiServer.HealthHandler())
	r.Get("/livez", apiServer.LivezHandler())
	r.Get("/readyz", apiServer.ReadyzHandler())
	r.Mount("/api", apiServer.Routes())
	r.Mount("/", webServer.Routes())

	return r
}

func requestLogger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			start := time.Now()
			next.ServeHTTP(ww, r)
			log.Info("http request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.Status(),
				"duration", time.Since(start),
				"remote", r.RemoteAddr,
				"request_id", reqid.From(r),
			)
		})
	}
}
