// Package config loads and validates immutable application configuration.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

const prefix = "MAILWISP_"

// Config contains all process-level MailWisp settings.
type Config struct {
	HTTP            HTTP
	LogLevel        slog.Level
	ShutdownTimeout time.Duration
}

// HTTP contains public HTTP server limits and timeouts.
type HTTP struct {
	Addr              string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	MaxHeaderBytes    int
}

// Load reads configuration from the process environment and validates it.
func Load() (Config, error) {
	logLevel, err := parseLogLevel(value("LOG_LEVEL", "info"))
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		HTTP: HTTP{
			Addr:              value("HTTP_ADDR", ":8080"),
			ReadHeaderTimeout: duration("READ_HEADER_TIMEOUT", 5*time.Second),
			ReadTimeout:       duration("READ_TIMEOUT", 10*time.Second),
			WriteTimeout:      duration("WRITE_TIMEOUT", 15*time.Second),
			IdleTimeout:       duration("IDLE_TIMEOUT", 60*time.Second),
			MaxHeaderBytes:    integer("MAX_HEADER_BYTES", 1<<20),
		},
		LogLevel:        logLevel,
		ShutdownTimeout: duration("SHUTDOWN_TIMEOUT", 10*time.Second),
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate rejects unsafe or nonsensical configuration.
func (c Config) Validate() error {
	var errs []error
	if strings.TrimSpace(c.HTTP.Addr) == "" {
		errs = append(errs, errors.New("MAILWISP_HTTP_ADDR must not be empty"))
	}
	if c.HTTP.ReadHeaderTimeout <= 0 {
		errs = append(errs, errors.New("MAILWISP_READ_HEADER_TIMEOUT must be positive"))
	}
	if c.HTTP.ReadTimeout <= 0 {
		errs = append(errs, errors.New("MAILWISP_READ_TIMEOUT must be positive"))
	}
	if c.HTTP.WriteTimeout <= 0 {
		errs = append(errs, errors.New("MAILWISP_WRITE_TIMEOUT must be positive"))
	}
	if c.HTTP.IdleTimeout <= 0 {
		errs = append(errs, errors.New("MAILWISP_IDLE_TIMEOUT must be positive"))
	}
	if c.HTTP.MaxHeaderBytes < 8<<10 || c.HTTP.MaxHeaderBytes > 4<<20 {
		errs = append(errs, errors.New("MAILWISP_MAX_HEADER_BYTES must be between 8192 and 4194304"))
	}
	if c.ShutdownTimeout <= 0 {
		errs = append(errs, errors.New("MAILWISP_SHUTDOWN_TIMEOUT must be positive"))
	}
	return errors.Join(errs...)
}

func value(name, fallback string) string {
	if raw := strings.TrimSpace(os.Getenv(prefix + name)); raw != "" {
		return raw
	}
	return fallback
}

func duration(name string, fallback time.Duration) time.Duration {
	raw := value(name, fallback.String())
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return 0
	}
	return parsed
}

func integer(name string, fallback int) int {
	raw := value(name, strconv.Itoa(fallback))
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		return 0
	}
	return parsed
}

func parseLogLevel(raw string) (slog.Level, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(raw)); err != nil {
		return 0, fmt.Errorf("MAILWISP_LOG_LEVEL: %w", err)
	}
	return level, nil
}
