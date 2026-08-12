package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/OpenDeskViewer/platform/api/internal/postgres"
	"github.com/google/uuid"
)

// Event represents an audit event
type Event struct {
	Type        string                 `json:"type"`
	ActorID     int64                  `json:"actor_id"`
	Resource    string                 `json:"resource"`
	ResourceID  string                 `json:"resource_id"`
	Description string                 `json:"description"`
	Metadata    map[string]interface{} `json:"metadata"`
	CreatedAt   time.Time              `json:"created_at"`
}

// Recorder interface for recording audit events
type Recorder interface {
	Record(ctx context.Context, e Event) error
}

// Service handles audit event recording
type Service struct {
	db *postgres.Pool
}

// New creates a new audit service
func New(db *postgres.Pool) *Service {
	return &Service{db: db}
}

// Record records an audit event
// This never fails the surrounding operation - it logs errors and returns nil
func (s *Service) Record(ctx context.Context, e Event) error {
	if e.Metadata == nil {
		e.Metadata = make(map[string]interface{})
	}

	metadataJSON, err := json.Marshal(e.Metadata)
	if err != nil {
		fmt.Printf("audit: failed to marshal metadata: %v\n", err)
		return nil
	}

	_, err = s.db.Exec(ctx, `
		INSERT INTO audit_events (event_type, user_id, description, metadata)
		VALUES ($1, $2, $3, $4)
	`, e.Type, e.ActorID, e.Description, metadataJSON)

	if err != nil {
		fmt.Printf("audit: failed to record event: %v\n", err)
		return nil
	}

	return nil
}

// RecordDeviceReassignment records a device reassignment event
func (s *Service) RecordDeviceReassignment(ctx context.Context, actorID int64, deviceID uuid.UUID, oldCustomerID, newCustomerID *uuid.UUID) error {
	return s.Record(ctx, Event{
		Type:        "device.reassigned",
		ActorID:     actorID,
		Resource:    "device",
		ResourceID:  deviceID.String(),
		Description: fmt.Sprintf("Device reassigned from customer %v to customer %v", oldCustomerID, newCustomerID),
		Metadata: map[string]interface{}{
			"old_customer_id": func() *string { if oldCustomerID != nil { s := oldCustomerID.String(); return &s }; return nil }(),
			"new_customer_id": func() *string { if newCustomerID != nil { s := newCustomerID.String(); return &s }; return nil }(),
		},
	})
}

// RecordUserRoleChange records a user role change event
func (s *Service) RecordUserRoleChange(ctx context.Context, actorID int64, userID int64, changedRole, newRole string) error {
	return s.Record(ctx, Event{
		Type:        "user.role_changed",
		ActorID:     actorID,
		Resource:    "user",
		ResourceID:  fmt.Sprintf("%d", userID),
		Description: fmt.Sprintf("User role changed from %s to %s", changedRole, newRole),
		Metadata: map[string]interface{}{
			"changed_role": changedRole,
			"new_role":     newRole,
			"user_id":      userID,
		},
	})
}
