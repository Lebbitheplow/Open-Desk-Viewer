package apiv1

import (
	"context"
	"net/http"
	"time"

	"github.com/OpenDeskViewer/platform/api/internal/httpx"
	"github.com/google/uuid"
)

// Stats is the dashboard's summary. Every field is counted from the database;
// none is derived from another. The previous implementation reported
// active_devices as a copy of total_devices, which made the dashboard agree
// with itself while being wrong.
type Stats struct {
	TotalDevices      int64 `json:"total_devices"`
	DiscoveredDevices int64 `json:"discovered_devices"`
	ActiveDevices     int64 `json:"active_devices"`
	StaleDevices      int64 `json:"stale_devices"`
	OfflineDevices    int64 `json:"offline_devices"`
	OnlineNow         int64 `json:"online_now"`
	TotalCustomers    int64 `json:"total_customers"`
	TotalTechnicians  int64 `json:"total_technicians"`
	Connections24h    int64 `json:"connections_24h"`
}

// DashboardResponse is what GET /api/v1/stats/dashboard returns.
type DashboardResponse struct {
	Stats             Stats     `json:"stats"`
	RecentConnections []Session `json:"recent_connections"`
}

// HandleStatsDashboard serves GET /api/v1/stats/dashboard.
//
// A technician sees counts over the devices they can reach; an administrator, a
// manager and a Read Only auditor see the fleet. All go through the same query,
// differing only in the device id filter, so the views cannot drift apart.
func (h *Handler) HandleStatsDashboard(w http.ResponseWriter, r *http.Request) {
	user, ok := h.caller(w, r)
	if !ok {
		return
	}

	unscoped, err := h.seesWholeFleet(r.Context(), user.ID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to check administrator status")
		return
	}

	// scope is nil for a fleet-wide caller, meaning "every device". For anyone
	// else it is the explicit id list, which may legitimately be empty.
	var scope []uuid.UUID
	if !unscoped {
		scope, err = h.access.GetAccessibleDevices(r.Context(), user.ID)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to resolve accessible devices")
			return
		}
		if scope == nil {
			scope = []uuid.UUID{}
		}
	}

	var stats Stats
	// $1 is "unscoped": true for an admin, in which case the id filter is not
	// applied at all. Doing it in SQL rather than by concatenating two query
	// strings keeps one statement to read and one plan to reason about.
	err = h.db.QueryRow(r.Context(), `
		SELECT
			COUNT(*),
			COUNT(*) FILTER (WHERE state = 'DISCOVERED'),
			COUNT(*) FILTER (WHERE state = 'ACTIVE'),
			COUNT(*) FILTER (WHERE connectivity = 'STALE'),
			COUNT(*) FILTER (WHERE connectivity = 'OFFLINE'),
			COUNT(*) FILTER (WHERE connectivity = 'ONLINE')
		FROM devices
		WHERE $1::boolean OR id = ANY($2::uuid[])`,
		unscoped, scope).
		Scan(&stats.TotalDevices, &stats.DiscoveredDevices, &stats.ActiveDevices,
			&stats.StaleDevices, &stats.OfflineDevices, &stats.OnlineNow)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to count devices")
		return
	}

	if err := h.db.QueryRow(r.Context(), `SELECT COUNT(*) FROM customers`).Scan(&stats.TotalCustomers); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to count customers")
		return
	}

	// A technician is a user with at least one support group membership. That is
	// the definition the address book uses, so the dashboard matches it.
	if err := h.db.QueryRow(r.Context(), `
		SELECT COUNT(DISTINCT u.id)
		FROM users u
		JOIN user_support_groups g ON g.user_id = u.id
		WHERE u.active`).Scan(&stats.TotalTechnicians); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to count technicians")
		return
	}

	since := time.Now().Add(-24 * time.Hour)
	if err := h.db.QueryRow(r.Context(), `
		SELECT COUNT(*)
		FROM connection_sessions
		WHERE start_time >= $1 AND ($2::boolean OR device_id = ANY($3::uuid[]))`,
		since, unscoped, scope).Scan(&stats.Connections24h); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to count connections")
		return
	}

	recent, err := h.recentConnections(r, unscoped, scope)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to list recent connections")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, DashboardResponse{Stats: stats, RecentConnections: recent})
}

// recentConnections returns the ten most recent connections in scope.
func (h *Handler) recentConnections(r *http.Request, unscoped bool, scope []uuid.UUID) ([]Session, error) {
	rows, err := h.db.Query(r.Context(), `
		SELECT s.id, s.device_id, d.name, s.user_id, u.email, s.start_time, s.end_time,
		       s.duration_seconds, s.status, s.connected_from, s.note
		FROM connection_sessions s
		LEFT JOIN devices d ON d.id = s.device_id
		LEFT JOIN users u ON u.id = s.user_id
		WHERE $1::boolean OR s.device_id = ANY($2::uuid[])
		ORDER BY s.start_time DESC
		LIMIT 10`, unscoped, scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanSessions(rows)
}

// HandleSettings serves GET /api/v1/settings.
//
// Read-only, and sourced from config rather than a settings table. The plan
// calls this out: making these editable needs a config table and a reload path,
// which is not in scope for 1.0, so the portal displays what the deployment was
// started with.
func (h *Handler) HandleSettings(w http.ResponseWriter, r *http.Request) {
	user, ok := h.caller(w, r)
	if !ok {
		return
	}

	// The role list comes from the roles table rather than a constant, and the
	// caller's own roles come with it.
	//
	// Both exist because the portal used to hardcode
	// ['Administrator', 'Manager', 'Technician']. "Manager" is not a role this
	// deployment has -- the seeded one is "Support Manager" -- so granting it
	// from the user screen always failed with "unknown role", and "Read Only"
	// could not be granted at all because it was not offered. A list the portal
	// reads from here cannot drift from the list the grant endpoint validates
	// against.
	roles, err := h.roleNames(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to read roles")
		return
	}

	callerRoles := make([]string, 0, len(user.Roles))
	for _, role := range user.Roles {
		callerRoles = append(callerRoles, role.Name)
	}

	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"address_book_max_peers":       h.cfg.AddressBookMaxPeers,
		"audit_retention_days":         h.cfg.AuditRetentionDays,
		"device_stale_after_seconds":   h.cfg.DeviceStaleAfterSeconds,
		"device_offline_after_seconds": h.cfg.DeviceOfflineAfterSeconds,
		"editable":                     false,
		"roles":                        roles,
		// So the portal can hide controls it knows will 403. The API refuses
		// regardless; this only stops the screen offering a button that cannot
		// work, which is a usability fix and not a security one.
		"caller_roles": callerRoles,
	})
}

func (h *Handler) roleNames(ctx context.Context) ([]string, error) {
	rows, err := h.db.Query(ctx, `SELECT name FROM roles ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	names := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}
