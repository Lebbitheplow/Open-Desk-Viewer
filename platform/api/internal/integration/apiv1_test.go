package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/OpenDeskViewer/platform/api/internal/apiv1"
	"github.com/OpenDeskViewer/platform/api/internal/audit"
	"github.com/OpenDeskViewer/platform/api/internal/config"
	"github.com/OpenDeskViewer/platform/api/internal/fleet"
	"github.com/OpenDeskViewer/platform/api/internal/httpx"
	"github.com/OpenDeskViewer/platform/api/internal/identity"
	"github.com/OpenDeskViewer/platform/api/internal/rustdeskapi"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// v1Server is the /api/v1 surface wired the way cmd/api wires it, so these
// tests exercise the real route table rather than calling handlers directly.
type v1Server struct {
	router *httpx.Mux
	token  string
	// accounts is the stand-in for Keycloak that POST and DELETE
	// /api/v1/users drive. Tests read it to assert the identity provider was
	// actually told, which is the half of removal that a database check cannot
	// see.
	accounts *fakeAccounts
}

// fakeAccounts records what the handler asked the identity provider to do.
//
// A real Keycloak is out of reach here, and the interesting assertions are not
// about Keycloak's behaviour: they are that an account is created before the
// local row, that a failed local insert takes the account back down with it,
// and that removing a user removes the account rather than only the row.
type fakeAccounts struct {
	mu        sync.Mutex
	created   map[string]string // subject -> email
	deleted   []string
	createErr error
	deleteErr error
}

func newFakeAccounts() *fakeAccounts {
	return &fakeAccounts{created: map[string]string{}}
}

func (a *fakeAccounts) CreateAccount(_ context.Context, email, _ string) (identity.Account, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.createErr != nil {
		return identity.Account{}, a.createErr
	}
	subject := "kc-" + email
	a.created[subject] = email
	return identity.Account{Subject: subject, TemporaryPassword: "temp-" + email}, nil
}

func (a *fakeAccounts) DeleteAccount(_ context.Context, subject string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.deleteErr != nil {
		return a.deleteErr
	}
	a.deleted = append(a.deleted, subject)
	delete(a.created, subject)
	return nil
}

func (a *fakeAccounts) wasDeleted(subject string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, s := range a.deleted {
		if s == subject {
			return true
		}
	}
	return false
}

func newV1Server(t *testing.T, f *fixture, userID int64) *v1Server {
	t.Helper()
	zerolog.SetGlobalLevel(zerolog.Disabled)

	cfg := &config.Config{
		AddressBookMaxPeers:       100,
		AuditRetentionDays:        90,
		DeviceStaleAfterSeconds:   300,
		DeviceOfflineAfterSeconds: 600,
	}

	authService := identity.NewAuthService(f.db, "")

	token := fmt.Sprintf("v1-session-%d", userID)
	if _, err := f.db.Exec(context.Background(), `
		INSERT INTO client_sessions (user_id, token_hash, expires_at)
		VALUES ($1, encode(sha256($2::bytea), 'hex'), now() + interval '1 hour')
	`, userID, token); err != nil {
		t.Fatalf("failed to create a session: %v", err)
	}

	accounts := newFakeAccounts()
	handler := apiv1.NewHandler(f.db, fleet.NewService(f.db, cfg), newResolver(f), audit.New(f.db), cfg, newDevicePasswords(t, f), accounts)

	public := httpx.NewRouter(
		httpx.RequestIDMiddleware(),
		httpx.RecoveryMiddleware(),
		httpx.ContextMiddleware(),
	)
	protected := public.Group(httpx.JWTMiddleware(rejectingValidator{}, authService))
	handler.Register(protected)

	return &v1Server{router: public, token: token, accounts: accounts}
}

func (s *v1Server) do(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
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

// listBody is the {total, data} envelope every list endpoint returns.
type listBody struct {
	Total      int64             `json:"total"`
	Current    int64             `json:"current"`
	PageSize   int64             `json:"pageSize"`
	TotalPages int64             `json:"totalPages"`
	Data       []json.RawMessage `json:"data"`
}

func decodeList(t *testing.T, w *httptest.ResponseRecorder) listBody {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var body listBody
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode list body: %v", err)
	}
	return body
}

