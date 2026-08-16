package apiv1

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/OpenDeskViewer/platform/api/internal/audit"
	"github.com/OpenDeskViewer/platform/api/internal/httpx"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// The heartbeat control channel, from the portal's side.
//
// A device polls /api/heartbeat every 15 seconds and applies what it is given:
// connection ids to terminate, and configuration to write. These two endpoints
// are how an operator puts something there. Before this, the platform could
// record a revocation in its own database and the device would never hear about
// it, which made "revocation" a label on a row rather than an action.

// pushableOptions is the allowlist of client configuration keys the platform
// will push.
//
// The boundary is deliberate and is the other half of 1.3. The keys that decide
// which deployment a client belongs to - custom-rendezvous-server, api-server,
// relay-server, key - are exactly what an attacker would want to change, and
// they are meant to be locked at build time via OVERWRITE_SETTINGS. Pushing
// them from here would reopen that door from the inside, so they are not in
// this list and adding them needs a decision, not an edit.
//
// Everything here is a policy setting: what the person at the device may do,
// and what the session records.
var pushableOptions = map[string]bool{
	"enable-audio":                     true,
	"enable-clipboard":                 true,
	"enable-file-transfer":             true,
	"enable-remote-restart":            true,
	"enable-tunnel":                    true,
	"enable-record-session":            true,
	"enable-block-input":               true,
	"allow-remote-config-modification": true,
	"approve-mode":                     true,
	"verification-method":              true,
	"temporary-password-length":        true,
	"custom-rendezvous-port":           false,
}

// HandleDeviceDisconnect serves POST /api/v1/devices/{id}/disconnect.
//
// The body may name connection ids; with none, every session the platform
// believes is open on that device is ended. The ids are the client's own
// connection ids, which is what the audit trail records as client_id, so the
// portal can offer "end this session" from the session list.
func (h *Handler) HandleDeviceDisconnect(w http.ResponseWriter, r *http.Request) {
	deviceID, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	userID, ok := h.authoriseDevice(w, r, deviceID)
	if !ok {
		return
	}

	var req struct {
		ConnIDs []int32 `json:"conn_ids"`
	}
	// An empty body is valid and means "all of them", so a decode failure is
	// only an error when there was something to decode.
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}

	connIDs := req.ConnIDs
	if len(connIDs) == 0 {
		var err error
		connIDs, err = h.openConnIDs(r, deviceID)
		if err != nil {
			dbError(w, err, "failed to read open sessions")
			return
		}
	}

	if len(connIDs) == 0 {
		// Nothing to do is a success, not a 404: the caller asked for the
		// device to have no live sessions, and it has none.
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"queued": 0})
		return
	}

	for _, connID := range connIDs {
		if _, err := h.db.Exec(r.Context(), `
			INSERT INTO device_disconnect_requests (device_id, conn_id, requested_by)
			VALUES ($1, $2, $3)
		`, deviceID, connID, userID); err != nil {
			dbError(w, err, "failed to queue disconnect")
			return
		}
	}

	h.record(r.Context(), audit.Event{
		Type:        "device.disconnect_requested",
		ActorID:     userID,
		Resource:    "device",
		ResourceID:  deviceID.String(),
		DeviceID:    &deviceID,
		Description: "Requested that live sessions be terminated",
		Metadata:    map[string]any{"conn_ids": connIDs},
	})

	// Deliberately honest about what has happened: the request is queued for
	// the next heartbeat, not executed. The client polls every 15 seconds.
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"queued":                 len(connIDs),
		"delivered_at_heartbeat": true,
	})
}

// openConnIDs reads the connection ids the audit trail shows as still open on
// this device. connection_sessions.client_id holds "<session_id>/<conn_id>" for
// records the client filed, so the conn id is the part after the slash.
func (h *Handler) openConnIDs(r *http.Request, deviceID uuid.UUID) ([]int32, error) {
	rows, err := h.db.Query(r.Context(), `
		SELECT DISTINCT split_part(client_id, '/', 2)::int
		FROM connection_sessions
		WHERE device_id = $1
		  AND status IN ('REQUESTED', 'STARTED')
		  AND client_id ~ '^[0-9]+/[0-9]+$'
	`, deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []int32
	for rows.Next() {
		var id int32
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// HandleDeviceStrategy serves GET and PUT /api/v1/devices/{id}/strategy.
func (h *Handler) HandleDeviceStrategy(w http.ResponseWriter, r *http.Request) {
	deviceID, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}

	if r.Method == http.MethodGet {
		// Configuration is a fact about the device, not a way onto it, so this
		// is the read-shaped check and a Read Only auditor may see it.
		if _, ok := h.authoriseDeviceView(w, r, deviceID); !ok {
			return
		}
		h.readStrategy(w, r, deviceID)
		return
	}

	// Writing configuration to a machine is an administrator's job, not a
	// technician's, even a technician who may connect to it.
	user, ok := h.admin(w, r)
	if !ok {
		return
	}
	h.writeStrategy(w, r, deviceID, user.ID)
}

func (h *Handler) readStrategy(w http.ResponseWriter, r *http.Request, deviceID uuid.UUID) {
	options := map[string]string{}
	var modifiedAt int64

	err := h.db.QueryRow(r.Context(), `
		SELECT config_options, modified_at FROM device_strategies WHERE device_id = $1
	`, deviceID).Scan(&options, &modifiedAt)
	// No row means no configuration has been pushed, which is a valid state
	// and not a 404: the device exists and has default settings.
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		dbError(w, err, "failed to read strategy")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"config_options": options,
		"modified_at":    modifiedAt,
		"pushable_keys":  pushableKeys(),
	})
}

