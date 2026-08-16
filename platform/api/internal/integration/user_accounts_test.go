package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/OpenDeskViewer/platform/api/internal/identity"
)

// Phase C: manager accounts.
//
// R1 asks an administrator to create, manage and remove manager accounts. Grant
// and revoke worked; create and remove did not exist, and the closest thing to
// removal left the person able to sign in again and be provisioned afresh. What
// these check is the whole round trip, including the two halves a database
// check cannot see on its own: that the identity provider was told, and that
// the credentials the removed person held stopped working.

// createdUser is the POST /api/v1/users response.
type createdUser struct {
	ID                int64    `json:"id"`
	Email             string   `json:"email"`
	DisplayName       string   `json:"display_name"`
	KeycloakSubject   string   `json:"keycloak_subject"`
	Roles             []string `json:"roles"`
	TemporaryPassword string   `json:"temporary_password"`
}

func createUser(t *testing.T, s *v1Server, body string) (createdUser, int) {
	t.Helper()

	w := s.do(t, http.MethodPost, "/api/v1/users", body)
	var created createdUser
	if w.Code == http.StatusCreated {
		if err := json.NewDecoder(w.Body).Decode(&created); err != nil {
			t.Fatalf("failed to decode the created user: %v", err)
		}
	}
	return created, w.Code
}

// The requirement, end to end: an administrator creates a manager, that account
// exists in both places, and removing it removes both.
func TestAdministratorCreatesAndRemovesAManagerAccount(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	admin := newV1Server(t, f, f.adminID)

	created, code := createUser(t, admin,
		`{"email":"Manager@Example.com","display_name":"Morgan Reyes","role":"Support Manager"}`)
	if code != http.StatusCreated {
		t.Fatalf("creating the manager got %d", code)
	}

	// Lowercased on the way in, so a later sign-in matching by address cannot
	// miss the row over capitalisation.
	if created.Email != "manager@example.com" {
		t.Errorf("created email is %q, want it normalised to lower case", created.Email)
	}
	if created.TemporaryPassword == "" {
		t.Error("no temporary password was returned, so the administrator has no way to hand the account over")
	}
	if len(created.Roles) != 1 || created.Roles[0] != identity.RoleSupportManager {
		t.Errorf("created user holds %v, want the requested Support Manager", created.Roles)
	}

	// The identity provider half. Without it the row is a user who can never
	// sign in, which is the state the portal was in before this.
	if admin.accounts.created[created.KeycloakSubject] != "manager@example.com" {
		t.Fatalf("the identity provider was not asked to create the account; it holds %v", admin.accounts.created)
	}

	// The role is a real grant, not a field on the response.
	var roles []string
	if err := f.db.QueryRow(ctx, `
		SELECT COALESCE(ARRAY_AGG(r.name), '{}')
		FROM user_roles ur JOIN roles r ON r.id = ur.role_id
		WHERE ur.user_id = $1`, created.ID).Scan(&roles); err != nil {
		t.Fatalf("failed to read the granted roles: %v", err)
	}
	if len(roles) != 1 || roles[0] != identity.RoleSupportManager {
		t.Errorf("the database records roles %v for the new manager", roles)
	}

	// And removal takes both halves away.
	w := admin.do(t, http.MethodDelete, fmt.Sprintf("/api/v1/users/%d", created.ID), "")
	if w.Code != http.StatusOK {
		t.Fatalf("removing the manager got %d: %s", w.Code, w.Body.String())
	}
	if !admin.accounts.wasDeleted(created.KeycloakSubject) {
		t.Error("the local row was removed and the identity provider account was left behind, so the person can sign in again")
	}

	var remaining int
	if err := f.db.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE id = $1`, created.ID).Scan(&remaining); err != nil {
		t.Fatalf("failed to count the user: %v", err)
	}
	if remaining != 0 {
		t.Error("the user row survived the delete")
	}

	// The audit trail outlives the user it names. audit_events has no foreign
	// key to users since 000006, and this is what that buys.
	var events int
	if err := f.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM audit_events WHERE event_type IN ('user.created', 'user.deleted')`).Scan(&events); err != nil {
		t.Fatalf("failed to count audit events: %v", err)
	}
	if events != 2 {
		t.Errorf("expected a user.created and a user.deleted audit row, found %d", events)
	}
}

