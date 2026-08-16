package apiv1

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/OpenDeskViewer/platform/api/internal/audit"
	"github.com/OpenDeskViewer/platform/api/internal/httpx"
	"github.com/google/uuid"
)

// DeviceGroup is a collection of devices that a support group can be granted.
type DeviceGroup struct {
	ID          uuid.UUID  `json:"id"`
	Name        string     `json:"name"`
	ParentID    *uuid.UUID `json:"parent_id,omitempty"`
	MemberCount int64      `json:"member_count"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// SupportGroup is a team of technicians and the device groups they reach.
type SupportGroup struct {
	ID           uuid.UUID      `json:"id"`
	Name         string         `json:"name"`
	Description  *string        `json:"description,omitempty"`
	Technicians  []Technician   `json:"technicians,omitempty"`
	DeviceGroups []GroupSummary `json:"device_groups,omitempty"`
	MemberCount  int64          `json:"member_count"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
}

// Technician is a user as the support group screen shows them.
type Technician struct {
	ID          int64  `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Active      bool   `json:"active"`
}

// ---------------------------------------------------------------------------
// Device groups
// ---------------------------------------------------------------------------

// HandleDeviceGroups serves GET /api/v1/device-groups.
func (h *Handler) HandleDeviceGroups(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.viewer(w, r); !ok {
		return
	}

	p := parsePage(r)

	var total int64
	if err := h.db.QueryRow(r.Context(), `SELECT COUNT(*) FROM device_groups`).Scan(&total); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to count device groups")
		return
	}

	rows, err := h.db.Query(r.Context(), `
		SELECT g.id, g.name, g.parent_id, g.created_at, g.updated_at, COUNT(m.device_id)
		FROM device_groups g
		LEFT JOIN device_group_members m ON m.device_group_id = g.id
		GROUP BY g.id
		ORDER BY g.name
		LIMIT $1 OFFSET $2`, p.PageSize, p.Offset)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to list device groups")
		return
	}
	defer rows.Close()

	groups := make([]DeviceGroup, 0)
	for rows.Next() {
		var g DeviceGroup
		if err := rows.Scan(&g.ID, &g.Name, &g.ParentID, &g.CreatedAt, &g.UpdatedAt, &g.MemberCount); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to read device group")
			return
		}
		groups = append(groups, g)
	}
	if rows.Err() != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to read device groups")
		return
	}

	writePage(w, p, total, groups)
}

// HandleDeviceGroupDetail serves GET /api/v1/device-groups/{id}.
func (h *Handler) HandleDeviceGroupDetail(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.viewer(w, r); !ok {
		return
	}
	groupID, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}

	var g DeviceGroup
	err := h.db.QueryRow(r.Context(), `
		SELECT g.id, g.name, g.parent_id, g.created_at, g.updated_at,
		       (SELECT COUNT(*) FROM device_group_members m WHERE m.device_group_id = g.id)
		FROM device_groups g WHERE g.id = $1`, groupID).
		Scan(&g.ID, &g.Name, &g.ParentID, &g.CreatedAt, &g.UpdatedAt, &g.MemberCount)
	if err != nil {
		dbError(w, err, "failed to read device group")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, g)
}

// HandleCreateDeviceGroup serves POST /api/v1/device-groups.
func (h *Handler) HandleCreateDeviceGroup(w http.ResponseWriter, r *http.Request) {
	user, ok := h.admin(w, r)
	if !ok {
		return
	}

	var req struct {
		Name     string  `json:"name"`
		ParentID *string `json:"parent_id"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeJSONError(w, http.StatusBadRequest, "name is required")
		return
	}

	var parentID *uuid.UUID
	if req.ParentID != nil {
		parsed, err := parseOptionalUUID(*req.ParentID)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid parent_id")
			return
		}
		parentID = parsed
	}

	var g DeviceGroup
	err := h.db.QueryRow(r.Context(), `
		INSERT INTO device_groups (name, parent_id)
		VALUES ($1, $2)
		RETURNING id, name, parent_id, created_at, updated_at`,
		strings.TrimSpace(req.Name), parentID).
		Scan(&g.ID, &g.Name, &g.ParentID, &g.CreatedAt, &g.UpdatedAt)
	if err != nil {
		dbError(w, err, "failed to create device group")
		return
	}

	h.record(r.Context(), audit.Event{
		Type:        "device_group.created",
		ActorID:     user.ID,
		Resource:    "device_group",
		ResourceID:  g.ID.String(),
		Description: "Device group created: " + g.Name,
	})

	httpx.WriteJSON(w, http.StatusCreated, g)
}

