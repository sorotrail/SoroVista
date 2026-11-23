package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sorotrail/sorovista/internal/metrics"
)

const clientAddr = "203.0.113.7:4155"

// limitedServer mounts the limiter over enough routes to exercise both the
// limited /api tree and the exempt probes, metrics and web UI.
func limitedServer(rl *RateLimiter) http.Handler {
	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }
	mux := http.NewServeMux()
	for _, path := range []string{"/api/events", "/health", "/livez", "/readyz", "/metrics", "/", "/contracts"} {
		mux.HandleFunc(path, ok)
	}
	return rl.Middleware(mux)
}

func get(t *testing.T, h http.Handler, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = clientAddr
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRateLimiterAllowsThenRejectsWithEnvelopeAndRetryAfter(t *testing.T) {
	m := metrics.New()
	now := time.Unix(1_700_000_000, 0)
	rl := NewRateLimiter(RateLimitConfig{RPS: 1, Burst: 1, Metrics: m})
	rl.now = func() time.Time { return now }
	h := limitedServer(rl)

	first := get(t, h, "/api/events", nil)
	require.Equal(t, http.StatusOK, first.Code)
	assert.Empty(t, first.Header().Get("Retry-After"))

	second := get(t, h, "/api/events", nil)
	require.Equal(t, http.StatusTooManyRequests, second.Code)
	assert.Equal(t, "1", second.Header().Get("Retry-After"))

	// The rejection uses the same error envelope as every other endpoint.
	var body errorResponse
	require.NoError(t, json.Unmarshal(second.Body.Bytes(), &body))
	assert.Contains(t, body.Error, "rate limit exceeded")
	assert.Equal(t, http.StatusText(http.StatusTooManyRequests), body.Code)

	// The rejection is visible to an operator.
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	assert.Contains(t, rec.Body.String(), "sorovista_http_rate_limited_total 1")
}

func TestRateLimiterRetryAfterReflectsRealState(t *testing.T) {
	rl := NewRateLimiter(RateLimitConfig{RPS: 0.5, Burst: 1})
	h := limitedServer(rl)

	require.Equal(t, http.StatusOK, get(t, h, "/api/events", nil).Code)
	// One token every two seconds, so the honest wait is 2s rather than a
	// constant that would lie about capacity returning.
	rec := get(t, h, "/api/events", nil)
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Equal(t, "2", rec.Header().Get("Retry-After"))
}

func TestRateLimiterReleasesCapacityOverTime(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	rl := NewRateLimiter(RateLimitConfig{RPS: 1, Burst: 1})
	rl.now = func() time.Time { return now }
	h := limitedServer(rl)

	require.Equal(t, http.StatusOK, get(t, h, "/api/events", nil).Code)
	require.Equal(t, http.StatusTooManyRequests, get(t, h, "/api/events", nil).Code)

	now = now.Add(1100 * time.Millisecond)
	assert.Equal(t, http.StatusOK, get(t, h, "/api/events", nil).Code,
		"a token must refill once the rate interval has elapsed")
}

func TestRateLimiterExemptsProbesMetricsAndWebUI(t *testing.T) {
	// A burst of one means any limited path would reject on the second hit.
	rl := NewRateLimiter(RateLimitConfig{RPS: 1, Burst: 1})
	h := limitedServer(rl)

	for _, path := range []string{"/health", "/livez", "/readyz", "/metrics", "/", "/contracts"} {
		for i := 0; i < 3; i++ {
			require.Equal(t, http.StatusOK, get(t, h, path, nil).Code,
				"%s must not be rate limited", path)
		}
	}
}

func TestRateLimiterKeysOnRemoteAddressByDefault(t *testing.T) {
	rl := NewRateLimiter(RateLimitConfig{RPS: 1, Burst: 1})
	h := limitedServer(rl)

	// A forwarded header must not let an untrusted client choose its bucket.
	require.Equal(t, http.StatusOK, get(t, h, "/api/events", map[string]string{"X-Forwarded-For": "198.51.100.1"}).Code)
	assert.Equal(t, http.StatusTooManyRequests, get(t, h, "/api/events", map[string]string{"X-Forwarded-For": "198.51.100.2"}).Code)
}

func TestRateLimiterTrustsForwardedHeaderOnlyWhenConfigured(t *testing.T) {
	rl := NewRateLimiter(RateLimitConfig{RPS: 1, Burst: 1, TrustedProxy: true})
	h := limitedServer(rl)

	assert.Equal(t, http.StatusOK, get(t, h, "/api/events", map[string]string{"X-Forwarded-For": "198.51.100.1"}).Code)
	assert.Equal(t, http.StatusOK, get(t, h, "/api/events", map[string]string{"X-Forwarded-For": "198.51.100.2, 10.0.0.1"}).Code)
	assert.Equal(t, http.StatusTooManyRequests, get(t, h, "/api/events", map[string]string{"X-Forwarded-For": "198.51.100.1"}).Code)
}

func TestRateLimiterDisabledIsNilAndPassesThrough(t *testing.T) {
	assert.Nil(t, NewRateLimiter(RateLimitConfig{}))
	assert.Nil(t, NewRateLimiter(RateLimitConfig{RPS: 5}))

	var called bool
	h := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
	// A nil limiter must be usable as middleware without a nil check.
	var rl *RateLimiter
	rl.Middleware(h).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/events", nil))
	assert.True(t, called)
}

func TestRateLimiterEvictsIdleKeys(t *testing.T) {
	rl := NewRateLimiter(RateLimitConfig{RPS: 1, Burst: 1})
	start := time.Unix(1_700_000_000, 0)
	rl.now = func() time.Time { return start.Add(time.Hour) }

	// Populate a key, then jump past the idle TTL and make a request so the
	// sweep runs. The stale entry must be gone rather than accumulating.
	rl.buckets.allow("192.0.2.1", start)
	rl.buckets.allow("192.0.2.2", start.Add(time.Hour))

	rl.buckets.mu.Lock()
	_, stale := rl.buckets.entries["192.0.2.1"]
	size := len(rl.buckets.entries)
	rl.buckets.mu.Unlock()

	assert.False(t, stale, "idle keys must be evicted")
	assert.Equal(t, 1, size)
}
