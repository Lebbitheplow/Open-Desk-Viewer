package integration

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/OpenDeskViewer/platform/api/internal/apiv1"
)

// The Read Only role, item 6.9.
//
// It was seeded by migration 000001 and referenced by no code: the portal
// offered it, granting it did nothing, and a user holding only it got 403 from
// every screen. A role that grants nothing is worse than an absent one, because
// somebody assigns it and believes they have given access.
//
// What it now means is fleet-wide read, no writes, no remote access, no
// credentials. The three tests below are one per clause, and the middle one is
// the important one: it is driven from the route table, so a route added later
// cannot quietly become writable by an auditor.

// readOnlyUser adds a user holding only the Read Only role and returns a server
// authenticated as them.
func readOnlyServer(t *testing.T, f *fixture) *v1Server {
	t.Helper()

	id := f.newUser(t, "sub-auditor", "auditor@example.com", apiv1.RoleReadOnly)
	return newV1Server(t, f, id)
}

// Fleet-wide read. A Read Only user belongs to no support group, so before this
// every one of these answered 403 or an empty list.
func TestReadOnlySeesTheWholeFleet(t *testing.T) {
	f := newFixture(t)
	auditor := readOnlyServer(t, f)

	t.Run("device list", func(t *testing.T) {
		w := auditor.do(t, http.MethodGet, "/api/v1/devices?pageSize=100", "")
		if w.Code != http.StatusOK {
			t.Fatalf("got %d: %s", w.Code, w.Body.String())
		}
		page := decodeList(t, w)
		// Five devices are seeded, across two device groups that no single
		// technician reaches. An auditor scoped to a support group would see
		// none of them.
		if page.Total < 5 {
			t.Errorf("the auditor sees %d devices; the fixture seeds 5 across two groups", page.Total)
		}
	})

	for _, path := range []string{
		"/api/v1/stats/dashboard",
		"/api/v1/customers",
		"/api/v1/device-groups",
		"/api/v1/support-groups",
		"/api/v1/users",
		"/api/v1/audit/events",
		"/api/v1/device-observations",
		"/api/v1/settings",
	} {
		t.Run(path, func(t *testing.T) {
			if w := auditor.do(t, http.MethodGet, path, ""); w.Code != http.StatusOK {
				t.Errorf("got %d: %s", w.Code, w.Body.String())
			}
		})
	}

	t.Run("a device in a group no technician of theirs reaches", func(t *testing.T) {
		if w := auditor.do(t, http.MethodGet, "/api/v1/devices/"+f.device4.String(), ""); w.Code != http.StatusOK {
			t.Errorf("got %d: %s", w.Code, w.Body.String())
		}
	})
}

// The completeness half. Driven from apiv1.Handler.Routes() rather than a hand
// written list, so a mutating route added later is covered the day it is added.
func TestReadOnlyCannotWriteAnything(t *testing.T) {
	f := newFixture(t)
	auditor := readOnlyServer(t, f)

	before := snapshot(t, f)

	mutating := 0
	for _, route := range apiv1.NewHandler(nil, nil, nil, nil, nil, nil, nil).Routes() {
		if !route.Mutating {
			continue
		}
		mutating++

		method, pattern, _ := strings.Cut(route.Pattern, " ")
		path := concretePath(f, pattern)

		t.Run(route.Pattern, func(t *testing.T) {
			w := auditor.do(t, method, path, mutatingBody(pattern))
			if w.Code != http.StatusForbidden {
				t.Errorf("a Read Only user got %d from %s, want 403: %s", w.Code, route.Pattern, w.Body.String())
			}
		})
	}

	if mutating == 0 {
		t.Fatal("no route is marked mutating, so this test asserted nothing")
	}

	// A 403 is half a test; the other half is that nothing moved. Same reasoning
	// as TestForeignRequestsChangeNothing.
	after := snapshot(t, f)
	for key, want := range before {
		if after[key] != want {
			t.Errorf("%s changed from %d to %d after the refused writes", key, want, after[key])
		}
	}
}

