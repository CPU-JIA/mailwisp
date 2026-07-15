package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"mailwisp/internal/config"
	"mailwisp/internal/message"
)

func TestNewComposesApplicationWithoutConnecting(t *testing.T) {
	t.Parallel()

	cfg := testConfig(t)
	application, err := New(context.Background(), cfg, discardLogger())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	application.pool.Close()
	if application.http == nil || application.lmtp == nil || application.repository == nil || application.parserWorker == nil || application.mailbox == nil {
		t.Fatal("New() did not compose all required services")
	}
}

func TestNewValidatesDependenciesAndDSN(t *testing.T) {
	t.Parallel()

	cfg := testConfig(t)
	if _, err := New(context.Background(), cfg, nil); err == nil {
		t.Fatal("New(nil logger) error = nil, want error")
	}
	cfg.Postgres.DSN = "://invalid"
	if _, err := New(context.Background(), cfg, discardLogger()); err == nil {
		t.Fatal("New(invalid DSN) error = nil, want error")
	}
}

func TestRunFailsWhenPostgresIsUnavailable(t *testing.T) {
	t.Parallel()

	cfg := testConfig(t)
	cfg.Postgres.DSN = "postgres://mailwisp:test@127.0.0.1:1/mailwisp?sslmode=disable"
	cfg.Postgres.ConnectTimeout = 100 * time.Millisecond
	application, err := New(context.Background(), cfg, discardLogger())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := application.Run(context.Background()); err == nil {
		t.Fatal("Run() error = nil, want PostgreSQL readiness error")
	}
}

func TestServiceResultClassification(t *testing.T) {
	t.Parallel()

	if err := unexpectedServiceError(serviceResult{name: "lmtp"}); err == nil {
		t.Fatal("unexpectedServiceError(nil) = nil, want error")
	}
	sourceErr := errors.New("listener failed")
	if err := unexpectedServiceError(serviceResult{name: "lmtp", err: sourceErr}); !errors.Is(err, sourceErr) {
		t.Fatalf("unexpectedServiceError() = %v, want source error", err)
	}
	if err := stoppedServiceError(serviceResult{name: "http", err: http.ErrServerClosed}); err != nil {
		t.Fatalf("stoppedServiceError(http closed) = %v", err)
	}
}

func TestWakingReceiverNotifiesOnlyAfterDurableSuccess(t *testing.T) {
	t.Parallel()

	inboxID := message.InboxID("018f26e5-8f04-7b44-8ba2-4a8f434dcb12")
	store := &appContentStoreStub{}
	repository := &appDeliveryRepositoryStub{messages: []message.StoredMessage{{ID: "message-id", InboxID: inboxID}}}
	receiver, err := message.NewReceiver(store, repository)
	if err != nil {
		t.Fatalf("message.NewReceiver() error = %v", err)
	}
	wakes := 0
	waking := &wakingReceiver{receiver: receiver, wake: func() { wakes++ }}
	request := message.ReceiveRequest{
		EnvelopeSender: "sender@example.com",
		Recipients:     []message.InboxID{inboxID},
		Raw:            bytes.NewReader([]byte("raw")),
	}
	if _, err := waking.Receive(context.Background(), request); err != nil {
		t.Fatalf("Receive(success) error = %v", err)
	}
	if wakes != 1 {
		t.Fatalf("wakes after success = %d, want 1", wakes)
	}
	repository.err = errors.New("database unavailable")
	request.Raw = bytes.NewReader([]byte("raw"))
	if _, err := waking.Receive(context.Background(), request); err == nil {
		t.Fatal("Receive(failure) error = nil")
	}
	if wakes != 1 {
		t.Fatalf("wakes after failure = %d, want 1", wakes)
	}
}

func testConfig(t *testing.T) config.Config {
	t.Helper()
	return config.Config{
		HTTP: config.HTTP{
			Addr:                "127.0.0.1:0",
			ReadHeaderTimeout:   time.Second,
			ReadTimeout:         time.Second,
			WriteTimeout:        time.Second,
			IdleTimeout:         time.Second,
			MaxHeaderBytes:      8 << 10,
			ReadinessTimeout:    time.Second,
			CreateRatePerMinute: 60,
			CreateRateBurst:     10,
		},
		LMTP: config.LMTP{
			Addr:             "127.0.0.1:0",
			Hostname:         "mailwisp.test",
			MaxMessageBytes:  1 << 20,
			MaxCommandBytes:  4 << 10,
			MaxDataLineBytes: 64 << 10,
			MaxRecipients:    10,
			MaxSessions:      10,
			SessionTimeout:   time.Second,
			DeliveryTimeout:  time.Second,
		},
		Postgres: config.Postgres{
			DSN:            "postgres://mailwisp:test@127.0.0.1:5432/mailwisp?sslmode=disable",
			MinConnections: 0,
			MaxConnections: 1,
			ConnectTimeout: time.Second,
		},
		Parser: config.Parser{
			Workers:       1,
			PollInterval:  100 * time.Millisecond,
			ParseTimeout:  100 * time.Millisecond,
			LeaseDuration: time.Second,
			MaxAttempts:   3,
			RetryBase:     100 * time.Millisecond,
			RetryMax:      time.Second,
		},
		Content: config.Content{
			Root:     filepath.Join(t.TempDir(), "content"),
			MaxBytes: 1 << 20,
		},
		Inbox: config.Inbox{
			PublicDomains:   []string{"mailwisp.test"},
			DefaultTTL:      time.Hour,
			MaxTTL:          24 * time.Hour,
			MaxMessages:     500,
			MaxStorageBytes: 1 << 20,
		},
		LogLevel:        slog.LevelInfo,
		ShutdownTimeout: time.Second,
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type appContentStoreStub struct{}

func (s *appContentStoreStub) Put(_ context.Context, source io.Reader) (message.ContentRef, error) {
	content, err := io.ReadAll(source)
	if err != nil {
		return message.ContentRef{}, err
	}
	return message.ContentRef{Key: "sha256/" + string(bytes.Repeat([]byte{'a'}, 64)), SizeBytes: int64(len(content))}, nil
}

type appDeliveryRepositoryStub struct {
	messages []message.StoredMessage
	err      error
}

func (s *appDeliveryRepositoryStub) CommitDelivery(context.Context, message.Delivery) ([]message.StoredMessage, error) {
	return s.messages, s.err
}
