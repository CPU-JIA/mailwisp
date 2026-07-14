package postgres

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestNewContentParseRepositoryValidatesPool(t *testing.T) {
	t.Parallel()

	if _, err := NewContentParseRepository(nil); err == nil {
		t.Fatal("NewContentParseRepository(nil) error = nil, want error")
	}
	if _, err := NewContentParseRepository(&pgxpool.Pool{}); err != nil {
		t.Fatalf("NewContentParseRepository(pool) error = %v", err)
	}
}
