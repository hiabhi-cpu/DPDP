package bootstrap

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NewDatabase creates a pgx connection pool and verifies connectivity.
// Panics if the database is unreachable — we never want a service to start
// without a working database connection.
func NewDatabase(ctx context.Context, databaseURL string) *pgxpool.Pool {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		panic(fmt.Sprintf("bootstrap: failed to parse DATABASE_URL: %v", err))
	}

	// Pool settings
	cfg.MaxConns = 10
	cfg.MinConns = 2
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		panic(fmt.Sprintf("bootstrap: failed to create DB pool: %v", err))
	}

	// Verify connectivity
	if err := pool.Ping(ctx); err != nil {
		panic(fmt.Sprintf("bootstrap: DB ping failed — is PostgreSQL running? %v", err))
	}

	return pool
}
