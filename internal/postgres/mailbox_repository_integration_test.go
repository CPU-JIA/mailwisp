//go:build integration

package postgres

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"mailwisp/internal/auth"
	"mailwisp/internal/contentstore"
	"mailwisp/internal/duckmail"
	"mailwisp/internal/mailbox"
	"mailwisp/internal/message"
	"mailwisp/migrations"
)

func TestDuckMailMigrationUpgradesVersionThreeData(t *testing.T) {
	dropIntegrationSchema(t)
	t.Cleanup(func() { recreateIntegrationSchema(t) })
	config, err := pgx.ParseConfig(integrationDataSourceName)
	if err != nil {
		t.Fatal(err)
	}
	database := stdlib.OpenDB(*config)
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = database.Close() })
	provider, err := goose.NewProvider(goose.DialectPostgres, database, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(context.Background(), 3); err != nil {
		t.Fatalf("apply migrations through version 3: %v", err)
	}
	if _, err := database.ExecContext(context.Background(), `INSERT INTO inboxes (address) VALUES ('v3@mailwisp.test')`); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(context.Background(), 4); err != nil {
		t.Fatalf("apply migration 4: %v", err)
	}
	var inboxCount, credentialTable, seenColumn int
	if err := database.QueryRowContext(context.Background(), `
		SELECT
		  (SELECT count(*) FROM inboxes WHERE address = 'v3@mailwisp.test'),
		  (SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name = 'duckmail_credentials'),
		  (SELECT count(*) FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'messages' AND column_name = 'seen_at')
	`).Scan(&inboxCount, &credentialTable, &seenColumn); err != nil {
		t.Fatal(err)
	}
	if inboxCount != 1 || credentialTable != 1 || seenColumn != 1 {
		t.Fatalf("migration counts = inbox %d table %d column %d", inboxCount, credentialTable, seenColumn)
	}
}

