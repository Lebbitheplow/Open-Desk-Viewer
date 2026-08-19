package enrollment

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/OpenDeskViewer/platform/api/internal/config"
	"github.com/OpenDeskViewer/platform/api/internal/deviceauth"
	"github.com/OpenDeskViewer/platform/api/internal/fleet"
	"github.com/OpenDeskViewer/platform/api/internal/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Token represents an enrollment token.
//
// MaxUses and ExpiresAt are pointers because NULL is meaningful in both: no
// limit and no expiry. They used to be values, so the handler's "not specified"
// became Go's zero, which ConsumeToken then read as "zero uses allowed" and
// "expired in year 1". Every token created without both fields was unusable.
type Token struct {
	ID              uuid.UUID
	Token           string
	Prefix          string
	CustomerID      uuid.UUID
	LocationID      *uuid.UUID
	DeviceGroupID   *uuid.UUID
	DeviceGroupName string
	AddressBookName string
	MaxUses         *int
	Uses            int
	ExpiresAt       *time.Time
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

// NewService creates a new enrollment service.
//
// The fleet service is passed in rather than constructed here. It used to be
// built in three places, which was harmless only for as long as fleet.Service
// stayed a pool and a config with nothing of its own: the first field it caches
// would silently exist in three unsynchronised copies. Taking it as an argument
// makes "which fleet service is this" a question with one answer.
func NewService(db *postgres.Pool, cfg *config.Config, fleetService *fleet.Service) *Service {
	return &Service{
		db:    db,
		cfg:   cfg,
		fleet: fleetService,
	}
}

// GenerateToken generates a new enrollment token. A nil maxUses means
// unlimited redemptions and a nil expiresAt means no expiry; both are stored as
// NULL, which is what ConsumeToken checks for.
func (s *Service) GenerateToken(ctx context.Context, customerID uuid.UUID, locationID *uuid.UUID, deviceGroupID *uuid.UUID, deviceGroupName, addressBookName string, maxUses *int, expiresAt *time.Time, createdBy int64) (*Token, error) {
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return nil, fmt.Errorf("failed to generate token: %w", err)
	}
	tokenStr := base64.URLEncoding.EncodeToString(token)

	// Store SHA-256 hash and a short prefix for display. The column is
	// VARCHAR(64), so the hash goes in as lowercase hex rather than as the raw
	// 32 bytes, which pgx would otherwise render as \x-escaped bytea text.
	hash := hashToken(tokenStr)
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

// hashToken is the storage form of a token: lowercase hex SHA-256, matching
// client_sessions.token_hash and device_credentials.secret_hash.
func hashToken(tokenStr string) string {
	sum := sha256.Sum256([]byte(tokenStr))
	return hex.EncodeToString(sum[:])
}

// tokenColumns is the RETURNING/SELECT list every Token scan uses, so the
// column order cannot drift between the three places that read a token.
const tokenColumns = `id, prefix, customer_id, location_id, device_group_id, device_group_name,
	          address_book_name, max_uses, uses, expires_at, created_at, created_by, revoked_at`

func scanToken(row interface{ Scan(...any) error }) (*Token, error) {
	var t Token
	err := row.Scan(&t.ID, &t.Prefix, &t.CustomerID, &t.LocationID, &t.DeviceGroupID,
		&t.DeviceGroupName, &t.AddressBookName, &t.MaxUses, &t.Uses, &t.ExpiresAt,
		&t.CreatedAt, &t.CreatedBy, &t.RevokedAt)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// ErrInvalidToken covers unknown, expired, exhausted and revoked alike. The
// caller is an unauthenticated device; distinguishing them would tell someone
// probing the endpoint which guesses were close.
var ErrInvalidToken = errors.New("invalid enrollment token")

// ConsumeToken consumes an enrollment token atomically via UPDATE...RETURNING.
//
// A NULL expires_at means no expiry. The previous condition was
// `expires_at > now()`, which is NULL for a token with no expiry and therefore
// never true, so those tokens could not be redeemed at all.
func (s *Service) ConsumeToken(ctx context.Context, tokenStr string) (*Token, error) {
	return s.consume(ctx, s.db, tokenStr)
}

// querier is the subset of the pool that a transaction also satisfies, so
// consumption can happen inside the enrollment transaction.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func (s *Service) consume(ctx context.Context, q querier, tokenStr string) (*Token, error) {
	row := q.QueryRow(ctx, `
		UPDATE enrollment_tokens
		SET uses = uses + 1
		WHERE token_hash = $1
		  AND (max_uses IS NULL OR uses < max_uses)
		  AND (expires_at IS NULL OR expires_at > now())
		  AND revoked_at IS NULL
		RETURNING `+tokenColumns, hashToken(tokenStr))

	t, err := scanToken(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrInvalidToken
	}
	if err != nil {
		return nil, err
	}
	return t, nil
}

// ValidateToken reports whether a token could be redeemed right now. It does
// not consume it.
func (s *Service) ValidateToken(ctx context.Context, tokenStr string) (bool, error) {
	var exists bool
	err := s.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM enrollment_tokens
			WHERE token_hash = $1
			  AND (expires_at IS NULL OR expires_at > now())
			  AND (max_uses IS NULL OR uses < max_uses)
			  AND revoked_at IS NULL
		)
	`, hashToken(tokenStr)).Scan(&exists)

	return exists, err
}

// RevokeToken soft-deletes by setting revoked_at
func (s *Service) RevokeToken(ctx context.Context, tokenID uuid.UUID) error {
	_, err := s.db.Exec(ctx, `
		UPDATE enrollment_tokens SET revoked_at = now() WHERE id = $1
	`, tokenID)

	return err
}

// ListTokens lists enrollment tokens, optionally filtered to one customer, one
// page at a time.
//
// The caller used to pass uuid.Nil to mean "all", which matched the customer_id
// of no token, so the list was always empty. A nil filter now means all, and
// the paging happens in SQL rather than by fetching everything and reporting a
// page size.
func (s *Service) ListTokens(ctx context.Context, customerID *uuid.UUID, offset, limit int64) ([]Token, int64, error) {
	var total int64
	if err := s.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM enrollment_tokens
		WHERE $1::uuid IS NULL OR customer_id = $1
	`, customerID).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := s.db.Query(ctx, `
		SELECT `+tokenColumns+`
		FROM enrollment_tokens
		WHERE $1::uuid IS NULL OR customer_id = $1
		ORDER BY created_at DESC
		LIMIT $2 OFFSET $3
	`, customerID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var tokens []Token
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, 0, err
		}
		tokens = append(tokens, *t)
	}

	return tokens, total, rows.Err()
}

