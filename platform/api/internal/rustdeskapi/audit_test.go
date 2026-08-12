package rustdeskapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleAuditConnMethodNotAllowed(t *testing.T) {
	h := &AuditHandler{}
	req := httptest.NewRequest(http.MethodGet, "/api/audit/conn", nil)
	w := httptest.NewRecorder()

	h.HandleAuditConn(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405, got %d", w.Code)
	}
}

func TestHandleAuditFileMethodNotAllowed(t *testing.T) {
	h := &AuditHandler{}
	req := httptest.NewRequest(http.MethodGet, "/api/audit/file", nil)
	w := httptest.NewRecorder()

	h.HandleAuditFile(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405, got %d", w.Code)
	}
}
