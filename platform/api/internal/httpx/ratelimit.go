package httpx

import (
	"net/http"
	"strconv"
	"sync"
	"time"
)

// The API had no rate limiting of any kind. Every unauthenticated route was
// therefore free to call as fast as a client could open sockets: /api/login
// could be walked through a password list at line rate, and /api/heartbeat
// could be used to insert device rows without bound.
//
// This is a per-key token bucket, keyed by client IP. It is deliberately not a
// distributed limiter: one API process is what this deployment runs, and a
// shared-state limiter would trade a real defence today for an operational
// dependency. If the API is ever scaled out, this becomes per-instance and the
// configured limits should be divided by the instance count.
//
// The honest limit: a NAT puts a whole office behind one address, so a limit
// tight enough to stop credential stuffing from one host also throttles a
// legitimate site sharing that host. The limits below are chosen per route
// group rather than globally for exactly that reason.

// RateLimiter is a token bucket per key.
type RateLimiter struct {
	ratePerSecond float64
	burst         float64

	mu        sync.Mutex
	buckets   map[string]*bucket
	lastSweep time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

const (
	// Buckets idle for longer than this are forgotten. Anything shorter and a
	// client on a slow poll loses its history between requests.
	bucketTTL = 10 * time.Minute

	// A sweep walks the whole map, so it is time-bounded rather than run on
	// every request.
	sweepInterval = 1 * time.Minute

	// A hard ceiling on tracked keys, so a spray of forged X-Real-IP values
	// cannot grow the map without bound. Reaching it forces a sweep.
	maxTrackedKeys = 50_000
)

// NewRateLimiter allows requestsPerMinute sustained, with burst requests
// available at once. Burst is what accommodates a page that fires several API
// calls on load; the sustained rate is what an attacker is left with.
func NewRateLimiter(requestsPerMinute, burst int) *RateLimiter {
	if burst < 1 {
		burst = 1
	}
	return &RateLimiter{
		ratePerSecond: float64(requestsPerMinute) / 60.0,
		burst:         float64(burst),
		buckets:       make(map[string]*bucket),
		lastSweep:     time.Now(),
	}
}

// Allow consumes a token for key. The second return is how long the caller
// should wait, and is only meaningful when the first is false.
func (l *RateLimiter) Allow(key string) (bool, time.Duration) {
	now := time.Now()

	l.mu.Lock()
	defer l.mu.Unlock()

	if now.Sub(l.lastSweep) > sweepInterval || len(l.buckets) > maxTrackedKeys {
		l.sweepLocked(now)
	}

	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	} else {
		b.tokens += now.Sub(b.last).Seconds() * l.ratePerSecond
		if b.tokens > l.burst {
			b.tokens = l.burst
		}
		b.last = now
	}

	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}

	// Round up: a Retry-After of 0 invites an immediate retry that also fails.
	wait := time.Duration((1-b.tokens)/l.ratePerSecond*float64(time.Second)) + time.Second
	return false, wait
}

// sweepLocked drops buckets that are both idle and back at full capacity, so
// forgetting them cannot hand anyone extra allowance.
func (l *RateLimiter) sweepLocked(now time.Time) {
	for key, b := range l.buckets {
		idle := now.Sub(b.last)
		if idle < bucketTTL {
			continue
		}
		if b.tokens+idle.Seconds()*l.ratePerSecond >= l.burst {
			delete(l.buckets, key)
		}
	}
	l.lastSweep = now
}

// RateLimitMiddleware rejects requests over the limit with 429 and a
// Retry-After. It keys on ClientIP, which prefers the proxy's X-Real-IP: Caddy
// sets that itself and does not pass a client-supplied one through, so it
// cannot be spoofed from outside. Running this API without that proxy in front
// would make the header attacker-controlled.
func RateLimitMiddleware(limiter *RateLimiter) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			allowed, wait := limiter.Allow(ClientIP(r))
			if !allowed {
				w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())))
				WriteError(w, http.StatusTooManyRequests, "too many requests")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// MaxBodyMiddleware caps request bodies. Without it, a single POST with a
// gigabyte body is read into memory by any handler that calls json.Decode,
// which is all of them.
//
// MaxBytesReader also sets the response to 413 when the limit is hit, so a
// handler that reports a decode failure as 400 still answers rather than
// hanging.
func MaxBodyMiddleware(maxBytes int64) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			}
			next.ServeHTTP(w, r)
		})
	}
}
