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
	"time"

	"github.com/OpenDeskViewer/platform/api/internal/audit"
	"github.com/OpenDeskViewer/platform/api/internal/config"
	"github.com/OpenDeskViewer/platform/api/internal/deviceauth"
	"github.com/OpenDeskViewer/platform/api/internal/enrollment"
	"github.com/OpenDeskViewer/platform/api/internal/fleet"
	"github.com/OpenDeskViewer/platform/api/internal/httpx"
	"github.com/OpenDeskViewer/platform/api/internal/identity"
	"github.com/OpenDeskViewer/platform/api/internal/monitoring"
	"github.com/OpenDeskViewer/platform/api/internal/rustdeskapi"
	"github.com/OpenDeskViewer/platform/api/internal/telemetry"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// deviceServer is the device-facing surface: enrollment, heartbeat and sysinfo,
// wired the way cmd/api wires them. These routes are public, so there is no
// token; the device's own credential is the whole point.
type deviceServer struct {
	router *httpx.Mux
	f      *fixture
}

func newDeviceServer(t *testing.T, f *fixture) *deviceServer {
	t.Helper()
	zerolog.SetGlobalLevel(zerolog.Disabled)

	cfg := &config.Config{
		DeviceStaleAfterSeconds:   300,
		DeviceOfflineAfterSeconds: 900,
		// The production default, from internal/config. Naming a device after
		// its serial is what makes it findable, so the harness has to carry the
		// same template the deployment does.
		DeviceNameTemplate: "{customer}-{location}-{serial}",
	}

	// One fleet service shared by everything here, as cmd/api wires it.
	fleetService := fleet.NewService(f.db, cfg)

	telemetryService := telemetry.NewService(f.db, cfg, fleetService, monitoring.New(f.db))
	deviceAuthService := deviceauth.New(f.db)
	enrollmentService := enrollment.NewService(f.db, cfg, fleetService)
	auditService := audit.New(f.db)

	handlers := rustdeskapi.NewHandlers(identity.NewAuthService(f.db, ""), telemetryService, auditService, deviceAuthService, newDevicePasswords(t, f))
	enrollHandler := rustdeskapi.NewEnrollmentHandler(f.db, newResolver(f), enrollmentService,
		fleetService, auditService)

	router := httpx.NewRouter(
		httpx.RequestIDMiddleware(),
		httpx.RecoveryMiddleware(),
		httpx.ContextMiddleware(),
	)
	router.HandleFunc("/api/enroll", enrollHandler.HandleEnroll)
	router.HandleFunc("/api/heartbeat", handlers.HandleHeartbeat)
	router.HandleFunc("/api/sysinfo", handlers.HandleSysinfo)

	return &deviceServer{router: router, f: f}
}

func (s *deviceServer) post(t *testing.T, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(http.MethodPost, path, reader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Real-IP", "198.51.100.20")

	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	return w
}

// issueToken creates a token the way the portal does, and returns the plaintext.
func (f *fixture) issueToken(t *testing.T, maxUses *int, expiresAt *time.Time) *enrollment.Token {
	t.Helper()

	svc := enrollment.NewService(f.db, &config.Config{}, fleet.NewService(f.db, &config.Config{}))
	token, err := svc.GenerateToken(context.Background(), f.customer, nil, &f.deviceGroup1,
		"Acme HQ", "", maxUses, expiresAt, f.adminID)
	if err != nil {
		t.Fatalf("failed to issue an enrollment token: %v", err)
	}
	return token
}

func decodeJSON(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode %q: %v", w.Body.String(), err)
	}
	return body
}

