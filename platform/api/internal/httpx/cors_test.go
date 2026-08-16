package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func corsHandler(origins []string) http.Handler {
	return CORSMiddleware(origins)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
}

// The defect this guards: an empty origin list used to mean "allow every
// origin", and the middleware pairs the reflected origin with
// Access-Control-Allow-Credentials. Any website could then call the API with a
// signed-in user's credentials. CORS_ORIGINS was absent from .env.example, so
// empty was the default a deployer got.
func TestCORSSendsNoHeadersWhenUnconfigured(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/devices", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	rec := httptest.NewRecorder()

	corsHandler(nil).ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("expected no Access-Control-Allow-Origin, got %q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("expected no Access-Control-Allow-Credentials, got %q", got)
	}
	// The request itself still reaches the handler; only the headers are absent.
	if rec.Code != http.StatusOK {
		t.Errorf("expected the request to be served, got %d", rec.Code)
	}
}

func TestCORSAllowsAConfiguredOrigin(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/devices", nil)
	req.Header.Set("Origin", "https://portal.example.com")
	rec := httptest.NewRecorder()

	corsHandler([]string{"https://portal.example.com"}).ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://portal.example.com" {
		t.Errorf("expected the configured origin to be echoed, got %q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Errorf("expected credentials to be allowed, got %q", got)
	}
	if got := rec.Header().Get("Vary"); got != "Origin" {
		t.Errorf("expected Vary: Origin so caches do not serve one origin's response to another, got %q", got)
	}
}

func TestCORSRejectsAnUnconfiguredOrigin(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/devices", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	rec := httptest.NewRecorder()

	corsHandler([]string{"https://portal.example.com"}).ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("expected no Access-Control-Allow-Origin for an unlisted origin, got %q", got)
	}
}

// A near-miss must not match: a prefix or suffix relationship to a configured
// origin is not the same origin.
func TestCORSRequiresAnExactOriginMatch(t *testing.T) {
	for _, origin := range []string{
		"https://portal.example.com.evil.test",
		"https://evil.test/https://portal.example.com",
		"http://portal.example.com",
		"https://portal.example.com/",
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/devices", nil)
		req.Header.Set("Origin", origin)
		rec := httptest.NewRecorder()

		corsHandler([]string{"https://portal.example.com"}).ServeHTTP(rec, req)

		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("origin %q must not be allowed, got %q", origin, got)
		}
	}
}

func TestCORSPreflightFromAnUnlistedOriginIsRefused(t *testing.T) {
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/devices", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	rec := httptest.NewRecorder()

	corsHandler([]string{"https://portal.example.com"}).ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 for a preflight from an unlisted origin, got %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("expected no Access-Control-Allow-Origin, got %q", got)
	}
}

// A request with no Origin header is not a cross-origin request and must be
// served normally, with no CORS headers.
func TestCORSIgnoresRequestsWithoutAnOrigin(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/devices", nil)
	rec := httptest.NewRecorder()

	corsHandler([]string{"https://portal.example.com"}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected a same-origin request to be served, got %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("expected no CORS headers without an Origin, got %q", got)
	}
}
