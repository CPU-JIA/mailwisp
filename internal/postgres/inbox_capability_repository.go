package postgres

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"mailwisp/internal/auth"
)

// InboxCapabilityRepository persists digest-only Inbox capability credentials.
type InboxCapabilityRepository struct {
	pool *pgxpool.Pool
}

// NewInboxCapabilityRepository constructs a PostgreSQL capability repository.
func NewInboxCapabilityRepository(pool *pgxpool.Pool) (*InboxCapabilityRepository, error) {
	if pool == nil {
		return nil, errors.New("postgres pool is required")
	}
	return &InboxCapabilityRepository{pool: pool}, nil
}

// CreateCapability inserts a capability only for an active, unexpired Inbox
// and never persists plaintext token material.
func (r *InboxCapabilityRepository) CreateCapability(ctx context.Context, capability auth.NewCapability) (auth.CapabilityRecord, error) {
	if err := validateNewCapability(capability); err != nil {
		return auth.CapabilityRecord{}, err
	}
	var record auth.CapabilityRecord
	var digest []byte
	var scopeMask int64
	err := r.pool.QueryRow(ctx, `
		INSERT INTO inbox_capabilities (
			inbox_id, kid, secret_digest, scope_mask, created_at, expires_at
		)
		SELECT $1::uuid, $2, $3, $4, $5, $6
		FROM inboxes
		WHERE id = $1::uuid
		  AND status = 'active'
		  AND (expires_at IS NULL OR expires_at > $5)
		  AND (expires_at IS NULL OR $6 <= expires_at)
		RETURNING id::text, inbox_id::text, kid, secret_digest, scope_mask,
		          created_at, expires_at, last_used_at, revoked_at
	`,
		string(capability.InboxID),
		capability.KID,
		capability.Digest[:],
		int64(capability.Scopes.Mask()),
		capability.CreatedAt.UTC(),
		capability.ExpiresAt.UTC(),
	).Scan(
		&record.ID,
		&record.InboxID,
		&record.KID,
		&digest,
		&scopeMask,
		&record.CreatedAt,
		&record.ExpiresAt,
		&record.LastUsedAt,
		&record.RevokedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.CapabilityRecord{}, r.mapUnavailableInbox(ctx, capability)
	}
	if err != nil {
		return auth.CapabilityRecord{}, mapCapabilityWriteError("insert Inbox capability", err)
	}
	if err := hydrateCapabilityRecord(&record, digest, scopeMask); err != nil {
		return auth.CapabilityRecord{}, err
	}
	record.InboxActive = true
	return record, nil
}

// FindCapabilityByKID returns credential and current Inbox lifecycle state.
func (r *InboxCapabilityRepository) FindCapabilityByKID(ctx context.Context, kid string) (auth.CapabilityRecord, error) {
	if !validCapabilityKID(kid) {
		return auth.CapabilityRecord{}, auth.ErrCapabilityNotFound
	}
	var record auth.CapabilityRecord
	var digest []byte
	var scopeMask int64
	var inboxStatus string
	err := r.pool.QueryRow(ctx, `
		SELECT capability.id::text,
		       capability.inbox_id::text,
		       capability.kid,
		       capability.secret_digest,
		       capability.scope_mask,
		       capability.created_at,
		       capability.expires_at,
		       capability.last_used_at,
		       capability.revoked_at,
		       inbox.status,
		       inbox.expires_at
		FROM inbox_capabilities AS capability
		JOIN inboxes AS inbox ON inbox.id = capability.inbox_id
		WHERE capability.kid = $1
	`, kid).Scan(
		&record.ID,
		&record.InboxID,
		&record.KID,
		&digest,
		&scopeMask,
		&record.CreatedAt,
		&record.ExpiresAt,
		&record.LastUsedAt,
		&record.RevokedAt,
		&inboxStatus,
		&record.InboxExpiresAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.CapabilityRecord{}, auth.ErrCapabilityNotFound
	}
	if err != nil {
		return auth.CapabilityRecord{}, fmt.Errorf("find Inbox capability by KID: %w", err)
	}
	if err := hydrateCapabilityRecord(&record, digest, scopeMask); err != nil {
		return auth.CapabilityRecord{}, err
	}
	record.InboxActive = inboxStatus == "active"
	return record, nil
}

