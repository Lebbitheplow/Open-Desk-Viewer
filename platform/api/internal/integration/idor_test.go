package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/OpenDeskViewer/platform/api/internal/apiv1"
	"github.com/google/uuid"
)

// The IDOR sweep: one case per /api/v1 route, driven as a technician who has no
// claim on the resource named in the path.
//
// The point is not that any single handler refuses. It is that *every* route
// does, and that a route added later cannot quietly skip the check: the
// completeness guard below fails if a pattern in apiv1.Handler.Routes() has no
// case here.
//
// Two different refusals are being asserted, and they are not the same thing:
//
//   - administration routes refuse a technician outright, whatever id is in the
//     path, because managing customers and groups is not a technician's job;
//   - device routes refuse *this* device, because it belongs to another support
//     group, while the same call against the technician's own device succeeds.
//
// A 403 on both is only half the property. The other half is that nothing
// changed, which TestForeignRequestsChangeNothing checks.

// idorCase is one route exercised against a resource the caller has no claim on.
type idorCase struct {
	// pattern is the apiv1 route this covers, verbatim, so the completeness
	// guard can match it.
	pattern string
	method  string
	path    string
	body    string
	// want is the status a technician must get. Scoped list endpoints answer
	// 200 and filter their rows instead; those carry wantScoped and are
	// asserted separately.
	want       int
	wantScoped bool
}

