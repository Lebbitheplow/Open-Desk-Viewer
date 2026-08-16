package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/OpenDeskViewer/platform/api/internal/config"
	"github.com/OpenDeskViewer/platform/api/internal/identity"
	"github.com/OpenDeskViewer/platform/api/internal/rustdeskapi"
)

// The RustDesk client's browser sign-in, end to end against a stub Keycloak.
//
// Three endpoints have to agree, and each of them passing alone proves very
// little: starting a sign-in that nothing can complete, a callback that nothing
// collects, or a poll that answers "pending" forever would all look fine in
// isolation. What these assert is the join.

// stubKeycloak stands in for the token endpoint. Only the token endpoint: the
// authorization endpoint is opened by a browser, which is exactly the part no
// test here can drive, and pretending otherwise would be the kind of green test
// that checks nothing.
type stubKeycloak struct {
	server *httptest.Server
	// lastForm is what the API posted, so the test can assert on PKCE rather
	// than assume it.
	lastForm url.Values
	// accessToken is what the stub returns; empty means answer 400.
	accessToken string
}

func newStubKeycloak(t *testing.T, realm, accessToken string) *stubKeycloak {
	t.Helper()

	stub := &stubKeycloak{accessToken: accessToken}
	mux := http.NewServeMux()
	mux.HandleFunc("/realms/"+realm+"/protocol/openid-connect/token",
		func(w http.ResponseWriter, r *http.Request) {
			if err := r.ParseForm(); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			stub.lastForm = r.PostForm

			if stub.accessToken == "" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": stub.accessToken,
				"token_type":   "Bearer",
			})
		})

	stub.server = httptest.NewServer(mux)
	t.Cleanup(stub.server.Close)
	return stub
}

// stubValidator accepts one token and rejects everything else, so a test can
// separate "the exchange worked" from "the token was trusted without checking".
type stubValidator struct {
	accept string
	claims *identity.JWTClaims
}

func (s stubValidator) ValidateToken(_ context.Context, token string) (*identity.JWTClaims, error) {
	if token != s.accept {
		return nil, fmt.Errorf("token rejected")
	}
	return s.claims, nil
}

type oidcHarness struct {
	login *rustdeskapi.OIDCLogin
	stub  *stubKeycloak
}

func newOIDCHarness(t *testing.T, f *fixture, subject, email string) *oidcHarness {
	t.Helper()

	const realm = "opendeskviewer"
	const token = "the-access-token"

	stub := newStubKeycloak(t, realm, token)

	cfg := &config.Config{
		KeycloakRealm: realm,
		// The token exchange is server to server, so it goes to the internal
		// Keycloak address. Pointing that at the stub is what makes this
		// testable without a live identity provider.
		KeycloakURL: stub.server.URL,
		OIDCIssuer:  "https://portal.example.com/realms/" + realm,
		PublicHost:  "portal.example.com",
	}

	validator := stubValidator{
		accept: token,
		claims: &identity.JWTClaims{
			Subject:           subject,
			Email:             email,
			Name:              email,
			PreferredUsername: email,
		},
	}

	return &oidcHarness{
		login: rustdeskapi.NewOIDCLogin(f.db, cfg, validator, identity.NewAuthService(f.db, "")),
		stub:  stub,
	}
}

