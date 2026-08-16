package rustdeskapi

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/OpenDeskViewer/platform/api/internal/config"
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

// The deployment script is the desktop half of enrollment provisioning. Before
// this, it configured server identity only, so every client it provisioned
// heartbeated, was refused for want of a credential, and never joined the fleet.
func TestDeployScriptCarriesTheEnrollmentToken(t *testing.T) {
	h := NewAuditHandler(nil, nil, nil, &config.Config{
		PublicHost:        "odv.example.com",
		RustdeskPublicKey: "testkey",
	})

	run := func(t *testing.T, body string) (int, string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/devices/deploy", strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.HandleDevicesDeploy(rec, req)
		return rec.Code, rec.Body.String()
	}

	t.Run("token is provisioned", func(t *testing.T) {
		code, script := run(t, `{"enrollment_token":"tok-abc123"}`)
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if !strings.Contains(script, `rustdesk --option enrollment-token`) {
			t.Error("script does not provision the enrollment token")
		}
		if !strings.Contains(script, "tok-abc123") {
			t.Error("script does not carry the token value")
		}
	})

	t.Run("no token still configures, and says what is missing", func(t *testing.T) {
		code, script := run(t, ``)
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if !strings.Contains(script, "--config") {
			t.Error("script no longer configures server identity")
		}
		if !strings.Contains(script, "will not join the fleet") {
			t.Error("script does not warn that the client cannot enrol")
		}
	})

	// The token is interpolated into a shell script. A value needing quotes was
	// not issued by this server, so it is refused rather than escaped.
	t.Run("a token that would break out is refused", func(t *testing.T) {
		for _, bad := range []string{`a"b`, `a\b`, "a b", "a\nb"} {
			code, _ := run(t, `{"enrollment_token":`+strconv.Quote(bad)+`}`)
			if code != http.StatusBadRequest {
				t.Errorf("token %q: status = %d, want 400", bad, code)
			}
		}
	})
}
