package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/OpenDeskViewer/platform/api/internal/postgres"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

// Role names seeded by the initial migration.
const (
	RoleAdministrator  = "Administrator"
	RoleSupportManager = "Support Manager"
	RoleTechnician     = "Technician"
)

// User represents a system user
type User struct {
	ID              int64
	KeycloakSubject string
	Email           string
	DisplayName     string
	Active          bool
	CreatedAt       time.Time
	UpdatedAt       time.Time
	LastLogin       *time.Time
	Roles           []Role
	SupportGroups   []uuid.UUID
}

// Role represents a system role
type Role struct {
	ID          int64
	Name        string
	Description string
}

// ClientSession represents a RustDesk client session
type ClientSession struct {
	ID            uuid.UUID
	UserID        int64
	RustdeskToken string
	RustdeskID    string
	ExpiresAt     time.Time
	CreatedAt     time.Time
}

// AuthService handles authentication and authorization
type AuthService struct {
	db *postgres.Pool

	// bootstrapAdminEmail is granted Administrator when that account is
	// provisioned on its first sign-in. Empty disables the bootstrap.
	bootstrapAdminEmail string
}

// NewAuthService creates a new auth service
func NewAuthService(db *postgres.Pool, bootstrapAdminEmail string) *AuthService {
	return &AuthService{db: db, bootstrapAdminEmail: bootstrapAdminEmail}
}

// userSelect is the one place a user is read from. Four functions used to carry
// their own copy of this column list, their own scan and their own two follow-up
// queries; the copies had already drifted, in that one of them loaded no support
// groups. A column added to users now reaches every lookup or none.
//
// The support groups come back as an array from the same round trip rather than
// a second query. That is not a micro-optimisation: it is what makes attaching
// them unconditional, so no caller has to know which lookup returns a fully
// populated user and which returns a half-populated one.
const userSelect = `
	SELECT u.id, u.keycloak_subject, u.email, u.display_name, u.active,
	       u.created_at, u.updated_at, u.last_login,
	       COALESCE(
	           (SELECT array_agg(usg.support_group_id)
	            FROM user_support_groups usg
	            WHERE usg.user_id = u.id),
	           '{}'::uuid[])
	FROM users u`

// loadUser runs userSelect with the given trailing clause, which may add joins
// as well as a WHERE, and returns the user with roles and support groups
// attached. A caller that finds no row gets pgx.ErrNoRows unwrapped, because
// "no such user" is a normal answer that callers distinguish.
func (s *AuthService) loadUser(ctx context.Context, clause string, args ...any) (*User, error) {
	var u User
	err := s.db.QueryRow(ctx, userSelect+"\n"+clause, args...).Scan(
		&u.ID, &u.KeycloakSubject, &u.Email, &u.DisplayName,
		&u.Active, &u.CreatedAt, &u.UpdatedAt, &u.LastLogin, &u.SupportGroups,
	)
	if err != nil {
		return nil, err
	}

	roles, err := s.getUserRoles(ctx, u.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to load user roles: %w", err)
	}
	u.Roles = roles

	return &u, nil
}

// GetUserByEmail retrieves a user by email
func (s *AuthService) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	return s.loadUser(ctx, `WHERE u.email = $1`, email)
}

// GetUserByID retrieves a user by ID
func (s *AuthService) GetUserByID(ctx context.Context, id int64) (*User, error) {
	return s.loadUser(ctx, `WHERE u.id = $1`, id)
}

// GetUserByKeycloakSubject retrieves a user by Keycloak subject
func (s *AuthService) GetUserByKeycloakSubject(ctx context.Context, subject string) (*User, error) {
	return s.loadUser(ctx, `WHERE u.keycloak_subject = $1`, subject)
}

