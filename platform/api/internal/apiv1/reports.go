package apiv1

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/OpenDeskViewer/platform/api/internal/audit"
	"github.com/OpenDeskViewer/platform/api/internal/httpx"
	"github.com/jackc/pgx/v5"
)

// Reporting, item 6.7.
//
// No new data. Everything here is a query over rows the platform already keeps:
// the fleet, connection_sessions, and the support-group chain. What was missing
// was a way to get any of it out in a form somebody can put in front of an
// auditor, which is the whole point of keeping it.
//
// Three reports, and they are the three questions this product exists to
// answer:
//
//   - inventory: what machines are we managing, for whom, and are they alive
//   - sessions:  who connected to what, when, and for how long
//   - access:    who *could* connect to what, which is the one an access review
//     actually asks and the one no screen answered
//
// CSV as well as JSON, because the recipient of a report is usually somebody
// who works in a spreadsheet, and "export" that means "here is some JSON" is a
// feature that gets used once.

// report is one report's definition: the query, the header row, and how to turn
// a row into strings. Defining them as data rather than as three near-identical
// handlers is what keeps the CSV and JSON paths from drifting.
type report struct {
	name    string
	columns []string
	query   func(r *http.Request) (string, []any)
	scan    func(rows pgx.Rows) ([]string, map[string]any, error)
}

// HandleReport serves GET /api/v1/reports/{report}.
//
// Administration, not per-device: a report scoped to one technician's support
// groups is not a report, it is a filtered list, and presenting it as one would
// let somebody sign off an access review over a fraction of the fleet.
func (h *Handler) HandleReport(w http.ResponseWriter, r *http.Request) {
	user, ok := h.viewer(w, r)
	if !ok {
		return
	}

	name := r.PathValue("report")
	rep, ok := h.reports()[name]
	if !ok {
		writeJSONError(w, http.StatusNotFound,
			"unknown report; this deployment has device-inventory, session-history and access-review")
		return
	}

	query, args := rep.query(r)
	rows, err := h.db.Query(r.Context(), query, args...)
	if err != nil {
		dbError(w, err, "failed to run the report")
		return
	}
	defer rows.Close()

	csvRows := make([][]string, 0, 256)
	jsonRows := make([]map[string]any, 0, 256)
	for rows.Next() {
		record, object, err := rep.scan(rows)
		if err != nil {
			dbError(w, err, "failed to read the report")
			return
		}
		csvRows = append(csvRows, record)
		jsonRows = append(jsonRows, object)
	}
	if err := rows.Err(); err != nil {
		dbError(w, err, "failed to read the report")
		return
	}

	// Running a report is itself worth recording. An access review is evidence,
	// and evidence that nobody can show was produced is worth less; it also
	// makes "who pulled a list of every machine and customer" answerable.
	h.record(r.Context(), audit.Event{
		Type:        "report.generated",
		ActorID:     user.ID,
		Resource:    "report",
		ResourceID:  name,
		Description: "Report generated: " + name,
		Metadata:    map[string]any{"rows": len(csvRows), "format": r.URL.Query().Get("format")},
	})

	if strings.EqualFold(r.URL.Query().Get("format"), "csv") {
		writeCSV(w, rep, csvRows)
		return
	}

	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"report":       name,
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"columns":      rep.columns,
		"total":        len(jsonRows),
		"data":         jsonRows,
	})
}

