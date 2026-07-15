package postgres

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"mailwisp/internal/abuse"
)

func TestNewCreateQuotaRepositoryValidatesPool(t *testing.T) {
	t.Parallel()

	if _, err := NewCreateQuotaRepository(nil); err == nil {
		t.Fatal("NewCreateQuotaRepository(nil) error = nil")
	}
	if _, err := NewCreateQuotaRepository(&pgxpool.Pool{}); err != nil {
		t.Fatalf("NewCreateQuotaRepository(pool) error = %v", err)
	}
}

func TestCreateQuotaRepositoryRejectsInvalidBucket(t *testing.T) {
	t.Parallel()

	repository, err := NewCreateQuotaRepository(&pgxpool.Pool{})
	if err != nil {
		t.Fatal(err)
	}
	var digest abuse.IdentityDigest
	for _, bucket := range []time.Time{
		time.Date(2026, 7, 15, 1, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 15, 0, 0, 0, 0, time.FixedZone("UTC-equivalent", 0)),
	} {
		if _, err := repository.ConsumeInboxCreate(t.Context(), digest, bucket, 1); err == nil {
			t.Fatalf("ConsumeInboxCreate(%s) error = nil", bucket)
		}
	}
	if _, err := repository.ConsumeInboxCreate(t.Context(), digest, time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC), 0); err == nil {
		t.Fatal("ConsumeInboxCreate(zero limit) error = nil")
	}
}
