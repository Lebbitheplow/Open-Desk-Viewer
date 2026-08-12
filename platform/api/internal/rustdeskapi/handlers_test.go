package rustdeskapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleLoginOptions(t *testing.T) {
	h := &Handlers{}
	req := httptest.NewRequest(http.MethodGet, "/api/login-options", nil)
	w := httptest.NewRecorder()

	h.HandleLoginOptions(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
}