func (h *Handler) writeStrategy(w http.ResponseWriter, r *http.Request, deviceID uuid.UUID, userID int64) {
	var req struct {
		ConfigOptions map[string]string `json:"config_options"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	for key := range req.ConfigOptions {
		if !pushableOptions[key] {
			// Naming the key is fine here: the caller is an authenticated
			// administrator, and a silent drop would be a configuration that
			// looks applied and is not.
			writeJSONError(w, http.StatusBadRequest,
				"config option "+key+" may not be pushed; server identity keys are locked at the client build")
			return
		}
	}
	if req.ConfigOptions == nil {
		req.ConfigOptions = map[string]string{}
	}

	// The device only reapplies configuration when modified_at changes, so the
	// timestamp is the version. Seconds, because that is what the client parses
	// out of its own strategy_timestamp.
	modifiedAt := time.Now().Unix()

	if _, err := h.db.Exec(r.Context(), `
		INSERT INTO device_strategies (device_id, config_options, modified_at, updated_by)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (device_id) DO UPDATE
		SET config_options = EXCLUDED.config_options,
		    modified_at = EXCLUDED.modified_at,
		    updated_by = EXCLUDED.updated_by,
		    updated_at = now()
	`, deviceID, req.ConfigOptions, modifiedAt, userID); err != nil {
		dbError(w, err, "failed to store strategy")
		return
	}

	h.record(r.Context(), audit.Event{
		Type:        "device.strategy_updated",
		ActorID:     userID,
		Resource:    "device",
		ResourceID:  deviceID.String(),
		DeviceID:    &deviceID,
		Description: "Device configuration pushed",
		Metadata:    map[string]any{"config_options": req.ConfigOptions},
	})

	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"config_options":         req.ConfigOptions,
		"modified_at":            modifiedAt,
		"delivered_at_heartbeat": true,
	})
}

// HandleDeviceObservations serves GET /api/v1/device-observations: the ids that
// tried to report in without a credential.
//
// This is what replaced silent auto-registration. An operator needs to see them,
// because "the device I shipped is not appearing" and "somebody is probing the
// heartbeat endpoint" look identical from the fleet list, and different here.
func (h *Handler) HandleDeviceObservations(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.viewer(w, r); !ok {
		return
	}

	p := parsePage(r)

	var total int64
	if err := h.db.QueryRow(r.Context(),
		`SELECT COUNT(*) FROM device_observations`).Scan(&total); err != nil {
		dbError(w, err, "failed to count observations")
		return
	}

	rows, err := h.db.Query(r.Context(), `
		SELECT rustdesk_id, device_uuid, host(client_ip), first_seen_at, last_seen_at, sightings
		FROM device_observations
		ORDER BY last_seen_at DESC
		LIMIT $1 OFFSET $2
	`, p.PageSize, p.Offset)
	if err != nil {
		dbError(w, err, "failed to list observations")
		return
	}
	defer rows.Close()

	type observation struct {
		RustdeskID  string    `json:"rustdesk_id"`
		DeviceUUID  *string   `json:"device_uuid"`
		ClientIP    *string   `json:"client_ip"`
		FirstSeenAt time.Time `json:"first_seen_at"`
		LastSeenAt  time.Time `json:"last_seen_at"`
		Sightings   int64     `json:"sightings"`
	}

	observations := []observation{}
	for rows.Next() {
		var o observation
		if err := rows.Scan(&o.RustdeskID, &o.DeviceUUID, &o.ClientIP,
			&o.FirstSeenAt, &o.LastSeenAt, &o.Sightings); err != nil {
			dbError(w, err, "failed to read observations")
			return
		}
		observations = append(observations, o)
	}
	if err := rows.Err(); err != nil {
		dbError(w, err, "failed to read observations")
		return
	}

	writePage(w, p, total, observations)
}

func pushableKeys() []string {
	keys := make([]string, 0, len(pushableOptions))
	for key, allowed := range pushableOptions {
		if allowed {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}
