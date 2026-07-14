package config

import (
	"log/slog"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	for _, name := range []string{
		"HTTP_ADDR",
		"LOG_LEVEL",
		"SHUTDOWN_TIMEOUT",
		"READ_HEADER_TIMEOUT",
		"READ_TIMEOUT",
		"WRITE_TIMEOUT",
		"IDLE_TIMEOUT",
		"MAX_HEADER_BYTES",
	} {
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
}

func TestLoadRejectsInvalidLimits(t *testing.T) {
	t.Setenv(prefix+"MAX_HEADER_BYTES", "12")
	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want validation error")
	}
}

func TestLoadRejectsInvalidLogLevel(t *testing.T) {
	t.Setenv(prefix+"LOG_LEVEL", "extremely-loud")
	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want parsing error")
	}
}
