package integration

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OpenDeskViewer/platform/api/internal/monitoring"
	"github.com/google/uuid"
)

// Monitoring and notifications, items 6.5 and 6.6.
//
// The property under test is the join. A transition recorded but never
// delivered, or a webhook that fires on nothing, would each look fine alone.

// receiver is a webhook endpoint that records what it was sent.
type receiver struct {
	server *httptest.Server

	mu        sync.Mutex
	bodies    []string
	events    []string
	signature string
	// status is what it answers; 200 unless a test wants a failure.
	status int
}

func newReceiver(t *testing.T) *receiver {
	t.Helper()

	r := &receiver{status: http.StatusOK}
	r.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)

		r.mu.Lock()
		r.bodies = append(r.bodies, string(body))
		r.events = append(r.events, req.Header.Get("X-ODV-Event"))
		r.signature = req.Header.Get("X-ODV-Signature")
		status := r.status
		r.mu.Unlock()

		w.WriteHeader(status)
	}))
	t.Cleanup(r.server.Close)
	return r
}

func (r *receiver) delivered() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.bodies)
}

// addTarget registers the receiver as a notification target. The URL check in
// the handler requires https, so this writes the row directly: the point of
// these tests is delivery, not the handler's validation, which the IDOR sweep
// and the unit tests already cover.
func addTarget(t *testing.T, f *fixture, url, secret string, eventTypes []string) uuid.UUID {
	t.Helper()

	var secretArg *string
	if secret != "" {
		secretArg = &secret
	}
	// NOT NULL with a default of '{}', so a nil slice is a constraint violation
	// rather than "no filter". The handler normalises the same way.
	if eventTypes == nil {
		eventTypes = []string{}
	}

	var id uuid.UUID
	if err := f.db.QueryRow(context.Background(), `
		INSERT INTO notification_targets (name, url, secret, event_types)
		VALUES ($1, $2, $3, $4) RETURNING id
	`, "target-"+uuid.NewString()[:8], url, secretArg, eventTypes).Scan(&id); err != nil {
		t.Fatalf("failed to add a notification target: %v", err)
	}
	return id
}

