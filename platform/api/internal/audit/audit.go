// Package audit records who changed what.
//
// The rule this package is built around: recording must never fail the request
// it describes. An audit write that fails a device reassignment turns a
// bookkeeping problem into an outage, so Record logs and returns nil instead of
// propagating. Dropped events are counted, because silence about a broken audit
// trail is worse than the gap itself.
package audit

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"time"

	"github.com/OpenDeskViewer/platform/api/internal/postgres"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

// Event is one recorded change.
type Event struct {
	Type        string         `json:"type"`
	ActorID     int64          `json:"actor_id"`
	Resource    string         `json:"resource"`
	ResourceID  string         `json:"resource_id"`
	Description string         `json:"description"`
	Metadata    map[string]any `json:"metadata"`
	CreatedAt   time.Time      `json:"created_at"`

	// The audit_events table has real foreign keys for these, so the audit log
	// can be filtered by device or customer without parsing metadata. Set them
	// where they apply; nil is fine where they do not.
	DeviceID       *uuid.UUID `json:"device_id,omitempty"`
	CustomerID     *uuid.UUID `json:"customer_id,omitempty"`
	SupportGroupID *uuid.UUID `json:"support_group_id,omitempty"`
}

// Recorder records audit events.
type Recorder interface {
	Record(ctx context.Context, e Event) error
}

// Service writes audit events to PostgreSQL.
type Service struct {
	db      *postgres.Pool
	dropped atomic.Int64
}

// New creates an audit service.
func New(db *postgres.Pool) *Service {
	return &Service{db: db}
}

// Dropped reports how many events failed to record since start. A non-zero
// value means the audit trail has holes in it.
func (s *Service) Dropped() int64 { return s.dropped.Load() }

// Record writes an event. It never returns an error: see the package comment.
func (s *Service) Record(ctx context.Context, e Event) error {
	if e.Metadata == nil {
		e.Metadata = make(map[string]any)
	}
	if e.Resource != "" {
		e.Metadata["resource"] = e.Resource
	}
	if e.ResourceID != "" {
		e.Metadata["resource_id"] = e.ResourceID
	}

	metadataJSON, err := json.Marshal(e.Metadata)
	if err != nil {
		s.drop(e, err, "failed to marshal metadata")
		return nil
	}

	// A zero actor is a device-driven or system event; the column is nullable
	// and a foreign key, so writing 0 would fail on every such event.
	var actorID *int64
	if e.ActorID != 0 {
		actorID = &e.ActorID
	}

	_, err = s.db.Exec(ctx, `
		INSERT INTO audit_events
			(event_type, user_id, device_id, customer_id, support_group_id, description, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		e.Type, actorID, e.DeviceID, e.CustomerID, e.SupportGroupID, e.Description, metadataJSON)
	if err != nil {
		s.drop(e, err, "failed to insert audit event")
		return nil
	}

	return nil
}

func (s *Service) drop(e Event, err error, msg string) {
	s.dropped.Add(1)
	log.Error().Err(err).
		Str("event_type", e.Type).
		Str("resource", e.Resource).
		Str("resource_id", e.ResourceID).
		Int64("dropped_total", s.dropped.Load()).
		Msg("audit: " + msg)
}
