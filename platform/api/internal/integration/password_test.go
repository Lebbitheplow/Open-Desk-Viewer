package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// Platform-managed device connection passwords, end to end.
//
// The two halves are tested together on purpose. The portal half passing on its
// own only shows that a row can be written and read; the device half passing on
// its own only shows that a heartbeat can carry a string. What has to be true is
// that the password an administrator rotates is the password the device ends up
// checking, and that is a property of the two halves agreeing.

// enrollDevice redeems a fresh token and returns the device's secret and id.
func enrollDevice(t *testing.T, f *fixture, s *deviceServer, rustdeskID string) (secret string, deviceID uuid.UUID) {
	t.Helper()

	token := f.issueToken(t, nil, nil)
	w := s.post(t, "/api/enroll", fmt.Sprintf(
		`{"token":%q,"id":%q,"uuid":"uuid-%s","hostname":"pw-host","os":"Android 14","version":"1.3.0"}`,
		token.Token, rustdeskID, rustdeskID))
	if w.Code != http.StatusOK {
		t.Fatalf("enroll got %d: %s", w.Code, w.Body.String())
	}

	body := decodeJSON(t, w)
	secret, _ = body["device_token"].(string)
	if secret == "" {
		t.Fatal("enrollment returned no device token")
	}

	if err := f.db.QueryRow(context.Background(),
		`SELECT id FROM devices WHERE rustdesk_id = $1`, rustdeskID).Scan(&deviceID); err != nil {
		t.Fatalf("failed to read the enrolled device: %v", err)
	}
	return secret, deviceID
}

// heartbeat posts one heartbeat carrying the device's credential and the
// password version it believes it has applied.
func (s *deviceServer) heartbeat(t *testing.T, rustdeskID, secret string, passwordVersion int64) map[string]any {
	t.Helper()

	w := s.post(t, "/api/heartbeat", fmt.Sprintf(
		`{"id":%q,"uuid":"uuid-%s","device_token":%q,"password_version":%d}`,
		rustdeskID, rustdeskID, secret, passwordVersion))
	if w.Code != http.StatusOK {
		t.Fatalf("heartbeat got %d: %s", w.Code, w.Body.String())
	}
	return decodeJSON(t, w)
}

// devicePassword pulls the password out of a heartbeat response, and reports
// whether one was sent at all.
func devicePassword(t *testing.T, resp map[string]any) (string, int64, bool) {
	t.Helper()

	raw, ok := resp["device_password"]
	if !ok {
		return "", 0, false
	}
	entry, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("device_password is not an object: %#v", raw)
	}
	value, _ := entry["value"].(string)
	version, _ := entry["version"].(json.Number)
	if value == "" {
		t.Fatalf("device_password carried no value: %#v", entry)
	}
	n, err := version.Int64()
	if err != nil {
		// json.Unmarshal into map[string]any gives float64 unless the decoder
		// uses numbers, so fall back rather than failing on the representation.
		if f, ok := entry["version"].(float64); ok {
			return value, int64(f), true
		}
		t.Fatalf("device_password version is not a number: %#v", entry["version"])
	}
	return value, n, true
}

// The core loop: a device that has never had a password gets one, and once it
// has echoed the version back it stops being sent one.
func TestHeartbeatDeliversAPasswordExactlyUntilItIsApplied(t *testing.T) {
	f := newFixture(t)
	s := newDeviceServer(t, f)

	const id = "700000101"
	secret, deviceID := enrollDevice(t, f, s, id)

	first := s.heartbeat(t, id, secret, 0)
	password, version, sent := devicePassword(t, first)
	if !sent {
		t.Fatal("the first heartbeat carried no password, so the device would never get one")
	}
	if version != 1 {
		t.Fatalf("the first password is version %d, want 1", version)
	}

	// The device has not confirmed yet, so the same password is offered again.
	// This is the point: a response lost to a dropped connection must not lose
	// the password with it.
	second := s.heartbeat(t, id, secret, 0)
	repeat, repeatVersion, sent := devicePassword(t, second)
	if !sent {
		t.Fatal("the password was sent once and not repeated, so a lost response loses it forever")
	}
	if repeat != password || repeatVersion != version {
		t.Fatalf("the repeat offered a different password: %q v%d then %q v%d",
			password, version, repeat, repeatVersion)
	}

	if applied := appliedVersion(t, f, deviceID); applied != nil {
		t.Fatalf("the device is recorded as having applied v%d before it confirmed anything", *applied)
	}

	// Now the device echoes the version. That is the acknowledgement.
	third := s.heartbeat(t, id, secret, version)
	if _, _, sent := devicePassword(t, third); sent {
		t.Error("the password is still being pushed after the device confirmed it")
	}

	applied := appliedVersion(t, f, deviceID)
	if applied == nil || *applied != version {
		t.Fatalf("applied_version is %v after the device confirmed v%d", applied, version)
	}
}