func idorCases(f *fixture) []idorCase {
	dev := f.device4.String()      // another support group's device
	sg := f.group2.String()        // another support group
	dg := f.deviceGroup2.String()  // its device group
	cust := f.customer.String()    // the only customer; admin-only either way
	loc := uuid.NewString()        // never resolved: the gate is hit first
	other := fmt.Sprint(f.tech2ID) // another user
	member := f.device4.String()   // a member of the foreign device group

	return []idorCase{
		{pattern: "GET /api/v1/stats/dashboard", method: http.MethodGet, path: "/api/v1/stats/dashboard", want: http.StatusOK, wantScoped: true},

		{pattern: "GET /api/v1/devices", method: http.MethodGet, path: "/api/v1/devices?pageSize=100", want: http.StatusOK, wantScoped: true},
		{pattern: "GET /api/v1/devices/{id}", method: http.MethodGet, path: "/api/v1/devices/" + dev, want: http.StatusForbidden},
		{pattern: "PATCH /api/v1/devices/{id}", method: http.MethodPatch, path: "/api/v1/devices/" + dev, body: `{"name":"stolen"}`, want: http.StatusForbidden},
		{pattern: "DELETE /api/v1/devices/{id}", method: http.MethodDelete, path: "/api/v1/devices/" + dev, want: http.StatusForbidden},
		{pattern: "POST /api/v1/devices/{id}/claim", method: http.MethodPost, path: "/api/v1/devices/" + dev + "/claim", body: `{"customer_id":"` + cust + `"}`, want: http.StatusForbidden},
		{pattern: "POST /api/v1/devices/{id}/reassign", method: http.MethodPost, path: "/api/v1/devices/" + dev + "/reassign", body: `{"customer_id":"` + cust + `"}`, want: http.StatusForbidden},
		{pattern: "POST /api/v1/devices/{id}/connect", method: http.MethodPost, path: "/api/v1/devices/" + dev + "/connect", want: http.StatusForbidden},
		{pattern: "GET /api/v1/devices/{id}/sessions", method: http.MethodGet, path: "/api/v1/devices/" + dev + "/sessions", want: http.StatusForbidden},

		{pattern: "POST /api/v1/devices/{id}/disconnect", method: http.MethodPost, path: "/api/v1/devices/" + dev + "/disconnect", body: `{"conn_ids":[7]}`, want: http.StatusForbidden},
		{pattern: "GET /api/v1/devices/{id}/strategy", method: http.MethodGet, path: "/api/v1/devices/" + dev + "/strategy", want: http.StatusForbidden},
		{pattern: "PUT /api/v1/devices/{id}/strategy", method: http.MethodPut, path: "/api/v1/devices/" + dev + "/strategy", body: `{"config_options":{"enable-audio":"N"}}`, want: http.StatusForbidden},

		// The credential itself. This is the case the whole sweep exists for:
		// every other refusal keeps a technician out of a screen, and this one
		// keeps them off a machine.
		{pattern: "GET /api/v1/devices/{id}/password", method: http.MethodGet, path: "/api/v1/devices/" + dev + "/password", want: http.StatusForbidden},
		{pattern: "POST /api/v1/devices/{id}/password/rotate", method: http.MethodPost, path: "/api/v1/devices/" + dev + "/password/rotate", want: http.StatusForbidden},

		{pattern: "GET /api/v1/device-observations", method: http.MethodGet, path: "/api/v1/device-observations", want: http.StatusForbidden},

		// Monitoring. The per-device history is scoped like the device itself;
		// the fleet-wide view and the notification surface are administration.
		{pattern: "GET /api/v1/devices/{id}/connectivity", method: http.MethodGet, path: "/api/v1/devices/" + dev + "/connectivity", want: http.StatusForbidden},
		{pattern: "GET /api/v1/monitoring/events", method: http.MethodGet, path: "/api/v1/monitoring/events", want: http.StatusForbidden},
		{pattern: "GET /api/v1/notification-targets", method: http.MethodGet, path: "/api/v1/notification-targets", want: http.StatusForbidden},
		{pattern: "POST /api/v1/notification-targets", method: http.MethodPost, path: "/api/v1/notification-targets", body: `{"name":"Injected","url":"https://attacker.example/hook"}`, want: http.StatusForbidden},
		{pattern: "DELETE /api/v1/notification-targets/{id}", method: http.MethodDelete, path: "/api/v1/notification-targets/" + uuid.NewString(), want: http.StatusForbidden},
		{pattern: "GET /api/v1/notification-deliveries", method: http.MethodGet, path: "/api/v1/notification-deliveries", want: http.StatusForbidden},

		{pattern: "GET /api/v1/customers", method: http.MethodGet, path: "/api/v1/customers", want: http.StatusForbidden},
		{pattern: "POST /api/v1/customers", method: http.MethodPost, path: "/api/v1/customers", body: `{"code":"IDOR","name":"Injected"}`, want: http.StatusForbidden},
		{pattern: "GET /api/v1/customers/{id}", method: http.MethodGet, path: "/api/v1/customers/" + cust, want: http.StatusForbidden},
		{pattern: "PATCH /api/v1/customers/{id}", method: http.MethodPatch, path: "/api/v1/customers/" + cust, body: `{"name":"Renamed"}`, want: http.StatusForbidden},
		{pattern: "DELETE /api/v1/customers/{id}", method: http.MethodDelete, path: "/api/v1/customers/" + cust, want: http.StatusForbidden},
		{pattern: "GET /api/v1/customers/{id}/locations", method: http.MethodGet, path: "/api/v1/customers/" + cust + "/locations", want: http.StatusForbidden},
		{pattern: "POST /api/v1/customers/{id}/locations", method: http.MethodPost, path: "/api/v1/customers/" + cust + "/locations", body: `{"name":"Injected"}`, want: http.StatusForbidden},
		{pattern: "PATCH /api/v1/customers/{id}/locations/{locationId}", method: http.MethodPatch, path: "/api/v1/customers/" + cust + "/locations/" + loc, body: `{"name":"Renamed"}`, want: http.StatusForbidden},
		{pattern: "DELETE /api/v1/customers/{id}/locations/{locationId}", method: http.MethodDelete, path: "/api/v1/customers/" + cust + "/locations/" + loc, want: http.StatusForbidden},

		{pattern: "GET /api/v1/device-groups", method: http.MethodGet, path: "/api/v1/device-groups", want: http.StatusForbidden},
		{pattern: "POST /api/v1/device-groups", method: http.MethodPost, path: "/api/v1/device-groups", body: `{"name":"Injected"}`, want: http.StatusForbidden},
		{pattern: "GET /api/v1/device-groups/{id}", method: http.MethodGet, path: "/api/v1/device-groups/" + dg, want: http.StatusForbidden},
		{pattern: "PATCH /api/v1/device-groups/{id}", method: http.MethodPatch, path: "/api/v1/device-groups/" + dg, body: `{"name":"Renamed"}`, want: http.StatusForbidden},
		{pattern: "DELETE /api/v1/device-groups/{id}", method: http.MethodDelete, path: "/api/v1/device-groups/" + dg, want: http.StatusForbidden},
		{pattern: "GET /api/v1/device-groups/{id}/members", method: http.MethodGet, path: "/api/v1/device-groups/" + dg + "/members", want: http.StatusForbidden},
		{pattern: "POST /api/v1/device-groups/{id}/members", method: http.MethodPost, path: "/api/v1/device-groups/" + dg + "/members", body: `{"device_id":"` + f.device1.String() + `"}`, want: http.StatusForbidden},
		{pattern: "DELETE /api/v1/device-groups/{id}/members/{deviceId}", method: http.MethodDelete, path: "/api/v1/device-groups/" + dg + "/members/" + member, want: http.StatusForbidden},

		{pattern: "GET /api/v1/support-groups", method: http.MethodGet, path: "/api/v1/support-groups", want: http.StatusForbidden},
		{pattern: "POST /api/v1/support-groups", method: http.MethodPost, path: "/api/v1/support-groups", body: `{"name":"Injected"}`, want: http.StatusForbidden},
		{pattern: "GET /api/v1/support-groups/{id}", method: http.MethodGet, path: "/api/v1/support-groups/" + sg, want: http.StatusForbidden},
		{pattern: "PATCH /api/v1/support-groups/{id}", method: http.MethodPatch, path: "/api/v1/support-groups/" + sg, body: `{"name":"Renamed"}`, want: http.StatusForbidden},
		{pattern: "DELETE /api/v1/support-groups/{id}", method: http.MethodDelete, path: "/api/v1/support-groups/" + sg, want: http.StatusForbidden},
		// The most valuable one in the file: a technician granting themselves
		// membership of the group that reaches every other customer's devices.
		{pattern: "POST /api/v1/support-groups/{id}/technicians", method: http.MethodPost, path: "/api/v1/support-groups/" + sg + "/technicians", body: fmt.Sprintf(`{"user_id":%d}`, f.tech1ID), want: http.StatusForbidden},
		{pattern: "DELETE /api/v1/support-groups/{id}/technicians/{userId}", method: http.MethodDelete, path: "/api/v1/support-groups/" + sg + "/technicians/" + other, want: http.StatusForbidden},
		// And the same escalation from the other direction: attaching the
		// foreign device group to the group the technician is already in.
		{pattern: "POST /api/v1/support-groups/{id}/device-groups", method: http.MethodPost, path: "/api/v1/support-groups/" + f.group1.String() + "/device-groups", body: `{"device_group_id":"` + dg + `"}`, want: http.StatusForbidden},
		{pattern: "DELETE /api/v1/support-groups/{id}/device-groups/{groupId}", method: http.MethodDelete, path: "/api/v1/support-groups/" + sg + "/device-groups/" + dg, want: http.StatusForbidden},

		{pattern: "GET /api/v1/users", method: http.MethodGet, path: "/api/v1/users", want: http.StatusForbidden},
		// A technician creating an account is the escalation that needs no
		// device at all: one call and they have an Administrator of their own.
		{pattern: "POST /api/v1/users", method: http.MethodPost, path: "/api/v1/users", body: `{"email":"injected@example.com","display_name":"Injected","role":"Administrator"}`, want: http.StatusForbidden},
		{pattern: "GET /api/v1/users/{id}", method: http.MethodGet, path: "/api/v1/users/" + other, want: http.StatusForbidden},
		{pattern: "PATCH /api/v1/users/{id}", method: http.MethodPatch, path: "/api/v1/users/" + other, body: `{"active":false}`, want: http.StatusForbidden},
		{pattern: "DELETE /api/v1/users/{id}", method: http.MethodDelete, path: "/api/v1/users/" + other, want: http.StatusForbidden},
		{pattern: "POST /api/v1/users/{id}/roles", method: http.MethodPost, path: fmt.Sprintf("/api/v1/users/%d/roles", f.tech1ID), body: `{"role":"Administrator"}`, want: http.StatusForbidden},
		{pattern: "DELETE /api/v1/users/{id}/roles/{role}", method: http.MethodDelete, path: "/api/v1/users/" + other + "/roles/Technician", want: http.StatusForbidden},

		{pattern: "GET /api/v1/audit/events", method: http.MethodGet, path: "/api/v1/audit/events", want: http.StatusForbidden},
		{pattern: "GET /api/v1/reports/{report}", method: http.MethodGet, path: "/api/v1/reports/access-review", want: http.StatusForbidden},
		{pattern: "GET /api/v1/audit/sessions", method: http.MethodGet, path: "/api/v1/audit/sessions?pageSize=100", want: http.StatusOK, wantScoped: true},

		// Settings are deployment-wide and carry no per-resource data, so a
		// technician reading them is not an IDOR. Listed so the completeness
		// guard stays honest rather than silently excluding it.
		{pattern: "GET /api/v1/settings", method: http.MethodGet, path: "/api/v1/settings", want: http.StatusOK},
	}
}

