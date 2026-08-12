package rustdeskapi

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/OpenDeskViewer/platform/api/internal/access"
	"github.com/OpenDeskViewer/platform/api/internal/httpx"
	"github.com/OpenDeskViewer/platform/api/internal/postgres"
	"github.com/google/uuid"
)

// AuditHandler handles audit endpoints
type AuditHandler struct {
	db         *postgres.Pool
	access     access.Resolver
}

// NewAuditHandler creates a new audit handler
func NewAuditHandler(db *postgres.Pool, accessResolver access.Resolver) *AuditHandler {
	return &AuditHandler{db: db, access: accessResolver}
}

// HandleAuditConn handles /api/audit/conn
func (h *AuditHandler) HandleAuditConn(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req struct {
		DeviceID      string `json:"device_id"`
		UserID        string `json:"user_id"`
		SupportGroup  string `json:"support_group_id"`
		ClientID      string `json:"client_id"`
		Protocol      string `json:"protocol"`
		StartTime     string `json:"start_time"`
		EndTime       string `json:"end_time"`
		Duration      int    `json:"duration_seconds"`
		ConnectedFrom string `json:"connected_from"`
		Status        string `json:"status"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	status := "REQUESTED"
	if req.Status != "" {
		status = req.Status
	}

	var sessionID uuid.UUID
	err := h.db.QueryRow(r.Context(), `
		INSERT INTO connection_sessions (device_id, user_id, support_group_id, client_id,
		                                 protocol, start_time, end_time, duration_seconds,
		                                 connected_from, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING id
	`, req.DeviceID, req.UserID, req.SupportGroup, req.ClientID,
		req.Protocol, req.StartTime, req.EndTime, req.Duration,
		req.ConnectedFrom, status).Scan(&sessionID)

	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "failed to record audit event")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"guid": sessionID.String(),
	})
}

// HandleAuditNote handles PUT /api/audit (note attached to a session)
func (h *AuditHandler) HandleAuditNote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		httpx.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req struct {
		GUID string `json:"guid"`
		Note string `json:"note"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Get user from context
	user, ok := httpx.GetUserFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusForbidden, "not authorized")
		return
	}

	// Parse GUID
	sessionID, err := uuid.Parse(req.GUID)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid session GUID")
		return
	}

	// Check if user can access this session (owner or admin/manager)
	canAccess, err := h.access.CanAccessSession(r.Context(), user.ID, sessionID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "failed to check access")
		return
	}

	if !canAccess {
		httpx.WriteError(w, http.StatusForbidden, "not authorized to update this session")
		return
	}

	// Update the note
	result, err := h.db.Exec(r.Context(), `
		UPDATE connection_sessions
		SET note = COALESCE(NULLIF($1, ''), note)
		WHERE id = $2
	`, req.Note, sessionID)

	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "failed to update audit note")
		return
	}

	affected := result.RowsAffected()
	if affected == 0 {
		httpx.WriteError(w, http.StatusNotFound, "session not found")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{})
}

// HandleDevicesDeploy handles POST /api/devices/deploy
func (h *AuditHandler) HandleDevicesDeploy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	
	// Return a shell script for Linux/Unix deployment
	script := `#!/bin/bash
# OpenDeskViewer Deploy Script
# Generated on $(date)

# Configure environment variables
export RENDEZVOUS_SERVERS="{{.RendezvousServer}}"
export RS_PUB_KEY="{{.RSKey}}"
export API_SERVER="{{.APIServer}}"

# Download and install
curl -fsSL https://github.com/OpenDeskViewer/OpenDeskViewer/releases/latest/download/openDeskViewer-latest.sh | bash
`
	
	w.Header().Set("Content-Type", "application/x-shellscript")
	w.Header().Set("Content-Disposition", `attachment; filename="openDeskViewer-deploy.sh"`)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(script))
}

// HandleDevicesCLI handles POST /api/devices/cli
func (h *AuditHandler) HandleDevicesCLI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	
	// Return CLI installation command
	script := `#!/bin/bash
# OpenDeskViewer CLI Installation Script

echo "Installing OpenDeskViewer..."
# Add installation commands here
echo "Installation complete. Run 'openDeskViewer --help' for usage."
`
	
	w.Header().Set("Content-Type", "application/x-shellscript")
	w.Header().Set("Content-Disposition", `attachment; filename="openDeskViewer-cli.sh"`)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(script))
}

// HandleRecord handles POST /api/record
func (h *AuditHandler) HandleRecord(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	
	httpx.WriteError(w, http.StatusNotImplemented, "session recording is not enabled")
}

// HandleSwitchGrant handles POST /api/switch-grant
func (h *AuditHandler) HandleSwitchGrant(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	
	// For now, return success - in production, validate the request
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"accepted": true,
	})
}

// HandlePluginSign handles POST /lic/web/api/plugin-sign
func (h *AuditHandler) HandlePluginSign(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	
	httpx.WriteError(w, http.StatusNotImplemented, "plugin signing verification is not enabled")
}

// HandleAuditFile handles /api/audit/file
func (h *AuditHandler) HandleAuditFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req struct {
		DeviceID  string `json:"device_id"`
		UserID    string `json:"user_id"`
		FileName  string `json:"file_name"`
		Direction string `json:"direction"`
		FilePath  string `json:"file_path"`
		Size      int64  `json:"size"`
		Timestamp string `json:"timestamp"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	_, err := h.db.Exec(r.Context(), `
		INSERT INTO audit_events (event_type, user_id, device_id, description, metadata)
		VALUES ('file_transfer', $1, $2, $3, $4)
	`, req.UserID, req.DeviceID, fmt.Sprintf("File %s: %s (%s)", req.Direction, req.FileName, req.Direction),
		map[string]interface{}{
			"file_name": req.FileName,
			"direction": req.Direction,
			"file_path": req.FilePath,
			"size":      req.Size,
			"timestamp": req.Timestamp,
		})

	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "failed to record audit event")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
	})
}