// A device that stops reporting is recorded as offline once, with a duration,
// and the event is enqueued for delivery.
func TestGoingOfflineIsRecordedAndEnqueued(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	monitor := monitoring.New(f.db)

	target := addTarget(t, f, "https://example.invalid/hook", "", nil)

	// The device was last heard from an hour ago.
	f.mustExec(t, `UPDATE devices SET last_seen_at = now() - interval '1 hour', connectivity = 'ONLINE' WHERE id = $1`, f.device1)

	moved, err := monitor.RecordAndNotify(ctx, "OFFLINE", 900, "STALE", "ONLINE")
	if err != nil {
		t.Fatal(err)
	}
	if moved != 1 {
		t.Fatalf("%d devices moved, want exactly the one that was overdue", moved)
	}

	var connectivity string
	if err := f.db.QueryRow(ctx, `SELECT connectivity FROM devices WHERE id = $1`, f.device1).Scan(&connectivity); err != nil {
		t.Fatal(err)
	}
	if connectivity != "OFFLINE" {
		t.Errorf("the device is %q", connectivity)
	}

	var recorded string
	var duration *int64
	if err := f.db.QueryRow(ctx, `
		SELECT connectivity, previous_duration_seconds FROM device_connectivity_events WHERE device_id = $1
	`, f.device1).Scan(&recorded, &duration); err != nil {
		t.Fatalf("no connectivity event was recorded: %v", err)
	}
	if recorded != "OFFLINE" {
		t.Errorf("the event records %q", recorded)
	}
	// The device had been unheard from for an hour, which is the number an
	// operator actually wants: not "it is offline" but "it has been for this
	// long".
	if duration == nil || *duration < 3500 {
		t.Errorf("previous_duration_seconds = %v, want about 3600", duration)
	}

	var queued int
	if err := f.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM notification_deliveries WHERE target_id = $1 AND event_type = 'device.offline'
	`, target).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Errorf("%d notifications queued, want 1", queued)
	}

	// Running again must not record it a second time: the device is already
	// OFFLINE, so it is no longer in the source set.
	again, err := monitor.RecordAndNotify(ctx, "OFFLINE", 900, "STALE", "ONLINE")
	if err != nil {
		t.Fatal(err)
	}
	if again != 0 {
		t.Errorf("a second pass moved %d devices; an unchanged fleet must produce no events", again)
	}
}

// The other direction, and it is on the heartbeat rather than the worker so a
// recovery does not wait up to a minute to be noticed.
func TestComingBackIsRecordedOnTheHeartbeat(t *testing.T) {
	f := newFixture(t)
	s := newDeviceServer(t, f)
	ctx := context.Background()

	const id = "950000001"
	secret, deviceID := enrollDevice(t, f, s, id)

	// Enrollment leaves the device ACTIVE and just-seen, so put it offline the
	// way the worker would.
	f.mustExec(t, `UPDATE devices SET connectivity = 'OFFLINE' WHERE id = $1`, deviceID)
	f.mustExec(t, `DELETE FROM device_connectivity_events WHERE device_id = $1`, deviceID)

	s.heartbeat(t, id, secret, 0)

	var connectivity string
	if err := f.db.QueryRow(ctx, `SELECT connectivity FROM devices WHERE id = $1`, deviceID).Scan(&connectivity); err != nil {
		t.Fatal(err)
	}
	if connectivity != "ONLINE" {
		t.Errorf("the device is %q after heartbeating", connectivity)
	}

	var events int
	if err := f.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM device_connectivity_events WHERE device_id = $1 AND connectivity = 'ONLINE'
	`, deviceID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Errorf("%d recovery events recorded, want 1", events)
	}

	// And a second heartbeat is not a second recovery. Without this the fleet
	// would produce an event every fifteen seconds per device forever.
	s.heartbeat(t, id, secret, 0)
	if err := f.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM device_connectivity_events WHERE device_id = $1 AND connectivity = 'ONLINE'
	`, deviceID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Errorf("a device that was already ONLINE produced %d recovery events", events)
	}
}

// Delivery, signing, and the fact that a delivered notification is not sent
// again.
func TestWebhookDeliveryIsSignedAndHappensOnce(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	monitor := monitoring.New(f.db)
	rx := newReceiver(t)

	const secret = "a-shared-secret"
	addTarget(t, f, rx.server.URL, secret, nil)

	payload := map[string]any{"device_name": "acme-hq-01", "to": "OFFLINE"}
	if err := monitor.Enqueue(ctx, monitoring.EventDeviceOffline, payload); err != nil {
		t.Fatal(err)
	}

	delivered, err := monitor.DeliverPending(ctx, rx.server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if delivered != 1 {
		t.Fatalf("%d delivered, want 1", delivered)
	}
	if rx.delivered() != 1 {
		t.Fatalf("the receiver got %d requests", rx.delivered())
	}

	rx.mu.Lock()
	body, event, signature := rx.bodies[0], rx.events[0], rx.signature
	rx.mu.Unlock()

	if event != monitoring.EventDeviceOffline {
		t.Errorf("X-ODV-Event = %q", event)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("the body is not JSON: %q", body)
	}
	if got["device_name"] != "acme-hq-01" {
		t.Errorf("the body does not name the device: %v", got)
	}

	// The signature is over the exact bytes sent, so a receiver that recomputes
	// it from the body it received agrees.
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if signature != want {
		t.Errorf("X-ODV-Signature = %q, want %q", signature, want)
	}

	// A second pass must send nothing. Without the delivered_at check, every
	// pass would resend the whole outbox.
	if again, err := monitor.DeliverPending(ctx, rx.server.Client()); err != nil {
		t.Fatal(err)
	} else if again != 0 {
		t.Errorf("a second pass delivered %d", again)
	}
	if rx.delivered() != 1 {
		t.Errorf("the receiver got %d requests in total", rx.delivered())
	}
}

// A receiver that refuses must not lose the notification, and must not be
// retried immediately.
func TestAFailedDeliveryIsRetriedLaterAndEventuallyAbandoned(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	monitor := monitoring.New(f.db)
	rx := newReceiver(t)
	rx.status = http.StatusInternalServerError

	target := addTarget(t, f, rx.server.URL, "", nil)
	if err := monitor.Enqueue(ctx, monitoring.EventDeviceOffline, map[string]any{"to": "OFFLINE"}); err != nil {
		t.Fatal(err)
	}

	if delivered, err := monitor.DeliverPending(ctx, rx.server.Client()); err != nil {
		t.Fatal(err)
	} else if delivered != 0 {
		t.Fatalf("%d reported delivered against a receiver answering 500", delivered)
	}

	var attempts int
	var nextAttempt time.Time
	var deliveredAt, abandonedAt *time.Time
	var lastError *string
	if err := f.db.QueryRow(ctx, `
		SELECT attempts, next_attempt_at, delivered_at, abandoned_at, last_error
		FROM notification_deliveries WHERE target_id = $1
	`, target).Scan(&attempts, &nextAttempt, &deliveredAt, &abandonedAt, &lastError); err != nil {
		t.Fatal(err)
	}

	if deliveredAt != nil {
		t.Error("a refused delivery is marked delivered")
	}
	if abandonedAt != nil {
		t.Error("a delivery was abandoned on its first failure")
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1", attempts)
	}
	if !nextAttempt.After(time.Now().Add(20 * time.Second)) {
		t.Errorf("next_attempt_at is %v, which is not a backoff", nextAttempt)
	}
	if lastError == nil {
		t.Error("no error was recorded, so nobody can see why it failed")
	}

	// An immediate second pass must find nothing due, or a failing receiver
	// would be hammered in a tight loop.
	if again, err := monitor.DeliverPending(ctx, rx.server.Client()); err != nil {
		t.Fatal(err)
	} else if again != 0 {
		t.Errorf("a delivery in backoff was retried immediately")
	}

	// Exhaust the budget. The row is abandoned rather than deleted, because
	// "which alerts never arrived" has to be answerable after an incident.
	f.mustExec(t, `UPDATE notification_deliveries SET attempts = 6, next_attempt_at = now() WHERE target_id = $1`, target)
	if _, err := monitor.DeliverPending(ctx, rx.server.Client()); err != nil {
		t.Fatal(err)
	}

	if err := f.db.QueryRow(ctx, `
		SELECT abandoned_at FROM notification_deliveries WHERE target_id = $1
	`, target).Scan(&abandonedAt); err != nil {
		t.Fatal(err)
	}
	if abandonedAt == nil {
		t.Error("a delivery past its attempt budget is still being retried")
	}

	var rows int
	if err := f.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM notification_deliveries WHERE target_id = $1`, target).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Error("the abandoned delivery was deleted, so nobody can see that it never arrived")
	}

	// The target's health reflects it, so a webhook that has been dead for a
	// week is visible rather than silently dropping everything.
	var failures int
	if err := f.db.QueryRow(ctx,
		`SELECT consecutive_failures FROM notification_targets WHERE id = $1`, target).Scan(&failures); err != nil {
		t.Fatal(err)
	}
	if failures < 2 {
		t.Errorf("consecutive_failures = %d after two failed passes", failures)
	}
}

