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
	if _, err := NewDeliveryRepositoryWithLimits(nil, DeliveryLimits{MaxInboxMessages: 1, MaxInboxStorageBytes: 1}); err == nil {
		t.Fatal("NewDeliveryRepositoryWithLimits(nil) error = nil")
	}
	if _, err := NewDeliveryRepositoryWithLimits(&pgxpool.Pool{}, DeliveryLimits{}); err == nil {
		t.Fatal("NewDeliveryRepositoryWithLimits(invalid) error = nil")
	}
	if _, err := NewDeliveryRepositoryWithLimits(&pgxpool.Pool{}, DeliveryLimits{MaxInboxMessages: 1, MaxInboxStorageBytes: 1}); err != nil {
		t.Fatalf("NewDeliveryRepositoryWithLimits(valid) error = %v", err)
	}
}
