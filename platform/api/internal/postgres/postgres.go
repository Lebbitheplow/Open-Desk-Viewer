package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Pool is a wrapper around pgxpool.Pool
type Pool struct {
	*pgxpool.Pool
	config *pgxpool.Config
}

// New creates a new connection pool
func New(ctx context.Context, host string, port int, database, user, password string) (*Pool, error) {
	connString := fmt.Sprintf("postgresql://%s:%s@%s:%d/%s?sslmode=disable",
		user, password, host, port, database)

	config, err := pgxpool.ParseConfig(connString)
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
