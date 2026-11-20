package tracing

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/sorotrail/sorovista/internal/reqid"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// newTestTracer builds an enabled Tracer backed by an in-memory exporter, so
// CI needs no collector.
func newTestTracer(t *testing.T) (*Tracer, *tracetest.InMemoryExporter) {
	t.Helper()
	mem := tracetest.NewInMemoryExporter()
	tr := setup(context.Background(), Config{
		Enabled:     true,
		SampleRate:  1,
		ServiceName: "sorovista-test",
	}, testLogger(), mem)
	t.Cleanup(func() {
		_ = tr.Shutdown(context.Background())
	})
	return tr, mem
}

func TestDisabledTracingRecordsNothing(t *testing.T) {
	tr, err := Setup(context.Background(), Config{Enabled: false}, testLogger())
	require.NoError(t, err)
	require.NotNil(t, tr)

	assert.False(t, tr.Enabled(), "disabled tracing must report itself disabled")
	assert.Nil(t, tr.provider, "disabled tracing must not construct an exporter/provider")

	_, span := tr.Start(context.Background(), "should.not.exist")
	assert.False(t, span.IsRecording(), "no span may be recorded when tracing is off")
	span.End()

	// The nil receiver must be equally safe: instrumentation is never
	// load-bearing, so call sites hold the pointer without conditionals.
	var nilTracer *Tracer
	assert.NotPanics(t, func() {
		_, span := nilTracer.Start(context.Background(), "also.not")
		span.End()
		assert.NoError(t, nilTracer.Shutdown(context.Background()))
	})

	// A disabled tracer must not wrap handlers or clients either.
	called := false
	h := tr.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	assert.True(t, called)
	assert.Nil(t, nilTracer.HTTPClient(nil))
}

func TestEnabledTracingProducesSpans(t *testing.T) {
	tr, mem := newTestTracer(t)

	ctx, span := tr.Start(context.Background(), "unit.span")
	require.True(t, span.IsRecording(), "an enabled tracer must record a real span")
	span.End()

	require.NoError(t, tr.provider.ForceFlush(ctx))
	spans := mem.GetSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, "unit.span", spans[0].Name)
}

func TestMiddlewareContinuesInboundTrace(t *testing.T) {
	tr, mem := newTestTracer(t)

	const (
		traceID = "4bf92f3577b34da6a3ce929d0e0e4736"
		spanID  = "00f067aa0ba902b7"
	)

	var (
		gotTraceID trace.TraceID
		gotReqID   string
	)
	handler := reqid.Middleware(tr.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sc := trace.SpanContextFromContext(r.Context())
		gotTraceID = sc.TraceID()
		gotReqID = reqid.From(r)
		w.WriteHeader(http.StatusOK)
	})))

	req := httptest.NewRequest(http.MethodGet, "/contracts", nil)
	req.Header.Set("traceparent", "00-"+traceID+"-"+spanID+"-01")
	req.Header.Set("X-Request-ID", "req-123")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	assert.Equal(t, traceID, gotTraceID.String(),
		"an inbound traceparent must continue the caller's trace, not start a new one")
	assert.Equal(t, "req-123", gotReqID)

	require.NoError(t, tr.provider.ForceFlush(context.Background()))
	spans := mem.GetSpans()
	require.Len(t, spans, 1)

	var sawRequestID bool
	for _, attr := range spans[0].Attributes {
		if attr.Key == "request_id" && attr.Value.AsString() == "req-123" {
			sawRequestID = true
		}
	}
	assert.True(t, sawRequestID, "the request span must carry the request ID so a trace and log line can be joined")
}

func TestHTTPClientPropagatesTrace(t *testing.T) {
	tr, _ := newTestTracer(t)

	var gotTraceparent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTraceparent = r.Header.Get("traceparent")
	}))
	defer srv.Close()

	client := tr.HTTPClient(srv.Client())
	ctx, span := tr.Start(context.Background(), "outbound")
	defer span.End()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	require.NoError(t, err)
	res, err := client.Do(req)
	require.NoError(t, err)
	defer res.Body.Close()

	assert.NotEmpty(t, gotTraceparent, "outbound requests must carry trace context onward")
	assert.Contains(t, gotTraceparent, span.SpanContext().TraceID().String())
}

var _ sdktrace.SpanExporter = (*tracetest.InMemoryExporter)(nil)