// The whole point of Phase 3.1: a device with no credential cannot enter the
// fleet, and one that enrolls can.
func TestEnrollThenHeartbeat(t *testing.T) {
	f := newFixture(t)
	s := newDeviceServer(t, f)

	// Before enrollment: the heartbeat is refused and nothing is registered.
	w := s.post(t, "/api/heartbeat", `{"id":"700000001","uuid":"uuid-700000001"}`)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unenrolled heartbeat got %d, want 401: %s", w.Code, w.Body.String())
	}

	var devices int
	if err := f.db.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM devices WHERE rustdesk_id = $1`, "700000001").Scan(&devices); err != nil {
		t.Fatalf("failed to count devices: %v", err)
	}
	if devices != 0 {
		t.Fatal("an unenrolled heartbeat registered a device")
	}

	// The sighting is recorded, which is what an operator needs to see.
	var sightings int64
	if err := f.db.QueryRow(context.Background(),
		`SELECT sightings FROM device_observations WHERE rustdesk_id = $1`, "700000001").Scan(&sightings); err != nil {
		t.Fatalf("no observation was recorded: %v", err)
	}
	if sightings != 1 {
		t.Errorf("sightings = %d, want 1", sightings)
	}

	// Enroll.
	token := f.issueToken(t, nil, nil)
	w = s.post(t, "/api/enroll", fmt.Sprintf(
		`{"token":%q,"id":"700000001","uuid":"uuid-700000001","hostname":"acme-hq-99","os":"Android 14"}`, token.Token))
	if w.Code != http.StatusOK {
		t.Fatalf("enrollment got %d, want 200: %s", w.Code, w.Body.String())
	}

	body := decodeJSON(t, w)
	secret, _ := body["device_token"].(string)
	if len(secret) < 32 {
		t.Fatalf("device_token = %q, want a high-entropy secret", secret)
	}

	// The device is now in the fleet, in the token's customer and group.
	var deviceID uuid.UUID
	var state, name string
	var customerID uuid.UUID
	if err := f.db.QueryRow(context.Background(),
		`SELECT id, state, name, customer_id FROM devices WHERE rustdesk_id = $1`,
		"700000001").Scan(&deviceID, &state, &name, &customerID); err != nil {
		t.Fatalf("the device was not created: %v", err)
	}
	if state != "ACTIVE" {
		t.Errorf("state = %q, want ACTIVE", state)
	}
	if name != "acme-hq-99" {
		t.Errorf("name = %q, want the hostname", name)
	}
	if customerID != f.customer {
		t.Errorf("customer = %s, want the token's %s", customerID, f.customer)
	}

	var inGroup bool
	if err := f.db.QueryRow(context.Background(), `
		SELECT EXISTS (SELECT 1 FROM device_group_members WHERE device_id = $1 AND device_group_id = $2)
	`, deviceID, f.deviceGroup1).Scan(&inGroup); err != nil {
		t.Fatalf("failed to check group membership: %v", err)
	}
	if !inGroup {
		t.Error("the enrolled device did not join the token's device group")
	}

	// The observation is cleared: the id is a device now.
	var observations int
	if err := f.db.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM device_observations WHERE rustdesk_id = $1`, "700000001").Scan(&observations); err != nil {
		t.Fatalf("failed to count observations: %v", err)
	}
	if observations != 0 {
		t.Error("enrollment left the id in device_observations")
	}

	// And the heartbeat now works, with the credential.
	w = s.post(t, "/api/heartbeat", fmt.Sprintf(
		`{"id":"700000001","uuid":"uuid-700000001","device_token":%q}`, secret))
	if w.Code != http.StatusOK {
		t.Fatalf("enrolled heartbeat got %d, want 200: %s", w.Code, w.Body.String())
	}

	var connectivity string
	if err := f.db.QueryRow(context.Background(),
		`SELECT connectivity FROM devices WHERE id = $1`, deviceID).Scan(&connectivity); err != nil {
		t.Fatalf("failed to read connectivity: %v", err)
	}
	if connectivity != "ONLINE" {
		t.Errorf("connectivity = %q, want ONLINE", connectivity)
	}
}

