package rustdeskapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleAuthQueryMethodNotAllowed(t *testing.T) {
	b := &OIDCBroker{}
	req := httptest.NewRequest(http.MethodGet, "/api/oidc/auth-query", nil)
	w := httptest.NewRecorder()

	b.HandleAuthQuery(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405, got %d", w.Code)
	}
}

func TestHandleJWKSMethodNotAllowed(t *testing.T) {
	b := &OIDCBroker{}
	req := httptest.NewRequest(http.MethodPost, "/api/oidc/jwks", nil)
	w := httptest.NewRecorder()

	b.HandleJWKS(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405, got %d", w.Code)
	}
}

func TestHandleLoginMethodNotAllowed(t *testing.T) {
	b := &OIDCBroker{}
	req := httptest.NewRequest(http.MethodGet, "/api/login", nil)
	w := httptest.NewRecorder()

	b.HandleLogin(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405, got %d", w.Code)
	}
}

func TestHandleLogoutMethodNotAllowed(t *testing.T) {
	b := &OIDCBroker{}
	req := httptest.NewRequest(http.MethodGet, "/api/logout", nil)
	w := httptest.NewRecorder()

	b.HandleLogout(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405, got %d", w.Code)
	}
}
