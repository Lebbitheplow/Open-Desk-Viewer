package rustdeskapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/OpenDeskViewer/platform/api/internal/access"
	"github.com/OpenDeskViewer/platform/api/internal/addressbook"
	"github.com/OpenDeskViewer/platform/api/internal/audit"
	"github.com/OpenDeskViewer/platform/api/internal/httpx"
	"github.com/OpenDeskViewer/platform/api/internal/identity"
	"github.com/OpenDeskViewer/platform/api/internal/postgres"
	"github.com/google/uuid"
)

// AddressBookHandler serves /api/ab/*, the paths in
// flutter/lib/models/ab_model.dart.
type AddressBookHandler struct {
	service *addressbook.Service
	// maxPeerOneAb is reported to the client as its per-book cap. Zero means no
	// limit, which is what the client assumes when the field is absent.
	maxPeerOneAb int
	auditRecorder audit.Recorder
}

// NewAddressBookHandler creates a new address book handler
func NewAddressBookHandler(db *postgres.Pool, accessResolver access.Resolver, maxPeerOneAb int, auditRecorder audit.Recorder) *AddressBookHandler {
	return &AddressBookHandler{
		service:      addressbook.NewService(db, accessResolver),
		maxPeerOneAb: maxPeerOneAb,
		auditRecorder: auditRecorder,
	}
}

// flexBool accepts either a JSON bool or a quoted one, because the client
// serialises forceAlwaysRelay as the string "true".
type flexBool bool

func (b *flexBool) UnmarshalJSON(data []byte) error {
	var asBool bool
	if err := json.Unmarshal(data, &asBool); err == nil {
		*b = flexBool(asBool)
		return nil
	}

	var asString string
	if err := json.Unmarshal(data, &asString); err != nil {
		return fmt.Errorf("expected a bool or a quoted bool, got %s", data)
	}
	*b = flexBool(asString == "true")
	return nil
}

// writeAbOK signals success for a mutation. The body must be empty: the client's
// _jsonDecodeActionResp treats any non-empty body on a 200 as an error message.
func writeAbOK(w http.ResponseWriter) {
	w.WriteHeader(http.StatusOK)
}

// writeAbError maps a service error onto the status the client expects.
func writeAbError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, addressbook.ErrForbidden):
		httpx.WriteError(w, http.StatusForbidden, "address book not accessible")
	case errors.Is(err, addressbook.ErrReadOnly):
		httpx.WriteError(w, http.StatusForbidden, "this address book is managed by the platform and cannot be edited here")
	case errors.Is(err, addressbook.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "not found")
	default:
		httpx.WriteError(w, http.StatusInternalServerError, "address book request failed")
	}
}

// abRequest pulls the caller and the book guid out of a request. The guid comes
// from the path on most routes and from the ab query parameter on /api/ab/peers.
func abRequest(w http.ResponseWriter, r *http.Request, guidSource string) (*identity.User, uuid.UUID, bool) {
	user, ok := httpx.GetUserFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return nil, uuid.Nil, false
	}

	raw := r.PathValue(guidSource)
	if guidSource == "query" {
		raw = r.URL.Query().Get("ab")
	}

	guid, err := uuid.Parse(raw)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid address book guid")
		return nil, uuid.Nil, false
	}

	return user, guid, true
}

// HandleSettings handles POST /api/ab/settings
func (h *AddressBookHandler) HandleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, map[string]interface{}{
		"max_peer_one_ab": h.maxPeerOneAb,
	})
}

// HandlePersonal handles POST /api/ab/personal. Answering this with a guid is
// what takes the client out of legacy mode.
func (h *AddressBookHandler) HandlePersonal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	user, ok := httpx.GetUserFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	profile, err := h.service.ResolvePersonalProfile(r.Context(), user.ID)
	if err != nil {
		writeAbError(w, err)
		return
	}

	httpx.WriteJSON(w, http.StatusOK, map[string]interface{}{
		"guid": profile.GUID.String(),
		"name": profile.Name,
		"rule": profile.Rule,
	})
}

// HandleSharedProfiles handles POST /api/ab/shared/profiles
func (h *AddressBookHandler) HandleSharedProfiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	user, ok := httpx.GetUserFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	current, pageSize, err := httpx.ParsePagination(r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid pagination parameters")
		return
	}

	profiles, total, err := h.service.ListSharedProfiles(r.Context(), user.ID, (current-1)*pageSize, pageSize)
	if err != nil {
		writeAbError(w, err)
		return
	}

	data := make([]map[string]interface{}, 0, len(profiles))
	for _, p := range profiles {
		note := ""
		if p.Note != nil {
			note = *p.Note
		}
		data = append(data, map[string]interface{}{
			"guid":  p.GUID.String(),
			"name":  p.Name,
			"owner": p.Owner,
			"note":  note,
			"rule":  p.Rule,
			"info":  nil,
		})
	}

	httpx.WriteJSON(w, http.StatusOK, map[string]interface{}{"total": total, "data": data})
}

