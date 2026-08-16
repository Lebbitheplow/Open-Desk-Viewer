// Package apiv1 is the management surface the React portal talks to.
//
// It is deliberately separate from internal/rustdeskapi, which exists to satisfy
// the RustDesk client's fixed contract. This package answers to the portal
// instead, so its shapes can change with the product. What it must not do is
// invent a second authorisation model: every scoping decision here goes through
// access.Resolver, the same one /api/peers uses.
package apiv1

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/OpenDeskViewer/platform/api/internal/access"
	"github.com/OpenDeskViewer/platform/api/internal/audit"
	"github.com/OpenDeskViewer/platform/api/internal/config"
	"github.com/OpenDeskViewer/platform/api/internal/devicepw"
	"github.com/OpenDeskViewer/platform/api/internal/fleet"
	"github.com/OpenDeskViewer/platform/api/internal/httpx"
	"github.com/OpenDeskViewer/platform/api/internal/identity"
	"github.com/OpenDeskViewer/platform/api/internal/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Handler serves every /api/v1 path.
type Handler struct {
	db       *postgres.Pool
	fleet    *fleet.Service
	access   access.Resolver
	audit    audit.Recorder
	cfg      *config.Config
	devicepw *devicepw.Service
	// accounts creates and removes the Keycloak account behind a portal user.
	// It is nil when the deployment has not configured one, and the two routes
	// that need it say so rather than half-creating a user; see users.go.
	accounts identity.AccountProvisioner
}

// NewHandler creates a handler for the /api/v1 endpoints.
func NewHandler(db *postgres.Pool, fleetService *fleet.Service, accessResolver access.Resolver, auditRecorder audit.Recorder, cfg *config.Config, passwords *devicepw.Service, accounts identity.AccountProvisioner) *Handler {
	return &Handler{
		db:       db,
		fleet:    fleetService,
		access:   accessResolver,
		audit:    auditRecorder,
		cfg:      cfg,
		devicepw: passwords,
		accounts: accounts,
	}
}

// Route pairs a ServeMux pattern with its handler. Routes returns the whole
// table as data so a test can assert on it and so registering it is one loop
// rather than fifty lines that can drift.
type Route struct {
	// Pattern is a Go 1.22 ServeMux pattern, method included. Every route here
	// names its method: /api/v1/devices/{id} carries three verbs, and
	// registering a bare pattern twice panics the mux at startup rather than
	// failing a request.
	Pattern string
	Handler http.HandlerFunc
	// Mutating marks the routes that must write an audit event. The route-table
	// test uses this to assert none is forgotten.
	Mutating bool
}