// An administrator sees the whole fleet, and a technician sees only what their
// support group reaches. This is the property the stubbed handler could not
// have had: it returned an empty list to everyone.
func TestDeviceListIsScopedByAccess(t *testing.T) {
	f := newFixture(t)

	admin := newV1Server(t, f, f.adminID)
	adminList := decodeList(t, admin.do(t, http.MethodGet, "/api/v1/devices?pageSize=100", ""))
	if adminList.Total != 5 {
		t.Errorf("administrator should see all 5 devices, saw total=%d", adminList.Total)
	}
	if len(adminList.Data) != 5 {
		t.Errorf("administrator should receive 5 rows, received %d", len(adminList.Data))
	}

	tech := newV1Server(t, f, f.tech1ID)
	techList := decodeList(t, tech.do(t, http.MethodGet, "/api/v1/devices?pageSize=100", ""))
	// tech1 reaches group1: device1, device2, device3 and the discovered one.
	if techList.Total == adminList.Total {
		t.Errorf("technician should not see the whole fleet, saw total=%d", techList.Total)
	}
	for _, raw := range techList.Data {
		var d struct {
			ID uuid.UUID `json:"id"`
		}
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatalf("failed to decode device: %v", err)
		}
		if d.ID == f.device4 {
			t.Error("technician saw a device belonging to another support group")
		}
	}
}

// The stubbed list ignored its window entirely. A real LIMIT/OFFSET has to
// return different rows per page and cover the whole set across pages.
func TestDeviceListPaginates(t *testing.T) {
	f := newFixture(t)
	s := newV1Server(t, f, f.adminID)

	first := decodeList(t, s.do(t, http.MethodGet, "/api/v1/devices?current=1&pageSize=2", ""))
	second := decodeList(t, s.do(t, http.MethodGet, "/api/v1/devices?current=2&pageSize=2", ""))

	if first.Total != 5 || second.Total != 5 {
		t.Fatalf("total should be the full count on every page, got %d and %d", first.Total, second.Total)
	}
	if first.TotalPages != 3 {
		t.Errorf("expected 3 pages of 2 over 5 devices, got %d", first.TotalPages)
	}
	if len(first.Data) != 2 || len(second.Data) != 2 {
		t.Fatalf("expected 2 rows per page, got %d and %d", len(first.Data), len(second.Data))
	}
	if string(first.Data[0]) == string(second.Data[0]) {
		t.Error("page 2 returned the same first row as page 1, so the offset is not applied")
	}
}

// Search has to actually filter. The stub counted every ACTIVE device and
// returned none of them.
func TestDeviceSearchFilters(t *testing.T) {
	f := newFixture(t)
	s := newV1Server(t, f, f.adminID)

	list := decodeList(t, s.do(t, http.MethodGet, "/api/v1/devices?q=acme-br", ""))
	if list.Total != 1 {
		t.Fatalf("expected exactly one match for acme-br, got %d", list.Total)
	}

	var d struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(list.Data[0], &d); err != nil {
		t.Fatalf("failed to decode device: %v", err)
	}
	if d.Name != "acme-br-01" {
		t.Errorf("expected acme-br-01, got %q", d.Name)
	}

	byState := decodeList(t, s.do(t, http.MethodGet, "/api/v1/devices?state=DISCOVERED", ""))
	if byState.Total != 1 {
		t.Errorf("expected exactly one DISCOVERED device, got %d", byState.Total)
	}
}

// A technician must not reach another support group's device through the
// detail endpoint, and the refusal is 403 rather than 404 so the response does
// not confirm the id exists.
func TestDeviceDetailRefusesForeignDevice(t *testing.T) {
	f := newFixture(t)
	tech := newV1Server(t, f, f.tech1ID)

	w := tech.do(t, http.MethodGet, "/api/v1/devices/"+f.device4.String(), "")
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for a device outside the technician's groups, got %d: %s", w.Code, w.Body.String())
	}

	own := tech.do(t, http.MethodGet, "/api/v1/devices/"+f.device1.String(), "")
	if own.Code != http.StatusOK {
		t.Errorf("expected 200 for the technician's own device, got %d: %s", own.Code, own.Body.String())
	}
}

// Administration endpoints must reject a technician. The previous
// implementation passed a hardcoded user id of 1 to the admin check, so the
// answer did not depend on who was asking.
func TestAdminEndpointsRejectTechnicians(t *testing.T) {
	f := newFixture(t)
	tech := newV1Server(t, f, f.tech1ID)

	for _, path := range []string{
		"/api/v1/customers",
		"/api/v1/device-groups",
		"/api/v1/support-groups",
		"/api/v1/users",
		"/api/v1/audit/events",
	} {
		w := tech.do(t, http.MethodGet, path, "")
		if w.Code != http.StatusForbidden {
			t.Errorf("%s: expected 403 for a technician, got %d: %s", path, w.Code, w.Body.String())
		}
	}
}