// EnrollRequest is what a device presents to redeem a token.
type EnrollRequest struct {
	Token      string
	RustdeskID string
	UUID       string
	Hostname   string
	OS         string
	Version    string
	ClientIP   string
	// Serial is what a technician searches the device by. Optional: a desktop
	// client has no source for one, and a device without it is still enrolled,
	// just harder to find.
	Serial string
}

// EnrollResult is what it gets back. Secret is the only time the plaintext
// exists outside the device.
type EnrollResult struct {
	DeviceID   uuid.UUID
	Name       string
	CustomerID uuid.UUID
	TokenID    uuid.UUID
	Secret     string
	Reenrolled bool
}

// ErrEnrollmentConflict is a device id that belongs to another customer. It is
// separate from ErrInvalidToken because it is not a guess: a valid token was
// presented for a device that is already somebody else's.
var ErrEnrollmentConflict = errors.New("device belongs to another customer")

// Enroll redeems a token and gives the device an identity it can prove.
//
// Everything happens in one transaction. Consuming the token and then failing
// to create the device would burn a use of a single-use token and leave the
// device unable to retry, which for a preconfigured Android client shipped to a
// customer site means a truck roll.
//
// Re-enrolling an id that already exists is allowed, and is how a reimaged
// device gets back in: it has lost its secret and has no other way to prove
// itself. Crossing customers is refused, so a leaked token cannot be used to
// take over another customer's device. Within one customer, a leaked token can
// still displace a device's credential, which is why tokens are short-lived and
// use-limited, and why redemption is audited.
func (s *Service) Enroll(ctx context.Context, req EnrollRequest) (*EnrollResult, error) {
	tx, err := s.db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	token, err := s.consume(ctx, tx, req.Token)
	if err != nil {
		return nil, err
	}

	// devices.uuid is NOT NULL and carries a unique index, so an enrollment
	// that omits it would collide with the next one that omits it. The
	// RustDesk id is unique by definition and is the identity the rest of the
	// system uses, so it is the fallback rather than an invented value.
	if req.UUID == "" {
		req.UUID = req.RustdeskID
	}

	// FOR UPDATE, so two devices racing on the same id cannot both create it.
	var deviceID uuid.UUID
	var existingCustomer *uuid.UUID
	var name string
	err = tx.QueryRow(ctx, `
		SELECT id, customer_id, name FROM devices WHERE rustdesk_id = $1 FOR UPDATE
	`, req.RustdeskID).Scan(&deviceID, &existingCustomer, &name)

	// A wiped, reimaged or reinstalled device comes back with a new RustDesk id
	// -- it is generated fresh with the config -- but the same serial. The
	// lookup above therefore misses, and the insert below used to collide with
	// idx_devices_serial_customer and fail the whole enrollment with a 500, so
	// the device could never rejoin: it heartbeat without a credential and was
	// recorded as an observation forever. Match the serial within the customer
	// as well, and treat that as the re-enrollment it is.
	if errors.Is(err, pgx.ErrNoRows) && req.Serial != "" {
		err = tx.QueryRow(ctx, `
			SELECT id, customer_id, name FROM devices
			WHERE serial_number = $1 AND customer_id = $2
			FOR UPDATE
		`, req.Serial, token.CustomerID).Scan(&deviceID, &existingCustomer, &name)
	}

	reenrolled := err == nil
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		name, err = s.deviceName(ctx, tx, token, req)
		if err != nil {
			return nil, err
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO devices (rustdesk_id, name, uuid, customer_id, location_id, state,
			                     os, hostname, client_version, serial_number, last_seen_at)
			VALUES ($1, $2, $3, $4, $5, 'ACTIVE', $6, $7, $8, $9, now())
			RETURNING id
		`, req.RustdeskID, name, req.UUID, token.CustomerID, token.LocationID,
			nullable(req.OS), nullable(req.Hostname), nullable(req.Version),
			nullable(req.Serial)).Scan(&deviceID); err != nil {
			return nil, fmt.Errorf("failed to create device: %w", err)
		}
	case err != nil:
		return nil, err
	default:
		if existingCustomer != nil && *existingCustomer != token.CustomerID {
			return nil, ErrEnrollmentConflict
		}
		// name is deliberately absent from this UPDATE. A device that has been
		// renamed through the API carries the customer's own identifier for it,
		// and re-enrollment must not overwrite that with a generated name. See
		// TestReenrollmentKeepsAnAPIAssignedName.
		if _, err := tx.Exec(ctx, `
			UPDATE devices
			SET uuid = $2,
			    -- Carried across because a reinstalled device generates a new
			    -- one, and the row must follow it or the portal would offer a
			    -- Connect that resolves to nothing.
			    rustdesk_id = $9,
			    customer_id = $3,
			    location_id = COALESCE($4, location_id),
			    state = CASE WHEN state IN ('DISABLED', 'DECOMMISSIONED') THEN state ELSE 'ACTIVE' END,
			    os = COALESCE($5, os),
			    hostname = COALESCE($6, hostname),
			    client_version = COALESCE($7, client_version),
			    serial_number = COALESCE($8, serial_number),
			    updated_at = now()
			WHERE id = $1
		`, deviceID, req.UUID, token.CustomerID, token.LocationID,
			nullable(req.OS), nullable(req.Hostname), nullable(req.Version),
			nullable(req.Serial), req.RustdeskID); err != nil {
			return nil, fmt.Errorf("failed to update device: %w", err)
		}
	}

	// Group membership is what makes the device reachable by a support group,
	// so a token that names no group produces a device nobody can see. That is
	// deliberate: it is safer than guessing a group.
	if token.DeviceGroupID != nil {
		if _, err := tx.Exec(ctx, `
			INSERT INTO device_group_members (device_id, device_group_id)
			VALUES ($1, $2)
			ON CONFLICT DO NOTHING
		`, deviceID, *token.DeviceGroupID); err != nil {
			return nil, fmt.Errorf("failed to add device to group: %w", err)
		}
	}

	secret, err := deviceauth.GenerateSecret()
	if err != nil {
		return nil, err
	}
	var ip *string
	if req.ClientIP != "" {
		ip = &req.ClientIP
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO device_credentials (device_id, secret_hash, enrolled_by_token, enrolled_from)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (device_id) DO UPDATE
		SET secret_hash = EXCLUDED.secret_hash,
		    enrolled_by_token = EXCLUDED.enrolled_by_token,
		    enrolled_from = EXCLUDED.enrolled_from,
		    rotated_at = now(),
		    revoked_at = NULL
	`, deviceID, deviceauth.HashSecret(secret), token.ID, ip); err != nil {
		return nil, fmt.Errorf("failed to store device credential: %w", err)
	}

	// The id is a device now, so its unclaimed sightings are history.
	if _, err := tx.Exec(ctx,
		`DELETE FROM device_observations WHERE rustdesk_id = $1`, req.RustdeskID); err != nil {
		return nil, fmt.Errorf("failed to clear observations: %w", err)
	}

	if err := seedDefaultStrategy(ctx, tx, deviceID); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	return &EnrollResult{
		DeviceID:   deviceID,
		Name:       name,
		CustomerID: token.CustomerID,
		TokenID:    token.ID,
		Secret:     secret,
		Reenrolled: reenrolled,
	}, nil
}

