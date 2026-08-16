package apiv1

import (
	"net/http"
	"strings"
	"testing"
)

// TestRoutesRegisterWithoutPanic is the regression test for the bug that made
// the API unstartable: /api/v1/devices/{id} was registered twice, and
// http.ServeMux panics on a duplicate pattern rather than returning an error.
// A panic in route registration is not caught by anything, so the process died
// before it ever listened. Building the table in a test turns that into a test
// failure.
func TestRoutesRegisterWithoutPanic(t *testing.T) {
	h := &Handler{}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("registering the route table panicked, which would kill the API at startup: %v", r)
		}
	}()

	mux := http.NewServeMux()
	for _, route := range h.Routes() {
		mux.HandleFunc(route.Pattern, route.Handler)
	}
}

// TestRoutesAreMethodScoped asserts every pattern names a method. A bare
// pattern silently claims every verb, so a GET-only handler would answer a
// DELETE with 200 and no action, which is worse than a 405.
func TestRoutesAreMethodScoped(t *testing.T) {
	methods := map[string]bool{"GET": true, "POST": true, "PATCH": true, "PUT": true, "DELETE": true}

	for _, route := range (&Handler{}).Routes() {
		method, path, found := strings.Cut(route.Pattern, " ")
		if !found {
			t.Errorf("route %q does not name a method", route.Pattern)
			continue
		}
		if !methods[method] {
			t.Errorf("route %q has an unrecognised method %q", route.Pattern, method)
		}
		if !strings.HasPrefix(path, "/api/v1/") {
			t.Errorf("route %q is not under /api/v1/", route.Pattern)
		}
	}
}

// TestMutatingRoutesAreNotGET checks the Mutating flag against the verb, so the
// audit-coverage test below cannot be satisfied by mislabelling a route.
func TestMutatingRoutesAreNotGET(t *testing.T) {
	for _, route := range (&Handler{}).Routes() {
		method, _, _ := strings.Cut(route.Pattern, " ")
		if route.Mutating && method == "GET" {
			t.Errorf("route %q is marked mutating but is a GET", route.Pattern)
		}
		if !route.Mutating && method != "GET" {
			t.Errorf("route %q changes state but is not marked mutating, so it escapes the audit check", route.Pattern)
		}
	}
}

// TestEveryPlannedPathIsRegistered pins the surface described in the plan's
// section D.2. A path that disappears from the table fails here rather than
// 404ing in the portal.
func TestEveryPlannedPathIsRegistered(t *testing.T) {
	required := []string{
		"GET /api/v1/stats/dashboard",
		"GET /api/v1/devices",
		"GET /api/v1/devices/{id}",
		"PATCH /api/v1/devices/{id}",
		"DELETE /api/v1/devices/{id}",
		"POST /api/v1/devices/{id}/claim",
		"POST /api/v1/devices/{id}/reassign",
		"POST /api/v1/devices/{id}/connect",
		"GET /api/v1/devices/{id}/sessions",
		"GET /api/v1/customers",
		"POST /api/v1/customers",
		"GET /api/v1/customers/{id}",
		"PATCH /api/v1/customers/{id}",
		"DELETE /api/v1/customers/{id}",
		"GET /api/v1/customers/{id}/locations",
		"POST /api/v1/customers/{id}/locations",
		"PATCH /api/v1/customers/{id}/locations/{locationId}",
		"DELETE /api/v1/customers/{id}/locations/{locationId}",
		"GET /api/v1/device-groups",
		"POST /api/v1/device-groups",
		"GET /api/v1/device-groups/{id}",
		"PATCH /api/v1/device-groups/{id}",
		"DELETE /api/v1/device-groups/{id}",
		"POST /api/v1/device-groups/{id}/members",
		"DELETE /api/v1/device-groups/{id}/members/{deviceId}",
		"GET /api/v1/support-groups",
		"POST /api/v1/support-groups",
		"GET /api/v1/support-groups/{id}",
		"PATCH /api/v1/support-groups/{id}",
		"DELETE /api/v1/support-groups/{id}",
		"POST /api/v1/support-groups/{id}/technicians",
		"DELETE /api/v1/support-groups/{id}/technicians/{userId}",
		"POST /api/v1/support-groups/{id}/device-groups",
		"DELETE /api/v1/support-groups/{id}/device-groups/{groupId}",
		"GET /api/v1/users",
		"GET /api/v1/users/{id}",
		"PATCH /api/v1/users/{id}",
		"POST /api/v1/users/{id}/roles",
		"DELETE /api/v1/users/{id}/roles/{role}",
		"GET /api/v1/audit/events",
		"GET /api/v1/audit/sessions",
		"GET /api/v1/settings",
	}

	registered := make(map[string]bool)
	for _, route := range (&Handler{}).Routes() {
		registered[route.Pattern] = true
	}

	for _, pattern := range required {
		if !registered[pattern] {
			t.Errorf("plan section D.2 requires %q, which is not registered", pattern)
		}
	}
}
