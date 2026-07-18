//go:build integration

package postgres

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"mailwisp/internal/auth"
	"mailwisp/internal/cloudflaretemp"
	"mailwisp/internal/contentstore"
	"mailwisp/internal/duckmail"
	"mailwisp/internal/mailbox"
	"mailwisp/internal/message"
	"mailwisp/internal/yyds"
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

func TestCloudflareTempMigrationUpgradesVersionFourData(t *testing.T) {
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
	if _, err := provider.UpTo(context.Background(), 4); err != nil {
		t.Fatalf("apply migrations through version 4: %v", err)
	}
	if _, err := database.ExecContext(context.Background(), `INSERT INTO inboxes (address) VALUES ('v4@mailwisp.test')`); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(context.Background(), 5); err != nil {
		t.Fatalf("apply migration 5: %v", err)
	}
	var inboxCount, inboxIDTable, messageIDTable int
	if err := database.QueryRowContext(context.Background(), `
		SELECT
		  (SELECT count(*) FROM inboxes WHERE address = 'v4@mailwisp.test'),
		  (SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name = 'cloudflare_temp_inbox_ids'),
		  (SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name = 'cloudflare_temp_message_ids')
	`).Scan(&inboxCount, &inboxIDTable, &messageIDTable); err != nil {
		t.Fatal(err)
	}
	if inboxCount != 1 || inboxIDTable != 1 || messageIDTable != 1 {
		t.Fatalf("migration counts = inbox %d tables %d/%d", inboxCount, inboxIDTable, messageIDTable)
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

func TestYYDSCreatesExactAddressAndAtomicallyRotatesCapability(t *testing.T) {
	pool := newIntegrationPool(t)
	resetIntegrationDatabase(t, pool)
	mailboxRepository, err := NewMailboxRepository(pool)
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
	mailboxes, err := mailbox.NewService(mailboxRepository, capabilities, store, mailbox.Options{
		PublicDomains: []string{"mailwisp.test"}, DefaultTTL: time.Hour, MaxTTL: 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := yyds.NewService(mailboxes, capabilities, []string{"mailwisp.test"})
	if err != nil {
		t.Fatal(err)
	}

	created, err := adapter.CreateAccount(context.Background(), yyds.CreateAccountRequest{LocalPart: "contract", Domain: "mailwisp.test"})
	if err != nil {
		t.Fatalf("CreateAccount() error = %v", err)
	}
	if created.Inbox.Address != "contract@mailwisp.test" {
		t.Fatalf("created address = %q", created.Inbox.Address)
	}
	oldToken := created.Capability.Plaintext
	principal, err := capabilities.Authenticate(context.Background(), oldToken, auth.ScopeMessageUpdate)
	if err != nil || principal.InboxID != created.Inbox.ID {
		t.Fatalf("Authenticate(old) = %+v, error = %v", principal, err)
	}

	rotated, inbox, err := adapter.RefreshToken(context.Background(), oldToken, created.Inbox.Address)
	if err != nil {
		t.Fatalf("RefreshToken() error = %v", err)
	}
	if inbox.ID != created.Inbox.ID || rotated.InboxID != created.Inbox.ID || rotated.Plaintext == oldToken {
		t.Fatalf("rotation = inbox %+v, capability %+v", inbox, rotated)
	}
	if _, err := capabilities.Authenticate(context.Background(), oldToken, auth.ScopeInboxRead); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("Authenticate(old after rotation) error = %v", err)
	}
	newPrincipal, err := capabilities.Authenticate(context.Background(), rotated.Plaintext, auth.ScopeMessageUpdate)
	if err != nil || newPrincipal.InboxID != created.Inbox.ID {
		t.Fatalf("Authenticate(rotated) = %+v, error = %v", newPrincipal, err)
	}
}

func TestCloudflareTempCreatesAddressAndAssignsStableOwnedIntegerIDs(t *testing.T) {
	pool := newIntegrationPool(t)
	resetIntegrationDatabase(t, pool)
	mailboxRepository, err := NewMailboxRepository(pool)
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
	mailboxes, err := mailbox.NewService(mailboxRepository, capabilities, store, mailbox.Options{
		PublicDomains: []string{"mailwisp.test"}, DefaultTTL: time.Hour, MaxTTL: 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	idRepository, err := NewCloudflareTempRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := cloudflaretemp.NewService(mailboxes, idRepository, []string{"mailwisp.test"})
	if err != nil {
		t.Fatal(err)
	}
	created, err := adapter.CreateAddress(context.Background(), "Contract.Name", "mailwisp.test")
	if err != nil {
		t.Fatalf("CreateAddress() error = %v", err)
	}
	if created.Inbox.Address != "contractname@mailwisp.test" || created.AddressID <= 0 {
		t.Fatalf("created = %+v", created)
	}
	principal, err := capabilities.Authenticate(context.Background(), created.Capability.Plaintext, auth.ScopeMessageRead)
	if err != nil || principal.InboxID != created.Inbox.ID {
		t.Fatalf("Authenticate() = %+v, error = %v", principal, err)
	}
	stableInboxID, err := idRepository.EnsureInboxID(context.Background(), created.Inbox.ID)
	if err != nil || stableInboxID != created.AddressID {
		t.Fatalf("EnsureInboxID() = %d, error = %v", stableInboxID, err)
	}

	ref := message.ContentRef{Key: contentKey("c"), SizeBytes: 12}
	if _, err := pool.Exec(context.Background(), "INSERT INTO mail_contents (content_key, size_bytes) VALUES ($1, $2)", ref.Key, ref.SizeBytes); err != nil {
		t.Fatal(err)
	}
	var canonicalMessageID message.MessageID
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO messages (inbox_id, content_key, envelope_sender, received_at)
		VALUES ($1::uuid, $2, 'sender@example.net', now())
		RETURNING id::text
	`, string(created.Inbox.ID), ref.Key).Scan(&canonicalMessageID); err != nil {
		t.Fatal(err)
	}
	mapped, err := idRepository.EnsureMessageIDs(context.Background(), created.Inbox.ID, []message.MessageID{canonicalMessageID})
	if err != nil || mapped[canonicalMessageID] <= 0 {
		t.Fatalf("EnsureMessageIDs() = %+v, error = %v", mapped, err)
	}
	stableMessageIDs, err := idRepository.EnsureMessageIDs(context.Background(), created.Inbox.ID, []message.MessageID{canonicalMessageID})
	if err != nil || stableMessageIDs[canonicalMessageID] != mapped[canonicalMessageID] {
		t.Fatalf("EnsureMessageIDs(stable) = %+v, error = %v", stableMessageIDs, err)
	}
	resolved, err := idRepository.FindMessageID(context.Background(), created.Inbox.ID, mapped[canonicalMessageID])
	if err != nil || resolved != canonicalMessageID {
		t.Fatalf("FindMessageID() = %q, error = %v", resolved, err)
	}
	other := createInbox(t, pool, "other@mailwisp.test")
	if _, err := idRepository.FindMessageID(context.Background(), other, mapped[canonicalMessageID]); !errors.Is(err, cloudflaretemp.ErrMessageIDNotFound) {
		t.Fatalf("cross-Inbox FindMessageID() error = %v", err)
	}
	if _, err := pool.Exec(context.Background(), "DELETE FROM messages WHERE id = $1::uuid", string(canonicalMessageID)); err != nil {
		t.Fatal(err)
	}
	if _, err := idRepository.FindMessageID(context.Background(), created.Inbox.ID, mapped[canonicalMessageID]); !errors.Is(err, cloudflaretemp.ErrMessageIDNotFound) {
		t.Fatalf("FindMessageID(after cascade) error = %v", err)
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
	if err != nil || len(listed.Items) != 1 || listed.Total != 1 || listed.Unread != 1 || listed.Items[0].ParseStatus != "pending" {
		t.Fatalf("ListMessages() = %+v, error = %v", listed, err)
	}
	if err := repository.MarkMessageSeen(context.Background(), first.ID, messageID, now.Add(time.Minute)); err != nil {
		t.Fatalf("MarkMessageSeen() error = %v", err)
	}
	listed, err = repository.ListMessages(context.Background(), first.ID, mailbox.Page{Limit: 10})
	if err != nil || listed.Unread != 0 {
		t.Fatalf("ListMessages(after seen) = %+v, error = %v", listed, err)
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
	deletions, err := repository.ListContentDeletions(context.Background(), 10)
	if err != nil || len(deletions) != 1 || deletions[0].Content != ref || deletions[0].Generation != 1 {
		t.Fatalf("ListContentDeletions() = %+v, %v", deletions, err)
	}
	if referenced, err := repository.ContentReferenced(context.Background(), ref.Key); err != nil || referenced {
		t.Fatalf("ContentReferenced() = %t, %v", referenced, err)
	}
	if acknowledged, err := repository.AcknowledgeContentDeletion(context.Background(), ref.Key, 2); err != nil || acknowledged {
		t.Fatalf("AcknowledgeContentDeletion(stale) = %t, %v", acknowledged, err)
	}
	if acknowledged, err := repository.AcknowledgeContentDeletion(context.Background(), ref.Key, 1); err != nil || !acknowledged {
		t.Fatalf("AcknowledgeContentDeletion(current) = %t, %v", acknowledged, err)
	}
}

func TestContentDeletionQueueGenerationPreventsStaleAcknowledgement(t *testing.T) {
	pool := newIntegrationPool(t)
	resetIntegrationDatabase(t, pool)
	repository, err := NewMailboxRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	ref := message.ContentRef{Key: contentKey("d"), SizeBytes: 64}
	for range 2 {
		transaction, err := pool.Begin(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if err := enqueueContentDeletion(context.Background(), transaction, ref); err != nil {
			_ = transaction.Rollback(context.Background())
			t.Fatal(err)
		}
		if err := transaction.Commit(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	deletions, err := repository.ListContentDeletions(context.Background(), 10)
	if err != nil || len(deletions) != 1 || deletions[0].Generation != 2 {
		t.Fatalf("ListContentDeletions() = %+v, %v", deletions, err)
	}
	if acknowledged, err := repository.AcknowledgeContentDeletion(context.Background(), ref.Key, 1); err != nil || acknowledged {
		t.Fatalf("AcknowledgeContentDeletion(old generation) = %t, %v", acknowledged, err)
	}
	if acknowledged, err := repository.AcknowledgeContentDeletion(context.Background(), ref.Key, 2); err != nil || !acknowledged {
		t.Fatalf("AcknowledgeContentDeletion(new generation) = %t, %v", acknowledged, err)
	}
}

func TestMailboxRepositoryCursorPaginationCoversFullInboxDuringConcurrentInsert(t *testing.T) {
	pool := newIntegrationPool(t)
	resetIntegrationDatabase(t, pool)
	repository, err := NewMailboxRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	inbox, err := repository.CreateInbox(context.Background(), mailbox.NewInbox{Address: "cursor@mailwisp.test", CreatedAt: now, ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	ref := message.ContentRef{Key: contentKey("c"), SizeBytes: 128}
	if _, err := pool.Exec(context.Background(), "INSERT INTO mail_contents (content_key, size_bytes) VALUES ($1, $2)", ref.Key, ref.SizeBytes); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO messages (inbox_id, content_key, envelope_sender, received_at)
		SELECT $1::uuid, $2, 'sender@example.net', $3::timestamptz - ((series / 5) * interval '1 second')
		FROM generate_series(1, 500) AS series
	`, string(inbox.ID), ref.Key, now); err != nil {
		t.Fatal(err)
	}

	seen := make(map[message.MessageID]struct{}, 500)
	var before *mailbox.MessageCursor
	var concurrentID message.MessageID
	for pageNumber := 0; pageNumber < 10; pageNumber++ {
		page, err := repository.ListMessages(context.Background(), inbox.ID, mailbox.Page{Limit: 100, Before: before})
		if err != nil {
			t.Fatalf("ListMessages(page %d) error = %v", pageNumber, err)
		}
		if len(page.Items) == 0 {
			break
		}
		if pageNumber == 0 {
			if err := pool.QueryRow(context.Background(), `
				INSERT INTO messages (inbox_id, content_key, envelope_sender, received_at)
				VALUES ($1::uuid, $2, 'newer@example.net', $3)
				RETURNING id::text
			`, string(inbox.ID), ref.Key, now.Add(time.Hour)).Scan(&concurrentID); err != nil {
				t.Fatal(err)
			}
		}
		for _, item := range page.Items {
			if item.ID == concurrentID {
				t.Fatalf("concurrent newer message %q leaked into an older cursor page", concurrentID)
			}
			if _, duplicate := seen[item.ID]; duplicate {
				t.Fatalf("message %q appeared in more than one cursor page", item.ID)
			}
			seen[item.ID] = struct{}{}
		}
		last := page.Items[len(page.Items)-1]
		before = &mailbox.MessageCursor{ReceivedAt: last.ReceivedAt, ID: last.ID}
		if pageNumber == 0 {
			if _, err := repository.DeleteMessage(context.Background(), inbox.ID, last.ID); err != nil {
				t.Fatalf("delete first-page cursor anchor: %v", err)
			}
		}
	}
	if len(seen) != 500 {
		t.Fatalf("cursor pagination returned %d original messages, want 500", len(seen))
	}
	latest, err := repository.ListMessages(context.Background(), inbox.ID, mailbox.Page{Limit: 1})
	if err != nil || len(latest.Items) != 1 || latest.Items[0].ID != concurrentID {
		t.Fatalf("latest page = %+v, error = %v, want concurrent message %q", latest, err, concurrentID)
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
	deletions, err := repository.ListContentDeletions(context.Background(), 10)
	if err != nil || len(deletions) != 0 {
		t.Fatalf("shared content deletion queue = %+v, %v, want empty", deletions, err)
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

func TestMailboxDeleteSerializesWithConcurrentDelivery(t *testing.T) {
	t.Run("delivery commits before deletion snapshot", func(t *testing.T) {
		pool := newIntegrationPool(t)
		resetIntegrationDatabase(t, pool)
		mailboxes, _ := NewMailboxRepository(pool)
		deliveries := newIntegrationRepository(t, pool)
		now := time.Now().UTC()
		inbox, _ := mailboxes.CreateInbox(context.Background(), mailbox.NewInbox{Address: "deliver-first@mailwisp.test", CreatedAt: now, ExpiresAt: now.Add(time.Hour)})
		ref := message.ContentRef{Key: contentKey("7"), SizeBytes: 7}
		beforeCommit := make(chan struct{})
		releaseCommit := make(chan struct{})
		deliveries.commitObserver = func(stage deliveryCommitStage) {
			if stage == deliveryCommitStageBefore {
				close(beforeCommit)
				<-releaseCommit
			}
		}
		deliveryResult := make(chan error, 1)
		go func() {
			_, err := deliveries.CommitDelivery(context.Background(), validDelivery(inbox.ID, ref.Key, ref.SizeBytes))
			deliveryResult <- err
		}()
		select {
		case <-beforeCommit:
		case <-time.After(5 * time.Second):
			t.Fatal("delivery did not reach the commit barrier")
		}
		type deleteResult struct {
			refs []message.ContentRef
			err  error
		}
		deleted := make(chan deleteResult, 1)
		go func() {
			refs, err := mailboxes.DeleteInbox(context.Background(), inbox.ID)
			deleted <- deleteResult{refs: refs, err: err}
		}()
		select {
		case result := <-deleted:
			t.Fatalf("deletion bypassed the delivery lock: %+v", result)
		case <-time.After(100 * time.Millisecond):
		}
		close(releaseCommit)
		if err := <-deliveryResult; err != nil {
			t.Fatalf("CommitDelivery() error = %v", err)
		}
		result := <-deleted
		if result.err != nil || len(result.refs) != 1 || result.refs[0] != ref {
			t.Fatalf("DeleteInbox() = %+v", result)
		}
		assertMailboxDeleteCounts(t, pool, 0, 0, 0)
	})

	t.Run("deletion commits before delivery admission", func(t *testing.T) {
		pool := newIntegrationPool(t)
		resetIntegrationDatabase(t, pool)
		mailboxes, _ := NewMailboxRepository(pool)
		deliveries := newIntegrationRepository(t, pool)
		now := time.Now().UTC()
		inbox, _ := mailboxes.CreateInbox(context.Background(), mailbox.NewInbox{Address: "delete-first@mailwisp.test", CreatedAt: now, ExpiresAt: now.Add(time.Hour)})
		ref := message.ContentRef{Key: contentKey("8"), SizeBytes: 8}
		deleteLocked := make(chan struct{})
		releaseDelete := make(chan struct{})
		mailboxes.lockObserver = func(message.InboxID) {
			close(deleteLocked)
			<-releaseDelete
		}
		deleteResult := make(chan error, 1)
		go func() {
			_, err := mailboxes.DeleteInbox(context.Background(), inbox.ID)
			deleteResult <- err
		}()
		select {
		case <-deleteLocked:
		case <-time.After(5 * time.Second):
			t.Fatal("deletion did not acquire the Inbox lock")
		}
		deliveryResult := make(chan error, 1)
		go func() {
			_, err := deliveries.CommitDelivery(context.Background(), validDelivery(inbox.ID, ref.Key, ref.SizeBytes))
			deliveryResult <- err
		}()
		select {
		case err := <-deliveryResult:
			t.Fatalf("delivery bypassed the deletion lock: %v", err)
		case <-time.After(100 * time.Millisecond):
		}
		close(releaseDelete)
		if err := <-deleteResult; err != nil {
			t.Fatalf("DeleteInbox() error = %v", err)
		}
		if err := <-deliveryResult; !errors.Is(err, message.ErrInboxNotFound) {
			t.Fatalf("CommitDelivery(after deletion) error = %v", err)
		}
		assertMailboxDeleteCounts(t, pool, 0, 0, 0)
	})
}

func assertMailboxDeleteCounts(t *testing.T, pool *pgxpool.Pool, wantInboxes, wantContents, wantMessages int) {
	t.Helper()
	var inboxes, contents, messages int
	if err := pool.QueryRow(context.Background(), "SELECT (SELECT count(*) FROM inboxes), (SELECT count(*) FROM mail_contents), (SELECT count(*) FROM messages)").Scan(&inboxes, &contents, &messages); err != nil {
		t.Fatal(err)
	}
	if inboxes != wantInboxes || contents != wantContents || messages != wantMessages {
		t.Fatalf("remaining counts = inboxes %d contents %d messages %d, want %d/%d/%d", inboxes, contents, messages, wantInboxes, wantContents, wantMessages)
	}
}

func TestMailboxCleanupDeletesExpiredInBoundedBatches(t *testing.T) {
	pool := newIntegrationPool(t)
	resetIntegrationDatabase(t, pool)
	repository, _ := NewMailboxRepository(pool)
	firstExpiry := time.Now().Add(-2 * time.Hour)
	secondExpiry := time.Now().Add(-time.Hour)
	activeExpiry := time.Now().Add(time.Hour)
	first := createInboxWithState(t, pool, "cleanup-first@mailwisp.test", "active", &firstExpiry)
	second := createInboxWithState(t, pool, "cleanup-second@mailwisp.test", "active", &secondExpiry)
	active := createInboxWithState(t, pool, "cleanup-active@mailwisp.test", "active", &activeExpiry)
	exclusive := message.ContentRef{Key: contentKey("1"), SizeBytes: 1}
	shared := message.ContentRef{Key: contentKey("2"), SizeBytes: 2}
	for _, ref := range []message.ContentRef{exclusive, shared} {
		if _, err := pool.Exec(context.Background(), "INSERT INTO mail_contents (content_key, size_bytes) VALUES ($1, $2)", ref.Key, ref.SizeBytes); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []struct {
		inbox message.InboxID
		ref   message.ContentRef
	}{{first, exclusive}, {second, shared}, {active, shared}} {
		if _, err := pool.Exec(context.Background(), `INSERT INTO messages (inbox_id, content_key, envelope_sender, received_at) VALUES ($1::uuid, $2, '', now())`, string(row.inbox), row.ref.Key); err != nil {
			t.Fatal(err)
		}
	}
	deleted, refs, err := repository.CleanupExpiredInboxes(context.Background(), 1)
	if err != nil || deleted != 1 || len(refs) != 1 || refs[0].Key != exclusive.Key {
		t.Fatalf("first cleanup = deleted %d refs %+v error %v", deleted, refs, err)
	}
	deleted, refs, err = repository.CleanupExpiredInboxes(context.Background(), 10)
	if err != nil || deleted != 1 || len(refs) != 0 {
		t.Fatalf("second cleanup = deleted %d refs %+v error %v", deleted, refs, err)
	}
	var inboxes, contents, messages int
	if err := pool.QueryRow(context.Background(), "SELECT (SELECT count(*) FROM inboxes), (SELECT count(*) FROM mail_contents), (SELECT count(*) FROM messages)").Scan(&inboxes, &contents, &messages); err != nil {
		t.Fatal(err)
	}
	if inboxes != 1 || contents != 1 || messages != 1 {
		t.Fatalf("remaining counts = inboxes %d contents %d messages %d", inboxes, contents, messages)
	}
	deletions, err := repository.ListContentDeletions(context.Background(), 10)
	if err != nil || len(deletions) != 1 || deletions[0].Content != exclusive {
		t.Fatalf("cleanup deletion queue = %+v, %v", deletions, err)
	}
}
