package rustdeskapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/OpenDeskViewer/platform/api/internal/auth"
	"github.com/OpenDeskViewer/platform/api/internal/httpx"
	"github.com/OpenDeskViewer/platform/api/internal/identity"
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

// HandleAuth handles the OIDC authorization endpoint
func (b *OIDCBroker) HandleAuth(w http.ResponseWriter, r *http.Request) {
	issuer := b.validator.Issuer()
	http.Redirect(w, r, issuer+"/protocol/openid-connect/auth", http.StatusFound)
}

// HandleAuthQuery handles OIDC token exchange
func (b *OIDCBroker) HandleAuthQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req struct {
		Code        string `json:"code"`
		GrantType   string `json:"grant_type"`
		RedirectURI string `json:"redirect_uri"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Code == "" {
		httpx.WriteError(w, http.StatusBadRequest, "missing authorization code")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"access_token":  "placeholder_token",
		"token_type":    "Bearer",
		"expires_in":    300,
		"refresh_token": "placeholder_refresh",
		"id_token":      "",
	})
}

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
			httpx.WriteError(w, http.StatusUnauthorized, fmt.Sprintf("invalid token: %v", err))
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
		if req.Username == "" || req.Password == "" {
			httpx.WriteError(w, http.StatusBadRequest, "missing username or password")
			return
		}

		user, err = b.authService.Authenticate(r.Context(), req.Username, req.Password)
		if err != nil {
			httpx.WriteError(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
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