// Routes returns every /api/v1 route.
func (h *Handler) Routes() []Route {
	return []Route{
		{Pattern: "GET /api/v1/stats/dashboard", Handler: h.HandleStatsDashboard},

		{Pattern: "GET /api/v1/devices", Handler: h.HandleDevices},
		{Pattern: "GET /api/v1/devices/{id}", Handler: h.HandleDeviceDetail},
		{Pattern: "PATCH /api/v1/devices/{id}", Handler: h.HandleDeviceUpdate, Mutating: true},
		{Pattern: "DELETE /api/v1/devices/{id}", Handler: h.HandleDeleteDevice, Mutating: true},
		{Pattern: "POST /api/v1/devices/{id}/claim", Handler: h.HandleClaimDevice, Mutating: true},
		{Pattern: "POST /api/v1/devices/{id}/reassign", Handler: h.HandleReassignDevice, Mutating: true},
		{Pattern: "POST /api/v1/devices/{id}/connect", Handler: h.HandleDeviceConnect, Mutating: true},
		{Pattern: "GET /api/v1/devices/{id}/sessions", Handler: h.HandleDeviceSessions},

		// The heartbeat control channel. Both are delivered on the device's
		// next heartbeat rather than immediately; see internal/apiv1/control.go.
		{Pattern: "POST /api/v1/devices/{id}/disconnect", Handler: h.HandleDeviceDisconnect, Mutating: true},
		{Pattern: "GET /api/v1/devices/{id}/strategy", Handler: h.HandleDeviceStrategy},
		{Pattern: "PUT /api/v1/devices/{id}/strategy", Handler: h.HandleDeviceStrategy, Mutating: true},

		// The platform-managed connection password. Reading it is a technician's
		// and is audited; rotating it is an administrator's, and is what makes
		// withdrawing access an action on the machine. See internal/devicepw.
		{Pattern: "GET /api/v1/devices/{id}/password", Handler: h.HandleDevicePassword},
		{Pattern: "POST /api/v1/devices/{id}/password/rotate", Handler: h.HandleRotateDevicePassword, Mutating: true},

		// Ids that tried to report in without a credential. This is what
		// auto-registration was replaced with.
		{Pattern: "GET /api/v1/device-observations", Handler: h.HandleDeviceObservations},

		// Monitoring: the history of connectivity changes the worker already
		// performs, which used to be overwritten in a single column.
		{Pattern: "GET /api/v1/devices/{id}/connectivity", Handler: h.HandleDeviceConnectivityHistory},
		{Pattern: "GET /api/v1/monitoring/events", Handler: h.HandleFleetConnectivityHistory},

		// Notifications: where those events are sent, and whether they arrived.
		{Pattern: "GET /api/v1/notification-targets", Handler: h.HandleNotificationTargets},
		{Pattern: "POST /api/v1/notification-targets", Handler: h.HandleCreateNotificationTarget, Mutating: true},
		{Pattern: "DELETE /api/v1/notification-targets/{id}", Handler: h.HandleDeleteNotificationTarget, Mutating: true},
		{Pattern: "GET /api/v1/notification-deliveries", Handler: h.HandleNotificationDeliveries},

		{Pattern: "GET /api/v1/customers", Handler: h.HandleCustomers},
		{Pattern: "POST /api/v1/customers", Handler: h.HandleCreateCustomer, Mutating: true},
		{Pattern: "GET /api/v1/customers/{id}", Handler: h.HandleCustomerDetail},
		{Pattern: "PATCH /api/v1/customers/{id}", Handler: h.HandleUpdateCustomer, Mutating: true},
		{Pattern: "DELETE /api/v1/customers/{id}", Handler: h.HandleDeleteCustomer, Mutating: true},
		{Pattern: "GET /api/v1/customers/{id}/locations", Handler: h.HandleLocations},
		{Pattern: "POST /api/v1/customers/{id}/locations", Handler: h.HandleCreateLocation, Mutating: true},
		{Pattern: "PATCH /api/v1/customers/{id}/locations/{locationId}", Handler: h.HandleUpdateLocation, Mutating: true},
		{Pattern: "DELETE /api/v1/customers/{id}/locations/{locationId}", Handler: h.HandleDeleteLocation, Mutating: true},

		{Pattern: "GET /api/v1/device-groups", Handler: h.HandleDeviceGroups},
		{Pattern: "POST /api/v1/device-groups", Handler: h.HandleCreateDeviceGroup, Mutating: true},
		{Pattern: "GET /api/v1/device-groups/{id}", Handler: h.HandleDeviceGroupDetail},
		{Pattern: "PATCH /api/v1/device-groups/{id}", Handler: h.HandleUpdateDeviceGroup, Mutating: true},
		{Pattern: "DELETE /api/v1/device-groups/{id}", Handler: h.HandleDeleteDeviceGroup, Mutating: true},
		{Pattern: "GET /api/v1/device-groups/{id}/members", Handler: h.HandleDeviceGroupMembers},
		{Pattern: "POST /api/v1/device-groups/{id}/members", Handler: h.HandleAddDeviceGroupMember, Mutating: true},
		{Pattern: "DELETE /api/v1/device-groups/{id}/members/{deviceId}", Handler: h.HandleRemoveDeviceGroupMember, Mutating: true},

		{Pattern: "GET /api/v1/support-groups", Handler: h.HandleSupportGroups},
		{Pattern: "POST /api/v1/support-groups", Handler: h.HandleCreateSupportGroup, Mutating: true},
		{Pattern: "GET /api/v1/support-groups/{id}", Handler: h.HandleSupportGroupDetail},
		{Pattern: "PATCH /api/v1/support-groups/{id}", Handler: h.HandleUpdateSupportGroup, Mutating: true},
		{Pattern: "DELETE /api/v1/support-groups/{id}", Handler: h.HandleDeleteSupportGroup, Mutating: true},
		{Pattern: "POST /api/v1/support-groups/{id}/technicians", Handler: h.HandleAddTechnician, Mutating: true},
		{Pattern: "DELETE /api/v1/support-groups/{id}/technicians/{userId}", Handler: h.HandleRemoveTechnician, Mutating: true},
		{Pattern: "POST /api/v1/support-groups/{id}/device-groups", Handler: h.HandleAddSupportGroupDeviceGroup, Mutating: true},
		{Pattern: "DELETE /api/v1/support-groups/{id}/device-groups/{groupId}", Handler: h.HandleRemoveSupportGroupDeviceGroup, Mutating: true},

		{Pattern: "GET /api/v1/users", Handler: h.HandleUsers},
		// Creation and removal are the two halves of R1 that did not exist: a
		// users row used to appear only as a side effect of somebody signing in,
		// and the closest thing to removal was an active flag. Both reach into
		// Keycloak, because an account this portal cannot create is not one an
		// administrator can be said to manage.
		{Pattern: "POST /api/v1/users", Handler: h.HandleCreateUser, Mutating: true},
		{Pattern: "GET /api/v1/users/{id}", Handler: h.HandleUserDetail},
		{Pattern: "PATCH /api/v1/users/{id}", Handler: h.HandleUpdateUser, Mutating: true},
		{Pattern: "DELETE /api/v1/users/{id}", Handler: h.HandleDeleteUser, Mutating: true},
		{Pattern: "POST /api/v1/users/{id}/roles", Handler: h.HandleGrantRole, Mutating: true},
		{Pattern: "DELETE /api/v1/users/{id}/roles/{role}", Handler: h.HandleRevokeRole, Mutating: true},

		// Enrollment tokens are deliberately absent here. A working handler
		// already exists in internal/rustdeskapi, and main.go mounts it at both
		// /api/enrollment-tokens and /api/v1/enrollment-tokens. Reimplementing
		// it would give the two paths two chances to disagree about who may
		// issue a token.

		{Pattern: "GET /api/v1/audit/events", Handler: h.HandleAuditEvents},
		{Pattern: "GET /api/v1/audit/sessions", Handler: h.HandleAuditSessions},

		// Reporting. One route rather than three, because the three differ only
		// in their query and their columns, and three handlers would be three
		// chances for the CSV and JSON paths to drift.
		{Pattern: "GET /api/v1/reports/{report}", Handler: h.HandleReport},

		{Pattern: "GET /api/v1/settings", Handler: h.HandleSettings},
	}
}

