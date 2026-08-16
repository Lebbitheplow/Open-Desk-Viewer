// Package migrations embeds the schema and applies it at API startup.
//
// The schema used to be applied only by Postgres's initdb, which runs solely on
// an empty data volume. Any deployment that already had a database would never
// receive a new migration. Embedding the files means the binary carries its own
// schema and cannot be run against one it does not understand.
package migrations

import (
	"embed"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

//go:embed *.sql
var files embed.FS

// FS exposes the embedded migration files for tests that need to read them
// without running them.
func FS() embed.FS { return files }

// Logger receives progress messages. It matches the subset of zerolog and the
// standard logger that the runner needs, so callers can pass either.
type Logger interface {
	Printf(format string, v ...any)
}

// Run applies every outstanding migration against databaseURL and reports
// whether anything changed. golang-migrate's postgres driver takes a session
// advisory lock for the duration, so two API replicas starting at once cannot
// migrate concurrently: the second blocks, then finds no work to do.
func Run(databaseURL string, log Logger) error {
	src, err := iofs.New(files, ".")
	if err != nil {
		return fmt.Errorf("open embedded migrations: %w", err)
	}

	m, err := migrate.NewWithSourceInstance("iofs", src, normaliseURL(databaseURL))
	if err != nil {
		return fmt.Errorf("init migrator: %w", err)
	}
	defer func() {
		// Close reports the source and database errors separately. Neither is
		// worth failing a successful migration over, but a stuck connection is
		// worth saying out loud.
		if srcErr, dbErr := m.Close(); srcErr != nil || dbErr != nil {
			log.Printf("migrations: close: source=%v database=%v", srcErr, dbErr)
		}
	}()

	switch err := m.Up(); {
	case errors.Is(err, migrate.ErrNoChange):
		log.Printf("migrations: no change")
		return nil
	case err != nil:
		return fmt.Errorf("apply migrations: %w", err)
	}

	version, dirty, err := m.Version()
	if err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if dirty {
		return fmt.Errorf("schema version %d is dirty: a previous migration failed part way and must be repaired by hand", version)
	}

	log.Printf("migrations: applied, schema now at version %d", version)
	return nil
}

// normaliseURL rewrites a postgres:// or postgresql:// URL to the pgx5 scheme
// golang-migrate's driver registers itself under. Passing the URL through
// unchanged would select the lib/pq driver, which is not a dependency here.
func normaliseURL(databaseURL string) string {
	for _, prefix := range []string{"postgresql://", "postgres://"} {
		if len(databaseURL) >= len(prefix) && databaseURL[:len(prefix)] == prefix {
			return "pgx5://" + databaseURL[len(prefix):]
		}
	}
	return databaseURL
}

// ensure the pgx/v5 driver is linked in; it registers itself via init.
var _ = pgx.Postgres{}