// No remote access and no credentials. These are reads, so they are not covered
// by the sweep above, and they are the two that matter most: an auditor who
// could read a device password would have exactly the access the role exists to
// withhold.
func TestReadOnlyGetsNoWayOntoAMachine(t *testing.T) {
	f := newFixture(t)
	s := newDeviceServer(t, f)
	auditor := readOnlyServer(t, f)

	const id = "900000001"
	secret, deviceID := enrollDevice(t, f, s, id)
	s.heartbeat(t, id, secret, 0) // give the device a password to refuse

	t.Run("may read the device record", func(t *testing.T) {
		if w := auditor.do(t, http.MethodGet, "/api/v1/devices/"+deviceID.String(), ""); w.Code != http.StatusOK {
			t.Errorf("got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("may not read its connection password", func(t *testing.T) {
		w := auditor.do(t, http.MethodGet, "/api/v1/devices/"+deviceID.String()+"/password", "")
		if w.Code != http.StatusForbidden {
			t.Errorf("got %d, want 403: %s", w.Code, w.Body.String())
		}
	})

	t.Run("may not start a connection", func(t *testing.T) {
		w := auditor.do(t, http.MethodPost, "/api/v1/devices/"+deviceID.String()+"/connect", "")
		if w.Code != http.StatusForbidden {
			t.Errorf("got %d, want 403: %s", w.Code, w.Body.String())
		}
	})
}

// A Read Only user must not appear to the RustDesk client as somebody who may
// connect. The portal's view widened; access.Resolver deliberately did not.
func TestReadOnlyIsNotGrantedDeviceAccessByTheResolver(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	id := f.newUser(t, "sub-auditor2", "auditor2@example.com", apiv1.RoleReadOnly)

	if ok, err := newResolver(f).CanAccessDevice(ctx, id, f.device1); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Error("the resolver grants a Read Only user device access, so the device would appear in their address book as connectable")
	}

	devices, err := newResolver(f).GetAccessibleDevices(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 0 {
		t.Errorf("the resolver returns %d accessible devices for a Read Only user", len(devices))
	}
}

// concretePath fills a route pattern with ids from the fixture. Ids that are
// never reached, because the role gate is hit first, are left as plausible
// values rather than resolved.
func concretePath(f *fixture, pattern string) string {
	r := strings.NewReplacer(
		"{id}", f.device4.String(),
		"{locationId}", "00000000-0000-0000-0000-000000000001",
		"{deviceId}", f.device4.String(),
		"{groupId}", f.deviceGroup2.String(),
		"{userId}", "1",
		"{role}", "Technician",
	)
	path := r.Replace(pattern)

	// The {id} of a group route is a group, not a device.
	switch {
	case strings.HasPrefix(pattern, "/api/v1/device-groups/"):
		path = strings.Replace(path, f.device4.String(), f.deviceGroup2.String(), 1)
	case strings.HasPrefix(pattern, "/api/v1/support-groups/"):
		path = strings.Replace(path, f.device4.String(), f.group2.String(), 1)
	case strings.HasPrefix(pattern, "/api/v1/customers/"):
		path = strings.Replace(path, f.device4.String(), f.customer.String(), 1)
	case strings.HasPrefix(pattern, "/api/v1/users/"):
		path = strings.Replace(path, f.device4.String(), "1", 1)
	}
	return path
}

// mutatingBody supplies a body for the routes that require one, so a refusal is
// the role gate rather than a 400 that would pass this test for the wrong
// reason.
func mutatingBody(pattern string) string {
	switch {
	case strings.Contains(pattern, "/customers/{id}/locations"):
		return `{"name":"Injected"}`
	case strings.HasSuffix(pattern, "/api/v1/customers"):
		return `{"code":"RO","name":"Injected"}`
	case strings.HasSuffix(pattern, "/api/v1/users"):
		return `{"email":"auditor-injected@example.com","display_name":"Injected","role":"Administrator"}`
	case strings.HasSuffix(pattern, "/device-groups"), strings.HasSuffix(pattern, "/support-groups"):
		return `{"name":"Injected"}`
	case strings.HasSuffix(pattern, "/members"):
		return `{"device_id":"00000000-0000-0000-0000-000000000001"}`
	case strings.HasSuffix(pattern, "/technicians"):
		return `{"user_id":1}`
	case strings.HasSuffix(pattern, "/roles"):
		return `{"role":"Administrator"}`
	case strings.HasSuffix(pattern, "/strategy"):
		return `{"config_options":{"enable-audio":"N"}}`
	case strings.HasSuffix(pattern, "/disconnect"):
		return `{"conn_ids":[7]}`
	case strings.HasSuffix(pattern, "/claim"), strings.HasSuffix(pattern, "/reassign"):
		return `{"customer_id":"00000000-0000-0000-0000-000000000001"}`
	case strings.HasPrefix(pattern, "PATCH"):
		return `{"name":"Renamed"}`
	default:
		return ""
	}
}