// HandlePeers handles POST /api/ab/peers?ab=<guid>
func (h *AddressBookHandler) HandlePeers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	user, guid, ok := abRequest(w, r, "query")
	if !ok {
		return
	}

	current, pageSize, err := httpx.ParsePagination(r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid pagination parameters")
		return
	}

	peers, total, err := h.service.ListPeers(r.Context(), user.ID, guid, (current-1)*pageSize, pageSize)
	if err != nil {
		writeAbError(w, err)
		return
	}

	data := make([]map[string]interface{}, 0, len(peers))
	for _, p := range peers {
		tags := p.Tags
		if tags == nil {
			tags = []string{}
		}
		data = append(data, map[string]interface{}{
			"id":                p.ID,
			"hash":              p.Hash,
			"password":          "",
			"username":          p.Username,
			"hostname":          p.Hostname,
			"platform":          p.Platform,
			"alias":             p.Alias,
			"tags":              tags,
			"forceAlwaysRelay":  boolString(p.ForceAlwaysRelay),
			"rdpPort":           p.RdpPort,
			"rdpUsername":       p.RdpUsername,
			"loginName":         p.LoginName,
			"device_group_name": p.DeviceGroupName,
			"note":              p.Note,
		})
	}

	httpx.WriteJSON(w, http.StatusOK, map[string]interface{}{"total": total, "data": data})
}

// boolString renders a bool the way the client's Peer model parses it.
func boolString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// HandlePeerAdd handles POST /api/ab/peer/add/{guid}
func (h *AddressBookHandler) HandlePeerAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	user, guid, ok := abRequest(w, r, "guid")
	if !ok {
		return
	}

	var body struct {
		ID               string   `json:"id"`
		Hash             string   `json:"hash"`
		Username         string   `json:"username"`
		Hostname         string   `json:"hostname"`
		Platform         string   `json:"platform"`
		Alias            string   `json:"alias"`
		Note             string   `json:"note"`
		Tags             []string `json:"tags"`
		ForceAlwaysRelay flexBool `json:"forceAlwaysRelay"`
		RdpPort          string   `json:"rdpPort"`
		RdpUsername      string   `json:"rdpUsername"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	err := h.service.AddPeer(r.Context(), user.ID, guid, addressbook.Peer{
		ID:               body.ID,
		Hash:             body.Hash,
		Username:         body.Username,
		Hostname:         body.Hostname,
		Platform:         body.Platform,
		Alias:            body.Alias,
		Note:             body.Note,
		Tags:             body.Tags,
		ForceAlwaysRelay: bool(body.ForceAlwaysRelay),
		RdpPort:          body.RdpPort,
		RdpUsername:      body.RdpUsername,
	})
	if err != nil {
		writeAbError(w, err)
		return
	}

	writeAbOK(w)
}

// HandlePeerUpdate handles PUT /api/ab/peer/update/{guid}. The client sends only
// the fields it is changing, so absent stays absent all the way to the UPDATE.
func (h *AddressBookHandler) HandlePeerUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		httpx.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	user, guid, ok := abRequest(w, r, "guid")
	if !ok {
		return
	}

	var body struct {
		ID               string    `json:"id"`
		Alias            *string   `json:"alias"`
		Note             *string   `json:"note"`
		Hash             *string   `json:"hash"`
		Tags             *[]string `json:"tags"`
		ForceAlwaysRelay *flexBool `json:"forceAlwaysRelay"`
		RdpPort          *string   `json:"rdpPort"`
		RdpUsername      *string   `json:"rdpUsername"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	update := addressbook.PeerUpdate{
		ID:          body.ID,
		Alias:       body.Alias,
		Note:        body.Note,
		Hash:        body.Hash,
		Tags:        body.Tags,
		RdpPort:     body.RdpPort,
		RdpUsername: body.RdpUsername,
	}
	if body.ForceAlwaysRelay != nil {
		relay := bool(*body.ForceAlwaysRelay)
		update.ForceAlwaysRelay = &relay
	}

	if err := h.service.UpdatePeer(r.Context(), user.ID, guid, update); err != nil {
		writeAbError(w, err)
		return
	}

	writeAbOK(w)
}

// HandlePeerDelete handles DELETE /api/ab/peer/{guid} with a body of ids.
func (h *AddressBookHandler) HandlePeerDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		httpx.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	user, guid, ok := abRequest(w, r, "guid")
	if !ok {
		return
	}

	var ids []string
	if err := json.NewDecoder(r.Body).Decode(&ids); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if err := h.service.DeletePeers(r.Context(), user.ID, guid, ids); err != nil {
		writeAbError(w, err)
		return
	}

	writeAbOK(w)
}
