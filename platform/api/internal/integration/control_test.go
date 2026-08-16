package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// The heartbeat control channel from the portal's side.
//
// enrollment_test.go covers the device's half: that a queued disconnect is
// delivered once, and that a strategy is withheld until its version changes.
// What was verified only by hand is the half that puts something in the queue,
// which is where the authorisation and the allowlist live.

// TestPortalDisconnectReachesTheDevice is the whole channel end to end: the
// portal queues, the device collects. Each half was tested separately, and a
// mismatch between them would have passed both.
func TestPortalDisconnectReachesTheDevice(t *testing.T) {
	f := newFixture(t)
	devices := newDeviceServer(t, f)
	portal := newV1Server(t, f, f.adminID)

	token := f.issueToken(t, nil, nil)
	w := devices.post(t, "/api/enroll", fmt.Sprintf(`{"token":%q,"id":"760000001"}`, token.Token))
	if w.Code != http.StatusOK {
		t.Fatalf("enrollment got %d: %s", w.Code, w.Body.String())
	}
	enrolled := decodeJSON(t, w)
	secret := enrolled["device_token"].(string)
	deviceID := enrolled["device_id"].(string)

	queued := portal.do(t, http.MethodPost, "/api/v1/devices/"+deviceID+"/disconnect", `{"conn_ids":[11,12]}`)
	if queued.Code != http.StatusOK {
		t.Fatalf("disconnect got %d: %s", queued.Code, queued.Body.String())
	}
	body := decodeMap(t, queued)
	if body["queued"] != float64(2) {
		t.Errorf("queued = %v, want 2", body["queued"])
	}
	// The response must not claim the sessions are already gone. They are not:
	// the device is polling on a 15-second timer.
	if body["delivered_at_heartbeat"] != true {
		t.Errorf("delivered_at_heartbeat = %v, want true", body["delivered_at_heartbeat"])
	}

	hb := devices.post(t, "/api/heartbeat", fmt.Sprintf(`{"id":"760000001","device_token":%q}`, secret))
	var resp struct {
		Disconnect []int32 `json:"disconnect"`
	}
	if err := json.Unmarshal(hb.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode the heartbeat: %v", err)
	}
	if len(resp.Disconnect) != 2 {
		t.Fatalf("the device received %v, want the two ids the portal queued", resp.Disconnect)
	}

	// And the operator who did it is on the record.
	var count int64
	if err := f.db.QueryRow(context.Background(), `
		SELECT COUNT(*) FROM audit_events
		WHERE event_type = 'device.disconnect_requested' AND user_id = $1
	`, f.adminID).Scan(&count); err != nil {
		t.Fatalf("failed to count audit events: %v", err)
	}
	if count != 1 {
		t.Errorf("expected one device.disconnect_requested audit row naming the admin, got %d", count)
	}
}

// With no body, the endpoint ends every session the platform believes is open.
// "Believes" is the operative word: it reads the audit trail, so a session
// already recorded as ENDED must not be queued again.
func TestDisconnectWithNoBodyEndsEveryOpenSession(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	portal := newV1Server(t, f, f.adminID)

	f.mustExec(t, `
		INSERT INTO connection_sessions (device_id, user_id, start_time, status, client_id)
		VALUES ($1, $2, now(), 'STARTED',   '800/21'),
		       ($1, $2, now(), 'REQUESTED', '800/22'),
		       ($1, $2, now(), 'ENDED',     '800/23'),
		       -- A note row filed without a conn id. The pattern guard exists
		       -- for this: split_part on it would yield an empty string and the
		       -- cast to int would fail the whole query.
		       ($1, $2, now(), 'STARTED',   'not-a-conn-id')
	`, f.device1, f.adminID)

	w := portal.do(t, http.MethodPost, "/api/v1/devices/"+f.device1.String()+"/disconnect", "")
	if w.Code != http.StatusOK {
		t.Fatalf("disconnect got %d: %s", w.Code, w.Body.String())
	}
	if queued := decodeMap(t, w)["queued"]; queued != float64(2) {
		t.Errorf("queued = %v, want the two open sessions only", queued)
	}

	var ids []int32
	rows, err := f.db.Query(ctx,
		`SELECT conn_id FROM device_disconnect_requests WHERE device_id = $1 ORDER BY conn_id`, f.device1)
	if err != nil {
		t.Fatalf("failed to read the queue: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int32
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("failed to scan conn id: %v", err)
		}
		ids = append(ids, id)
	}
	if len(ids) != 2 || ids[0] != 21 || ids[1] != 22 {
		t.Errorf("queued conn ids = %v, want [21 22]", ids)
	}
}

