package postgres

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestNewCloudflareTempRepositoryValidatesPool(t *testing.T) {
	if _, err := NewCloudflareTempRepository(nil); err == nil {
		t.Fatal("NewCloudflareTempRepository(nil) error = nil")
	}
	if _, err := NewCloudflareTempRepository(&pgxpool.Pool{}); err != nil {
		t.Fatalf("NewCloudflareTempRepository(pool) error = %v", err)
	}
}
