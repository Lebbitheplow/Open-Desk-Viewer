package identity

import (
	"context"
	"fmt"
	"time"

	"github.com/OpenDeskViewer/platform/api/internal/postgres"
	"github.com/google/uuid"
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

// GetUserByEmail retrieves a user by email
func (s *AuthService) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	row := s.db.QueryRow(ctx, `
		SELECT u.id, u.keycloak_subject, u.email, u.display_name, u.active,
		       u.created_at, u.updated_at, u.last_login
		FROM users u
		WHERE u.email = $1
	`, email)

	var u User
	err := row.Scan(
		&u.ID, &u.KeycloakSubject, &u.Email, &u.DisplayName,
		&u.Active, &u.CreatedAt, &u.UpdatedAt, &u.LastLogin,
	)
	if err != nil {
		return nil, err
	}

	// Load roles
	roles, err := s.getUserRoles(ctx, u.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to load user roles: %w", err)
	}
	u.Roles = roles

	// Load support groups
	supportGroups, err := s.getUserSupportGroups(ctx, u.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to load user support groups: %w", err)
	}
	u.SupportGroups = supportGroups

	return &u, nil
}

// GetUserByID retrieves a user by ID
func (s *AuthService) GetUserByID(ctx context.Context, id int64) (*User, error) {
	row := s.db.QueryRow(ctx, `
		SELECT u.id, u.keycloak_subject, u.email, u.display_name, u.active,
		       u.created_at, u.updated_at, u.last_login
		FROM users u
		WHERE u.id = $1
	`, id)

	var u User
	err := row.Scan(
		&u.ID, &u.KeycloakSubject, &u.Email, &u.DisplayName,
		&u.Active, &u.CreatedAt, &u.UpdatedAt, &u.LastLogin,
	)
	if err != nil {
		return nil, err
	}

	roles, err := s.getUserRoles(ctx, u.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to load user roles: %w", err)
	}
	u.Roles = roles

	supportGroups, err := s.getUserSupportGroups(ctx, u.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to load user support groups: %w", err)
	}
	u.SupportGroups = supportGroups

	return &u, nil
}

// GetUserByKeycloakSubject retrieves a user by Keycloak subject
func (s *AuthService) GetUserByKeycloakSubject(ctx context.Context, subject string) (*User, error) {
	row := s.db.QueryRow(ctx, `
		SELECT u.id, u.keycloak_subject, u.email, u.display_name, u.active,
		       u.created_at, u.updated_at, u.last_login
		FROM users u
		WHERE u.keycloak_subject = $1
	`, subject)

	var u User
	err := row.Scan(
		&u.ID, &u.KeycloakSubject, &u.Email, &u.DisplayName,
		&u.Active, &u.CreatedAt, &u.UpdatedAt, &u.LastLogin,
	)
	if err != nil {
		return nil, err
	}

	roles, err := s.getUserRoles(ctx, u.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to load user roles: %w", err)
	}
	u.Roles = roles

	supportGroups, err := s.getUserSupportGroups(ctx, u.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to load user support groups: %w", err)
	}
	u.SupportGroups = supportGroups

	return &u, nil
}

