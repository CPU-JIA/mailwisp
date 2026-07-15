package postgres

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestNewDeliveryRepositoryValidatesPool(t *testing.T) {
	t.Parallel()

	if _, err := NewDeliveryRepository(nil); err == nil {
		t.Fatal("NewDeliveryRepository(nil) error = nil, want error")
	}
	if _, err := NewDeliveryRepository(&pgxpool.Pool{}); err != nil {
		t.Fatalf("NewDeliveryRepository(pool) error = %v", err)
	}
}