// Rotation is the whole reason this exists: the new password has to reach the
// device, and the portal has to be honest about the window before it does.
func TestRotationReachesTheDeviceAndSaysSoOnlyWhenItHas(t *testing.T) {
	f := newFixture(t)
	s := newDeviceServer(t, f)
	admin := newV1Server(t, f, f.adminID)

	const id = "700000102"
	secret, deviceID := enrollDevice(t, f, s, id)

	original, version, _ := devicePassword(t, s.heartbeat(t, id, secret, 0))
	s.heartbeat(t, id, secret, version) // confirm it

	w := admin.do(t, http.MethodPost, "/api/v1/devices/"+deviceID.String()+"/password/rotate", "")
	if w.Code != http.StatusOK {
		t.Fatalf("rotate got %d: %s", w.Code, w.Body.String())
	}
	rotated := decodeJSON(t, w)

	if rotated["password"] == original {
		t.Error("rotation returned the same password")
	}
	// The honest part. The device has not heartbeated since, so the machine is
	// still accepting the old password and the response must not imply
	// otherwise.
	if rotated["applied"] != false {
		t.Error("a rotation the device has not seen yet reports as applied")
	}
	if rotated["delivered_at_heartbeat"] != true {
		t.Error("the response does not say the change is delivered at the next heartbeat")
	}

	next := s.heartbeat(t, id, secret, version)
	delivered, newVersion, sent := devicePassword(t, next)
	if !sent {
		t.Fatal("the rotated password was not delivered at the next heartbeat")
	}
	if delivered == original {
		t.Error("the device was handed the password that was just rotated away")
	}
	if delivered != rotated["password"] {
		t.Errorf("the device got %q while the portal showed the administrator %q", delivered, rotated["password"])
	}
	if newVersion <= version {
		t.Errorf("the rotated password is version %d, which does not follow %d", newVersion, version)
	}

	s.heartbeat(t, id, secret, newVersion)
	applied := appliedVersion(t, f, deviceID)
	if applied == nil || *applied != newVersion {
		t.Fatalf("applied_version is %v after the device confirmed v%d", applied, newVersion)
	}
}

// A technician with access reads it; the audit trail says who. Without the
// second half the endpoint is just a credential dispenser.
func TestRevealIsAllowedForAccessAndAlwaysAudited(t *testing.T) {
	f := newFixture(t)
	s := newDeviceServer(t, f)
	tech := newV1Server(t, f, f.tech1ID)

	const id = "700000103"
	secret, deviceID := enrollDevice(t, f, s, id)
	// The enrolled device lands in deviceGroup1, which group1 reaches, which is
	// tech1's group.
	expected, _, _ := devicePassword(t, s.heartbeat(t, id, secret, 0))

	w := tech.do(t, http.MethodGet, "/api/v1/devices/"+deviceID.String()+"/password", "")
	if w.Code != http.StatusOK {
		t.Fatalf("reveal got %d: %s", w.Code, w.Body.String())
	}
	body := decodeJSON(t, w)
	if body["password"] != expected {
		t.Errorf("the portal showed %q while the device was given %q", body["password"], expected)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control is %q, so a credential may be cached between here and the browser", got)
	}

	var events int
	if err := f.db.QueryRow(context.Background(), `
		SELECT COUNT(*) FROM audit_events
		WHERE event_type = 'device.password_revealed' AND user_id = $1 AND device_id = $2
	`, f.tech1ID, deviceID).Scan(&events); err != nil {
		t.Fatalf("failed to read audit events: %v", err)
	}
	if events != 1 {
		t.Errorf("reveal wrote %d audit events, want exactly 1", events)
	}
}

// Rotation is an administrator's. A technician who can read the password must
// not be able to change it: they are about to be removed from the group, and
// rotating on the way out would be them keeping the only working credential.
func TestTechnicianMayReadButNotRotate(t *testing.T) {
	f := newFixture(t)
	s := newDeviceServer(t, f)
	tech := newV1Server(t, f, f.tech1ID)

	const id = "700000104"
	secret, deviceID := enrollDevice(t, f, s, id)
	s.heartbeat(t, id, secret, 0)

	if w := tech.do(t, http.MethodGet, "/api/v1/devices/"+deviceID.String()+"/password", ""); w.Code != http.StatusOK {
		t.Fatalf("technician read got %d, want 200: %s", w.Code, w.Body.String())
	}
	if w := tech.do(t, http.MethodPost, "/api/v1/devices/"+deviceID.String()+"/password/rotate", ""); w.Code != http.StatusForbidden {
		t.Fatalf("technician rotate got %d, want 403: %s", w.Code, w.Body.String())
	}
}

