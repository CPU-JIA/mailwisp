package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PoolOptions configures the shared PostgreSQL connection policy.
type PoolOptions struct {
	DSN               string
	MinConnections    int32
	MaxConnections    int32
	ConnectTimeout    time.Duration
	HealthCheckPeriod time.Duration
	MaxConnectionIdle time.Duration
}

// OpenPool constructs a PostgreSQL pool with explicit bounded settings.
func OpenPool(ctx context.Context, options PoolOptions) (*pgxpool.Pool, error) {
	if options.DSN == "" {
		return nil, errors.New("postgres DSN is required")
	}
	if options.MaxConnections <= 0 || options.MinConnections < 0 || options.MinConnections > options.MaxConnections {
		return nil, errors.New("invalid postgres connection bounds")
	}
	if options.ConnectTimeout <= 0 || options.HealthCheckPeriod <= 0 || options.MaxConnectionIdle <= 0 {
		return nil, errors.New("postgres pool durations must be positive")
	}
	poolConfig, err := pgxpool.ParseConfig(options.DSN)
	if err != nil {
		return nil, fmt.Errorf("parse postgres pool configuration: %w", err)
	}
	poolConfig.MinConns = options.MinConnections
	poolConfig.MaxConns = options.MaxConnections
	poolConfig.ConnConfig.ConnectTimeout = options.ConnectTimeout
	poolConfig.HealthCheckPeriod = options.HealthCheckPeriod
	poolConfig.MaxConnIdleTime = options.MaxConnectionIdle
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("create postgres pool: %w", err)
	}
	return pool, nil
}
