// Package postgres implements MailWisp persistence using PostgreSQL and pgx.
package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"mailwisp/migrations"
)

const migrationAdvisoryLockID int64 = 0x4d61696c57697370

// Migrate applies all pending embedded SQL migrations while holding a
// PostgreSQL session advisory lock.
func Migrate(ctx context.Context, dataSourceName string) error {
	config, err := pgx.ParseConfig(dataSourceName)
	if err != nil {
		return fmt.Errorf("parse postgres migration DSN: %w", err)
	}
	database := stdlib.OpenDB(*config)
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	database.SetConnMaxLifetime(0)
	defer database.Close()

	if err := database.PingContext(ctx); err != nil {
		return fmt.Errorf("ping postgres for migration: %w", err)
	}
	if _, err := database.ExecContext(ctx, "SELECT pg_advisory_lock($1)", migrationAdvisoryLockID); err != nil {
		return fmt.Errorf("acquire postgres migration lock: %w", err)
	}
	defer func() {
		unlockContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = database.ExecContext(unlockContext, "SELECT pg_advisory_unlock($1)", migrationAdvisoryLockID)
	}()

	provider, err := goose.NewProvider(goose.DialectPostgres, database, migrations.FS)
	if err != nil {
		return fmt.Errorf("create postgres migration provider: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("apply postgres migrations: %w", err)
	}
	return nil
}
