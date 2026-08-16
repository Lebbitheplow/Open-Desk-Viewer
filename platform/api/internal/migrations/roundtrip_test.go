// Migration round-trip tests.
//
// The up files are exercised on every integration run, because the fixture
// applies them. The down files are exercised nowhere, which means they are
// assertions nobody has checked: a rollback is the thing you reach for when a
// deployment is already going wrong, and finding out then that a down file is
// broken is finding out at the worst possible moment.
//
// These live in package migrations rather than package integration for two
// reasons. They need normaliseURL, which is unexported; and they must not share
// a database with the integration suite, which runs as a separate package and so
// runs concurrently under `go test ./...`. Dropping every table while those tests
// are mid-run would look like a hundred unrelated failures. So the round trip
// gets a scratch database of its own.
package migrations

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/OpenDeskViewer/platform/api/internal/postgres"
	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	pgxv5 "github.com/jackc/pgx/v5"
)

// scratchDB is the database the round trip is run in. It is dropped and
// recreated per run, so a previous failure cannot leave state that makes the
// next run pass.
const scratchDB = "odv_migration_roundtrip"

// ---------------------------------------------------------------------------
// The file-level checks, which need no database and so run everywhere.
// ---------------------------------------------------------------------------

var migrationName = regexp.MustCompile(`^(\d{6})_([a-z0-9_]+)\.(up|down)\.sql$`)

// Every up file needs a down file, the versions have to be contiguous from 1,
// and no version may appear twice. golang-migrate silently ignores a file it
// cannot parse, so a typo in a name is a migration that never runs.
func TestMigrationFilesArePairedAndContiguous(t *testing.T) {
	entries, err := files.ReadDir(".")
	if err != nil {
		t.Fatalf("failed to read the embedded migrations: %v", err)
	}

	type pair struct{ up, down, name string }
	byVersion := map[int]*pair{}

	for _, entry := range entries {
		match := migrationName.FindStringSubmatch(entry.Name())
		if match == nil {
			t.Errorf("%s does not match NNNNNN_name.(up|down).sql, so golang-migrate will ignore it", entry.Name())
			continue
		}

		version, err := strconv.Atoi(match[1])
		if err != nil {
			t.Fatalf("unparseable version in %s: %v", entry.Name(), err)
		}
		if byVersion[version] == nil {
			byVersion[version] = &pair{name: match[2]}
		}
		p := byVersion[version]

		if p.name != match[2] {
			t.Errorf("version %06d has two different names, %s and %s", version, p.name, match[2])
		}
		if match[3] == "up" {
			if p.up != "" {
				t.Errorf("version %06d has two up files", version)
			}
			p.up = entry.Name()
		} else {
			if p.down != "" {
				t.Errorf("version %06d has two down files", version)
			}
			p.down = entry.Name()
		}
	}

	if len(byVersion) == 0 {
		t.Fatal("no migrations are embedded at all")
	}

	for version := 1; version <= len(byVersion); version++ {
		p := byVersion[version]
		if p == nil {
			t.Errorf("version %06d is missing; golang-migrate stops at the first gap", version)
			continue
		}
		if p.up == "" {
			t.Errorf("version %06d has a down file but no up file", version)
		}
		if p.down == "" {
			t.Errorf("version %06d has no down file, so it cannot be rolled back", version)
		}
	}
	for version := range byVersion {
		if version < 1 || version > len(byVersion) {
			t.Errorf("version %06d is outside the contiguous range 1..%d", version, len(byVersion))
		}
	}
}

