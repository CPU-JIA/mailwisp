package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"mailwisp/internal/message"
)

var (
	// ErrContentMetadataConflict indicates that one content key has conflicting metadata.
	ErrContentMetadataConflict = errors.New("content metadata conflict")
)

// DeliveryRepository atomically writes content metadata and recipient message rows.
type DeliveryRepository struct {
	pool *pgxpool.Pool
}

// NewDeliveryRepository constructs a PostgreSQL delivery repository.
func NewDeliveryRepository(pool *pgxpool.Pool) (*DeliveryRepository, error) {
	if pool == nil {
		return nil, errors.New("postgres pool is required")
	}
	return &DeliveryRepository{pool: pool}, nil
}

// Ready verifies that PostgreSQL can serve a short request.
func (r *DeliveryRepository) Ready(ctx context.Context) error {
	var ready int
	if err := r.pool.QueryRow(ctx, `
		SELECT 1
		FROM inboxes
		WHERE false
	`).Scan(&ready); !errors.Is(err, pgx.ErrNoRows) {
		if err != nil {
			return fmt.Errorf("verify postgres schema: %w", err)
		}
	}
	return nil
}

// ResolveInbox finds an active, unexpired inbox by canonical address.
func (r *DeliveryRepository) ResolveInbox(ctx context.Context, address string) (message.InboxID, error) {
	var inboxID string
	err := r.pool.QueryRow(ctx, `
		SELECT id::text
		FROM inboxes
		WHERE address = $1
		  AND status = 'active'
		  AND (expires_at IS NULL OR expires_at > now())
	`, address).Scan(&inboxID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", message.ErrInboxNotFound
	}
	if err != nil {
		return "", fmt.Errorf("resolve inbox by address: %w", err)
	}
	return message.InboxID(inboxID), nil
}

// CommitDelivery creates one message per recipient in a single transaction.
func (r *DeliveryRepository) CommitDelivery(ctx context.Context, delivery message.Delivery) ([]message.StoredMessage, error) {
	if err := delivery.Validate(); err != nil {
		return nil, err
	}

	transaction, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("begin delivery transaction: %w", err)
	}
	defer func() {
		rollbackContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = transaction.Rollback(rollbackContext)
	}()

	tag, err := transaction.Exec(ctx, `
		INSERT INTO mail_contents (content_key, size_bytes)
		VALUES ($1, $2)
		ON CONFLICT (content_key) DO NOTHING
	`, delivery.Content.Key, delivery.Content.SizeBytes)
	if err != nil {
		return nil, fmt.Errorf("insert content metadata: %w", err)
	}
	if tag.RowsAffected() == 0 {
		var existingSize int64
		if err := transaction.QueryRow(ctx, `
			SELECT size_bytes
			FROM mail_contents
			WHERE content_key = $1
		`, delivery.Content.Key).Scan(&existingSize); err != nil {
			return nil, fmt.Errorf("read existing content metadata: %w", err)
		}
		if existingSize != delivery.Content.SizeBytes {
			return nil, fmt.Errorf("%w: key %q has size %d, incoming size %d", ErrContentMetadataConflict, delivery.Content.Key, existingSize, delivery.Content.SizeBytes)
		}
	}

	stored := make([]message.StoredMessage, 0, len(delivery.Recipients))
	for _, inboxID := range delivery.Recipients {
		var messageID string
		err := transaction.QueryRow(ctx, `
			INSERT INTO messages (inbox_id, content_key, envelope_sender, received_at)
			VALUES ($1::uuid, $2, $3, $4)
			RETURNING id::text
		`, string(inboxID), delivery.Content.Key, delivery.EnvelopeSender, delivery.ReceivedAt.UTC()).Scan(&messageID)
		if err != nil {
			return nil, mapDeliveryError(err)
		}
		stored = append(stored, message.StoredMessage{
			ID:      message.MessageID(messageID),
			InboxID: inboxID,
		})
	}

	if err := transaction.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit delivery transaction: %w", err)
	}
	return stored, nil
}

func mapDeliveryError(err error) error {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.Code == "23503" {
		return fmt.Errorf("%w: %s", message.ErrInboxNotFound, postgresError.ConstraintName)
	}
	return fmt.Errorf("insert recipient message: %w", err)
}
