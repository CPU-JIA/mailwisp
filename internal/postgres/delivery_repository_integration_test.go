//go:build integration

package postgres

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/moby/moby/api/types/network"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"mailwisp/internal/contentstore"
	"mailwisp/internal/lmtp"
	"mailwisp/internal/message"
)

const postgresTestImage = "registry-1.docker.io/library/postgres:18.4-alpine3.22@sha256:774521500f4c22761b25a6bdb772a0a3c2e8dd32468210bdad9231c5752ea398"

var integrationDataSourceName string

func TestMain(testingMain *testing.M) {
	os.Exit(runIntegrationTests(testingMain))
}

func runIntegrationTests(testingMain *testing.M) int {
	if os.Getenv(deliveryCrashHelperEnvironment) == "1" {
		integrationDataSourceName = os.Getenv(deliveryCrashDSNEnvironment)
		return testingMain.Run()
	}
	if err := os.Setenv("TESTCONTAINERS_RYUK_DISABLED", "true"); err != nil {
		fmt.Fprintf(os.Stderr, "disable testcontainers ryuk: %v\n", err)
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	integrationPassword := uuid.NewString()
	container, err := tcpostgres.Run(ctx,
		postgresTestImage,
		tcpostgres.WithDatabase("mailwisp_test"),
		tcpostgres.WithUsername("mailwisp"),
		tcpostgres.WithPassword(integrationPassword),
		testcontainers.WithWaitStrategy(
			wait.ForSQL("5432/tcp", "pgx", func(host string, port network.Port) string {
				return fmt.Sprintf("postgres://mailwisp:%s@%s:%s/mailwisp_test?sslmode=disable", integrationPassword, host, port.Port())
			}).WithStartupTimeout(3*time.Minute),
		),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "start PostgreSQL integration container: %v\n", err)
		return 1
	}
	defer func() {
		terminateContext, terminateCancel := context.WithTimeout(context.Background(), time.Minute)
		defer terminateCancel()
		if err := testcontainers.TerminateContainer(container, testcontainers.StopContext(terminateContext)); err != nil {
			fmt.Fprintf(os.Stderr, "terminate PostgreSQL integration container: %v\n", err)
		}
	}()

	integrationDataSourceName, err = container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create PostgreSQL integration DSN: %v\n", err)
		return 1
	}
	if err := Migrate(ctx, integrationDataSourceName); err != nil {
		fmt.Fprintf(os.Stderr, "migrate PostgreSQL integration database: %v\n", err)
		return 1
	}

	return testingMain.Run()
}

func TestMigrateIsIdempotentAndConcurrentSafe(t *testing.T) {
	const workers = 4
	var waitGroup sync.WaitGroup
	errorsChannel := make(chan error, workers)
	for range workers {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			errorsChannel <- Migrate(context.Background(), integrationDataSourceName)
		}()
	}
	waitGroup.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Errorf("Migrate() concurrent error = %v", err)
		}
	}
}

func TestDeliveryRepositoryWalksContentCatalogInBoundedPages(t *testing.T) {
	pool := newIntegrationPool(t)
	resetIntegrationDatabase(t, pool)
	repository := newIntegrationRepository(t, pool)
	rows := []struct {
		key  string
		size int64
	}{
		{key: contentKey("a"), size: 1},
		{key: contentKey("b"), size: 2},
		{key: contentKey("c"), size: 3},
	}
	for _, row := range rows {
		if _, err := pool.Exec(context.Background(), "INSERT INTO mail_contents (content_key, size_bytes) VALUES ($1, $2)", row.key, row.size); err != nil {
			t.Fatalf("insert content metadata: %v", err)
		}
	}

	var refs []message.ContentRef
	if err := repository.WalkContentRefs(context.Background(), 2, func(ref message.ContentRef) error {
		refs = append(refs, ref)
		return nil
	}); err != nil {
		t.Fatalf("WalkContentRefs() error = %v", err)
	}
	if len(refs) != len(rows) || refs[0].Key != rows[0].key || refs[2].SizeBytes != rows[2].size {
		t.Fatalf("WalkContentRefs() = %+v", refs)
	}
	existing, err := repository.ExistingContentKeys(context.Background(), []string{rows[0].key, contentKey("d"), rows[2].key})
	if err != nil {
		t.Fatalf("ExistingContentKeys() error = %v", err)
	}
	if len(existing) != 2 {
		t.Fatalf("ExistingContentKeys() = %v, want 2 keys", existing)
	}
}

