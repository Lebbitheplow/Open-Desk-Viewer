// Package monitoring turns the fleet's connectivity into history and events.
//
// The platform already knew whether a device was up: telemetry recomputes
// devices.connectivity every minute and the heartbeat sets it back to ONLINE.
// What it had was one column, holding one value, which answers "is this machine
// up now" and nothing else. An operator asking "how often does this site drop
// out" or "tell me when a machine goes down" had nothing to work with.
//
// So this package records the transitions the recomputation already performs,
// and enqueues a notification for each one. It collects no new data from any
// device.
package monitoring

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/OpenDeskViewer/platform/api/internal/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Event types, which are also the notification event types an operator
// subscribes a webhook target to.
const (
	EventDeviceOffline = "device.offline"
	EventDeviceStale   = "device.stale"
	EventDeviceOnline  = "device.online"
)

// Service records connectivity transitions.
type Service struct {
	db *postgres.Pool
}

// New creates a monitoring service.
func New(db *postgres.Pool) *Service {
	return &Service{db: db}
}

// Transition is one device changing connectivity.
type Transition struct {
	DeviceID                uuid.UUID
	DeviceName              string
	RustdeskID              string
	CustomerName            *string
	From                    string
	To                      string
	PreviousDurationSeconds *int64
	OccurredAt              time.Time
}

// EventType maps a transition to the event an operator subscribes to. The name
// describes the state entered, because that is what somebody reading an alert
// at three in the morning needs first.
func (t Transition) EventType() string {
	switch t.To {
	case "OFFLINE":
		return EventDeviceOffline
	case "STALE":
		return EventDeviceStale
	case "ONLINE":
		return EventDeviceOnline
	default:
		return "device.connectivity_changed"
	}
}

// Payload is the notification body. Flat and self-describing: a receiver should
// not have to call back for the device's name to write a useful alert.
func (t Transition) Payload() map[string]any {
	payload := map[string]any{
		"device_id":   t.DeviceID.String(),
		"device_name": t.DeviceName,
		"rustdesk_id": t.RustdeskID,
		"from":        t.From,
		"to":          t.To,
		"occurred_at": t.OccurredAt.UTC().Format(time.RFC3339),
	}
	if t.CustomerName != nil {
		payload["customer"] = *t.CustomerName
	}
	if t.PreviousDurationSeconds != nil {
		payload["previous_duration_seconds"] = *t.PreviousDurationSeconds
	}
	return payload
}

