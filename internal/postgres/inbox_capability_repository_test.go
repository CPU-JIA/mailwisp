package postgres

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestNewInboxCapabilityRepositoryValidatesPool(t *testing.T) {
	t.Parallel()

	if _, err := NewInboxCapabilityRepository(nil); err == nil {
		t.Fatal("NewInboxCapabilityRepository(nil) error = nil, want error")
	}
	if _, err := NewInboxCapabilityRepository(&pgxpool.Pool{}); err != nil {
		t.Fatalf("NewInboxCapabilityRepository(pool) error = %v", err)
	}
}