func TestMaintenanceLeaseExcludesReconciliationDuringServe(t *testing.T) {
	pool, err := OpenPool(context.Background(), PoolOptions{
		DSN:               integrationDataSourceName,
		MaxConnections:    1,
		ConnectTimeout:    5 * time.Second,
		HealthCheckPeriod: time.Minute,
		MaxConnectionIdle: time.Minute,
	})
	if err != nil {
		t.Fatalf("OpenPool(max one) error = %v", err)
	}
	t.Cleanup(pool.Close)
	shared, err := AcquireServiceLease(context.Background(), integrationDataSourceName)
	if err != nil {
		t.Fatalf("AcquireServiceLease() error = %v", err)
	}
	repository := newIntegrationRepository(t, pool)
	readyContext, readyCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := repository.Ready(readyContext); err != nil {
		readyCancel()
		t.Fatalf("Ready() with one pooled connection and shared lease error = %v", err)
	}
	readyCancel()
	exclusive, err := TryAcquireMaintenanceLease(context.Background(), integrationDataSourceName)
	if !errors.Is(err, ErrServiceActive) || exclusive != nil {
		t.Fatalf("TryAcquireMaintenanceLease() = lease %v, error %v, want ErrServiceActive", exclusive, err)
	}
	releaseContext, releaseCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := shared.Release(releaseContext); err != nil {
		releaseCancel()
		t.Fatalf("release shared lease: %v", err)
	}
	releaseCancel()

	exclusive, err = TryAcquireMaintenanceLease(context.Background(), integrationDataSourceName)
	if err != nil {
		t.Fatalf("TryAcquireMaintenanceLease(after release) error = %v", err)
	}
	blockedContext, blockedCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer blockedCancel()
	if _, err := AcquireServiceLease(blockedContext, integrationDataSourceName); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("AcquireServiceLease(while exclusive) error = %v, want deadline", err)
	}
	if err := exclusive.Release(context.Background()); err != nil {
		t.Fatalf("release exclusive lease: %v", err)
	}
}

func TestContentReconciliationWithPostgresCatalog(t *testing.T) {
	pool := newIntegrationPool(t)
	resetIntegrationDatabase(t, pool)
	repository := newIntegrationRepository(t, pool)
	store, err := contentstore.Open(t.TempDir(), contentstore.Options{MaxBytes: 1 << 20})
	if err != nil {
		t.Fatalf("contentstore.Open() error = %v", err)
	}
	orphan, err := store.Put(context.Background(), bytes.NewReader([]byte("orphan")))
	if err != nil {
		t.Fatalf("store orphan content: %v", err)
	}
	missing := contentKey("f")
	if _, err := pool.Exec(context.Background(), "INSERT INTO mail_contents (content_key, size_bytes) VALUES ($1, $2)", missing, 7); err != nil {
		t.Fatalf("insert missing metadata: %v", err)
	}

	summary, err := store.Reconcile(context.Background(), repository, contentstore.ReconcileOptions{BatchSize: 1}, nil)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if summary.Missing != 1 || summary.Orphans != 1 || summary.Unresolved() != 2 {
		t.Fatalf("report summary = %+v", summary)
	}
	summary, err = store.Reconcile(context.Background(), repository, contentstore.ReconcileOptions{BatchSize: 1, RepairOrphans: true}, nil)
	if err != nil {
		t.Fatalf("Reconcile(repair) error = %v", err)
	}
	if summary.RepairedOrphans != 1 || summary.Unresolved() != 1 {
		t.Fatalf("repair summary = %+v", summary)
	}
	if _, err := store.OpenContent(orphan); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("repaired orphan OpenContent() error = %v, want not exist", err)
	}
}