// start runs POST /api/oidc/auth and returns the polling handle and the URL the
// browser would be sent to.
func (h *oidcHarness) start(t *testing.T, id, deviceUUID string) (code string, authURL *url.URL) {
	t.Helper()

	body := fmt.Sprintf(`{"op":"keycloak","id":%q,"uuid":%q}`, id, deviceUUID)
	req := httptest.NewRequest(http.MethodPost, "/api/oidc/auth", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.login.HandleAuth(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("starting a sign-in got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Code string `json:"code"`
		URL  string `json:"url"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("the start response is not JSON the client could parse: %v", err)
	}
	if resp.Code == "" || resp.URL == "" {
		t.Fatalf("the start response is missing code or url: %s", w.Body.String())
	}

	parsed, err := url.Parse(resp.URL)
	if err != nil {
		t.Fatalf("the authorization URL does not parse, so the client would reject it: %v", err)
	}
	return resp.Code, parsed
}

func (h *oidcHarness) poll(t *testing.T, code, id, deviceUUID string) map[string]any {
	t.Helper()

	target := fmt.Sprintf("/api/oidc/auth-query?code=%s&id=%s&uuid=%s",
		url.QueryEscape(code), url.QueryEscape(id), url.QueryEscape(deviceUUID))
	w := httptest.NewRecorder()
	h.login.HandleAuthQuery(w, httptest.NewRequest(http.MethodGet, target, nil))

	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("the poll response is not JSON: %q", w.Body.String())
	}
	return body
}

func (h *oidcHarness) callback(t *testing.T, query string) *httptest.ResponseRecorder {
	t.Helper()

	w := httptest.NewRecorder()
	h.login.HandleCallback(w, httptest.NewRequest(http.MethodGet, "/api/oidc/callback?"+query, nil))
	return w
}

// The whole flow, in the order the client drives it.
func TestBrowserSignInEndToEnd(t *testing.T) {
	f := newFixture(t)
	h := newOIDCHarness(t, f, "sub-tech1", "tech1@example.com")

	const id = "800000001"
	const deviceUUID = "uuid-800000001"

	code, authURL := h.start(t, id, deviceUUID)

	// What the browser is sent. Each of these is something the flow breaks on
	// silently if it is wrong.
	q := authURL.Query()
	if q.Get("response_type") != "code" {
		t.Errorf("response_type is %q", q.Get("response_type"))
	}
	if q.Get("client_id") != "odv-portal" {
		t.Errorf("client_id is %q; the exchange has no secret, so it must run as the public client", q.Get("client_id"))
	}
	if q.Get("code_challenge_method") != "S256" {
		t.Errorf("code_challenge_method is %q; the realm pins S256 on odv-portal", q.Get("code_challenge_method"))
	}
	if q.Get("code_challenge") == "" {
		t.Error("no PKCE challenge, so nothing proves the exchange belongs to the party that started the flow")
	}
	if q.Get("redirect_uri") != "https://portal.example.com/api/oidc/callback" {
		t.Errorf("redirect_uri is %q", q.Get("redirect_uri"))
	}
	state := q.Get("state")
	if state == "" {
		t.Fatal("no state, so any authorization code from anywhere would be accepted")
	}

	// Before the browser has finished, the poll must say "pending" in the exact
	// words the client keeps waiting on.
	pending := h.poll(t, code, id, deviceUUID)
	if pending["error"] != "No authed oidc is found" {
		t.Fatalf("a pending poll answered %v, which the client reads as a failed sign-in", pending)
	}
	if pending["access_token"] != nil {
		t.Fatal("a pending poll returned a token")
	}

	// The browser comes back.
	w := h.callback(t, "code=an-authorization-code&state="+url.QueryEscape(state))
	if w.Code != http.StatusOK {
		t.Fatalf("the callback got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "the-access-token") {
		t.Error("the callback page contains the access token; it is meant to carry none")
	}

	// PKCE actually reached Keycloak, and no client secret did.
	form := h.stub.lastForm
	if form.Get("code_verifier") == "" {
		t.Error("the exchange sent no code_verifier")
	}
	if form.Get("client_secret") != "" {
		t.Error("the exchange sent a client secret; odv-portal is public and this deployment holds no secret for it")
	}
	if form.Get("grant_type") != "authorization_code" {
		t.Errorf("grant_type is %q", form.Get("grant_type"))
	}

	// And now the client collects a real session token.
	done := h.poll(t, code, id, deviceUUID)
	token, _ := done["access_token"].(string)
	if token == "" {
		t.Fatalf("the poll after a completed callback returned no token: %v", done)
	}
	if done["type"] != "access_token" {
		t.Errorf(`type is %v; the client only stores the token when it reads "access_token"`, done["type"])
	}

	// The token has to be a session this API will accept, not a string.
	user, err := identity.NewAuthService(f.db, "").GetSessionUser(context.Background(), token)
	if err != nil || user == nil {
		t.Fatalf("the token the client was given is not a usable session: %v", err)
	}
	if user.Email != "tech1@example.com" {
		t.Errorf("the session belongs to %q", user.Email)
	}
}

// A completed sign-in is collectable once. The client polls in a loop, so a
// handle that keeps minting sessions would mint one per poll.
func TestACompletedSignInIsCollectedOnce(t *testing.T) {
	f := newFixture(t)
	h := newOIDCHarness(t, f, "sub-tech1", "tech1@example.com")

	const id = "800000002"
	const deviceUUID = "uuid-800000002"

	code, authURL := h.start(t, id, deviceUUID)
	h.callback(t, "code=an-authorization-code&state="+url.QueryEscape(authURL.Query().Get("state")))

	if first := h.poll(t, code, id, deviceUUID); first["access_token"] == nil {
		t.Fatalf("the first poll did not collect the sign-in: %v", first)
	}
	second := h.poll(t, code, id, deviceUUID)
	if second["access_token"] != nil {
		t.Error("the same handle was collected twice")
	}
}

// The state parameter is the only thing tying an incoming authorization code to
// a request this server started, and the callback is reachable by anyone.
func TestCallbackRefusesAnUnknownState(t *testing.T) {
	f := newFixture(t)
	h := newOIDCHarness(t, f, "sub-tech1", "tech1@example.com")

	const id = "800000003"
	const deviceUUID = "uuid-800000003"
	code, _ := h.start(t, id, deviceUUID)

	w := h.callback(t, "code=an-authorization-code&state=a-state-nobody-issued")
	if w.Code == http.StatusOK {
		t.Error("the callback accepted a state it never issued")
	}
	if h.stub.lastForm != nil {
		t.Error("the callback exchanged a code before checking the state")
	}
	if body := h.poll(t, code, id, deviceUUID); body["access_token"] != nil {
		t.Error("a forged callback completed somebody else's sign-in")
	}
}

// A handle is bound to the device that asked for it. Not a secret defence on
// its own, since a RustDesk id is not secret, but it is what the client's own
// contract says and it costs one WHERE clause.
func TestPollRequiresTheDeviceThatStartedTheSignIn(t *testing.T) {
	f := newFixture(t)
	h := newOIDCHarness(t, f, "sub-tech1", "tech1@example.com")

	const id = "800000004"
	const deviceUUID = "uuid-800000004"
	code, authURL := h.start(t, id, deviceUUID)
	h.callback(t, "code=an-authorization-code&state="+url.QueryEscape(authURL.Query().Get("state")))

	if other := h.poll(t, code, "800000999", deviceUUID); other["access_token"] != nil {
		t.Error("another device collected the sign-in")
	}
	if mine := h.poll(t, code, id, deviceUUID); mine["access_token"] == nil {
		t.Error("the device that started the sign-in could no longer collect it")
	}
}

// A token the validator refuses must not become a session, however well the
// exchange went. This is the difference between "Keycloak answered" and "the
// answer was checked".
func TestCallbackRefusesATokenThatDoesNotValidate(t *testing.T) {
	f := newFixture(t)
	h := newOIDCHarness(t, f, "sub-tech1", "tech1@example.com")
	h.stub.accessToken = "a-token-the-validator-does-not-accept"

	const id = "800000005"
	const deviceUUID = "uuid-800000005"
	code, authURL := h.start(t, id, deviceUUID)

	w := h.callback(t, "code=an-authorization-code&state="+url.QueryEscape(authURL.Query().Get("state")))
	if w.Code == http.StatusOK {
		t.Error("the callback accepted a token that failed validation")
	}
	if body := h.poll(t, code, id, deviceUUID); body["access_token"] != nil {
		t.Error("an unvalidated token produced a session")
	}
}

// The pending answer must not distinguish a handle that exists from one that
// does not, and it must not be a 4xx: the client keeps polling on this and a
// different shape ends the loop.
func TestPollOnAnUnknownHandleLooksLikePending(t *testing.T) {
	f := newFixture(t)
	h := newOIDCHarness(t, f, "sub-tech1", "tech1@example.com")

	w := httptest.NewRecorder()
	h.login.HandleAuthQuery(w, httptest.NewRequest(http.MethodGet,
		"/api/oidc/auth-query?code=never-issued&id=1&uuid=2", nil))

	if w.Code != http.StatusOK {
		t.Errorf("an unknown handle answered %d; the client stops polling on anything but a 200 body it can parse", w.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["error"] != "No authed oidc is found" {
		t.Errorf("an unknown handle answered %v, which tells a prober that the handle does not exist", body)
	}
}