// CreateClientSession creates a client session for a token the caller already
// holds. The token is hashed on the way in, exactly as CreateSession does.
func (s *AuthService) CreateClientSession(ctx context.Context, userID int64, rustdeskToken, rustdeskID string, expiresAt time.Time) (*ClientSession, error) {
	var id uuid.UUID
	err := s.db.QueryRow(ctx, `
		INSERT INTO client_sessions (user_id, token_hash, rustdesk_id, expires_at)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, userID, hashSessionToken(rustdeskToken), rustdeskID, expiresAt).Scan(&id)

	if err != nil {
		return nil, fmt.Errorf("failed to create client session: %w", err)
	}

	return &ClientSession{
		ID:            id,
		UserID:        userID,
		RustdeskToken: rustdeskToken,
		RustdeskID:    rustdeskID,
		ExpiresAt:     expiresAt,
		CreatedAt:     time.Now(),
	}, nil
}

// GetClientSession retrieves a client session by RustDesk token.
//
// RustdeskToken on the result is the value the caller passed in, echoed back:
// the stored form is a hash and the plaintext cannot be recovered from it.
func (s *AuthService) GetClientSession(ctx context.Context, rustdeskToken string) (*ClientSession, error) {
	row := s.db.QueryRow(ctx, `
		SELECT cs.id, cs.user_id, cs.rustdesk_id, cs.expires_at, cs.created_at
		FROM client_sessions cs
		WHERE cs.token_hash = $1
	`, hashSessionToken(rustdeskToken))

	var cs ClientSession
	err := row.Scan(&cs.ID, &cs.UserID, &cs.RustdeskID, &cs.ExpiresAt, &cs.CreatedAt)
	if err != nil {
		return nil, err
	}
	cs.RustdeskToken = rustdeskToken

	return &cs, nil
}

// GetSessionUser retrieves user from a live client session. Expiry is part of
// the predicate, so a stale token reads as no session at all.
func (s *AuthService) GetSessionUser(ctx context.Context, rustdeskToken string) (*User, error) {
	return s.loadUser(ctx, `
		JOIN client_sessions cs ON u.id = cs.user_id
		WHERE cs.token_hash = $1 AND cs.expires_at > now()`,
		hashSessionToken(rustdeskToken))
}

// InvalidateClientSession deletes a client session
func (s *AuthService) InvalidateClientSession(ctx context.Context, rustdeskToken string) error {
	_, err := s.db.Exec(ctx, `
		DELETE FROM client_sessions WHERE token_hash = $1
	`, hashSessionToken(rustdeskToken))
	return err
}

// getUserRoles fetches roles for a user
func (s *AuthService) getUserRoles(ctx context.Context, userID int64) ([]Role, error) {
	rows, err := s.db.Query(ctx, `
		SELECT r.id, r.name, r.description
		FROM roles r
		JOIN user_roles ur ON r.id = ur.role_id
		WHERE ur.user_id = $1
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var roles []Role
	for rows.Next() {
		var r Role
		if err := rows.Scan(&r.ID, &r.Name, &r.Description); err != nil {
			return nil, err
		}
		roles = append(roles, r)
	}

	// rows.Err() was not checked here. A connection that dies mid-read ends the
	// loop with no error, so a user would have been reported as holding fewer
	// roles than they do, which for a role check fails in the permissive
	// direction on the way back out.
	return roles, rows.Err()
}

// JWTClaims represents JWT token claims
type JWTClaims struct {
	Subject           string
	Audience          string
	Expiry            int64
	Issuer            string
	Email             string
	PreferredUsername string
	Name              string
}

// Password sign-in used to live here: Authenticate, SetPassword, an Argon2id
// hash, a per-account lockout and a per-IP throttle, over a user_credentials
// table. It was removed with migration 000012, and the reasoning is worth
// keeping because the code was correct.
//
// SetPassword had no HTTP caller and never had one, so no credential could be
// created except by hand-writing a row: /api/login's password branch could not
// authenticate anybody, and offering a door that cannot open is what the audit
// singled out. The two ways to fix that were to build the missing
// change-password surface or to stop offering the door, and this deployment's
// identity lives in Keycloak -- which owns password policy, lockout, MFA and
// disablement. A second credential store here would have been governed by none
// of them, and would have kept working after an account was disabled in
// Keycloak. The RustDesk client signs in through /api/oidc/auth instead, which
// is implemented, tested and now actually offered: HandleLoginOptions used to
// advertise "common", a value the client ignores, so the sign-in dialog showed
// no provider button at all.
//
// This is the same call B.5 made about api_clients, for the same reason: a
// half-built second authentication path is a worse thing to leave lying around
// than most.

// CreateSession issues a session token and stores only its hash.
//
// The token is 32 bytes from crypto/rand. The previous implementation built it
// from three time.Now().UnixNano() readings and the user ID, which is guessable
// by anyone who can observe or trigger a login.
func (s *AuthService) CreateSession(ctx context.Context, user *User) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("failed to generate session token: %w", err)
	}
	sessionToken := base64.RawURLEncoding.EncodeToString(raw)

	_, err := s.db.Exec(ctx, `
		INSERT INTO client_sessions (user_id, token_hash, expires_at)
		VALUES ($1, $2, $3)
	`, user.ID, hashSessionToken(sessionToken), time.Now().Add(24*time.Hour))
	if err != nil {
		return "", fmt.Errorf("failed to create session: %w", err)
	}

	// The session is already committed and valid, so a failure to stamp
	// last_login is logged rather than returned. Returning an error here used
	// to hand the user a 500 while leaving them a working session.
	if _, err := s.db.Exec(ctx, `
		UPDATE users SET last_login = now() WHERE id = $1
	`, user.ID); err != nil {
		log.Error().Err(err).Int64("user_id", user.ID).Msg("failed to update last_login")
	}

	// Returned exactly once. Only the hash is retrievable afterwards.
	return sessionToken, nil
}

// RevokeSession invalidates a session token
func (s *AuthService) RevokeSession(ctx context.Context, sessionToken string) error {
	_, err := s.db.Exec(ctx, `
		DELETE FROM client_sessions WHERE token_hash = $1
	`, hashSessionToken(sessionToken))

	return err
}

// hashSessionToken maps a session token to the value stored in the database.
// SHA-256 is right here where it would be wrong for a password: the input is
// 32 bytes of CSPRNG output, so there is nothing to brute force.
func hashSessionToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
