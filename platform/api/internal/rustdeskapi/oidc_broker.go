package rustdeskapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/OpenDeskViewer/platform/api/internal/config"
	"github.com/OpenDeskViewer/platform/api/internal/httpx"
	"github.com/OpenDeskViewer/platform/api/internal/identity"
	"github.com/OpenDeskViewer/platform/api/internal/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog/log"
)

// Browser sign-in for the RustDesk client.
//
// The client is a desktop or Android application and cannot host a redirect
// URI, so the flow is split in three. It asks this API to start a sign-in and
// gets back a URL and a polling handle; it opens the URL in the user's browser;
// Keycloak sends the browser back to /api/oidc/callback, which finishes the
// exchange; and the client's poll of /api/oidc/auth-query then collects a
// session token. src/hbbs_http/account.rs is the other half of this contract and
// was read to write it, rather than guessed at.
//
// This replaces the 501 from item 2.3, and the decision that item was waiting on
// turned out not to need making. Both options on the table involved getting the
// odv-api client secret to the API somehow. Neither is necessary: the exchange
// runs against odv-portal, which is a public client with PKCE, so what proves
// the exchange belongs to the party that started the flow is the code verifier,
// not a shared secret. No secret is stored, templated, or read out of Keycloak.

const (
	// authRequestTTL is how long a started sign-in stays collectable. The
	// client stops polling after its own timeout; there is no value in keeping a
	// pending row past the point where anyone is waiting for it.
	authRequestTTL = 10 * time.Minute

	// pendingMessage is matched verbatim by the client, which keeps polling on
	// this exact string and stops on any other error (account.rs:325). Changing
	// it turns "still waiting for the browser" into "sign-in failed".
	pendingMessage = "No authed oidc is found"
)

// OIDCLogin implements the client's browser sign-in.
type OIDCLogin struct {
	db          *postgres.Pool
	cfg         *config.Config
	validator   tokenValidator
	authService *identity.AuthService
	http        *http.Client
	// tokenURL is the endpoint the authorization code is exchanged at. It is
	// the internal Keycloak address, not the public one, for the same reason
	// the JWKS is: the exchange is server to server and has no business leaving
	// the private network or depending on public DNS resolving from inside it.
	tokenURL string
	// authURL is the public authorization endpoint, because a browser opens it.
	authURL string
	// redirectURI is the public callback, and must be registered on the
	// odv-portal client. The realm gives it redirectUris ["/*"] relative to the
	// deployment origin, which covers this without naming the host.
	redirectURI string
}

// tokenValidator is the part of auth.JWTValidator this needs, named as an
// interface so a test can substitute one without a live Keycloak.
type tokenValidator interface {
	ValidateToken(ctx context.Context, token string) (*identity.JWTClaims, error)
}

// NewOIDCLogin wires the browser sign-in flow.
func NewOIDCLogin(db *postgres.Pool, cfg *config.Config, validator tokenValidator, authService *identity.AuthService) *OIDCLogin {
	realm := strings.TrimSuffix(cfg.OIDCIssuer, "/")

	return &OIDCLogin{
		db:          db,
		cfg:         cfg,
		validator:   validator,
		authService: authService,
		// A deadline rather than the default of none: Keycloak being slow must
		// not tie up a request goroutine indefinitely.
		http:        &http.Client{Timeout: 15 * time.Second},
		tokenURL:    fmt.Sprintf("%s/realms/%s/protocol/openid-connect/token", strings.TrimSuffix(cfg.KeycloakURL, "/"), cfg.KeycloakRealm),
		authURL:     realm + "/protocol/openid-connect/auth",
		redirectURI: "https://" + cfg.PublicHost + "/api/oidc/callback",
	}
}

// clientID is the public Keycloak client the exchange runs as. odv-portal
// rather than odv-api: odv-api is confidential and has the standard flow turned
// off, and odv-portal already carries the audience mapper that puts odv-api in
// the access token this API then validates.
func (o *OIDCLogin) clientID() string {
	if o.cfg.OIDCClientPortal != "" {
		return o.cfg.OIDCClientPortal
	}
	return "odv-portal"
}

