package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"mailwisp/internal/cloudflaretemp"
	"mailwisp/internal/message"
)

// CloudflareTempRepository persists stable integer compatibility IDs.
type CloudflareTempRepository struct{ pool *pgxpool.Pool }

// NewCloudflareTempRepository constructs a compatibility ID repository.
func NewCloudflareTempRepository(pool *pgxpool.Pool) (*CloudflareTempRepository, error) {
	if pool == nil {
		return nil, errors.New("postgres pool is required")
	}
	return &CloudflareTempRepository{pool: pool}, nil
}

// EnsureInboxID returns the existing ID or atomically creates one.
func (r *CloudflareTempRepository) EnsureInboxID(ctx context.Context, inboxID message.InboxID) (int64, error) {
	var compatibilityID int64
	err := r.pool.QueryRow(ctx, `
		SELECT id
		FROM cloudflare_temp_inbox_ids
		WHERE inbox_id = $1::uuid
	`, string(inboxID)).Scan(&compatibilityID)
	if err == nil {
		return compatibilityID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("find Cloudflare Temp Email Inbox ID: %w", err)
	}
	err = r.pool.QueryRow(ctx, `
		INSERT INTO cloudflare_temp_inbox_ids (inbox_id)
		VALUES ($1::uuid)
		ON CONFLICT (inbox_id) DO NOTHING
		RETURNING id
	`, string(inboxID)).Scan(&compatibilityID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = r.pool.QueryRow(ctx, `
			SELECT id
			FROM cloudflare_temp_inbox_ids
			WHERE inbox_id = $1::uuid
		`, string(inboxID)).Scan(&compatibilityID)
	}
	if err != nil {
		return 0, fmt.Errorf("ensure Cloudflare Temp Email Inbox ID: %w", err)
	}
	return compatibilityID, nil
}

// EnsureMessageIDs returns stable IDs for messages owned by one Inbox.
func (r *CloudflareTempRepository) EnsureMessageIDs(ctx context.Context, inboxID message.InboxID, messageIDs []message.MessageID) (map[message.MessageID]int64, error) {
	result := make(map[message.MessageID]int64, len(messageIDs))
	if len(messageIDs) == 0 {
		return result, nil
	}
	transaction, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin Cloudflare Temp Email message ID assignment: %w", err)
	}
	defer rollbackTransaction(transaction)
	for _, messageID := range messageIDs {
		compatibilityID, err := ensureCloudflareTempMessageID(ctx, transaction, inboxID, messageID)
		if err != nil {
			return nil, err
		}
		result[messageID] = compatibilityID
	}
	if err := transaction.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit Cloudflare Temp Email message ID assignment: %w", err)
	}
	return result, nil
}

func ensureCloudflareTempMessageID(ctx context.Context, transaction pgx.Tx, inboxID message.InboxID, messageID message.MessageID) (int64, error) {
	var compatibilityID int64
	err := transaction.QueryRow(ctx, `
		SELECT id
		FROM cloudflare_temp_message_ids
		WHERE inbox_id = $1::uuid
		  AND message_id = $2::uuid
	`, string(inboxID), string(messageID)).Scan(&compatibilityID)
	if err == nil {
		return compatibilityID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("find Cloudflare Temp Email message ID: %w", err)
	}
	err = transaction.QueryRow(ctx, `
		INSERT INTO cloudflare_temp_message_ids (inbox_id, message_id)
		SELECT $1::uuid, message.id
		FROM messages AS message
		WHERE message.id = $2::uuid
		  AND message.inbox_id = $1::uuid
		ON CONFLICT (message_id) DO NOTHING
		RETURNING id
	`, string(inboxID), string(messageID)).Scan(&compatibilityID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = transaction.QueryRow(ctx, `
			SELECT id
			FROM cloudflare_temp_message_ids
			WHERE inbox_id = $1::uuid
			  AND message_id = $2::uuid
		`, string(inboxID), string(messageID)).Scan(&compatibilityID)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, cloudflaretemp.ErrMessageIDNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("ensure Cloudflare Temp Email message ID: %w", err)
	}
	return compatibilityID, nil
}

// FindMessageID resolves a positive compatibility ID with Inbox ownership.
func (r *CloudflareTempRepository) FindMessageID(ctx context.Context, inboxID message.InboxID, compatibilityID int64) (message.MessageID, error) {
	if compatibilityID <= 0 {
		return "", cloudflaretemp.ErrMessageIDNotFound
	}
	var messageID message.MessageID
	err := r.pool.QueryRow(ctx, `
		SELECT mapping.message_id::text
		FROM cloudflare_temp_message_ids AS mapping
		JOIN messages AS message
		  ON message.id = mapping.message_id
		 AND message.inbox_id = mapping.inbox_id
		WHERE mapping.id = $2
		  AND mapping.inbox_id = $1::uuid
	`, string(inboxID), compatibilityID).Scan(&messageID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", cloudflaretemp.ErrMessageIDNotFound
	}
	if err != nil {
		return "", fmt.Errorf("find Cloudflare Temp Email message ID: %w", err)
	}
	return messageID, nil
}

var _ cloudflaretemp.IDRepository = (*CloudflareTempRepository)(nil)