// HandleUpdateDeviceGroup serves PATCH /api/v1/device-groups/{id}.
func (h *Handler) HandleUpdateDeviceGroup(w http.ResponseWriter, r *http.Request) {
	user, ok := h.admin(w, r)
	if !ok {
		return
	}
	groupID, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}

	var req struct {
		Name     *string `json:"name"`
		ParentID *string `json:"parent_id"`
	}
	if !decodeBody(w, r, &req) {
		return
	}

	var parentID *uuid.UUID
	if req.ParentID != nil {
		parsed, err := parseOptionalUUID(*req.ParentID)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid parent_id")
			return
		}
		// A group that is its own parent makes the hierarchy a cycle, and the
		// address book projection walks it.
		if parsed != nil && *parsed == groupID {
			writeJSONError(w, http.StatusBadRequest, "a device group cannot be its own parent")
			return
		}
		parentID = parsed
	}

	var g DeviceGroup
	err := h.db.QueryRow(r.Context(), `
		UPDATE device_groups
		SET name       = COALESCE($2, name),
		    parent_id  = CASE WHEN $3::boolean THEN $4::uuid ELSE parent_id END,
		    updated_at = now()
		WHERE id = $1
		RETURNING id, name, parent_id, created_at, updated_at`,
		groupID, req.Name, req.ParentID != nil, parentID).
		Scan(&g.ID, &g.Name, &g.ParentID, &g.CreatedAt, &g.UpdatedAt)
	if err != nil {
		dbError(w, err, "failed to update device group")
		return
	}

	h.record(r.Context(), audit.Event{
		Type:        "device_group.updated",
		ActorID:     user.ID,
		Resource:    "device_group",
		ResourceID:  g.ID.String(),
		Description: "Device group updated: " + g.Name,
	})

	httpx.WriteJSON(w, http.StatusOK, g)
}

// HandleDeleteDeviceGroup serves DELETE /api/v1/device-groups/{id}.
func (h *Handler) HandleDeleteDeviceGroup(w http.ResponseWriter, r *http.Request) {
	user, ok := h.admin(w, r)
	if !ok {
		return
	}
	groupID, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}

	var name string
	if err := h.db.QueryRow(r.Context(), `SELECT name FROM device_groups WHERE id = $1`, groupID).Scan(&name); err != nil {
		dbError(w, err, "failed to read device group")
		return
	}

	if _, err := h.db.Exec(r.Context(), `DELETE FROM device_groups WHERE id = $1`, groupID); err != nil {
		dbError(w, err, "failed to delete device group")
		return
	}

	h.record(r.Context(), audit.Event{
		Type:        "device_group.deleted",
		ActorID:     user.ID,
		Resource:    "device_group",
		ResourceID:  groupID.String(),
		Description: "Device group deleted: " + name,
		Metadata:    map[string]any{"name": name},
	})

	httpx.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}

