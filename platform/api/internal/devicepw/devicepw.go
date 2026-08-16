// Package devicepw owns the connection password of every managed device.
//
// This is the piece that made revocation real. A RustDesk device decides for
// itself whether to accept a connection, and it decides on a password that the
// platform did not know and could not change. Removing a technician's access in
// the portal therefore changed nothing on the machine: whoever had been given
// the password still had it, and the device still honoured it.
//
// So the platform generates the password, keeps it encrypted, hands it to the
// device over the heartbeat channel, and releases it to a technician only after
// an access check and only with an audit record. Ending access is a rotation.
//
// Two honest limits, both of which the callers surface rather than hide:
//
//   - A rotation reaches a device on its next heartbeat. Until then the machine
//     still accepts the previous password, and an offline device may accept it
//     for as long as it stays offline. applied_version is what says whether the
//     rotation has landed, and the portal reports it.
//   - Anyone who can read both the database and DEVICE_PASSWORD_KEY can read
//     every password. Encryption at rest protects a database dump, a replica and
//     a backup; it is not a defence against the API host itself.
package devicepw

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrNoPassword means the device has no password on record yet. It is not an
// error state for a device that has never heartbeated: one is created at
// enrollment, and lazily on first contact for devices enrolled before this
// existed.
var ErrNoPassword = errors.New("device has no platform-managed password")

// KeyBytes is the AES-256 key length DEVICE_PASSWORD_KEY must decode to.
const KeyBytes = 32

// passwordLength is the generated password length.
//
// Sixteen characters from the 32-character alphabet below is 80 bits, which is
// far beyond what a device that rate-limits connection attempts needs, and it
// costs nothing because no human types it: the technician copies it out of the
// portal. It is well inside the client's own limit on what it will encrypt
// (hbb_common's ENCRYPT_MAX_LEN).
const passwordLength = 16

// passwordAlphabet omits the characters that get misread when somebody does
// have to read one aloud or off a screen: 0/O, 1/l/I, and the symbols that a
// shell or a URL would treat as syntax.
const passwordAlphabet = "abcdefghijkmnpqrstuvwxyz23456789"

// Cipher seals and opens a password. It is separated from Service so a caller
// inside another package's transaction can encrypt without a second pool.
type Cipher struct {
	aead cipher.AEAD
}

// ParseKey decodes DEVICE_PASSWORD_KEY. Standard or URL base64, padded or not,
// because an operator generating one with `openssl rand -base64 32` and an
// operator generating one with a password manager produce different spellings
// of the same 32 bytes.
func ParseKey(encoded string) ([]byte, error) {
	if encoded == "" {
		return nil, errors.New("DEVICE_PASSWORD_KEY is empty")
	}
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	} {
		if key, err := enc.DecodeString(encoded); err == nil {
			if len(key) != KeyBytes {
				return nil, fmt.Errorf("DEVICE_PASSWORD_KEY must decode to %d bytes, got %d", KeyBytes, len(key))
			}
			return key, nil
		}
	}
	return nil, errors.New("DEVICE_PASSWORD_KEY is not valid base64")
}

// NewCipher builds an AES-256-GCM cipher from a 32-byte key.
func NewCipher(key []byte) (*Cipher, error) {
	if len(key) != KeyBytes {
		return nil, fmt.Errorf("device password key must be %d bytes, got %d", KeyBytes, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Cipher{aead: aead}, nil
}

// Seal encrypts a password, returning the ciphertext and the nonce it used.
func (c *Cipher) Seal(plaintext string) (ciphertext, nonce []byte, err error) {
	nonce = make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, fmt.Errorf("failed to generate nonce: %w", err)
	}
	return c.aead.Seal(nil, nonce, []byte(plaintext), nil), nonce, nil
}

// Open decrypts a password. A failure here means the key changed or the row was
// tampered with, and both are worth distinguishing from "no password".
func (c *Cipher) Open(ciphertext, nonce []byte) (string, error) {
	if len(nonce) != c.aead.NonceSize() {
		return "", fmt.Errorf("stored nonce is %d bytes, expected %d", len(nonce), c.aead.NonceSize())
	}
	plaintext, err := c.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt device password (wrong DEVICE_PASSWORD_KEY?): %w", err)
	}
	return string(plaintext), nil
}

// Generate returns a fresh connection password.
func Generate() (string, error) {
	limit := big.NewInt(int64(len(passwordAlphabet)))
	out := make([]byte, passwordLength)
	for i := range out {
		// crypto/rand.Int rather than a modulo of a random byte: the alphabet
		// length does not divide 256, so a modulo would make some characters
		// measurably more likely than others.
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", fmt.Errorf("failed to generate device password: %w", err)
		}
		out[i] = passwordAlphabet[n.Int64()]
	}
	return string(out), nil
}

// Querier is the subset of the pool a pgx.Tx also satisfies, so a caller can
// rotate inside its own transaction. enrollment.Enroll does exactly that: a
// device that redeemed a token and then failed to get a password would be in
// the fleet with no way in.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Service reads and rotates device passwords.
type Service struct {
	db     Querier
	cipher *Cipher
}

// New creates the service. The key is required: a deployment without one has no
// device passwords at all, and the API refuses to start rather than running
// with the feature silently absent.
func New(db Querier, key []byte) (*Service, error) {
	c, err := NewCipher(key)
	if err != nil {
		return nil, err
	}
	return &Service{db: db, cipher: c}, nil
}

// Password is a device's current connection password.
type Password struct {
	// Value is the plaintext. It is only ever populated by Reveal and by the
	// rotation that created it, and it is never logged.
	Value string
	// Version is what the device echoes once it has applied this password.
	Version int64
	// AppliedVersion is the last version the device confirmed, and is nil for a
	// device that has never confirmed one.
	AppliedVersion *int64
	AppliedAt      *time.Time
	RotatedAt      time.Time
}

