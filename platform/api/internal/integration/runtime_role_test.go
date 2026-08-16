package integration

import (
	"context"
	"testing"
)

// The non-owning runtime role, migration 000008.
//
// These assert the privilege system rather than the trigger. The trigger from
// 000006 is still there and still refuses an UPDATE or DELETE on audit_events;
// what it could never do is stop the owner turning it off, or stop a TRUNCATE,
// which does not fire row triggers at all. The grants below are what closes
// those, and a grant is exactly the kind of thing that is easy to write in a
// migration and never check.

// privilege reports whether odv_app holds one privilege on one table.
func privilege(t *testing.T, f *fixture, table, priv string) bool {
	t.Helper()

	var has bool
	if err := f.db.QueryRow(context.Background(),
		`SELECT has_table_privilege('odv_app', $1, $2)`, table, priv).Scan(&has); err != nil {
		t.Fatalf("failed to check %s on %s: %v", priv, table, err)
	}
	return has
}

func TestRuntimeRoleExistsAndDoesNotOwnTheSchema(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	var exists bool
	if err := f.db.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'odv_app')`).Scan(&exists); err != nil {
		t.Fatalf("failed to look for the runtime role: %v", err)
	}
	if !exists {
		t.Fatal("migration 000008 did not create odv_app, so every assertion below would pass vacuously")
	}

	// A superuser passes has_table_privilege for everything, which would make
	// the refusals below meaningless.
	var superuser, owner bool
	if err := f.db.QueryRow(ctx,
		`SELECT rolsuper FROM pg_roles WHERE rolname = 'odv_app'`).Scan(&superuser); err != nil {
		t.Fatalf("failed to read the role: %v", err)
	}
	if superuser {
		t.Error("odv_app is a superuser, so none of its restrictions apply")
	}

	if err := f.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_tables
			WHERE schemaname = 'public' AND tableowner = 'odv_app')
	`).Scan(&owner); err != nil {
		t.Fatalf("failed to check table ownership: %v", err)
	}
	if owner {
		t.Error("odv_app owns a table, and an owner can disable that table's triggers")
	}
}

func TestRuntimeRoleCanOnlyAppendToTheAuditLog(t *testing.T) {
	f := newFixture(t)

	if !privilege(t, f, "audit_events", "SELECT") {
		t.Error("odv_app cannot read audit_events, so the audit screens would 403 at the database")
	}
	if !privilege(t, f, "audit_events", "INSERT") {
		t.Error("odv_app cannot write audit_events, so nothing would be recorded")
	}

	// The three that matter. Each is a way to change the past.
	for _, priv := range []string{"UPDATE", "DELETE", "TRUNCATE"} {
		if privilege(t, f, "audit_events", priv) {
			t.Errorf("odv_app holds %s on audit_events; the audit log is not append-only against the process an attacker reaches", priv)
		}
	}
}

// The counterpart, and the reason the test above is not simply "revoke
// everything": the API has to be able to do its job. A device that cannot be
// updated is a fleet that cannot report.
func TestRuntimeRoleCanStillServeRequests(t *testing.T) {
	f := newFixture(t)

	for _, table := range []string{
		"devices", "device_credentials", "device_passwords", "device_strategies",
		"customers", "users", "user_roles", "client_sessions", "connection_sessions",
		"ab_peers", "enrollment_tokens", "device_observations",
	} {
		for _, priv := range []string{"SELECT", "INSERT", "UPDATE", "DELETE"} {
			if !privilege(t, f, table, priv) {
				t.Errorf("odv_app cannot %s %s, which the API does at request time", priv, table)
			}
		}
		// Nothing the API does needs to empty a table, and TRUNCATE bypasses
		// both foreign keys and row triggers.
		if privilege(t, f, table, "TRUNCATE") {
			t.Errorf("odv_app holds TRUNCATE on %s", table)
		}
	}
}

// schema_migrations is the migration runner's bookkeeping. An API that could
// write to it could convince the next deployment that a migration it never ran
// had already been applied.
func TestRuntimeRoleCannotTouchTheMigrationLedger(t *testing.T) {
	f := newFixture(t)

	for _, priv := range []string{"SELECT", "INSERT", "UPDATE", "DELETE"} {
		if privilege(t, f, "schema_migrations", priv) {
			t.Errorf("odv_app holds %s on schema_migrations", priv)
		}
	}
}
