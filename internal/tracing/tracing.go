// Package tracing holds SoroVista's opt-in OpenTelemetry instrumentation.
//
// Tracing is off by default: an operator who sets nothing gets no exporter, no
// background goroutine and no added latency. When enabled, spans cover inbound
// HTTP requests, outbound Stellar RPC and SoroTrail calls, store queries and
// the ingest poll cycle, and W3C trace context propagates through both
// directions so a trace continues across SoroVista rather than starting here.
//
// Every method is safe on a nil *Tracer, following the same convention as
// internal/metrics: instrumentation is never load-bearing, so call sites never
// need a conditional.
package tracing

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/sorotrail/sorovista/internal/buildinfo"
	"github.com/sorotrail/sorovista/internal/reqid"
)

// Config is the subset of configuration tracing needs. It is built from the
// OTEL_* environment variables by internal/config, so this package stays
// independent of how configuration is loaded.
type Config struct {
	Enabled     bool
	Endpoint    string
	SampleRate  float64
	ServiceName string
}

// Tracer is a thin, nil-safe wrapper around an OpenTelemetry tracer. A zero
// value (or nil) records nothing, which is what disabled tracing means.
type Tracer struct {
	provider *sdktrace.TracerProvider
	tracer   trace.Tracer
	enabled  bool
}

// Setup builds a Tracer. With Enabled false it returns a disabled Tracer and
// never constructs an exporter, so no background goroutine exists. When an
// unreachable collector is configured, exporter failures are logged once and
// otherwise ignored: tracing must never fail a request or log per span.
//
// The caller owns Shutdown and should bound it with its own timeout, so a slow
// collector cannot delay process exit.
func Setup(ctx context.Context, cfg Config, log *slog.Logger) (*Tracer, error) {
	if !cfg.Enabled {
		return &Tracer{}, nil
	}
	exporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpointURL(normalizeEndpoint(cfg.Endpoint)),
	)
	if err != nil {
		return nil, fmt.Errorf("creating otlp trace exporter: %w", err)
	}
	return setup(ctx, cfg, log, exporter), nil
}

// setup builds the provider around an exporter. Tests call it directly with an
// in-memory exporter, so CI needs no collector.
func setup(_ context.Context, cfg Config, log *slog.Logger, exporter sdktrace.SpanExporter) *Tracer {
	if cfg.ServiceName == "" {
		cfg.ServiceName = "sorovista"
	}

	// One error handler for the process. An unreachable collector produces
	// repeated export failures; logging only the first keeps a broken
	// collector from turning into a log flood.
	var once sync.Once
	if log != nil {
		otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
			once.Do(func() {
				log.Warn("otel exporter error; further errors suppressed", "error", err)
			})
		}))
	}

	// ParentBased honours an inbound sampling decision, so a caller that
	// already sampled a trace gets the whole path rather than a fragment.
	sampler := sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRate))
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithSampler(sampler),
		sdktrace.WithResource(resource.NewSchemaless(
			attribute.String("service.name", cfg.ServiceName),
			attribute.String("service.version", buildinfo.Version),
		)),
	)

	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return &Tracer{
		provider: provider,
		tracer:   provider.Tracer("github.com/sorotrail/sorovista"),
		enabled:  true,
	}
}

// Enabled reports whether this Tracer records spans.
func (t *Tracer) Enabled() bool { return t != nil && t.enabled }

// Start begins a span. On a nil or disabled Tracer it returns the context's
// existing span, so nested instrumentation stays free of conditionals.
func (t *Tracer) Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	if !t.Enabled() || t.tracer == nil {
		return ctx, trace.SpanFromContext(ctx)
	}
	return t.tracer.Start(ctx, name, opts...)
}

// Shutdown flushes pending spans. It is safe on a nil Tracer, and the caller's
// context bounds how long a slow or dead collector may hold up exit.
func (t *Tracer) Shutdown(ctx context.Context) error {
	if t == nil || t.provider == nil {
		return nil
	}
	return t.provider.Shutdown(ctx)
}

// Middleware instruments inbound HTTP requests. It relies on otelhttp for
// extraction, so a caller's traceparent header continues their trace. The span
// name is fixed rather than derived from the URL: contract and event IDs in a
// path would make span-name cardinality unbounded, the same reason the metrics
// middleware labels by route pattern.
//
// It must run after reqid.Middleware so the request ID is available to attach
// to the span; that attribute is how a trace and a log line are joined.
func (t *Tracer) Middleware(next http.Handler) http.Handler {
	if !t.Enabled() {
		return next
	}

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id := reqid.From(r); id != "" {
			trace.SpanFromContext(r.Context()).SetAttributes(
				attribute.String("request_id", id),
			)
		}
		next.ServeHTTP(w, r)
	})

	return otelhttp.NewHandler(inner, "sorovista.http")
}

// HTTPClient returns a clone of base (or a default client) whose transport
// injects trace context into outbound requests and records a span per call.
// Passing nil, or calling on a disabled Tracer, returns base unchanged.
func (t *Tracer) HTTPClient(base *http.Client) *http.Client {
	if !t.Enabled() {
		return base
	}
	if base == nil {
		base = &http.Client{Timeout: 30 * time.Second}
	}
	clone := *base
	clone.Transport = otelhttp.NewTransport(base.Transport)
	return &clone
}

// normalizeEndpoint accepts both the standard OTEL_EXPORTER_OTLP_ENDPOINT form
// (host:port) and a full URL, defaulting to plain HTTP for a local collector.
func normalizeEndpoint(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return "http://localhost:4318"
	}
	if !strings.Contains(endpoint, "://") {
		return "http://" + endpoint
	}
	return endpoint
}