// A device with nothing open is a success, not a 404. The caller asked for it to
// have no live sessions and it has none.
func TestDisconnectWithNothingOpenSucceeds(t *testing.T) {
	f := newFixture(t)
	portal := newV1Server(t, f, f.adminID)

	w := portal.do(t, http.MethodPost, "/api/v1/devices/"+f.device1.String()+"/disconnect", "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if queued := decodeMap(t, w)["queued"]; queued != float64(0) {
		t.Errorf("queued = %v, want 0", queued)
	}
}

// The allowlist is the other half of 1.3. These four keys decide which
// deployment a client belongs to, and pushing them from the portal would reopen
// from the inside exactly the door OVERWRITE_SETTINGS is meant to close at the
// client build.
func TestStrategyRefusesServerIdentityKeys(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	portal := newV1Server(t, f, f.adminID)

	for _, key := range []string{"custom-rendezvous-server", "api-server", "relay-server", "key"} {
		t.Run(key, func(t *testing.T) {
			body := fmt.Sprintf(`{"config_options":{%q:"evil.example.com"}}`, key)
			w := portal.do(t, http.MethodPut, "/api/v1/devices/"+f.device1.String()+"/strategy", body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for %s, got %d: %s", key, w.Code, w.Body.String())
			}
		})
	}

	// A refusal that half-writes is worse than no refusal: the operator sees an
	// error and the device gets the configuration anyway.
	var rows int64
	if err := f.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM device_strategies WHERE device_id = $1`, f.device1).Scan(&rows); err != nil {
		t.Fatalf("failed to count strategies: %v", err)
	}
	if rows != 0 {
		t.Error("a refused strategy was stored anyway")
	}

	// One bad key in an otherwise valid map rejects the whole request, rather
	// than silently applying the rest, which would look applied and not be.
	mixed := portal.do(t, http.MethodPut, "/api/v1/devices/"+f.device1.String()+"/strategy",
		`{"config_options":{"enable-audio":"N","api-server":"http://evil.example.com"}}`)
	if mixed.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a map containing a locked key, got %d", mixed.Code)
	}
	if err := f.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM device_strategies WHERE device_id = $1`, f.device1).Scan(&rows); err != nil {
		t.Fatalf("failed to count strategies: %v", err)
	}
	if rows != 0 {
		t.Error("the allowed half of a rejected strategy was stored")
	}
}

// The strategy round trips, and its version moves when it changes. The version
// is what the device compares against, so a stalled modified_at means an update
// that never reaches the machine.
func TestStrategyRoundTripsAndVersions(t *testing.T) {
	f := newFixture(t)
	portal := newV1Server(t, f, f.adminID)
	path := "/api/v1/devices/" + f.device1.String() + "/strategy"

	// No strategy pushed yet is a valid state, not a 404.
	empty := portal.do(t, http.MethodGet, path, "")
	if empty.Code != http.StatusOK {
		t.Fatalf("expected 200 for a device with no strategy, got %d: %s", empty.Code, empty.Body.String())
	}
	emptyBody := decodeMap(t, empty)
	if emptyBody["modified_at"] != float64(0) {
		t.Errorf("modified_at = %v before anything is pushed, want 0", emptyBody["modified_at"])
	}
	// The portal needs to know which keys it may offer, rather than guessing and
	// finding out with a 400.
	pushable, ok := emptyBody["pushable_keys"].([]any)
	if !ok || len(pushable) == 0 {
		t.Fatalf("pushable_keys = %v, want the allowlist", emptyBody["pushable_keys"])
	}
	for _, key := range pushable {
		if key == "api-server" || key == "key" {
			t.Errorf("pushable_keys advertises %v, which is locked at the client build", key)
		}
	}

	put := portal.do(t, http.MethodPut, path, `{"config_options":{"enable-file-transfer":"N"}}`)
	if put.Code != http.StatusOK {
		t.Fatalf("put got %d: %s", put.Code, put.Body.String())
	}
	first := decodeMap(t, put)["modified_at"].(float64)
	if first == 0 {
		t.Fatal("modified_at is 0 after a push, so the device would never apply it")
	}

	got := decodeMap(t, portal.do(t, http.MethodGet, path, ""))
	options, ok := got["config_options"].(map[string]any)
	if !ok || options["enable-file-transfer"] != "N" {
		t.Fatalf("config_options = %v, want the pushed value", got["config_options"])
	}
	if got["modified_at"] != first {
		t.Errorf("GET reports modified_at %v, PUT reported %v", got["modified_at"], first)
	}

	// Pushing again replaces rather than accumulating: the device applies what
	// it is given, so a stale key left behind is a setting nobody chose.
	second := portal.do(t, http.MethodPut, path, `{"config_options":{"enable-clipboard":"N"}}`)
	if second.Code != http.StatusOK {
		t.Fatalf("second put got %d: %s", second.Code, second.Body.String())
	}
	after := decodeMap(t, portal.do(t, http.MethodGet, path, ""))
	afterOptions := after["config_options"].(map[string]any)
	if _, present := afterOptions["enable-file-transfer"]; present {
		t.Error("the replaced key survived the second push")
	}
	if afterOptions["enable-clipboard"] != "N" {
		t.Errorf("config_options = %v after the second push", afterOptions)
	}
}

