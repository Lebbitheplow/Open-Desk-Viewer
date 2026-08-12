package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// ResolveUser returns the local user for a validated token, creating the record
// on first sign-in. Steady-state requests only read, so the write path is taken
// once per user.
func (s *AuthService) ResolveUser(ctx context.Context, claims *JWTClaims) (*User, error) {
	user, err := s.GetUserByKeycloakSubject(ctx, claims.Subject)
	if err == nil {
		return user, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	return s.provisionUser(ctx, claims)
}

// provisionUser creates a user from token claims and grants it a default role.
func (s *AuthService) provisionUser(ctx context.Context, claims *JWTClaims) (*User, error) {
	email := strings.TrimSpace(claims.Email)
	if email == "" {
		return nil, fmt.Errorf("cannot provision subject %q: token carries no email claim", claims.Subject)
	}

	displayName := firstNonEmpty(claims.Name, claims.PreferredUsername, email)

	// xmax = 0 is true only for a freshly inserted row, which distinguishes a
	// first sign-in from a claim refresh on an existing account.
	var userID int64
	var created bool
	err := s.db.QueryRow(ctx, `
		INSERT INTO users (keycloak_subject, email, display_name)
		VALUES ($1, $2, $3)
		ON CONFLICT (keycloak_subject) DO UPDATE
		    SET email = EXCLUDED.email,
		        display_name = EXCLUDED.display_name,
		        updated_at = now()
		RETURNING id, (xmax = 0)
	`, claims.Subject, email, displayName).Scan(&userID, &created)
	if err != nil {
		return nil, fmt.Errorf("failed to provision user %q: %w", email, err)
	}

	if created {
		role := RoleTechnician
		if s.bootstrapAdminEmail != "" && strings.EqualFold(email, s.bootstrapAdminEmail) {
			role = RoleAdministrator
		}

		// Only on creation: re-granting on every sign-in would undo an
		// administrator who deliberately removed the role.
		_, err = s.db.Exec(ctx, `
			INSERT INTO user_roles (user_id, role_id)
			SELECT $1, r.id FROM roles r WHERE r.name = $2
			ON CONFLICT DO NOTHING
		`, userID, role)
		if err != nil {
			return nil, fmt.Errorf("failed to grant %q to user %q: %w", role, email, err)
		}
	}

	return s.GetUserByID(ctx, userID)
}

// ListAllUsers returns every user, for administrators and support managers.
func (s *AuthService) ListAllUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, keycloak_subject, email, display_name, active
		FROM users
		ORDER BY display_name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanUserSummaries(rows)
}

// ListUsersSharingSupportGroups returns the users who belong to at least one of
// the given user's support groups.
func (s *AuthService) ListUsersSharingSupportGroups(ctx context.Context, userID int64) ([]User, error) {
	rows, err := s.db.Query(ctx, `
		SELECT DISTINCT u.id, u.keycloak_subject, u.email, u.display_name, u.active
		FROM users u
		JOIN user_support_groups usg ON u.id = usg.user_id
		WHERE usg.support_group_id IN (
			SELECT support_group_id FROM user_support_groups WHERE user_id = $1
		)
		ORDER BY u.display_name
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanUserSummaries(rows)
}

// scanUserSummaries reads the shared column list used by the list queries.
func scanUserSummaries(rows pgx.Rows) ([]User, error) {
	var users []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.KeycloakSubject, &u.Email, &u.DisplayName, &u.Active); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// ListAllUsersPaginated returns users for administrators with pagination.
func (s *AuthService) ListAllUsersPaginated(ctx context.Context, current, pageSize int64) ([]User, int64, error) {
	offset := (current - 1) * pageSize
	
	rows, err := s.db.Query(ctx, `
		SELECT id, keycloak_subject, email, display_name, active
		FROM users
		ORDER BY display_name
		LIMIT $1 OFFSET $2
	`, pageSize, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	
	users, err := scanUserSummaries(rows)
	if err != nil {
		return nil, 0, err
	}
	
	// Get total count
	var total int64
	err = s.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM users
	`).Scan(&total)
	if err != nil {
		return nil, 0, err
	}
	
	return users, total, nil
}

// ListUsersSharingSupportGroupsPaginated returns users sharing support groups with pagination.
func (s *AuthService) ListUsersSharingSupportGroupsPaginated(ctx context.Context, userID int64, current, pageSize int64) ([]User, int64, error) {
	offset := (current - 1) * pageSize
	
	rows, err := s.db.Query(ctx, `
		SELECT DISTINCT u.id, u.keycloak_subject, u.email, u.display_name, u.active
		FROM users u
		JOIN user_support_groups usg ON u.id = usg.user_id
		WHERE usg.support_group_id IN (
			SELECT support_group_id FROM user_support_groups WHERE user_id = $1
		)
		ORDER BY u.display_name
		LIMIT $2 OFFSET $3
	`, userID, pageSize, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	
	users, err := scanUserSummaries(rows)
	if err != nil {
		return nil, 0, err
	}
	
	// Get total count
	var total int64
	err = s.db.QueryRow(ctx, `
		SELECT COUNT(DISTINCT u.id)
		FROM users u
		JOIN user_support_groups usg ON u.id = usg.user_id
		WHERE usg.support_group_id IN (
			SELECT support_group_id FROM user_support_groups WHERE user_id = $1
		)
	`, userID).Scan(&total)
	if err != nil {
		return nil, 0, err
	}
	
	return users, total, nil
}

// firstNonEmpty returns the first argument that is not blank.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
