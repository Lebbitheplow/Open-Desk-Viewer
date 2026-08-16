// Package deviceauth gives a device an identity it can prove.
//
// The identity a RustDesk client asserts, its rustdesk_id, is a self-chosen
// string. Anyone can send one. That is fine for a rendezvous server, whose job
// is to connect whoever shows up, and not fine for a management platform whose
// entire proposition is knowing which machines it manages.
//
// So a device redeems an enrollment token once, at provisioning, and gets a
// secret in exchange. Every heartbeat after that carries the secret. The
// rustdesk_id stays what it always was, a routing label; the secret is what
// makes a claim about a device believable.
//
// The secret is 32 bytes from crypto/rand, stored as a SHA-256 hash. A fast
// hash is correct here and would be wrong for a password: there is no
// low-entropy input to guess, and this runs on every heartbeat from every
// device in the fleet.
package deviceauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/OpenDeskViewer/platform/api/internal/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ErrUnauthorized is returned for an unknown device, a wrong secret and a
// revoked credential alike. The caller is a device, not an operator: telling it
// which of the three happened only helps someone probing the endpoint.
var ErrUnauthorized = errors.New("device not authorized")

// Device is the identity behind a verified secret.
type Device struct {
	ID         uuid.UUID
	RustdeskID string
	State      string
}

// Service issues and verifies device credentials.
type Service struct {
	db *postgres.Pool
}

// New creates a device credential service.
func New(db *postgres.Pool) *Service {
	return &Service{db: db}
}

// HashSecret is the one place the storage form is defined. Lowercase hex, so a
// credential can be looked up with an indexed equality comparison. Exported so
// enrollment can write the credential inside the same transaction that consumes
// the token, rather than in a second step that can fail on its own.
func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// GenerateSecret returns a URL-safe secret with 256 bits of entropy. Base64url
// without padding, so it survives a header, a query string and a config file
// unescaped.
func GenerateSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("failed to generate device secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// Issue mints a credential for a device and returns the plaintext secret,
// which is the only time it exists outside the client.
//
// Re-enrolling an already-enrolled device replaces its credential rather than
// failing: a device that was reimaged has lost its secret and has no way to
// prove it is the same machine except by redeeming another enrollment token.
// The replacement is what makes the old secret useless, which is the same
// operation as rotation.
func (s *Service) Issue(ctx context.Context, deviceID uuid.UUID, tokenID *uuid.UUID, clientIP string) (string, error) {
	secret, err := GenerateSecret()
	if err != nil {
		return "", err
	}

	var ip *string
	if clientIP != "" {
		ip = &clientIP
	}

	_, err = s.db.Exec(ctx, `
		INSERT INTO device_credentials (device_id, secret_hash, enrolled_by_token, enrolled_from)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (device_id) DO UPDATE
		SET secret_hash = EXCLUDED.secret_hash,
		    enrolled_by_token = EXCLUDED.enrolled_by_token,
		    enrolled_from = EXCLUDED.enrolled_from,
		    rotated_at = now(),
		    revoked_at = NULL
	`, deviceID, HashSecret(secret), tokenID, ip)
	if err != nil {
		return "", fmt.Errorf("failed to store device credential: %w", err)
	}

	return secret, nil
}

// Authenticate verifies a secret and returns the device it belongs to.
//
// The lookup is by secret alone, not by (rustdesk_id, secret): a device that
// holds a valid secret is that device, whatever id it claims. The caller checks
// the claimed id against the returned one, so a device presenting another
// device's id is caught with the mismatch named rather than as a bare 401.
func (s *Service) Authenticate(ctx context.Context, secret string) (*Device, error) {
	if secret == "" {
		return nil, ErrUnauthorized
	}

	var d Device
	err := s.db.QueryRow(ctx, `
		UPDATE device_credentials c
		SET last_used_at = now()
		FROM devices d
		WHERE c.device_id = d.id
		  AND c.secret_hash = $1
		  AND c.revoked_at IS NULL
		RETURNING d.id, d.rustdesk_id, d.state
	`, HashSecret(secret)).Scan(&d.ID, &d.RustdeskID, &d.State)

	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrUnauthorized
	}
	if err != nil {
		return nil, err
	}

	// A decommissioned or disabled device keeps its row so the history stays
	// readable, but it is not a member of the fleet any more.
	switch d.State {
	case "DISABLED", "DECOMMISSIONED":
		return nil, ErrUnauthorized
	}

	return &d, nil
}

// Revoke ends a device's credential. The device stays in the fleet and its
// history stays readable; it simply cannot speak to the API until it enrolls
// again.
func (s *Service) Revoke(ctx context.Context, deviceID uuid.UUID) error {
	_, err := s.db.Exec(ctx, `
		UPDATE device_credentials SET revoked_at = now()
		WHERE device_id = $1 AND revoked_at IS NULL
	`, deviceID)
	return err
}

// IsEnrolled reports whether a device holds a live credential. The portal shows
// this, because a device row with no credential is a device that cannot report
// and will never come online.
func (s *Service) IsEnrolled(ctx context.Context, deviceID uuid.UUID) (bool, error) {
	var enrolled bool
	err := s.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM device_credentials
			WHERE device_id = $1 AND revoked_at IS NULL
		)
	`, deviceID).Scan(&enrolled)
	return enrolled, err
}
