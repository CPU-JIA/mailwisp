package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"mailwisp/internal/mailbox"
	"mailwisp/internal/message"
)

const cleanupAdvisoryLockID int64 = 0x4d575350434c4e50

// MailboxRepository persists Inbox lifecycle and ownership-scoped message access.
type MailboxRepository struct {
	pool *pgxpool.Pool
}

// NewMailboxRepository constructs a PostgreSQL mailbox repository.
func NewMailboxRepository(pool *pgxpool.Pool) (*MailboxRepository, error) {
	if pool == nil {
		return nil, errors.New("postgres pool is required")
	}
	return &MailboxRepository{pool: pool}, nil
}

// CreateInbox inserts one active temporary Inbox.
func (r *MailboxRepository) CreateInbox(ctx context.Context, candidate mailbox.NewInbox) (mailbox.Inbox, error) {
	var inbox mailbox.Inbox
	err := r.pool.QueryRow(ctx, `
		INSERT INTO inboxes (address, status, expires_at, created_at, updated_at)
		VALUES ($1, 'active', $2, $3, $3)
		RETURNING id::text, address, status, expires_at, created_at
	`, candidate.Address, candidate.ExpiresAt.UTC(), candidate.CreatedAt.UTC()).Scan(
		&inbox.ID, &inbox.Address, &inbox.Status, &inbox.ExpiresAt, &inbox.CreatedAt,
	)
	if err != nil {
		var postgresError *pgconn.PgError
		if errors.As(err, &postgresError) && postgresError.Code == "23505" && postgresError.ConstraintName == "inboxes_address_key" {
			return mailbox.Inbox{}, mailbox.ErrAddressConflict
		}
		return mailbox.Inbox{}, fmt.Errorf("insert Inbox: %w", err)
	}
	return inbox, nil
}

// GetInbox returns one active, unexpired Inbox.
func (r *MailboxRepository) GetInbox(ctx context.Context, inboxID message.InboxID) (mailbox.Inbox, error) {
	var inbox mailbox.Inbox
	err := r.pool.QueryRow(ctx, `
		SELECT id::text, address, status, expires_at, created_at
		FROM inboxes
		WHERE id = $1::uuid
		  AND status = 'active'
		  AND expires_at > now()
	`, string(inboxID)).Scan(&inbox.ID, &inbox.Address, &inbox.Status, &inbox.ExpiresAt, &inbox.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return mailbox.Inbox{}, mailbox.ErrInboxNotFound
	}
	if err != nil {
		return mailbox.Inbox{}, fmt.Errorf("read Inbox: %w", err)
	}
	return inbox, nil
}

// DeleteInbox removes an Inbox and returns content objects no longer referenced by any message.
func (r *MailboxRepository) DeleteInbox(ctx context.Context, inboxID message.InboxID) ([]message.ContentRef, error) {
	transaction, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("begin Inbox deletion: %w", err)
	}
	defer rollbackTransaction(transaction)

	rows, err := transaction.Query(ctx, `
		SELECT content.content_key, content.size_bytes
		FROM messages AS owned
		JOIN mail_contents AS content ON content.content_key = owned.content_key
		WHERE owned.inbox_id = $1::uuid
		GROUP BY content.content_key, content.size_bytes
		ORDER BY content.content_key
	`, string(inboxID))
	if err != nil {
		return nil, fmt.Errorf("lock Inbox content: %w", err)
	}
	refs, err := pgx.CollectRows(rows, pgx.RowToStructByPos[message.ContentRef])
	if err != nil {
		return nil, fmt.Errorf("collect Inbox content: %w", err)
	}

	tag, err := transaction.Exec(ctx, `
		DELETE FROM inboxes
		WHERE id = $1::uuid
		  AND status = 'active'
		  AND expires_at > now()
	`, string(inboxID))
	if err != nil {
		return nil, fmt.Errorf("delete Inbox: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return nil, mailbox.ErrInboxNotFound
	}

	orphans := make([]message.ContentRef, 0, len(refs))
	for _, ref := range refs {
		tag, err := transaction.Exec(ctx, `
			DELETE FROM mail_contents AS content
			WHERE content.content_key = $1
			  AND NOT EXISTS (
				SELECT 1 FROM messages WHERE messages.content_key = content.content_key
			  )
		`, ref.Key)
		if err != nil {
			return nil, fmt.Errorf("delete unreferenced Inbox content: %w", err)
		}
		if tag.RowsAffected() == 1 {
			orphans = append(orphans, ref)
		}
	}
	if err := transaction.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit Inbox deletion: %w", err)
	}
	return orphans, nil
}