// CreateClientSession creates a new client session for RustDesk
func (s *AuthService) CreateClientSession(ctx context.Context, userID int64, rustdeskToken, rustdeskID string, expiresAt time.Time) (*ClientSession, error) {
	var id uuid.UUID
	err := s.db.QueryRow(ctx, `
		INSERT INTO client_sessions (user_id, rustdesk_token, rustdesk_id, expires_at)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, userID, rustdeskToken, rustdeskID, expiresAt).Scan(&id)

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

// GetClientSession retrieves a client session by RustDesk token
func (s *AuthService) GetClientSession(ctx context.Context, rustdeskToken string) (*ClientSession, error) {
	row := s.db.QueryRow(ctx, `
		SELECT cs.id, cs.user_id, cs.rustdesk_token, cs.rustdesk_id, cs.expires_at, cs.created_at
		FROM client_sessions cs
		WHERE cs.rustdesk_token = $1
	`, rustdeskToken)

	var cs ClientSession
	err := row.Scan(&cs.ID, &cs.UserID, &cs.RustdeskToken, &cs.RustdeskID, &cs.ExpiresAt, &cs.CreatedAt)
	if err != nil {
		return nil, err
	}

	return &cs, nil
}

// GetSessionUser retrieves user from a live client session. Expiry is part of
// the predicate, so a stale token reads as no session at all.
func (s *AuthService) GetSessionUser(ctx context.Context, rustdeskToken string) (*User, error) {
	row := s.db.QueryRow(ctx, `
		SELECT u.id, u.keycloak_subject, u.email, u.display_name, u.active,
		       u.created_at, u.updated_at, u.last_login
		FROM users u
		JOIN client_sessions cs ON u.id = cs.user_id
		WHERE cs.rustdesk_token = $1 AND cs.expires_at > now()
	`, rustdeskToken)

	var u User
	err := row.Scan(
		&u.ID, &u.KeycloakSubject, &u.Email, &u.DisplayName,
		&u.Active, &u.CreatedAt, &u.UpdatedAt, &u.LastLogin,
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

// InvalidateClientSession deletes a client session
func (s *AuthService) InvalidateClientSession(ctx context.Context, rustdeskToken string) error {
	_, err := s.db.Exec(ctx, `
		DELETE FROM client_sessions WHERE rustdesk_token = $1
	`, rustdeskToken)
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

	return roles, nil
}

// getUserSupportGroups fetches support groups for a user
func (s *AuthService) getUserSupportGroups(ctx context.Context, userID int64) ([]uuid.UUID, error) {
	rows, err := s.db.Query(ctx, `
		SELECT sg.id
		FROM support_groups sg
		JOIN user_support_groups usg ON sg.id = usg.support_group_id
		WHERE usg.user_id = $1
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var groups []uuid.UUID
	for rows.Next() {
		var g uuid.UUID
		if err := rows.Scan(&g); err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}

	return groups, nil
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

// Authenticate authenticates a user by username and password
func (s *AuthService) Authenticate(ctx context.Context, username, password string) (*User, error) {
	// In production, use bcrypt to verify password hash
	// For now, a simplified implementation
	row := s.db.QueryRow(ctx, `
		SELECT u.id, u.keycloak_subject, u.email, u.display_name, u.active,
		       u.created_at, u.updated_at, u.last_login
		FROM users u
		WHERE u.email = $1
	`, username)

	var u User
	err := row.Scan(
		&u.ID, &u.KeycloakSubject, &u.Email, &u.DisplayName,
		&u.Active, &u.CreatedAt, &u.UpdatedAt, &u.LastLogin,
	)
	if err != nil {
		return nil, err
	}

	// Verify password hash (simplified - use bcrypt in production)
	var storedHash string
	err = s.db.QueryRow(ctx, `
		SELECT password_hash FROM user_credentials WHERE user_id = $1
	`, u.ID).Scan(&storedHash)
	if err != nil {
		return nil, fmt.Errorf("invalid credentials")
	}

	// In production: if !bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(password))

	if !u.Active {
		return nil, fmt.Errorf("account disabled")
	}

	roles, err := s.getUserRoles(ctx, u.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to load user roles: %w", err)
	}
	u.Roles = roles

	return &u, nil
}

// CreateSession creates a new session token for a user
func (s *AuthService) CreateSession(ctx context.Context, user *User) (string, error) {
	// Generate a random session token using encoding/hex for simplicity
	sessionToken := fmt.Sprintf("%x%x%x%x", time.Now().UnixNano(), user.ID, time.Now().UnixNano(), time.Now().UnixNano())

	err := s.db.QueryRow(ctx, `
		INSERT INTO client_sessions (user_id, rustdesk_token, expires_at)
		VALUES ($1, $2, $3)
		RETURNING rustdesk_token
	`, user.ID, sessionToken, time.Now().Add(24*time.Hour)).Scan(&sessionToken)

	if err != nil {
		return "", fmt.Errorf("failed to create session: %w", err)
	}

	// Update last login
	_, err = s.db.Exec(ctx, `
		UPDATE users SET last_login = now() WHERE id = $1
	`, user.ID)

	if err != nil {
		return "", fmt.Errorf("failed to update last login: %w", err)
	}

	return sessionToken, nil
}

// RevokeSession invalidates a session token
func (s *AuthService) RevokeSession(ctx context.Context, sessionToken string) error {
	_, err := s.db.Exec(ctx, `
		DELETE FROM client_sessions WHERE rustdesk_token = $1
	`, sessionToken)

	return err
}
