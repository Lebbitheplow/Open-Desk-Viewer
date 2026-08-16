// Package integration holds the tests that need a real PostgreSQL.
//
// They are skipped unless ODV_TEST_DB=1, so `go test ./...` stays runnable
// without a database. To run them:
//
//	docker run -d --name odv-test -e POSTGRES_PASSWORD=odvtest -e POSTGRES_USER=odv \
//	    -e POSTGRES_DB=odv -p 55432:5432 postgres:16-alpine
//	ODV_TEST_DB=1 go test ./internal/integration/
//
// The schema is applied by the same embedded migration runner the API uses at
// startup, so these tests fail if a migration does not apply cleanly.
//
// These exist because the queries they cover compiled fine while being wrong:
// columns that do not exist, a uuid joined to a bigint, and placeholders that
// filtered by page size. Only a real server catches that class of bug.
package integration

import (
	"context"
	"os"
	"strconv"
	"testing"

	"github.com/OpenDeskViewer/platform/api/internal/devicepw"
	"github.com/OpenDeskViewer/platform/api/internal/migrations"
	"github.com/OpenDeskViewer/platform/api/internal/postgres"
	"github.com/google/uuid"
)

// testDevicePasswordKey is a fixed 32-byte AES key, base64 encoded. Fixed
// rather than random so a failing test can be reproduced, and confined to this
// package: config.ValidateAPI requires a real one, and a deployment reusing
// this value would have every device password readable by anyone with the
// source.
const testDevicePasswordKey = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="

// newDevicePasswords builds the password service the way cmd/api builds it.
func newDevicePasswords(t *testing.T, f *fixture) *devicepw.Service {
	t.Helper()

	key, err := devicepw.ParseKey(testDevicePasswordKey)
	if err != nil {
		t.Fatalf("test device password key does not parse: %v", err)
	}
	svc, err := devicepw.New(f.db, key)
	if err != nil {
		t.Fatalf("failed to build the device password service: %v", err)
	}
	return svc
}

// testPrinter routes migration progress into the test log.
type testPrinter struct{ t *testing.T }

func (p testPrinter) Printf(format string, v ...any) { p.t.Logf(format, v...) }

// fixture is the seeded world the tests assert against.
type fixture struct {
	db *postgres.Pool

	adminID int64
	tech1ID int64
	tech2ID int64

	group1 uuid.UUID // support group tech1 belongs to
	group2 uuid.UUID // support group tech2 belongs to

	// The customer and device groups the seeded devices belong to. Enrollment
	// tokens name both, so the device-facing tests need them.
	customer     uuid.UUID
	deviceGroup1 uuid.UUID
	deviceGroup2 uuid.UUID

	// device1..device3 are in group1, device4 is in group2, and discovered is in
	// group1 but not yet claimed.
	device1    uuid.UUID
	device2    uuid.UUID
	device3    uuid.UUID
	device4    uuid.UUID
	discovered uuid.UUID
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// newFixture connects, wipes and reseeds. Each test gets the same known world.
func newFixture(t *testing.T) *fixture {
	t.Helper()

	if os.Getenv("ODV_TEST_DB") != "1" {
		t.Skip("set ODV_TEST_DB=1 to run the database integration tests")
	}

	port, err := strconv.Atoi(envOr("ODV_TEST_PG_PORT", "55432"))
	if err != nil {
		t.Fatalf("invalid ODV_TEST_PG_PORT: %v", err)
	}

	host := envOr("ODV_TEST_PG_HOST", "127.0.0.1")
	dbName := envOr("ODV_TEST_PG_DB", "odv")
	user := envOr("ODV_TEST_PG_USER", "odv")
	password := envOr("ODV_TEST_PG_PASSWORD", "odvtest")

	// Apply the schema the same way the API does, so a migration that does not
	// apply cleanly fails here rather than in production.
	dsn := postgres.DSN(host, port, dbName, user, password, "disable")
	if err := migrations.Run(dsn, testPrinter{t}); err != nil {
		t.Fatalf("failed to apply migrations: %v", err)
	}

	ctx := context.Background()
	db, err := postgres.New(ctx, host, port, dbName, user, password, "disable")
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	t.Cleanup(db.Close)

	f := &fixture{db: db}
	f.reset(t)
	f.seed(t)
	return f
}

func (f *fixture) reset(t *testing.T) {
	t.Helper()

	_, err := f.db.Exec(context.Background(), `
		TRUNCATE ab_tags, ab_peers, ab_profiles,
		         device_group_members, support_group_device_groups,
		         user_support_groups, user_roles,
		         devices, device_groups, support_groups,
	         enrollment_tokens, device_credentials, device_observations,
	         device_disconnect_requests, device_strategies, device_passwords,
	         device_connectivity_events, notification_deliveries, notification_targets,
		         locations, customers, users,
		         -- Cascaded from users, but listed so the intent survives a
		         -- schema change that drops the foreign key.
		         client_sessions,
		         -- Cascaded only for rows that already name a user; a sign-in
		         -- that has not reached its callback yet has a NULL user_id and
		         -- would survive into the next test.
		         oidc_auth_requests,
		         -- Not reachable by CASCADE since migration 000006 dropped
		         -- its foreign keys to make it append-only. TRUNCATE is the one
		         -- way to clear it: the append-only trigger is FOR EACH ROW on
		         -- DELETE, and TRUNCATE does not fire row triggers.
		         audit_events
		RESTART IDENTITY CASCADE
	`)
	if err != nil {
		t.Fatalf("failed to reset the database: %v", err)
	}
}

func (f *fixture) mustExec(t *testing.T, sql string, args ...interface{}) {
	t.Helper()
	if _, err := f.db.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("seed failed: %v\nSQL: %s", err, sql)
	}
}

