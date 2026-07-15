//go:build integration

package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"mailwisp/internal/contentstore"
	"mailwisp/internal/jobs"
	"mailwisp/internal/mail"
	"mailwisp/internal/message"
	"mailwisp/migrations"
)

func TestContentParseMigrationUpgradesExistingIngressData(t *testing.T) {
	dropIntegrationSchema(t)
	t.Cleanup(func() { recreateIntegrationSchema(t) })

	config, err := pgx.ParseConfig(integrationDataSourceName)
	if err != nil {
		t.Fatalf("pgx.ParseConfig() error = %v", err)
	}
	database := stdlib.OpenDB(*config)
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = database.Close() })
	provider, err := goose.NewProvider(goose.DialectPostgres, database, migrations.FS)
	if err != nil {
		t.Fatalf("goose.NewProvider() error = %v", err)
	}
	if _, err := provider.UpTo(context.Background(), 1); err != nil {
		t.Fatalf("apply migration 1: %v", err)
	}

	inboxID := "018f26e5-8f04-7b44-8ba2-4a8f434dcb12"
	key := contentKey("d")
	if _, err := database.ExecContext(context.Background(), "INSERT INTO inboxes (id, address) VALUES ($1::uuid, 'upgrade@example.com')", inboxID); err != nil {
		t.Fatalf("insert migration 1 inbox: %v", err)
	}
	if _, err := database.ExecContext(context.Background(), "INSERT INTO mail_contents (content_key, size_bytes) VALUES ($1, 128)", key); err != nil {
		t.Fatalf("insert migration 1 content: %v", err)
	}
	if _, err := database.ExecContext(context.Background(), `
		INSERT INTO messages (inbox_id, content_key, envelope_sender, received_at, parse_status)
		VALUES ($1::uuid, $2, 'sender@example.net', now(), 'pending')
	`, inboxID, key); err != nil {
		t.Fatalf("insert migration 1 message: %v", err)
	}
	if _, err := provider.UpTo(context.Background(), 2); err != nil {
		t.Fatalf("apply migration 2: %v", err)
	}

	var messageCount int
	var parseStatus string
	if err := database.QueryRowContext(context.Background(), `
		SELECT
			(SELECT count(*) FROM messages WHERE content_key = $1),
			(SELECT parse_status FROM mail_contents WHERE content_key = $1)
	`, key).Scan(&messageCount, &parseStatus); err != nil {
		t.Fatalf("read upgraded data: %v", err)
	}
	if messageCount != 1 || parseStatus != "pending" {
		t.Fatalf("upgraded data = messages %d, parse status %q", messageCount, parseStatus)
	}
	var oldColumnCount, resultTableCount int
	if err := database.QueryRowContext(context.Background(), `
		SELECT
			(SELECT count(*) FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'messages' AND column_name = 'parse_status'),
			(SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name = 'mail_content_parses')
	`).Scan(&oldColumnCount, &resultTableCount); err != nil {
		t.Fatalf("inspect upgraded schema: %v", err)
	}
	if oldColumnCount != 0 || resultTableCount != 1 {
		t.Fatalf("upgraded schema = old column %d, result table %d", oldColumnCount, resultTableCount)
	}
}