// Register mounts every route on mux.
func (h *Handler) Register(mux interface {
	HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request))
}) {
	for _, route := range h.Routes() {
		mux.HandleFunc(route.Pattern, route.Handler)
	}
}

// ---------------------------------------------------------------------------
// Shared request plumbing
// ---------------------------------------------------------------------------

// writeJSONError writes a JSON error body. Every failure path here returns JSON,
// because the portal parses every response as JSON.
func writeJSONError(w http.ResponseWriter, status int, message string) {
	httpx.WriteJSON(w, status, map[string]any{"error": message})
}

// caller returns the authenticated user, or writes 401 and reports false.
func (h *Handler) caller(w http.ResponseWriter, r *http.Request) (*identity.User, bool) {
	user, ok := httpx.GetUserFromContext(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return nil, false
	}
	return user, true
}

// RoleReadOnly is the auditor role, seeded by migration 000001.
//
// It was referenced by no code at all until item 6.9: the portal offered it in
// the role list, granting it did nothing, and a user who held only it got 403
// from every screen. A role that grants nothing is worse than an absent one,
// because somebody assigns it and believes they have given access.
//
// What it means here is deliberately narrow and is worth stating in one place:
// **fleet-wide read, no writes, no remote access, no credentials.** A Read Only
// user sees every device, customer, group, user and audit event, and cannot
// create, change or delete any of them, cannot start a connection, cannot read
// or rotate a device password, and cannot push configuration. It is the role for
// somebody performing an access review, which is the reason a product like this
// has an audit log at all.
//
// It deliberately does not widen access.Resolver. The resolver answers the
// client-facing question -- which devices belong in this user's address book,
// which ones /api/peers returns -- and a Read Only auditor has no business
// appearing to their RustDesk client as somebody who may connect to the fleet.
// This role exists on the portal surface only.
const RoleReadOnly = "Read Only"

