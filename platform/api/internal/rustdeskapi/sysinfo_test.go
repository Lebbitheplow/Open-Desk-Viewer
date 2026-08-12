package rustdeskapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleSysinfoVerMethodNotAllowed(t *testing.T) {
	h := &SysinfoVerHandler{}
	req := httptest.NewRequest(http.MethodPost, "/api/sysinfo_ver", nil)
	w := httptest.NewRecorder()

	h.HandleSysinfoVer(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405, got %d", w.Code)
	}
}
