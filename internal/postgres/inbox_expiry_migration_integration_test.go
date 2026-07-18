//go:build integration

package postgres

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"mailwisp/migrations"
)

func TestInboxExpiryMigrationClosesPermanentInboxHoleAndReadinessRejectsOldSchema(t *testing.T) {
	dropIntegrationSchema(t)
	t.Cleanup(func() { recreateIntegrationSchema(t) })
	config, err := pgx.ParseConfig(integrationDataSourceName)
	if err != nil {
		t.Fatal(err)
	}
	database := stdlib.OpenDB(*config)
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = database.Close() })
	provider, err := goose.NewProvider(goose.DialectPostgres, database, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(context.Background(), 6); err != nil {
		t.Fatalf("apply migrations through version 6: %v", err)
	}
	if _, err := database.ExecContext(context.Background(), "INSERT INTO inboxes (address) VALUES ('permanent-hole@mailwisp.test')"); err != nil {
		t.Fatal(err)
	}
	pool := newIntegrationPool(t)
	repository := newIntegrationRepository(t, pool)
	if err := repository.Ready(context.Background()); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("require %d", migrations.LatestVersion)) {
		t.Fatalf("Ready(schema 6) error = %v", err)
	}
	if _, err := provider.UpTo(context.Background(), migrations.LatestVersion); err != nil {
		t.Fatalf("apply migration %d: %v", migrations.LatestVersion, err)
	}
	if err := repository.Ready(context.Background()); err != nil {
		t.Fatalf("Ready(latest) error = %v", err)
	}
	var status string
	var expiresAt time.Time
	if err := database.QueryRowContext(context.Background(), "SELECT status, expires_at FROM inboxes WHERE address = 'permanent-hole@mailwisp.test'").Scan(&status, &expiresAt); err != nil {
		t.Fatal(err)
	}
	if status != "expired" || expiresAt.After(time.Now().Add(time.Second)) {
		t.Fatalf("migrated Inbox status/expiry = %q/%s", status, expiresAt)
	}
	if _, err := database.ExecContext(context.Background(), "INSERT INTO inboxes (address) VALUES ('still-permanent@mailwisp.test')"); err == nil {
		t.Fatal("latest schema accepted an Inbox without expires_at")
	}
}