// A target subscribed to specific events gets those and not the others.
func TestTargetsOnlyReceiveTheEventsTheySubscribedTo(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	monitor := monitoring.New(f.db)

	offlineOnly := addTarget(t, f, "https://example.invalid/offline", "", []string{monitoring.EventDeviceOffline})
	everything := addTarget(t, f, "https://example.invalid/all", "", nil)

	if err := monitor.Enqueue(ctx, monitoring.EventDeviceOnline, map[string]any{"to": "ONLINE"}); err != nil {
		t.Fatal(err)
	}

	var toOfflineOnly, toEverything int
	if err := f.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM notification_deliveries WHERE target_id = $1`, offlineOnly).Scan(&toOfflineOnly); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM notification_deliveries WHERE target_id = $1`, everything).Scan(&toEverything); err != nil {
		t.Fatal(err)
	}

	if toOfflineOnly != 0 {
		t.Error("a target subscribed to device.offline received a device.online")
	}
	if toEverything != 1 {
		t.Errorf("a target with no filter received %d of 1 event", toEverything)
	}
}

// The portal's side: the history is readable, and scoped the way the device is.
func TestConnectivityHistoryIsReadableAndScoped(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	monitor := monitoring.New(f.db)

	f.mustExec(t, `UPDATE devices SET last_seen_at = now() - interval '1 hour', connectivity = 'ONLINE'`)
	if _, err := monitor.RecordAndNotify(ctx, "OFFLINE", 900, "STALE", "ONLINE"); err != nil {
		t.Fatal(err)
	}

	tech := newV1Server(t, f, f.tech1ID)
	admin := newV1Server(t, f, f.adminID)

	// device1 is in tech1's reach; device4 is not.
	if w := tech.do(t, http.MethodGet, "/api/v1/devices/"+f.device1.String()+"/connectivity", ""); w.Code != http.StatusOK {
		t.Errorf("the technician got %d for their own device: %s", w.Code, w.Body.String())
	} else if page := decodeList(t, w); page.Total == 0 {
		t.Error("the history is empty for a device that just went offline")
	}

	if w := tech.do(t, http.MethodGet, "/api/v1/devices/"+f.device4.String()+"/connectivity", ""); w.Code != http.StatusForbidden {
		t.Errorf("the technician got %d for a foreign device, want 403", w.Code)
	}

	// The fleet view is administration.
	if w := admin.do(t, http.MethodGet, "/api/v1/monitoring/events", ""); w.Code != http.StatusOK {
		t.Errorf("the administrator got %d: %s", w.Code, w.Body.String())
	} else if page := decodeList(t, w); page.Total < 5 {
		t.Errorf("the fleet view shows %d events after five devices went offline", page.Total)
	}
	if w := tech.do(t, http.MethodGet, "/api/v1/monitoring/events", ""); w.Code != http.StatusForbidden {
		t.Errorf("the technician got %d for the fleet view, want 403", w.Code)
	}
}