func TestTechnicianIsRefusedEveryForeignResource(t *testing.T) {
	f := newFixture(t)
	tech := newV1Server(t, f, f.tech1ID)

	for _, tc := range idorCases(f) {
		t.Run(tc.pattern, func(t *testing.T) {
			w := tech.do(t, tc.method, tc.path, tc.body)
			if w.Code != tc.want {
				t.Errorf("%s: expected %d, got %d: %s", tc.pattern, tc.want, w.Code, w.Body.String())
			}
		})
	}
}

// The scoped endpoints answer 200 to anybody, so the only evidence they scope at
// all is that the answer depends on who asked. tech1 reaches three devices and
// tech2 reaches one, so an identical body means the caller is not being consulted
// - which is exactly the bug this surface used to have, when the admin check was
// handed a hardcoded user id of 1.
func TestScopedEndpointsAnswerDifferentlyPerCaller(t *testing.T) {
	f := newFixture(t)
	seedSessions(t, f)
	tech1 := newV1Server(t, f, f.tech1ID)
	tech2 := newV1Server(t, f, f.tech2ID)

	var scoped int
	for _, tc := range idorCases(f) {
		if !tc.wantScoped {
			continue
		}
		scoped++
		t.Run(tc.pattern, func(t *testing.T) {
			first := tech1.do(t, tc.method, tc.path, tc.body)
			second := tech2.do(t, tc.method, tc.path, tc.body)

			if first.Code != http.StatusOK || second.Code != http.StatusOK {
				t.Fatalf("expected 200 for both technicians, got %d and %d", first.Code, second.Code)
			}
			if first.Body.String() == second.Body.String() {
				t.Errorf("%s returned an identical body to two technicians with different access, so it is not scoped to the caller", tc.pattern)
			}
		})
	}
	if scoped == 0 {
		t.Fatal("no case is marked wantScoped, so this test asserted nothing")
	}
}