// HandleDeviceGroupMembers serves GET /api/v1/device-groups/{id}/members.
func (h *Handler) HandleDeviceGroupMembers(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.viewer(w, r); !ok {
		return
	}
	groupID, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}

	p := parsePage(r)

	var total int64
	if err := h.db.QueryRow(r.Context(),
		`SELECT COUNT(*) FROM device_group_members WHERE device_group_id = $1`, groupID).Scan(&total); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to count members")
		return
	}

	rows, err := h.db.Query(r.Context(), `SELECT `+deviceColumns+deviceJoins+`
		JOIN device_group_members m ON m.device_id = d.id
		WHERE m.device_group_id = $1
		ORDER BY d.name
		LIMIT $2 OFFSET $3`, groupID, p.PageSize, p.Offset)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to list members")
		return
	}
	defer rows.Close()

	devices := make([]Device, 0)
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to read member")
			return
		}
		devices = append(devices, d)
	}
	if rows.Err() != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to read members")
		return
	}

	writePage(w, p, total, devices)
}

// HandleAddDeviceGroupMember serves POST /api/v1/device-groups/{id}/members.
func (h *Handler) HandleAddDeviceGroupMember(w http.ResponseWriter, r *http.Request) {
	user, ok := h.admin(w, r)
	if !ok {
		return
	}
	groupID, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}

	var req struct {
		DeviceID string `json:"device_id"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	deviceID, err := uuid.Parse(strings.TrimSpace(req.DeviceID))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "device_id is required")
		return
	}

	if _, err := h.db.Exec(r.Context(), `
		INSERT INTO device_group_members (device_group_id, device_id)
		VALUES ($1, $2) ON CONFLICT DO NOTHING`, groupID, deviceID); err != nil {
		dbError(w, err, "failed to add device to group")
		return
	}

	h.record(r.Context(), audit.Event{
		Type:        "device_group.member_added",
		ActorID:     user.ID,
		Resource:    "device_group",
		ResourceID:  groupID.String(),
		DeviceID:    &deviceID,
		Description: "Device added to group",
	})

	httpx.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}

// HandleRemoveDeviceGroupMember serves
// DELETE /api/v1/device-groups/{id}/members/{deviceId}.
func (h *Handler) HandleRemoveDeviceGroupMember(w http.ResponseWriter, r *http.Request) {
	user, ok := h.admin(w, r)
	if !ok {
		return
	}
	groupID, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	deviceID, ok := pathUUID(w, r, "deviceId")
	if !ok {
		return
	}

	tag, err := h.db.Exec(r.Context(),
		`DELETE FROM device_group_members WHERE device_group_id = $1 AND device_id = $2`, groupID, deviceID)
	if err != nil {
		dbError(w, err, "failed to remove device from group")
		return
	}
	if tag.RowsAffected() == 0 {
		writeJSONError(w, http.StatusNotFound, "device is not a member of this group")
		return
	}

	h.record(r.Context(), audit.Event{
		Type:        "device_group.member_removed",
		ActorID:     user.ID,
		Resource:    "device_group",
		ResourceID:  groupID.String(),
		DeviceID:    &deviceID,
		Description: "Device removed from group",
	})

	httpx.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}

// ---------------------------------------------------------------------------
// Support groups
// ---------------------------------------------------------------------------

// HandleSupportGroups serves GET /api/v1/support-groups.
func (h *Handler) HandleSupportGroups(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.viewer(w, r); !ok {
		return
	}

	p := parsePage(r)

	var total int64
	if err := h.db.QueryRow(r.Context(), `SELECT COUNT(*) FROM support_groups`).Scan(&total); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to count support groups")
		return
	}

	rows, err := h.db.Query(r.Context(), `
		SELECT g.id, g.name, g.description, g.created_at, g.updated_at,
		       COUNT(DISTINCT m.user_id)
		FROM support_groups g
		LEFT JOIN user_support_groups m ON m.support_group_id = g.id
		GROUP BY g.id
		ORDER BY g.name
		LIMIT $1 OFFSET $2`, p.PageSize, p.Offset)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to list support groups")
		return
	}
	defer rows.Close()

	groups := make([]SupportGroup, 0)
	for rows.Next() {
		var g SupportGroup
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &g.CreatedAt, &g.UpdatedAt, &g.MemberCount); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to read support group")
			return
		}
		groups = append(groups, g)
	}
	if rows.Err() != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to read support groups")
		return
	}

	writePage(w, p, total, groups)
}

// HandleSupportGroupDetail serves GET /api/v1/support-groups/{id}, including
// its technicians and the device groups it reaches. This is the screen where
// the authorisation model is administered, so it returns both sides of the
// grant rather than making the portal assemble them.
func (h *Handler) HandleSupportGroupDetail(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.viewer(w, r); !ok {
		return
	}
	groupID, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}

	var g SupportGroup
	err := h.db.QueryRow(r.Context(), `
		SELECT id, name, description, created_at, updated_at
		FROM support_groups WHERE id = $1`, groupID).
		Scan(&g.ID, &g.Name, &g.Description, &g.CreatedAt, &g.UpdatedAt)
	if err != nil {
		dbError(w, err, "failed to read support group")
		return
	}

	techRows, err := h.db.Query(r.Context(), `
		SELECT u.id, u.email, u.display_name, u.active
		FROM users u
		JOIN user_support_groups m ON m.user_id = u.id
		WHERE m.support_group_id = $1
		ORDER BY u.display_name`, groupID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to read technicians")
		return
	}
	defer techRows.Close()

	g.Technicians = make([]Technician, 0)
	for techRows.Next() {
		var t Technician
		if err := techRows.Scan(&t.ID, &t.Email, &t.DisplayName, &t.Active); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to read technician")
			return
		}
		g.Technicians = append(g.Technicians, t)
	}
	if techRows.Err() != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to read technicians")
		return
	}
	g.MemberCount = int64(len(g.Technicians))

	dgRows, err := h.db.Query(r.Context(), `
		SELECT dg.id, dg.name
		FROM device_groups dg
		JOIN support_group_device_groups sgdg ON sgdg.device_group_id = dg.id
		WHERE sgdg.support_group_id = $1
		ORDER BY dg.name`, groupID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to read device groups")
		return
	}
	defer dgRows.Close()

	g.DeviceGroups = make([]GroupSummary, 0)
	for dgRows.Next() {
		var s GroupSummary
		if err := dgRows.Scan(&s.ID, &s.Name); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to read device group")
			return
		}
		g.DeviceGroups = append(g.DeviceGroups, s)
	}
	if dgRows.Err() != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to read device groups")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, g)
}

// HandleCreateSupportGroup serves POST /api/v1/support-groups.
func (h *Handler) HandleCreateSupportGroup(w http.ResponseWriter, r *http.Request) {
	user, ok := h.admin(w, r)
	if !ok {
		return
	}

	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeJSONError(w, http.StatusBadRequest, "name is required")
		return
	}

	var g SupportGroup
	err := h.db.QueryRow(r.Context(), `
		INSERT INTO support_groups (name, description)
		VALUES ($1, $2)
		RETURNING id, name, description, created_at, updated_at`,
		strings.TrimSpace(req.Name), nullableString(req.Description)).
		Scan(&g.ID, &g.Name, &g.Description, &g.CreatedAt, &g.UpdatedAt)
	if err != nil {
		dbError(w, err, "failed to create support group")
		return
	}

	h.record(r.Context(), audit.Event{
		Type:           "support_group.created",
		ActorID:        user.ID,
		Resource:       "support_group",
		ResourceID:     g.ID.String(),
		SupportGroupID: &g.ID,
		Description:    "Support group created: " + g.Name,
	})

	httpx.WriteJSON(w, http.StatusCreated, g)
}

// HandleUpdateSupportGroup serves PATCH /api/v1/support-groups/{id}.
func (h *Handler) HandleUpdateSupportGroup(w http.ResponseWriter, r *http.Request) {
	user, ok := h.admin(w, r)
	if !ok {
		return
	}
	groupID, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}

	var req struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
	}
	if !decodeBody(w, r, &req) {
		return
	}

	var g SupportGroup
	err := h.db.QueryRow(r.Context(), `
		UPDATE support_groups
		SET name        = COALESCE($2, name),
		    description = CASE WHEN $3::boolean THEN $4 ELSE description END,
		    updated_at  = now()
		WHERE id = $1
		RETURNING id, name, description, created_at, updated_at`,
		groupID, req.Name, req.Description != nil, derefOrNil(req.Description)).
		Scan(&g.ID, &g.Name, &g.Description, &g.CreatedAt, &g.UpdatedAt)
	if err != nil {
		dbError(w, err, "failed to update support group")
		return
	}

	h.record(r.Context(), audit.Event{
		Type:           "support_group.updated",
		ActorID:        user.ID,
		Resource:       "support_group",
		ResourceID:     g.ID.String(),
		SupportGroupID: &g.ID,
		Description:    "Support group updated: " + g.Name,
	})

	httpx.WriteJSON(w, http.StatusOK, g)
}

// HandleDeleteSupportGroup serves DELETE /api/v1/support-groups/{id}.
func (h *Handler) HandleDeleteSupportGroup(w http.ResponseWriter, r *http.Request) {
	user, ok := h.admin(w, r)
	if !ok {
		return
	}
	groupID, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}

	var name string
	if err := h.db.QueryRow(r.Context(), `SELECT name FROM support_groups WHERE id = $1`, groupID).Scan(&name); err != nil {
		dbError(w, err, "failed to read support group")
		return
	}

	if _, err := h.db.Exec(r.Context(), `DELETE FROM support_groups WHERE id = $1`, groupID); err != nil {
		dbError(w, err, "failed to delete support group")
		return
	}

	h.record(r.Context(), audit.Event{
		Type:        "support_group.deleted",
		ActorID:     user.ID,
		Resource:    "support_group",
		ResourceID:  groupID.String(),
		Description: "Support group deleted: " + name,
		Metadata:    map[string]any{"name": name},
	})

	httpx.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}

// HandleAddTechnician serves POST /api/v1/support-groups/{id}/technicians.
func (h *Handler) HandleAddTechnician(w http.ResponseWriter, r *http.Request) {
	user, ok := h.admin(w, r)
	if !ok {
		return
	}
	groupID, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}

	var req struct {
		UserID int64 `json:"user_id"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if req.UserID == 0 {
		writeJSONError(w, http.StatusBadRequest, "user_id is required")
		return
	}

	if _, err := h.db.Exec(r.Context(), `
		INSERT INTO user_support_groups (user_id, support_group_id)
		VALUES ($1, $2) ON CONFLICT DO NOTHING`, req.UserID, groupID); err != nil {
		dbError(w, err, "failed to add technician")
		return
	}

	h.record(r.Context(), audit.Event{
		Type:           "support_group.technician_added",
		ActorID:        user.ID,
		Resource:       "support_group",
		ResourceID:     groupID.String(),
		SupportGroupID: &groupID,
		Description:    "Technician added to support group",
		Metadata:       map[string]any{"user_id": req.UserID},
	})

	httpx.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}

