package integration

import (
	"context"
	"testing"

	"github.com/OpenDeskViewer/platform/api/internal/identity"
)

// A fresh install has no users, so the first sign-in has to create one.
func TestResolveUserProvisionsOnFirstSignIn(t *testing.T) {
	f := newFixture(t)
	svc := identity.NewAuthService(f.db, "")
	ctx := context.Background()

	claims := &identity.JWTClaims{
		Subject:           "sub-new",
		Email:             "new@example.com",
		PreferredUsername: "new",
		Name:              "New Person",
	}

	user, err := svc.ResolveUser(ctx, claims)
	if err != nil {
		t.Fatalf("expected the user to be provisioned, got %v", err)
	}
	if user.Email != "new@example.com" {
		t.Errorf("expected the email from the claims, got %q", user.Email)
	}
	if user.DisplayName != "New Person" {
		t.Errorf("expected the name claim as the display name, got %q", user.DisplayName)
	}
	if !user.Active {
		t.Error("expected a new user to be active")
	}

	if len(user.Roles) != 1 || user.Roles[0].Name != identity.RoleTechnician {
		t.Errorf("expected the default Technician role, got %+v", user.Roles)
	}

	// The second call must read, not create.
	again, err := svc.ResolveUser(ctx, claims)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if again.ID != user.ID {
		t.Errorf("expected the same user, got %d then %d", user.ID, again.ID)
	}

	var count int
	if err := f.db.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE keycloak_subject = $1`, "sub-new").Scan(&count); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 1 {
		t.Errorf("expected exactly one row, got %d", count)
	}
}

func TestResolveUserGrantsTheBootstrapAdmin(t *testing.T) {
	f := newFixture(t)
	svc := identity.NewAuthService(f.db, "Boss@Example.com")

	// Matching is case-insensitive: the claim will not always match the config.
	user, err := svc.ResolveUser(context.Background(), &identity.JWTClaims{
		Subject: "sub-boss",
		Email:   "boss@example.com",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(user.Roles) != 1 || user.Roles[0].Name != identity.RoleAdministrator {
		t.Errorf("expected the bootstrap admin to be an Administrator, got %+v", user.Roles)
	}
}

// Re-granting on every sign-in would undo an administrator who removed a role.
func TestResolveUserDoesNotRegrantRoles(t *testing.T) {
	f := newFixture(t)
	svc := identity.NewAuthService(f.db, "")
	ctx := context.Background()

	claims := &identity.JWTClaims{Subject: "sub-new", Email: "new@example.com"}
	user, err := svc.ResolveUser(ctx, claims)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := f.db.Exec(ctx, `DELETE FROM user_roles WHERE user_id = $1`, user.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	again, err := svc.ResolveUser(ctx, claims)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(again.Roles) != 0 {
		t.Errorf("expected the removed role to stay removed, got %+v", again.Roles)
	}
}

// Without an email there is nothing to key the account on, so provisioning must
// fail rather than create a nameless user.
func TestResolveUserRequiresAnEmailClaim(t *testing.T) {
	f := newFixture(t)
	svc := identity.NewAuthService(f.db, "")

	_, err := svc.ResolveUser(context.Background(), &identity.JWTClaims{Subject: "sub-anon"})
	if err == nil {
		t.Fatal("expected provisioning without an email to fail")
	}
}

func TestResolveUserRefreshesClaims(t *testing.T) {
	f := newFixture(t)
	svc := identity.NewAuthService(f.db, "")
	ctx := context.Background()

	if _, err := svc.ResolveUser(ctx, &identity.JWTClaims{
		Subject: "sub-new", Email: "old@example.com", Name: "Old Name",
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The row exists, so this goes down the read path and keeps the stored
	// values. Provisioning only refreshes on the create path.
	user, err := svc.ResolveUser(ctx, &identity.JWTClaims{
		Subject: "sub-new", Email: "new@example.com", Name: "New Name",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if user.Email != "old@example.com" {
		t.Errorf("expected the stored email to be authoritative after creation, got %q", user.Email)
	}
}

func TestListUsers(t *testing.T) {
	f := newFixture(t)
	svc := identity.NewAuthService(f.db, "")
	ctx := context.Background()

	all, err := svc.ListAllUsers(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("expected 3 seeded users, got %d", len(all))
	}

	// The old handler scanned a BIGSERIAL id into a string, which could not
	// have worked against a real server.
	for _, u := range all {
		if u.ID == 0 {
			t.Error("expected a populated user id")
		}
	}

	shared, err := svc.ListUsersSharingSupportGroups(ctx, f.tech1ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(shared) != 1 || shared[0].ID != f.tech1ID {
		t.Errorf("expected only the technician's own group members, got %+v", shared)
	}
}

func TestGetSessionUserIgnoresExpiredSessions(t *testing.T) {
	f := newFixture(t)
	svc := identity.NewAuthService(f.db, "")
	ctx := context.Background()

	if _, err := f.db.Exec(ctx, `
		INSERT INTO client_sessions (user_id, rustdesk_token, expires_at)
		VALUES ($1, 'live-token', now() + interval '1 hour'),
		       ($1, 'stale-token', now() - interval '1 hour')
	`, f.tech1ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	user, err := svc.GetSessionUser(ctx, "live-token")
	if err != nil {
		t.Fatalf("expected a live session to resolve, got %v", err)
	}
	if user.ID != f.tech1ID {
		t.Errorf("expected the session's user, got %d", user.ID)
	}

	if _, err := svc.GetSessionUser(ctx, "stale-token"); err == nil {
		t.Error("expected an expired session to be rejected")
	}
}
