package integration

import (
	"context"
	"encoding/json"
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
	"github.com/rs/zerolog"
)

// /api/peers is what a technician's RustDesk client lists under Devices, and it
// is the surface the portal's own device list cannot substitute for: the portal
// shows a device, the client connects to one.
//
// The contract is with flutter/lib/common/hbbs/hbbs.dart, where
// PeerPayload.fromJson reads `id` as the connect target and PeerPayload.toPeer
// builds the display name from info.device_name. The handler used to send the
// internal device uuid as `id`, so every peer a technician saw was unreachable,
// and it sent neither device_name nor a real status.

type peerServer struct {
	router *httpx.Mux
	token  string
}

func newPeerServer(t *testing.T, f *fixture, userID int64) *peerServer {
	t.Helper()
	zerolog.SetGlobalLevel(zerolog.Disabled)

	authService := identity.NewAuthService(f.db, "")

	token := fmt.Sprintf("peers-session-%d", userID)
	if _, err := f.db.Exec(context.Background(), `
		INSERT INTO client_sessions (user_id, token_hash, expires_at)
		VALUES ($1, encode(sha256($2::bytea), 'hex'), now() + interval '1 hour')
	`, userID, token); err != nil {
		t.Fatalf("failed to create a session: %v", err)
	}

	handler := rustdeskapi.NewPeerHandler(f.db, newResolver(f), audit.New(f.db))

	public := httpx.NewRouter(
		httpx.RequestIDMiddleware(),
		httpx.RecoveryMiddleware(),
		httpx.ContextMiddleware(),
	)
	protected := public.Group(httpx.JWTMiddleware(rejectingValidator{}, authService))
	protected.HandleFunc("/api/peers", handler.HandlePeers)

	return &peerServer{router: public, token: token}
}

func (s *peerServer) get(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader = strings.NewReader("")
	req := httptest.NewRequest(http.MethodGet, path, reader)
	req.Header.Set("Authorization", "Bearer "+s.token)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	return w
}

// The assertion that matters is the comparison, not the value: /api/peers and
// /api/v1/devices/{id}/connect both hand a technician a way onto a machine, and
// each passed its own test while disagreeing about what an id is.
func TestPeersAnswerWhatTheClientConnectsTo(t *testing.T) {
	f := newFixture(t)
	devices := newDeviceServer(t, f)
	portal := newV1Server(t, f, f.adminID)
	peers := newPeerServer(t, f, f.adminID)

	token := f.issueToken(t, nil, nil)
	w := devices.post(t, "/api/enroll", fmt.Sprintf(`{"token":%q,"id":"760000042"}`, token.Token))
	if w.Code != http.StatusOK {
		t.Fatalf("enrollment got %d: %s", w.Code, w.Body.String())
	}
	enrolled := decodeJSON(t, w)
	deviceID := enrolled["device_id"].(string)

	connect := portal.do(t, http.MethodPost, "/api/v1/devices/"+deviceID+"/connect", "")
	if connect.Code != http.StatusOK {
		t.Fatalf("connect got %d: %s", connect.Code, connect.Body.String())
	}
	wantID := decodeMap(t, connect)["rustdesk_id"]

	listed := peers.get(t, "/api/peers?current=1&pageSize=100")
	if listed.Code != http.StatusOK {
		t.Fatalf("peers got %d: %s", listed.Code, listed.Body.String())
	}
	var body struct {
		Data []struct {
			ID   string          `json:"id"`
			Name string          `json:"name"`
			Info json.RawMessage `json:"info"`
		} `json:"data"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode the peer list: %v", err)
	}

	var listedIDs []string
	found := -1
	for i, p := range body.Data {
		listedIDs = append(listedIDs, p.ID)
		if p.ID == wantID {
			found = i
		}
	}
	if found < 0 {
		t.Fatalf("the portal connects to %q and the peer list offers %v. The client "+
			"uses this field as its connect target, so a device listed under a "+
			"different id cannot be reached.", wantID, listedIDs)
	}

	// The platform-assigned name is what a technician searches on. It reaches
	// the client only through info.device_name.
	var info map[string]any
	if err := json.Unmarshal(body.Data[found].Info, &info); err != nil {
		t.Fatalf("failed to decode peer info: %v", err)
	}
	if info["device_name"] != body.Data[found].Name {
		t.Errorf("info.device_name = %v, want the device name %q; the client "+
			"displays this field and ignores the top-level name",
			info["device_name"], body.Data[found].Name)
	}
}

// A status hardcoded to 0 reported the whole fleet offline, so a technician had
// no way to tell whether a machine was worth trying before trying it.
func TestPeerStatusFollowsConnectivity(t *testing.T) {
	f := newFixture(t)
	devices := newDeviceServer(t, f)
	peers := newPeerServer(t, f, f.adminID)

	token := f.issueToken(t, nil, nil)
	w := devices.post(t, "/api/enroll", fmt.Sprintf(`{"token":%q,"id":"760000043"}`, token.Token))
	if w.Code != http.StatusOK {
		t.Fatalf("enrollment got %d: %s", w.Code, w.Body.String())
	}
	deviceID := decodeJSON(t, w)["device_id"].(string)

	const rustdeskID = "760000043"
	statusFor := func(t *testing.T) float64 {
		t.Helper()
		listed := peers.get(t, "/api/peers?current=1&pageSize=100")
		var body struct {
			Data []struct {
				ID     string  `json:"id"`
				Status float64 `json:"status"`
			} `json:"data"`
		}
		if err := json.Unmarshal(listed.Body.Bytes(), &body); err != nil {
			t.Fatalf("failed to decode the peer list: %v", err)
		}
		for _, p := range body.Data {
			if p.ID == rustdeskID {
				return p.Status
			}
		}
		t.Fatalf("the enrolled device %s is not in the peer list", rustdeskID)
		return 0
	}

	setConnectivity := func(t *testing.T, value string) {
		t.Helper()
		if _, err := f.db.Exec(context.Background(),
			`UPDATE devices SET connectivity = $2 WHERE id = $1`, deviceID, value); err != nil {
			t.Fatalf("failed to set connectivity: %v", err)
		}
	}

	// Discriminating rather than vacuous: the same query must give three
	// different answers, which a hardcoded value cannot.
	setConnectivity(t, "ONLINE")
	if got := statusFor(t); got != 1 {
		t.Errorf("ONLINE reported as %v, want 1", got)
	}
	setConnectivity(t, "OFFLINE")
	if got := statusFor(t); got != 0 {
		t.Errorf("OFFLINE reported as %v, want 0", got)
	}
	setConnectivity(t, "UNKNOWN")
	if got := statusFor(t); got != -1 {
		t.Errorf("UNKNOWN reported as %v, want -1", got)
	}
}