// RotateCapability atomically revokes one current credential and creates its replacement.
func (r *InboxCapabilityRepository) RotateCapability(
	ctx context.Context,
	currentID auth.CredentialID,
	replacement auth.ReplacementCapability,
	rotatedAt time.Time,
) (auth.CapabilityRecord, error) {
	if _, err := uuid.Parse(string(currentID)); err != nil {
		return auth.CapabilityRecord{}, auth.ErrCapabilityNotFound
	}
	if err := validateReplacementCapability(replacement, rotatedAt); err != nil {
		return auth.CapabilityRecord{}, err
	}
	transaction, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return auth.CapabilityRecord{}, fmt.Errorf("begin capability rotation: %w", err)
	}
	defer func() {
		rollbackContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = transaction.Rollback(rollbackContext)
	}()

	var current auth.CapabilityRecord
	var scopeMask int64
	var inboxStatus string
	err = transaction.QueryRow(ctx, `
		SELECT capability.id::text,
		       capability.inbox_id::text,
		       capability.scope_mask,
		       capability.created_at,
		       capability.expires_at,
		       capability.revoked_at,
		       inbox.status,
		       inbox.expires_at
		FROM inbox_capabilities AS capability
		JOIN inboxes AS inbox ON inbox.id = capability.inbox_id
		WHERE capability.id = $1::uuid
		FOR UPDATE OF capability
	`, string(currentID)).Scan(
		&current.ID,
		&current.InboxID,
		&scopeMask,
		&current.CreatedAt,
		&current.ExpiresAt,
		&current.RevokedAt,
		&inboxStatus,
		&current.InboxExpiresAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.CapabilityRecord{}, auth.ErrCapabilityNotFound
	}
	if err != nil {
		return auth.CapabilityRecord{}, fmt.Errorf("lock current Inbox capability: %w", err)
	}
	if current.RevokedAt != nil {
		return auth.CapabilityRecord{}, auth.ErrCapabilityAlreadyRotated
	}
	rotatedAt = rotatedAt.UTC()
	if inboxStatus != "active" || !rotatedAt.Before(current.ExpiresAt.UTC()) ||
		(current.InboxExpiresAt != nil && !rotatedAt.Before(current.InboxExpiresAt.UTC())) {
		return auth.CapabilityRecord{}, auth.ErrUnauthenticated
	}
	scopes, err := scopeSetFromDatabase(scopeMask)
	if err != nil {
		return auth.CapabilityRecord{}, err
	}

	if _, err := transaction.Exec(ctx, `
		UPDATE inbox_capabilities
		SET revoked_at = $2
		WHERE id = $1::uuid AND revoked_at IS NULL
	`, string(currentID), rotatedAt); err != nil {
		return auth.CapabilityRecord{}, fmt.Errorf("revoke current Inbox capability: %w", err)
	}

	var created auth.CapabilityRecord
	var digest []byte
	var createdScopeMask int64
	err = transaction.QueryRow(ctx, `
		INSERT INTO inbox_capabilities (
			inbox_id, kid, secret_digest, scope_mask, created_at, expires_at, rotated_from_id
		)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7::uuid)
		RETURNING id::text, inbox_id::text, kid, secret_digest, scope_mask,
		          created_at, expires_at, last_used_at, revoked_at
	`,
		string(current.InboxID),
		replacement.KID,
		replacement.Digest[:],
		int64(scopes.Mask()),
		replacement.CreatedAt.UTC(),
		current.ExpiresAt.UTC(),
		string(currentID),
	).Scan(
		&created.ID,
		&created.InboxID,
		&created.KID,
		&digest,
		&createdScopeMask,
		&created.CreatedAt,
		&created.ExpiresAt,
		&created.LastUsedAt,
		&created.RevokedAt,
	)
	if err != nil {
		return auth.CapabilityRecord{}, mapCapabilityWriteError("insert replacement Inbox capability", err)
	}
	if err := hydrateCapabilityRecord(&created, digest, createdScopeMask); err != nil {
		return auth.CapabilityRecord{}, err
	}
	created.InboxActive = true
	created.InboxExpiresAt = current.InboxExpiresAt
	if err := transaction.Commit(ctx); err != nil {
		return auth.CapabilityRecord{}, fmt.Errorf("commit capability rotation: %w", err)
	}
	return created, nil
}