// HandleAuth serves POST /api/oidc/auth: start a sign-in.
//
// The previous implementation answered with a 302. The client does not follow
// it; it parses the body as JSON looking for {code, url} (account.rs:25-28,
// 174-177), so a redirect arrived as a parse failure and the sign-in button did
// nothing an operator could interpret.
func (o *OIDCLogin) HandleAuth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req struct {
		Op   string `json:"op"`
		ID   string `json:"id"`
		UUID string `json:"uuid"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.ID == "" || req.UUID == "" {
		httpx.WriteError(w, http.StatusBadRequest, "missing id or uuid")
		return
	}

	code, codeHash, err := randomHandle()
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "failed to start sign-in")
		return
	}
	state, stateHash, err := randomHandle()
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "failed to start sign-in")
		return
	}
	verifier, err := randomToken()
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "failed to start sign-in")
		return
	}

	if _, err := o.db.Exec(r.Context(), `
		INSERT INTO oidc_auth_requests
			(code_hash, state_hash, code_verifier, rustdesk_id, device_uuid, expires_at)
		VALUES ($1, $2, $3, $4, $5, now() + make_interval(secs => $6))
	`, codeHash, stateHash, verifier, req.ID, req.UUID, authRequestTTL.Seconds()); err != nil {
		log.Error().Err(err).Msg("failed to record an OIDC auth request")
		httpx.WriteError(w, http.StatusInternalServerError, "failed to start sign-in")
		return
	}

	// Opportunistic, and deliberately after the insert: expired rows are
	// harmless, and failing a sign-in because a cleanup failed would be a
	// worse trade than leaving a few rows behind.
	if _, err := o.db.Exec(r.Context(),
		`DELETE FROM oidc_auth_requests WHERE expires_at < now()`); err != nil {
		log.Warn().Err(err).Msg("failed to expire old OIDC auth requests")
	}

	params := url.Values{
		"response_type":         {"code"},
		"client_id":             {o.clientID()},
		"redirect_uri":          {o.redirectURI},
		"scope":                 {"openid profile email"},
		"state":                 {state},
		"code_challenge":        {pkceChallenge(verifier)},
		"code_challenge_method": {"S256"},
	}

	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"code": code,
		"url":  o.authURL + "?" + params.Encode(),
	})
}

// HandleAuthQuery serves GET /api/oidc/auth-query: collect the finished
// sign-in.
//
// It is a GET with query parameters. Item 2.3 recorded openapi.yaml as being
// wrong about that and "corrected" it to POST to match a handler that only
// answered POST. Reading account.rs:181-195 settles it the other way: the
// client builds a URL with code, id and uuid and issues a GET, so the handler
// was what was wrong and the spec was right.
func (o *OIDCLogin) HandleAuthQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	code := r.URL.Query().Get("code")
	id := r.URL.Query().Get("id")
	deviceUUID := r.URL.Query().Get("uuid")
	if code == "" {
		httpx.WriteError(w, http.StatusBadRequest, "missing code")
		return
	}

	// One statement claims the row, and only claims one the callback has
	// finished. Claiming unconditionally would need releasing again on the
	// pending path, and a poll that crashed between the two would strand the
	// sign-in. `user_id IS NOT NULL` makes "still waiting" simply not match.
	//
	// The claim is what makes a completed sign-in single-use: the client polls
	// on a loop, and two polls in flight must not both be handed a session.
	var userID int64
	err := o.db.QueryRow(r.Context(), `
		UPDATE oidc_auth_requests
		SET collected_at = now()
		WHERE code_hash = $1
		  AND rustdesk_id = $2
		  AND device_uuid = $3
		  AND expires_at > now()
		  AND collected_at IS NULL
		  AND user_id IS NOT NULL
		RETURNING user_id
	`, hashHandle(code), id, deviceUUID).Scan(&userID)

	if errors.Is(err, pgx.ErrNoRows) {
		// Pending, unknown, expired and already-collected all answer the same
		// way. The client keeps polling on this string, which is right for the
		// pending case and harmless for the rest: its own timeout ends the loop,
		// and an unknown handle learns nothing from the answer.
		httpx.WriteError(w, http.StatusOK, pendingMessage)
		return
	}
	if err != nil {
		log.Error().Err(err).Msg("failed to read an OIDC auth request")
		httpx.WriteError(w, http.StatusInternalServerError, "sign-in failed")
		return
	}

	user, err := o.authService.GetUserByID(r.Context(), userID)
	if err != nil || user == nil {
		httpx.WriteError(w, http.StatusUnauthorized, "user not found")
		return
	}
	if !user.Active {
		httpx.WriteError(w, http.StatusForbidden, "account disabled")
		return
	}

	// Minted here rather than at the callback, so the plaintext session token
	// exists only in this response and never in the database.
	sessionToken, err := o.authService.CreateSession(r.Context(), user)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "failed to create session")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"access_token": sessionToken,
		// The client stores the token only when this reads "access_token"
		// (account.rs:303). Anything else is treated as a sign-in that did not
		// produce one.
		"type": "access_token",
		"user": userPayload(user),
	})
}

// HandleCallback serves GET /api/oidc/callback, which is where Keycloak sends
// the browser.
//
// Everything about this endpoint is reachable by anyone, so it proves three
// things before it trusts a code: the state matches a request this server
// started, that request has not expired, and the token Keycloak returns
// validates against the same issuer, audience and signing keys every other
// route requires.
func (o *OIDCLogin) HandleCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	query := r.URL.Query()
	if errCode := query.Get("error"); errCode != "" {
		// The user pressed cancel, or Keycloak refused. Say so on the page and
		// leave the request row to expire, so the client's poll times out
		// rather than hanging on a sign-in that will never arrive.
		log.Info().Str("error", errCode).Msg("OIDC sign-in was refused at the identity provider")
		o.renderCallback(w, http.StatusOK, "Sign-in was cancelled", "You can close this window and try again from the application.")
		return
	}

	authCode := query.Get("code")
	state := query.Get("state")
	if authCode == "" || state == "" {
		o.renderCallback(w, http.StatusBadRequest, "Sign-in could not be completed", "The identity provider did not return an authorization code.")
		return
	}

	var requestID string
	var verifier string
	err := o.db.QueryRow(r.Context(), `
		SELECT id, code_verifier FROM oidc_auth_requests
		WHERE state_hash = $1 AND expires_at > now() AND completed_at IS NULL
	`, hashHandle(state)).Scan(&requestID, &verifier)
	if errors.Is(err, pgx.ErrNoRows) {
		o.renderCallback(w, http.StatusBadRequest, "Sign-in could not be completed",
			"This sign-in has expired or was already completed. Start it again from the application.")
		return
	}
	if err != nil {
		log.Error().Err(err).Msg("failed to look up an OIDC auth request")
		o.renderCallback(w, http.StatusInternalServerError, "Sign-in could not be completed", "Something went wrong. Try again from the application.")
		return
	}

	accessToken, err := o.exchange(r.Context(), authCode, verifier)
	if err != nil {
		// The upstream error names endpoints, client ids and grant types. It
		// goes to the log, not to a page anyone can reach.
		log.Warn().Err(err).Msg("OIDC authorization code exchange failed")
		o.renderCallback(w, http.StatusBadGateway, "Sign-in could not be completed", "The identity provider rejected the sign-in. Try again from the application.")
		return
	}

	claims, err := o.validator.ValidateToken(r.Context(), accessToken)
	if err != nil {
		log.Warn().Err(err).Msg("OIDC callback token failed validation")
		o.renderCallback(w, http.StatusUnauthorized, "Sign-in could not be completed", "The sign-in could not be verified. Try again from the application.")
		return
	}

	// Provisions the local row on first sign-in, the same way HandleLogin does,
	// so a Keycloak user who has never used the portal can still sign in here.
	user, err := o.authService.ResolveUser(r.Context(), claims)
	if err != nil || user == nil {
		log.Warn().Err(err).Msg("OIDC callback could not resolve a user")
		o.renderCallback(w, http.StatusForbidden, "Sign-in could not be completed", "This account is not permitted to use this deployment.")
		return
	}
	if !user.Active {
		o.renderCallback(w, http.StatusForbidden, "Sign-in could not be completed", "This account is disabled.")
		return
	}

	if _, err := o.db.Exec(r.Context(), `
		UPDATE oidc_auth_requests SET user_id = $2, completed_at = now() WHERE id = $1
	`, requestID, user.ID); err != nil {
		log.Error().Err(err).Msg("failed to complete an OIDC auth request")
		o.renderCallback(w, http.StatusInternalServerError, "Sign-in could not be completed", "Something went wrong. Try again from the application.")
		return
	}

	o.renderCallback(w, http.StatusOK, "You are signed in",
		"You can close this window and return to the application.")
}

// exchange trades the authorization code for an access token. No client secret:
// odv-portal is public and PKCE is what authenticates the exchange.
func (o *OIDCLogin) exchange(ctx context.Context, authCode, verifier string) (string, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {authCode},
		"redirect_uri":  {o.redirectURI},
		"client_id":     {o.clientID()},
		"code_verifier": {verifier},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := o.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	// Capped, because this body is parsed and an identity provider that starts
	// streaming should not be able to exhaust memory here.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token endpoint returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("token endpoint returned a body that is not JSON: %w", err)
	}
	if parsed.AccessToken == "" {
		return "", errors.New("token endpoint returned no access token")
	}
	return parsed.AccessToken, nil
}

// callbackPage is the only HTML this API serves. It is a template rather than
// string concatenation so that a message can never become markup, and it holds
// no token: the client collects that over its own polling channel.
var callbackPage = template.Must(template.New("callback").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
body { font-family: system-ui, sans-serif; margin: 0; display: grid; place-items: center; min-height: 100vh; background: #f9fafb; color: #111827; }
main { max-width: 26rem; padding: 2rem; text-align: center; }
h1 { font-size: 1.25rem; margin: 0 0 .5rem; }
p { margin: 0; color: #6b7280; line-height: 1.5; }
</style>
</head>
<body><main><h1>{{.Title}}</h1><p>{{.Message}}</p></main></body>
</html>
`))

func (o *OIDCLogin) renderCallback(w http.ResponseWriter, status int, title, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := callbackPage.Execute(w, struct{ Title, Message string }{title, message}); err != nil {
		log.Error().Err(err).Msg("failed to render the OIDC callback page")
	}
}

// userPayload is the user shape the client parses, shared with HandleLogin so
// the two sign-in routes cannot describe the same person differently.
func userPayload(user *identity.User) map[string]any {
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
	return map[string]any{
		"name":         user.Email,
		"display_name": user.DisplayName,
		"avatar":       "",
		"email":        user.Email,
		"note":         "",
		"verifier":     "",
		"status":       status,
		"is_admin":     isAdmin,
	}
}

// randomHandle returns 32 bytes of entropy and the SHA-256 stored against it.
func randomHandle() (handle, hash string, err error) {
	handle, err = randomToken()
	if err != nil {
		return "", "", err
	}
	return handle, hashHandle(handle), nil
}

func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func hashHandle(handle string) string {
	sum := sha256.Sum256([]byte(handle))
	return hex.EncodeToString(sum[:])
}

// pkceChallenge is S256, which is what the realm pins on odv-portal. "plain"
// would be accepted by nothing here and is not offered.
func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