// Reassignment is the operation the plan singles out. Moving a device to
// another customer must drop the group memberships that gave the old
// technician access, and it must do so atomically.
func TestReassignmentRevokesPreviousAccess(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// A second customer, and a group only tech2's support group can reach.
	var otherCustomer uuid.UUID
	if err := f.db.QueryRow(ctx,
		`INSERT INTO customers (name, code) VALUES ('Globex', 'GLBX') RETURNING id`).Scan(&otherCustomer); err != nil {
		t.Fatalf("failed to create the second customer: %v", err)
	}

	var branchGroup uuid.UUID
	if err := f.db.QueryRow(ctx, `
		SELECT dg.id FROM device_groups dg
		JOIN support_group_device_groups s ON s.device_group_id = dg.id
		WHERE s.support_group_id = $1`, f.group2).Scan(&branchGroup); err != nil {
		t.Fatalf("failed to find the second support group's device group: %v", err)
	}

	resolver := newResolver(f)

	// Before: tech1 reaches device1, tech2 does not.
	if ok, err := resolver.CanAccessDevice(ctx, f.tech1ID, f.device1); err != nil || !ok {
		t.Fatalf("expected tech1 to reach device1 before reassignment (ok=%v, err=%v)", ok, err)
	}
	if ok, _ := resolver.CanAccessDevice(ctx, f.tech2ID, f.device1); ok {
		t.Fatal("expected tech2 not to reach device1 before reassignment")
	}

	admin := newV1Server(t, f, f.adminID)
	body := fmt.Sprintf(`{"customer_id":%q,"device_group_ids":[%q]}`, otherCustomer, branchGroup)
	w := admin.do(t, http.MethodPost, "/api/v1/devices/"+f.device1.String()+"/reassign", body)
	if w.Code != http.StatusOK {
		t.Fatalf("reassignment failed: %d %s", w.Code, w.Body.String())
	}

	// After: the access has swapped over, which is the whole point.
	if ok, _ := resolver.CanAccessDevice(ctx, f.tech1ID, f.device1); ok {
		t.Error("tech1 still reaches the device after it was reassigned away")
	}
	if ok, err := resolver.CanAccessDevice(ctx, f.tech2ID, f.device1); err != nil || !ok {
		t.Errorf("tech2 should reach the device after reassignment (ok=%v, err=%v)", ok, err)
	}

	var customerID uuid.UUID
	if err := f.db.QueryRow(ctx, `SELECT customer_id FROM devices WHERE id = $1`, f.device1).Scan(&customerID); err != nil {
		t.Fatalf("failed to read the device customer: %v", err)
	}
	if customerID != otherCustomer {
		t.Errorf("device customer is %s, expected %s", customerID, otherCustomer)
	}
}

