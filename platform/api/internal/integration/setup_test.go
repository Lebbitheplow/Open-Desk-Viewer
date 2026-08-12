// Package integration holds the tests that need a real PostgreSQL.
//
// They are skipped unless ODV_TEST_DB=1, so `go test ./...` stays runnable
// without a database. To run them:
//
//	docker run -d --name odv-test -e POSTGRES_PASSWORD=odvtest -e POSTGRES_USER=odv \
//	    -e POSTGRES_DB=odv -p 55432:5432 postgres:16-alpine
//	psql < platform/migrations/00001_initial_schema.up.sql
//	psql < platform/migrations/00002_address_book.up.sql
//	ODV_TEST_DB=1 go test ./internal/integration/
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

	"github.com/OpenDeskViewer/platform/api/internal/postgres"
	"github.com/google/uuid"
)

// fixture is the seeded world the tests assert against.
type fixture struct {
	db *postgres.Pool

	adminID int64
	tech1ID int64
	tech2ID int64

	group1 uuid.UUID // support group tech1 belongs to
	group2 uuid.UUID // support group tech2 belongs to

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

	ctx := context.Background()
	db, err := postgres.New(ctx,
		envOr("ODV_TEST_PG_HOST", "127.0.0.1"), port,
		envOr("ODV_TEST_PG_DB", "odv"),
		envOr("ODV_TEST_PG_USER", "odv"),
		envOr("ODV_TEST_PG_PASSWORD", "odvtest"),
	)
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
		         locations, customers, users
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
	`).Scan(&customer); err != nil {
		t.Fatalf("failed to create customer: %v", err)
	}

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
