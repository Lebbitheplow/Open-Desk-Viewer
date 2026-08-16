package rustdeskapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The verbs the browser sign-in accepts, pinned because the client picks one
// per endpoint and gets no say in it: POST to start, GET to poll, GET for the
// callback the browser is redirected to. Answering the wrong verb with 200 was
// how /api/oidc/auth came to serve a 302 the client could not parse.
func TestBrowserSignInVerbs(t *testing.T) {
	o := &OIDCLogin{}

	cases := []struct {
		name    string
		handler func(http.ResponseWriter, *http.Request)
		wrong   string
		path    string
	}{
		{"auth is POST", o.HandleAuth, http.MethodGet, "/api/oidc/auth"},
		{"auth-query is GET", o.HandleAuthQuery, http.MethodPost, "/api/oidc/auth-query"},
		{"callback is GET", o.HandleCallback, http.MethodPost, "/api/oidc/callback"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			tc.handler(w, httptest.NewRequest(tc.wrong, tc.path, nil))

			if w.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s %s: expected 405, got %d", tc.wrong, tc.path, w.Code)
			}
		})
	}
}

// The string the client polls on. account.rs:325 keeps the loop running only
// while the error reads exactly this; any other text ends the sign-in with a
// failure message. A well-meant rewording here would look like a broken flow.
func TestPendingMessageIsTheOneTheClientWaitsOn(t *testing.T) {
	if pendingMessage != "No authed oidc is found" {
		t.Errorf("pendingMessage is %q; src/hbbs_http/account.rs matches on \"No authed oidc is found\" and treats anything else as a failure", pendingMessage)
	}
}

// PKCE S256, which is what the realm pins on odv-portal, checked against the
// worked example in RFC 7636 appendix B so the encoding is verified against the
// specification rather than against itself.
func TestPKCEChallengeMatchesRFC7636(t *testing.T) {
	const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	const want = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"

	if got := pkceChallenge(verifier); got != want {
		t.Errorf("pkceChallenge = %q, want %q (RFC 7636 appendix B)", got, want)
	}
}

// A handle is stored as a hash, like every other credential in this codebase.
// A database reader who could see the plaintext could collect somebody else's
// sign-in in progress.
func TestHandlesAreRandomAndStoredHashed(t *testing.T) {
	first, firstHash, err := randomHandle()
	if err != nil {
		t.Fatal(err)
	}
	second, secondHash, err := randomHandle()
	if err != nil {
		t.Fatal(err)
	}

	if first == second {
		t.Error("two handles came out identical")
	}
	if firstHash == first {
		t.Error("the stored form is the handle itself")
	}
	if len(firstHash) != 64 {
		t.Errorf("the stored form is %d characters, want a 64-character hex SHA-256", len(firstHash))
	}
	if firstHash == secondHash {
		t.Error("two different handles hash the same")
	}
	if hashHandle(first) != firstHash {
		t.Error("hashHandle is not what randomHandle returned, so a lookup would never match")
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