// Every route in the table has to exist, and every route that exists has to be
// in the table. Without the second half, adding a route is enough to add an
// unchecked one.
func TestIDORSweepCoversEveryRoute(t *testing.T) {
	f := newFixture(t)

	covered := map[string]bool{}
	for _, tc := range idorCases(f) {
		if covered[tc.pattern] {
			t.Errorf("duplicate case for %s", tc.pattern)
		}
		covered[tc.pattern] = true
	}

	// Routes() reads no field of the handler, so a zero handler is enough to
	// ask it for the table.
	registered := map[string]bool{}
	for _, route := range apiv1.NewHandler(nil, nil, nil, nil, nil, nil, nil).Routes() {
		registered[route.Pattern] = true
		if !covered[route.Pattern] {
			t.Errorf("route %s has no IDOR case; add one to idorCases", route.Pattern)
		}
	}
	for pattern := range covered {
		if !registered[pattern] {
			t.Errorf("idorCases covers %s, which is not a registered route", pattern)
		}
	}
}

// A refusal that still writes is not a refusal. This runs the whole sweep and
// then checks the world is where it started.
func TestForeignRequestsChangeNothing(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	tech := newV1Server(t, f, f.tech1ID)

	before := snapshot(t, f)

	for _, tc := range idorCases(f) {
		tech.do(t, tc.method, tc.path, tc.body)
	}

	after := snapshot(t, f)
	for key, want := range before {
		if after[key] != want {
			t.Errorf("%s changed from %d to %d after the refused requests", key, want, after[key])
		}
	}

	// The specific escalations, named rather than counted, because a count can
	// stay level while a membership moves.
	var inForeignGroup bool
	if err := f.db.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM user_support_groups WHERE user_id = $1 AND support_group_id = $2)
	`, f.tech1ID, f.group2).Scan(&inForeignGroup); err != nil {
		t.Fatalf("failed to check support group membership: %v", err)
	}
	if inForeignGroup {
		t.Error("the technician joined the support group they were refused")
	}

	var isAdmin bool
	if err := f.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM user_roles ur JOIN roles r ON r.id = ur.role_id
			WHERE ur.user_id = $1 AND r.name = 'Administrator')
	`, f.tech1ID).Scan(&isAdmin); err != nil {
		t.Fatalf("failed to check roles: %v", err)
	}
	if isAdmin {
		t.Error("the technician granted themselves the Administrator role")
	}

	// And access itself, which is the thing all of the above is protecting.
	if ok, _ := newResolver(f).CanAccessDevice(ctx, f.tech1ID, f.device4); ok {
		t.Error("the technician can reach the foreign device after the sweep")
	}
}