// admin returns the authenticated user only if it is an admin or manager.
// Administration endpoints call this rather than checking a role inline, so the
// definition of "may administer" lives in exactly one place.
func (h *Handler) admin(w http.ResponseWriter, r *http.Request) (*identity.User, bool) {
	user, ok := h.caller(w, r)
	if !ok {
		return nil, false
	}

	isAdmin, err := h.access.IsAdminOrManager(r.Context(), user.ID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to check administrator status")
		return nil, false
	}
	if !isAdmin {
		writeJSONError(w, http.StatusForbidden, "administrator access required")
		return nil, false
	}
	return user, true
}

// viewer is admin for read-only endpoints: it additionally admits Read Only.
//
// Every administration GET calls this and every administration mutation calls
// admin, which is what keeps the role from drifting into write access one
// handler at a time. TestReadOnlyCannotWriteAnything asserts the split holds
// across the whole route table rather than trusting it.
func (h *Handler) viewer(w http.ResponseWriter, r *http.Request) (*identity.User, bool) {
	user, ok := h.caller(w, r)
	if !ok {
		return nil, false
	}

	allowed, err := h.seesWholeFleet(r.Context(), user.ID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to check access")
		return nil, false
	}
	if !allowed {
		writeJSONError(w, http.StatusForbidden, "administrator access required")
		return nil, false
	}
	return user, true
}

// seesWholeFleet reports whether this caller's view is the deployment rather
// than their own support groups. Administrators and managers because they run
// the place; Read Only because an access review over a subset is not a review.
func (h *Handler) seesWholeFleet(ctx context.Context, userID int64) (bool, error) {
	isAdmin, err := h.access.IsAdminOrManager(ctx, userID)
	if err != nil || isAdmin {
		return isAdmin, err
	}
	return h.access.HasRole(ctx, userID, RoleReadOnly)
}

// pathUUID reads a {name} path segment as a UUID.
func pathUUID(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	raw := r.PathValue(name)
	if raw == "" {
		writeJSONError(w, http.StatusBadRequest, name+" is required")
		return uuid.Nil, false
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid "+name)
		return uuid.Nil, false
	}
	return id, true
}

// decodeBody reads a JSON request body into dst.
func decodeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}

// page holds a resolved pagination window.
type page struct {
	Current  int64
	PageSize int64
	Offset   int64
}

// parsePage resolves current/pageSize into a LIMIT/OFFSET window. It caps the
// page size so a caller cannot ask for the whole table in one request.
func parsePage(r *http.Request) page {
	current, pageSize, _ := httpx.ParsePagination(r)
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}
	return page{
		Current:  current,
		PageSize: pageSize,
		Offset:   (current - 1) * pageSize,
	}
}

const maxPageSize = 500

// writePage writes a list response in the {total, data} envelope the Pro
// surface uses, plus the page metadata the portal needs.
func writePage(w http.ResponseWriter, p page, total int64, data any) {
	httpx.WritePaginatedResponsePage(w, p.Current, p.PageSize, total, data)
}

// dbError maps a database failure onto a status. A unique violation is the
// caller's fault, not ours, and returning 500 for it makes a duplicate customer
// code look like an outage.
func dbError(w http.ResponseWriter, err error, fallback string) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505": // unique_violation
			writeJSONError(w, http.StatusConflict, "already exists")
			return
		case "23503": // foreign_key_violation
			writeJSONError(w, http.StatusBadRequest, "referenced record does not exist")
			return
		}
	}
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSONError(w, http.StatusNotFound, "not found")
		return
	}
	writeJSONError(w, http.StatusInternalServerError, fallback)
}

// record writes an audit event. Recording never fails the request it describes:
// audit.Recorder swallows its own errors, and losing an audit row is strictly
// better than failing the device reassignment that produced it.
func (h *Handler) record(ctx context.Context, e audit.Event) {
	_ = h.audit.Record(ctx, e)
}

// nullableString turns an empty string into a nil, for nullable columns where
// the portal sends "" to mean "unset".
func nullableString(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}
