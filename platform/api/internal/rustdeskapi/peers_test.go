package rustdeskapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandlePeersMethodNotAllowed(t *testing.T) {
	h := &PeerHandler{}
	req := httptest.NewRequest(http.MethodPost, "/api/peers", nil)
	w := httptest.NewRecorder()

	h.HandlePeers(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405, got %d", w.Code)
	}
}

func TestHandleDeviceGroupAccessibleMethodNotAllowed(t *testing.T) {
	h := &PeerHandler{}
	req := httptest.NewRequest(http.MethodPost, "/api/device-group/accessible", nil)
	w := httptest.NewRecorder()

	h.HandleDeviceGroupAccessible(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405, got %d", w.Code)
	}
}