func TestContentParseRepositoryPersistsOneResultPerRawContent(t *testing.T) {
	pool := newIntegrationPool(t)
	resetIntegrationDatabase(t, pool)
	deliveryRepository := newIntegrationRepository(t, pool)
	parseRepository, err := NewContentParseRepository(pool)
	if err != nil {
		t.Fatalf("NewContentParseRepository() error = %v", err)
	}
	store, err := contentstore.Open(t.TempDir(), contentstore.Options{MaxBytes: 1 << 20})
	if err != nil {
		t.Fatalf("contentstore.Open() error = %v", err)
	}
	firstInbox := createInbox(t, pool, "parse-first@example.com")
	secondInbox := createInbox(t, pool, "parse-second@example.com")
	receiver, err := message.NewReceiver(store, deliveryRepository)
	if err != nil {
		t.Fatalf("message.NewReceiver() error = %v", err)
	}
	raw := []byte("From: Sender <sender@example.net>\r\nTo: parse-first@example.com\r\nSubject: Fast mail\r\n\r\nZero trace.\r\n")
	for _, recipients := range [][]message.InboxID{{firstInbox, secondInbox}, {firstInbox}} {
		if _, err := receiver.Receive(context.Background(), message.ReceiveRequest{
			EnvelopeSender: "sender@example.net",
			Recipients:     recipients,
			Raw:            bytes.NewReader(raw),
		}); err != nil {
			t.Fatalf("Receive() error = %v", err)
		}
	}

	claim, found, err := parseRepository.ClaimContent(context.Background(), time.Minute)
	if err != nil || !found {
		t.Fatalf("ClaimContent() = %+v, %v, %v", claim, found, err)
	}
	parser, err := mail.NewParser(mail.DefaultLimits())
	if err != nil {
		t.Fatalf("mail.NewParser() error = %v", err)
	}
	file, err := store.OpenRaw(context.Background(), claim.Content)
	if err != nil {
		t.Fatalf("OpenRaw() error = %v", err)
	}
	parsed, parseErr := parser.Parse(context.Background(), file)
	closeErr := file.Close()
	if parseErr != nil || closeErr != nil {
		t.Fatalf("Parse() error = %v, close = %v", parseErr, closeErr)
	}
	parsedAt := time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC)
	if err := parseRepository.CompleteContent(context.Background(), claim, mail.ParserRevision, parsed, parsedAt); err != nil {
		t.Fatalf("CompleteContent() error = %v", err)
	}
	if next, found, err := parseRepository.ClaimContent(context.Background(), time.Minute); err != nil || found {
		t.Fatalf("second ClaimContent() = %+v, %v, %v", next, found, err)
	}

	var contentCount, messageCount, parseCount, revision int
	var status, subject, textBody, fromJSON string
	var storedParsedAt time.Time
	err = pool.QueryRow(context.Background(), `
		SELECT
			(SELECT count(*) FROM mail_contents),
			(SELECT count(*) FROM messages),
			(SELECT count(*) FROM mail_content_parses),
			content.parse_status,
			parsed.parser_revision,
			parsed.subject,
			parsed.text_body,
			parsed.from_addresses::text,
			parsed.parsed_at
		FROM mail_contents AS content
		JOIN mail_content_parses AS parsed USING (content_key)
	`).Scan(&contentCount, &messageCount, &parseCount, &status, &revision, &subject, &textBody, &fromJSON, &storedParsedAt)
	if err != nil {
		t.Fatalf("read persisted parse result: %v", err)
	}
	if contentCount != 1 || messageCount != 3 || parseCount != 1 || status != "parsed" {
		t.Fatalf("counts/status = %d/%d/%d/%q", contentCount, messageCount, parseCount, status)
	}
	if revision != mail.ParserRevision || subject != "Fast mail" || textBody != "Zero trace." || !storedParsedAt.Equal(parsedAt) {
		t.Fatalf("persisted result = revision %d subject %q text %q parsed_at %s", revision, subject, textBody, storedParsedAt)
	}
	var addresses []mail.Address
	if err := json.Unmarshal([]byte(fromJSON), &addresses); err != nil {
		t.Fatalf("unmarshal persisted From: %v", err)
	}
	if len(addresses) != 1 || addresses[0].Address != "sender@example.net" || !strings.Contains(fromJSON, `"address"`) {
		t.Fatalf("persisted From = %s", fromJSON)
	}
}

