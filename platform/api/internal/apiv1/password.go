package apiv1

import (
	"context"
	"errors"
	"net/http"

	"github.com/OpenDeskViewer/platform/api/internal/audit"
	"github.com/OpenDeskViewer/platform/api/internal/devicepw"
	"github.com/OpenDeskViewer/platform/api/internal/httpx"
	"github.com/google/uuid"
)

// Platform-managed device connection passwords, from the portal's side.
//
// The point of these two endpoints is that "who may reach this machine" stops
// being a claim about rows in our database. A technician gets the password only
// after CanAccessDevice says so, and only with a line in the audit trail saying
// they were given it. Withdrawing access rotates the password, which is an
// action on the machine rather than a label on a row.

// passwordResponse is the shape both endpoints answer with. Applied is the
// field that keeps this honest: a rotation is not in force until the device has
// confirmed it, and a portal that showed only the new password would be telling
// an operator that access was withdrawn from a machine that is still accepting
// the old one.
type passwordResponse struct {
	Password       string  `json:"password"`
	Version        int64   `json:"version"`
	AppliedVersion *int64  `json:"applied_version"`
	Applied        bool    `json:"applied"`
	AppliedAt      *string `json:"applied_at"`
	RotatedAt      string  `json:"rotated_at"`
	// DeliveredAtHeartbeat says the same thing the control channel says: the
	// device learns about this on its next poll, not now.
	DeliveredAtHeartbeat bool `json:"delivered_at_heartbeat"`
}

func newPasswordResponse(p *devicepw.Password) passwordResponse {
	resp := passwordResponse{
		Password:             p.Value,
		Version:              p.Version,
		AppliedVersion:       p.AppliedVersion,
		Applied:              p.Applied(),
		RotatedAt:            p.RotatedAt.UTC().Format(timeFormat),
		DeliveredAtHeartbeat: true,
	}
	if p.AppliedAt != nil {
		at := p.AppliedAt.UTC().Format(timeFormat)
		resp.AppliedAt = &at
	}
	return resp
}

const timeFormat = "2006-01-02T15:04:05Z07:00"

// HandleDevicePassword serves GET /api/v1/devices/{id}/password.
//
// A technician with access to the device may read it. That is the whole point:
// the alternative, which is what this replaced, is a password set once at
// provisioning and passed around out of band, which nobody can revoke and
// nobody can audit.
func (h *Handler) HandleDevicePassword(w http.ResponseWriter, r *http.Request) {
	deviceID, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	userID, ok := h.authoriseDevice(w, r, deviceID)
	if !ok {
		return
	}
	if !h.passwordsAvailable(w) {
		return
	}

	password, err := h.devicepw.Reveal(r.Context(), deviceID)
	if errors.Is(err, devicepw.ErrNoPassword) {
		// A device that has never contacted the platform has no password yet.
		// Generating one here would be worse than saying so: it would hand the
		// technician a credential the machine has never heard of.
		writeJSONError(w, http.StatusNotFound,
			"this device has no platform-managed password yet; it receives one on its first heartbeat")
		return
	}
	if err != nil {
		dbError(w, err, "failed to read the device password")
		return
	}

	// Reading a credential is a fact about a person, not about a device, and it
	// is the record that makes the difference between "the platform can hand out
	// passwords" and "the platform can account for who has one".
	h.record(r.Context(), audit.Event{
		Type:        "device.password_revealed",
		ActorID:     userID,
		Resource:    "device",
		ResourceID:  deviceID.String(),
		DeviceID:    &deviceID,
		Description: "Device connection password shown to a technician",
		Metadata:    map[string]any{"version": password.Version},
	})

	noStore(w)
	httpx.WriteJSON(w, http.StatusOK, newPasswordResponse(password))
}

// HandleRotateDevicePassword serves POST /api/v1/devices/{id}/password/rotate.
//
// An administrator's, not a technician's. Rotation is how access is taken away,
// and a technician who has just been removed from a support group should not be
// able to perform it on the way out; more practically, it is the same decision
// as pushing configuration, which is already an administrator's (PUT strategy).
func (h *Handler) HandleRotateDevicePassword(w http.ResponseWriter, r *http.Request) {
	deviceID, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	user, ok := h.admin(w, r)
	if !ok {
		return
	}
	if !h.passwordsAvailable(w) {
		return
	}

	// A device row has to exist, or the insert fails on the foreign key and the
	// caller gets a 500 for what is really a 404.
	var exists bool
	if err := h.db.QueryRow(r.Context(),
		`SELECT EXISTS (SELECT 1 FROM devices WHERE id = $1)`, deviceID).Scan(&exists); err != nil {
		dbError(w, err, "failed to look up the device")
		return
	}
	if !exists {
		writeJSONError(w, http.StatusNotFound, "not found")
		return
	}

	password, err := h.devicepw.Rotate(r.Context(), deviceID, &user.ID)
	if err != nil {
		dbError(w, err, "failed to rotate the device password")
		return
	}

	h.record(r.Context(), audit.Event{
		Type:        "device.password_rotated",
		ActorID:     user.ID,
		Resource:    "device",
		ResourceID:  deviceID.String(),
		DeviceID:    &deviceID,
		Description: "Device connection password rotated",
		Metadata:    map[string]any{"version": password.Version},
	})

	noStore(w)
	httpx.WriteJSON(w, http.StatusOK, newPasswordResponse(password))
}

