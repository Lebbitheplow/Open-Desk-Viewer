package rustdeskapi

import (
	"encoding/json"
	"net/http"

	"github.com/OpenDeskViewer/platform/api/internal/addressbook"
	"github.com/OpenDeskViewer/platform/api/internal/httpx"
)

// HandleTags handles POST /api/ab/tags/{guid}. The client decodes this one as a
// bare JSON array, not as a {total, data} envelope.
func (h *AddressBookHandler) HandleTags(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	user, guid, ok := abRequest(w, r, "guid")
	if !ok {
		return
	}

	tags, err := h.service.ListTags(r.Context(), user.ID, guid)
	if err != nil {
		writeAbError(w, err)
		return
	}

	data := make([]map[string]interface{}, 0, len(tags))
	for _, t := range tags {
		data = append(data, map[string]interface{}{
			"name":  t.Name,
			"color": t.Color,
		})
	}

	httpx.WriteJSON(w, http.StatusOK, data)
}

// HandleTagAdd handles POST /api/ab/tag/add/{guid}
func (h *AddressBookHandler) HandleTagAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	user, guid, ok := abRequest(w, r, "guid")
	if !ok {
		return
	}

	var body struct {
		Name  string `json:"name"`
		Color int64  `json:"color"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	err := h.service.AddTag(r.Context(), user.ID, guid, addressbook.Tag{Name: body.Name, Color: body.Color})
	if err != nil {
		writeAbError(w, err)
		return
	}

	writeAbOK(w)
}

// HandleTagRename handles PUT /api/ab/tag/rename/{guid}
func (h *AddressBookHandler) HandleTagRename(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		httpx.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	user, guid, ok := abRequest(w, r, "guid")
	if !ok {
		return
	}

	var body struct {
		Old string `json:"old"`
		New string `json:"new"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if err := h.service.RenameTag(r.Context(), user.ID, guid, body.Old, body.New); err != nil {
		writeAbError(w, err)
		return
	}

	writeAbOK(w)
}

// HandleTagUpdate handles PUT /api/ab/tag/update/{guid}, which sets the colour.
func (h *AddressBookHandler) HandleTagUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		httpx.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	user, guid, ok := abRequest(w, r, "guid")
	if !ok {
		return
	}

	var body struct {
		Name  string `json:"name"`
		Color int64  `json:"color"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	err := h.service.UpdateTagColor(r.Context(), user.ID, guid, addressbook.Tag{Name: body.Name, Color: body.Color})
	if err != nil {
		writeAbError(w, err)
		return
	}

	writeAbOK(w)
}

// HandleTagDelete handles DELETE /api/ab/tag/{guid} with a body of tag names.
func (h *AddressBookHandler) HandleTagDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		httpx.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	user, guid, ok := abRequest(w, r, "guid")
	if !ok {
		return
	}

	var names []string
	if err := json.NewDecoder(r.Body).Decode(&names); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if err := h.service.DeleteTags(r.Context(), user.ID, guid, names); err != nil {
		writeAbError(w, err)
		return
	}

	writeAbOK(w)
}
