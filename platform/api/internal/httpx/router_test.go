package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"

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
