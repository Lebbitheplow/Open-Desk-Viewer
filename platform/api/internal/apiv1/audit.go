package apiv1

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// AuditEvent is a recorded administrative change.
type AuditEvent struct {
	ID             uuid.UUID      `json:"id"`
	EventType      string         `json:"event_type"`
	UserID         *int64         `json:"user_id,omitempty"`
	UserEmail      *string        `json:"user_email,omitempty"`
	DeviceID       *uuid.UUID     `json:"device_id,omitempty"`
	DeviceName     *string        `json:"device_name,omitempty"`
	CustomerID     *uuid.UUID     `json:"customer_id,omitempty"`
	CustomerName   *string        `json:"customer_name,omitempty"`
	SupportGroupID *uuid.UUID     `json:"support_group_id,omitempty"`
	Description    *string        `json:"description,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
}

// timeRange parses the from and to query parameters shared by both audit
// endpoints. Both are optional and accept RFC 3339 or a bare date.
func timeRange(r *http.Request) (*time.Time, *time.Time, error) {
	parse := func(key string) (*time.Time, error) {
		raw := strings.TrimSpace(r.URL.Query().Get(key))
		if raw == "" {
			return nil, nil
		}
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			return &t, nil
		}
		t, err := time.Parse("2006-01-02", raw)
		if err != nil {
			return nil, err
		}
		return &t, nil
	}

	from, err := parse("from")
	if err != nil {
		return nil, nil, err
	}
	to, err := parse("to")
	if err != nil {
		return nil, nil, err
	}
	return from, to, nil
}

// HandleAuditEvents serves GET /api/v1/audit/events.
func (h *Handler) HandleAuditEvents(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.viewer(w, r); !ok {
		return
	}

	p := parsePage(r)
	query := r.URL.Query()

	from, to, err := timeRange(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid from or to date")
		return
	}

	var conditions []string
	var args []any
	add := func(clause string, value any) {
		args = append(args, value)
		conditions = append(conditions, strings.Replace(clause, "$?", "$"+strconv.Itoa(len(args)), 1))
	}

	if actor := strings.TrimSpace(query.Get("actor")); actor != "" {
		actorID, err := strconv.ParseInt(actor, 10, 64)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid actor")
			return
		}
		add("e.user_id = $?", actorID)
	}
	if resource := strings.TrimSpace(query.Get("resource")); resource != "" {
		add("e.metadata->>'resource' = $?", resource)
	}
	if eventType := strings.TrimSpace(query.Get("type")); eventType != "" {
		add("e.event_type = $?", eventType)
	}
	if device := strings.TrimSpace(query.Get("device")); device != "" {
		deviceID, err := uuid.Parse(device)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid device")
			return
		}
		add("e.device_id = $?", deviceID)
	}
	if from != nil {
		add("e.created_at >= $?", *from)
	}
	if to != nil {
		add("e.created_at <= $?", *to)
	}

	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}

	var total int64
	if err := h.db.QueryRow(r.Context(), `SELECT COUNT(*) FROM audit_events e`+where, args...).Scan(&total); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to count audit events")
		return
	}

	args = append(args, p.PageSize, p.Offset)
	rows, err := h.db.Query(r.Context(), `
		SELECT e.id, e.event_type, e.user_id, u.email, e.device_id, d.name,
		       e.customer_id, c.name, e.support_group_id, e.description, e.metadata, e.created_at
		FROM audit_events e
		LEFT JOIN users u ON u.id = e.user_id
		LEFT JOIN devices d ON d.id = e.device_id
		LEFT JOIN customers c ON c.id = e.customer_id`+where+`
		ORDER BY e.created_at DESC
		LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to list audit events")
		return
	}
	defer rows.Close()

	events := make([]AuditEvent, 0)
	for rows.Next() {
		var e AuditEvent
		if err := rows.Scan(&e.ID, &e.EventType, &e.UserID, &e.UserEmail, &e.DeviceID, &e.DeviceName,
			&e.CustomerID, &e.CustomerName, &e.SupportGroupID, &e.Description, &e.Metadata, &e.CreatedAt); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to read audit event")
			return
		}
		events = append(events, e)
	}
	if rows.Err() != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to read audit events")
		return
	}

	writePage(w, p, total, events)
}

// HandleAuditSessions serves GET /api/v1/audit/sessions: the connection log,
// including the operator note that PUT /api/audit attaches.
func (h *Handler) HandleAuditSessions(w http.ResponseWriter, r *http.Request) {
	user, ok := h.caller(w, r)
	if !ok {
		return
	}

	isAdmin, err := h.access.IsAdminOrManager(r.Context(), user.ID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to check administrator status")
		return
	}

	p := parsePage(r)
	query := r.URL.Query()

	from, to, err := timeRange(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid from or to date")
		return
	}

	var conditions []string
	var args []any
	add := func(clause string, value any) {
		args = append(args, value)
		conditions = append(conditions, strings.Replace(clause, "$?", "$"+strconv.Itoa(len(args)), 1))
	}

	if device := strings.TrimSpace(query.Get("device")); device != "" {
		deviceID, err := uuid.Parse(device)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid device")
			return
		}
		add("s.device_id = $?", deviceID)
	}
	if u := strings.TrimSpace(query.Get("user")); u != "" {
		targetID, err := strconv.ParseInt(u, 10, 64)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid user")
			return
		}
		add("s.user_id = $?", targetID)
	}
	if from != nil {
		add("s.start_time >= $?", *from)
	}
	if to != nil {
		add("s.start_time <= $?", *to)
	}

	// A technician sees only sessions on devices they can reach. Without this
	// the connection log would be a way to enumerate the whole fleet.
	if !isAdmin {
		accessible, err := h.access.GetAccessibleDevices(r.Context(), user.ID)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to resolve accessible devices")
			return
		}
		if len(accessible) == 0 {
			writePage(w, p, 0, []Session{})
			return
		}
		add("s.device_id = ANY($?)", accessible)
	}

	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}

	var total int64
	if err := h.db.QueryRow(r.Context(),
		`SELECT COUNT(*) FROM connection_sessions s`+where, args...).Scan(&total); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to count sessions")
		return
	}

	args = append(args, p.PageSize, p.Offset)
	rows, err := h.db.Query(r.Context(), `
		SELECT s.id, s.device_id, d.name, s.user_id, u.email, s.start_time, s.end_time,
		       s.duration_seconds, s.status, s.connected_from, s.note
		FROM connection_sessions s
		LEFT JOIN devices d ON d.id = s.device_id
		LEFT JOIN users u ON u.id = s.user_id`+where+`
		ORDER BY s.start_time DESC
		LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
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