// A webhook target's secret must never come back out of the API. It is the one
// value on that screen that would let somebody forge deliveries.
func TestTheWebhookSecretIsNeverReturned(t *testing.T) {
	f := newFixture(t)
	admin := newV1Server(t, f, f.adminID)

	w := admin.do(t, http.MethodPost, "/api/v1/notification-targets",
		`{"name":"ops","url":"https://hooks.example.com/odv","secret":"do-not-echo-this"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("creating a target got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "do-not-echo-this") {
		t.Error("the create response echoed the secret")
	}

	list := admin.do(t, http.MethodGet, "/api/v1/notification-targets", "")
	if strings.Contains(list.Body.String(), "do-not-echo-this") {
		t.Error("the target list returned the secret")
	}
	if !strings.Contains(list.Body.String(), `"has_secret":true`) {
		t.Error("the list does not say whether deliveries are signed")
	}

	// And the audit trail is read by the same people the secret is kept from.
	var metadata string
	if err := f.db.QueryRow(context.Background(), `
		SELECT metadata::text FROM audit_events WHERE event_type = 'notification_target.created'
	`).Scan(&metadata); err != nil {
		t.Fatalf("no audit event was written: %v", err)
	}
	if strings.Contains(metadata, "do-not-echo-this") {
		t.Error("the audit event recorded the secret")
	}
}

// http is refused. The payload names devices and customers and leaves this
// network, so it is one of the few places plaintext would be a real leak.
func TestAWebhookTargetMustBeHTTPS(t *testing.T) {
	f := newFixture(t)
	admin := newV1Server(t, f, f.adminID)

	w := admin.do(t, http.MethodPost, "/api/v1/notification-targets",
		`{"name":"plaintext","url":"http://hooks.example.com/odv"}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("a plaintext webhook URL got %d, want 400: %s", w.Code, w.Body.String())
	}
}