func writeCSV(w http.ResponseWriter, rep report, records [][]string) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	// The date in the filename, because the second thing anyone does with a
	// report is save it next to last month's.
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="odv-%s-%s.csv"`,
		rep.name, time.Now().UTC().Format("2006-01-02")))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	out := csv.NewWriter(w)
	_ = out.Write(rep.columns)
	for _, record := range records {
		_ = out.Write(record)
	}
	out.Flush()
}

func (h *Handler) reports() map[string]report {
	return map[string]report{
		"device-inventory": {
			name:    "device-inventory",
			columns: []string{"rustdesk_id", "name", "hostname", "os", "customer", "location", "state", "connectivity", "last_seen_at", "client_version", "enrolled", "device_groups"},
			query: func(r *http.Request) (string, []any) {
				return `
					SELECT d.rustdesk_id, d.name, COALESCE(d.hostname, ''), COALESCE(d.os, ''),
					       COALESCE(c.name, ''), COALESCE(l.name, ''), d.state, d.connectivity,
					       d.last_seen_at, COALESCE(d.client_version, ''),
					       EXISTS (SELECT 1 FROM device_credentials dc
					               WHERE dc.device_id = d.id AND dc.revoked_at IS NULL),
					       COALESCE((SELECT string_agg(g.name, '; ' ORDER BY g.name)
					                 FROM device_group_members m
					                 JOIN device_groups g ON g.id = m.device_group_id
					                 WHERE m.device_id = d.id), '')
					FROM devices d
					LEFT JOIN customers c ON c.id = d.customer_id
					LEFT JOIN locations l ON l.id = d.location_id
					ORDER BY COALESCE(c.name, ''), d.name`, nil
			},
			scan: func(rows pgx.Rows) ([]string, map[string]any, error) {
				var rustdeskID, name, hostname, os, customer, location, state, conn, version, groups string
				var lastSeen *time.Time
				var enrolled bool
				if err := rows.Scan(&rustdeskID, &name, &hostname, &os, &customer, &location,
					&state, &conn, &lastSeen, &version, &enrolled, &groups); err != nil {
					return nil, nil, err
				}
				return []string{rustdeskID, name, hostname, os, customer, location, state, conn,
						formatTime(lastSeen), version, strconv.FormatBool(enrolled), groups},
					map[string]any{
						"rustdesk_id": rustdeskID, "name": name, "hostname": hostname, "os": os,
						"customer": customer, "location": location, "state": state,
						"connectivity": conn, "last_seen_at": lastSeen, "client_version": version,
						// The column an inventory is usually run to find: a
						// device row that exists and holds no credential is a
						// machine that was shipped and never came up.
						"enrolled": enrolled, "device_groups": groups,
					}, nil
			},
		},

		"session-history": {
			name:    "session-history",
			columns: []string{"started", "ended", "duration_seconds", "technician", "device", "rustdesk_id", "customer", "status", "connected_from", "note"},
			query: func(r *http.Request) (string, []any) {
				// Bounded by default. An unbounded session history is a table
				// scan that grows with the deployment's whole life, and the
				// question is almost always about a period.
				from, to := reportWindow(r)
				return `
					SELECT s.start_time, s.end_time, s.duration_seconds,
					       COALESCE(u.email, ''), COALESCE(d.name, ''), COALESCE(d.rustdesk_id, ''),
					       COALESCE(c.name, ''), s.status, COALESCE(s.connected_from::text, ''),
					       COALESCE(s.note, '')
					FROM connection_sessions s
					LEFT JOIN users u ON u.id = s.user_id
					LEFT JOIN devices d ON d.id = s.device_id
					LEFT JOIN customers c ON c.id = d.customer_id
					WHERE s.start_time >= $1 AND s.start_time < $2
					ORDER BY s.start_time DESC`, []any{from, to}
			},
			scan: func(rows pgx.Rows) ([]string, map[string]any, error) {
				var start time.Time
				var end *time.Time
				var duration *int64
				var tech, device, rustdeskID, customer, status, from, note string
				if err := rows.Scan(&start, &end, &duration, &tech, &device, &rustdeskID,
					&customer, &status, &from, &note); err != nil {
					return nil, nil, err
				}
				durationStr := ""
				if duration != nil {
					durationStr = strconv.FormatInt(*duration, 10)
				}
				return []string{start.UTC().Format(time.RFC3339), formatTime(end), durationStr,
						tech, device, rustdeskID, customer, status, from, note},
					map[string]any{
						"started": start, "ended": end, "duration_seconds": duration,
						"technician": tech, "device": device, "rustdesk_id": rustdeskID,
						"customer": customer, "status": status, "connected_from": from, "note": note,
					}, nil
			},
		},

		"access-review": {
			name:    "access-review",
			columns: []string{"technician", "email", "active", "support_group", "device_group", "devices", "customers"},
			query: func(r *http.Request) (string, []any) {
				// The question an access review actually asks: not "who is in
				// which group" but "who can reach which machines", which is the
				// same chain access.Resolver walks. Answering it from the group
				// tables by hand is how a reviewer signs off on access that
				// does not match what the API enforces.
				return `
					SELECT u.display_name, u.email, u.active, sg.name, dg.name,
					       -- d.id, not m.device_id. The join to devices carries
					       -- state = 'ACTIVE', which is what access.Resolver
					       -- enforces; counting members instead reports access
					       -- to devices that cannot in fact be reached, which is
					       -- the one thing an access review must not do.
					       COUNT(DISTINCT d.id),
					       COALESCE(string_agg(DISTINCT c.name, '; '), '')
					FROM users u
					JOIN user_support_groups usg ON usg.user_id = u.id
					JOIN support_groups sg ON sg.id = usg.support_group_id
					LEFT JOIN support_group_device_groups sgdg ON sgdg.support_group_id = sg.id
					LEFT JOIN device_groups dg ON dg.id = sgdg.device_group_id
					LEFT JOIN device_group_members m ON m.device_group_id = dg.id
					LEFT JOIN devices d ON d.id = m.device_id AND d.state = 'ACTIVE'
					LEFT JOIN customers c ON c.id = d.customer_id
					GROUP BY u.id, u.display_name, u.email, u.active, sg.name, dg.name
					ORDER BY u.email, sg.name, dg.name`, nil
			},
			scan: func(rows pgx.Rows) ([]string, map[string]any, error) {
				var name, email, customers string
				var active bool
				var supportGroup string
				var deviceGroup *string
				var devices int64
				if err := rows.Scan(&name, &email, &active, &supportGroup, &deviceGroup,
					&devices, &customers); err != nil {
					return nil, nil, err
				}
				group := ""
				if deviceGroup != nil {
					group = *deviceGroup
				}
				return []string{name, email, strconv.FormatBool(active), supportGroup, group,
						strconv.FormatInt(devices, 10), customers},
					map[string]any{
						"technician": name, "email": email, "active": active,
						"support_group": supportGroup, "device_group": group,
						"devices": devices, "customers": customers,
					}, nil
			},
		},
	}
}

// reportWindow resolves from/to, defaulting to the last thirty days.
func reportWindow(r *http.Request) (time.Time, time.Time) {
	to := time.Now()
	from := to.AddDate(0, 0, -30)

	if raw := strings.TrimSpace(r.URL.Query().Get("from")); raw != "" {
		if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
			from = parsed
		}
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("to")); raw != "" {
		if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
			to = parsed
		}
	}
	return from, to
}

func formatTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
