package api

import (
	"fmt"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/sorotrail/sorovista/internal/metrics"
)

// Limiter memory bounds. The map is an attack surface of its own: without a
// cap, one request per spoofed address would grow it until the process dies.
const (
	rateLimiterIdleTTL       = 10 * time.Minute
	rateLimiterSweepInterval = time.Minute
	rateLimiterMaxKeys       = 10_000
)

// RateLimitConfig configures the per-client limiter. Zero RPS or Burst
// disables it.
type RateLimitConfig struct {
	RPS          float64
	Burst        int
	TrustedProxy bool
	Metrics      *metrics.Metrics
}

// RateLimiter is chi-compatible middleware that rejects clients exceeding a
// token-bucket allowance with 429 and a Retry-After header.
type RateLimiter struct {
	buckets      *keyedLimiter
	trustedProxy bool
	metrics      *metrics.Metrics
	// now is a seam for tests; production always uses time.Now.
	now func() time.Time
}

// NewRateLimiter builds a limiter, or returns nil when rate limiting is
// disabled, so callers can mount it unconditionally.
func NewRateLimiter(cfg RateLimitConfig) *RateLimiter {
	if cfg.RPS <= 0 || cfg.Burst < 1 {
		return nil
	}
	return &RateLimiter{
		buckets:      newKeyedLimiter(cfg.RPS, cfg.Burst),
		trustedProxy: cfg.TrustedProxy,
		metrics:      cfg.Metrics,
		now:          time.Now,
	}
}

// Middleware applies the limit to /api only. Probes and the metrics endpoint
// are exempt because limiting them would cause the restart loops and blind
// monitoring they exist to prevent. The web UI is exempt too: its htmx
// partials are ordinary page loads on the root routes, and a limit tuned for
// API clients must not lock a browsing session out.
func (l *RateLimiter) Middleware(next http.Handler) http.Handler {
	if l == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if exemptFromRateLimit(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		ok, retryAfter := l.buckets.allow(l.clientKey(r), l.now())
		if ok {
			next.ServeHTTP(w, r)
			return
		}

		if l.metrics != nil {
			l.metrics.RecordRateLimitRejection()
		}

		// Retry-After is whole seconds so a client can honour it directly.
		// Round up: understating the wait just earns another 429.
		seconds := int(math.Ceil(retryAfter.Seconds()))
		if seconds < 1 {
			seconds = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		writeError(w, r, http.StatusTooManyRequests,
			fmt.Errorf("rate limit exceeded; retry after %d seconds", seconds))
	})
}

func exemptFromRateLimit(path string) bool {
	switch path {
	case "/health", "/livez", "/readyz", "/metrics":
		return true
	}
	return path != "/api" && !strings.HasPrefix(path, "/api/")
}

// clientKey is the remote address by default. Behind a proxy that overwrites
// X-Forwarded-For, the first entry is the real client; trusting the header is
// opt-in because an untrusted client could otherwise pick its own bucket.
func (l *RateLimiter) clientKey(r *http.Request) string {
	if l.trustedProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			first, _, _ := strings.Cut(xff, ",")
			if first = strings.TrimSpace(first); first != "" {
				return first
			}
		}
	}
	return remoteHost(r.RemoteAddr)
}

func remoteHost(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

// keyedLimiter holds one token bucket per client key, sweeping idle entries so
// the map stays bounded by recent activity rather than by all history.
type keyedLimiter struct {
	mu         sync.Mutex
	entries    map[string]*limiterEntry
	rps        rate.Limit
	burst      int
	lastSweep  time.Time
	idleTTL    time.Duration
	sweepEvery time.Duration
	maxKeys    int
}

type limiterEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

func newKeyedLimiter(rps float64, burst int) *keyedLimiter {
	return &keyedLimiter{
		entries:    make(map[string]*limiterEntry),
		rps:        rate.Limit(rps),
		burst:      burst,
		idleTTL:    rateLimiterIdleTTL,
		sweepEvery: rateLimiterSweepInterval,
		maxKeys:    rateLimiterMaxKeys,
	}
}

// allow reports whether the key may proceed and, when it may not, how long
// until a token is available. The reservation is cancelled on rejection so
// refused requests do not consume future capacity.
func (k *keyedLimiter) allow(key string, now time.Time) (bool, time.Duration) {
	k.mu.Lock()
	if k.lastSweep.IsZero() || now.Sub(k.lastSweep) >= k.sweepEvery || len(k.entries) >= k.maxKeys {
		k.evictIdleLocked(now)
	}
	entry, ok := k.entries[key]
	if !ok {
		if len(k.entries) >= k.maxKeys {
			k.evictOldestLocked()
		}
		entry = &limiterEntry{limiter: rate.NewLimiter(k.rps, k.burst)}
		k.entries[key] = entry
	}
	entry.lastSeen = now
	lim := entry.limiter
	k.mu.Unlock()

	reservation := lim.ReserveN(now, 1)
	if !reservation.OK() {
		// Only reachable if the burst were smaller than one request.
		return false, time.Second
	}
	if delay := reservation.DelayFrom(now); delay > 0 {
		reservation.CancelAt(now)
		return false, delay
	}
	return true, 0
}

func (k *keyedLimiter) evictIdleLocked(now time.Time) {
	for key, entry := range k.entries {
		if now.Sub(entry.lastSeen) > k.idleTTL {
			delete(k.entries, key)
		}
	}
	k.lastSweep = now
}

func (k *keyedLimiter) evictOldestLocked() {
	var (
		oldestKey  string
		oldestSeen time.Time
	)
	for key, entry := range k.entries {
		if oldestKey == "" || entry.lastSeen.Before(oldestSeen) {
			oldestKey, oldestSeen = key, entry.lastSeen
		}
	}
	if oldestKey != "" {
		delete(k.entries, oldestKey)
	}
}