// PurgeInbox compensates a failed capability issuance.
func (r *MailboxRepository) PurgeInbox(ctx context.Context, inboxID message.InboxID) error {
	if _, err := r.pool.Exec(ctx, "DELETE FROM inboxes WHERE id = $1::uuid", string(inboxID)); err != nil {
		return fmt.Errorf("purge Inbox: %w", err)
	}
	return nil
}

// ListMessages returns a bounded newest-first page for one active Inbox.
func (r *MailboxRepository) ListMessages(ctx context.Context, inboxID message.InboxID, page mailbox.Page) (mailbox.MessagePage, error) {
	if page.Limit <= 0 || page.Offset < 0 || (page.Before != nil && (page.Offset != 0 || page.Before.ID == "" || page.Before.ReceivedAt.IsZero())) {
		return mailbox.MessagePage{}, errors.New("message page is invalid")
	}
	var total, unread int
	if err := r.pool.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE message.seen_at IS NULL)
		FROM messages AS message
		JOIN inboxes AS inbox ON inbox.id = message.inbox_id
		WHERE message.inbox_id = $1::uuid
		  AND inbox.status = 'active'
		  AND inbox.expires_at > now()
	`, string(inboxID)).Scan(&total, &unread); err != nil {
		return mailbox.MessagePage{}, fmt.Errorf("count Inbox messages: %w", err)
	}
	query := `
		SELECT message.id::text,
		       message.envelope_sender,
		       COALESCE(parsed.subject, ''),
		       LEFT(REGEXP_REPLACE(COALESCE(parsed.text_body, ''), E'[\\n\\r\\t ]+', ' ', 'g'), 240),
		       message.received_at,
		       content.parse_status,
		       content.size_bytes,
		       COALESCE(jsonb_array_length(parsed.attachments), 0) > 0,
		       message.seen_at IS NOT NULL
		FROM messages AS message
		JOIN inboxes AS inbox ON inbox.id = message.inbox_id
		JOIN mail_contents AS content ON content.content_key = message.content_key
		LEFT JOIN mail_content_parses AS parsed ON parsed.content_key = content.content_key
		WHERE message.inbox_id = $1::uuid
		  AND inbox.status = 'active'
		  AND inbox.expires_at > now()
	`
	arguments := []any{string(inboxID), page.Limit, page.Offset}
	if page.Before != nil {
		query += ` AND (message.received_at, message.id) < ($4::timestamptz, $5::uuid)`
		arguments = append(arguments, page.Before.ReceivedAt.UTC(), string(page.Before.ID))
	}
	query += `
		ORDER BY message.received_at DESC, message.id DESC
		LIMIT $2 OFFSET $3
	`
	rows, err := r.pool.Query(ctx, query, arguments...)
	if err != nil {
		return mailbox.MessagePage{}, fmt.Errorf("list Inbox messages: %w", err)
	}
	messages, err := pgx.CollectRows(rows, pgx.RowToStructByPos[mailbox.MessageSummary])
	if err != nil {
		return mailbox.MessagePage{}, fmt.Errorf("collect Inbox messages: %w", err)
	}
	if messages == nil {
		messages = []mailbox.MessageSummary{}
	}
	return mailbox.MessagePage{Items: messages, Total: total, Unread: unread}, nil
}

// GetMessage returns one message only when it belongs to the active Inbox.
func (r *MailboxRepository) GetMessage(ctx context.Context, inboxID message.InboxID, messageID message.MessageID) (mailbox.MessageDetail, error) {
	var detail mailbox.MessageDetail
	var fromJSON, toJSON, ccJSON, attachmentsJSON, warningsJSON []byte
	err := r.pool.QueryRow(ctx, `
		SELECT message.id::text,
		       message.envelope_sender,
		       COALESCE(parsed.subject, ''),
		       LEFT(REGEXP_REPLACE(COALESCE(parsed.text_body, ''), E'[\\n\\r\\t ]+', ' ', 'g'), 240),
		       message.received_at,
		       content.parse_status,
		       content.size_bytes,
		       COALESCE(jsonb_array_length(parsed.attachments), 0) > 0,
		       message.seen_at IS NOT NULL,
		       COALESCE(parsed.header_message_id, ''),
		       COALESCE(parsed.from_addresses, '[]'::jsonb),
		       COALESCE(parsed.to_addresses, '[]'::jsonb),
		       COALESCE(parsed.cc_addresses, '[]'::jsonb),
		       parsed.sent_at,
		       COALESCE(parsed.text_body, ''),
		       COALESCE(parsed.html_source, ''),
		       COALESCE(parsed.attachments, '[]'::jsonb),
		       COALESCE(parsed.warnings, '[]'::jsonb)
		FROM messages AS message
		JOIN inboxes AS inbox ON inbox.id = message.inbox_id
		JOIN mail_contents AS content ON content.content_key = message.content_key
		LEFT JOIN mail_content_parses AS parsed ON parsed.content_key = content.content_key
		WHERE message.id = $2::uuid
		  AND message.inbox_id = $1::uuid
		  AND inbox.status = 'active'
		  AND inbox.expires_at > now()
	`, string(inboxID), string(messageID)).Scan(
		&detail.ID,
		&detail.EnvelopeSender,
		&detail.Subject,
		&detail.Preview,
		&detail.ReceivedAt,
		&detail.ParseStatus,
		&detail.SizeBytes,
		&detail.HasAttachments,
		&detail.Seen,
		&detail.HeaderMessageID,
		&fromJSON,
		&toJSON,
		&ccJSON,
		&detail.SentAt,
		&detail.Text,
		&detail.HTMLSource,
		&attachmentsJSON,
		&warningsJSON,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return mailbox.MessageDetail{}, mailbox.ErrMessageNotFound
	}
	if err != nil {
		return mailbox.MessageDetail{}, fmt.Errorf("read Inbox message: %w", err)
	}
	for name, target := range map[string]any{
		"From": &detail.From, "To": &detail.To, "Cc": &detail.Cc,
		"attachments": &detail.Attachments, "warnings": &detail.Warnings,
	} {
		var source []byte
		switch name {
		case "From":
			source = fromJSON
		case "To":
			source = toJSON
		case "Cc":
			source = ccJSON
		case "attachments":
			source = attachmentsJSON
		case "warnings":
			source = warningsJSON
		}
		if err := json.Unmarshal(source, target); err != nil {
			return mailbox.MessageDetail{}, fmt.Errorf("decode parsed %s: %w", name, err)
		}
	}
	return detail, nil
}

// MarkMessageSeen records that an owned message was opened.
func (r *MailboxRepository) MarkMessageSeen(ctx context.Context, inboxID message.InboxID, messageID message.MessageID, seenAt time.Time) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE messages AS message
		SET seen_at = COALESCE(message.seen_at, $3)
		FROM inboxes AS inbox
		WHERE message.id = $2::uuid
		  AND message.inbox_id = $1::uuid
		  AND inbox.id = message.inbox_id
		  AND inbox.status = 'active'
		  AND inbox.expires_at > now()
	`, string(inboxID), string(messageID), seenAt.UTC())
	if err != nil {
		return fmt.Errorf("mark Inbox message seen: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return mailbox.ErrMessageNotFound
	}
	return nil
}