// Every mutation must leave an audit row naming the actor and the resource.
// The audit_events table was dead weight before this.
func TestMutationsAreAudited(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	admin := newV1Server(t, f, f.adminID)

	var customerID uuid.UUID
	if err := f.db.QueryRow(ctx,
		`INSERT INTO customers (name, code) VALUES ('Initech', 'INIT') RETURNING id`).Scan(&customerID); err != nil {
		t.Fatalf("failed to create a customer: %v", err)
	}

	body := fmt.Sprintf(`{"customer_id":%q}`, customerID)
	if w := admin.do(t, http.MethodPost, "/api/v1/devices/"+f.discovered.String()+"/claim", body); w.Code != http.StatusOK {
		t.Fatalf("claim failed: %d %s", w.Code, w.Body.String())
	}

	var count int64
	if err := f.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM audit_events WHERE event_type = 'device.claimed'`).Scan(&count); err != nil {
		t.Fatalf("failed to count audit events: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly one device.claimed audit row, got %d", count)
	}

	var actorID int64
	var deviceID uuid.UUID
	if err := f.db.QueryRow(ctx, `
		SELECT user_id, device_id FROM audit_events WHERE event_type = 'device.claimed'`).
		Scan(&actorID, &deviceID); err != nil {
		t.Fatalf("failed to read the audit event: %v", err)
	}
	if actorID != f.adminID {
		t.Errorf("audit row names actor %d, expected %d", actorID, f.adminID)
	}
	if deviceID != f.discovered {
		t.Errorf("audit row names device %s, expected %s", deviceID, f.discovered)
	}

	// The claim also has to have taken effect.
	var state string
	if err := f.db.QueryRow(ctx, `SELECT state FROM devices WHERE id = $1`, f.discovered).Scan(&state); err != nil {
		t.Fatalf("failed to read device state: %v", err)
	}
	if state != "ACTIVE" {
		t.Errorf("claimed device is in state %q, expected ACTIVE", state)
	}

	// Claiming twice must not silently succeed: the second attempt would
	// otherwise bypass the reassignment path that revokes old access.
	if w := admin.do(t, http.MethodPost, "/api/v1/devices/"+f.discovered.String()+"/claim", body); w.Code != http.StatusConflict {
		t.Errorf("expected 409 on a second claim, got %d: %s", w.Code, w.Body.String())
	}
}

// The dashboard's numbers have to be counted, not derived from each other. The
// stub reported active_devices as a copy of total_devices.
func TestDashboardCountsAreReal(t *testing.T) {
	f := newFixture(t)
	admin := newV1Server(t, f, f.adminID)

	w := admin.do(t, http.MethodGet, "/api/v1/stats/dashboard", "")
	if w.Code != http.StatusOK {
		t.Fatalf("dashboard failed: %d %s", w.Code, w.Body.String())
	}

	var body struct {
		Stats struct {
			TotalDevices      int64 `json:"total_devices"`
			ActiveDevices     int64 `json:"active_devices"`
			DiscoveredDevices int64 `json:"discovered_devices"`
			TotalCustomers    int64 `json:"total_customers"`
			TotalTechnicians  int64 `json:"total_technicians"`
		} `json:"stats"`
		RecentConnections []json.RawMessage `json:"recent_connections"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode dashboard: %v", err)
	}

	if body.Stats.TotalDevices != 5 {
		t.Errorf("total_devices is %d, expected 5", body.Stats.TotalDevices)
	}
	if body.Stats.ActiveDevices != 4 {
		t.Errorf("active_devices is %d, expected 4", body.Stats.ActiveDevices)
	}
	if body.Stats.DiscoveredDevices != 1 {
		t.Errorf("discovered_devices is %d, expected 1", body.Stats.DiscoveredDevices)
	}
	if body.Stats.ActiveDevices == body.Stats.TotalDevices {
		t.Error("active_devices equals total_devices, which is the stub's behaviour")
	}
	if body.Stats.TotalCustomers != 1 {
		t.Errorf("total_customers is %d, expected 1", body.Stats.TotalCustomers)
	}
	if body.Stats.TotalTechnicians != 2 {
		t.Errorf("total_technicians is %d, expected 2", body.Stats.TotalTechnicians)
	}
	if body.RecentConnections == nil {
		t.Error("recent_connections should be an array, not null")
	}
}

