package httpx

import (
	"context"
	"net/http"
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
					event = event.Str("query", r.URL.RawQuery)
				}

				event.Msg("request completed")
			}()

			next.ServeHTTP(ww, r)
		})
	}
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

// CORSMiddleware adds CORS headers with proper origin validation
func CORSMiddleware(allowedOrigins []string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			allowed := false
			if len(allowedOrigins) > 0 {
				for _, o := range allowedOrigins {
					if o == origin {
						allowed = true
						break
					}
				}
			} else {
				allowed = true
			}
			if !allowed {
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

// HealthzHandler returns health status
func HealthzHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
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
