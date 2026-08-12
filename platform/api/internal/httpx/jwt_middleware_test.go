package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/OpenDeskViewer/platform/api/internal/identity"
	"github.com/rs/zerolog"
)

// stubValidator accepts one token string and rejects everything else.
type stubValidator struct {
	accept string
	claims *identity.JWTClaims
}

func (s stubValidator) ValidateToken(_ context.Context, token string) (*identity.JWTClaims, error) {
	if token != s.accept {
		return nil, errors.New("signature mismatch")
	}
	return s.claims, nil
}

// stubDirectory records the claims it was asked to resolve, and answers session
// lookups from sessions.
type stubDirectory struct {
	user     *identity.User
	err      error
	called   *identity.JWTClaims
	sessions map[string]*identity.User
}

func (s *stubDirectory) ResolveUser(_ context.Context, claims *identity.JWTClaims) (*identity.User, error) {
	s.called = claims
	if s.err != nil {
		return nil, s.err
	}
	return s.user, nil
}

func (s *stubDirectory) GetSessionUser(_ context.Context, token string) (*identity.User, error) {
	if user, ok := s.sessions[token]; ok {
		return user, nil
	}
	return nil, errors.New("no such session")
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
}

func activeUser() *identity.User {
	return &identity.User{ID: 7, Email: "tech@example.com", Active: true, KeycloakSubject: "sub-7"}
}

// newTestMiddleware wires the middleware around a handler that always succeeds.
func newTestMiddleware(t *testing.T, validator TokenValidator, dir UserDirectory) http.Handler {
	t.Helper()
	zerolog.SetGlobalLevel(zerolog.Disabled)
	return JWTMiddleware(validator, dir)(okHandler())
}

func assertErrorResponse(t *testing.T, w *httptest.ResponseRecorder, status int, message string) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("expected status %d, got %d", status, w.Code)
	}
	var resp ErrorResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Error != message {
		t.Errorf("expected error %q, got %q", message, resp.Error)
	}
}

func TestJWTMiddlewareMissingAuthorizationHeader(t *testing.T) {
	handler := newTestMiddleware(t, stubValidator{}, &stubDirectory{user: activeUser()})

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/peers", nil))

	assertErrorResponse(t, w, http.StatusUnauthorized, "missing authorization header")
}

func TestJWTMiddlewareInvalidAuthorizationFormat(t *testing.T) {
	handler := newTestMiddleware(t, stubValidator{}, &stubDirectory{user: activeUser()})

	req := httptest.NewRequest(http.MethodGet, "/api/peers", nil)
	req.Header.Set("Authorization", "InvalidFormat token123")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	assertErrorResponse(t, w, http.StatusUnauthorized, "invalid authorization header format")
}

func TestJWTMiddlewareRejectsInvalidToken(t *testing.T) {
	handler := newTestMiddleware(t,
		stubValidator{accept: "hdr.body.sig", claims: &identity.JWTClaims{Subject: "sub-7"}},
		&stubDirectory{user: activeUser()},
	)

	req := httptest.NewRequest(http.MethodGet, "/api/peers", nil)
	req.Header.Set("Authorization", "Bearer bad.body.sig")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	assertErrorResponse(t, w, http.StatusUnauthorized, "invalid token")
}

func TestJWTMiddlewareAcceptsValidToken(t *testing.T) {
	claims := &identity.JWTClaims{Subject: "sub-7", Email: "tech@example.com"}
	dir := &stubDirectory{user: activeUser()}
	handler := newTestMiddleware(t, stubValidator{accept: "hdr.body.sig", claims: claims}, dir)

	req := httptest.NewRequest(http.MethodGet, "/api/peers", nil)
	req.Header.Set("Authorization", "Bearer hdr.body.sig")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}
	if dir.called == nil || dir.called.Subject != "sub-7" {
		t.Errorf("expected the directory to resolve subject sub-7, got %+v", dir.called)
	}
}

// A Keycloak account with no local row must be provisioned, not rejected.
func TestJWTMiddlewarePassesClaimsForProvisioning(t *testing.T) {
	claims := &identity.JWTClaims{
		Subject:           "sub-new",
		Email:             "new@example.com",
		PreferredUsername: "new",
	}
	dir := &stubDirectory{user: &identity.User{ID: 99, Email: "new@example.com", Active: true}}
	handler := newTestMiddleware(t, stubValidator{accept: "hdr.body.sig", claims: claims}, dir)

	req := httptest.NewRequest(http.MethodGet, "/api/peers", nil)
	req.Header.Set("Authorization", "Bearer hdr.body.sig")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200 for a provisionable user, got %d", w.Code)
	}
	if dir.called == nil || dir.called.Email != "new@example.com" {
		t.Errorf("expected the email claim to reach the directory, got %+v", dir.called)
	}
}

