package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/OpenDeskViewer/platform/api/internal/audit"
	"github.com/OpenDeskViewer/platform/api/internal/httpx"
	"github.com/OpenDeskViewer/platform/api/internal/identity"
	"github.com/OpenDeskViewer/platform/api/internal/rustdeskapi"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// rejectingValidator stands in for Keycloak. These tests authenticate with the
// opaque session token /api/login issues, which is the credential the RustDesk
// client actually replays, so the JWT path is never taken.
type rejectingValidator struct{}

func (rejectingValidator) ValidateToken(context.Context, string) (*identity.JWTClaims, error) {
	return nil, errors.New("no OIDC issuer in tests")
}

// abServer is the real router, wired the way cmd/api wires it.
type abServer struct {
	router *httpx.Mux
	token  string
}

func newABServer(t *testing.T, f *fixture, userID int64) *abServer {
	t.Helper()
	zerolog.SetGlobalLevel(zerolog.Disabled)

	authService := identity.NewAuthService(f.db, "")

	token := fmt.Sprintf("session-token-%d", userID)
	_, err := f.db.Exec(context.Background(), `
		INSERT INTO client_sessions (user_id, token_hash, expires_at)
		VALUES ($1, encode(sha256($2::bytea), 'hex'), now() + interval '1 hour')
	`, userID, token)
	if err != nil {
		t.Fatalf("failed to create a session: %v", err)
	}

	auditService := audit.New(f.db)
	handler := rustdeskapi.NewAddressBookHandler(f.db, newResolver(f), 0, auditService)

	public := httpx.NewRouter(
		httpx.RequestIDMiddleware(),
		httpx.RecoveryMiddleware(),
		httpx.ContextMiddleware(),
	)
	protected := public.Group(httpx.JWTMiddleware(rejectingValidator{}, authService))

	protected.HandleFunc("/api/ab/settings", handler.HandleSettings)
	protected.HandleFunc("/api/ab/personal", handler.HandlePersonal)
	protected.HandleFunc("/api/ab/shared/profiles", handler.HandleSharedProfiles)
	protected.HandleFunc("/api/ab/peers", handler.HandlePeers)
	protected.HandleFunc("/api/ab/tags/{guid}", handler.HandleTags)
	protected.HandleFunc("/api/ab/peer/add/{guid}", handler.HandlePeerAdd)
	protected.HandleFunc("/api/ab/peer/update/{guid}", handler.HandlePeerUpdate)
	protected.HandleFunc("/api/ab/peer/{guid}", handler.HandlePeerDelete)
	protected.HandleFunc("/api/ab/tag/add/{guid}", handler.HandleTagAdd)
	protected.HandleFunc("/api/ab/tag/rename/{guid}", handler.HandleTagRename)
	protected.HandleFunc("/api/ab/tag/update/{guid}", handler.HandleTagUpdate)
	protected.HandleFunc("/api/ab/tag/{guid}", handler.HandleTagDelete)

	return &abServer{router: public, token: token}
}

// do issues a request the way the client does, with the session token attached.
func (s *abServer) do(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	return w
}

// anonymous issues the same request with no credential.
func (s *abServer) anonymous(t *testing.T, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, httptest.NewRequest(method, path, nil))
	return w
}

func decodeMap(t *testing.T, w *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var body map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode body: %v", err)
	}
	return body
}

