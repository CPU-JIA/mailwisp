//go:build integration

package postgres

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"mailwisp/internal/abuse"
	"mailwisp/migrations"
)

func TestCreateQuotaMigrationUpgradesVersionFiveData(t *testing.T) {
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
	if _, err := provider.UpTo(context.Background(), 5); err != nil {
		t.Fatalf("apply migrations through version 5: %v", err)
	}
	if _, err := database.ExecContext(context.Background(), `INSERT INTO inboxes (address) VALUES ('v5@mailwisp.test')`); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(context.Background(), 6); err != nil {
		t.Fatalf("apply migration 6: %v", err)
	}
	var inboxCount, quotaTable int
	if err := database.QueryRowContext(context.Background(), `
		SELECT
		  (SELECT count(*) FROM inboxes WHERE address = 'v5@mailwisp.test'),
		  (SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name = 'inbox_create_quotas')
	`).Scan(&inboxCount, &quotaTable); err != nil {
		t.Fatal(err)
	}
	if inboxCount != 1 || quotaTable != 1 {
		t.Fatalf("migration counts = Inbox %d quota table %d", inboxCount, quotaTable)
	}
}

func TestCreateQuotaRepositoryEnforcesUTCDateBuckets(t *testing.T) {
	pool := newIntegrationPool(t)
	resetIntegrationDatabase(t, pool)
	repository, err := NewCreateQuotaRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	var digest abuse.IdentityDigest
	digest[0] = 1
	bucket := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	for want := 1; want <= 2; want++ {
		used, err := repository.ConsumeInboxCreate(context.Background(), digest, bucket, 2)
		if err != nil || used != want {
			t.Fatalf("ConsumeInboxCreate(%d) = %d, %v", want, used, err)
		}
	}
	if used, err := repository.ConsumeInboxCreate(context.Background(), digest, bucket, 2); !errors.Is(err, abuse.ErrDailyCreateQuotaExceeded) || used != 2 {
		t.Fatalf("ConsumeInboxCreate(limit) = %d, %v", used, err)
	}
	if used, err := repository.ConsumeInboxCreate(context.Background(), digest, bucket.Add(24*time.Hour), 2); err != nil || used != 1 {
		t.Fatalf("ConsumeInboxCreate(next day) = %d, %v", used, err)
	}
}

func TestCreateQuotaRepositoryIsAtomicUnderConcurrency(t *testing.T) {
	pool := newIntegrationPool(t)
	resetIntegrationDatabase(t, pool)
	repository, err := NewCreateQuotaRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	var digest abuse.IdentityDigest
	digest[0] = 2
	bucket := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			_, err := repository.ConsumeInboxCreate(context.Background(), digest, bucket, 1)
			results <- err
		}()
	}
	close(start)
	var allowed, rejected int
	for range 2 {
		err := <-results
		if err == nil {
			allowed++
		} else if errors.Is(err, abuse.ErrDailyCreateQuotaExceeded) {
			rejected++
		} else {
			t.Fatalf("ConsumeInboxCreate() error = %v", err)
		}
	}
	if allowed != 1 || rejected != 1 {
		t.Fatalf("quota outcomes allowed=%d rejected=%d", allowed, rejected)
	}
}

func TestCreateQuotaRepositoryCleansStaleRowsInBoundedBatches(t *testing.T) {
	pool := newIntegrationPool(t)
	resetIntegrationDatabase(t, pool)
	bucket := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	stale := bucket.AddDate(0, 0, -3)
	for index := 0; index < 150; index++ {
		digest := make([]byte, 32)
		binary.BigEndian.PutUint16(digest[30:], uint16(index))
		if _, err := pool.Exec(context.Background(), `
			INSERT INTO inbox_create_quotas (identity_digest, bucket_date, used, updated_at)
			VALUES ($1, $2, 1, now())
		`, digest, stale); err != nil {
			t.Fatal(err)
		}
	}
	repository, err := NewCreateQuotaRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	var current abuse.IdentityDigest
	current[0] = 9
	if _, err := repository.ConsumeInboxCreate(context.Background(), current, bucket, 10); err != nil {
		t.Fatal(err)
	}
	var staleCount int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM inbox_create_quotas WHERE bucket_date = $1", stale).Scan(&staleCount); err != nil {
		t.Fatal(err)
	}
	if staleCount != 50 {
		t.Fatalf("stale rows after one bounded cleanup = %d, want 50", staleCount)
	}
	if _, err := repository.ConsumeInboxCreate(context.Background(), current, bucket, 10); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM inbox_create_quotas WHERE bucket_date = $1", stale).Scan(&staleCount); err != nil {
		t.Fatal(err)
	}
	if staleCount != 0 {
		t.Fatalf("stale rows after second bounded cleanup = %d", staleCount)
	}
}
