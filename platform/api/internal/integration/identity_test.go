package integration

import (
	"context"
	"strings"
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

	// Sessions store a SHA-256 of the token, never the token itself, so the
	// fixture has to hash on the way in just as CreateSession does.
	if _, err := f.db.Exec(ctx, `
		INSERT INTO client_sessions (user_id, token_hash, expires_at)
		VALUES ($1, encode(sha256('live-token'), 'hex'), now() + interval '1 hour'),
		       ($1, encode(sha256('stale-token'), 'hex'), now() - interval '1 hour')
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

// Every way of looking a user up has to return the same user. The four lookups
// used to carry four copies of the query, and the copies had drifted: the
// session path attached no support groups, so the same account was a different
// object depending on which door it came through.
func TestEveryUserLookupReturnsTheSameUser(t *testing.T) {
	f := newFixture(t)
	svc := identity.NewAuthService(f.db, "")
	ctx := context.Background()

	if _, err := f.db.Exec(ctx, `
		INSERT INTO client_sessions (user_id, token_hash, expires_at)
		VALUES ($1, encode(sha256('lookup-token'), 'hex'), now() + interval '1 hour')
	`, f.tech1ID); err != nil {
		t.Fatalf("failed to create a session: %v", err)
	}

	lookups := map[string]func() (*identity.User, error){
		"by id":      func() (*identity.User, error) { return svc.GetUserByID(ctx, f.tech1ID) },
		"by email":   func() (*identity.User, error) { return svc.GetUserByEmail(ctx, "tech1@example.com") },
		"by subject": func() (*identity.User, error) { return svc.GetUserByKeycloakSubject(ctx, "sub-tech1") },
		"by session": func() (*identity.User, error) { return svc.GetSessionUser(ctx, "lookup-token") },
	}

	for name, lookup := range lookups {
		t.Run(name, func(t *testing.T) {
			u, err := lookup()
			if err != nil {
				t.Fatalf("lookup failed: %v", err)
			}
			if u.ID != f.tech1ID {
				t.Errorf("id = %d, want %d", u.ID, f.tech1ID)
			}
			if u.Email != "tech1@example.com" {
				t.Errorf("email = %q", u.Email)
			}
			if len(u.Roles) != 1 || u.Roles[0].Name != "Technician" {
				t.Errorf("roles = %+v, want exactly Technician", u.Roles)
			}
			if len(u.SupportGroups) != 1 || u.SupportGroups[0] != f.group1 {
				t.Errorf("support groups = %v, want [%s]", u.SupportGroups, f.group1)
			}
		})
	}

	// A user in no group reads as an empty list rather than failing to scan,
	// which is what the array_agg COALESCE is for.
	lonely := f.newUser(t, "sub-lonely", "lonely@example.com", "Technician")
	u, err := svc.GetUserByID(ctx, lonely)
	if err != nil {
		t.Fatalf("lookup of a user in no support group failed: %v", err)
	}
	if len(u.SupportGroups) != 0 {
		t.Errorf("support groups = %v, want none", u.SupportGroups)
	}
}

// An external system authenticates with a Keycloak client-credentials grant.
// That token carries no email claim, which used to fail provisioning outright,
// so no machine caller could authenticate at all and the ODV API could not be
// driven from another system.
func TestServiceAccountIsProvisionedWithoutAnEmail(t *testing.T) {
	f := newFixture(t)
	svc := identity.NewAuthService(f.db, "")
	ctx := context.Background()

	claims := &identity.JWTClaims{
		Subject:           "sub-crm-integration",
		PreferredUsername: identity.ServiceAccountPrefix + "odv-crm",
	}
	user, err := svc.ResolveUser(ctx, claims)
	if err != nil {
		t.Fatalf("a service account could not be provisioned: %v", err)
	}

	// Granted nothing. A machine account's reach is a decision an administrator
	// makes, not a default, so its first call must be refused until they do.
	if len(user.Roles) != 0 {
		t.Errorf("roles = %v, want none; a service account must start inert", user.Roles)
	}

	// The synthetic address must not be able to collide with a real mailbox,
	// or an invitation could be sent to a machine identity.
	if !strings.HasSuffix(user.Email, ".invalid") {
		t.Errorf("email = %q, want an address in a domain no mailbox can occupy", user.Email)
	}

	// Resolving again returns the same account rather than making a second one.
	again, err := svc.ResolveUser(ctx, claims)
	if err != nil {
		t.Fatalf("second resolve failed: %v", err)
	}
	if again.ID != user.ID {
		t.Errorf("second resolve created a new user (%d then %d)", user.ID, again.ID)
	}
}

// A person's token still needs an email, because that is what identifies them
// and what an administrator invites. Only the service-account shape is exempt.
func TestATokenWithNoEmailAndNoServiceAccountIsStillRefused(t *testing.T) {
	f := newFixture(t)
	svc := identity.NewAuthService(f.db, "")

	_, err := svc.ResolveUser(context.Background(), &identity.JWTClaims{
		Subject:           "sub-anonymous",
		PreferredUsername: "someone",
	})
	if err == nil {
		t.Fatal("a token with no email and no service-account username was provisioned")
	}
}
