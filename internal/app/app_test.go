package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"mailwisp/internal/config"
)

func TestNewComposesApplicationWithoutConnecting(t *testing.T) {
	t.Parallel()

	cfg := testConfig(t)
	application, err := New(context.Background(), cfg, discardLogger())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	application.pool.Close()
	if application.http == nil || application.lmtp == nil || application.repository == nil {
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

func testConfig(t *testing.T) config.Config {
	t.Helper()
	return config.Config{
		HTTP: config.HTTP{
			Addr:              "127.0.0.1:0",
			ReadHeaderTimeout: time.Second,
			ReadTimeout:       time.Second,
			WriteTimeout:      time.Second,
			IdleTimeout:       time.Second,
			MaxHeaderBytes:    8 << 10,
			ReadinessTimeout:  time.Second,
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
		Content: config.Content{
			Root:     filepath.Join(t.TempDir(), "content"),
			MaxBytes: 1 << 20,
		},
		LogLevel:        slog.LevelInfo,
		ShutdownTimeout: time.Second,
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
