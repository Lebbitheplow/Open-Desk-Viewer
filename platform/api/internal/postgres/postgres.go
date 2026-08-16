package postgres

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Pool is a wrapper around pgxpool.Pool
type Pool struct {
	*pgxpool.Pool
	config *pgxpool.Config
}

// DSN builds a libpq connection string.
//
// It is built with net/url rather than fmt.Sprintf because the password is user
// data: a '@', '/' or '?' in it used to be interpolated raw, which either fails
// to parse or, worse, parses as a different host. url.UserPassword escapes it.
//
// sslMode is the caller's, not a constant. It was hardcoded to "disable" here
// and again in the migration runner, so a deployment whose database sits
// anywhere but the compose network sent its credentials and every row in the
// clear with no way to say otherwise. An empty value means "require", the safe
// default; compose overrides it to "disable" for the private network.
func DSN(host string, port int, database, user, password, sslMode string) string {
	if sslMode == "" {
		sslMode = "require"
	}
	u := url.URL{
		Scheme:   "postgresql",
		User:     url.UserPassword(user, password),
		Host:     net.JoinHostPort(host, strconv.Itoa(port)),
		Path:     "/" + database,
		RawQuery: url.Values{"sslmode": []string{sslMode}}.Encode(),
	}
	return u.String()
}

// New creates a new connection pool
func New(ctx context.Context, host string, port int, database, user, password, sslMode string) (*Pool, error) {
	config, err := pgxpool.ParseConfig(DSN(host, port, database, user, password, sslMode))
	if err != nil {
		return nil, fmt.Errorf("failed to parse connection config: %w", err)
	}

	config.HealthCheckPeriod = 10 * time.Second
	config.MaxConns = 20
	config.MinConns = 2

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection pool: %w", err)
	}

	return &Pool{Pool: pool, config: config}, nil
}

// Close closes the connection pool
func (p *Pool) Close() {
	p.Pool.Close()
}

// Tx begins a new transaction
func (p *Pool) Tx(ctx context.Context) (pgx.Tx, error) {
	return p.Pool.Begin(ctx)
}