// Customer CRUD has to round trip, and a duplicate code is the caller's error
// rather than an outage.
func TestCustomerCRUD(t *testing.T) {
	f := newFixture(t)
	admin := newV1Server(t, f, f.adminID)

	created := admin.do(t, http.MethodPost, "/api/v1/customers", `{"code":"UMBR","name":"Umbrella"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create failed: %d %s", created.Code, created.Body.String())
	}
	var customer struct {
		ID   uuid.UUID `json:"id"`
		Code string    `json:"code"`
		Name string    `json:"name"`
	}
	if err := json.NewDecoder(created.Body).Decode(&customer); err != nil {
		t.Fatalf("failed to decode the created customer: %v", err)
	}
	if customer.Code != "UMBR" {
		t.Errorf("created customer has code %q", customer.Code)
	}

	duplicate := admin.do(t, http.MethodPost, "/api/v1/customers", `{"code":"UMBR","name":"Umbrella Two"}`)
	if duplicate.Code != http.StatusConflict {
		t.Errorf("expected 409 for a duplicate code, got %d: %s", duplicate.Code, duplicate.Body.String())
	}

	renamed := admin.do(t, http.MethodPatch, "/api/v1/customers/"+customer.ID.String(), `{"name":"Umbrella Corp"}`)
	if renamed.Code != http.StatusOK {
		t.Fatalf("update failed: %d %s", renamed.Code, renamed.Body.String())
	}
	var updated struct {
		Name string `json:"name"`
		Code string `json:"code"`
	}
	if err := json.NewDecoder(renamed.Body).Decode(&updated); err != nil {
		t.Fatalf("failed to decode the updated customer: %v", err)
	}
	if updated.Name != "Umbrella Corp" {
		t.Errorf("name is %q after update", updated.Name)
	}
	if updated.Code != "UMBR" {
		t.Errorf("a PATCH that sent only a name changed the code to %q", updated.Code)
	}

	// A location nested under the customer.
	loc := admin.do(t, http.MethodPost, "/api/v1/customers/"+customer.ID.String()+"/locations", `{"name":"Raccoon City"}`)
	if loc.Code != http.StatusCreated {
		t.Fatalf("location create failed: %d %s", loc.Code, loc.Body.String())
	}
	locations := decodeList(t, admin.do(t, http.MethodGet, "/api/v1/customers/"+customer.ID.String()+"/locations", ""))
	if locations.Total != 1 {
		t.Errorf("expected 1 location, got %d", locations.Total)
	}

	if w := admin.do(t, http.MethodDelete, "/api/v1/customers/"+customer.ID.String(), ""); w.Code != http.StatusOK {
		t.Errorf("delete failed: %d %s", w.Code, w.Body.String())
	}
}

// Granting a device group to a support group is what makes devices appear in a
// technician's address book, so the endpoint has to change the resolver's answer.
func TestSupportGroupGrantChangesAccess(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	admin := newV1Server(t, f, f.adminID)
	resolver := newResolver(f)

	// tech2 cannot reach device1 to begin with.
	if ok, _ := resolver.CanAccessDevice(ctx, f.tech2ID, f.device1); ok {
		t.Fatal("expected tech2 not to reach device1 before the grant")
	}

	var hqGroup uuid.UUID
	if err := f.db.QueryRow(ctx, `
		SELECT device_group_id FROM device_group_members WHERE device_id = $1`, f.device1).Scan(&hqGroup); err != nil {
		t.Fatalf("failed to find the device's group: %v", err)
	}

	body := fmt.Sprintf(`{"device_group_id":%q}`, hqGroup)
	w := admin.do(t, http.MethodPost, "/api/v1/support-groups/"+f.group2.String()+"/device-groups", body)
	if w.Code != http.StatusOK {
		t.Fatalf("grant failed: %d %s", w.Code, w.Body.String())
	}

	if ok, err := resolver.CanAccessDevice(ctx, f.tech2ID, f.device1); err != nil || !ok {
		t.Errorf("tech2 should reach device1 after the grant (ok=%v, err=%v)", ok, err)
	}

	// And revoking takes it away again.
	revoke := admin.do(t, http.MethodDelete,
		"/api/v1/support-groups/"+f.group2.String()+"/device-groups/"+hqGroup.String(), "")
	if revoke.Code != http.StatusOK {
		t.Fatalf("revoke failed: %d %s", revoke.Code, revoke.Body.String())
	}
	if ok, _ := resolver.CanAccessDevice(ctx, f.tech2ID, f.device1); ok {
		t.Error("tech2 still reaches device1 after the grant was revoked")
	}
}

// The user screen has to report roles, and deactivation has to stick.
func TestUserListAndDeactivation(t *testing.T) {
	f := newFixture(t)
	admin := newV1Server(t, f, f.adminID)

	users := decodeList(t, admin.do(t, http.MethodGet, "/api/v1/users?pageSize=50", ""))
	if users.Total != 3 {
		t.Fatalf("expected 3 users, got %d", users.Total)
	}

	var withRoles int
	for _, raw := range users.Data {
		var u struct {
			Roles []string `json:"roles"`
		}
		if err := json.Unmarshal(raw, &u); err != nil {
			t.Fatalf("failed to decode user: %v", err)
		}
		if len(u.Roles) > 0 {
			withRoles++
		}
	}
	if withRoles != 3 {
		t.Errorf("expected every seeded user to report a role, %d did", withRoles)
	}

	path := fmt.Sprintf("/api/v1/users/%d", f.tech1ID)
	if w := admin.do(t, http.MethodPatch, path, `{"active":false}`); w.Code != http.StatusOK {
		t.Fatalf("deactivation failed: %d %s", w.Code, w.Body.String())
	}

	var active bool
	if err := f.db.QueryRow(context.Background(),
		`SELECT active FROM users WHERE id = $1`, f.tech1ID).Scan(&active); err != nil {
		t.Fatalf("failed to read the user: %v", err)
	}
	if active {
		t.Error("user is still active after deactivation")
	}

	// An administrator locking themselves out is a support call, not a feature.
	self := fmt.Sprintf("/api/v1/users/%d", f.adminID)
	if w := admin.do(t, http.MethodPatch, self, `{"active":false}`); w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 when deactivating your own account, got %d", w.Code)
	}
}

// Settings are read-only and come from config, not from constants baked into
// the handler.
func TestSettingsComeFromConfig(t *testing.T) {
	f := newFixture(t)
	admin := newV1Server(t, f, f.adminID)

	w := admin.do(t, http.MethodGet, "/api/v1/settings", "")
	if w.Code != http.StatusOK {
		t.Fatalf("settings failed: %d %s", w.Code, w.Body.String())
	}

	body := decodeMap(t, w)
	if body["audit_retention_days"] != float64(90) {
		t.Errorf("audit_retention_days is %v, expected the configured 90", body["audit_retention_days"])
	}
	if body["device_stale_after_seconds"] != float64(300) {
		t.Errorf("device_stale_after_seconds is %v, expected the configured 300", body["device_stale_after_seconds"])
	}
	if body["editable"] != false {
		t.Error("settings should declare themselves read-only for 1.0")
	}
}

// An unknown method on a registered path has to be a 405, not a 200 that does
// nothing. Method-scoped patterns give this for free; bare ones did not.
func TestUnsupportedMethodIsRejected(t *testing.T) {
	f := newFixture(t)
	admin := newV1Server(t, f, f.adminID)

	w := admin.do(t, http.MethodPut, "/api/v1/devices/"+f.device1.String(), `{}`)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 for PUT on a device, got %d", w.Code)
	}
}

// switch-grant answers a policy question: may this user drive that peer? It
// used to return {"accepted": true} unconditionally, which is the server
// approving every control switch, including onto devices the requester cannot
// see. The answer must track CanAccessDevice.
func TestSwitchGrantFollowsDeviceAccess(t *testing.T) {
	f := newFixture(t)

	server := newSwitchGrantServer(t, f, f.tech1ID)

	// device1 is in tech1's support group; device4 is not.
	granted := server.do(t, http.MethodPost, "/api/switch-grant", `{"peer_id":"100000001"}`)
	if granted.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", granted.Code, granted.Body.String())
	}
	if accepted := decodeMap(t, granted)["accepted"]; accepted != true {
		t.Errorf("expected the switch onto tech1's own device to be accepted, got %v", accepted)
	}

	denied := server.do(t, http.MethodPost, "/api/switch-grant", `{"peer_id":"100000004"}`)
	if denied.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", denied.Code, denied.Body.String())
	}
	if accepted := decodeMap(t, denied)["accepted"]; accepted != false {
		t.Errorf("expected the switch onto another group's device to be denied, got %v", accepted)
	}

	// An unknown peer is a denial, not an error.
	unknown := server.do(t, http.MethodPost, "/api/switch-grant", `{"peer_id":"999999999"}`)
	if unknown.Code != http.StatusOK {
		t.Fatalf("expected 200 for an unknown peer, got %d", unknown.Code)
	}
	if accepted := decodeMap(t, unknown)["accepted"]; accepted != false {
		t.Errorf("expected an unknown peer to be denied, got %v", accepted)
	}
}

func newSwitchGrantServer(t *testing.T, f *fixture, userID int64) *v1Server {
	t.Helper()
	zerolog.SetGlobalLevel(zerolog.Disabled)

	authService := identity.NewAuthService(f.db, "")

	token := fmt.Sprintf("switch-session-%d", userID)
	if _, err := f.db.Exec(context.Background(), `
		INSERT INTO client_sessions (user_id, token_hash, expires_at)
		VALUES ($1, encode(sha256($2::bytea), 'hex'), now() + interval '1 hour')
	`, userID, token); err != nil {
		t.Fatalf("failed to create a session: %v", err)
	}

	handler := rustdeskapi.NewAuditHandler(f.db, newResolver(f), audit.New(f.db), nil)

	public := httpx.NewRouter(
		httpx.RequestIDMiddleware(),
		httpx.RecoveryMiddleware(),
		httpx.ContextMiddleware(),
	)
	protected := public.Group(httpx.JWTMiddleware(rejectingValidator{}, authService))
	protected.HandleFunc("/api/switch-grant", handler.HandleSwitchGrant)

	return &v1Server{router: public, token: token}
}
