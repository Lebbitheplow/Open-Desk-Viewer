package apiv1

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/OpenDeskViewer/platform/api/internal/audit"
	"github.com/OpenDeskViewer/platform/api/internal/httpx"
	"github.com/OpenDeskViewer/platform/api/internal/identity"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

// User is a portal user with the roles and groups the admin screen edits.
type User struct {
	ID              int64          `json:"id"`
	KeycloakSubject string         `json:"keycloak_subject"`
	Email           string         `json:"email"`
	DisplayName     string         `json:"display_name"`
	Active          bool           `json:"active"`
	Roles           []string       `json:"roles"`
	SupportGroups   []GroupSummary `json:"support_groups,omitempty"`
	LastLogin       *time.Time     `json:"last_login,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
}

// HandleUsers serves GET /api/v1/users.
func (h *Handler) HandleUsers(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.viewer(w, r); !ok {
		return
	}

	p := parsePage(r)
	query := r.URL.Query()

	var conditions []string
	var args []any

	if q := strings.TrimSpace(query.Get("q")); q != "" {
		args = append(args, "%"+q+"%")
		conditions = append(conditions, "(u.email ILIKE $1 OR u.display_name ILIKE $1)")
	}
	if active := strings.TrimSpace(query.Get("active")); active != "" {
		parsed, err := strconv.ParseBool(active)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid active filter")
			return
		}
		args = append(args, parsed)
		conditions = append(conditions, "u.active = $"+strconv.Itoa(len(args)))
	}

	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}

	var total int64
	if err := h.db.QueryRow(r.Context(), `SELECT COUNT(*) FROM users u`+where, args...).Scan(&total); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to count users")
		return
	}

	args = append(args, p.PageSize, p.Offset)
	rows, err := h.db.Query(r.Context(), `
		SELECT u.id, u.keycloak_subject, u.email, u.display_name, u.active, u.last_login, u.created_at,
		       COALESCE(ARRAY_AGG(r.name) FILTER (WHERE r.name IS NOT NULL), '{}')
		FROM users u
		LEFT JOIN user_roles ur ON ur.user_id = u.id
		LEFT JOIN roles r ON r.id = ur.role_id`+where+`
		GROUP BY u.id
		ORDER BY u.display_name
		LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to list users")
		return
	}
	defer rows.Close()

	users := make([]User, 0)
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.KeycloakSubject, &u.Email, &u.DisplayName, &u.Active,
			&u.LastLogin, &u.CreatedAt, &u.Roles); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to read user")
			return
		}
		users = append(users, u)
	}
	if rows.Err() != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to read users")
		return
	}

	writePage(w, p, total, users)
}

// CreatedUser is the answer to POST /api/v1/users.
//
// TemporaryPassword is returned exactly once, in this response, and is stored
// nowhere: Keycloak holds only its hash and the account carries the
// UPDATE_PASSWORD required action, so the value is spent the first time the
// person signs in. The administrator hands it over out of band, which is what
// makes this work in a deployment with no mail server -- and the alpha has no
// mail server, so an invitation email would have been a feature that silently
// did nothing.
type CreatedUser struct {
	User
	TemporaryPassword string `json:"temporary_password"`
}