// The sequence ab_model.dart runs at sign-in. Answering settings and personal is
// what takes the client out of legacy mode and makes the rest reachable.
func TestAddressBookSignInSequence(t *testing.T) {
	f := newFixture(t)
	s := newABServer(t, f, f.tech1ID)

	settings := s.do(t, http.MethodPost, "/api/ab/settings", "")
	if settings.Code != http.StatusOK {
		t.Fatalf("/api/ab/settings: expected 200, got %d", settings.Code)
	}
	if _, ok := decodeMap(t, settings)["max_peer_one_ab"]; !ok {
		t.Error("/api/ab/settings: expected max_peer_one_ab")
	}

	personal := s.do(t, http.MethodPost, "/api/ab/personal", "")
	if personal.Code != http.StatusOK {
		t.Fatalf("/api/ab/personal: expected 200, got %d", personal.Code)
	}
	guidValue, ok := decodeMap(t, personal)["guid"].(string)
	if !ok {
		t.Fatal("/api/ab/personal: expected a guid string")
	}
	if _, err := uuid.Parse(guidValue); err != nil {
		t.Fatalf("/api/ab/personal: expected a uuid, got %q", guidValue)
	}

	shared := s.do(t, http.MethodPost, "/api/ab/shared/profiles?current=1&pageSize=100", "")
	if shared.Code != http.StatusOK {
		t.Fatalf("/api/ab/shared/profiles: expected 200, got %d", shared.Code)
	}
	sharedBody := decodeMap(t, shared)
	if sharedBody["total"] != float64(1) {
		t.Errorf("/api/ab/shared/profiles: expected a total of 1, got %v", sharedBody["total"])
	}
	data, ok := sharedBody["data"].([]interface{})
	if !ok || len(data) != 1 {
		t.Fatalf("/api/ab/shared/profiles: expected 1 profile, got %v", sharedBody["data"])
	}
	profile := data[0].(map[string]interface{})
	for _, field := range []string{"guid", "name", "owner", "note", "rule"} {
		if _, ok := profile[field]; !ok {
			t.Errorf("/api/ab/shared/profiles: expected the %s field", field)
		}
	}

	// The shared book's peers are the support group's devices.
	sharedGUID := profile["guid"].(string)
	peers := s.do(t, http.MethodPost, "/api/ab/peers?current=1&pageSize=100&ab="+sharedGUID, "")
	if peers.Code != http.StatusOK {
		t.Fatalf("/api/ab/peers: expected 200, got %d", peers.Code)
	}
	peersBody := decodeMap(t, peers)
	if peersBody["total"] != float64(3) {
		t.Errorf("/api/ab/peers: expected a total of 3, got %v", peersBody["total"])
	}
	peerList := peersBody["data"].([]interface{})
	if len(peerList) != 3 {
		t.Fatalf("/api/ab/peers: expected 3 peers, got %d", len(peerList))
	}

	// The field names and types the client's Peer model reads.
	peer := peerList[0].(map[string]interface{})
	for _, field := range []string{"id", "hash", "username", "hostname", "platform", "alias", "tags", "note"} {
		if _, ok := peer[field]; !ok {
			t.Errorf("/api/ab/peers: expected the %s field", field)
		}
	}
	if _, ok := peer["forceAlwaysRelay"].(string); !ok {
		t.Errorf("/api/ab/peers: expected forceAlwaysRelay to be a string, got %T", peer["forceAlwaysRelay"])
	}
	if _, ok := peer["tags"].([]interface{}); !ok {
		t.Errorf("/api/ab/peers: expected tags to be an array, got %T", peer["tags"])
	}

	// Tags come back as a bare array, not a {total, data} envelope.
	tags := s.do(t, http.MethodPost, "/api/ab/tags/"+guidValue, "")
	if tags.Code != http.StatusOK {
		t.Fatalf("/api/ab/tags: expected 200, got %d", tags.Code)
	}
	var tagList []interface{}
	if err := json.NewDecoder(tags.Body).Decode(&tagList); err != nil {
		t.Fatalf("/api/ab/tags: expected a JSON array, got %v", err)
	}
}

// The client sends current and pageSize in the query on a POST with an empty
// body. Reading only the body would page at the default size and lose the tail.
func TestAddressBookPeersHonoursQueryPagination(t *testing.T) {
	f := newFixture(t)
	s := newABServer(t, f, f.tech1ID)

	page := s.do(t, http.MethodPost, "/api/ab/peers?current=1&pageSize=2&ab="+f.group1.String(), "")
	if page.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", page.Code)
	}

	body := decodeMap(t, page)
	if body["total"] != float64(3) {
		t.Errorf("expected a total of 3, got %v", body["total"])
	}
	if data := body["data"].([]interface{}); len(data) != 2 {
		t.Errorf("expected the requested page size of 2, got %d rows", len(data))
	}

	second := s.do(t, http.MethodPost, "/api/ab/peers?current=2&pageSize=2&ab="+f.group1.String(), "")
	if data := decodeMap(t, second)["data"].([]interface{}); len(data) != 1 {
		t.Errorf("expected 1 row on the second page, got %d", len(data))
	}
}

// The full set of mutations, each with the body the client sends. A successful
// mutation must answer 200 with an empty body.
func TestAddressBookMutationRoutes(t *testing.T) {
	f := newFixture(t)
	s := newABServer(t, f, f.tech1ID)

	personal := s.do(t, http.MethodPost, "/api/ab/personal", "")
	guid := decodeMap(t, personal)["guid"].(string)

	steps := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"peer add", http.MethodPost, "/api/ab/peer/add/" + guid,
			`{"id":"500000001","hash":"h","username":"u","hostname":"host","platform":"Linux","alias":"a","tags":["t1"],"forceAlwaysRelay":"true","rdpPort":"3389","rdpUsername":"admin"}`},
		{"tag add", http.MethodPost, "/api/ab/tag/add/" + guid, `{"name":"t1","color":4294198070}`},
		{"peer update tags", http.MethodPut, "/api/ab/peer/update/" + guid, `{"id":"500000001","tags":["t1"]}`},
		{"peer update alias", http.MethodPut, "/api/ab/peer/update/" + guid, `{"id":"500000001","alias":"renamed"}`},
		{"peer update note", http.MethodPut, "/api/ab/peer/update/" + guid, `{"id":"500000001","note":"a note"}`},
		{"peer update hash", http.MethodPut, "/api/ab/peer/update/" + guid, `{"id":"500000001","hash":"newhash"}`},
		{"tag rename", http.MethodPut, "/api/ab/tag/rename/" + guid, `{"old":"t1","new":"t2"}`},
		{"tag update", http.MethodPut, "/api/ab/tag/update/" + guid, `{"name":"t2","color":42}`},
		{"tag delete", http.MethodDelete, "/api/ab/tag/" + guid, `["t2"]`},
		{"peer delete", http.MethodDelete, "/api/ab/peer/" + guid, `["500000001"]`},
	}

	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			w := s.do(t, step.method, step.path, step.body)

			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d (%s)", w.Code, w.Body.String())
			}
			// _jsonDecodeActionResp treats any body on a 200 as an error.
			if w.Body.Len() != 0 {
				t.Errorf("expected an empty body, got %q", w.Body.String())
			}
		})
	}
}

