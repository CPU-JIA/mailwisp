package postgres

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"mailwisp/internal/message"
)

var (
	// ErrContentMetadataConflict indicates that one content key has conflicting metadata.
	ErrContentMetadataConflict = errors.New("content metadata conflict")
)

// DeliveryRepository atomically writes content metadata and recipient message rows.
type DeliveryRepository struct {
	pool          *pgxpool.Pool
	limits        DeliveryLimits
	enforceLimits bool
	// commitObserver is an unexported deterministic crash-test seam.
	// Production composition never assigns it.
	commitObserver func(deliveryCommitStage)
}

// DeliveryLimits bounds logical storage consumed by one active Inbox.
type DeliveryLimits struct {
	MaxInboxMessages     int
	MaxInboxStorageBytes int64
}

type deliveryCommitStage string

const (
	deliveryCommitStageBefore deliveryCommitStage = "before-commit"
	deliveryCommitStageAfter  deliveryCommitStage = "after-commit"
)

// NewDeliveryRepository constructs a PostgreSQL delivery repository.
func NewDeliveryRepository(pool *pgxpool.Pool) (*DeliveryRepository, error) {
	if pool == nil {
		return nil, errors.New("postgres pool is required")
	}
	return &DeliveryRepository{pool: pool}, nil
}

// NewDeliveryRepositoryWithLimits constructs a quota-enforcing delivery repository.
func NewDeliveryRepositoryWithLimits(pool *pgxpool.Pool, limits DeliveryLimits) (*DeliveryRepository, error) {
	if pool == nil {
		return nil, errors.New("postgres pool is required")
	}
	if limits.MaxInboxMessages <= 0 || limits.MaxInboxStorageBytes <= 0 {
		return nil, errors.New("delivery Inbox limits must be positive")
	}
	return &DeliveryRepository{pool: pool, limits: limits, enforceLimits: true}, nil
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
	return r.ResolveInboxForDelivery(ctx, address, 0)
}

// ResolveInboxForDelivery resolves a recipient and rejects an already-full Inbox before DATA.
func (r *DeliveryRepository) ResolveInboxForDelivery(ctx context.Context, address string, declaredSize int64) (message.InboxID, error) {
	if declaredSize < 0 {
		return "", message.ErrInvalidDelivery
	}
	if !r.enforceLimits {
		return r.resolveInbox(ctx, address)
	}
	var inboxID string
	var messageCount int
	var storageBytes int64
	err := r.pool.QueryRow(ctx, `
		SELECT inbox.id::text,
		       count(message.id),
		       COALESCE(sum(content.size_bytes), 0)
		FROM inboxes AS inbox
		LEFT JOIN messages AS message ON message.inbox_id = inbox.id
		LEFT JOIN mail_contents AS content ON content.content_key = message.content_key
		WHERE inbox.address = $1
		  AND inbox.status = 'active'
		  AND (inbox.expires_at IS NULL OR inbox.expires_at > now())
		GROUP BY inbox.id
	`, address).Scan(&inboxID, &messageCount, &storageBytes)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", message.ErrInboxNotFound
	}
	if err != nil {
		return "", fmt.Errorf("resolve Inbox quota by address: %w", err)
	}
	if messageCount >= r.limits.MaxInboxMessages {
		return "", message.ErrInboxMessageQuotaExceeded
	}
	if storageBytes >= r.limits.MaxInboxStorageBytes || declaredSize > r.limits.MaxInboxStorageBytes-storageBytes {
		return "", message.ErrInboxStorageQuotaExceeded
	}
	return message.InboxID(inboxID), nil
}

func (r *DeliveryRepository) resolveInbox(ctx context.Context, address string) (message.InboxID, error) {
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
	if r.enforceLimits {
		if err := r.lockAndCheckDeliveryRecipients(ctx, transaction, delivery); err != nil {
			return nil, err
		}
	}

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

	r.observeCommit(deliveryCommitStageBefore)
	if err := transaction.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit delivery transaction: %w", err)
	}
	r.observeCommit(deliveryCommitStageAfter)
	return stored, nil
}

func (r *DeliveryRepository) lockAndCheckDeliveryRecipients(ctx context.Context, transaction pgx.Tx, delivery message.Delivery) error {
	recipientUUIDs := make([]pgtype.UUID, 0, len(delivery.Recipients))
	for _, inboxID := range delivery.Recipients {
		parsed, err := uuid.Parse(string(inboxID))
		if err != nil {
			return message.ErrInvalidDelivery
		}
		recipientUUIDs = append(recipientUUIDs, pgtype.UUID{Bytes: parsed, Valid: true})
	}
	sort.Slice(recipientUUIDs, func(left, right int) bool { return recipientUUIDs[left].String() < recipientUUIDs[right].String() })
	rows, err := transaction.Query(ctx, `
		SELECT id::text
		FROM inboxes
		WHERE id = ANY($1::uuid[])
		  AND status = 'active'
		  AND (expires_at IS NULL OR expires_at > now())
		ORDER BY id
		FOR UPDATE
	`, recipientUUIDs)
	if err != nil {
		return fmt.Errorf("lock delivery recipients: %w", err)
	}
	locked, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("collect locked delivery recipients: %w", err)
	}
	if len(locked) != len(delivery.Recipients) {
		return message.ErrInboxNotFound
	}

	rows, err = transaction.Query(ctx, `
		SELECT message.inbox_id::text,
		       count(message.id),
		       COALESCE(sum(content.size_bytes), 0)
		FROM messages AS message
		JOIN mail_contents AS content ON content.content_key = message.content_key
		WHERE message.inbox_id = ANY($1::uuid[])
		GROUP BY message.inbox_id
	`, recipientUUIDs)
	if err != nil {
		return fmt.Errorf("read delivery recipient quotas: %w", err)
	}
	type usage struct {
		InboxID      string
		MessageCount int
		StorageBytes int64
	}
	usageRows, err := pgx.CollectRows(rows, pgx.RowToStructByPos[usage])
	if err != nil {
		return fmt.Errorf("collect delivery recipient quotas: %w", err)
	}
	byInbox := make(map[string]usage, len(usageRows))
	for _, current := range usageRows {
		byInbox[current.InboxID] = current
	}
	for _, inboxID := range delivery.Recipients {
		current := byInbox[string(inboxID)]
		if current.MessageCount >= r.limits.MaxInboxMessages {
			return message.ErrInboxMessageQuotaExceeded
		}
		if delivery.Content.SizeBytes > r.limits.MaxInboxStorageBytes-current.StorageBytes {
			return message.ErrInboxStorageQuotaExceeded
		}
	}
	return nil
}

func (r *DeliveryRepository) observeCommit(stage deliveryCommitStage) {
	if r.commitObserver != nil {
		r.commitObserver(stage)
	}
}

func mapDeliveryError(err error) error {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.Code == "23503" {
		return fmt.Errorf("%w: %s", message.ErrInboxNotFound, postgresError.ConstraintName)
	}
	return fmt.Errorf("insert recipient message: %w", err)
}