// HandleCreateUser serves POST /api/v1/users: create an account.
//
// This is the half of R1 that did not exist. Until now a users row appeared
// only as a side effect of somebody signing in through Keycloak, and the realm
// sets registrationAllowed false, so creating a manager meant reaching the
// Keycloak admin console -- which platform/Caddyfile deliberately does not
// route, so it meant container-level access. "Administrators can create manager
// accounts" was not true of anything an administrator could reach.
//
// The account is created in Keycloak first and the local row second, because
// the subject the local row keys on is Keycloak's to issue. A local insert that
// fails therefore has to take the Keycloak account back down with it, or the
// next attempt at the same address answers 409 forever.
func (h *Handler) HandleCreateUser(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.admin(w, r)
	if !ok {
		return
	}
	if h.accounts == nil {
		writeJSONError(w, http.StatusServiceUnavailable,
			"account creation is not configured: set KEYCLOAK_URL, KEYCLOAK_REALM, KEYCLOAK_CLIENT_API and KEYCLOAK_CLIENT_SECRET, and grant that service account the realm-management role manage-users")
		return
	}

	var req struct {
		Email       string `json:"email"`
		DisplayName string `json:"display_name"`
		Role        string `json:"role"`
	}
	if !decodeBody(w, r, &req) {
		return
	}

	email := strings.ToLower(strings.TrimSpace(req.Email))
	if !plausibleEmail(email) {
		writeJSONError(w, http.StatusBadRequest, "a valid email address is required")
		return
	}
	// .invalid is where identity.provisionUser puts machine accounts precisely
	// because no mailbox can occupy it. A person created there could never be
	// reached, and the address would collide with a service account's.
	if identity.IsServiceAccountEmail(email) {
		writeJSONError(w, http.StatusBadRequest, "that address belongs to the machine-account namespace")
		return
	}

	displayName := strings.TrimSpace(req.DisplayName)
	if displayName == "" {
		displayName = email
	}

	// An unknown role would otherwise create the account and grant nothing,
	// which looks like a working account that answers 403 everywhere.
	role := strings.TrimSpace(req.Role)
	if role != "" {
		var exists bool
		if err := h.db.QueryRow(r.Context(),
			`SELECT EXISTS (SELECT 1 FROM roles WHERE name = $1)`, role).Scan(&exists); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to check the role")
			return
		}
		if !exists {
			writeJSONError(w, http.StatusBadRequest, "unknown role: "+role)
			return
		}
	}

	// Checked before Keycloak is touched, so the common mistake -- inviting
	// somebody twice -- does not leave an account behind to compensate for.
	var taken bool
	if err := h.db.QueryRow(r.Context(),
		`SELECT EXISTS (SELECT 1 FROM users WHERE lower(email) = $1)`, email).Scan(&taken); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to check the address")
		return
	}
	if taken {
		writeJSONError(w, http.StatusConflict, "a user with that email already exists")
		return
	}

	account, err := h.accounts.CreateAccount(r.Context(), email, displayName)
	if err != nil {
		if errors.Is(err, identity.ErrAccountExists) {
			writeJSONError(w, http.StatusConflict, "the identity provider already has an account for that email")
			return
		}
		log.Error().Err(err).Str("email", email).Msg("failed to create the identity provider account")
		writeJSONError(w, http.StatusBadGateway, "the identity provider refused to create the account")
		return
	}

	var u User
	err = h.db.QueryRow(r.Context(), `
		INSERT INTO users (keycloak_subject, email, display_name)
		VALUES ($1, $2, $3)
		RETURNING id, keycloak_subject, email, display_name, active, last_login, created_at`,
		account.Subject, email, displayName).
		Scan(&u.ID, &u.KeycloakSubject, &u.Email, &u.DisplayName, &u.Active, &u.LastLogin, &u.CreatedAt)
	if err != nil {
		// The account exists and nothing here references it. Leaving it would
		// make the retry that the administrator is about to attempt fail with a
		// conflict they cannot resolve from the portal.
		if undo := h.accounts.DeleteAccount(r.Context(), account.Subject); undo != nil {
			log.Error().Err(undo).Str("subject", account.Subject).
				Msg("failed to remove the identity provider account after the local insert failed; it is now orphaned")
		}
		dbError(w, err, "failed to create the user")
		return
	}

	u.Roles = []string{}
	if role != "" {
		if _, err := h.db.Exec(r.Context(), `
			INSERT INTO user_roles (user_id, role_id)
			SELECT $1, r.id FROM roles r WHERE r.name = $2`, u.ID, role); err != nil {
			// The account and the row both exist, so this is recoverable from
			// the portal with one more click. Reporting success would not be.
			dbError(w, err, "the account was created but the role could not be granted")
			return
		}
		u.Roles = []string{role}
	}

	h.record(r.Context(), audit.Event{
		Type:        "user.created",
		ActorID:     actor.ID,
		Resource:    "user",
		ResourceID:  strconv.FormatInt(u.ID, 10),
		Description: "User " + email + " created",
		Metadata:    map[string]any{"email": email, "role": role, "user_id": u.ID},
	})

	noStore(w)
	httpx.WriteJSON(w, http.StatusCreated, CreatedUser{User: u, TemporaryPassword: account.TemporaryPassword})
}

// plausibleEmail is a shape check, not a validation: Keycloak is the authority
// on what it will accept, and this only exists to turn the obvious mistakes
// into a 400 rather than a 502.
func plausibleEmail(email string) bool {
	at := strings.Index(email, "@")
	return at > 0 && at < len(email)-1 &&
		!strings.ContainsAny(email, " \t\r\n") &&
		strings.Contains(email[at:], ".")
}