// Removal has to withdraw the credentials the person already holds, not just
// their ability to ask for more. This is the hole C.2 named: a deactivated
// manager who wrote the device passwords down still had working ones.
func TestRemovingAUserRotatesThePasswordsTheyCouldRead(t *testing.T) {
	f := newFixture(t)
	admin := newV1Server(t, f, f.adminID)

	// tech1 reaches deviceGroup1 through group1; device4 is in the other group.
	seedPassword(t, f, f.device1)
	seedPassword(t, f, f.device4)
	reachableBefore := passwordVersion(t, f, f.device1)
	foreignBefore := passwordVersion(t, f, f.device4)

	w := admin.do(t, http.MethodDelete, fmt.Sprintf("/api/v1/users/%d", f.tech1ID), "")
	if w.Code != http.StatusOK {
		t.Fatalf("removing the technician got %d: %s", w.Code, w.Body.String())
	}
	body := decodeJSON(t, w)
	if n, _ := body["passwords_rotated"].(float64); n < 1 {
		t.Fatalf("removal rotated %v passwords; the devices they could reach were not among them", body["passwords_rotated"])
	}

	if got := passwordVersion(t, f, f.device1); got == reachableBefore {
		t.Error("a device the removed user could reach kept its password")
	}
	if got := passwordVersion(t, f, f.device4); got != foreignBefore {
		t.Error("a device the removed user could not reach was rotated, which is churn on a machine nobody lost access to")
	}
}

// Deactivation is the softer half of the same withdrawal and leaks the same
// credential, so it rotates too.
func TestDeactivationRotatesThePasswordsTheyCouldRead(t *testing.T) {
	f := newFixture(t)
	admin := newV1Server(t, f, f.adminID)

	seedPassword(t, f, f.device1)
	seedPassword(t, f, f.device4)
	reachableBefore := passwordVersion(t, f, f.device1)
	foreignBefore := passwordVersion(t, f, f.device4)

	w := admin.do(t, http.MethodPatch, fmt.Sprintf("/api/v1/users/%d", f.tech1ID), `{"active":false}`)
	if w.Code != http.StatusOK {
		t.Fatalf("deactivation got %d: %s", w.Code, w.Body.String())
	}

	if got := passwordVersion(t, f, f.device1); got == reachableBefore {
		t.Error("deactivation left the passwords the user had been shown working")
	}
	if got := passwordVersion(t, f, f.device4); got != foreignBefore {
		t.Error("deactivation rotated a device the user never reached")
	}
}

// C.3. The self-lockout guard compared against "admin" and "manager"; the
// seeded roles are "Administrator" and "Support Manager", so it matched nothing
// and an administrator could revoke their own role with no way back through the
// portal. Reverting the constants to those literals fails here.
func TestAnAdministratorCannotRevokeTheirOwnAdministratorRole(t *testing.T) {
	f := newFixture(t)
	admin := newV1Server(t, f, f.adminID)

	path := fmt.Sprintf("/api/v1/users/%d/roles/%s", f.adminID, "Administrator")
	if w := admin.do(t, http.MethodDelete, path, ""); w.Code != http.StatusBadRequest {
		t.Fatalf("revoking your own Administrator role got %d, want 400: %s", w.Code, w.Body.String())
	}

	var stillHolds bool
	if err := f.db.QueryRow(context.Background(), `
		SELECT EXISTS (
			SELECT 1 FROM user_roles ur JOIN roles r ON r.id = ur.role_id
			WHERE ur.user_id = $1 AND r.name = 'Administrator')`, f.adminID).Scan(&stillHolds); err != nil {
		t.Fatalf("failed to read the roles: %v", err)
	}
	if !stillHolds {
		t.Error("the role was revoked despite the refusal")
	}
}

// The self guard is not the invariant that matters. A Support Manager also
// passes the administration gate, so the three routes that can leave a
// deployment with nobody able to administer it are checked from that direction:
// somebody who is not the last administrator removing the one who is.
func TestTheLastAdministratorCannotBeRemovedByAnybody(t *testing.T) {
	f := newFixture(t)

	managerID := f.newUser(t, "sub-manager", "manager@example.com", identity.RoleSupportManager)
	manager := newV1Server(t, f, managerID)

	user := fmt.Sprintf("/api/v1/users/%d", f.adminID)

	t.Run("revoke", func(t *testing.T) {
		if w := manager.do(t, http.MethodDelete, user+"/roles/Administrator", ""); w.Code != http.StatusBadRequest {
			t.Errorf("revoking the last Administrator got %d, want 400: %s", w.Code, w.Body.String())
		}
	})
	t.Run("deactivate", func(t *testing.T) {
		if w := manager.do(t, http.MethodPatch, user, `{"active":false}`); w.Code != http.StatusBadRequest {
			t.Errorf("deactivating the last administrator got %d, want 400: %s", w.Code, w.Body.String())
		}
	})
	t.Run("delete", func(t *testing.T) {
		if w := manager.do(t, http.MethodDelete, user, ""); w.Code != http.StatusBadRequest {
			t.Errorf("removing the last administrator got %d, want 400: %s", w.Code, w.Body.String())
		}
	})

	// And the guard is not simply "never touch an administrator": with a second
	// one in place the same call goes through.
	second := f.newUser(t, "sub-admin2", "admin2@example.com", identity.RoleAdministrator)
	if w := manager.do(t, http.MethodDelete, user, ""); w.Code != http.StatusOK {
		t.Fatalf("removing an administrator while another remains got %d, want 200: %s", w.Code, w.Body.String())
	}
	var left int64
	if err := f.db.QueryRow(context.Background(), `SELECT COUNT(*) FROM users WHERE id = $1`, second).Scan(&left); err != nil {
		t.Fatalf("failed to count: %v", err)
	}
	if left != 1 {
		t.Error("the second administrator went missing")
	}
}