// rotateForAccessChange rotates every device a caller could reach, after that
// reach has been withdrawn.
//
// This is what closes the gap the README used to have to admit to: removing a
// technician from a support group stopped them seeing the device in the portal
// and did nothing about the password they had already been shown. Rotating on
// the way out means the credential they hold stops working, once the device
// next checks in.
//
// The withdrawal has three shapes -- a technician leaves a support group, a
// device group leaves a support group, and a user is deactivated or removed --
// and all three leak the same credential, so all three come through here. The
// resource pair names which one it was, because "why was this password rotated"
// is a question the audit trail has to answer.
//
// It deliberately does not fail the request that triggered it. The access change
// itself has already committed and is the thing the operator asked for; a
// rotation that could not be written is reported in the audit trail and in the
// response rather than rolling back a revocation.
func (h *Handler) rotateForAccessChange(ctx context.Context, actorID int64, deviceIDs []uuid.UUID, reason, resource, resourceID string) int {
	if h.devicepw == nil || len(deviceIDs) == 0 {
		return 0
	}

	rotated, err := h.devicepw.RotateMany(ctx, deviceIDs, &actorID)

	metadata := map[string]any{
		"reason":         reason,
		"devices":        len(deviceIDs),
		"rotated":        rotated,
		"resource":       resource,
		"resource_id":    resourceID,
		"in_force_after": "the next heartbeat of each device",
	}
	if err != nil {
		metadata["error"] = err.Error()
	}

	h.record(ctx, audit.Event{
		Type:        "device.passwords_rotated_on_access_change",
		ActorID:     actorID,
		Resource:    resource,
		ResourceID:  resourceID,
		Description: "Device passwords rotated because access was withdrawn",
		Metadata:    metadata,
	})

	return rotated
}

// devicesReachableByUser lists every device this user can reach, through every
// support group they belong to. It is the set whose passwords they could have
// read, which is what deactivation and removal have to rotate.
func (h *Handler) devicesReachableByUser(ctx context.Context, userID int64) ([]uuid.UUID, error) {
	return h.deviceIDs(ctx, `
		SELECT DISTINCT m.device_id
		FROM user_support_groups usg
		JOIN support_group_device_groups sgdg ON sgdg.support_group_id = usg.support_group_id
		JOIN device_group_members m ON m.device_group_id = sgdg.device_group_id
		WHERE usg.user_id = $1
	`, userID)
}

// devicesReachableBySupportGroup lists the devices a support group can reach,
// through the device groups granted to it.
func (h *Handler) devicesReachableBySupportGroup(ctx context.Context, supportGroupID uuid.UUID) ([]uuid.UUID, error) {
	return h.deviceIDs(ctx, `
		SELECT DISTINCT m.device_id
		FROM support_group_device_groups sgdg
		JOIN device_group_members m ON m.device_group_id = sgdg.device_group_id
		WHERE sgdg.support_group_id = $1
	`, supportGroupID)
}

// devicesInDeviceGroup lists the devices in one device group.
func (h *Handler) devicesInDeviceGroup(ctx context.Context, deviceGroupID uuid.UUID) ([]uuid.UUID, error) {
	return h.deviceIDs(ctx,
		`SELECT device_id FROM device_group_members WHERE device_group_id = $1`, deviceGroupID)
}

func (h *Handler) deviceIDs(ctx context.Context, query string, args ...any) ([]uuid.UUID, error) {
	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// passwordsAvailable guards the two endpoints against a handler built without a
// password service. config.ValidateAPI makes that impossible for the running
// API; it is reachable from a test that constructs a partial handler, and a nil
// dereference there would look like a crash rather than a missing dependency.
func (h *Handler) passwordsAvailable(w http.ResponseWriter) bool {
	if h.devicepw == nil {
		writeJSONError(w, http.StatusServiceUnavailable,
			"device passwords are not configured on this deployment")
		return false
	}
	return true
}

// noStore keeps a credential out of any cache between here and the browser.
func noStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
}