// The header is the preferred channel, and has to work as well as the body.
func TestHeartbeatAcceptsCredentialInHeader(t *testing.T) {
	f := newFixture(t)
	s := newDeviceServer(t, f)

	token := f.issueToken(t, nil, nil)
	w := s.post(t, "/api/enroll", fmt.Sprintf(`{"token":%q,"id":"700000002"}`, token.Token))
	if w.Code != http.StatusOK {
		t.Fatalf("enrollment got %d: %s", w.Code, w.Body.String())
	}
	secret := decodeJSON(t, w)["device_token"].(string)

	req := httptest.NewRequest(http.MethodPost, "/api/heartbeat",
		strings.NewReader(`{"id":"700000002"}`))
	req.Header.Set("X-Device-Token", secret)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("heartbeat with the header credential got %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

// A valid credential presented with somebody else's id must not be accepted for
// either device.
func TestHeartbeatRefusesCredentialForAnotherID(t *testing.T) {
	f := newFixture(t)
	s := newDeviceServer(t, f)

	token := f.issueToken(t, nil, nil)
	w := s.post(t, "/api/enroll", fmt.Sprintf(`{"token":%q,"id":"700000003"}`, token.Token))
	if w.Code != http.StatusOK {
		t.Fatalf("enrollment got %d: %s", w.Code, w.Body.String())
	}
	secret := decodeJSON(t, w)["device_token"].(string)

	w = s.post(t, "/api/heartbeat", fmt.Sprintf(
		`{"id":"100000001","device_token":%q}`, secret))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("a credential used with another id got %d, want 401", w.Code)
	}

	// device1 is the id that was claimed; its liveness must be untouched.
	var lastSeen *time.Time
	if err := f.db.QueryRow(context.Background(),
		`SELECT last_seen_at FROM devices WHERE id = $1`, f.device1).Scan(&lastSeen); err != nil {
		t.Fatalf("failed to read device1: %v", err)
	}
	if lastSeen != nil {
		t.Error("a forged heartbeat updated another device's last_seen_at")
	}
}

// Expiry, exhaustion and revocation each have to actually stop a redemption.
// The expiry case is the one that mattered most: the old query rejected tokens
// with no expiry at all, so this covers both directions.
func TestEnrollmentTokenLimits(t *testing.T) {
	f := newFixture(t)
	s := newDeviceServer(t, f)

	t.Run("expired", func(t *testing.T) {
		past := time.Now().Add(-time.Hour)
		token := f.issueToken(t, nil, &past)
		w := s.post(t, "/api/enroll", fmt.Sprintf(`{"token":%q,"id":"710000001"}`, token.Token))
		if w.Code != http.StatusUnauthorized {
			t.Errorf("an expired token got %d, want 401", w.Code)
		}
	})

	t.Run("no expiry is redeemable", func(t *testing.T) {
		token := f.issueToken(t, nil, nil)
		w := s.post(t, "/api/enroll", fmt.Sprintf(`{"token":%q,"id":"710000002"}`, token.Token))
		if w.Code != http.StatusOK {
			t.Errorf("a token with no expiry got %d, want 200: %s", w.Code, w.Body.String())
		}
	})

	t.Run("max uses", func(t *testing.T) {
		once := 1
		token := f.issueToken(t, &once, nil)

		w := s.post(t, "/api/enroll", fmt.Sprintf(`{"token":%q,"id":"710000003"}`, token.Token))
		if w.Code != http.StatusOK {
			t.Fatalf("the first redemption got %d, want 200: %s", w.Code, w.Body.String())
		}

		w = s.post(t, "/api/enroll", fmt.Sprintf(`{"token":%q,"id":"710000004"}`, token.Token))
		if w.Code != http.StatusUnauthorized {
			t.Errorf("the second redemption of a single-use token got %d, want 401", w.Code)
		}
	})

	t.Run("revoked", func(t *testing.T) {
		token := f.issueToken(t, nil, nil)
		svc := enrollment.NewService(f.db, &config.Config{}, fleet.NewService(f.db, &config.Config{}))
		if err := svc.RevokeToken(context.Background(), token.ID); err != nil {
			t.Fatalf("failed to revoke: %v", err)
		}

		w := s.post(t, "/api/enroll", fmt.Sprintf(`{"token":%q,"id":"710000005"}`, token.Token))
		if w.Code != http.StatusUnauthorized {
			t.Errorf("a revoked token got %d, want 401", w.Code)
		}
	})

	t.Run("unknown", func(t *testing.T) {
		w := s.post(t, "/api/enroll", `{"token":"not-a-real-token","id":"710000006"}`)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("an unknown token got %d, want 401", w.Code)
		}
	})
}