// Applied reports whether the device has confirmed the current password. False
// means the machine is still accepting the previous one.
func (p Password) Applied() bool {
	return p.AppliedVersion != nil && *p.AppliedVersion == p.Version
}

// Rotate replaces a device's password and returns the new plaintext.
//
// The first call for a device creates the row at version 1. actor is the user
// who caused it and is nil for a rotation the platform performed itself, such
// as the one at enrollment.
func (s *Service) Rotate(ctx context.Context, deviceID uuid.UUID, actor *int64) (*Password, error) {
	return RotateWith(ctx, s.db, s.cipher, deviceID, actor)
}

// RotateWith is Rotate against a caller-supplied querier, for use inside an
// existing transaction.
func RotateWith(ctx context.Context, q Querier, c *Cipher, deviceID uuid.UUID, actor *int64) (*Password, error) {
	plaintext, err := Generate()
	if err != nil {
		return nil, err
	}
	ciphertext, nonce, err := c.Seal(plaintext)
	if err != nil {
		return nil, err
	}

	var p Password
	err = q.QueryRow(ctx, `
		INSERT INTO device_passwords (device_id, ciphertext, nonce, version, rotated_by)
		VALUES ($1, $2, $3, 1, $4)
		ON CONFLICT (device_id) DO UPDATE
		SET ciphertext = EXCLUDED.ciphertext,
		    nonce = EXCLUDED.nonce,
		    version = device_passwords.version + 1,
		    rotated_at = now(),
		    rotated_by = EXCLUDED.rotated_by,
		    -- Deliberately not cleared: applied_version keeps pointing at the
		    -- password the device is actually using, which is what makes
		    -- "rotated but not yet landed" visible instead of looking like a
		    -- device that has never reported.
		    applied_version = device_passwords.applied_version,
		    applied_at = device_passwords.applied_at
		RETURNING version, applied_version, applied_at, rotated_at
	`, deviceID, ciphertext, nonce, actor).
		Scan(&p.Version, &p.AppliedVersion, &p.AppliedAt, &p.RotatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to store device password: %w", err)
	}

	p.Value = plaintext
	return &p, nil
}

// Reveal decrypts a device's current password. The caller is responsible for
// having checked access first and for writing the audit event: this function
// deliberately does neither, so that neither can be done here and forgotten
// there.
func (s *Service) Reveal(ctx context.Context, deviceID uuid.UUID) (*Password, error) {
	var p Password
	var ciphertext, nonce []byte

	err := s.db.QueryRow(ctx, `
		SELECT ciphertext, nonce, version, applied_version, applied_at, rotated_at
		FROM device_passwords WHERE device_id = $1
	`, deviceID).Scan(&ciphertext, &nonce, &p.Version, &p.AppliedVersion, &p.AppliedAt, &p.RotatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoPassword
	}
	if err != nil {
		return nil, err
	}

	p.Value, err = s.cipher.Open(ciphertext, nonce)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// Status is Reveal without the plaintext, for the places that need to say
// whether a rotation has landed without handing anyone a credential.
func (s *Service) Status(ctx context.Context, deviceID uuid.UUID) (*Password, error) {
	var p Password
	err := s.db.QueryRow(ctx, `
		SELECT version, applied_version, applied_at, rotated_at
		FROM device_passwords WHERE device_id = $1
	`, deviceID).Scan(&p.Version, &p.AppliedVersion, &p.AppliedAt, &p.RotatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoPassword
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// Pending is the heartbeat side. It returns the password the device should be
// using when that differs from the version the device says it has applied, and
// nil when the two agree.
//
// Agreement is the acknowledgement, so this also records it. There is no
// separate ack endpoint on purpose: an ack the device has to send as a second
// request is an ack that a dropped connection loses, and the next heartbeat
// carries the same information anyway.
//
// A device with no row gets one. Devices enrolled before this feature existed
// are the reason: they would otherwise never receive a password, and the fleet
// would be split into managed and unmanaged devices with nothing saying which.
func (s *Service) Pending(ctx context.Context, deviceID uuid.UUID, appliedVersion int64) (*Password, error) {
	p, err := s.Reveal(ctx, deviceID)
	if errors.Is(err, ErrNoPassword) {
		return s.Rotate(ctx, deviceID, nil)
	}
	if err != nil {
		return nil, err
	}

	if appliedVersion == p.Version {
		if !p.Applied() {
			if err := s.MarkApplied(ctx, deviceID, p.Version); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}

	return p, nil
}

// MarkApplied records that the device is now using this version.
func (s *Service) MarkApplied(ctx context.Context, deviceID uuid.UUID, version int64) error {
	_, err := s.db.Exec(ctx, `
		UPDATE device_passwords
		SET applied_version = $2, applied_at = now()
		WHERE device_id = $1 AND version = $2
	`, deviceID, version)
	return err
}

// RotateMany rotates a set of devices and returns how many were rotated.
//
// This is what turns "remove this technician from the support group" into an
// action on every machine that technician could reach. It stops at the first
// failure rather than continuing: a partial rotation that reports success would
// leave an operator believing access was withdrawn from machines where it was
// not.
func (s *Service) RotateMany(ctx context.Context, deviceIDs []uuid.UUID, actor *int64) (int, error) {
	rotated := 0
	for _, id := range deviceIDs {
		if _, err := s.Rotate(ctx, id, actor); err != nil {
			return rotated, err
		}
		rotated++
	}
	return rotated, nil
}
