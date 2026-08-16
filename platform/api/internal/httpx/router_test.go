package httpx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/OpenDeskViewer/platform/api/internal/identity"
	"github.com/rs/zerolog"
)

// newSplitRouter mirrors the wiring in cmd/api: a public mux plus a protected
// group that adds the JWT middleware.
func newSplitRouter(t *testing.T) *Mux {
	t.Helper()
	zerolog.SetGlobalLevel(zerolog.Disabled)

	public := NewRouter(
		RequestIDMiddleware(),
		LoggerMiddleware(),
		RecoveryMiddleware(),
		CORSMiddleware(nil),
		ContextMiddleware(),
	)
	protected := public.Group(JWTMiddleware(
		stubValidator{accept: "hdr.body.sig", claims: &identity.JWTClaims{Subject: "sub-7"}},
		&stubDirectory{user: activeUser()},
	))

	public.HandleFunc("/healthz", HealthzHandler())
	public.HandleFunc("/api/login", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	public.HandleFunc("/api/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	protected.HandleFunc("/api/peers", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	return public
}

func TestPublicRoutesDoNotRequireAToken(t *testing.T) {
	router := newSplitRouter(t)

	// /healthz gates the compose healthcheck, and login and heartbeat are
	// reached before any token exists.
	for _, path := range []string{"/healthz", "/api/login", "/api/heartbeat"} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))

		if w.Code != http.StatusOK {
			t.Errorf("%s without an Authorization header: expected 200, got %d", path, w.Code)
		}
	}
}

func TestProtectedRoutesRequireAToken(t *testing.T) {
	router := newSplitRouter(t)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/peers", nil))

	if w.Code != http.StatusUnauthorized {
		t.Errorf("/api/peers without an Authorization header: expected 401, got %d", w.Code)
	}
}

func TestProtectedRoutesRejectAForgedToken(t *testing.T) {
	router := newSplitRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/api/peers", nil)
	req.Header.Set("Authorization", "Bearer bad.body.sig")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("/api/peers with a forged token: expected 401, got %d", w.Code)
	}
}

func TestProtectedRoutesAcceptAValidToken(t *testing.T) {
	router := newSplitRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/api/peers", nil)
	req.Header.Set("Authorization", "Bearer hdr.body.sig")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("/api/peers with a valid token: expected 200, got %d", w.Code)
	}
}

// The first middleware in the list must wrap the rest, so that request IDs and
// panic recovery are in place before anything else runs.
func TestMiddlewareAppliesOutermostFirst(t *testing.T) {
	var order []string
	record := func(name string) Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name)
				next.ServeHTTP(w, r)
			})
		}
	}

	router := NewRouter(record("first"), record("second"))
	router.Group(record("group")).HandleFunc("/x", func(w http.ResponseWriter, r *http.Request) {
		order = append(order, "handler")
	})

	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))

	want := []string{"first", "second", "group", "handler"}
	if len(order) != len(want) {
		t.Fatalf("expected %v, got %v", want, order)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("expected %v, got %v", want, order)
		}
	}
}

// Registering on a group must not authenticate the base mux's routes, and the
// group must share the base mux so both are served by one handler.
func TestGroupSharesTheUnderlyingMux(t *testing.T) {
	base := NewRouter()
	group := base.Group()

	group.HandleFunc("/grouped", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})

	w := httptest.NewRecorder()
	base.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/grouped", nil))

	if w.Code != http.StatusTeapot {
		t.Errorf("expected the base mux to serve a grouped route, got %d", w.Code)
	}
}

// The request log used to carry the query string verbatim, so a ?token=… on a
// signed URL outlived its session in whatever ships the logs.
func TestRedactQueryHidesSensitiveValues(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"token", "token=abc123", "token=REDACTED"},
		{"access token", "access_token=abc123", "access_token=REDACTED"},
		{"api key", "api_key=abc123", "api_key=REDACTED"},
		{"client secret", "client_secret=abc123", "client_secret=REDACTED"},
		{"authorization code", "code=abc123", "code=REDACTED"},
		{"mixed case", "Token=abc123", "Token=REDACTED"},
		{"harmless values survive", "page=2&pageSize=50", "page=2&pageSize=50"},
		{"only the secret is hidden", "page=2&token=abc", "page=2&token=REDACTED"},
		{"unparseable is dropped whole", "%zz=1", "[unparseable]"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := redactQuery(tc.in); got != tc.want {
				t.Errorf("redactQuery(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// stubDropCounter stands in for audit.Service.
type stubDropCounter int64

func (c stubDropCounter) Dropped() int64 { return int64(c) }

// The count of dropped audit events has to leave the process. It is the only
// signal that the audit trail, which recording is deliberately allowed to fail
// silently to protect, has holes in it.
func TestHealthzPublishesDroppedAuditEvents(t *testing.T) {
	cases := []struct {
		name     string
		counters []DropCounter
		want     string
	}{
		{"no counter", nil, `{"status":"ok","audit_events_dropped":0}`},
		{"nothing dropped", []DropCounter{stubDropCounter(0)}, `{"status":"ok","audit_events_dropped":0}`},
		{"events dropped", []DropCounter{stubDropCounter(7)}, `{"status":"ok","audit_events_dropped":7}`},
		// A nil interface value is what the worker would pass; it must not panic
		// the health check, which is the one endpoint that has to answer.
		{"nil counter", []DropCounter{nil}, `{"status":"ok","audit_events_dropped":0}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			HealthzHandler(tc.counters...).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))

			// Still 200 with a non-zero count: a hole in the audit trail is a
			// reason to page somebody, not to take the container out of
			// rotation.
			if w.Code != http.StatusOK {
				t.Errorf("status = %d, want 200", w.Code)
			}
			if got := w.Body.String(); got != tc.want {
				t.Errorf("body = %s, want %s", got, tc.want)
			}
		})
	}
}

// A handler that passes r.Context() to the database gets a deadline from the
// middleware rather than waiting on the client to go away.
func TestTimeoutMiddlewareGivesTheRequestContextADeadline(t *testing.T) {
	var deadline time.Time
	var hadDeadline bool

	handler := TimeoutMiddleware(50 * time.Millisecond)(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			deadline, hadDeadline = r.Context().Deadline()
			w.WriteHeader(http.StatusOK)
		}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/peers", nil))

	if !hadDeadline {
		t.Fatal("the request context has no deadline, so a blocked query holds a pool connection until the client disconnects")
	}
	if remaining := time.Until(deadline); remaining <= 0 || remaining > 50*time.Millisecond {
		t.Errorf("deadline is %v away, want at most the configured 50ms", remaining)
	}
}

// And it actually fires: a handler that waits longer than the deadline sees its
// context cancelled, which is what stops the query at the database.
func TestTimeoutMiddlewareCancelsALongRequest(t *testing.T) {
	var cause error

	handler := TimeoutMiddleware(20 * time.Millisecond)(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
				cause = r.Context().Err()
			case <-time.After(2 * time.Second):
			}
			w.WriteHeader(http.StatusOK)
		}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/peers", nil))

	if !errors.Is(cause, context.DeadlineExceeded) {
		t.Errorf("context error = %v, want DeadlineExceeded", cause)
	}
}