// seedSessions puts one connection session on each technician's own device, so
// the scoped list endpoints have something to include and something to withhold.
// Without it an empty list looks identical to a correctly filtered one.
func seedSessions(t *testing.T, f *fixture) {
	t.Helper()
	ctx := context.Background()

	if _, err := f.db.Exec(ctx, `
		INSERT INTO connection_sessions (device_id, user_id, start_time, status, client_id)
		VALUES ($1, $2, now(), 'STARTED', '900/4'),
		       ($3, $4, now(), 'STARTED', '901/5')
	`, f.device4, f.tech2ID, f.device1, f.tech1ID); err != nil {
		t.Fatalf("failed to seed connection sessions: %v", err)
	}
}

// counts is the state the sweep must not move.
func snapshot(t *testing.T, f *fixture) map[string]int64 {
	t.Helper()
	ctx := context.Background()

	counts := map[string]int64{}
	for _, table := range []string{
		"devices", "customers", "locations", "device_groups", "support_groups",
		"device_group_members", "support_group_device_groups", "user_support_groups",
		"user_roles", "users", "device_strategies", "device_disconnect_requests",
		// A refused rotate must not have created a password row, and a refused
		// read must not have created one either: either would mean the handler
		// touched the credential before deciding it was allowed to.
		"device_passwords",
		// A refused POST must not have registered a webhook. This is the one
		// route on the surface that would send fleet data to an address the
		// caller chose, so it is worth counting explicitly.
		"notification_targets",
	} {
		var n int64
		if err := f.db.QueryRow(ctx, `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
			t.Fatalf("failed to count %s: %v", table, err)
		}
		counts[table] = n
	}
	return counts
}

// The two endpoints that answer 200 to a technician have to answer it with
// somebody else's rows removed, not present. A 200 is where a scoping bug hides.
func TestScopedListsExcludeForeignRows(t *testing.T) {
	f := newFixture(t)
	seedSessions(t, f)
	tech := newV1Server(t, f, f.tech1ID)

	devices := decodeList(t, tech.do(t, http.MethodGet, "/api/v1/devices?pageSize=100", ""))
	for _, raw := range devices.Data {
		var d struct {
			ID uuid.UUID `json:"id"`
		}
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatalf("failed to decode device: %v", err)
		}
		if d.ID == f.device4 {
			t.Error("/api/v1/devices leaked a device from another support group")
		}
	}

	sessions := decodeList(t, tech.do(t, http.MethodGet, "/api/v1/audit/sessions?pageSize=100", ""))
	if sessions.Total != 1 {
		t.Errorf("expected the technician to see exactly their own device's session, got total=%d", sessions.Total)
	}
	for _, raw := range sessions.Data {
		var s struct {
			DeviceID *uuid.UUID `json:"device_id"`
		}
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatalf("failed to decode session: %v", err)
		}
		if s.DeviceID != nil && *s.DeviceID == f.device4 {
			t.Error("/api/v1/audit/sessions leaked a session on another support group's device")
		}
	}

	// Filtering by the foreign device id explicitly must not be a way around
	// the scope: the filter narrows, it does not widen.
	filtered := decodeList(t, tech.do(t, http.MethodGet, "/api/v1/audit/sessions?device="+f.device4.String(), ""))
	if filtered.Total != 0 {
		t.Errorf("filtering by a foreign device returned %d sessions, expected 0", filtered.Total)
	}

	// The dashboard counts the same scoped set.
	w := tech.do(t, http.MethodGet, "/api/v1/stats/dashboard", "")
	var dashboard struct {
		Stats struct {
			TotalDevices int64 `json:"total_devices"`
		} `json:"stats"`
	}
	if err := json.NewDecoder(w.Body).Decode(&dashboard); err != nil {
		t.Fatalf("failed to decode the dashboard: %v", err)
	}
	if dashboard.Stats.TotalDevices != 3 {
		t.Errorf("dashboard total_devices is %d for a technician who reaches 3 devices", dashboard.Stats.TotalDevices)
	}

	// The same technician on their own device is a 200, which is what makes the
	// 403s above evidence of scoping rather than of a broken route table.
	own := tech.do(t, http.MethodGet, "/api/v1/devices/"+f.device1.String()+"/sessions", "")
	if own.Code != http.StatusOK {
		t.Errorf("expected 200 on the technician's own device sessions, got %d: %s", own.Code, own.Body.String())
	}
}