// HandleUserDetail serves GET /api/v1/users/{id}.
func (h *Handler) HandleUserDetail(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.viewer(w, r); !ok {
		return
	}
	userID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid user id")
		return
	}

	var u User
	err = h.db.QueryRow(r.Context(), `
		SELECT u.id, u.keycloak_subject, u.email, u.display_name, u.active, u.last_login, u.created_at,
		       COALESCE(ARRAY_AGG(r.name) FILTER (WHERE r.name IS NOT NULL), '{}')
		FROM users u
		LEFT JOIN user_roles ur ON ur.user_id = u.id
		LEFT JOIN roles r ON r.id = ur.role_id
		WHERE u.id = $1
		GROUP BY u.id`, userID).
		Scan(&u.ID, &u.KeycloakSubject, &u.Email, &u.DisplayName, &u.Active,
			&u.LastLogin, &u.CreatedAt, &u.Roles)
	if err != nil {
		dbError(w, err, "failed to read user")
		return
	}

	groupRows, err := h.db.Query(r.Context(), `
		SELECT g.id, g.name
		FROM support_groups g
		JOIN user_support_groups m ON m.support_group_id = g.id
		WHERE m.user_id = $1
		ORDER BY g.name`, userID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to read support groups")
		return
	}
	defer groupRows.Close()

	u.SupportGroups = make([]GroupSummary, 0)
	for groupRows.Next() {
		var g GroupSummary
		if err := groupRows.Scan(&g.ID, &g.Name); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to read support group")
			return
		}
		u.SupportGroups = append(u.SupportGroups, g)
	}
	if groupRows.Err() != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to read support groups")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, u)
}

// HandleUpdateUser serves PATCH /api/v1/users/{id}: activate and deactivate.
//
// Deactivation has to take effect immediately, which it does: the JWT
// middleware rejects a disabled user on every request, so this is not merely a
// display flag.
func (h *Handler) HandleUpdateUser(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.admin(w, r)
	if !ok {
		return
	}
	userID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid user id")
		return
	}

	var req struct {
		Active      *bool   `json:"active"`
		DisplayName *string `json:"display_name"`
	}
	if !decodeBody(w, r, &req) {
		return
	}

	// Locking yourself out is a support call, not a feature.
	if req.Active != nil && !*req.Active && userID == actor.ID {
		writeJSONError(w, http.StatusBadRequest, "you cannot deactivate your own account")
		return
	}

	deactivating := req.Active != nil && !*req.Active

	if deactivating {
		last, err := h.isLastAdministrator(r.Context(), userID)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to check administrator cover")
			return
		}
		if last {
			writeJSONError(w, http.StatusBadRequest,
				"that is the only active administrator; grant Administrator to somebody else first")
			return
		}
	}

	// Read before the update, so this is the set they could reach while the
	// account still worked.
	var reachable []uuid.UUID
	if deactivating {
		var err error
		if reachable, err = h.devicesReachableByUser(r.Context(), userID); err != nil {
			dbError(w, err, "failed to read the devices this user can reach")
			return
		}
	}

	var u User
	err = h.db.QueryRow(r.Context(), `
		UPDATE users
		SET active       = COALESCE($2, active),
		    display_name = COALESCE($3, display_name),
		    updated_at   = now()
		WHERE id = $1
		RETURNING id, keycloak_subject, email, display_name, active, last_login, created_at`,
		userID, req.Active, req.DisplayName).
		Scan(&u.ID, &u.KeycloakSubject, &u.Email, &u.DisplayName, &u.Active, &u.LastLogin, &u.CreatedAt)
	if err != nil {
		dbError(w, err, "failed to update user")
		return
	}
	u.Roles = []string{}

	eventType := "user.updated"
	if req.Active != nil {
		if *req.Active {
			eventType = "user.activated"
		} else {
			eventType = "user.deactivated"
		}
	}

	h.record(r.Context(), audit.Event{
		Type:        eventType,
		ActorID:     actor.ID,
		Resource:    "user",
		ResourceID:  strconv.FormatInt(userID, 10),
		Description: "User " + u.Email + " updated",
		Metadata:    map[string]any{"active": req.Active, "user_id": userID},
	})

	// Deactivation stops them making a request. It does nothing about the device
	// passwords they were already shown, and the device would go on accepting
	// them, so an ex-manager who wrote the passwords down still holds working
	// credentials for every machine they could reach. That is the same hole
	// item 3.3 closed for support-group membership, and it is closed the same
	// way.
	if deactivating {
		h.rotateForAccessChange(r.Context(), actor.ID, reachable,
			"user deactivated", "user", strconv.FormatInt(userID, 10))
	}

	httpx.WriteJSON(w, http.StatusOK, u)
}