// GetMessageContent returns one owned Raw MIME reference.
func (r *MailboxRepository) GetMessageContent(ctx context.Context, inboxID message.InboxID, messageID message.MessageID) (message.ContentRef, error) {
	var ref message.ContentRef
	err := r.pool.QueryRow(ctx, `
		SELECT content.content_key, content.size_bytes
		FROM messages AS message
		JOIN inboxes AS inbox ON inbox.id = message.inbox_id
		JOIN mail_contents AS content ON content.content_key = message.content_key
		WHERE message.id = $2::uuid
		  AND message.inbox_id = $1::uuid
		  AND inbox.status = 'active'
		  AND inbox.expires_at > now()
	`, string(inboxID), string(messageID)).Scan(&ref.Key, &ref.SizeBytes)
	if errors.Is(err, pgx.ErrNoRows) {
		return message.ContentRef{}, mailbox.ErrMessageNotFound
	}
	if err != nil {
		return message.ContentRef{}, fmt.Errorf("read Inbox message content: %w", err)
	}
	return ref, nil
}

// CleanupExpiredInboxes deletes one bounded, single-runner batch and returns
// Raw MIME objects whose final database reference was removed.
func (r *MailboxRepository) CleanupExpiredInboxes(ctx context.Context, batchSize int) (int, []message.ContentRef, error) {
	if batchSize <= 0 || batchSize > 1000 {
		return 0, nil, errors.New("cleanup batch size must be between 1 and 1000")
	}
	transaction, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return 0, nil, fmt.Errorf("begin expired Inbox cleanup: %w", err)
	}
	defer rollbackTransaction(transaction)
	var locked bool
	if err := transaction.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock($1)", cleanupAdvisoryLockID).Scan(&locked); err != nil {
		return 0, nil, fmt.Errorf("acquire cleanup advisory lock: %w", err)
	}
	if !locked {
		if err := transaction.Commit(ctx); err != nil {
			return 0, nil, fmt.Errorf("finish skipped cleanup: %w", err)
		}
		return 0, []message.ContentRef{}, nil
	}
	rows, err := transaction.Query(ctx, `
		SELECT id
		FROM inboxes
		WHERE expires_at IS NOT NULL
		  AND expires_at <= now()
		ORDER BY expires_at, id
		FOR UPDATE SKIP LOCKED
		LIMIT $1
	`, batchSize)
	if err != nil {
		return 0, nil, fmt.Errorf("select expired Inbox batch: %w", err)
	}
	inboxIDs, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return 0, nil, fmt.Errorf("collect expired Inbox batch: %w", err)
	}
	if len(inboxIDs) == 0 {
		if err := transaction.Commit(ctx); err != nil {
			return 0, nil, fmt.Errorf("finish empty cleanup: %w", err)
		}
		return 0, []message.ContentRef{}, nil
	}
	contentRows, err := transaction.Query(ctx, `
		SELECT content.content_key, content.size_bytes
		FROM messages AS owned
		JOIN mail_contents AS content ON content.content_key = owned.content_key
		WHERE owned.inbox_id = ANY($1::uuid[])
		GROUP BY content.content_key, content.size_bytes
		ORDER BY content.content_key
	`, inboxIDs)
	if err != nil {
		return 0, nil, fmt.Errorf("read expired Inbox content: %w", err)
	}
	refs, err := pgx.CollectRows(contentRows, pgx.RowToStructByPos[message.ContentRef])
	if err != nil {
		return 0, nil, fmt.Errorf("collect expired Inbox content: %w", err)
	}
	tag, err := transaction.Exec(ctx, "DELETE FROM inboxes WHERE id = ANY($1::uuid[])", inboxIDs)
	if err != nil {
		return 0, nil, fmt.Errorf("delete expired Inboxes: %w", err)
	}
	orphans := make([]message.ContentRef, 0, len(refs))
	for _, ref := range refs {
		contentTag, err := transaction.Exec(ctx, `
			DELETE FROM mail_contents AS content
			WHERE content.content_key = $1
			  AND NOT EXISTS (SELECT 1 FROM messages WHERE messages.content_key = content.content_key)
		`, ref.Key)
		if err != nil {
			return 0, nil, fmt.Errorf("delete expired unreferenced content: %w", err)
		}
		if contentTag.RowsAffected() == 1 {
			orphans = append(orphans, ref)
		}
	}
	if err := transaction.Commit(ctx); err != nil {
		return 0, nil, fmt.Errorf("commit expired Inbox cleanup: %w", err)
	}
	return int(tag.RowsAffected()), orphans, nil
}

