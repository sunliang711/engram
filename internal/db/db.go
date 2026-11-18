package db

import (
	"context"
	"fmt"
	"time"

	"engram/internal/config"
	"engram/migrations"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Database wraps pgxpool.Pool for explicit transactional SQL, optimistic locking, and LISTEN/NOTIFY.
type Database struct {
	Pool *pgxpool.Pool
}

// Open connects to PostgreSQL, configures the pool, and optionally runs schema migrations.
func Open(ctx context.Context, cfg config.DatabaseConfig) (*Database, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("parse database dsn: %w", err)
	}
	if cfg.MaxConns > 0 {
		poolCfg.MaxConns = cfg.MaxConns
	}
	if cfg.MinConns >= 0 {
		poolCfg.MinConns = cfg.MinConns
	}
	if cfg.MaxConnLifetime > 0 {
		poolCfg.MaxConnLifetime = cfg.MaxConnLifetime
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("create pgx pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	if cfg.AutoMigrate {
		if err := migrations.Run(ctx, pool); err != nil {
			pool.Close()
			return nil, fmt.Errorf("run migrations: %w", err)
		}
	}

	return &Database{
		Pool: pool,
	}, nil
}

// Close closes the underlying connection pool.
func (d *Database) Close() {
	if d != nil && d.Pool != nil {
		d.Pool.Close()
	}
}

// Ping verifies database connectivity for readiness probes.
func (d *Database) Ping(ctx context.Context) error {
	if d == nil || d.Pool == nil {
		return fmt.Errorf("database pool not initialized")
	}
	return d.Pool.Ping(ctx)
}
