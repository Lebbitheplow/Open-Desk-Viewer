package apiv1

import (
	"net/http"
	"strings"
	"time"

	"github.com/OpenDeskViewer/platform/api/internal/audit"
	"github.com/OpenDeskViewer/platform/api/internal/httpx"
	"github.com/google/uuid"
)

// Monitoring and notifications, items 6.5 and 6.6, from the portal's side.

// ConnectivityEvent is one recorded change of a device's connectivity.
type ConnectivityEvent struct {
	ID                      int64     `json:"id"`
	DeviceID                uuid.UUID `json:"device_id"`
	DeviceName              string    `json:"device_name"`
	Connectivity            string    `json:"connectivity"`
	PreviousDurationSeconds *int64    `json:"previous_duration_seconds"`
	OccurredAt              time.Time `json:"occurred_at"`
}

// HandleDeviceConnectivityHistory serves
// GET /api/v1/devices/{id}/connectivity.
//
// A read about one device, so it takes the read-shaped gate: a technician who
// can reach the device, and a Read Only auditor.
func (h *Handler) HandleDeviceConnectivityHistory(w http.ResponseWriter, r *http.Request) {
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
		`SELECT COUNT(*) FROM device_connectivity_events WHERE device_id = $1`, deviceID).Scan(&total); err != nil {
		dbError(w, err, "failed to count connectivity events")
		return
	}

	rows, err := h.db.Query(r.Context(), `
		SELECT e.id, e.device_id, d.name, e.connectivity, e.previous_duration_seconds, e.occurred_at
		FROM device_connectivity_events e
		JOIN devices d ON d.id = e.device_id
		WHERE e.device_id = $1
		ORDER BY e.occurred_at DESC
		LIMIT $2 OFFSET $3`, deviceID, p.PageSize, p.Offset)
	if err != nil {
		dbError(w, err, "failed to read connectivity events")
		return
	}
	defer rows.Close()

	events, err := scanConnectivityEvents(rows)
	if err != nil {
		dbError(w, err, "failed to read connectivity events")
		return
	}

	writePage(w, p, total, events)
}

// HandleFleetConnectivityHistory serves GET /api/v1/monitoring/events.
//
// The fleet-wide view, which is what "how often is this fleet dropping out"
// actually needs. Administrator, manager or auditor: a technician's answer over
// their own support group would be a different and more misleading number.
func (h *Handler) HandleFleetConnectivityHistory(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.viewer(w, r); !ok {
		return
	}

	p := parsePage(r)

	// Optional filters. `since` is the one an operator reaches for first, so it
	// defaults to the last seven days rather than to the whole history: an
	// unbounded first page of a year of events is a slow query that answers a
	// question nobody asked.
	since := time.Now().Add(-7 * 24 * time.Hour)
	if raw := strings.TrimSpace(r.URL.Query().Get("since")); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "since must be an RFC 3339 timestamp")
			return
		}
		since = parsed
	}
	connectivity := strings.TrimSpace(r.URL.Query().Get("connectivity"))

	var total int64
	if err := h.db.QueryRow(r.Context(), `
		SELECT COUNT(*) FROM device_connectivity_events
		WHERE occurred_at >= $1 AND ($2 = '' OR connectivity = $2)`,
		since, connectivity).Scan(&total); err != nil {
		dbError(w, err, "failed to count connectivity events")
		return
	}

	rows, err := h.db.Query(r.Context(), `
		SELECT e.id, e.device_id, d.name, e.connectivity, e.previous_duration_seconds, e.occurred_at
		FROM device_connectivity_events e
		JOIN devices d ON d.id = e.device_id
		WHERE e.occurred_at >= $1 AND ($2 = '' OR e.connectivity = $2)
		ORDER BY e.occurred_at DESC
		LIMIT $3 OFFSET $4`, since, connectivity, p.PageSize, p.Offset)
	if err != nil {
		dbError(w, err, "failed to read connectivity events")
		return
	}
	defer rows.Close()

	events, err := scanConnectivityEvents(rows)
	if err != nil {
		dbError(w, err, "failed to read connectivity events")
		return
	}

	writePage(w, p, total, events)
}