// HandleRemoveTechnician serves
// DELETE /api/v1/support-groups/{id}/technicians/{userId}.
func (h *Handler) HandleRemoveTechnician(w http.ResponseWriter, r *http.Request) {
	user, ok := h.admin(w, r)
	if !ok {
		return
	}
	groupID, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	targetID, err := strconv.ParseInt(r.PathValue("userId"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid userId")
		return
	}

	// Which devices the group reaches, read before the membership is removed so
	// the answer is the set the departing technician could reach.
	reachable, err := h.devicesReachableBySupportGroup(r.Context(), groupID)
	if err != nil {
		dbError(w, err, "failed to read the group's devices")
		return
	}

	tag, err := h.db.Exec(r.Context(),
		`DELETE FROM user_support_groups WHERE support_group_id = $1 AND user_id = $2`, groupID, targetID)
	if err != nil {
		dbError(w, err, "failed to remove technician")
		return
	}
	if tag.RowsAffected() == 0 {
		writeJSONError(w, http.StatusNotFound, "technician is not a member of this group")
		return
	}

	h.record(r.Context(), audit.Event{
		Type:           "support_group.technician_removed",
		ActorID:        user.ID,
		Resource:       "support_group",
		ResourceID:     groupID.String(),
		SupportGroupID: &groupID,
		Description:    "Technician removed from support group",
		Metadata:       map[string]any{"user_id": targetID},
	})

	// Removing the membership stops the portal showing them the device. It does
	// nothing about a password they were already shown, and the device would go
	// on accepting it. Rotating closes that, and is the reason 3.3 exists.
	rotated := h.rotateForAccessChange(r.Context(), user.ID, reachable,
		"technician removed from support group", "support_group", groupID.String())

	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"success":           true,
		"passwords_rotated": rotated,
		"rotation_in_force": "at each device's next heartbeat",
	})
}

