package rustdeskapi

import (
	"encoding/json"
	"net/http"

	"github.com/OpenDeskViewer/platform/api/internal/audit"
	"github.com/OpenDeskViewer/platform/api/internal/httpx"
	"github.com/OpenDeskViewer/platform/api/internal/identity"
	"github.com/OpenDeskViewer/platform/api/internal/telemetry"
)

// Handlers handles RustDesk Pro API endpoints
type Handlers struct {
	authService      *identity.AuthService
	telemetryService *telemetry.Service
	auditRecorder    audit.Recorder
}

// NewHandlers creates new handlers
func NewHandlers(auth *identity.AuthService, telem *telemetry.Service, auditRecorder audit.Recorder) *Handlers {
	return &Handlers{
		authService:      auth,
		telemetryService: telem,
		auditRecorder:    auditRecorder,
	}
}

// HandleLoginOptions returns available login options
func (h *Handlers) HandleLoginOptions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode([]string{"common"})
}

// HandleCurrentUser returns current user info
func (h *Handlers) HandleCurrentUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	user, ok := httpx.GetUserFromContext(ctx)
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	isAdmin := false
	for _, role := range user.Roles {
		if role.Name == "Administrator" || role.Name == "Support Manager" {
			isAdmin = true
			break
		}
	}

	status := 1
	if !user.Active {
		status = 0
	}

	resp := map[string]interface{}{
		"name":         user.Email,
		"display_name": user.DisplayName,
		"avatar":       "",
		"email":        user.Email,
		"note":         "",
		"verifier":     "",
		"status":       status,
		"is_admin":     isAdmin,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}

// heartbeatRequest represents the JSON body of a heartbeat
type heartbeatRequest struct {
	ID         string `json:"id"`
	UUID       string `json:"uuid"`
	Ver        string `json:"ver"`
	Conns      int    `json:"conns"`
	ModifiedAt int64  `json:"modified_at"`
}

// HandleHeartbeat handles device heartbeat
func (h *Handlers) HandleHeartbeat(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req heartbeatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if req.ID == "" {
		httpx.WriteError(w, http.StatusBadRequest, "missing device id")
		return
	}

	if err := h.telemetryService.ProcessHeartbeat(ctx, req.ID, req.UUID, true); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "failed to process heartbeat")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"disconnect": []string{},
		"strategy":   map[string]interface{}{},
	})
}

// sysinfoRequest represents the JSON body of a sysinfo post
type sysinfoRequest struct {
	ID       string `json:"id"`
	UUID     string `json:"uuid"`
	Hostname string `json:"hostname"`
	Version  string `json:"version"`
	OS       string `json:"os"`
}

// HandleSysinfo handles device sysinfo
func (h *Handlers) HandleSysinfo(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req sysinfoRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if req.ID == "" || req.UUID == "" {
		httpx.WriteError(w, http.StatusBadRequest, "missing required fields")
		return
	}

	if err := h.telemetryService.ProcessSysinfo(ctx, req.ID, req.UUID, req.Hostname, req.Version, req.OS); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "failed to process sysinfo")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{}`))
}
