package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// gatherNames returns the metric family names currently exposed by a Metrics
// registry, failing the test if gathering reports an error.
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

func TestNew(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{
			name: "registers the full metric set",
			run: func(t *testing.T) {
				m := New()

				// Label-vector metrics only appear in a gather once at least
				// one label combination has been observed, so exercise every
				// recording method before asserting the names.
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
			},
		},
		{
			// Calling New twice must never panic: each instance owns its own
			// registry, which is the error path a shared registry would hit
			// when MustRegister sees an already-registered collector.
			name: "instances use independent registries",
			run: func(t *testing.T) {
				first := New()
				second := New()

				first.RecordEvents(5)
				second.RecordEvents(7)

				firstNames := gatherNames(t, first)
				secondNames := gatherNames(t, second)
				if !firstNames["sorovista_events_ingested_total"] || !secondNames["sorovista_events_ingested_total"] {
					t.Fatal("each instance should register its own event counter")
				}
				if first.registry == second.registry {
					t.Fatal("New returned instances sharing one registry")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}

func TestNilMetricsIsSafe(t *testing.T) {
	var m *Metrics
	m.RecordPoll(true, time.Second)
	m.SetLag(1)
	m.RecordEvents(1)
	m.TickPollAge(1)
}

func TestHandlerServesMetrics(t *testing.T) {
	m := New()
	m.RecordEvents(1)

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /metrics status = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "sorovista_events_ingested_total") {
		t.Fatalf("metrics body does not include the events counter:\n%s", body)
	}
}