// Reading a device's configuration is a technician's business; writing it is
// not. This is the one route in the pair where the two verbs have different
// gates, so it is the one where a refactor would quietly level them.
func TestStrategyWriteRequiresAdminEvenOnAReachableDevice(t *testing.T) {
	f := newFixture(t)
	tech := newV1Server(t, f, f.tech1ID)
	path := "/api/v1/devices/" + f.device1.String() + "/strategy"

	if w := tech.do(t, http.MethodGet, path, ""); w.Code != http.StatusOK {
		t.Errorf("expected a technician to read their own device's strategy, got %d: %s", w.Code, w.Body.String())
	}

	w := tech.do(t, http.MethodPut, path, `{"config_options":{"enable-audio":"N"}}`)
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 when a technician writes configuration, got %d: %s", w.Code, w.Body.String())
	}

	// The disconnect is deliberately the other way round: ending a session on a
	// device you may connect to is a technician's job.
	if w := tech.do(t, http.MethodPost, "/api/v1/devices/"+f.device1.String()+"/disconnect", `{"conn_ids":[31]}`); w.Code != http.StatusOK {
		t.Errorf("expected a technician to disconnect their own device, got %d: %s", w.Code, w.Body.String())
	}
}

// Observations are what auto-registration was replaced with, so an operator has
// to be able to see them. An empty list and a broken query look the same from
// the portal, which is why this asserts on a seeded row.
func TestDeviceObservationsAreListed(t *testing.T) {
	f := newFixture(t)
	portal := newV1Server(t, f, f.adminID)
	devices := newDeviceServer(t, f)

	// An id reporting in with no credential, twice, is one row with two
	// sightings rather than two rows.
	for i := 0; i < 2; i++ {
		if w := devices.post(t, "/api/heartbeat", `{"id":"770000001"}`); w.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 for an unenrolled device, got %d: %s", w.Code, w.Body.String())
		}
	}

	list := decodeList(t, portal.do(t, http.MethodGet, "/api/v1/device-observations", ""))
	if list.Total != 1 {
		t.Fatalf("expected one observation, got %d", list.Total)
	}

	var o struct {
		RustdeskID string `json:"rustdesk_id"`
		Sightings  int64  `json:"sightings"`
	}
	if err := json.Unmarshal(list.Data[0], &o); err != nil {
		t.Fatalf("failed to decode the observation: %v", err)
	}
	if o.RustdeskID != "770000001" {
		t.Errorf("rustdesk_id = %q", o.RustdeskID)
	}
	if o.Sightings != 2 {
		t.Errorf("sightings = %d, want 2 for the same id twice", o.Sightings)
	}

	// And it did not become a device.
	var devicesWithThatID int64
	if err := f.db.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM devices WHERE rustdesk_id = '770000001'`).Scan(&devicesWithThatID); err != nil {
		t.Fatalf("failed to count devices: %v", err)
	}
	if devicesWithThatID != 0 {
		t.Error("an unenrolled id was registered as a device")
	}
}

// A disconnect queued for a device that does not exist must not create a row
// pointing at nothing. The foreign key is what enforces it; this checks the
// handler surfaces that as the caller's error rather than a 500.
func TestDisconnectForAnUnknownDeviceIsRefused(t *testing.T) {
	f := newFixture(t)
	portal := newV1Server(t, f, f.adminID)

	w := portal.do(t, http.MethodPost, "/api/v1/devices/"+uuid.NewString()+"/disconnect", `{"conn_ids":[1]}`)
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for an unknown device id, got %d: %s", w.Code, w.Body.String())
	}
}