func TestMailboxServiceCreatesAuthenticatableInbox(t *testing.T) {
	pool := newIntegrationPool(t)
	resetIntegrationDatabase(t, pool)
	repository, err := NewMailboxRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	capabilityRepository, err := NewInboxCapabilityRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	capabilities, err := auth.NewCapabilityService(capabilityRepository)
	if err != nil {
		t.Fatal(err)
	}
	store, err := contentstore.Open(t.TempDir(), contentstore.Options{MaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	service, err := mailbox.NewService(repository, capabilities, store, mailbox.Options{
		PublicDomains: []string{"mailwisp.test"}, DefaultTTL: time.Hour, MaxTTL: 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(context.Background(), mailbox.CreateRequest{})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	principal, err := capabilities.Authenticate(context.Background(), created.Capability.Plaintext, auth.ScopeInboxRead, auth.ScopeMessageRead)
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if principal.InboxID != created.Inbox.ID {
		t.Fatalf("principal Inbox = %q, want %q", principal.InboxID, created.Inbox.ID)
	}
	if _, err := service.Get(context.Background(), principal.InboxID); err != nil {
		t.Fatalf("Get() error = %v", err)
	}
}

func TestDuckMailRepositoryPasswordLoginIssuesCanonicalCapability(t *testing.T) {
	pool := newIntegrationPool(t)
	resetIntegrationDatabase(t, pool)
	mailboxRepository, _ := NewMailboxRepository(pool)
	capabilityRepository, _ := NewInboxCapabilityRepository(pool)
	capabilities, _ := auth.NewCapabilityService(capabilityRepository)
	store, err := contentstore.Open(t.TempDir(), contentstore.Options{MaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	mailboxes, err := mailbox.NewService(mailboxRepository, capabilities, store, mailbox.Options{PublicDomains: []string{"mailwisp.test"}, DefaultTTL: time.Hour, MaxTTL: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	duckRepository, err := NewDuckMailRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := duckmail.NewService(duckRepository, mailboxes, capabilities, duckmail.Options{PublicDomains: []string{"mailwisp.test"}, DefaultTTL: time.Hour, MaxTTL: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	created, err := adapter.CreateAccount(context.Background(), duckmail.CreateAccountRequest{Address: "contract@mailwisp.test", Password: "secret-password"})
	if err != nil {
		t.Fatalf("CreateAccount() error = %v", err)
	}
	issued, err := adapter.Login(context.Background(), created.Address, "secret-password")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	principal, err := capabilities.Authenticate(context.Background(), issued.Plaintext, auth.ScopeMessageUpdate)
	if err != nil || principal.InboxID != created.ID {
		t.Fatalf("Authenticate() = %+v, error = %v", principal, err)
	}
}

func TestMailboxRepositoryEnforcesOwnershipAndDeletesUnreferencedContent(t *testing.T) {
	pool := newIntegrationPool(t)
	resetIntegrationDatabase(t, pool)
	repository, err := NewMailboxRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	first, err := repository.CreateInbox(context.Background(), mailbox.NewInbox{Address: "first@mailwisp.test", CreatedAt: now, ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	second, err := repository.CreateInbox(context.Background(), mailbox.NewInbox{Address: "second@mailwisp.test", CreatedAt: now, ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	ref := message.ContentRef{Key: contentKey("e"), SizeBytes: 128}
	if _, err := pool.Exec(context.Background(), "INSERT INTO mail_contents (content_key, size_bytes) VALUES ($1, $2)", ref.Key, ref.SizeBytes); err != nil {
		t.Fatal(err)
	}
	var messageID message.MessageID
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO messages (inbox_id, content_key, envelope_sender, received_at)
		VALUES ($1::uuid, $2, 'sender@example.net', $3)
		RETURNING id::text
	`, string(first.ID), ref.Key, now).Scan(&messageID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.GetMessage(context.Background(), second.ID, messageID); !errors.Is(err, mailbox.ErrMessageNotFound) {
		t.Fatalf("cross-Inbox GetMessage() error = %v", err)
	}
	listed, err := repository.ListMessages(context.Background(), first.ID, mailbox.Page{Limit: 10})
	if err != nil || len(listed.Items) != 1 || listed.Total != 1 || listed.Items[0].ParseStatus != "pending" {
		t.Fatalf("ListMessages() = %+v, error = %v", listed, err)
	}
	if err := repository.MarkMessageSeen(context.Background(), first.ID, messageID, now.Add(time.Minute)); err != nil {
		t.Fatalf("MarkMessageSeen() error = %v", err)
	}
	detail, err := repository.GetMessage(context.Background(), first.ID, messageID)
	if err != nil || !detail.Seen {
		t.Fatalf("GetMessage(seen) = %+v, error = %v", detail, err)
	}
	deletedRef, err := repository.DeleteMessage(context.Background(), first.ID, messageID)
	if err != nil {
		t.Fatalf("DeleteMessage() error = %v", err)
	}
	if deletedRef == nil || deletedRef.Key != ref.Key {
		t.Fatalf("DeleteMessage() ref = %+v", deletedRef)
	}
	var contentCount int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM mail_contents WHERE content_key = $1", ref.Key).Scan(&contentCount); err != nil {
		t.Fatal(err)
	}
	if contentCount != 0 {
		t.Fatalf("content count = %d, want 0", contentCount)
	}
}

func TestMailboxDeleteKeepsContentSharedWithAnotherInbox(t *testing.T) {
	pool := newIntegrationPool(t)
	resetIntegrationDatabase(t, pool)
	repository, _ := NewMailboxRepository(pool)
	now := time.Now().UTC()
	first, _ := repository.CreateInbox(context.Background(), mailbox.NewInbox{Address: "shared-first@mailwisp.test", CreatedAt: now, ExpiresAt: now.Add(time.Hour)})
	second, _ := repository.CreateInbox(context.Background(), mailbox.NewInbox{Address: "shared-second@mailwisp.test", CreatedAt: now, ExpiresAt: now.Add(time.Hour)})
	ref := message.ContentRef{Key: contentKey("d"), SizeBytes: 4}
	if _, err := pool.Exec(context.Background(), "INSERT INTO mail_contents (content_key, size_bytes) VALUES ($1, $2)", ref.Key, ref.SizeBytes); err != nil {
		t.Fatal(err)
	}
	for _, inboxID := range []message.InboxID{first.ID, second.ID} {
		if _, err := pool.Exec(context.Background(), `
			INSERT INTO messages (inbox_id, content_key, envelope_sender, received_at)
			VALUES ($1::uuid, $2, '', $3)
		`, string(inboxID), ref.Key, now); err != nil {
			t.Fatal(err)
		}
	}
	orphans, err := repository.DeleteInbox(context.Background(), first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(orphans) != 0 {
		t.Fatalf("orphans = %+v, want none", orphans)
	}
	var remaining int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM messages WHERE content_key = $1", ref.Key).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 1 {
		t.Fatalf("remaining messages = %d, want 1", remaining)
	}

	store, err := contentstore.Open(t.TempDir(), contentstore.Options{MaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.Put(context.Background(), bytes.NewReader([]byte("mail")))
	if err != nil || stored.SizeBytes != 4 {
		t.Fatalf("content fixture = %+v, error = %v", stored, err)
	}
}