// HandleAddSupportGroupDeviceGroup serves
// POST /api/v1/support-groups/{id}/device-groups. This is the grant that makes
// devices visible in a technician's address book.
func (h *Handler) HandleAddSupportGroupDeviceGroup(w http.ResponseWriter, r *http.Request) {
	user, ok := h.admin(w, r)
	if !ok {
		return
	}
	groupID, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}

	var req struct {
		DeviceGroupID string `json:"device_group_id"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	deviceGroupID, err := uuid.Parse(strings.TrimSpace(req.DeviceGroupID))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "device_group_id is required")
		return
	}

	if _, err := h.db.Exec(r.Context(), `
		INSERT INTO support_group_device_groups (support_group_id, device_group_id)
		VALUES ($1, $2) ON CONFLICT DO NOTHING`, groupID, deviceGroupID); err != nil {
		dbError(w, err, "failed to grant device group")
		return
	}

	h.record(r.Context(), audit.Event{
		Type:           "support_group.device_group_granted",
		ActorID:        user.ID,
		Resource:       "support_group",
		ResourceID:     groupID.String(),
		SupportGroupID: &groupID,
		Description:    "Device group granted to support group",
		Metadata:       map[string]any{"device_group_id": deviceGroupID.String()},
	})

	httpx.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}

// HandleRemoveSupportGroupDeviceGroup serves
// DELETE /api/v1/support-groups/{id}/device-groups/{groupId}.
func (h *Handler) HandleRemoveSupportGroupDeviceGroup(w http.ResponseWriter, r *http.Request) {
	user, ok := h.admin(w, r)
	if !ok {
		return
	}
	groupID, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	deviceGroupID, ok := pathUUID(w, r, "groupId")
	if !ok {
		return
	}

	// Only the devices in the group being revoked, not the whole support group's
	// reach: the technicians keep legitimate access to everything else, and
	// rotating those passwords too would be churn with no security value.
	affected, err := h.devicesInDeviceGroup(r.Context(), deviceGroupID)
	if err != nil {
		dbError(w, err, "failed to read the device group's devices")
		return
	}

	tag, err := h.db.Exec(r.Context(),
		`DELETE FROM support_group_device_groups WHERE support_group_id = $1 AND device_group_id = $2`,
		groupID, deviceGroupID)
	if err != nil {
		dbError(w, err, "failed to revoke device group")
		return
	}
	if tag.RowsAffected() == 0 {
		writeJSONError(w, http.StatusNotFound, "device group is not granted to this support group")
		return
	}

	h.record(r.Context(), audit.Event{
		Type:           "support_group.device_group_revoked",
		ActorID:        user.ID,
		Resource:       "support_group",
		ResourceID:     groupID.String(),
		SupportGroupID: &groupID,
		Description:    "Device group revoked from support group",
		Metadata:       map[string]any{"device_group_id": deviceGroupID.String()},
	})

	rotated := h.rotateForAccessChange(r.Context(), user.ID, affected,
		"device group revoked from support group", "support_group", groupID.String())

	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"success":           true,
		"passwords_rotated": rotated,
		"rotation_in_force": "at each device's next heartbeat",
	})
}