// HandleDeleteUser serves DELETE /api/v1/users/{id}.
//
// It removes the Keycloak account as well as the local row, and refuses when it
// cannot. A local-only delete is not a removal: the person signs in again, the
// JWT middleware provisions a fresh row, and identity.provisionUser grants it
// the default Technician role -- so "removed" would mean "demoted to technician
// until they next sign in".
func (h *Handler) HandleDeleteUser(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.admin(w, r)
	if !ok {
		return
	}
	userID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	if userID == actor.ID {
		writeJSONError(w, http.StatusBadRequest, "you cannot remove your own account")
		return
	}

	var email, subject string
	err = h.db.QueryRow(r.Context(),
		`SELECT email, keycloak_subject FROM users WHERE id = $1`, userID).Scan(&email, &subject)
	if err != nil {
		dbError(w, err, "failed to read the user")
		return
	}

	last, err := h.isLastAdministrator(r.Context(), userID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to check administrator cover")
		return
	}
	if last {
		writeJSONError(w, http.StatusBadRequest,
			"that is the only active administrator; grant Administrator to somebody else first")
		return
	}

	reachable, err := h.devicesReachableByUser(r.Context(), userID)
	if err != nil {
		dbError(w, err, "failed to read the devices this user can reach")
		return
	}

	// A machine identity's Keycloak account belongs to the client it hangs off,
	// not to this route: removing it here would break the client rather than
	// the integration's access, which is what revoking its roles is for.
	machine := identity.IsServiceAccountEmail(email)
	if !machine {
		if h.accounts == nil {
			writeJSONError(w, http.StatusServiceUnavailable,
				"account removal is not configured: set KEYCLOAK_URL, KEYCLOAK_REALM, KEYCLOAK_CLIENT_API and KEYCLOAK_CLIENT_SECRET, and grant that service account the realm-management role manage-users. Deactivating the user withdraws access in the meantime")
			return
		}
		if err := h.accounts.DeleteAccount(r.Context(), subject); err != nil {
			log.Error().Err(err).Str("subject", subject).Msg("failed to remove the identity provider account")
			writeJSONError(w, http.StatusBadGateway,
				"the identity provider refused to remove the account, so the user was left in place")
			return
		}
	}

	// audit_events deliberately outlives the rows it names (item 3.5): migration
	// 000006 dropped its foreign keys to make it append-only, so "who did this"
	// survives the removal of who. Everything else about a user cascades.
	if _, err := h.db.Exec(r.Context(), `DELETE FROM users WHERE id = $1`, userID); err != nil {
		dbError(w, err, "failed to remove the user")
		return
	}

	h.record(r.Context(), audit.Event{
		Type:        "user.deleted",
		ActorID:     actor.ID,
		Resource:    "user",
		ResourceID:  strconv.FormatInt(userID, 10),
		Description: "User " + email + " removed",
		Metadata: map[string]any{
			"email":                    email,
			"user_id":                  userID,
			"identity_account_removed": !machine,
		},
	})

	rotated := h.rotateForAccessChange(r.Context(), actor.ID, reachable,
		"user removed", "user", strconv.FormatInt(userID, 10))

	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"success":                  true,
		"identity_account_removed": !machine,
		"passwords_rotated":        rotated,
		"rotation_in_force":        "at each device's next heartbeat",
	})
}

