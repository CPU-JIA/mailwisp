//go:build integration

package postgres

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"mailwisp/internal/auth"
	"mailwisp/internal/contentstore"
	"mailwisp/internal/mailbox"
	"mailwisp/internal/message"
)

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
	if err != nil || len(listed) != 1 || listed[0].ParseStatus != "pending" {
		t.Fatalf("ListMessages() = %+v, error = %v", listed, err)
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
