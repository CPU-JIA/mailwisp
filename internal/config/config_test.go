package config

import (
	"log/slog"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	clearConfigurationEnvironment(t)
	t.Setenv(prefix+"POSTGRES_DSN", "postgres://mailwisp:test@127.0.0.1:5432/mailwisp?sslmode=disable")

	for _, name := range []string{"LOG_LEVEL"} {
		t.Setenv(prefix+name, "")
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.HTTP.Addr != ":8080" {
		t.Fatalf("HTTP.Addr = %q, want %q", cfg.HTTP.Addr, ":8080")
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Fatalf("LogLevel = %v, want %v", cfg.LogLevel, slog.LevelInfo)
	}
	if cfg.ShutdownTimeout != 10*time.Second {
		t.Fatalf("ShutdownTimeout = %v, want %v", cfg.ShutdownTimeout, 10*time.Second)
	}
	if cfg.LMTP.Addr != "127.0.0.1:2525" || cfg.LMTP.MaxMessageBytes != 25<<20 {
		t.Fatalf("LMTP defaults = %+v", cfg.LMTP)
	}
	if cfg.Parser.Workers != 2 || cfg.Parser.ParseTimeout != 30*time.Second || cfg.Parser.LeaseDuration != time.Minute {
		t.Fatalf("Parser defaults = %+v", cfg.Parser)
	}
	if cfg.Postgres.MaxConnections != 10 || cfg.Postgres.MinConnections != 1 {
		t.Fatalf("Postgres defaults = %+v", cfg.Postgres)
	}
	if cfg.Content.Root != "./data/content" || cfg.Content.MaxBytes != 25<<20 {
		t.Fatalf("Content defaults = %+v", cfg.Content)
	}
}

func TestLoadRejectsInvalidLimits(t *testing.T) {
	clearConfigurationEnvironment(t)
	t.Setenv(prefix+"POSTGRES_DSN", "postgres://mailwisp:test@127.0.0.1:5432/mailwisp?sslmode=disable")
	t.Setenv(prefix+"MAX_HEADER_BYTES", "12")
	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want validation error")
	}
}

func TestLoadRejectsInvalidLogLevel(t *testing.T) {
	clearConfigurationEnvironment(t)
	t.Setenv(prefix+"POSTGRES_DSN", "postgres://mailwisp:test@127.0.0.1:5432/mailwisp?sslmode=disable")
	t.Setenv(prefix+"LOG_LEVEL", "extremely-loud")
	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want parsing error")
	}
}

func TestLoadRequiresPostgresDSN(t *testing.T) {
	clearConfigurationEnvironment(t)
	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want missing PostgreSQL DSN error")
	}
}

func TestValidateRejectsCrossComponentLimitMismatch(t *testing.T) {
	clearConfigurationEnvironment(t)
	t.Setenv(prefix+"POSTGRES_DSN", "postgres://mailwisp:test@127.0.0.1:5432/mailwisp?sslmode=disable")
	t.Setenv(prefix+"LMTP_MAX_MESSAGE_BYTES", "1024")
	t.Setenv(prefix+"CONTENT_MAX_BYTES", "512")
	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want content/LMTP mismatch error")
	}
}

func TestValidateRejectsInvalidPostgresPool(t *testing.T) {
	clearConfigurationEnvironment(t)
	t.Setenv(prefix+"POSTGRES_DSN", "postgres://mailwisp:test@127.0.0.1:5432/mailwisp?sslmode=disable")
	t.Setenv(prefix+"POSTGRES_MIN_CONNECTIONS", "11")
	t.Setenv(prefix+"POSTGRES_MAX_CONNECTIONS", "10")
	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want invalid PostgreSQL pool error")
	}
}

func TestValidateRejectsParserLeaseNotLongerThanTimeout(t *testing.T) {
	clearConfigurationEnvironment(t)
	t.Setenv(prefix+"POSTGRES_DSN", "postgres://mailwisp:test@127.0.0.1:5432/mailwisp?sslmode=disable")
	t.Setenv(prefix+"PARSER_TIMEOUT", "30s")
	t.Setenv(prefix+"PARSER_LEASE_DURATION", "30s")
	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want parser lease validation error")
	}
}

func clearConfigurationEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"HTTP_ADDR",
		"LOG_LEVEL",
		"SHUTDOWN_TIMEOUT",
		"READ_HEADER_TIMEOUT",
		"READ_TIMEOUT",
		"WRITE_TIMEOUT",
		"IDLE_TIMEOUT",
		"MAX_HEADER_BYTES",
		"READINESS_TIMEOUT",
		"LMTP_ADDR",
		"LMTP_HOSTNAME",
		"LMTP_MAX_MESSAGE_BYTES",
		"LMTP_MAX_COMMAND_BYTES",
		"LMTP_MAX_DATA_LINE_BYTES",
		"LMTP_MAX_RECIPIENTS",
		"LMTP_MAX_SESSIONS",
		"LMTP_SESSION_TIMEOUT",
		"LMTP_DELIVERY_TIMEOUT",
		"PARSER_WORKERS",
		"PARSER_POLL_INTERVAL",
		"PARSER_TIMEOUT",
		"PARSER_LEASE_DURATION",
		"PARSER_MAX_ATTEMPTS",
		"PARSER_RETRY_BASE",
		"PARSER_RETRY_MAX",
		"POSTGRES_DSN",
		"POSTGRES_MIN_CONNECTIONS",
		"POSTGRES_MAX_CONNECTIONS",
		"POSTGRES_CONNECT_TIMEOUT",
		"CONTENT_ROOT",
		"CONTENT_MAX_BYTES",
	} {
		t.Setenv(prefix+name, "")
	}
}
