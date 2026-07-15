package config

import (
	"log/slog"
	"os"
	"path/filepath"
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
	if len(cfg.Inbox.PublicDomains) != 1 || cfg.Inbox.DefaultTTL != 24*time.Hour || cfg.Compatibility.DuckMailEnabled || cfg.Compatibility.YYDSEnabled {
		t.Fatalf("Inbox/compatibility defaults = %+v/%+v", cfg.Inbox, cfg.Compatibility)
	}
	if len(cfg.BrowserSession.Key) != 0 || cfg.BrowserSession.Lifetime != 12*time.Hour {
		t.Fatalf("BrowserSession defaults = %+v", cfg.BrowserSession)
	}
	if cfg.Cleanup.BatchSize != 100 || cfg.Cleanup.Interval != 5*time.Minute || cfg.Cleanup.Timeout != 2*time.Minute {
		t.Fatalf("Cleanup defaults = %+v", cfg.Cleanup)
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

func TestLoadRejectsInvalidCompatibilityBoolean(t *testing.T) {
	for _, name := range []string{"DUCKMAIL_ENABLED", "YYDS_ENABLED"} {
		t.Run(name, func(t *testing.T) {
			clearConfigurationEnvironment(t)
			t.Setenv(prefix+"POSTGRES_DSN", "postgres://mailwisp:test@127.0.0.1:5432/mailwisp?sslmode=disable")
			t.Setenv(prefix+name, "sometimes")
			if _, err := Load(); err == nil {
				t.Fatal("Load() error = nil, want compatibility boolean parsing error")
			}
		})
	}
}

func TestLoadBrowserSessionKey(t *testing.T) {
	clearConfigurationEnvironment(t)
	t.Setenv(prefix+"POSTGRES_DSN", "postgres://mailwisp:test@127.0.0.1:5432/mailwisp?sslmode=disable")
	t.Setenv(prefix+"BROWSER_SESSION_KEY", "a2tra2tra2tra2tra2tra2tra2tra2tra2tra2tra2s")
	cfg, err := Load()
	if err != nil || len(cfg.BrowserSession.Key) != 32 {
		t.Fatalf("Load() key length = %d, error = %v", len(cfg.BrowserSession.Key), err)
	}
}

func TestLoadBrowserSessionKeyFile(t *testing.T) {
	clearConfigurationEnvironment(t)
	path := filepath.Join(t.TempDir(), "browser-session-key")
	if err := os.WriteFile(path, []byte("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(prefix+"POSTGRES_DSN", "postgres://mailwisp:test@127.0.0.1:5432/mailwisp?sslmode=disable")
	t.Setenv(prefix+"BROWSER_SESSION_KEY_FILE", path)
	cfg, err := Load()
	if err != nil || len(cfg.BrowserSession.Key) != 32 {
		t.Fatalf("Load() key length = %d, error = %v", len(cfg.BrowserSession.Key), err)
	}
}

func TestLoadRequiresPostgresDSN(t *testing.T) {
	clearConfigurationEnvironment(t)
	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want missing PostgreSQL DSN error")
	}
}

func TestLoadPostgresPasswordFile(t *testing.T) {
	clearConfigurationEnvironment(t)
	path := filepath.Join(t.TempDir(), "postgres-password")
	if err := os.WriteFile(path, []byte("p@ss word\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(prefix+"POSTGRES_DSN", "postgres://mailwisp@postgres:5432/mailwisp?sslmode=disable")
	t.Setenv(prefix+"POSTGRES_PASSWORD_FILE", path)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Postgres.DSN != "postgres://mailwisp:p%40ss%20word@postgres:5432/mailwisp?sslmode=disable" {
		t.Fatalf("Postgres.DSN = %q", cfg.Postgres.DSN)
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
		"CREATE_RATE_PER_MINUTE",
		"CREATE_RATE_BURST",
		"TRUSTED_PROXY_CIDRS",
		"PUBLIC_DOMAINS",
		"INBOX_DEFAULT_TTL",
		"INBOX_MAX_TTL",
		"DUCKMAIL_ENABLED",
		"YYDS_ENABLED",
		"BROWSER_SESSION_KEY",
		"BROWSER_SESSION_KEY_FILE",
		"BROWSER_SESSION_LIFETIME",
		"CLEANUP_BATCH_SIZE",
		"CLEANUP_INTERVAL",
		"CLEANUP_TIMEOUT",
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
		"POSTGRES_PASSWORD_FILE",
		"POSTGRES_MIN_CONNECTIONS",
		"POSTGRES_MAX_CONNECTIONS",
		"POSTGRES_CONNECT_TIMEOUT",
		"CONTENT_ROOT",
		"CONTENT_MAX_BYTES",
	} {
		t.Setenv(prefix+name, "")
	}
}
