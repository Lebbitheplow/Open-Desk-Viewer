package rustdeskapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleUsersMethodNotAllowed(t *testing.T) {
	h := &UsersHandler{}
	req := httptest.NewRequest(http.MethodPost, "/api/users", nil)
	w := httptest.NewRecorder()

	h.HandleUsers(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405, got %d", w.Code)
	}
}
