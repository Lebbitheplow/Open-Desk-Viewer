package rustdeskapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/OpenDeskViewer/platform/api/internal/auth"
	"github.com/OpenDeskViewer/platform/api/internal/httpx"
	"github.com/OpenDeskViewer/platform/api/internal/identity"
	"github.com/rs/zerolog/log"
)

// OIDCBroker handles OIDC authentication flows with real JWT validation
type OIDCBroker struct {
	validator   *auth.JWTValidator
	authService *identity.AuthService
}

// NewOIDCBroker creates a new OIDC broker with real JWT signature verification
func NewOIDCBroker(validator *auth.JWTValidator, authSvc *identity.AuthService) *OIDCBroker {
	return &OIDCBroker{
		validator:   validator,
		authService: authSvc,
	}
}

// /api/oidc/auth and /api/oidc/auth-query used to live here, as a 302 and a 501
// respectively. Both are now implemented in oidc_broker.go, which is where the
// state they need lives. The 501 is what item 2.3 left behind after removing a
// handler that answered 200 with {"access_token": "placeholder_token"} — a
// value no caller could tell from a real token.

// HandleJWKS serves the JWKS endpoint for client token validation
func (b *OIDCBroker) HandleJWKS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	jwks, err := b.validator.JWKS(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "failed to fetch JWKS")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(jwks)
}

// authRequest represents a login request
type authRequest struct {
	Username         string `json:"username"`
	Password         string `json:"password"`
	VerificationCode string `json:"verificationCode"`
	TFACode          string `json:"tfaCode"`
	Secret           string `json:"secret"`
	Type             string `json:"type"`
}

// HandleLogin handles user login with real OIDC JWT validation
func (b *OIDCBroker) HandleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req authRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	var token string
	if req.Secret != "" {
		token = req.Secret
	}

	var user *identity.User
	var err error

	if token != "" {
		claims, err := b.validator.ValidateToken(r.Context(), token)
		if err != nil {
			// The library's error names the claim that failed, the expected
			// audience, the issuer and the clock skew. That is a description of
			// how to build an acceptable token, handed to whoever presented an
			// unacceptable one. Log it, return the generic form, exactly as
			// httpx/jwt_middleware.go already does.
			log.Warn().Err(err).Str("path", r.URL.Path).Msg("token validation failed")
			httpx.WriteError(w, http.StatusUnauthorized, "invalid token")
			return
		}

		// Provisions the account on first sign-in, so a Keycloak user with no
		// local row can still complete a login.
		user, err = b.authService.ResolveUser(r.Context(), claims)
		if err != nil {
			httpx.WriteError(w, http.StatusUnauthorized, "user not found")
			return
		}
	} else {
		// Password sign-in is not offered by this deployment, and saying so is
		// the point: it used to accept a username and a password, verify them
		// against user_credentials, and refuse everybody, because nothing could
		// ever write a credential there. Identity lives in Keycloak, so the
		// client's route in is /api/oidc/auth, which /api/login-options now
		// advertises. See the note in internal/identity/service.go.
		httpx.WriteError(w, http.StatusBadRequest,
			"password sign-in is not enabled; use the single sign-on option in the sign-in dialog")
		return
	}

	if user == nil {
		httpx.WriteError(w, http.StatusUnauthorized, "user not found")
		return
	}

	if !user.Active {
		httpx.WriteError(w, http.StatusForbidden, "account disabled")
		return
	}

	sessionToken, err := b.authService.CreateSession(r.Context(), user)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "failed to create session")
		return
	}

	isAdmin := false
	for _, role := range user.Roles {
		if role.Name == "Administrator" || role.Name == "Support Manager" {
			isAdmin = true
			break
		}
	}

	status := 1
	if !user.Active {
		status = 0
	}

	resp := map[string]interface{}{
		"access_token": sessionToken,
		"type":         "Bearer",
		"user": map[string]interface{}{
			"name":         user.Email,
			"display_name": user.DisplayName,
			"avatar":       "",
			"email":        user.Email,
			"note":         "",
			"verifier":     "",
			"status":       status,
			"is_admin":     isAdmin,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}

// HandleLogout handles user logout
func (b *OIDCBroker) HandleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		httpx.WriteError(w, http.StatusUnauthorized, "missing authorization header")
		return
	}

	// The stored token is the bare value; revoking the header verbatim would
	// never match a row.
	sessionToken := strings.TrimPrefix(authHeader, "Bearer ")

	ctx := r.Context()
	err := b.authService.RevokeSession(ctx, sessionToken)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "failed to revoke session")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
	})
}