func TestDeliveryRepositoryCommitsAllRecipients(t *testing.T) {
	pool := newIntegrationPool(t)
	resetIntegrationDatabase(t, pool)
	repository := newIntegrationRepository(t, pool)
	firstInbox := createInbox(t, pool, "first@example.com")
	secondInbox := createInbox(t, pool, "second@example.com")
	receivedAt := time.Date(2026, 7, 14, 6, 30, 0, 123000000, time.UTC)
	delivery := message.Delivery{
		Content: message.ContentRef{
			Key:       contentKey("a"),
			SizeBytes: 128,
		},
		EnvelopeSender: "sender@example.net",
		Recipients:     []message.InboxID{firstInbox, secondInbox},
		ReceivedAt:     receivedAt,
	}

	stored, err := repository.CommitDelivery(context.Background(), delivery)
	if err != nil {
		t.Fatalf("CommitDelivery() error = %v", err)
	}
	if len(stored) != 2 {
		t.Fatalf("CommitDelivery() messages = %d, want 2", len(stored))
	}
	if stored[0].InboxID != firstInbox || stored[1].InboxID != secondInbox {
		t.Fatalf("CommitDelivery() recipient order = %+v", stored)
	}

	var contentCount, messageCount, versionSevenCount int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM mail_contents").Scan(&contentCount); err != nil {
		t.Fatalf("count mail_contents: %v", err)
	}
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM messages").Scan(&messageCount); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM messages WHERE uuid_extract_version(id) = 7").Scan(&versionSevenCount); err != nil {
		t.Fatalf("count UUIDv7 messages: %v", err)
	}
	if contentCount != 1 || messageCount != 2 || versionSevenCount != 2 {
		t.Fatalf("database counts content=%d messages=%d uuidv7=%d", contentCount, messageCount, versionSevenCount)
	}

	var gotSender string
	var gotReceivedAt time.Time
	if err := pool.QueryRow(context.Background(), `
		SELECT envelope_sender, received_at
		FROM messages
		WHERE id = $1::uuid
	`, string(stored[0].ID)).Scan(&gotSender, &gotReceivedAt); err != nil {
		t.Fatalf("read stored message: %v", err)
	}
	if gotSender != delivery.EnvelopeSender || !gotReceivedAt.Equal(receivedAt) {
		t.Fatalf("stored sender/time = %q/%s, want %q/%s", gotSender, gotReceivedAt, delivery.EnvelopeSender, receivedAt)
	}
}

func TestDeliveryRepositoryResolveInbox(t *testing.T) {
	pool := newIntegrationPool(t)
	resetIntegrationDatabase(t, pool)
	repository := newIntegrationRepository(t, pool)
	active := createInbox(t, pool, "active@example.com")
	createInboxWithState(t, pool, "disabled@example.com", "disabled", nil)
	expiredAt := time.Now().Add(-time.Hour)
	createInboxWithState(t, pool, "expired@example.com", "active", &expiredAt)

	got, err := repository.ResolveInbox(context.Background(), "active@example.com")
	if err != nil {
		t.Fatalf("ResolveInbox(active) error = %v", err)
	}
	if got != active {
		t.Fatalf("ResolveInbox(active) = %q, want %q", got, active)
	}
	for _, address := range []string{"missing@example.com", "disabled@example.com", "expired@example.com"} {
		if _, err := repository.ResolveInbox(context.Background(), address); !errors.Is(err, message.ErrInboxNotFound) {
			t.Errorf("ResolveInbox(%q) error = %v, want message.ErrInboxNotFound", address, err)
		}
	}
}

func TestDeliveryRepositoryReusesContentMetadata(t *testing.T) {
	pool := newIntegrationPool(t)
	resetIntegrationDatabase(t, pool)
	repository := newIntegrationRepository(t, pool)
	inboxID := createInbox(t, pool, "reuse@example.com")
	delivery := validDelivery(inboxID, contentKey("b"), 64)

	for range 2 {
		if _, err := repository.CommitDelivery(context.Background(), delivery); err != nil {
			t.Fatalf("CommitDelivery() error = %v", err)
		}
	}

	var contentCount, messageCount int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM mail_contents").Scan(&contentCount); err != nil {
		t.Fatalf("count mail_contents: %v", err)
	}
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM messages").Scan(&messageCount); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if contentCount != 1 || messageCount != 2 {
		t.Fatalf("database counts content=%d messages=%d, want 1/2", contentCount, messageCount)
	}
}

func TestDeliveryRepositoryRejectsContentMetadataConflict(t *testing.T) {
	pool := newIntegrationPool(t)
	resetIntegrationDatabase(t, pool)
	repository := newIntegrationRepository(t, pool)
	inboxID := createInbox(t, pool, "conflict@example.com")
	key := contentKey("c")
	if _, err := repository.CommitDelivery(context.Background(), validDelivery(inboxID, key, 10)); err != nil {
		t.Fatalf("first CommitDelivery() error = %v", err)
	}

	_, err := repository.CommitDelivery(context.Background(), validDelivery(inboxID, key, 11))
	if !errors.Is(err, ErrContentMetadataConflict) {
		t.Fatalf("second CommitDelivery() error = %v, want ErrContentMetadataConflict", err)
	}
	assertCounts(t, pool, 1, 1)
}

