package httpx

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/OpenDeskViewer/platform/api/internal/postgres"
	"github.com/rs/zerolog/log"
)

// Middleware is a function that wraps an http.Handler
type Middleware func(http.Handler) http.Handler

// NewRouter creates a new mux with middleware applied
func NewRouter(mw ...Middleware) *Mux {
	m := &Mux{
		mux:        http.NewServeMux(),
		middleware: mw,
	}
	return m
}

// Mux is a wrapper around http.ServeMux with middleware support
type Mux struct {
	mux        *http.ServeMux
	middleware []Middleware
}

// Group returns a Mux that registers into the same underlying ServeMux but
// applies extra middleware to everything registered through it. This is how the
// protected route group is built: routes are only authenticated if they are
// registered on the group, so forgetting to list a route leaves it protected by
// nothing rather than exposing it by accident.
func (m *Mux) Group(mw ...Middleware) *Mux {
	combined := make([]Middleware, 0, len(m.middleware)+len(mw))
	combined = append(combined, m.middleware...)
	combined = append(combined, mw...)
	return &Mux{mux: m.mux, middleware: combined}
}

// Handle registers a handler with middleware applied. Middleware is applied in
// reverse so the first entry of the list is the outermost wrapper and runs
// first.
func (m *Mux) Handle(pattern string, handler http.Handler) {
	for i := len(m.middleware) - 1; i >= 0; i-- {
		handler = m.middleware[i](handler)
	}
	m.mux.Handle(pattern, handler)
}

// HandleFunc registers a handler function with middleware applied
func (m *Mux) HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request)) {
	m.Handle(pattern, http.HandlerFunc(handler))
}

// ServeHTTP implements http.Handler
func (m *Mux) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mux.ServeHTTP(w, r)
}

// RequestIDMiddleware generates/validates request IDs
func RequestIDMiddleware() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get("X-Request-ID")
			if id == "" {
				id = newRequestID()
			}
			r = r.WithContext(withRequestID(r.Context(), id))
			w.Header().Set("X-Request-ID", id)
			next.ServeHTTP(w, r)
		})
	}
}

// LoggerMiddleware logs requests
func LoggerMiddleware() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := NewWrapResponseWriter(w, r.ProtoMajor)

			defer func() {
				duration := time.Since(start)
				requestID := requestIDFromContext(r.Context())

				event := log.Info().Str("request_id", requestID).
					Str("method", r.Method).
					Str("path", r.URL.Path).
					Dur("duration", duration).
					Int("status", ww.Status()).
					Int64("bytes", int64(ww.BytesWritten()))

				if r.URL.RawQuery != "" {
					event = event.Str("query", redactQuery(r.URL.RawQuery))
				}

				event.Msg("request completed")
			}()

			next.ServeHTTP(ww, r)
		})
	}
}

// sensitiveQueryKeys are the query parameters whose values never belong in a
// log line. Logs are shipped, retained and read by people who are not entitled
// to a bearer token, so a single ?token=… in a request line outlives the
// session it belongs to.
//
// The list is matched case-insensitively and by substring, so api_key, secret,
// client_secret and access_token are all covered without enumerating them.
var sensitiveQueryKeys = []string{"token", "secret", "password", "passwd", "key", "code", "auth", "signature", "sig"}

// redactQuery replaces the values of sensitive parameters with REDACTED and
// keeps the rest, so the log still says which endpoint was called with which
// filters. A query string that does not parse is dropped whole rather than
// logged raw, because an unparseable query is exactly where an unexpected
// secret would hide.
func redactQuery(raw string) string {
	values, err := url.ParseQuery(raw)
	if err != nil {
		return "[unparseable]"
	}

	for key, vals := range values {
		if !isSensitiveQueryKey(key) {
			continue
		}
		for i := range vals {
			vals[i] = "REDACTED"
		}
	}
	return values.Encode()
}