// The alias survived a later update that did not mention it, and the tag rename
// reached the peer. This is the partial-update contract end to end.
func TestAddressBookMutationsPersist(t *testing.T) {
	f := newFixture(t)
	s := newABServer(t, f, f.tech1ID)

	guid := decodeMap(t, s.do(t, http.MethodPost, "/api/ab/personal", ""))["guid"].(string)

	s.do(t, http.MethodPost, "/api/ab/peer/add/"+guid, `{"id":"600000001","alias":"first","tags":["blue"]}`)
	s.do(t, http.MethodPost, "/api/ab/tag/add/"+guid, `{"name":"blue","color":7}`)
	s.do(t, http.MethodPut, "/api/ab/peer/update/"+guid, `{"id":"600000001","note":"only the note"}`)
	s.do(t, http.MethodPut, "/api/ab/tag/rename/"+guid, `{"old":"blue","new":"green"}`)

	peers := s.do(t, http.MethodPost, "/api/ab/peers?current=1&pageSize=100&ab="+guid, "")
	data := decodeMap(t, peers)["data"].([]interface{})
	if len(data) != 1 {
		t.Fatalf("expected 1 peer, got %d", len(data))
	}

	peer := data[0].(map[string]interface{})
	if peer["alias"] != "first" {
		t.Errorf("expected the alias to survive a note-only update, got %v", peer["alias"])
	}
	if peer["note"] != "only the note" {
		t.Errorf("expected the note to be set, got %v", peer["note"])
	}
	tags := peer["tags"].([]interface{})
	if len(tags) != 1 || tags[0] != "green" {
		t.Errorf("expected the renamed tag on the peer, got %v", tags)
	}
}

func TestAddressBookRejectsWritesToASharedBook(t *testing.T) {
	f := newFixture(t)
	s := newABServer(t, f, f.tech1ID)

	w := s.do(t, http.MethodPost, "/api/ab/peer/add/"+f.group1.String(), `{"id":"700000001"}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
	if _, ok := decodeMap(t, w)["error"]; !ok {
		t.Error("expected an error field the client can surface")
	}
}

func TestAddressBookRejectsAnotherGroupsBook(t *testing.T) {
	f := newFixture(t)
	s := newABServer(t, f, f.tech1ID)

	w := s.do(t, http.MethodPost, "/api/ab/peers?current=1&pageSize=100&ab="+f.group2.String(), "")
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for another group's book, got %d", w.Code)
	}
}

func TestAddressBookRequiresAuthentication(t *testing.T) {
	f := newFixture(t)
	s := newABServer(t, f, f.tech1ID)

	for _, path := range []string{"/api/ab/settings", "/api/ab/personal", "/api/ab/shared/profiles"} {
		w := s.anonymous(t, http.MethodPost, path)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s without a credential: expected 401, got %d", path, w.Code)
		}
	}
}

// An admin gets the fleet book alongside every support group.
func TestAddressBookFleetProfileForAdmins(t *testing.T) {
	f := newFixture(t)
	s := newABServer(t, f, f.adminID)

	shared := s.do(t, http.MethodPost, "/api/ab/shared/profiles?current=1&pageSize=100", "")
	body := decodeMap(t, shared)
	if body["total"] != float64(3) {
		t.Fatalf("expected 3 shared books for an admin, got %v", body["total"])
	}

	fleet := body["data"].([]interface{})[0].(map[string]interface{})
	peers := s.do(t, http.MethodPost, "/api/ab/peers?current=1&pageSize=100&ab="+fleet["guid"].(string), "")
	if peers.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", peers.Code)
	}
	if total := decodeMap(t, peers)["total"]; total != float64(4) {
		t.Errorf("expected the fleet book to hold every active device, got %v", total)
	}
}