func TestDeliveryRepositoryRollsBackAllRecipients(t *testing.T) {
	pool := newIntegrationPool(t)
	resetIntegrationDatabase(t, pool)
	repository := newIntegrationRepository(t, pool)
	validInbox := createInbox(t, pool, "valid@example.com")
	unknownInbox := message.InboxID(uuid.NewString())
	delivery := validDelivery(validInbox, contentKey("d"), 32)
	delivery.Recipients = []message.InboxID{validInbox, unknownInbox}

	_, err := repository.CommitDelivery(context.Background(), delivery)
	if !errors.Is(err, message.ErrInboxNotFound) {
		t.Fatalf("CommitDelivery() error = %v, want message.ErrInboxNotFound", err)
	}
	assertCounts(t, pool, 0, 0)
}

func TestReceiverPersistsRawContentAndMetadata(t *testing.T) {
	pool := newIntegrationPool(t)
	resetIntegrationDatabase(t, pool)
	repository := newIntegrationRepository(t, pool)
	inboxID := createInbox(t, pool, "receiver@example.com")
	store, err := contentstore.Open(t.TempDir(), contentstore.Options{MaxBytes: 1 << 20})
	if err != nil {
		t.Fatalf("contentstore.Open() error = %v", err)
	}
	receiver, err := message.NewReceiver(store, repository)
	if err != nil {
		t.Fatalf("message.NewReceiver() error = %v", err)
	}
	raw := []byte("From: sender@example.net\r\nTo: receiver@example.com\r\nSubject: test\r\n\r\nhello")

	receipt, err := receiver.Receive(context.Background(), message.ReceiveRequest{
		EnvelopeSender: "sender@example.net",
		Recipients:     []message.InboxID{inboxID},
		Raw:            bytes.NewReader(raw),
	})
	if err != nil {
		t.Fatalf("Receive() error = %v", err)
	}
	if err := store.Verify(context.Background(), receipt.Content); err != nil {
		t.Fatalf("contentstore.Verify() error = %v", err)
	}
	if len(receipt.Messages) != 1 {
		t.Fatalf("Receive() messages = %d, want 1", len(receipt.Messages))
	}
	assertCounts(t, pool, 1, 1)
}

func TestLMTPDeliveryEndToEnd(t *testing.T) {
	pool := newIntegrationPool(t)
	resetIntegrationDatabase(t, pool)
	repository := newIntegrationRepository(t, pool)
	createInbox(t, pool, "lmtp@example.com")
	store, err := contentstore.Open(t.TempDir(), contentstore.Options{MaxBytes: 1 << 20})
	if err != nil {
		t.Fatalf("contentstore.Open() error = %v", err)
	}
	receiver, err := message.NewReceiver(store, repository)
	if err != nil {
		t.Fatalf("message.NewReceiver() error = %v", err)
	}
	options := lmtp.DefaultOptions("mailwisp.integration")
	options.MaxMessageBytes = 1 << 20
	options.MaxSessions = 4
	options.SessionTimeout = 10 * time.Second
	options.DeliveryTimeout = 10 * time.Second
	server, err := lmtp.NewServer(options, repository, receiver, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("lmtp.NewServer() error = %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	serveContext, cancelServe := context.WithCancel(context.Background())
	serveError := make(chan error, 1)
	go func() { serveError <- server.Serve(serveContext, listener) }()

	connection, err := net.DialTimeout("tcp", listener.Addr().String(), 5*time.Second)
	if err != nil {
		cancelServe()
		t.Fatalf("net.DialTimeout() error = %v", err)
	}
	if err := connection.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		_ = connection.Close()
		cancelServe()
		t.Fatalf("SetDeadline() error = %v", err)
	}
	reader := bufio.NewReader(connection)
	writer := bufio.NewWriter(connection)
	expectLMTPCode(t, reader, 220)
	sendLMTPLine(t, writer, "LHLO postfix.integration")
	expectLMTPCode(t, reader, 250)
	sendLMTPLine(t, writer, "MAIL FROM:<sender@example.net>")
	expectLMTPCode(t, reader, 250)
	sendLMTPLine(t, writer, "RCPT TO:<lmtp@example.com>")
	expectLMTPCode(t, reader, 250)
	sendLMTPLine(t, writer, "DATA")
	expectLMTPCode(t, reader, 354)
	raw := "From: sender@example.net\r\nTo: lmtp@example.com\r\nSubject: Integration\r\n\r\nFast mail. Zero trace.\r\n"
	if _, err := io.WriteString(writer, raw+".\r\n"); err != nil {
		t.Fatalf("write LMTP DATA: %v", err)
	}
	if err := writer.Flush(); err != nil {
		t.Fatalf("flush LMTP DATA: %v", err)
	}
	expectLMTPCode(t, reader, 250)
	sendLMTPLine(t, writer, "QUIT")
	expectLMTPCode(t, reader, 221)
	_ = connection.Close()
	cancelServe()
	select {
	case err := <-serveError:
		if err != nil {
			t.Fatalf("LMTP Serve() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("LMTP Serve() did not stop")
	}

	var contentKey string
	var contentSize int64
	var messageCount int
	if err := pool.QueryRow(context.Background(), `
		SELECT m.content_key, c.size_bytes, count(*) OVER ()
		FROM messages m
		JOIN mail_contents c ON c.content_key = m.content_key
	`).Scan(&contentKey, &contentSize, &messageCount); err != nil {
		t.Fatalf("read persisted LMTP message: %v", err)
	}
	if messageCount != 1 {
		t.Fatalf("persisted LMTP message count = %d, want 1", messageCount)
	}
	ref := message.ContentRef{Key: contentKey, SizeBytes: contentSize}
	if err := store.Verify(context.Background(), ref); err != nil {
		t.Fatalf("verify persisted LMTP content: %v", err)
	}
	file, err := store.OpenContent(ref)
	if err != nil {
		t.Fatalf("open persisted LMTP content: %v", err)
	}
	gotRaw, readErr := io.ReadAll(file)
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read persisted LMTP content error = %v, close = %v", readErr, closeErr)
	}
	if string(gotRaw) != raw {
		t.Fatalf("persisted LMTP raw = %q, want %q", gotRaw, raw)
	}
}

func newIntegrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), integrationDataSourceName)
	if err != nil {
		t.Fatalf("pgxpool.New() error = %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(context.Background()); err != nil {
		t.Fatalf("pool.Ping() error = %v", err)
	}
	return pool
}

