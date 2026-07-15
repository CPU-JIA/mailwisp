package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"mailwisp/internal/abuse"
)

const staleCreateQuotaCleanupBatch = 100

// CreateQuotaRepository persists atomic UTC-day Inbox creation counters.
type CreateQuotaRepository struct {
	pool *pgxpool.Pool
}

// NewCreateQuotaRepository constructs a persistent create quota repository.
func NewCreateQuotaRepository(pool *pgxpool.Pool) (*CreateQuotaRepository, error) {
	if pool == nil {
		return nil, errors.New("postgres pool is required")
	}
	return &CreateQuotaRepository{pool: pool}, nil
}

// ConsumeInboxCreate atomically increments one identity bucket and performs bounded stale cleanup.
func (r *CreateQuotaRepository) ConsumeInboxCreate(ctx context.Context, digest abuse.IdentityDigest, bucket time.Time, limit int) (int, error) {
	if bucket.Location() != time.UTC || bucket.Hour() != 0 || bucket.Minute() != 0 || bucket.Second() != 0 || bucket.Nanosecond() != 0 || limit <= 0 {
		return 0, errors.New("create quota request is invalid")
	}
	transaction, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return 0, fmt.Errorf("begin create quota transaction: %w", err)
	}
	defer rollbackTransaction(transaction)

	if _, err := transaction.Exec(ctx, `
		WITH stale AS (
			SELECT identity_digest, bucket_date
			FROM inbox_create_quotas
			WHERE bucket_date < $1::date - 2
			ORDER BY bucket_date, identity_digest
			FOR UPDATE SKIP LOCKED
			LIMIT $2
		)
		DELETE FROM inbox_create_quotas AS quota
		USING stale
		WHERE quota.identity_digest = stale.identity_digest
		  AND quota.bucket_date = stale.bucket_date
	`, bucket, staleCreateQuotaCleanupBatch); err != nil {
		return 0, fmt.Errorf("cleanup stale create quotas: %w", err)
	}

	var used int
	err = transaction.QueryRow(ctx, `
		INSERT INTO inbox_create_quotas (identity_digest, bucket_date, used, updated_at)
		VALUES ($1, $2::date, 1, now())
		ON CONFLICT (identity_digest, bucket_date) DO UPDATE
		SET used = inbox_create_quotas.used + 1,
		    updated_at = now()
		WHERE inbox_create_quotas.used < $3
		RETURNING used
	`, digest[:], bucket, limit).Scan(&used)
	if errors.Is(err, pgx.ErrNoRows) {
		return limit, abuse.ErrDailyCreateQuotaExceeded
	}
	if err != nil {
		return 0, fmt.Errorf("consume Inbox create quota: %w", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit create quota transaction: %w", err)
	}
	return used, nil
}

var _ abuse.Repository = (*CreateQuotaRepository)(nil)
