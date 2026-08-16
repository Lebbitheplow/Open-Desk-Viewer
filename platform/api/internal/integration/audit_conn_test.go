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
	"github.com/OpenDeskViewer/platform/api/internal/config"
	"github.com/OpenDeskViewer/platform/api/internal/httpx"
	"github.com/OpenDeskViewer/platform/api/internal/identity"
	"github.com/OpenDeskViewer/platform/api/internal/rustdeskapi"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// auditServer is /api/audit/conn and /api/audit/file behind the same middleware
// chain cmd/api puts them behind, so the token is what names the actor.
type auditServer struct {
	router *httpx.Mux
	token  string
}

func newAuditServer(t *testing.T, f *fixture, userID int64) *auditServer {
	t.Helper()
	zerolog.SetGlobalLevel(zerolog.Disabled)

	cfg := &config.Config{PublicHost: "odv.example.com"}
	authService := identity.NewAuthService(f.db, "")

	token := fmt.Sprintf("audit-session-%d", userID)
	if _, err := f.db.Exec(context.Background(), `
		INSERT INTO client_sessions (user_id, token_hash, expires_at)
		VALUES ($1, encode(sha256($2::bytea), 'hex'), now() + interval '1 hour')
	`, userID, token); err != nil {
		t.Fatalf("failed to create a session: %v", err)
	}

	handler := rustdeskapi.NewAuditHandler(f.db, newResolver(f), audit.New(f.db), cfg)

	public := httpx.NewRouter(
		httpx.RequestIDMiddleware(),
		httpx.RecoveryMiddleware(),
		httpx.ContextMiddleware(),
	)
	protected := public.Group(httpx.JWTMiddleware(rejectingValidator{}, authService))
	protected.HandleFunc("/api/audit/conn", handler.HandleAuditConn)
	protected.HandleFunc("/api/audit/file", handler.HandleAuditFile)

	return &auditServer{router: public, token: token}
}

func (s *auditServer) post(t *testing.T, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(http.MethodPost, path, reader)
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	return w
}

// session is one connection_sessions row, read back to check what was stored
// rather than what the response claimed.
type storedSession struct {
	deviceID uuid.UUID
	userID   int64
	status   string
}

func readSessions(t *testing.T, f *fixture) []storedSession {
	t.Helper()

	rows, err := f.db.Query(context.Background(),
		`SELECT device_id, user_id, status FROM connection_sessions ORDER BY start_time`)
	if err != nil {
		t.Fatalf("failed to read sessions: %v", err)
	}
	defer rows.Close()

	var out []storedSession
	for rows.Next() {
		var s storedSession
		if err := rows.Scan(&s.deviceID, &s.userID, &s.status); err != nil {
			t.Fatalf("failed to scan session: %v", err)
		}
		out = append(out, s)
	}
	return out
}

// The defect this covers: user_id came straight from the body, so a technician
// could file a connection record naming somebody else. The claim is now ignored
// and the token decides.
func TestAuditConnIgnoresForgedUserID(t *testing.T) {
	f := newFixture(t)
	s := newAuditServer(t, f, f.tech1ID)

	body := fmt.Sprintf(`{"device_id":%q,"user_id":%d,"status":"STARTED"}`, "100000001", f.adminID)
	w := s.post(t, "/api/audit/conn", body)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	stored := readSessions(t, f)
	if len(stored) != 1 {
		t.Fatalf("expected one session, got %d", len(stored))
	}
	if stored[0].userID != f.tech1ID {
		t.Errorf("session recorded against user %d, want the authenticated %d",
			stored[0].userID, f.tech1ID)
	}
	if stored[0].deviceID != f.device1 {
		t.Errorf("session recorded against device %s, want %s", stored[0].deviceID, f.device1)
	}
	if stored[0].status != "STARTED" {
		t.Errorf("status = %q, want STARTED", stored[0].status)
	}
}

