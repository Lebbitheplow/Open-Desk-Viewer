package rustdeskapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/OpenDeskViewer/platform/api/internal/addressbook"
)

// The client is strict about methods, so each route accepts exactly one.
func TestAddressBookRoutesRejectTheWrongMethod(t *testing.T) {
	h := &AddressBookHandler{}

	cases := []struct {
		name    string
		method  string
		handler http.HandlerFunc
	}{
		{"settings", http.MethodGet, h.HandleSettings},
		{"personal", http.MethodGet, h.HandlePersonal},
		{"shared profiles", http.MethodGet, h.HandleSharedProfiles},
		{"peers", http.MethodGet, h.HandlePeers},
		{"tags", http.MethodGet, h.HandleTags},
		{"peer add", http.MethodPut, h.HandlePeerAdd},
		{"peer update", http.MethodPost, h.HandlePeerUpdate},
		{"peer delete", http.MethodPost, h.HandlePeerDelete},
		{"tag add", http.MethodPut, h.HandleTagAdd},
		{"tag rename", http.MethodPost, h.HandleTagRename},
		{"tag update", http.MethodPost, h.HandleTagUpdate},
		{"tag delete", http.MethodPost, h.HandleTagDelete},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			tc.handler(w, httptest.NewRequest(tc.method, "/api/ab/x", nil))

			if w.Code != http.StatusMethodNotAllowed {
				t.Errorf("expected 405, got %d", w.Code)
			}
		})
	}
}

// max_peer_one_ab is what the client caps a book at; zero means no limit.
func TestHandleSettingsReportsThePeerCap(t *testing.T) {
	h := &AddressBookHandler{maxPeerOneAb: 250}

	w := httptest.NewRecorder()
	h.HandleSettings(w, httptest.NewRequest(http.MethodPost, "/api/ab/settings", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var body map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if body["max_peer_one_ab"] != float64(250) {
		t.Errorf("expected the configured cap, got %v", body["max_peer_one_ab"])
	}
}

func TestAddressBookRoutesRequireAUser(t *testing.T) {
	h := &AddressBookHandler{}

	w := httptest.NewRecorder()
	h.HandlePersonal(w, httptest.NewRequest(http.MethodPost, "/api/ab/personal", nil))

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 with no user in context, got %d", w.Code)
	}
}

// The client treats any non-empty body on a 200 as an error message, so a
// successful mutation has to write nothing at all.
func TestWriteAbOKWritesAnEmptyBody(t *testing.T) {
	w := httptest.NewRecorder()
	writeAbOK(w)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Errorf("expected an empty body, got %q", w.Body.String())
	}
}

func TestWriteAbErrorMapsServiceErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"forbidden", addressbook.ErrForbidden, http.StatusForbidden},
		{"read only", addressbook.ErrReadOnly, http.StatusForbidden},
		{"not found", addressbook.ErrNotFound, http.StatusNotFound},
		{"anything else", errString("boom"), http.StatusInternalServerError},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			writeAbError(w, tc.err)

			if w.Code != tc.want {
				t.Errorf("expected %d, got %d", tc.want, w.Code)
			}

			// The client reads the error field, so it has to be present.
			var body map[string]interface{}
			if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
				t.Fatalf("failed to decode: %v", err)
			}
			if _, ok := body["error"]; !ok {
				t.Error("expected an error field in the body")
			}
		})
	}
}

type errString string

func (e errString) Error() string { return string(e) }

// The client serialises forceAlwaysRelay as the string "true", not as a bool.
func TestFlexBoolAcceptsBothEncodings(t *testing.T) {
	cases := []struct {
		json string
		want bool
	}{
		{`true`, true},
		{`false`, false},
		{`"true"`, true},
		{`"false"`, false},
	}

	for _, tc := range cases {
		var got flexBool
		if err := json.Unmarshal([]byte(tc.json), &got); err != nil {
			t.Fatalf("failed to decode %s: %v", tc.json, err)
		}
		if bool(got) != tc.want {
			t.Errorf("%s: expected %v, got %v", tc.json, tc.want, bool(got))
		}
	}

	var got flexBool
	if err := json.Unmarshal([]byte(`42`), &got); err == nil {
		t.Error("expected a number to be rejected")
	}
}