// newUser inserts a user and grants it a role.
func (f *fixture) newUser(t *testing.T, subject, email, role string) int64 {
	t.Helper()

	var id int64
	err := f.db.QueryRow(context.Background(), `
		INSERT INTO users (keycloak_subject, email, display_name)
		VALUES ($1, $2, $2) RETURNING id
	`, subject, email).Scan(&id)
	if err != nil {
		t.Fatalf("failed to create user %s: %v", email, err)
	}

	f.mustExec(t, `
		INSERT INTO user_roles (user_id, role_id)
		SELECT $1, r.id FROM roles r WHERE r.name = $2
	`, id, role)

	return id
}

// newDevice inserts a device and puts it in a device group.
func (f *fixture) newDevice(t *testing.T, rustdeskID, name, state string, group uuid.UUID, customer uuid.UUID) uuid.UUID {
	t.Helper()

	var id uuid.UUID
	err := f.db.QueryRow(context.Background(), `
		INSERT INTO devices (rustdesk_id, name, uuid, state, os, hostname, customer_id)
		VALUES ($1, $2, $1, $3, 'Windows 11', $2, $4)
		RETURNING id
	`, rustdeskID, name, state, customer).Scan(&id)
	if err != nil {
		t.Fatalf("failed to create device %s: %v", name, err)
	}

	f.mustExec(t, `
		INSERT INTO device_group_members (device_id, device_group_id) VALUES ($1, $2)
	`, id, group)

	return id
}

func (f *fixture) seed(t *testing.T) {
	t.Helper()
	ctx := context.Background()

	f.adminID = f.newUser(t, "sub-admin", "admin@example.com", "Administrator")
	f.tech1ID = f.newUser(t, "sub-tech1", "tech1@example.com", "Technician")
	f.tech2ID = f.newUser(t, "sub-tech2", "tech2@example.com", "Technician")

	var customer uuid.UUID
	if err := f.db.QueryRow(ctx, `
		INSERT INTO customers (name, code) VALUES ('Acme', 'ACME') RETURNING id
	`).Scan(&f.customer); err != nil {
		t.Fatalf("failed to create customer: %v", err)
	}

	customer = f.customer

	var deviceGroup1, deviceGroup2 uuid.UUID
	if err := f.db.QueryRow(ctx, `
		INSERT INTO device_groups (name) VALUES ('Acme HQ') RETURNING id
	`).Scan(&deviceGroup1); err != nil {
		t.Fatalf("failed to create device group: %v", err)
	}
	if err := f.db.QueryRow(ctx, `
		INSERT INTO device_groups (name) VALUES ('Acme Branch') RETURNING id
	`).Scan(&deviceGroup2); err != nil {
		t.Fatalf("failed to create device group: %v", err)
	}
	f.deviceGroup1, f.deviceGroup2 = deviceGroup1, deviceGroup2

	if err := f.db.QueryRow(ctx, `
		INSERT INTO support_groups (name) VALUES ('Team One') RETURNING id
	`).Scan(&f.group1); err != nil {
		t.Fatalf("failed to create support group: %v", err)
	}
	if err := f.db.QueryRow(ctx, `
		INSERT INTO support_groups (name) VALUES ('Team Two') RETURNING id
	`).Scan(&f.group2); err != nil {
		t.Fatalf("failed to create support group: %v", err)
	}

	f.mustExec(t, `
		INSERT INTO support_group_device_groups (support_group_id, device_group_id)
		VALUES ($1, $2), ($3, $4)
	`, f.group1, deviceGroup1, f.group2, deviceGroup2)

	f.mustExec(t, `
		INSERT INTO user_support_groups (user_id, support_group_id)
		VALUES ($1, $2), ($3, $4)
	`, f.tech1ID, f.group1, f.tech2ID, f.group2)

	f.device1 = f.newDevice(t, "100000001", "acme-hq-01", "ACTIVE", deviceGroup1, customer)
	f.device2 = f.newDevice(t, "100000002", "acme-hq-02", "ACTIVE", deviceGroup1, customer)
	f.device3 = f.newDevice(t, "100000003", "acme-hq-03", "ACTIVE", deviceGroup1, customer)
	f.device4 = f.newDevice(t, "100000004", "acme-br-01", "ACTIVE", deviceGroup2, customer)
	f.discovered = f.newDevice(t, "100000005", "unclaimed-01", "DISCOVERED", deviceGroup1, customer)
}

// contains reports whether the id is present.
func contains(ids []uuid.UUID, want uuid.UUID) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}
