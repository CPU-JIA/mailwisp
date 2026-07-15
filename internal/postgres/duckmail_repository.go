package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"mailwisp/internal/duckmail"
	"mailwisp/internal/mailbox"
)

// DuckMailRepository persists compatibility-only password credentials.
type DuckMailRepository struct{ pool *pgxpool.Pool }

// NewDuckMailRepository constructs a DuckMail compatibility repository.
func NewDuckMailRepository(pool *pgxpool.Pool) (*DuckMailRepository, error) {
	if pool == nil {
		return nil, errors.New("postgres pool is required")
	}
	return &DuckMailRepository{pool: pool}, nil
}

// CreateAccount atomically creates an exact Inbox address and password credential.
func (r *DuckMailRepository) CreateAccount(ctx context.Context, account duckmail.NewAccount) (mailbox.Inbox, error) {
	transaction, err := r.pool.Begin(ctx)
	if err != nil {
		return mailbox.Inbox{}, fmt.Errorf("begin DuckMail account creation: %w", err)
	}
	defer rollbackTransaction(transaction)
	var inbox mailbox.Inbox
	err = transaction.QueryRow(ctx, `
		INSERT INTO inboxes (address, status, expires_at, created_at, updated_at)
		VALUES ($1, 'active', $2, $3, $3)
		RETURNING id::text, address, status, expires_at, created_at
	`, account.Address, account.ExpiresAt.UTC(), account.CreatedAt.UTC()).Scan(
		&inbox.ID, &inbox.Address, &inbox.Status, &inbox.ExpiresAt, &inbox.CreatedAt,
	)
	if err != nil {
		var postgresError *pgconn.PgError
		if errors.As(err, &postgresError) && postgresError.Code == "23505" {
			return mailbox.Inbox{}, duckmail.ErrAccountConflict
		}
		return mailbox.Inbox{}, fmt.Errorf("insert DuckMail Inbox: %w", err)
	}
	if _, err := transaction.Exec(ctx, `
		INSERT INTO duckmail_credentials (inbox_id, password_hash, created_at, updated_at)
		VALUES ($1::uuid, $2, $3, $3)
	`, string(inbox.ID), account.PasswordHash, account.CreatedAt.UTC()); err != nil {
		return mailbox.Inbox{}, fmt.Errorf("insert DuckMail credential: %w", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return mailbox.Inbox{}, fmt.Errorf("commit DuckMail account creation: %w", err)
	}
	return inbox, nil
}

// FindAccountByAddress returns only active, unexpired DuckMail accounts.
func (r *DuckMailRepository) FindAccountByAddress(ctx context.Context, address string) (duckmail.Account, error) {
	var account duckmail.Account
	err := r.pool.QueryRow(ctx, `
		SELECT inbox.id::text, inbox.address, inbox.status, inbox.expires_at, inbox.created_at, credential.password_hash
		FROM duckmail_credentials AS credential
		JOIN inboxes AS inbox ON inbox.id = credential.inbox_id
		WHERE inbox.address = $1
		  AND inbox.status = 'active'
		  AND inbox.expires_at > now()
	`, address).Scan(
		&account.Inbox.ID, &account.Inbox.Address, &account.Inbox.Status, &account.Inbox.ExpiresAt, &account.Inbox.CreatedAt, &account.PasswordHash,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return duckmail.Account{}, duckmail.ErrLoginFailed
	}
	if err != nil {
		return duckmail.Account{}, fmt.Errorf("find DuckMail account: %w", err)
	}
	return account, nil
}

var _ duckmail.Repository = (*DuckMailRepository)(nil)
