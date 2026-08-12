package rustdeskapi

import (
	"encoding/json"
	"net/http"

	"github.com/OpenDeskViewer/platform/api/internal/httpx"
	"github.com/OpenDeskViewer/platform/api/internal/postgres"
)

// SysinfoVerHandler handles /api/sysinfo_ver
type SysinfoVerHandler struct {
	db *postgres.Pool
}

// NewSysinfoVerHandler creates a new sysinfo version handler
func NewSysinfoVerHandler(db *postgres.Pool) *SysinfoVerHandler {
	return &SysinfoVerHandler{db: db}
}

// HandleSysinfoVer handles /api/sysinfo_ver
func (h *SysinfoVerHandler) HandleSysinfoVer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"version": "1.0",
	})
}
