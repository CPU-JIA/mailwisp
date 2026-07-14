package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"mailwisp/internal/jobs"
	"mailwisp/internal/mail"
)

// ContentParseRepository persists fenced content-level parse claims and outcomes.
type ContentParseRepository struct {
	pool *pgxpool.Pool
}

// NewContentParseRepository constructs a PostgreSQL content parse repository.
func NewContentParseRepository(pool *pgxpool.Pool) (*ContentParseRepository, error) {
	if pool == nil {
		return nil, errors.New("postgres pool is required")
	}
	return &ContentParseRepository{pool: pool}, nil
}

// ClaimContent leases one available content row without blocking other workers.
func (r *ContentParseRepository) ClaimContent(ctx context.Context, leaseDuration time.Duration) (jobs.ParseClaim, bool, error) {
	if leaseDuration <= 0 {
		return jobs.ParseClaim{}, false, errors.New("content parse lease duration must be positive")
	}
	leaseUUID, err := uuid.NewRandom()
	if err != nil {
		return jobs.ParseClaim{}, false, fmt.Errorf("generate content parse lease token: %w", err)
	}
	leaseToken := leaseUUID.String()
	var claim jobs.ParseClaim
	err = r.pool.QueryRow(ctx, `
		WITH candidate AS (
			SELECT content_key
			FROM mail_contents
			WHERE (parse_status = 'pending' AND parse_available_at <= now())
			   OR (parse_status = 'processing' AND parse_lease_until <= now())
			ORDER BY parse_available_at, created_at, content_key
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE mail_contents AS content
		SET parse_status = 'processing',
			parse_attempts = content.parse_attempts + 1,
			parse_lease_token = $1::uuid,
			parse_lease_until = now() + ($2 * interval '1 microsecond'),
			parse_updated_at = now()
		FROM candidate
		WHERE content.content_key = candidate.content_key
		RETURNING content.content_key, content.size_bytes, content.parse_lease_token::text, content.parse_attempts
	`, leaseToken, leaseDuration.Microseconds()).Scan(
		&claim.Content.Key,
		&claim.Content.SizeBytes,
		&claim.LeaseToken,
		&claim.Attempt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return jobs.ParseClaim{}, false, nil
	}
	if err != nil {
		return jobs.ParseClaim{}, false, fmt.Errorf("claim content parse work: %w", err)
	}
	return claim, true, nil
}