// A down file that only drops a table leaves the enum types and functions its up
// file created, and the next up then fails on "type already exists". Reading for
// the shape rather than running it catches this without a database.
func TestDownFilesUndoTheirOwnCreations(t *testing.T) {
	entries, err := files.ReadDir(".")
	if err != nil {
		t.Fatalf("failed to read the embedded migrations: %v", err)
	}

	// The object kinds whose creation has to be undone by name. An index inside
	// a CREATE TABLE goes with the table, so only standalone CREATE INDEX would
	// matter, and dropping the table takes those too.
	kinds := []struct {
		create *regexp.Regexp
		drop   string
	}{
		{regexp.MustCompile(`(?i)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([a-z0-9_]+)`), "table"},
		{regexp.MustCompile(`(?i)CREATE\s+TYPE\s+([a-z0-9_]+)`), "type"},
		{regexp.MustCompile(`(?i)CREATE\s+(?:OR\s+REPLACE\s+)?FUNCTION\s+([a-z0-9_]+)`), "function"},
		{regexp.MustCompile(`(?i)CREATE\s+TRIGGER\s+([a-z0-9_]+)`), "trigger"},
	}

	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".up.sql") {
			continue
		}
		downName := strings.TrimSuffix(entry.Name(), ".up.sql") + ".down.sql"

		up, err := files.ReadFile(entry.Name())
		if err != nil {
			t.Fatalf("failed to read %s: %v", entry.Name(), err)
		}
		down, err := files.ReadFile(downName)
		if err != nil {
			t.Errorf("failed to read %s: %v", downName, err)
			continue
		}
		upSQL, downSQL := stripComments(string(up)), strings.ToLower(stripComments(string(down)))

		for _, kind := range kinds {
			for _, match := range kind.create.FindAllStringSubmatch(upSQL, -1) {
				name := strings.ToLower(match[1])
				if !strings.Contains(downSQL, name) {
					t.Errorf("%s creates the %s %q and %s never mentions it", entry.Name(), kind.drop, name, downName)
				}
			}
		}
	}
}