// DeleteMessage removes one owned message and returns newly unreferenced Raw MIME.
func (r *MailboxRepository) DeleteMessage(ctx context.Context, inboxID message.InboxID, messageID message.MessageID) (*message.ContentRef, error) {
	transaction, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("begin message deletion: %w", err)
	}
	defer rollbackTransaction(transaction)

	var ref message.ContentRef
	err = transaction.QueryRow(ctx, `
		SELECT content.content_key, content.size_bytes
		FROM messages AS owned
		JOIN inboxes AS inbox ON inbox.id = owned.inbox_id
		JOIN mail_contents AS content ON content.content_key = owned.content_key
		WHERE owned.id = $2::uuid
		  AND owned.inbox_id = $1::uuid
		  AND inbox.status = 'active'
		  AND inbox.expires_at > now()
		FOR UPDATE OF owned, content
	`, string(inboxID), string(messageID)).Scan(&ref.Key, &ref.SizeBytes)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, mailbox.ErrMessageNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lock Inbox message: %w", err)
	}
	if _, err := transaction.Exec(ctx, "DELETE FROM messages WHERE id = $1::uuid", string(messageID)); err != nil {
		return nil, fmt.Errorf("delete Inbox message: %w", err)
	}
	tag, err := transaction.Exec(ctx, `
		DELETE FROM mail_contents AS content
		WHERE content.content_key = $1
		  AND NOT EXISTS (SELECT 1 FROM messages WHERE messages.content_key = content.content_key)
	`, ref.Key)
	if err != nil {
		return nil, fmt.Errorf("delete unreferenced message content: %w", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit message deletion: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, nil
	}
	return &ref, nil
}

func rollbackTransaction(transaction pgx.Tx) {
	rollbackContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = transaction.Rollback(rollbackContext)
}

var _ mailbox.Repository = (*MailboxRepository)(nil)
