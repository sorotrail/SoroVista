package metrics

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	return rec.Body.String()
}

func TestMiddlewareWithoutRoutePatternFallsBackToRawPath(t *testing.T) {
	// Constructing a Metrics without wiring WithRoutePattern must not panic
	// and must record the request under the raw URL path as a safety net.
	m := New()

	h := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))

	rec := httptest.NewRecorder()
	require.NotPanics(t, func() {
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/events/123", nil))
	})
	assert.Equal(t, http.StatusAccepted, rec.Code)

	body := scrape(t, m)
	assert.Contains(t, body,
		`sorovista_http_request_duration_seconds_count{method="GET",route="/api/events/123",status="202"} 1`)
}

func TestMiddlewareUsesConfiguredRoutePattern(t *testing.T) {
	m := New().WithRoutePattern(func(r *http.Request) string {
		if r.URL.Path == "/unmatched" {
			return ""
		}
		return "/api/events/{id}"
	})

	h := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))

	// Matched route uses the bounded pattern from the resolver.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/events/999", nil))
	assert.Equal(t, http.StatusOK, rec.Code)

	// Empty pattern from resolver falls back to the raw path.
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/unmatched", nil))
	assert.Equal(t, http.StatusOK, rec2.Code)

	body := scrape(t, m)
	assert.Contains(t, body,
		`sorovista_http_request_duration_seconds_count{method="GET",route="/api/events/{id}",status="200"} 1`)
	assert.Contains(t, body,
		`sorovista_http_request_duration_seconds_count{method="GET",route="/unmatched",status="200"} 1`)
}

func TestWithRoutePatternNilSafe(t *testing.T) {
	var m *Metrics
	require.NotPanics(t, func() {
		assert.Nil(t, m.WithRoutePattern(func(*http.Request) string { return "/x" }))
	})

	called := false
	h := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	rec := httptest.NewRecorder()
	require.NotPanics(t, func() {
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	})
	assert.True(t, called)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}