// isLastAdministrator reports whether this user is the only active
// Administrator left, which is the state that turns a routine removal into a
// deployment nobody can administer.
//
// It answers false when the user is not an active administrator at all, so a
// deployment that already has none is not stopped from removing anybody else.
func (h *Handler) isLastAdministrator(ctx context.Context, userID int64) (bool, error) {
	const holders = `
		SELECT 1
		FROM user_roles ur
		JOIN roles r ON r.id = ur.role_id
		JOIN users u ON u.id = ur.user_id
		WHERE r.name = $2 AND u.active`

	var last bool
	err := h.db.QueryRow(ctx, `
		SELECT EXISTS (`+holders+` AND u.id = $1)
		   AND NOT EXISTS (`+holders+` AND u.id <> $1)`,
		userID, identity.RoleAdministrator).Scan(&last)
	return last, err
}

// HandleGrantRole serves POST /api/v1/users/{id}/roles.
func (h *Handler) HandleGrantRole(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.admin(w, r)
	if !ok {
		return
	}
	userID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid user id")
		return
	}

	var req struct {
		Role string `json:"role"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	role := strings.TrimSpace(req.Role)
	if role == "" {
		writeJSONError(w, http.StatusBadRequest, "role is required")
		return
	}

	// Resolving the role by name in the insert means an unknown role inserts
	// nothing rather than creating one by accident.
	tag, err := h.db.Exec(r.Context(), `
		INSERT INTO user_roles (user_id, role_id)
		SELECT $1, r.id FROM roles r WHERE r.name = $2
		ON CONFLICT DO NOTHING`, userID, role)
	if err != nil {
		dbError(w, err, "failed to grant role")
		return
	}
	if tag.RowsAffected() == 0 {
		// Either the role does not exist or the user already has it. Tell the
		// caller which, because they mean different things.
		var exists bool
		if err := h.db.QueryRow(r.Context(),
			`SELECT EXISTS (SELECT 1 FROM roles WHERE name = $1)`, role).Scan(&exists); err == nil && !exists {
			writeJSONError(w, http.StatusBadRequest, "unknown role: "+role)
			return
		}
	}

	h.record(r.Context(), audit.Event{
		Type:        "user.role_granted",
		ActorID:     actor.ID,
		Resource:    "user",
		ResourceID:  strconv.FormatInt(userID, 10),
		Description: "Role " + role + " granted",
		Metadata:    map[string]any{"role": role, "user_id": userID},
	})

	httpx.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}

// HandleRevokeRole serves DELETE /api/v1/users/{id}/roles/{role}.
func (h *Handler) HandleRevokeRole(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.admin(w, r)
	if !ok {
		return
	}
	userID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	role := strings.TrimSpace(r.PathValue("role"))
	if role == "" {
		writeJSONError(w, http.StatusBadRequest, "role is required")
		return
	}

	// Removing your own admin role is the same class of mistake as deactivating
	// yourself, and it is not recoverable through the portal.
	//
	// The names come from identity rather than being written here. They used to
	// be the literals "admin" and "manager"; the seeded roles are
	// "Administrator" and "Support Manager", so the guard matched nothing and an
	// administrator could revoke their own Administrator role with no way back.
	// That is the same defect item 6.9 found in the portal's hardcoded role
	// list: a role name written by hand that does not match the database.
	if userID == actor.ID &&
		(role == identity.RoleAdministrator || role == identity.RoleSupportManager) {
		writeJSONError(w, http.StatusBadRequest, "you cannot revoke your own administrative role")
		return
	}

	// The self guard is not enough on its own: two administrators can revoke
	// each other, and one administrator can revoke another and then be
	// deactivated. This is the invariant the deployment actually needs.
	if role == identity.RoleAdministrator {
		last, err := h.isLastAdministrator(r.Context(), userID)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to check administrator cover")
			return
		}
		if last {
			writeJSONError(w, http.StatusBadRequest,
				"that is the only active administrator; grant Administrator to somebody else first")
			return
		}
	}

	tag, err := h.db.Exec(r.Context(), `
		DELETE FROM user_roles
		WHERE user_id = $1 AND role_id = (SELECT id FROM roles WHERE name = $2)`, userID, role)
	if err != nil {
		dbError(w, err, "failed to revoke role")
		return
	}
	if tag.RowsAffected() == 0 {
		writeJSONError(w, http.StatusNotFound, "user does not have that role")
		return
	}

	h.record(r.Context(), audit.Event{
		Type:        "user.role_revoked",
		ActorID:     actor.ID,
		Resource:    "user",
		ResourceID:  strconv.FormatInt(userID, 10),
		Description: "Role " + role + " revoked",
		Metadata:    map[string]any{"role": role, "user_id": userID},
	})

	httpx.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}