// An address that already has an account is refused before Keycloak is touched,
// so the common mistake -- inviting somebody twice -- leaves nothing behind.
func TestCreatingADuplicateAccountTouchesNothing(t *testing.T) {
	f := newFixture(t)
	admin := newV1Server(t, f, f.adminID)

	if _, code := createUser(t, admin, `{"email":"tech1@example.com","role":"Technician"}`); code != http.StatusConflict {
		t.Fatalf("creating a user with a seeded address got %d, want 409", code)
	}
	if len(admin.accounts.created) != 0 {
		t.Errorf("an account was created in the identity provider for a duplicate address: %v", admin.accounts.created)
	}
}

// An unknown role is refused rather than creating an account that answers 403
// from every screen and looks like a broken deployment.
func TestCreatingAUserWithAnUnknownRoleIsRefused(t *testing.T) {
	f := newFixture(t)
	admin := newV1Server(t, f, f.adminID)

	if _, code := createUser(t, admin, `{"email":"nobody@example.com","role":"Manager"}`); code != http.StatusBadRequest {
		t.Fatalf(`creating a user with the role "Manager" got %d, want 400: the seeded role is "Support Manager"`, code)
	}
	if len(admin.accounts.created) != 0 {
		t.Error("an account was created in the identity provider for a request that was refused")
	}
}

// The compensation path. The account is created before the local row because
// the subject is Keycloak's to issue, so a local insert that fails has to take
// the account back down with it -- otherwise the retry the administrator is
// about to make answers 409 forever, from a conflict they cannot see.
func TestAFailedLocalInsertRemovesTheAccountAgain(t *testing.T) {
	f := newFixture(t)
	admin := newV1Server(t, f, f.adminID)

	// The fake derives its subject from the email, so seeding a row that
	// already holds that subject makes the insert fail on the unique index
	// while the address itself is free.
	f.mustExec(t, `
		INSERT INTO users (keycloak_subject, email, display_name)
		VALUES ('kc-collision@example.com', 'someone-else@example.com', 'Someone Else')`)

	_, code := createUser(t, admin, `{"email":"collision@example.com","role":"Technician"}`)
	if code != http.StatusConflict {
		t.Fatalf("the colliding create got %d, want 409", code)
	}
	if !admin.accounts.wasDeleted("kc-collision@example.com") {
		t.Error("the identity provider account was orphaned by the failed insert")
	}
}

// A machine identity's Keycloak account belongs to the client it hangs off.
// Deleting it here would break the integration's client rather than withdraw
// its access, which is what revoking its roles is for.
func TestRemovingAServiceAccountLeavesTheIdentityProviderAlone(t *testing.T) {
	f := newFixture(t)
	admin := newV1Server(t, f, f.adminID)

	machineID := f.newUser(t, "sub-service", "service-account-odv-integration@service-account.invalid", identity.RoleTechnician)

	w := admin.do(t, http.MethodDelete, fmt.Sprintf("/api/v1/users/%d", machineID), "")
	if w.Code != http.StatusOK {
		t.Fatalf("removing the service account got %d: %s", w.Code, w.Body.String())
	}
	if body := decodeJSON(t, w); body["identity_account_removed"] != false {
		t.Error("the response claims the identity provider account was removed")
	}
	if len(admin.accounts.deleted) != 0 {
		t.Errorf("the identity provider was asked to delete %v", admin.accounts.deleted)
	}
}

// Removal refuses when the identity provider does, rather than deleting the
// local row on its own. Deleting locally while the account lives is the
// dangerous direction: the person signs in again and provisionUser gives them a
// fresh Technician role.
func TestRemovalIsRefusedWhenTheIdentityProviderFails(t *testing.T) {
	f := newFixture(t)
	admin := newV1Server(t, f, f.adminID)
	admin.accounts.deleteErr = errors.New("keycloak is unreachable")

	w := admin.do(t, http.MethodDelete, fmt.Sprintf("/api/v1/users/%d", f.tech1ID), "")
	if w.Code != http.StatusBadGateway {
		t.Fatalf("removal with a failing identity provider got %d, want 502: %s", w.Code, w.Body.String())
	}

	var remaining int
	if err := f.db.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM users WHERE id = $1`, f.tech1ID).Scan(&remaining); err != nil {
		t.Fatalf("failed to count the user: %v", err)
	}
	if remaining != 1 {
		t.Error("the local row was removed even though the identity provider account survives")
	}
}