func scanConnectivityEvents(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
}) ([]ConnectivityEvent, error) {
	events := []ConnectivityEvent{}
	for rows.Next() {
		var e ConnectivityEvent
		if err := rows.Scan(&e.ID, &e.DeviceID, &e.DeviceName, &e.Connectivity,
			&e.PreviousDurationSeconds, &e.OccurredAt); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// ---------------------------------------------------------------------------
// Notification targets
// ---------------------------------------------------------------------------

// NotificationTarget is a webhook the platform posts events to.
//
// The secret is never returned. It is write-only from the portal's point of
// view: an operator who has lost it sets a new one, and a screen that displays
// it is a screen that leaks it to anyone who can read a device list.
type NotificationTarget struct {
	ID                  uuid.UUID  `json:"id"`
	Name                string     `json:"name"`
	URL                 string     `json:"url"`
	HasSecret           bool       `json:"has_secret"`
	EventTypes          []string   `json:"event_types"`
	Enabled             bool       `json:"enabled"`
	LastSuccessAt       *time.Time `json:"last_success_at"`
	LastFailureAt       *time.Time `json:"last_failure_at"`
	LastFailureReason   *string    `json:"last_failure_reason"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	CreatedAt           time.Time  `json:"created_at"`
}

// HandleNotificationTargets serves GET /api/v1/notification-targets.
func (h *Handler) HandleNotificationTargets(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.viewer(w, r); !ok {
		return
	}

	rows, err := h.db.Query(r.Context(), `
		SELECT id, name, url, secret IS NOT NULL AND secret <> '', event_types, enabled,
		       last_success_at, last_failure_at, last_failure_reason, consecutive_failures, created_at
		FROM notification_targets
		ORDER BY name`)
	if err != nil {
		dbError(w, err, "failed to list notification targets")
		return
	}
	defer rows.Close()

	targets := []NotificationTarget{}
	for rows.Next() {
		var t NotificationTarget
		if err := rows.Scan(&t.ID, &t.Name, &t.URL, &t.HasSecret, &t.EventTypes, &t.Enabled,
			&t.LastSuccessAt, &t.LastFailureAt, &t.LastFailureReason,
			&t.ConsecutiveFailures, &t.CreatedAt); err != nil {
			dbError(w, err, "failed to read notification targets")
			return
		}
		targets = append(targets, t)
	}
	if err := rows.Err(); err != nil {
		dbError(w, err, "failed to read notification targets")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": targets, "total": len(targets)})
}

// HandleCreateNotificationTarget serves POST /api/v1/notification-targets.
func (h *Handler) HandleCreateNotificationTarget(w http.ResponseWriter, r *http.Request) {
	user, ok := h.admin(w, r)
	if !ok {
		return
	}

	var req struct {
		Name       string   `json:"name"`
		URL        string   `json:"url"`
		Secret     string   `json:"secret"`
		EventTypes []string `json:"event_types"`
		Enabled    *bool    `json:"enabled"`
	}
	if !decodeBody(w, r, &req) {
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.URL = strings.TrimSpace(req.URL)
	if req.Name == "" {
		writeJSONError(w, http.StatusBadRequest, "name is required")
		return
	}
	// https only. The payload names devices and customers, and a webhook is one
	// of the few places this platform deliberately sends data off its own
	// network; sending it in the clear would be a worse leak than anything the
	// portal shows.
	if !strings.HasPrefix(req.URL, "https://") {
		writeJSONError(w, http.StatusBadRequest,
			"url must be https: the payload names devices and customers and leaves this network")
		return
	}
	if req.EventTypes == nil {
		req.EventTypes = []string{}
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	var secret *string
	if req.Secret != "" {
		secret = &req.Secret
	}

	var id uuid.UUID
	if err := h.db.QueryRow(r.Context(), `
		INSERT INTO notification_targets (name, url, secret, event_types, enabled, created_by)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id`,
		req.Name, req.URL, secret, req.EventTypes, enabled, user.ID).Scan(&id); err != nil {
		dbError(w, err, "failed to create the notification target")
		return
	}

	// The URL is recorded, the secret is not. The audit trail is read by the
	// same people the secret is being kept from.
	h.record(r.Context(), audit.Event{
		Type:        "notification_target.created",
		ActorID:     user.ID,
		Resource:    "notification_target",
		ResourceID:  id.String(),
		Description: "Notification target created: " + req.Name,
		Metadata: map[string]any{
			"url":         req.URL,
			"event_types": req.EventTypes,
			"signed":      secret != nil,
		},
	})

	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"id": id, "name": req.Name})
}

// HandleDeleteNotificationTarget serves
// DELETE /api/v1/notification-targets/{id}.
func (h *Handler) HandleDeleteNotificationTarget(w http.ResponseWriter, r *http.Request) {
	user, ok := h.admin(w, r)
	if !ok {
		return
	}
	targetID, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}

	tag, err := h.db.Exec(r.Context(), `DELETE FROM notification_targets WHERE id = $1`, targetID)
	if err != nil {
		dbError(w, err, "failed to delete the notification target")
		return
	}
	if tag.RowsAffected() == 0 {
		writeJSONError(w, http.StatusNotFound, "not found")
		return
	}

	h.record(r.Context(), audit.Event{
		Type:        "notification_target.deleted",
		ActorID:     user.ID,
		Resource:    "notification_target",
		ResourceID:  targetID.String(),
		Description: "Notification target deleted",
	})

	httpx.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}

// HandleNotificationDeliveries serves GET /api/v1/notification-deliveries.
//
// The outbox, which is the answer to "did the alert go out". Abandoned rows are
// kept precisely so this can show them: an operator investigating an incident
// needs to know which notifications never arrived, and a queue that deletes its
// failures cannot tell them.
func (h *Handler) HandleNotificationDeliveries(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.viewer(w, r); !ok {
		return
	}

	p := parsePage(r)
	// state=pending|delivered|abandoned, empty for all.
	state := strings.TrimSpace(r.URL.Query().Get("state"))

	const filter = `
		WHERE ($1 = ''
		   OR ($1 = 'pending'   AND delivered_at IS NULL AND abandoned_at IS NULL)
		   OR ($1 = 'delivered' AND delivered_at IS NOT NULL)
		   OR ($1 = 'abandoned' AND abandoned_at IS NOT NULL))`

	var total int64
	if err := h.db.QueryRow(r.Context(),
		`SELECT COUNT(*) FROM notification_deliveries`+filter, state).Scan(&total); err != nil {
		dbError(w, err, "failed to count notification deliveries")
		return
	}

	rows, err := h.db.Query(r.Context(), `
		SELECT d.id, t.name, d.event_type, d.attempts, d.next_attempt_at,
		       d.delivered_at, d.abandoned_at, d.last_error, d.created_at
		FROM notification_deliveries d
		JOIN notification_targets t ON t.id = d.target_id`+filter+`
		ORDER BY d.created_at DESC
		LIMIT $2 OFFSET $3`, state, p.PageSize, p.Offset)
	if err != nil {
		dbError(w, err, "failed to read notification deliveries")
		return
	}
	defer rows.Close()

	type delivery struct {
		ID            int64      `json:"id"`
		Target        string     `json:"target"`
		EventType     string     `json:"event_type"`
		Attempts      int        `json:"attempts"`
		NextAttemptAt time.Time  `json:"next_attempt_at"`
		DeliveredAt   *time.Time `json:"delivered_at"`
		AbandonedAt   *time.Time `json:"abandoned_at"`
		LastError     *string    `json:"last_error"`
		CreatedAt     time.Time  `json:"created_at"`
	}

	deliveries := []delivery{}
	for rows.Next() {
		var d delivery
		if err := rows.Scan(&d.ID, &d.Target, &d.EventType, &d.Attempts, &d.NextAttemptAt,
			&d.DeliveredAt, &d.AbandonedAt, &d.LastError, &d.CreatedAt); err != nil {
			dbError(w, err, "failed to read notification deliveries")
			return
		}
		deliveries = append(deliveries, d)
	}
	if err := rows.Err(); err != nil {
		dbError(w, err, "failed to read notification deliveries")
		return
	}

	writePage(w, p, total, deliveries)
}