func TestJWTMiddlewareRejectsUnknownUser(t *testing.T) {
	handler := newTestMiddleware(t,
		stubValidator{accept: "hdr.body.sig", claims: &identity.JWTClaims{Subject: "sub-7"}},
		&stubDirectory{err: errors.New("no email claim")},
	)

	req := httptest.NewRequest(http.MethodGet, "/api/peers", nil)
	req.Header.Set("Authorization", "Bearer hdr.body.sig")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	assertErrorResponse(t, w, http.StatusUnauthorized, "invalid token")
}

// The RustDesk client replays the opaque token /api/login gave it, not a JWT.
func TestJWTMiddlewareAcceptsAClientSessionToken(t *testing.T) {
	handler := newTestMiddleware(t,
		stubValidator{accept: "hdr.body.sig"},
		&stubDirectory{sessions: map[string]*identity.User{"deadbeef": activeUser()}},
	)

	req := httptest.NewRequest(http.MethodGet, "/api/peers", nil)
	req.Header.Set("Authorization", "Bearer deadbeef")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected a live session token to be accepted, got %d", w.Code)
	}
}

func TestJWTMiddlewareRejectsAnUnknownSessionToken(t *testing.T) {
	handler := newTestMiddleware(t,
		stubValidator{accept: "hdr.body.sig"},
		&stubDirectory{sessions: map[string]*identity.User{"deadbeef": activeUser()}},
	)

	req := httptest.NewRequest(http.MethodGet, "/api/peers", nil)
	req.Header.Set("Authorization", "Bearer expired-or-forged")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	assertErrorResponse(t, w, http.StatusUnauthorized, "invalid token")
}

func TestJWTMiddlewareRejectsDisabledUserOnASessionToken(t *testing.T) {
	disabled := activeUser()
	disabled.Active = false

	handler := newTestMiddleware(t,
		stubValidator{accept: "hdr.body.sig"},
		&stubDirectory{sessions: map[string]*identity.User{"deadbeef": disabled}},
	)

	req := httptest.NewRequest(http.MethodGet, "/api/peers", nil)
	req.Header.Set("Authorization", "Bearer deadbeef")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	assertErrorResponse(t, w, http.StatusUnauthorized, "account disabled")
}

func TestJWTMiddlewareRejectsDisabledUser(t *testing.T) {
	disabled := activeUser()
	disabled.Active = false

	handler := newTestMiddleware(t,
		stubValidator{accept: "hdr.body.sig", claims: &identity.JWTClaims{Subject: "sub-7"}},
		&stubDirectory{user: disabled},
	)

	req := httptest.NewRequest(http.MethodGet, "/api/peers", nil)
	req.Header.Set("Authorization", "Bearer hdr.body.sig")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	assertErrorResponse(t, w, http.StatusUnauthorized, "account disabled")
}

// A nil validator would authenticate nobody and authorise everybody, so it must
// be impossible to construct rather than a silent per-request skip.
func TestJWTMiddlewareRequiresValidator(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("expected JWTMiddleware to panic on a nil validator")
		}
	}()
	JWTMiddleware(nil, &stubDirectory{})
}

func TestJWTMiddlewareRequiresUserDirectory(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("expected JWTMiddleware to panic on a nil user directory")
		}
	}()
	JWTMiddleware(stubValidator{}, nil)
}

func TestGetUserFromContextSuccess(t *testing.T) {
	user := &identity.User{
		ID:              123,
		Email:           "test@example.com",
		DisplayName:     "Test User",
		Active:          true,
		KeycloakSubject: "subject123",
	}

	ctx := withUser(context.Background(), user)

	retrievedUser, ok := GetUserFromContext(ctx)

	if !ok {
		t.Error("expected ok to be true")
	}

	if retrievedUser == nil {
		t.Fatal("expected user to not be nil")
	}

	if retrievedUser.ID != 123 {
		t.Errorf("expected user ID 123, got %d", retrievedUser.ID)
	}

	if retrievedUser.Email != "test@example.com" {
		t.Errorf("expected email 'test@example.com', got '%s'", retrievedUser.Email)
	}
}

func TestGetUserFromContextNotFound(t *testing.T) {
	user, ok := GetUserFromContext(context.Background())

	if ok {
		t.Error("expected ok to be false")
	}

	if user != nil {
		t.Error("expected user to be nil")
	}
}

func TestGetUserFromContextWithOtherValues(t *testing.T) {
	ctx := context.WithValue(context.Background(), startedAtKey, "test")

	user, ok := GetUserFromContext(ctx)

	if ok {
		t.Error("expected ok to be false")
	}

	if user != nil {
		t.Error("expected user to be nil")
	}
}