// Re-enrollment replaces the credential, which is how a reimaged device comes
// back, and is also what makes the old secret useless.
func TestReenrollmentRotatesTheCredential(t *testing.T) {
	f := newFixture(t)
	s := newDeviceServer(t, f)

	first := f.issueToken(t, nil, nil)
	w := s.post(t, "/api/enroll", fmt.Sprintf(`{"token":%q,"id":"720000001"}`, first.Token))
	if w.Code != http.StatusOK {
		t.Fatalf("first enrollment got %d: %s", w.Code, w.Body.String())
	}
	oldSecret := decodeJSON(t, w)["device_token"].(string)

	second := f.issueToken(t, nil, nil)
	w = s.post(t, "/api/enroll", fmt.Sprintf(`{"token":%q,"id":"720000001"}`, second.Token))
	if w.Code != http.StatusOK {
		t.Fatalf("re-enrollment got %d: %s", w.Code, w.Body.String())
	}
	newSecret := decodeJSON(t, w)["device_token"].(string)

	if oldSecret == newSecret {
		t.Fatal("re-enrollment returned the same secret")
	}

	w = s.post(t, "/api/heartbeat", fmt.Sprintf(`{"id":"720000001","device_token":%q}`, oldSecret))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("the replaced secret still worked: got %d, want 401", w.Code)
	}

	w = s.post(t, "/api/heartbeat", fmt.Sprintf(`{"id":"720000001","device_token":%q}`, newSecret))
	if w.Code != http.StatusOK {
		t.Errorf("the new secret did not work: got %d, want 200", w.Code)
	}
}

// A revoked credential stops the device reporting, which is what makes
// revocation mean something at the device rather than only in the portal.
func TestRevokedCredentialStopsHeartbeat(t *testing.T) {
	f := newFixture(t)
	s := newDeviceServer(t, f)

	token := f.issueToken(t, nil, nil)
	w := s.post(t, "/api/enroll", fmt.Sprintf(`{"token":%q,"id":"730000001"}`, token.Token))
	if w.Code != http.StatusOK {
		t.Fatalf("enrollment got %d: %s", w.Code, w.Body.String())
	}
	secret := decodeJSON(t, w)["device_token"].(string)
	deviceID := uuid.MustParse(decodeJSON(t, w)["device_id"].(string))

	if err := deviceauth.New(f.db).Revoke(context.Background(), deviceID); err != nil {
		t.Fatalf("failed to revoke: %v", err)
	}

	w = s.post(t, "/api/heartbeat", fmt.Sprintf(`{"id":"730000001","device_token":%q}`, secret))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("a revoked device still reported in: got %d, want 401", w.Code)
	}
}

