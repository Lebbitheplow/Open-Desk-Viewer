package enrollment

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/OpenDeskViewer/platform/api/internal/config"
	"github.com/OpenDeskViewer/platform/api/internal/fleet"
	"github.com/OpenDeskViewer/platform/api/internal/postgres"
	"github.com/google/uuid"
)

// Token represents an enrollment token
type Token struct {
	ID              uuid.UUID
	Token           string
	Prefix          string
	CustomerID      uuid.UUID
	LocationID      *uuid.UUID
	DeviceGroupID   *uuid.UUID
	DeviceGroupName string
	AddressBookName string
	MaxUses         int
	Uses            int
	ExpiresAt       time.Time
	CreatedAt       time.Time
	CreatedBy       int64
	RevokedAt       *time.Time
}

// Service handles enrollment operations
type Service struct {
	db    *postgres.Pool
	cfg   *config.Config
	fleet *fleet.Service
}

// NewService creates a new enrollment service
func NewService(db *postgres.Pool, cfg *config.Config) *Service {
	return &Service{
		db:    db,
		cfg:   cfg,
		fleet: fleet.NewService(db, cfg),
	}
}

// GenerateToken generates a new enrollment token
func (s *Service) GenerateToken(ctx context.Context, customerID uuid.UUID, locationID *uuid.UUID, deviceGroupID *uuid.UUID, deviceGroupName, addressBookName string, maxUses int, expiresAt time.Time, createdBy int64) (*Token, error) {
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return nil, fmt.Errorf("failed to generate token: %w", err)
	}
	tokenStr := base64.URLEncoding.EncodeToString(token)

	// Store SHA-256 hash and a short prefix for display
	hash := sha256.Sum256([]byte(tokenStr))
	prefix := tokenStr[:8]

	var id uuid.UUID
	err := s.db.QueryRow(ctx, `
		INSERT INTO enrollment_tokens (token_hash, prefix, customer_id, location_id, device_group_id, 
		                               device_group_name, address_book_name, max_uses, uses, expires_at, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 0, $9, $10)
		RETURNING id
	`, hash, prefix, customerID, locationID, deviceGroupID, deviceGroupName, addressBookName, maxUses, expiresAt, createdBy).Scan(&id)

	if err != nil {
		return nil, fmt.Errorf("failed to create token: %w", err)
	}

	// Return plaintext exactly once at creation
	return &Token{
		ID:              id,
		Token:           tokenStr,
		Prefix:          prefix,
		CustomerID:      customerID,
		LocationID:      locationID,
		DeviceGroupID:   deviceGroupID,
		DeviceGroupName: deviceGroupName,
		AddressBookName: addressBookName,
		MaxUses:         maxUses,
		Uses:            0,
		ExpiresAt:       expiresAt,
		CreatedAt:       time.Now(),
		CreatedBy:       createdBy,
	}, nil
}

// ConsumeToken consumes an enrollment token atomically via UPDATE...RETURNING
func (s *Service) ConsumeToken(ctx context.Context, tokenStr string) (*Token, error) {
	hash := sha256.Sum256([]byte(tokenStr))

	var t Token
	err := s.db.QueryRow(ctx, `
		UPDATE enrollment_tokens
		SET uses = uses + 1
		WHERE token_hash = $1
		  AND (max_uses IS NULL OR uses < max_uses)
		  AND expires_at > now()
		  AND revoked_at IS NULL
		RETURNING id, prefix, customer_id, location_id, device_group_id, device_group_name,
		          address_book_name, max_uses, uses, expires_at, created_at, created_by, revoked_at
	`, hash).Scan(&t.ID, &t.Prefix, &t.CustomerID, &t.LocationID, &t.DeviceGroupID,
		&t.DeviceGroupName, &t.AddressBookName, &t.MaxUses, &t.Uses, &t.ExpiresAt,
		&t.CreatedAt, &t.CreatedBy, &t.RevokedAt)

	if err != nil {
		return nil, fmt.Errorf("invalid token")
	}

	return &t, nil
}

// ValidateToken validates an enrollment token
func (s *Service) ValidateToken(ctx context.Context, tokenStr string) (bool, error) {
	hash := sha256.Sum256([]byte(tokenStr))
	var count int
	err := s.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM enrollment_tokens
		WHERE token_hash = $1 AND expires_at > now() AND (max_uses IS NULL OR uses < max_uses) AND revoked_at IS NULL
	`, hash).Scan(&count)

	return count > 0, err
}

// RevokeToken soft-deletes by setting revoked_at
func (s *Service) RevokeToken(ctx context.Context, tokenID uuid.UUID) error {
	_, err := s.db.Exec(ctx, `
		UPDATE enrollment_tokens SET revoked_at = now() WHERE id = $1
	`, tokenID)

	return err
}

// ListTokens lists enrollment tokens for a customer
func (s *Service) ListTokens(ctx context.Context, customerID uuid.UUID) ([]Token, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, prefix, customer_id, location_id, device_group_id, device_group_name,
		       address_book_name, max_uses, uses, expires_at, created_at, created_by, revoked_at
		FROM enrollment_tokens
		WHERE customer_id = $1
		ORDER BY created_at DESC
	`, customerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tokens []Token
	for rows.Next() {
		var t Token
		err := rows.Scan(&t.ID, &t.Prefix, &t.CustomerID, &t.LocationID, &t.DeviceGroupID,
			&t.DeviceGroupName, &t.AddressBookName, &t.MaxUses, &t.Uses, &t.ExpiresAt,
			&t.CreatedAt, &t.CreatedBy, &t.RevokedAt)
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, t)
	}

	return tokens, nil
}

// GetDeviceEnrollmentInfo returns default location and device group for a customer
func (s *Service) GetDeviceEnrollmentInfo(ctx context.Context, customerID uuid.UUID) (*fleet.Location, *fleet.DeviceGroup, error) {
	// Return the first location and first device group for this customer
	var location *fleet.Location
	var deviceGroup *fleet.DeviceGroup

	// Get default location
	rows, err := s.db.Query(ctx, `
		SELECT id, customer_id, name, address, created_at, updated_at
		FROM locations
		WHERE customer_id = $1
		ORDER BY name
		LIMIT 1
	`, customerID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to query locations: %w", err)
	}
	defer rows.Close()

	var loc fleet.Location
	if rows.Next() {
		if err := rows.Scan(&loc.ID, &loc.CustomerID, &loc.Name, &loc.Address, &loc.CreatedAt, &loc.UpdatedAt); err != nil {
			return nil, nil, fmt.Errorf("failed to scan location: %w", err)
		}
		location = &loc
	}

	// Get default device group
	rows2, err := s.db.Query(ctx, `
		SELECT id, name, parent_id, created_at, updated_at
		FROM device_groups
		ORDER BY name
		LIMIT 1
	`)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to query device groups: %w", err)
	}
	defer rows2.Close()

	var dg fleet.DeviceGroup
	if rows2.Next() {
		var parentID *string
		if err := rows2.Scan(&dg.ID, &dg.Name, &parentID, &dg.CreatedAt, &dg.UpdatedAt); err != nil {
			return nil, nil, fmt.Errorf("failed to scan device group: %w", err)
		}
		if parentID != nil {
			p, err := uuid.Parse(*parentID)
			if err == nil {
				dg.ParentID = &p
			}
		}
		deviceGroup = &dg
	}

	return location, deviceGroup, nil
}
