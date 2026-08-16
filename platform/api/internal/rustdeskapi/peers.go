package rustdeskapi

import (
	"net/http"

	"github.com/OpenDeskViewer/platform/api/internal/access"
	"github.com/OpenDeskViewer/platform/api/internal/audit"
	"github.com/OpenDeskViewer/platform/api/internal/httpx"
	"github.com/OpenDeskViewer/platform/api/internal/peers"
	"github.com/OpenDeskViewer/platform/api/internal/postgres"
)

// PeerHandler handles peers endpoints
type PeerHandler struct {
	service       *peers.Service
	auditRecorder audit.Recorder
}

// NewPeerHandler creates a new peer handler
func NewPeerHandler(db *postgres.Pool, accessResolver access.Resolver, auditRecorder audit.Recorder) *PeerHandler {
	return &PeerHandler{
		service:       peers.NewService(db, accessResolver),
		auditRecorder: auditRecorder,
	}
}

// HandlePeers handles /api/peers
func (h *PeerHandler) HandlePeers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.getPeers(w, r)
	default:
		httpx.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// getPeers retrieves all accessible peers with pagination
func (h *PeerHandler) getPeers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	user, ok := httpx.GetUserFromContext(ctx)
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	current, pageSize, err := httpx.ParsePagination(r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid pagination parameters")
		return
	}

	offset := (current - 1) * pageSize

	peersList, total, err := h.service.ListAccessiblePeers(ctx, user.ID, offset, pageSize)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "failed to fetch peers")
		return
	}

	// The field names here are a contract with the client, not a choice.
	// PeerPayload.fromJson (flutter/lib/common/hbbs/hbbs.dart) reads `id` as the
	// peer to connect to and builds the display name from info.device_name, so
	// a response shaped any other way lists devices a technician cannot reach or
	// identify. See TestPeersAnswerWhatTheClientConnectsTo.
	var responsePeers []map[string]interface{}
	for _, p := range peersList {
		peer := map[string]interface{}{
			"id":   p.RustdeskID,
			"name": p.Name,
			"info": map[string]interface{}{
				// device_name rather than hostname: this is the field the client
				// displays, and it is where the platform-assigned name (the
				// serial, or a name set through the API) has to land for a
				// technician to search on it.
				"device_name": p.Name,
				"hostname":    p.Hostname,
				"os":          p.OS,
			},
			"status":            peerStatus(p.Connectivity),
			"user":              p.UserName,
			"user_name":         p.UserName,
			"device_group_name": p.GroupName,
			"note":              "",
		}
		responsePeers = append(responsePeers, peer)
	}

	httpx.WritePaginatedResponsePage(w, current, pageSize, total, responsePeers)
}

// peerStatus maps devices.connectivity onto the client's tri-state.
//
// 1 online, 0 offline, -1 unknown. STALE is reported offline rather than
// unknown: a technician deciding whether to try a connection is better served
// by "probably not" than by a state the client renders as nothing at all.
func peerStatus(connectivity string) int {
	switch connectivity {
	case "ONLINE":
		return 1
	case "OFFLINE", "STALE":
		return 0
	default:
		return -1
	}
}

// HandleDeviceGroupAccessible handles /api/device-group/accessible
func (h *PeerHandler) HandleDeviceGroupAccessible(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Parse pagination parameters - prefer query string for paged GET requests
	current, pageSize, err := httpx.ParsePagination(r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid pagination parameters")
		return
	}

	ctx := r.Context()

	user, ok := httpx.GetUserFromContext(ctx)
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	groups, total, err := h.service.ListDeviceGroups(ctx, user.ID, current, pageSize)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "failed to fetch device groups")
		return
	}

	var responseGroups []map[string]interface{}
	for _, g := range groups {
		group := map[string]interface{}{
			"id":   g.ID.String(),
			"name": g.Name,
		}
		responseGroups = append(responseGroups, group)
	}

	httpx.WritePaginatedResponsePage(w, current, pageSize, total, responseGroups)
}