// tech2 is in group2 and device1 is in group1, so this is the IDOR case: the
// caller is authenticated but the device is not theirs.
func TestAuditConnRefusesUnreachableDevice(t *testing.T) {
	f := newFixture(t)
	s := newAuditServer(t, f, f.tech2ID)

	w := s.post(t, "/api/audit/conn", `{"device_id":"100000001"}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
	if len(readSessions(t, f)) != 0 {
		t.Error("a refused request still wrote a connection_sessions row")
	}
}

// An unknown device id must not distinguish itself from an unauthorised one,
// or the endpoint enumerates the fleet for anyone with a token.
func TestAuditConnRefusesUnknownDevice(t *testing.T) {
	f := newFixture(t)
	s := newAuditServer(t, f, f.tech1ID)

	w := s.post(t, "/api/audit/conn", `{"device_id":"999999999"}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

// The stock client names the device by its RustDesk id and reports the
// connection with action rather than status (src/server/connection.rs:1507).
func TestAuditConnAcceptsStockClientShape(t *testing.T) {
	f := newFixture(t)
	s := newAuditServer(t, f, f.tech1ID)

	body := `{"id":"100000002","action":"close","conn_id":7,` +
		`"session_id":18446744073709551615,"ip":"203.0.113.9",` +
		`"nonce":"c0ffee00-0000-0000-0000-000000000000"}`
	w := s.post(t, "/api/audit/conn", body)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		GUID string `json:"guid"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if _, err := uuid.Parse(resp.GUID); err != nil {
		t.Errorf("guid %q is not a uuid: %v", resp.GUID, err)
	}

	stored := readSessions(t, f)
	if len(stored) != 1 {
		t.Fatalf("expected one session, got %d", len(stored))
	}
	if stored[0].deviceID != f.device2 {
		t.Errorf("device = %s, want %s", stored[0].deviceID, f.device2)
	}
	if stored[0].status != "ENDED" {
		t.Errorf("status = %q, want ENDED for action=close", stored[0].status)
	}

	var endTime *string
	if err := f.db.QueryRow(context.Background(),
		`SELECT end_time::text FROM connection_sessions`).Scan(&endTime); err != nil {
		t.Fatalf("failed to read end_time: %v", err)
	}
	if endTime == nil {
		t.Error("a closed session was stored with no end_time")
	}
}

// A status outside the connection_status enum used to reach Postgres and come
// back as a 500. It is a client error.
func TestAuditConnRejectsUnknownStatus(t *testing.T) {
	f := newFixture(t)
	s := newAuditServer(t, f, f.tech1ID)

	w := s.post(t, "/api/audit/conn", `{"device_id":"100000001","status":"BOGUS"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// A support group the caller does not belong to is dropped rather than stored,
// so the connection log cannot be attributed to another team.
func TestAuditConnDropsForeignSupportGroup(t *testing.T) {
	f := newFixture(t)
	s := newAuditServer(t, f, f.tech1ID)

	body := fmt.Sprintf(`{"device_id":"100000001","support_group_id":%q}`, f.group2)
	w := s.post(t, "/api/audit/conn", body)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var groupID *uuid.UUID
	if err := f.db.QueryRow(context.Background(),
		`SELECT support_group_id FROM connection_sessions`).Scan(&groupID); err != nil {
		t.Fatalf("failed to read support_group_id: %v", err)
	}
	if groupID != nil {
		t.Errorf("support_group_id = %s, want null", groupID)
	}
}

// /api/audit/file had the same two defects and gets the same rules.
func TestAuditFileAuthorizesDevice(t *testing.T) {
	f := newFixture(t)

	denied := newAuditServer(t, f, f.tech2ID)
	w := denied.post(t, "/api/audit/file",
		`{"device_id":"100000001","file_name":"payroll.xlsx","direction":"send"}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for a device outside the caller's groups, got %d", w.Code)
	}

	allowed := newAuditServer(t, f, f.tech1ID)
	w = allowed.post(t, "/api/audit/file",
		`{"device_id":"100000001","file_name":"payroll.xlsx","direction":"send","size":4096}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var actor int64
	var device uuid.UUID
	if err := f.db.QueryRow(context.Background(), `
		SELECT user_id, device_id FROM audit_events WHERE event_type = 'file_transfer'
	`).Scan(&actor, &device); err != nil {
		t.Fatalf("failed to read the audit event: %v", err)
	}
	if actor != f.tech1ID {
		t.Errorf("audit event actor = %d, want %d", actor, f.tech1ID)
	}
	if device != f.device1 {
		t.Errorf("audit event device = %s, want %s", device, f.device1)
	}
}

// The audit log is append-only: the application may add to it and may not edit
// or remove what is there. 2.1 stopped the HTTP surface accepting a forged
// entry; this is what stops an entry being altered after the fact.
func TestAuditEventsAreAppendOnly(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	f.mustExec(t, `
		INSERT INTO audit_events (event_type, user_id, description)
		VALUES ('device.connect_requested', $1, 'original')
	`, f.adminID)

	if _, err := f.db.Exec(ctx,
		`UPDATE audit_events SET description = 'rewritten'`); err == nil {
		t.Error("an audit event was rewritten")
	}

	if _, err := f.db.Exec(ctx, `DELETE FROM audit_events`); err == nil {
		t.Error("an audit event was deleted")
	}

	var description string
	if err := f.db.QueryRow(ctx,
		`SELECT description FROM audit_events`).Scan(&description); err != nil {
		t.Fatalf("failed to read the event back: %v", err)
	}
	if description != "original" {
		t.Errorf("description = %q, want the original text", description)
	}

	// The retention pass is the one legitimate deleter, and only inside a
	// transaction that announces itself.
	tx, err := f.db.Tx(ctx)
	if err != nil {
		t.Fatalf("failed to begin: %v", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `SET LOCAL odv.audit_retention = 'on'`); err != nil {
		t.Fatalf("failed to mark the transaction: %v", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM audit_events`); err != nil {
		t.Fatalf("the retention pass could not delete: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	var remaining int
	if err := f.db.QueryRow(ctx, `SELECT COUNT(*) FROM audit_events`).Scan(&remaining); err != nil {
		t.Fatalf("failed to count: %v", err)
	}
	if remaining != 0 {
		t.Errorf("%d events survived the retention pass", remaining)
	}
}