// CompleteContent atomically persists one parse result and marks its content parsed.
func (r *ContentParseRepository) CompleteContent(
	ctx context.Context,
	claim jobs.ParseClaim,
	parserRevision int,
	parsed mail.ParsedMessage,
	parsedAt time.Time,
) error {
	if err := validateParseClaim(claim); err != nil {
		return err
	}
	if parserRevision <= 0 {
		return errors.New("parser revision must be positive")
	}
	if parsedAt.IsZero() {
		return errors.New("parsed time is required")
	}
	fromAddresses, err := marshalJSONArray(parsed.From)
	if err != nil {
		return fmt.Errorf("marshal parsed From addresses: %w", err)
	}
	toAddresses, err := marshalJSONArray(parsed.To)
	if err != nil {
		return fmt.Errorf("marshal parsed To addresses: %w", err)
	}
	ccAddresses, err := marshalJSONArray(parsed.Cc)
	if err != nil {
		return fmt.Errorf("marshal parsed Cc addresses: %w", err)
	}
	attachments, err := marshalJSONArray(parsed.Attachments)
	if err != nil {
		return fmt.Errorf("marshal parsed attachments: %w", err)
	}
	warnings, err := marshalJSONArray(parsed.Warnings)
	if err != nil {
		return fmt.Errorf("marshal parser warnings: %w", err)
	}

	transaction, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("begin content parse completion: %w", err)
	}
	defer func() {
		rollbackContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = transaction.Rollback(rollbackContext)
	}()

	tag, err := transaction.Exec(ctx, `
		UPDATE mail_contents
		SET parse_status = 'parsed',
			parse_lease_token = NULL,
			parse_lease_until = NULL,
			parse_error_code = NULL,
			parse_updated_at = $3
		WHERE content_key = $1
		  AND parse_status = 'processing'
		  AND parse_lease_token = $2::uuid
	`, claim.Content.Key, claim.LeaseToken, parsedAt.UTC())
	if err != nil {
		return fmt.Errorf("fence content parse completion: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return jobs.ErrStaleParseClaim
	}

	_, err = transaction.Exec(ctx, `
		INSERT INTO mail_content_parses (
			content_key, parser_revision, subject, header_message_id,
			from_addresses, to_addresses, cc_addresses, sent_at,
			text_body, html_source, attachments, warnings, parsed_at
		)
		VALUES (
			$1, $2, $3, $4,
			$5::jsonb, $6::jsonb, $7::jsonb, $8,
			$9, $10, $11::jsonb, $12::jsonb, $13
		)
		ON CONFLICT (content_key) DO UPDATE SET
			parser_revision = EXCLUDED.parser_revision,
			subject = EXCLUDED.subject,
			header_message_id = EXCLUDED.header_message_id,
			from_addresses = EXCLUDED.from_addresses,
			to_addresses = EXCLUDED.to_addresses,
			cc_addresses = EXCLUDED.cc_addresses,
			sent_at = EXCLUDED.sent_at,
			text_body = EXCLUDED.text_body,
			html_source = EXCLUDED.html_source,
			attachments = EXCLUDED.attachments,
			warnings = EXCLUDED.warnings,
			parsed_at = EXCLUDED.parsed_at
	`,
		claim.Content.Key,
		parserRevision,
		parsed.Subject,
		parsed.MessageID,
		fromAddresses,
		toAddresses,
		ccAddresses,
		parsed.Date,
		parsed.Text,
		string(parsed.HTMLSource),
		attachments,
		warnings,
		parsedAt.UTC(),
	)
	if err != nil {
		return fmt.Errorf("persist parsed content: %w", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit parsed content: %w", err)
	}
	return nil
}

// RetryContent returns one transiently failed claim to the durable queue.
func (r *ContentParseRepository) RetryContent(ctx context.Context, claim jobs.ParseClaim, code jobs.ParseErrorCode, availableAt time.Time) error {
	if availableAt.IsZero() {
		return errors.New("content parse retry time is required")
	}
	return r.finishClaim(ctx, claim, "pending", code, availableAt.UTC(), false)
}

// FailContent records one terminal parser outcome without deleting Raw MIME.
func (r *ContentParseRepository) FailContent(ctx context.Context, claim jobs.ParseClaim, code jobs.ParseErrorCode, failedAt time.Time) error {
	if failedAt.IsZero() {
		return errors.New("content parse failure time is required")
	}
	return r.finishClaim(ctx, claim, "failed", code, failedAt.UTC(), false)
}

// ReleaseContent promptly returns canceled work without consuming an attempt.
func (r *ContentParseRepository) ReleaseContent(ctx context.Context, claim jobs.ParseClaim, availableAt time.Time) error {
	if availableAt.IsZero() {
		return errors.New("content parse release time is required")
	}
	return r.finishClaim(ctx, claim, "pending", "", availableAt.UTC(), true)
}

func (r *ContentParseRepository) finishClaim(
	ctx context.Context,
	claim jobs.ParseClaim,
	status string,
	code jobs.ParseErrorCode,
	availableAt time.Time,
	decrementAttempt bool,
) error {
	if err := validateParseClaim(claim); err != nil {
		return err
	}
	if status != "pending" && status != "failed" {
		return errors.New("invalid content parse finish status")
	}
	if status == "failed" && code == "" {
		return errors.New("terminal content parse failure code is required")
	}
	if code != "" && !validParseErrorCode(code) {
		return errors.New("invalid content parse error code")
	}
	var errorCode any
	if code != "" {
		errorCode = string(code)
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE mail_contents
		SET parse_status = $3,
			parse_attempts = CASE WHEN $6 THEN GREATEST(parse_attempts - 1, 0) ELSE parse_attempts END,
			parse_available_at = $4,
			parse_lease_token = NULL,
			parse_lease_until = NULL,
			parse_error_code = $5,
			parse_updated_at = now()
		WHERE content_key = $1
		  AND parse_status = 'processing'
		  AND parse_lease_token = $2::uuid
	`, claim.Content.Key, claim.LeaseToken, status, availableAt, errorCode, decrementAttempt)
	if err != nil {
		return fmt.Errorf("finish content parse claim: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return jobs.ErrStaleParseClaim
	}
	return nil
}

func validParseErrorCode(code jobs.ParseErrorCode) bool {
	value := string(code)
	if len(value) == 0 || len(value) > 64 || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, character := range value[1:] {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '_' {
			return false
		}
	}
	return true
}

func validateParseClaim(claim jobs.ParseClaim) error {
	if err := claim.Content.Validate(); err != nil {
		return err
	}
	if _, err := uuid.Parse(claim.LeaseToken); err != nil {
		return errors.New("content parse lease token must be a UUID")
	}
	if claim.Attempt <= 0 {
		return errors.New("content parse attempt must be positive")
	}
	return nil
}

func marshalJSONArray[T any](values []T) (string, error) {
	if values == nil {
		values = []T{}
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

var _ jobs.ParseQueue = (*ContentParseRepository)(nil)
