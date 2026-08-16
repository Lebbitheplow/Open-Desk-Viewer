package rustdeskapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The client's own parser, transcribed from
// flutter/lib/models/user_model.dart:queryOidcLoginOptions. Asserting against
// our own idea of the format would prove nothing: the bug this exists to catch
// was a response that was valid JSON, was accepted without error, and was then
// discarded entirely, leaving the sign-in dialog with no provider button.
func providersTheClientWouldShow(t *testing.T, body []byte) []map[string]any {
	t.Helper()

	var options []string
	if err := json.Unmarshal(body, &options); err != nil {
		t.Fatalf("the client parses the body as a list of strings, and it does not: %v", err)
	}

	for _, option := range options {
		if rest, ok := strings.CutPrefix(option, "common-oidc/"); ok {
			var providers []map[string]any
			if err := json.Unmarshal([]byte(rest), &providers); err != nil {
				t.Fatalf("the common-oidc payload is not a JSON array the client can read: %v", err)
			}
			return providers
		}
	}

	// The fallback branch: bare "oidc/<name>" entries. Everything else is
	// dropped on the floor, which is what used to happen to "common".
	var providers []map[string]any
	for _, option := range options {
		if name, ok := strings.CutPrefix(option, "oidc/"); ok {
			providers = append(providers, map[string]any{"name": name})
		}
	}
	return providers
}

func TestLoginOptionsOfferAProviderTheClientCanRender(t *testing.T) {
	h := NewHandlers(nil, nil, nil, nil, nil)

	w := httptest.NewRecorder()
	h.HandleLoginOptions(w, httptest.NewRequest(http.MethodGet, "/api/login-options", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("login options answered %d", w.Code)
	}

	providers := providersTheClientWouldShow(t, w.Body.Bytes())
	if len(providers) == 0 {
		t.Fatal("the client would show no sign-in provider at all, so there is no way into the client")
	}
	if name, _ := providers[0]["name"].(string); name == "" {
		t.Errorf("the provider carries no name, so its button has no label: %#v", providers[0])
	}
}

// The negative control, kept as a test rather than a comment: the previous
// response passed every check except the one that matters.
func TestTheOldLoginOptionsResponseOfferedNothing(t *testing.T) {
	if providers := providersTheClientWouldShow(t, []byte(`["common"]`)); len(providers) != 0 {
		t.Fatalf(`"common" is supposed to be discarded by the client's parser, and this transcription kept %v`, providers)
	}
}

// Password sign-in answers something an operator can act on, rather than the
// "invalid credentials" it used to return to everybody for want of any way to
// set a password.
func TestPasswordSignInIsRefusedWithAnExplanation(t *testing.T) {
	b := NewOIDCBroker(nil, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/login",
		strings.NewReader(`{"username":"tech@example.com","password":"whatever"}`))
	b.HandleLogin(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("password sign-in answered %d, want 400: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(strings.ToLower(w.Body.String()), "single sign-on") {
		t.Errorf("the refusal does not say what to do instead: %s", w.Body.String())
	}
}