// The control channel: a queued disconnect reaches the device on its next
// heartbeat, exactly once.
func TestHeartbeatDeliversPendingDisconnects(t *testing.T) {
	f := newFixture(t)
	s := newDeviceServer(t, f)

	token := f.issueToken(t, nil, nil)
	w := s.post(t, "/api/enroll", fmt.Sprintf(`{"token":%q,"id":"740000001"}`, token.Token))
	if w.Code != http.StatusOK {
		t.Fatalf("enrollment got %d: %s", w.Code, w.Body.String())
	}
	body := decodeJSON(t, w)
	secret := body["device_token"].(string)
	deviceID := uuid.MustParse(body["device_id"].(string))

	f.mustExec(t, `
		INSERT INTO device_disconnect_requests (device_id, conn_id, requested_by)
		VALUES ($1, 42, $2), ($1, 43, $2)
	`, deviceID, f.adminID)

	w = s.post(t, "/api/heartbeat", fmt.Sprintf(`{"id":"740000001","device_token":%q}`, secret))
	if w.Code != http.StatusOK {
		t.Fatalf("heartbeat got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Disconnect []int32 `json:"disconnect"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode the heartbeat response: %v", err)
	}
	if len(resp.Disconnect) != 2 {
		t.Fatalf("disconnect = %v, want two connection ids", resp.Disconnect)
	}

	// Delivered once: a second heartbeat must not re-kill connections that
	// have already been terminated, or a reused conn id would be dropped.
	w = s.post(t, "/api/heartbeat", fmt.Sprintf(`{"id":"740000001","device_token":%q}`, secret))
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode the second heartbeat: %v", err)
	}
	if len(resp.Disconnect) != 0 {
		t.Errorf("disconnect = %v on the second heartbeat, want empty", resp.Disconnect)
	}
}

// The strategy is sent when the device's version differs, and withheld when it
// matches, so a 15-second heartbeat does not rewrite the client's config every
// 15 seconds.
func TestHeartbeatSendsStrategyOnlyWhenChanged(t *testing.T) {
	f := newFixture(t)
	s := newDeviceServer(t, f)

	token := f.issueToken(t, nil, nil)
	w := s.post(t, "/api/enroll", fmt.Sprintf(`{"token":%q,"id":"750000001"}`, token.Token))
	if w.Code != http.StatusOK {
		t.Fatalf("enrollment got %d: %s", w.Code, w.Body.String())
	}
	body := decodeJSON(t, w)
	secret := body["device_token"].(string)
	deviceID := uuid.MustParse(body["device_id"].(string))

	// Enrollment already seeded the default password policy, so this is an
	// administrator overwriting it rather than the first strategy the device has
	// ever had.
	f.mustExec(t, `
		INSERT INTO device_strategies (device_id, config_options, modified_at, updated_by)
		VALUES ($1, '{"enable-file-transfer":"N"}'::jsonb, 1700000000, $2)
		ON CONFLICT (device_id) DO UPDATE
		SET config_options = EXCLUDED.config_options,
		    modified_at = EXCLUDED.modified_at,
		    updated_by = EXCLUDED.updated_by
	`, deviceID, f.adminID)

	// The device reports modified_at 0, so it has never applied this.
	w = s.post(t, "/api/heartbeat", fmt.Sprintf(
		`{"id":"750000001","device_token":%q,"modified_at":0}`, secret))
	resp := decodeJSON(t, w)
	if resp["strategy"] == nil {
		t.Fatalf("the strategy was withheld from a device that has never applied it: %s", w.Body.String())
	}
	if resp["modified_at"] != float64(1700000000) {
		t.Errorf("modified_at = %v, want 1700000000", resp["modified_at"])
	}

	// Now the device reports the version it has, and must not be sent it again.
	w = s.post(t, "/api/heartbeat", fmt.Sprintf(
		`{"id":"750000001","device_token":%q,"modified_at":1700000000}`, secret))
	resp = decodeJSON(t, w)
	if resp["strategy"] != nil {
		t.Errorf("the strategy was re-sent to a device that already has it: %s", w.Body.String())
	}
}

// sysinfo is the other device endpoint, and had the same hole.
func TestSysinfoRequiresACredential(t *testing.T) {
	f := newFixture(t)
	s := newDeviceServer(t, f)

	w := s.post(t, "/api/sysinfo", `{"id":"100000001","uuid":"u","hostname":"attacker","os":"whatever"}`)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated sysinfo got %d, want 401", w.Code)
	}

	var hostname *string
	if err := f.db.QueryRow(context.Background(),
		`SELECT hostname FROM devices WHERE id = $1`, f.device1).Scan(&hostname); err != nil {
		t.Fatalf("failed to read the device: %v", err)
	}
	if hostname != nil && *hostname == "attacker" {
		t.Error("unauthenticated sysinfo rewrote a device's hostname")
	}
}
