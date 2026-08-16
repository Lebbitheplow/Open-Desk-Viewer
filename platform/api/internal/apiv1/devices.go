package apiv1

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/OpenDeskViewer/platform/api/internal/audit"
	"github.com/OpenDeskViewer/platform/api/internal/httpx"
	"github.com/google/uuid"
)

// Device is a row of the portal's device list.
type Device struct {
	ID           uuid.UUID  `json:"id"`
	RustdeskID   string     `json:"rustdesk_id"`
	Name         string     `json:"name"`
	Hostname     *string    `json:"hostname,omitempty"`
	OS           *string    `json:"os,omitempty"`
	State        string     `json:"state"`
	Connectivity string     `json:"connectivity"`
	LastSeenAt   *time.Time `json:"last_seen_at,omitempty"`
	CustomerID   *uuid.UUID `json:"customer_id,omitempty"`
	CustomerName *string    `json:"customer_name,omitempty"`
	LocationID   *uuid.UUID `json:"location_id,omitempty"`
	LocationName *string    `json:"location_name,omitempty"`
	ClientVer    *string    `json:"client_version,omitempty"`
	SerialNumber *string    `json:"serial_number,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// DeviceDetail adds the things only the detail page needs.
type DeviceDetail struct {
	Device
	Groups  []GroupSummary `json:"groups"`
	Sysinfo *Sysinfo       `json:"sysinfo,omitempty"`
}

// Sysinfo is the most recent hardware report from a device.
type Sysinfo struct {
	CPU         *string   `json:"cpu,omitempty"`
	Memory      *string   `json:"memory,omitempty"`
	Disk        *string   `json:"disk,omitempty"`
	Network     []string  `json:"network,omitempty"`
	Software    []string  `json:"software,omitempty"`
	CollectedAt time.Time `json:"collected_at"`
}

// GroupSummary names a group without dragging its whole record along.
type GroupSummary struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

// deviceColumns is the select list every device query shares, so the list and
// the detail endpoint cannot disagree about what a device looks like.
const deviceColumns = `
	d.id, d.rustdesk_id, d.name, d.hostname, d.os, d.state, d.connectivity,
	d.last_seen_at, d.customer_id, c.name, d.location_id, l.name,
	d.client_version, d.serial_number, d.created_at, d.updated_at`

const deviceJoins = `
	FROM devices d
	LEFT JOIN customers c ON c.id = d.customer_id
	LEFT JOIN locations l ON l.id = d.location_id`

func scanDevice(row interface{ Scan(...any) error }) (Device, error) {
	var d Device
	err := row.Scan(
		&d.ID, &d.RustdeskID, &d.Name, &d.Hostname, &d.OS, &d.State, &d.Connectivity,
		&d.LastSeenAt, &d.CustomerID, &d.CustomerName, &d.LocationID, &d.LocationName,
		&d.ClientVer, &d.SerialNumber, &d.CreatedAt, &d.UpdatedAt,
	)
	return d, err
}

// HandleDevices serves GET /api/v1/devices.
//
// Filters: q (name, hostname or serial), serial (exact), state, customer, group.
// A technician sees only what access.Resolver grants them, which is the same
// rule /api/peers applies, not a second one. An administrator, a manager and a
// Read Only auditor see the fleet.
func (h *Handler) HandleDevices(w http.ResponseWriter, r *http.Request) {
	user, ok := h.caller(w, r)
	if !ok {
		return
	}

	unscoped, err := h.seesWholeFleet(r.Context(), user.ID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to check administrator status")
		return
	}

	p := parsePage(r)
	query := r.URL.Query()

	// Conditions accumulate as a slice so the argument numbering cannot drift
	// from the placeholders.
	var conditions []string
	var args []any
	add := func(clause string, value any) {
		args = append(args, value)
		conditions = append(conditions, fmt.Sprintf(clause, len(args)))
	}

	if q := strings.TrimSpace(query.Get("q")); q != "" {
		// One argument, three columns: the trigram indexes on devices.name and
		// devices.hostname exist for exactly this. serial_number has no trigram
		// index, deliberately: a serial is searched whole, which is what the
		// `serial` parameter below is for, and this substring match is the
		// convenience case over a column that is short and rarely non-null.
		args = append(args, "%"+q+"%")
		conditions = append(conditions, fmt.Sprintf(
			"(d.name ILIKE $%d OR d.hostname ILIKE $%d OR d.serial_number ILIKE $%d)",
			len(args), len(args), len(args)))
	}
	// Exact serial lookup. This is the route an external system takes: it knows
	// a serial or an asset tag and needs the device id before it can PATCH a
	// name, and a substring search would be both slower and ambiguous.
	if serial := strings.TrimSpace(query.Get("serial")); serial != "" {
		add("d.serial_number = $%d", serial)
	}
	if state := strings.TrimSpace(query.Get("state")); state != "" {
		add("d.state = $%d", state)
	}
	if customer := strings.TrimSpace(query.Get("customer")); customer != "" {
		customerID, err := uuid.Parse(customer)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid customer")
			return
		}
		add("d.customer_id = $%d", customerID)
	}
	if group := strings.TrimSpace(query.Get("group")); group != "" {
		groupID, err := uuid.Parse(group)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid group")
			return
		}
		add("EXISTS (SELECT 1 FROM device_group_members m WHERE m.device_id = d.id AND m.device_group_id = $%d)", groupID)
	}

	if !unscoped {
		// Scope to the devices this user's support groups reach. Passing the id
		// list as one array argument keeps this a single round trip and avoids
		// building an IN list whose placeholder count varies.
		accessible, err := h.access.GetAccessibleDevices(r.Context(), user.ID)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to resolve accessible devices")
			return
		}
		if len(accessible) == 0 {
			writePage(w, p, 0, []Device{})
			return
		}
		add("d.id = ANY($%d)", accessible)
	}

	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}

	var total int64
	if err := h.db.QueryRow(r.Context(), `SELECT COUNT(*) `+deviceJoins+where, args...).Scan(&total); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to count devices")
		return
	}

	args = append(args, p.PageSize, p.Offset)
	rows, err := h.db.Query(r.Context(),
		`SELECT `+deviceColumns+deviceJoins+where+
			fmt.Sprintf(" ORDER BY d.name ASC, d.id ASC LIMIT $%d OFFSET $%d", len(args)-1, len(args)),
		args...)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to list devices")
		return
	}
	defer rows.Close()

	devices := make([]Device, 0, p.PageSize)
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to read device")
			return
		}
		devices = append(devices, d)
	}
	if rows.Err() != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to read devices")
		return
	}

	writePage(w, p, total, devices)
}

// HandleDeviceDetail serves GET /api/v1/devices/{id}.
func (h *Handler) HandleDeviceDetail(w http.ResponseWriter, r *http.Request) {
	deviceID, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	if _, ok := h.authoriseDeviceView(w, r, deviceID); !ok {
		return
	}

	row := h.db.QueryRow(r.Context(), `SELECT `+deviceColumns+deviceJoins+` WHERE d.id = $1`, deviceID)

	var detail DeviceDetail
	device, err := scanDevice(row)
	detail.Device = device
	if err != nil {
		dbError(w, err, "failed to read device")
		return
	}

	groups, err := h.deviceGroups(r.Context(), deviceID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to read device groups")
		return
	}
	detail.Groups = groups

	// Sysinfo is optional: a device that has never reported has no row, and the
	// detail page renders fine without one.
	var sysinfo Sysinfo
	err = h.db.QueryRow(r.Context(), `
		SELECT cpu, memory, disk, network, software, collected_at
		FROM device_sysinfo WHERE device_id = $1`, deviceID).
		Scan(&sysinfo.CPU, &sysinfo.Memory, &sysinfo.Disk, &sysinfo.Network,
			&sysinfo.Software, &sysinfo.CollectedAt)
	if err == nil {
		detail.Sysinfo = &sysinfo
	}

	httpx.WriteJSON(w, http.StatusOK, detail)
}

func (h *Handler) deviceGroups(ctx context.Context, deviceID uuid.UUID) ([]GroupSummary, error) {
	rows, err := h.db.Query(ctx, `
		SELECT g.id, g.name
		FROM device_groups g
		JOIN device_group_members m ON m.device_group_id = g.id
		WHERE m.device_id = $1
		ORDER BY g.name`, deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	groups := make([]GroupSummary, 0)
	for rows.Next() {
		var g GroupSummary
		if err := rows.Scan(&g.ID, &g.Name); err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}
	return groups, rows.Err()
}

// authoriseDevice resolves the caller and confirms it may *reach* deviceID.
//
// This is the connect-shaped question, and it gates the endpoints that act on a
// machine or hand out a credential: connect, the connection password, and the
// disconnect queue. Technicians probing for device ids get 403, not 404, so the
// response does not distinguish "exists but not yours" from "does not exist".
//
// Read Only is deliberately not admitted here. An auditor may look at a device
// and may not be given the password to it.
func (h *Handler) authoriseDevice(w http.ResponseWriter, r *http.Request, deviceID uuid.UUID) (int64, bool) {
	user, ok := h.caller(w, r)
	if !ok {
		return 0, false
	}

	canAccess, err := h.access.CanAccessDevice(r.Context(), user.ID, deviceID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to check device access")
		return 0, false
	}
	if !canAccess {
		writeJSONError(w, http.StatusForbidden, "not authorised for this device")
		return 0, false
	}
	return user.ID, true
}

// authoriseDeviceView is the read-shaped question: may this caller look at the
// device's record. It admits everyone authoriseDevice does, plus Read Only.
//
// The two are separate functions rather than a boolean argument because the
// difference between them is the difference between reading a row and being
// handed a way onto somebody's machine, and that is not a distinction to make
// with a flag at the call site.
func (h *Handler) authoriseDeviceView(w http.ResponseWriter, r *http.Request, deviceID uuid.UUID) (int64, bool) {
	user, ok := h.caller(w, r)
	if !ok {
		return 0, false
	}

	canAccess, err := h.access.CanAccessDevice(r.Context(), user.ID, deviceID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to check device access")
		return 0, false
	}
	if !canAccess {
		readOnly, err := h.access.HasRole(r.Context(), user.ID, RoleReadOnly)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to check device access")
			return 0, false
		}
		if !readOnly {
			writeJSONError(w, http.StatusForbidden, "not authorised for this device")
			return 0, false
		}
		// A Read Only caller still only sees devices that exist. Without this a
		// probe for random uuids would answer 200 with an empty body rather
		// than 403, which is a different oracle from the one above but an
		// oracle all the same.
		var exists bool
		if err := h.db.QueryRow(r.Context(),
			`SELECT EXISTS (SELECT 1 FROM devices WHERE id = $1)`, deviceID).Scan(&exists); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to check device access")
			return 0, false
		}
		if !exists {
			writeJSONError(w, http.StatusForbidden, "not authorised for this device")
			return 0, false
		}
	}
	return user.ID, true
}

// HandleDeviceUpdate serves PATCH /api/v1/devices/{id}: rename, and move to a
// different customer or location.
func (h *Handler) HandleDeviceUpdate(w http.ResponseWriter, r *http.Request) {
	user, ok := h.admin(w, r)
	if !ok {
		return
	}
	deviceID, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}

	var req struct {
		Name       *string `json:"name"`
		CustomerID *string `json:"customer_id"`
		LocationID *string `json:"location_id"`
	}
	if !decodeBody(w, r, &req) {
		return
	}

	// COALESCE leaves an omitted field untouched, so a PATCH that sends only a
	// name cannot blank the customer.
	var customerID, locationID *uuid.UUID
	if req.CustomerID != nil {
		parsed, err := parseOptionalUUID(*req.CustomerID)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid customer_id")
			return
		}
		customerID = parsed
	}
	if req.LocationID != nil {
		parsed, err := parseOptionalUUID(*req.LocationID)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid location_id")
			return
		}
		locationID = parsed
	}

	tag, err := h.db.Exec(r.Context(), `
		UPDATE devices
		SET name        = COALESCE($2, name),
		    customer_id = CASE WHEN $3::boolean THEN $4::uuid ELSE customer_id END,
		    location_id = CASE WHEN $5::boolean THEN $6::uuid ELSE location_id END,
		    updated_at  = now()
		WHERE id = $1`,
		deviceID, req.Name,
		req.CustomerID != nil, customerID,
		req.LocationID != nil, locationID)
	if err != nil {
		dbError(w, err, "failed to update device")
		return
	}
	if tag.RowsAffected() == 0 {
		writeJSONError(w, http.StatusNotFound, "device not found")
		return
	}

	h.record(r.Context(), audit.Event{
		Type:        "device.updated",
		ActorID:     user.ID,
		Resource:    "device",
		ResourceID:  deviceID.String(),
		Description: "Device updated",
		Metadata: map[string]any{
			"name":        req.Name,
			"customer_id": req.CustomerID,
			"location_id": req.LocationID,
		},
	})

	httpx.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}

func parseOptionalUUID(raw string) (*uuid.UUID, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// HandleClaimDevice serves POST /api/v1/devices/{id}/claim: a DISCOVERED device
// becomes ACTIVE and gains a customer, which is what makes it visible to the
// technicians whose support groups reach it.
func (h *Handler) HandleClaimDevice(w http.ResponseWriter, r *http.Request) {
	user, ok := h.admin(w, r)
	if !ok {
		return
	}
	deviceID, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}

	var req struct {
		CustomerID string  `json:"customer_id"`
		LocationID *string `json:"location_id"`
		Name       *string `json:"name"`
	}
	if !decodeBody(w, r, &req) {
		return
	}

	customerID, err := uuid.Parse(strings.TrimSpace(req.CustomerID))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "customer_id is required")
		return
	}

	var locationID *uuid.UUID
	if req.LocationID != nil {
		locationID, err = parseOptionalUUID(*req.LocationID)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid location_id")
			return
		}
	}

	// Only a DISCOVERED device can be claimed. Without that guard, a second
	// claim would silently reassign an already-active device and bypass the
	// reassignment path, which is the one that revokes the old access.
	tag, err := h.db.Exec(r.Context(), `
		UPDATE devices
		SET state       = 'ACTIVE',
		    customer_id = $2,
		    location_id = COALESCE($3, location_id),
		    name        = COALESCE($4, name),
		    updated_at  = now()
		WHERE id = $1 AND state = 'DISCOVERED'`,
		deviceID, customerID, locationID, req.Name)
	if err != nil {
		dbError(w, err, "failed to claim device")
		return
	}
	if tag.RowsAffected() == 0 {
		writeJSONError(w, http.StatusConflict, "device is not awaiting claim")
		return
	}

	h.record(r.Context(), audit.Event{
		Type:        "device.claimed",
		ActorID:     user.ID,
		Resource:    "device",
		ResourceID:  deviceID.String(),
		CustomerID:  &customerID,
		DeviceID:    &deviceID,
		Description: "Device claimed and activated",
		Metadata:    map[string]any{"customer_id": customerID.String()},
	})

	httpx.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}

// HandleReassignDevice serves POST /api/v1/devices/{id}/reassign.
//
// This is the operation the plan singles out as needing care. Moving a device
// between customers must also drop the group memberships that belonged to the
// old customer, or the previous technician keeps reaching it through a stale
// group. Doing that in one transaction is the whole point: a partial
// reassignment leaves a device visible to two customers at once.
func (h *Handler) HandleReassignDevice(w http.ResponseWriter, r *http.Request) {
	user, ok := h.admin(w, r)
	if !ok {
		return
	}
	deviceID, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}

	var req struct {
		CustomerID    string      `json:"customer_id"`
		LocationID    *string     `json:"location_id"`
		DeviceGroupID []uuid.UUID `json:"device_group_ids"`
	}
	if !decodeBody(w, r, &req) {
		return
	}

	newCustomerID, err := uuid.Parse(strings.TrimSpace(req.CustomerID))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "customer_id is required")
		return
	}

	var locationID *uuid.UUID
	if req.LocationID != nil {
		locationID, err = parseOptionalUUID(*req.LocationID)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid location_id")
			return
		}
	}

	tx, err := h.db.Tx(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to begin transaction")
		return
	}
	defer tx.Rollback(r.Context())

	var oldCustomerID *uuid.UUID
	if err := tx.QueryRow(r.Context(),
		`SELECT customer_id FROM devices WHERE id = $1 FOR UPDATE`, deviceID).Scan(&oldCustomerID); err != nil {
		dbError(w, err, "failed to read device")
		return
	}

	if _, err := tx.Exec(r.Context(), `
		UPDATE devices
		SET customer_id = $2, location_id = $3, updated_at = now()
		WHERE id = $1`, deviceID, newCustomerID, locationID); err != nil {
		dbError(w, err, "failed to reassign device")
		return
	}

	// Drop every membership whose group is reachable only by the old customer's
	// support groups. Deleting all memberships is the safe move here: the caller
	// states the new ones explicitly, so nothing is inherited by accident.
	if _, err := tx.Exec(r.Context(),
		`DELETE FROM device_group_members WHERE device_id = $1`, deviceID); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to clear device groups")
		return
	}

	for _, groupID := range req.DeviceGroupID {
		if _, err := tx.Exec(r.Context(), `
			INSERT INTO device_group_members (device_group_id, device_id)
			VALUES ($1, $2)
			ON CONFLICT DO NOTHING`, groupID, deviceID); err != nil {
			dbError(w, err, "failed to add device to group")
			return
		}
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to commit reassignment")
		return
	}

	var oldCustomerStr *string
	if oldCustomerID != nil {
		s := oldCustomerID.String()
		oldCustomerStr = &s
	}

	h.record(r.Context(), audit.Event{
		Type:        "device.reassigned",
		ActorID:     user.ID,
		Resource:    "device",
		ResourceID:  deviceID.String(),
		DeviceID:    &deviceID,
		CustomerID:  &newCustomerID,
		Description: "Device reassigned to a different customer",
		Metadata: map[string]any{
			"old_customer_id": oldCustomerStr,
			"new_customer_id": newCustomerID.String(),
		},
	})

	httpx.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}

// HandleDeviceConnect serves POST /api/v1/devices/{id}/connect.
//
// The URI shape is the client's, from flutter/lib/common.dart:2482-2500. The
// password is deliberately not included: the plan warns against putting a
// permanent credential in a URL that lands in browser history, and this
// deployment has no short-lived credential store yet. The operator is prompted
// by the client instead. Opening the session is still recorded, so the audit log
// shows who initiated it even if the connection never completes.
func (h *Handler) HandleDeviceConnect(w http.ResponseWriter, r *http.Request) {
	deviceID, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	userID, ok := h.authoriseDevice(w, r, deviceID)
	if !ok {
		return
	}

	var rustdeskID string
	if err := h.db.QueryRow(r.Context(),
		`SELECT rustdesk_id FROM devices WHERE id = $1`, deviceID).Scan(&rustdeskID); err != nil {
		dbError(w, err, "failed to read device")
		return
	}

	h.record(r.Context(), audit.Event{
		Type:        "device.connect_requested",
		ActorID:     userID,
		Resource:    "device",
		ResourceID:  deviceID.String(),
		DeviceID:    &deviceID,
		Description: "Connection initiated from the portal",
		Metadata:    map[string]any{"rustdesk_id": rustdeskID},
	})

	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"uri":         fmt.Sprintf("rustdesk://connect/%s", rustdeskID),
		"rustdesk_id": rustdeskID,
	})
}

// HandleDeleteDevice serves DELETE /api/v1/devices/{id}.
func (h *Handler) HandleDeleteDevice(w http.ResponseWriter, r *http.Request) {
	user, ok := h.admin(w, r)
	if !ok {
		return
	}
	deviceID, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}

	// Read the name before deleting: an audit row naming only a uuid that no
	// longer resolves is not much of an audit row.
	var name string
	if err := h.db.QueryRow(r.Context(), `SELECT name FROM devices WHERE id = $1`, deviceID).Scan(&name); err != nil {
		dbError(w, err, "failed to read device")
		return
	}

	if _, err := h.db.Exec(r.Context(), `DELETE FROM devices WHERE id = $1`, deviceID); err != nil {
		dbError(w, err, "failed to delete device")
		return
	}

	h.record(r.Context(), audit.Event{
		Type:        "device.deleted",
		ActorID:     user.ID,
		Resource:    "device",
		ResourceID:  deviceID.String(),
		Description: "Device deleted: " + name,
		Metadata:    map[string]any{"name": name},
	})

	httpx.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}

// Session is a connection record as the portal's audit log shows it.
type Session struct {
	ID            uuid.UUID  `json:"id"`
	DeviceID      *uuid.UUID `json:"device_id,omitempty"`
	DeviceName    *string    `json:"device_name,omitempty"`
	UserID        *int64     `json:"user_id,omitempty"`
	UserEmail     *string    `json:"user_email,omitempty"`
	StartTime     time.Time  `json:"start_time"`
	EndTime       *time.Time `json:"end_time,omitempty"`
	Duration      *int32     `json:"duration_seconds,omitempty"`
	Status        string     `json:"status"`
	ConnectedFrom *string    `json:"connected_from,omitempty"`
	Note          string     `json:"note"`
}

// HandleDeviceSessions serves GET /api/v1/devices/{id}/sessions.
func (h *Handler) HandleDeviceSessions(w http.ResponseWriter, r *http.Request) {
	deviceID, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	if _, ok := h.authoriseDeviceView(w, r, deviceID); !ok {
		return
	}

	p := parsePage(r)

	var total int64
	if err := h.db.QueryRow(r.Context(),
		`SELECT COUNT(*) FROM connection_sessions WHERE device_id = $1`, deviceID).Scan(&total); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to count sessions")
		return
	}

	rows, err := h.db.Query(r.Context(), `
		SELECT s.id, s.device_id, d.name, s.user_id, u.email, s.start_time, s.end_time,
		       s.duration_seconds, s.status, s.connected_from, s.note
		FROM connection_sessions s
		LEFT JOIN devices d ON d.id = s.device_id
		LEFT JOIN users u ON u.id = s.user_id
		WHERE s.device_id = $1
		ORDER BY s.start_time DESC
		LIMIT $2 OFFSET $3`, deviceID, p.PageSize, p.Offset)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to list sessions")
		return
	}
	defer rows.Close()

	sessions, err := scanSessions(rows)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to read sessions")
		return
	}

	writePage(w, p, total, sessions)
}

func scanSessions(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
}) ([]Session, error) {
	sessions := make([]Session, 0)
	for rows.Next() {
		var s Session
		if err := rows.Scan(&s.ID, &s.DeviceID, &s.DeviceName, &s.UserID, &s.UserEmail,
			&s.StartTime, &s.EndTime, &s.Duration, &s.Status, &s.ConnectedFrom, &s.Note); err != nil {
			return nil, err
		}
		sessions = append(sessions, s)
	}
	return sessions, rows.Err()
}