// RevokeCapability idempotently marks one persisted credential revoked.
func (r *InboxCapabilityRepository) RevokeCapability(ctx context.Context, id auth.CredentialID, revokedAt time.Time) error {
	if _, err := uuid.Parse(string(id)); err != nil {
		return auth.ErrCapabilityNotFound
	}
	if revokedAt.IsZero() {
		return errors.New("capability revocation time is required")
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE inbox_capabilities
		SET revoked_at = COALESCE(revoked_at, $2)
		WHERE id = $1::uuid
	`, string(id), revokedAt.UTC())
	if err != nil {
		return fmt.Errorf("revoke Inbox capability: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return auth.ErrCapabilityNotFound
	}
	return nil
}

func (r *InboxCapabilityRepository) mapUnavailableInbox(ctx context.Context, capability auth.NewCapability) error {
	var status string
	var inboxExpiresAt *time.Time
	err := r.pool.QueryRow(ctx, `
		SELECT status, expires_at
		FROM inboxes
		WHERE id = $1::uuid
	`, string(capability.InboxID)).Scan(&status, &inboxExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.ErrCapabilitySubjectUnavailable
	}
	if err != nil {
		return fmt.Errorf("inspect unavailable capability Inbox: %w", err)
	}
	if status == "active" && inboxExpiresAt != nil {
		if !inboxExpiresAt.UTC().After(capability.CreatedAt.UTC()) {
			return auth.ErrCapabilitySubjectUnavailable
		}
		if capability.ExpiresAt.After(inboxExpiresAt.UTC()) {
			return auth.ErrCapabilityLifetime
		}
	}
	return auth.ErrCapabilitySubjectUnavailable
}

func validateNewCapability(capability auth.NewCapability) error {
	if _, err := uuid.Parse(string(capability.InboxID)); err != nil {
		return auth.ErrCapabilitySubjectUnavailable
	}
	if !validCapabilityKID(capability.KID) {
		return errors.New("invalid capability KID")
	}
	if _, err := auth.ScopeSetFromMask(capability.Scopes.Mask()); err != nil {
		return err
	}
	if capability.CreatedAt.IsZero() || capability.ExpiresAt.IsZero() || !capability.ExpiresAt.After(capability.CreatedAt) {
		return auth.ErrCapabilityLifetime
	}
	return nil
}

func validateReplacementCapability(replacement auth.ReplacementCapability, rotatedAt time.Time) error {
	if !validCapabilityKID(replacement.KID) {
		return errors.New("invalid replacement capability KID")
	}
	if replacement.CreatedAt.IsZero() || rotatedAt.IsZero() || !replacement.CreatedAt.Equal(rotatedAt) {
		return errors.New("replacement capability creation and rotation times must match")
	}
	return nil
}

func validCapabilityKID(kid string) bool {
	if len(kid) != 24 {
		return false
	}
	for _, character := range kid {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func hydrateCapabilityRecord(record *auth.CapabilityRecord, digest []byte, scopeMask int64) error {
	if len(digest) != len(record.Digest) {
		return errors.New("persisted capability digest has invalid length")
	}
	if scopeMask <= 0 || scopeMask > math.MaxUint32 {
		return errors.New("persisted capability scope mask is invalid")
	}
	scopes, err := scopeSetFromDatabase(scopeMask)
	if err != nil {
		return err
	}
	copy(record.Digest[:], digest)
	record.Scopes = scopes
	record.CreatedAt = record.CreatedAt.UTC()
	record.ExpiresAt = record.ExpiresAt.UTC()
	if record.LastUsedAt != nil {
		value := record.LastUsedAt.UTC()
		record.LastUsedAt = &value
	}
	if record.RevokedAt != nil {
		value := record.RevokedAt.UTC()
		record.RevokedAt = &value
	}
	if record.InboxExpiresAt != nil {
		value := record.InboxExpiresAt.UTC()
		record.InboxExpiresAt = &value
	}
	return nil
}

func scopeSetFromDatabase(mask int64) (auth.ScopeSet, error) {
	if mask <= 0 || mask > math.MaxUint32 {
		return 0, errors.New("persisted capability scope mask is invalid")
	}
	return auth.ScopeSetFromMask(uint32(mask))
}

func mapCapabilityWriteError(operation string, err error) error {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.Code == "23505" {
		switch postgresError.ConstraintName {
		case "inbox_capabilities_kid_key":
			return auth.ErrCapabilityKIDConflict
		case "inbox_capabilities_rotated_from_id_key":
			return auth.ErrCapabilityAlreadyRotated
		}
	}
	return fmt.Errorf("%s: %w", operation, err)
}

var _ auth.CapabilityRepository = (*InboxCapabilityRepository)(nil)