func isSensitiveQueryKey(key string) bool {
	lower := strings.ToLower(key)
	for _, needle := range sensitiveQueryKeys {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

// RecoveryMiddleware recovers from panics
func RecoveryMiddleware() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if err := recover(); err != nil {
					requestID := requestIDFromContext(r.Context())
					log.Error().Str("request_id", requestID).
						Interface("panic", err).
						Str("stack", string(debugStack())).
						Msg("panic recovered")

					http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
				}
			}()

			next.ServeHTTP(w, r)
		})
	}
}

// CORSMiddleware adds CORS headers for explicitly configured origins.
//
// An empty allowedOrigins means no CORS headers at all, which is both the safe
// default and the correct one for this deployment: the portal is served from
// the same origin as the API, so it never needs them. The previous behaviour
// was the opposite, treating "nothing configured" as "allow every origin" while
// also sending Access-Control-Allow-Credentials, which lets any website make
// credentialed cross-origin calls on a signed-in user's behalf. CORS_ORIGINS is
// absent from .env.example, so that was the default a deployer got.
func CORSMiddleware(allowedOrigins []string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")

			allowed := false
			if origin != "" {
				for _, o := range allowedOrigins {
					if o == origin {
						allowed = true
						break
					}
				}
			}

			if !allowed {
				// A preflight for a disallowed origin is answered without CORS
				// headers rather than passed to a handler that would treat it
				// as a real request.
				if r.Method == http.MethodOptions && origin != "" {
					w.Header().Set("Vary", "Origin")
					w.WriteHeader(http.StatusForbidden)
					return
				}
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Request-ID")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Vary", "Origin")

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// TimeoutMiddleware gives every request context a deadline.
//
// Handlers pass r.Context() straight into pgx. Without a deadline on it, that
// context is only cancelled when the client goes away, so a query that blocks
// on a lock holds a pool connection for as long as the database will let it.
// With MaxConns at 20, twenty such requests is the whole pool, and the symptom
// is a healthy-looking API that answers nothing.
//
// The server's ReadTimeout and WriteTimeout do not cover this: they are socket
// deadlines, and neither cancels the request context or the query behind it.
//
// A deadline here reaches all the way down. pgx sends a cancellation request to
// PostgreSQL, the handler's database call returns an error, and the request ends
// as a 500 with the connection returned to the pool. That is a bad answer, but
// it is an answer, and the process survives to give it.
//
// This is deliberately shorter than the server's 15-second WriteTimeout, so the
// handler loses the race and gets to write its own error rather than having the
// socket closed underneath it.
func TimeoutMiddleware(d time.Duration) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

const startedAtKey contextKey = "started_at"

// ContextMiddleware adds request context
func ContextMiddleware() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), startedAtKey, time.Now())
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// DropCounter reports how many events a component has lost since start.
// audit.Service implements it.
type DropCounter interface {
	Dropped() int64
}

// HealthzHandler returns health status, and the counters that would otherwise
// only ever be visible in a log line nobody is reading.
//
// audit_events_dropped is the one that matters. Recording an audit event is
// deliberately allowed to fail without failing the request it describes, which
// is right: a bookkeeping problem should not become an outage. The cost of that
// choice is that the audit trail can develop holes silently, and the audit log
// is what this product is sold on. Publishing the count is what turns "silently"
// into "visibly".
//
// It stays 200 with a non-zero count on purpose. A hole in the audit trail is a
// reason to page somebody, not a reason to take the container out of rotation:
// restarting it loses the count and fixes nothing.
//
// counters may be nil, which reports zero. The worker has no audit service.
func HealthzHandler(counters ...DropCounter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var dropped int64
		for _, c := range counters {
			if c != nil {
				dropped += c.Dropped()
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"status":"ok","audit_events_dropped":%d}`, dropped)
	}
}

// ReadyzHandler returns readiness status with database check
func ReadyzHandler(db *postgres.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		err := db.Ping(ctx)
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"status":"unavailable","error":"database_ping_failed"}`))
			return
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}
}