// defaultStrategy is the policy a newly enrolled device starts under.
//
// Both settings exist because of the platform-managed connection password
// (internal/devicepw). Without them the password the platform holds is not the
// credential the device actually checks:
//
//   - verification-method defaults to accepting a temporary password as well,
//     which is one the device generates for itself and nobody can revoke
//     centrally.
//   - approve-mode defaults to accepting a click at the device instead of a
//     password, which for an unattended machine at a customer site means either
//     nobody can connect or anybody standing at it can let somebody in.
//
// It is seeded rather than hardcoded, and with ON CONFLICT DO NOTHING, so an
// administrator who has changed either through PUT /strategy keeps their change
// when the device re-enrolls.
var defaultStrategy = map[string]string{
	"verification-method": "use-permanent-password",
	"approve-mode":        "password",
	// Remote input is the point of the product, so it is on from the first
	// heartbeat rather than waiting for somebody to notice a session that
	// shows the screen and ignores every tap. This is the protocol-level
	// permission; the accessibility grant on the device is separate and is
	// what actually delivers the events.
	"enable-keyboard": "Y",
}

func seedDefaultStrategy(ctx context.Context, q querier, deviceID uuid.UUID) error {
	if _, err := q.Exec(ctx, `
		INSERT INTO device_strategies (device_id, config_options)
		VALUES ($1, $2)
		ON CONFLICT (device_id) DO NOTHING
	`, deviceID, defaultStrategy); err != nil {
		return fmt.Errorf("failed to seed the default device strategy: %w", err)
	}
	return nil
}