// stripComments removes -- line comments so a name that only appears in prose
// does not count as a drop.
func stripComments(sql string) string {
	var b strings.Builder
	for _, line := range strings.Split(sql, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// The round trip itself, against a real database.
// ---------------------------------------------------------------------------

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// scratchDSN creates an empty database for this run and returns its DSN. The
// database is dropped when the test finishes, whether or not it passed: leaving
// it behind would make the next run's DROP the thing that fails.
func scratchDSN(t *testing.T) string {
	t.Helper()

	if os.Getenv("ODV_TEST_DB") != "1" {
		t.Skip("set ODV_TEST_DB=1 to run the migration round trip")
	}

	port, err := strconv.Atoi(envOr("ODV_TEST_PG_PORT", "55432"))
	if err != nil {
		t.Fatalf("invalid ODV_TEST_PG_PORT: %v", err)
	}
	host := envOr("ODV_TEST_PG_HOST", "127.0.0.1")
	user := envOr("ODV_TEST_PG_USER", "odv")
	password := envOr("ODV_TEST_PG_PASSWORD", "odvtest")
	admin := envOr("ODV_TEST_PG_DB", "odv")

	ctx := context.Background()
	conn, err := pgxv5.Connect(ctx, postgres.DSN(host, port, admin, user, password, "disable"))
	if err != nil {
		t.Fatalf("failed to connect to %s: %v", admin, err)
	}
	defer conn.Close(ctx)

	// FORCE terminates any connection left over from an interrupted run.
	if _, err := conn.Exec(ctx, `DROP DATABASE IF EXISTS `+scratchDB+` WITH (FORCE)`); err != nil {
		t.Fatalf("failed to drop the scratch database: %v", err)
	}
	if _, err := conn.Exec(ctx, `CREATE DATABASE `+scratchDB); err != nil {
		t.Fatalf("failed to create the scratch database: %v", err)
	}

	t.Cleanup(func() {
		ctx := context.Background()
		conn, err := pgxv5.Connect(ctx, postgres.DSN(host, port, admin, user, password, "disable"))
		if err != nil {
			t.Logf("failed to connect to drop the scratch database: %v", err)
			return
		}
		defer conn.Close(ctx)
		if _, err := conn.Exec(ctx, `DROP DATABASE IF EXISTS `+scratchDB+` WITH (FORCE)`); err != nil {
			t.Logf("failed to drop the scratch database: %v", err)
		}
	})

	return postgres.DSN(host, port, scratchDB, user, password, "disable")
}

func newMigrator(t *testing.T, dsn string) *migrate.Migrate {
	t.Helper()

	src, err := iofs.New(files, ".")
	if err != nil {
		t.Fatalf("failed to open the embedded migrations: %v", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, normaliseURL(dsn))
	if err != nil {
		t.Fatalf("failed to create the migrator: %v", err)
	}
	t.Cleanup(func() {
		if srcErr, dbErr := m.Close(); srcErr != nil || dbErr != nil {
			t.Logf("migrator close: source=%v database=%v", srcErr, dbErr)
		}
	})
	return m
}

// The headline round trip: up to the top, all the way back down, and up again.
//
// The second up is the part that matters. A down file that leaves an enum type
// or a function behind lets the first up succeed and the second fail, and the
// only deployment that ever runs a second up is one that has just rolled back.
func TestMigrationsRoundTrip(t *testing.T) {
	dsn := scratchDSN(t)
	m := newMigrator(t, dsn)

	if err := m.Up(); err != nil {
		t.Fatalf("first up failed: %v", err)
	}
	top, dirty, err := m.Version()
	if err != nil {
		t.Fatalf("failed to read the version: %v", err)
	}
	if dirty {
		t.Fatal("the schema is dirty after a clean up")
	}

	conn := connect(t, dsn)
	atTop := fingerprint(t, conn)

	if err := m.Down(); err != nil {
		t.Fatalf("down failed: %v", err)
	}

	// Nothing of ours may survive a full rollback. schema_migrations is
	// golang-migrate's own bookkeeping and stays by design.
	leftovers := fingerprint(t, conn)
	for _, line := range leftovers {
		if strings.Contains(line, "schema_migrations") {
			continue
		}
		t.Errorf("a full rollback left %s behind", line)
	}

	if err := m.Up(); err != nil {
		t.Fatalf("up after a full down failed, so the rollback is not usable: %v", err)
	}
	again, _, err := m.Version()
	if err != nil {
		t.Fatalf("failed to read the version after the second up: %v", err)
	}
	if again != top {
		t.Errorf("the second up reached version %d, the first reached %d", again, top)
	}

	// And it has to rebuild the same schema, not merely some schema.
	diff(t, "after up, down, up", atTop, fingerprint(t, conn))
}

// Each migration has to be reversible on its own, not only as part of a full
// rollback. Rolling back one release is the common case; rolling back to an
// empty database is not a thing anyone does deliberately.
//
// This walks up one version at a time recording the schema at each, then walks
// back down comparing against what was recorded. A difference means that down
// file does not undo its up file, and the operator who rolls back gets a schema
// that is not the one that release was tested against.
func TestEachMigrationIsIndividuallyReversible(t *testing.T) {
	dsn := scratchDSN(t)
	m := newMigrator(t, dsn)
	conn := connect(t, dsn)

	// Climb, recording the schema at every version including the empty one.
	byVersion := map[uint][]string{0: nil}
	var top uint
	for {
		if err := m.Steps(1); err != nil {
			if err == migrate.ErrNoChange || strings.Contains(err.Error(), "file does not exist") {
				break
			}
			t.Fatalf("stepping up from %d failed: %v", top, err)
		}
		version, dirty, err := m.Version()
		if err != nil {
			t.Fatalf("failed to read the version: %v", err)
		}
		if dirty {
			t.Fatalf("version %d is dirty after applying it", version)
		}
		top = version
		byVersion[version] = fingerprint(t, conn)
	}
	if top == 0 {
		t.Fatal("no migration applied, so this test asserted nothing")
	}

	// Descend, comparing.
	for version := top; version >= 1; version-- {
		if err := m.Steps(-1); err != nil {
			t.Fatalf("stepping down from %d failed: %v", version, err)
		}

		want := byVersion[version-1]
		got := fingerprint(t, conn)
		if version == 1 {
			// Below version 1 there is nothing but golang-migrate's own table.
			for _, line := range got {
				if !strings.Contains(line, "schema_migrations") {
					t.Errorf("rolling back 000001 left %s behind", line)
				}
			}
			continue
		}
		diff(t, fmt.Sprintf("rolling %06d back to %06d", version, version-1), want, got)
	}
}

// knownRollbackDifferences are the schema differences a rollback is allowed to
// leave, each one deliberate and argued for in the down file that causes it.
//
// This list is the point of the test, not a way around it. A difference that is
// not here fails; a difference that is here had to be written down and justified
// before it could pass.
var knownRollbackDifferences = []string{
	// 000006 drops four ON DELETE SET NULL foreign keys out of audit_events so
	// the append-only trigger cannot be defeated by a cascade. Its down file
	// restores them NOT VALID, because rows written while they were absent may
	// reference entities that have since been deleted and a validating ADD
	// CONSTRAINT would fail against real data. The constraint is therefore
	// enforced for new rows and unverified for old ones, which is the only
	// rollback that can be relied on. See 3.5 in plans/audit-remediation.md.
	"constraint audit_events audit_events_user_id_fkey",
	"constraint audit_events audit_events_device_id_fkey",
	"constraint audit_events audit_events_customer_id_fkey",
	"constraint audit_events audit_events_support_group_id_fkey",
}

// diff compares two schema fingerprints, allowing the differences named above.
func diff(t *testing.T, what string, want, got []string) {
	t.Helper()

	allowed := func(line string) bool {
		for _, known := range knownRollbackDifferences {
			if strings.HasPrefix(line, known) {
				return true
			}
		}
		return false
	}

	inGot := map[string]bool{}
	for _, line := range got {
		inGot[line] = true
	}
	inWant := map[string]bool{}
	for _, line := range want {
		inWant[line] = true
	}

	for _, line := range want {
		if !inGot[line] && !allowed(line) {
			t.Errorf("%s: lost %s", what, line)
		}
	}
	for _, line := range got {
		if !inWant[line] && !allowed(line) {
			t.Errorf("%s: gained %s", what, line)
		}
	}
}

func connect(t *testing.T, dsn string) *pgxv5.Conn {
	t.Helper()
	conn, err := pgxv5.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatalf("failed to connect to the scratch database: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

// fingerprint describes the public schema as a sorted list of lines, so two
// states can be compared and the difference named rather than reported as
// "the schemas differ".
//
// It covers what a migration can get wrong: columns and their types, constraints
// including whether they are validated, indexes, enum labels, triggers and
// functions. Comparing only table names would pass a down file that recreates a
// table with the wrong column types.
func fingerprint(t *testing.T, conn *pgxv5.Conn) []string {
	t.Helper()
	ctx := context.Background()

	queries := []string{
		`SELECT format('column %s.%s %s %s %s',
		               c.table_name, c.column_name, c.data_type, c.is_nullable,
		               coalesce(c.column_default, '-'))
		 FROM information_schema.columns c
		 JOIN information_schema.tables t
		   ON t.table_schema = c.table_schema AND t.table_name = c.table_name
		 WHERE c.table_schema = 'public' AND t.table_type = 'BASE TABLE'`,

		// convalidated is included deliberately: it is exactly what a NOT VALID
		// restoration changes, and leaving it out would hide the one difference
		// this schema is known to have.
		`SELECT format('constraint %s %s %s validated=%s',
		               con.conrelid::regclass, con.conname, con.contype, con.convalidated)
		 FROM pg_constraint con
		 JOIN pg_namespace n ON n.oid = con.connamespace
		 WHERE n.nspname = 'public'`,

		`SELECT format('index %s %s', tablename, indexdef)
		 FROM pg_indexes WHERE schemaname = 'public'`,

		`SELECT format('enum %s %s', t.typname, e.enumlabel)
		 FROM pg_type t JOIN pg_enum e ON e.enumtypid = t.oid
		 JOIN pg_namespace n ON n.oid = t.typnamespace
		 WHERE n.nspname = 'public'`,

		`SELECT format('trigger %s %s', tgrelid::regclass, tgname)
		 FROM pg_trigger WHERE NOT tgisinternal`,

		`SELECT format('function %s', p.proname)
		 FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		 WHERE n.nspname = 'public'`,
	}

	var lines []string
	for _, query := range queries {
		rows, err := conn.Query(ctx, query)
		if err != nil {
			t.Fatalf("fingerprint query failed: %v", err)
		}
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				rows.Close()
				t.Fatalf("failed to scan a fingerprint row: %v", err)
			}
			lines = append(lines, line)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("fingerprint query failed: %v", err)
		}
		rows.Close()
	}

	sort.Strings(lines)
	return lines
}