func newIntegrationRepository(t *testing.T, pool *pgxpool.Pool) *DeliveryRepository {
	t.Helper()
	repository, err := NewDeliveryRepository(pool)
	if err != nil {
		t.Fatalf("NewDeliveryRepository() error = %v", err)
	}
	return repository
}

func resetIntegrationDatabase(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), "TRUNCATE messages, mail_contents, inboxes CASCADE"); err != nil {
		t.Fatalf("truncate integration database: %v", err)
	}
}

func createInbox(t *testing.T, pool *pgxpool.Pool, address string) message.InboxID {
	t.Helper()
	return createInboxWithState(t, pool, address, "active", nil)
}

func createInboxWithState(t *testing.T, pool *pgxpool.Pool, address, status string, expiresAt *time.Time) message.InboxID {
	t.Helper()
	var inboxID string
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO inboxes (address, status, expires_at)
		VALUES ($1, $2, $3)
		RETURNING id::text
	`, address, status, expiresAt).Scan(&inboxID); err != nil {
		t.Fatalf("create inbox %q: %v", address, err)
	}
	return message.InboxID(inboxID)
}

func validDelivery(inboxID message.InboxID, key string, size int64) message.Delivery {
	return message.Delivery{
		Content:        message.ContentRef{Key: key, SizeBytes: size},
		EnvelopeSender: "sender@example.net",
		Recipients:     []message.InboxID{inboxID},
		ReceivedAt:     time.Date(2026, 7, 14, 7, 0, 0, 0, time.UTC),
	}
}

func contentKey(character string) string {
	return "sha256/" + string(bytes.Repeat([]byte(character), 64))
}

func assertCounts(t *testing.T, pool *pgxpool.Pool, wantContent, wantMessages int) {
	t.Helper()
	var contentCount, messageCount int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM mail_contents").Scan(&contentCount); err != nil {
		t.Fatalf("count mail_contents: %v", err)
	}
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM messages").Scan(&messageCount); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if contentCount != wantContent || messageCount != wantMessages {
		t.Fatalf("database counts content=%d messages=%d, want %d/%d", contentCount, messageCount, wantContent, wantMessages)
	}
}

func sendLMTPLine(t *testing.T, writer *bufio.Writer, line string) {
	t.Helper()
	if _, err := fmt.Fprintf(writer, "%s\r\n", line); err != nil {
		t.Fatalf("send LMTP line %q: %v", line, err)
	}
	if err := writer.Flush(); err != nil {
		t.Fatalf("flush LMTP line %q: %v", line, err)
	}
}

func expectLMTPCode(t *testing.T, reader *bufio.Reader, wantCode int) {
	t.Helper()
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read LMTP response %d: %v", wantCode, err)
		}
		if len(line) < 4 || line[:3] != fmt.Sprintf("%03d", wantCode) {
			t.Fatalf("LMTP response = %q, want code %d", line, wantCode)
		}
		if line[3] == ' ' {
			return
		}
		if line[3] != '-' {
			t.Fatalf("invalid LMTP response separator in %q", line)
		}
	}
}
