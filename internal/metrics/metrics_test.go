package metrics

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func gatherNames(t *testing.T, m *Metrics) map[string]bool {
	t.Helper()
	families, err := m.registry.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	names := make(map[string]bool, len(families))
	for _, family := range families {
		names[family.GetName()] = true
	}
	return names
}

func TestNewRegistersMetrics(t *testing.T) {
	m := New()

	// Label-vector metrics only appear after at least one label combination
	// has been observed.
	m.RecordPoll(true, 10*time.Millisecond)
	m.RecordPoll(false, 20*time.Millisecond)
	m.SetLag(5)
	m.RecordEvents(3)
	m.TickPollAge(1.5)
	m.httpDuration.WithLabelValues("/api/events", http.MethodGet, "200").Observe(0.01)

	want := []string{
		"sorovista_polls_total",
		"sorovista_poll_duration_seconds",
		"sorovista_ingest_lag_ledgers",
		"sorovista_events_ingested_total",
		"sorovista_seconds_since_last_poll",
		"sorovista_http_request_duration_seconds",
	}
	got := gatherNames(t, m)
	for _, name := range want {
		if !got[name] {
			t.Errorf("metric %q is not registered", name)
		}
	}
}

// scrape renders the registry through Handler, the same path Prometheus takes
// in production, so every assertion below sees exactly what a real scraper
// would receive. prometheus/testutil would be more convenient but pulls a new
// module into go.mod for one test package, which is not worth it.
func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	return rec.Body.String()
}

// setRoutePattern swaps the package-level function variable for the duration of
// one test and returns a restore function. Middleware calls RoutePattern
// unguarded, so a test that drives it has to provide one; restoring it keeps
// that requirement from leaking into tests that do not.
func setRoutePattern(fn func(*http.Request) string) func() {
	prev := RoutePattern
	RoutePattern = fn
	return func() { RoutePattern = prev }
}

// The nil contract lets call sites wire metrics unconditionally. Table-driven
// over the exported methods so a future method without a nil guard is caught.
func TestNilMetricsRecordsNothing(t *testing.T) {
	var m *Metrics

	calls := []struct {
		name string
		call func()
	}{
		{"RecordPoll ok", func() { m.RecordPoll(true, 25*time.Millisecond) }},
		{"RecordPoll error", func() { m.RecordPoll(false, 25*time.Millisecond) }},
		{"SetLag", func() { m.SetLag(42) }},
		{"TickPollAge", func() { m.TickPollAge(12.5) }},
		{"RecordEvents", func() { m.RecordEvents(7) }},
	}
	for _, c := range calls {
		t.Run(c.name, func(t *testing.T) {
			assert.NotPanics(t, c.call, "a nil *Metrics must record nothing rather than panic")
		})
	}

	t.Run("Middleware still calls the next handler", func(t *testing.T) {
		var called bool
		next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			called = true
			w.WriteHeader(http.StatusTeapot)
		})

		rec := httptest.NewRecorder()
		assert.NotPanics(t, func() {
			m.Middleware(next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/events", nil))
		}, "instrumentation must never be load-bearing")
		assert.True(t, called, "the wrapped handler must still run")
		assert.Equal(t, http.StatusTeapot, rec.Code)
	})
}

// New builds its own registry precisely so two instances in one process do not
// collide on duplicate registration.
func TestNewInstancesDoNotShareARegistry(t *testing.T) {
	a := New()
	b := New()
	a.RecordEvents(3)

	assert.Contains(t, scrape(t, a), "sorovista_events_ingested_total 3")
	assert.NotContains(t, scrape(t, b), "sorovista_events_ingested_total 3",
		"each instance must report only its own observations")
}

func TestRecordPollLabelsByOutcome(t *testing.T) {
	m := New()
	m.RecordPoll(true, 250*time.Millisecond)

	body := scrape(t, m)
	assert.Contains(t, body, `sorovista_polls_total{outcome="ok"} 1`)
	assert.NotContains(t, body, `sorovista_polls_total{outcome="error"}`,
		"a successful poll must not create the error series")
	assert.Contains(t, body, "sorovista_poll_duration_seconds_count 1")
	assert.Contains(t, body, "sorovista_poll_duration_seconds_sum 0.25")

	m.RecordPoll(false, 2*time.Second)

	body = scrape(t, m)
	assert.Contains(t, body, `sorovista_polls_total{outcome="ok"} 1`)
	assert.Contains(t, body, `sorovista_polls_total{outcome="error"} 1`,
		"a failed poll lands in its own series, leaving the ok series alone")
	assert.Contains(t, body, "sorovista_poll_duration_seconds_count 2")
}

func TestGaugesAndCountersReportValues(t *testing.T) {
	m := New()
	m.SetLag(123)
	m.TickPollAge(45.5)
	m.RecordEvents(9)

	body := scrape(t, m)
	assert.Contains(t, body, "sorovista_ingest_lag_ledgers 123")
	assert.Contains(t, body, "sorovista_seconds_since_last_poll 45.5")
	assert.Contains(t, body, "sorovista_events_ingested_total 9")
}

func TestMiddlewareRecordsHandlerStatus(t *testing.T) {
	t.Cleanup(setRoutePattern(func(*http.Request) string { return "/api/contracts/{id}" }))

	tests := []struct {
		name       string
		handler    http.HandlerFunc
		wantStatus int
	}{
		{
			name: "explicit status",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotFound)
			},
			wantStatus: http.StatusNotFound,
		},
		{
			name: "implicit 200",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, err := w.Write([]byte("ok"))
				assert.NoError(t, err)
			},
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := New()
			rec := httptest.NewRecorder()

			m.Middleware(tt.handler).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/contracts/CABC", nil))

			assert.Equal(t, tt.wantStatus, rec.Code)
			body := scrape(t, m)
			assert.Contains(t, body, fmt.Sprintf(
				`sorovista_http_request_duration_seconds_count{method="GET",route="/api/contracts/{id}",status="%s"} 1`,
				strconv.Itoa(tt.wantStatus),
			))
		})
	}

	t.Run("falls back to the request path when no pattern is known", func(t *testing.T) {
		t.Cleanup(setRoutePattern(func(*http.Request) string { return "" }))

		m := New()
		m.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).
			ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/unmatched/path", nil))

		assert.Contains(t, scrape(t, m),
			`sorovista_http_request_duration_seconds_count{method="GET",route="/unmatched/path",status="200"} 1`)
	})
}

func TestHandlerServesPrometheusTextFormat(t *testing.T) {
	m := New()
	m.RecordPoll(true, time.Millisecond)
	m.SetLag(5)
	m.TickPollAge(1)
	m.RecordEvents(2)

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain"),
		"Prometheus text exposition should be served as text/plain, got %q", rec.Header().Get("Content-Type"))

	body := rec.Body.String()
	for _, want := range []string{
		"# HELP sorovista_polls_total",
		`sorovista_polls_total{outcome="ok"} 1`,
		"sorovista_poll_duration_seconds_count 1",
		"sorovista_ingest_lag_ledgers 5",
		"sorovista_events_ingested_total 2",
		"sorovista_seconds_since_last_poll 1",
	} {
		assert.Contains(t, body, want)
	}
}