// deviceName picks something an operator can recognise in a device list. The
// hostname is what a technician would call the machine; the RustDesk id is the
// fallback because devices.name is NOT NULL and an empty name is a row nobody
// can identify.
// deviceName decides what a device is called when its row is created.
//
// The serial is what a technician is given to find a machine with, so when the
// client reports one the name comes from DEVICE_NAME_TEMPLATE, which places it
// alongside the customer and location. Without a serial there is nothing better
// than the hostname, and without that nothing better than the id.
//
// Only on creation. A name set afterwards through the API is the customer's own
// identifier for the device and is never regenerated.
func (s *Service) deviceName(ctx context.Context, tx pgx.Tx, token *Token, req EnrollRequest) (string, error) {
	if req.Serial == "" {
		if req.Hostname != "" {
			return req.Hostname, nil
		}
		return req.RustdeskID, nil
	}

	// The template names the customer and location, and the token carries their
	// ids rather than their names. A missing location is ordinary: a token need
	// not name one.
	var customer string
	var location *string
	if err := tx.QueryRow(ctx, `
		SELECT c.name, l.name
		FROM customers c
		LEFT JOIN locations l ON l.id = $2
		WHERE c.id = $1
	`, token.CustomerID, token.LocationID).Scan(&customer, &location); err != nil {
		return "", fmt.Errorf("failed to read the customer for naming: %w", err)
	}

	locationName := ""
	if location != nil {
		locationName = *location
	}
	name := strings.TrimSpace(s.fleet.GenerateDeviceName(customer, locationName, &req.Serial))
	if name == "" {
		// A deployment that blanked DEVICE_NAME_TEMPLATE would otherwise insert
		// an empty name into a NOT NULL column and produce a device nobody can
		// see in a list sorted by name.
		return req.Serial, nil
	}
	return name, nil
}

// nullable keeps an empty string from overwriting a column that already holds
// something better.
func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
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

	// Get default device group.
	//
	// This used to select the first device group in the deployment, ignoring
	// the customer entirely, so enrolling a device for customer B suggested
	// customer A's group. device_groups has no customer_id column, so the link
	// is through the devices already in the group.
	rows2, err := s.db.Query(ctx, `
		SELECT g.id, g.name, g.parent_id, g.created_at, g.updated_at
		FROM device_groups g
		WHERE EXISTS (
			SELECT 1
			FROM device_group_members m
			JOIN devices d ON d.id = m.device_id
			WHERE m.device_group_id = g.id AND d.customer_id = $1
		)
		ORDER BY g.name
		LIMIT 1
	`, customerID)
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