// Withdrawing access rotates. This is the property the README used to have to
// disclaim: before this, removing a technician from a support group left every
// password they had been shown working.
func TestRemovingATechnicianRotatesTheirDevices(t *testing.T) {
	f := newFixture(t)
	s := newDeviceServer(t, f)
	admin := newV1Server(t, f, f.adminID)

	const id = "700000105"
	secret, _ := enrollDevice(t, f, s, id)
	before, version, _ := devicePassword(t, s.heartbeat(t, id, secret, 0))
	s.heartbeat(t, id, secret, version)

	w := admin.do(t, http.MethodDelete,
		fmt.Sprintf("/api/v1/support-groups/%s/technicians/%d", f.group1, f.tech1ID), "")
	if w.Code != http.StatusOK {
		t.Fatalf("removing the technician got %d: %s", w.Code, w.Body.String())
	}
	body := decodeJSON(t, w)
	if n, _ := body["passwords_rotated"].(float64); n < 1 {
		t.Fatalf("removing the technician rotated %v passwords; the device they could reach was not among them", body["passwords_rotated"])
	}

	after, newVersion, sent := devicePassword(t, s.heartbeat(t, id, secret, version))
	if !sent {
		t.Fatal("the rotated password never reached the device")
	}
	if after == before {
		t.Error("the password did not change when the technician was removed")
	}
	if newVersion <= version {
		t.Errorf("the rotation did not advance the version: %d then %d", version, newVersion)
	}
}

// Revoking a device group rotates only that group's devices. Rotating the whole
// support group's reach would be churn on machines nobody lost access to.
func TestRevokingADeviceGroupRotatesOnlyItsDevices(t *testing.T) {
	f := newFixture(t)
	admin := newV1Server(t, f, f.adminID)

	// Give both fixture devices a password to compare against.
	seedPassword(t, f, f.device1)
	seedPassword(t, f, f.device4)

	group1Before := passwordVersion(t, f, f.device1)
	group2Before := passwordVersion(t, f, f.device4)

	w := admin.do(t, http.MethodDelete,
		fmt.Sprintf("/api/v1/support-groups/%s/device-groups/%s", f.group1, f.deviceGroup1), "")
	if w.Code != http.StatusOK {
		t.Fatalf("revoking the device group got %d: %s", w.Code, w.Body.String())
	}

	if got := passwordVersion(t, f, f.device1); got == group1Before {
		t.Error("a device in the revoked group kept its password")
	}
	if got := passwordVersion(t, f, f.device4); got != group2Before {
		t.Errorf("a device in an unrelated group was rotated from v%d to v%d", group2Before, got)
	}
}

// Enrollment leaves the device under a policy where the platform's password is
// the credential that actually decides. Without this the password would be held,
// shown and rotated while the device went on accepting a temporary password it
// generated for itself.
func TestEnrollmentSeedsThePasswordPolicy(t *testing.T) {
	f := newFixture(t)
	s := newDeviceServer(t, f)

	const id = "700000106"
	_, deviceID := enrollDevice(t, f, s, id)

	options := map[string]string{}
	if err := f.db.QueryRow(context.Background(),
		`SELECT config_options FROM device_strategies WHERE device_id = $1`, deviceID).Scan(&options); err != nil {
		t.Fatalf("enrollment seeded no strategy: %v", err)
	}
	if options["verification-method"] != "use-permanent-password" {
		t.Errorf("verification-method is %q, so the device still accepts a self-generated temporary password",
			options["verification-method"])
	}
	if options["approve-mode"] != "password" {
		t.Errorf("approve-mode is %q, so an unattended device still needs somebody to click at it",
			options["approve-mode"])
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func appliedVersion(t *testing.T, f *fixture, deviceID uuid.UUID) *int64 {
	t.Helper()

	var applied *int64
	if err := f.db.QueryRow(context.Background(),
		`SELECT applied_version FROM device_passwords WHERE device_id = $1`, deviceID).Scan(&applied); err != nil {
		t.Fatalf("failed to read applied_version: %v", err)
	}
	return applied
}

func passwordVersion(t *testing.T, f *fixture, deviceID uuid.UUID) int64 {
	t.Helper()

	var version int64
	if err := f.db.QueryRow(context.Background(),
		`SELECT version FROM device_passwords WHERE device_id = $1`, deviceID).Scan(&version); err != nil {
		t.Fatalf("failed to read the password version: %v", err)
	}
	return version
}

func seedPassword(t *testing.T, f *fixture, deviceID uuid.UUID) {
	t.Helper()

	if _, err := newDevicePasswords(t, f).Rotate(context.Background(), deviceID, nil); err != nil {
		t.Fatalf("failed to seed a device password: %v", err)
	}
}