func TestParserWorkerProcessesDurableContentEndToEnd(t *testing.T) {
	pool := newIntegrationPool(t)
	resetIntegrationDatabase(t, pool)
	deliveryRepository := newIntegrationRepository(t, pool)
	parseRepository, err := NewContentParseRepository(pool)
	if err != nil {
		t.Fatalf("NewContentParseRepository() error = %v", err)
	}
	store, err := contentstore.Open(t.TempDir(), contentstore.Options{MaxBytes: 1 << 20})
	if err != nil {
		t.Fatalf("contentstore.Open() error = %v", err)
	}
	inboxID := createInbox(t, pool, "worker@example.com")
	receiver, err := message.NewReceiver(store, deliveryRepository)
	if err != nil {
		t.Fatalf("message.NewReceiver() error = %v", err)
	}
	raw := []byte("From: sender@example.net\r\nTo: worker@example.com\r\nSubject: Worker E2E\r\n\r\nParsed asynchronously.\r\n")
	receipt, err := receiver.Receive(context.Background(), message.ReceiveRequest{
		EnvelopeSender: "sender@example.net",
		Recipients:     []message.InboxID{inboxID},
		Raw:            bytes.NewReader(raw),
	})
	if err != nil {
		t.Fatalf("Receive() error = %v", err)
	}
	parser, err := mail.NewParser(mail.DefaultLimits())
	if err != nil {
		t.Fatalf("mail.NewParser() error = %v", err)
	}
	worker, err := jobs.NewParserWorker(parseRepository, store, parser, slog.New(slog.NewTextHandler(io.Discard, nil)), jobs.ParserOptions{
		Workers:       2,
		PollInterval:  20 * time.Millisecond,
		ParseTimeout:  time.Second,
		LeaseDuration: 2 * time.Second,
		MaxAttempts:   3,
		RetryBase:     20 * time.Millisecond,
		RetryMax:      100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("jobs.NewParserWorker() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	runResult := make(chan error, 1)
	go func() { runResult <- worker.Run(ctx) }()
	worker.Notify()

	deadline := time.Now().Add(5 * time.Second)
	for {
		var status string
		var subject, textBody *string
		err := pool.QueryRow(context.Background(), `
			SELECT content.parse_status, parsed.subject, parsed.text_body
			FROM mail_contents AS content
			LEFT JOIN mail_content_parses AS parsed USING (content_key)
			WHERE content.content_key = $1
		`, receipt.Content.Key).Scan(&status, &subject, &textBody)
		if err != nil {
			cancel()
			t.Fatalf("read worker parse state: %v", err)
		}
		if status == "parsed" {
			if subject == nil || *subject != "Worker E2E" || textBody == nil || *textBody != "Parsed asynchronously." {
				cancel()
				t.Fatalf("parsed worker result = %v/%v", subject, textBody)
			}
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("worker parse status = %q after deadline", status)
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if err := <-runResult; err != nil {
		t.Fatalf("ParserWorker.Run() error = %v", err)
	}
}

func TestContentParseRepositoryClaimsConcurrentlyWithoutDuplicates(t *testing.T) {
	pool := newIntegrationPool(t)
	resetIntegrationDatabase(t, pool)
	repository, err := NewContentParseRepository(pool)
	if err != nil {
		t.Fatalf("NewContentParseRepository() error = %v", err)
	}
	const contentCount = 8
	for index := range contentCount {
		key := contentKey(string("01234567"[index]))
		if _, err := pool.Exec(context.Background(), `
			INSERT INTO mail_contents (content_key, size_bytes) VALUES ($1, $2)
		`, key, index+1); err != nil {
			t.Fatalf("insert content %d: %v", index, err)
		}
	}

	claims := make(chan jobs.ParseClaim, contentCount)
	errorsChannel := make(chan error, contentCount)
	var workers sync.WaitGroup
	for range contentCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			claim, found, err := repository.ClaimContent(context.Background(), time.Minute)
			if err != nil {
				errorsChannel <- err
				return
			}
			if !found {
				errorsChannel <- errors.New("no parse claim found")
				return
			}
			claims <- claim
		}()
	}
	workers.Wait()
	close(claims)
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Errorf("concurrent ClaimContent() error = %v", err)
		}
	}
	seen := make(map[string]struct{}, contentCount)
	for claim := range claims {
		if _, exists := seen[claim.Content.Key]; exists {
			t.Fatalf("duplicate content claim %q", claim.Content.Key)
		}
		seen[claim.Content.Key] = struct{}{}
	}
	if len(seen) != contentCount {
		t.Fatalf("unique claims = %d, want %d", len(seen), contentCount)
	}
}

func TestContentParseRepositoryLeaseExpiryFencesOldWorker(t *testing.T) {
	pool := newIntegrationPool(t)
	resetIntegrationDatabase(t, pool)
	repository, err := NewContentParseRepository(pool)
	if err != nil {
		t.Fatalf("NewContentParseRepository() error = %v", err)
	}
	key := contentKey("a")
	if _, err := pool.Exec(context.Background(), "INSERT INTO mail_contents (content_key, size_bytes) VALUES ($1, 1)", key); err != nil {
		t.Fatalf("insert content: %v", err)
	}
	first, found, err := repository.ClaimContent(context.Background(), time.Minute)
	if err != nil || !found {
		t.Fatalf("first ClaimContent() = %+v, %v, %v", first, found, err)
	}
	if _, err := pool.Exec(context.Background(), `
		UPDATE mail_contents SET parse_lease_until = now() - interval '1 second' WHERE content_key = $1
	`, key); err != nil {
		t.Fatalf("expire first lease: %v", err)
	}
	second, found, err := repository.ClaimContent(context.Background(), time.Minute)
	if err != nil || !found {
		t.Fatalf("second ClaimContent() = %+v, %v, %v", second, found, err)
	}
	if second.LeaseToken == first.LeaseToken || second.Attempt != 2 {
		t.Fatalf("second claim = %+v, first = %+v", second, first)
	}
	if err := repository.CompleteContent(context.Background(), first, mail.ParserRevision, mail.ParsedMessage{}, time.Now()); !errors.Is(err, jobs.ErrStaleParseClaim) {
		t.Fatalf("old CompleteContent() error = %v, want ErrStaleParseClaim", err)
	}
	if err := repository.CompleteContent(context.Background(), second, mail.ParserRevision, mail.ParsedMessage{Subject: "new owner"}, time.Now()); err != nil {
		t.Fatalf("new CompleteContent() error = %v", err)
	}
}

func TestContentParseRepositoryRetryAndTerminalFailure(t *testing.T) {
	pool := newIntegrationPool(t)
	resetIntegrationDatabase(t, pool)
	repository, err := NewContentParseRepository(pool)
	if err != nil {
		t.Fatalf("NewContentParseRepository() error = %v", err)
	}
	key := contentKey("b")
	if _, err := pool.Exec(context.Background(), "INSERT INTO mail_contents (content_key, size_bytes) VALUES ($1, 1)", key); err != nil {
		t.Fatalf("insert content: %v", err)
	}
	first, found, err := repository.ClaimContent(context.Background(), time.Minute)
	if err != nil || !found {
		t.Fatalf("ClaimContent() = %+v, %v, %v", first, found, err)
	}
	availableAt := time.Now().UTC().Add(time.Hour)
	if err := repository.RetryContent(context.Background(), first, jobs.ParseErrorContentUnavailable, availableAt); err != nil {
		t.Fatalf("RetryContent() error = %v", err)
	}
	if _, found, err := repository.ClaimContent(context.Background(), time.Minute); err != nil || found {
		t.Fatalf("ClaimContent(before retry) = %v, %v", found, err)
	}
	if _, err := pool.Exec(context.Background(), "UPDATE mail_contents SET parse_available_at = now() - interval '1 second' WHERE content_key = $1", key); err != nil {
		t.Fatalf("make retry available: %v", err)
	}
	second, found, err := repository.ClaimContent(context.Background(), time.Minute)
	if err != nil || !found || second.Attempt != 2 {
		t.Fatalf("retry ClaimContent() = %+v, %v, %v", second, found, err)
	}
	failedAt := time.Now().UTC()
	if err := repository.FailContent(context.Background(), second, jobs.ParseErrorContentUnavailable, failedAt); err != nil {
		t.Fatalf("FailContent() error = %v", err)
	}
	var status, errorCode string
	var attempts int
	if err := pool.QueryRow(context.Background(), `
		SELECT parse_status, parse_attempts, parse_error_code
		FROM mail_contents WHERE content_key = $1
	`, key).Scan(&status, &attempts, &errorCode); err != nil {
		t.Fatalf("read failed parse state: %v", err)
	}
	if status != "failed" || attempts != 2 || errorCode != string(jobs.ParseErrorContentUnavailable) {
		t.Fatalf("failed parse state = %q/%d/%q", status, attempts, errorCode)
	}
}

func TestContentParseRepositoryConstraintFailureRollsBackStatus(t *testing.T) {
	pool := newIntegrationPool(t)
	resetIntegrationDatabase(t, pool)
	repository, err := NewContentParseRepository(pool)
	if err != nil {
		t.Fatalf("NewContentParseRepository() error = %v", err)
	}
	key := contentKey("c")
	if _, err := pool.Exec(context.Background(), "INSERT INTO mail_contents (content_key, size_bytes) VALUES ($1, 1)", key); err != nil {
		t.Fatalf("insert content: %v", err)
	}
	claim, found, err := repository.ClaimContent(context.Background(), time.Minute)
	if err != nil || !found {
		t.Fatalf("ClaimContent() = %+v, %v, %v", claim, found, err)
	}
	err = repository.CompleteContent(context.Background(), claim, mail.ParserRevision, mail.ParsedMessage{
		Subject: strings.Repeat("x", 999),
	}, time.Now())
	if err == nil {
		t.Fatal("CompleteContent(oversized subject) error = nil")
	}
	var status, leaseToken string
	if err := pool.QueryRow(context.Background(), `
		SELECT parse_status, parse_lease_token::text
		FROM mail_contents WHERE content_key = $1
	`, key).Scan(&status, &leaseToken); err != nil {
		t.Fatalf("read rolled back parse state: %v", err)
	}
	if status != "processing" || leaseToken != claim.LeaseToken {
		t.Fatalf("parse state after rollback = %q/%q", status, leaseToken)
	}
}
