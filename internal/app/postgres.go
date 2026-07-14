package app

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"mailwisp/internal/config"
	"mailwisp/internal/postgres"
)

func openPostgresPool(ctx context.Context, cfg config.Postgres) (*pgxpool.Pool, error) {
	pool, err := postgres.OpenPool(ctx, postgres.PoolOptions{
		DSN:               cfg.DSN,
		MinConnections:    cfg.MinConnections,
		MaxConnections:    cfg.MaxConnections,
		ConnectTimeout:    cfg.ConnectTimeout,
		HealthCheckPeriod: 30 * time.Second,
		MaxConnectionIdle: 5 * time.Minute,
	})
	if err != nil {
		return nil, fmt.Errorf("open postgres pool: %w", err)
	}
	return pool, nil
}