// RecordTransitions applies a connectivity change to every device matching the
// condition, and returns what changed.
//
// The whole thing is one statement, and that is the point rather than an
// optimisation. Reading the devices that are about to change and then updating
// them is a race: the heartbeat that arrives between the two would be
// overwritten, and the device would be reported offline while it was talking to
// us. `UPDATE … RETURNING` cannot lose that race.
//
// staleSeconds is compared against last_seen_at. The caller supplies the
// condition rather than this package inventing thresholds, because the
// thresholds are already configuration that telemetry reads.
func (s *Service) RecordTransitions(ctx context.Context, to string, afterSeconds int64, from ...string) ([]Transition, error) {
	if len(from) == 0 {
		return nil, fmt.Errorf("RecordTransitions needs at least one source state")
	}

	rows, err := s.db.Query(ctx, `
		WITH changed AS (
			UPDATE devices d
			SET connectivity = $1::varchar, updated_at = now()
			WHERE d.last_seen_at IS NOT NULL
			  AND now() - d.last_seen_at > make_interval(secs => $2::double precision)
			  AND d.connectivity = ANY($3::text[])
			RETURNING d.id, d.name, d.rustdesk_id, d.customer_id, d.connectivity AS new_state,
			          EXTRACT(EPOCH FROM (now() - d.last_seen_at))::bigint AS since_seen
		),
		recorded AS (
			INSERT INTO device_connectivity_events
				(device_id, previous_connectivity, connectivity, previous_duration_seconds)
			SELECT c.id, NULL, c.new_state, c.since_seen FROM changed c
			RETURNING device_id, occurred_at
		)
		SELECT c.id, c.name, c.rustdesk_id, cu.name, c.new_state, c.since_seen, r.occurred_at
		FROM changed c
		JOIN recorded r ON r.device_id = c.id
		LEFT JOIN customers cu ON cu.id = c.customer_id
	`, to, afterSeconds, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return s.scanTransitions(rows, from[0])
}

// RecordHeartbeatRecovery is the other direction: a device that was stale or
// offline has reported in.
//
// It is separate from RecordTransitions because the trigger is different. That
// one is a clock passing a threshold, driven by the worker; this one is a
// device speaking, driven by the heartbeat. Folding them together would mean a
// recovery waiting up to a minute for the worker to notice.
func (s *Service) RecordHeartbeatRecovery(ctx context.Context, deviceID uuid.UUID) (*Transition, error) {
	rows, err := s.db.Query(ctx, `
		WITH changed AS (
			UPDATE devices d
			SET connectivity = 'ONLINE', last_seen_at = now(), updated_at = now()
			WHERE d.id = $1 AND d.connectivity <> 'ONLINE'
			RETURNING d.id, d.name, d.rustdesk_id, d.customer_id,
			          EXTRACT(EPOCH FROM (now() - COALESCE(d.last_seen_at, now())))::bigint AS since_seen
		),
		recorded AS (
			INSERT INTO device_connectivity_events
				(device_id, previous_connectivity, connectivity, previous_duration_seconds)
			SELECT c.id, NULL, 'ONLINE', c.since_seen FROM changed c
			RETURNING device_id, occurred_at
		)
		SELECT c.id, c.name, c.rustdesk_id, cu.name, 'ONLINE', c.since_seen, r.occurred_at
		FROM changed c
		JOIN recorded r ON r.device_id = c.id
		LEFT JOIN customers cu ON cu.id = c.customer_id
	`, deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	transitions, err := s.scanTransitions(rows, "")
	if err != nil || len(transitions) == 0 {
		// No row means the device was already ONLINE, which is the common case
		// and is not an event.
		return nil, err
	}
	return &transitions[0], nil
}

func (s *Service) scanTransitions(rows pgx.Rows, from string) ([]Transition, error) {
	var out []Transition
	for rows.Next() {
		var t Transition
		var duration *int64
		if err := rows.Scan(&t.DeviceID, &t.DeviceName, &t.RustdeskID, &t.CustomerName,
			&t.To, &duration, &t.OccurredAt); err != nil {
			return nil, err
		}
		t.From = from
		t.PreviousDurationSeconds = duration
		out = append(out, t)
	}
	return out, rows.Err()
}

// Enqueue writes one notification per enabled target subscribed to this event.
//
// A target with an empty event_types receives everything, which is what an
// operator setting up their first webhook wants. Delivery is the worker's job;
// this only puts rows in the outbox, so a slow or dead receiver cannot delay
// whatever produced the event.
func (s *Service) Enqueue(ctx context.Context, eventType string, payload map[string]any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	// $1 is cast explicitly in both places it appears. Without that, Postgres
	// deduces VARCHAR(100) from notification_deliveries.event_type and TEXT from
	// the comparison against event_types, and refuses the statement outright
	// with "inconsistent types deduced for parameter $1". One parameter used at
	// two types is not something to leave to inference.
	_, err = s.db.Exec(ctx, `
		INSERT INTO notification_deliveries (target_id, event_type, payload)
		SELECT t.id, $1::text, $2::jsonb
		FROM notification_targets t
		WHERE t.enabled
		  AND (cardinality(t.event_types) = 0 OR $1::text = ANY(t.event_types))
	`, eventType, body)
	return err
}

// RecordAndNotify is what callers use: record the transitions and enqueue a
// notification for each, so the two cannot be done separately and get out of
// step.
func (s *Service) RecordAndNotify(ctx context.Context, to string, afterSeconds int64, from ...string) (int, error) {
	transitions, err := s.RecordTransitions(ctx, to, afterSeconds, from...)
	if err != nil {
		return 0, err
	}
	for _, t := range transitions {
		if err := s.Enqueue(ctx, t.EventType(), t.Payload()); err != nil {
			// The transition is already recorded and is the more important half.
			// Failing here would lose it to a rollback that helps nobody.
			return len(transitions), err
		}
	}
	return len(transitions), nil
}
