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
	LMTP            LMTP
	Postgres        Postgres
	Content         Content
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
	ReadinessTimeout  time.Duration
}

// LMTP contains local delivery protocol limits and timeouts.
type LMTP struct {
	Addr             string
	Hostname         string
	MaxMessageBytes  int64
	MaxCommandBytes  int
	MaxDataLineBytes int
	MaxRecipients    int
	MaxSessions      int
	SessionTimeout   time.Duration
	DeliveryTimeout  time.Duration
}

// Postgres contains PostgreSQL connection pool settings.
type Postgres struct {
	DSN            string
	MinConnections int32
	MaxConnections int32
	ConnectTimeout time.Duration
}

// Content contains immutable raw-content storage settings.
type Content struct {
	Root     string
	MaxBytes int64
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
			ReadinessTimeout:  duration("READINESS_TIMEOUT", 2*time.Second),
		},
		LMTP: LMTP{
			Addr:             value("LMTP_ADDR", "127.0.0.1:2525"),
			Hostname:         value("LMTP_HOSTNAME", "mailwisp.local"),
			MaxMessageBytes:  integer64("LMTP_MAX_MESSAGE_BYTES", 25<<20),
			MaxCommandBytes:  integer("LMTP_MAX_COMMAND_BYTES", 4<<10),
			MaxDataLineBytes: integer("LMTP_MAX_DATA_LINE_BYTES", 64<<10),
			MaxRecipients:    integer("LMTP_MAX_RECIPIENTS", 100),
			MaxSessions:      integer("LMTP_MAX_SESSIONS", 64),
			SessionTimeout:   duration("LMTP_SESSION_TIMEOUT", 5*time.Minute),
			DeliveryTimeout:  duration("LMTP_DELIVERY_TIMEOUT", 30*time.Second),
		},
		Postgres: Postgres{
			DSN:            value("POSTGRES_DSN", ""),
			MinConnections: integer32("POSTGRES_MIN_CONNECTIONS", 1),
			MaxConnections: integer32("POSTGRES_MAX_CONNECTIONS", 10),
			ConnectTimeout: duration("POSTGRES_CONNECT_TIMEOUT", 5*time.Second),
		},
		Content: Content{
			Root:     value("CONTENT_ROOT", "./data/content"),
			MaxBytes: integer64("CONTENT_MAX_BYTES", 25<<20),
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
	if c.HTTP.ReadinessTimeout <= 0 || c.HTTP.ReadinessTimeout > 10*time.Second {
		errs = append(errs, errors.New("MAILWISP_READINESS_TIMEOUT must be between 1ns and 10s"))
	}
	if strings.TrimSpace(c.LMTP.Addr) == "" {
		errs = append(errs, errors.New("MAILWISP_LMTP_ADDR must not be empty"))
	}
	if strings.TrimSpace(c.LMTP.Hostname) == "" || strings.ContainsAny(c.LMTP.Hostname, "\r\n\t ") {
		errs = append(errs, errors.New("MAILWISP_LMTP_HOSTNAME must be a non-empty hostname without whitespace"))
	}
	if c.LMTP.MaxMessageBytes <= 0 {
		errs = append(errs, errors.New("MAILWISP_LMTP_MAX_MESSAGE_BYTES must be positive"))
	}
	if c.LMTP.MaxCommandBytes < 512 || c.LMTP.MaxCommandBytes > 64<<10 {
		errs = append(errs, errors.New("MAILWISP_LMTP_MAX_COMMAND_BYTES must be between 512 and 65536"))
	}
	if c.LMTP.MaxDataLineBytes < 998 || c.LMTP.MaxDataLineBytes > 1<<20 {
		errs = append(errs, errors.New("MAILWISP_LMTP_MAX_DATA_LINE_BYTES must be between 998 and 1048576"))
	}
	if c.LMTP.MaxRecipients <= 0 || c.LMTP.MaxRecipients > 1000 {
		errs = append(errs, errors.New("MAILWISP_LMTP_MAX_RECIPIENTS must be between 1 and 1000"))
	}
	if c.LMTP.MaxSessions <= 0 || c.LMTP.MaxSessions > 10000 {
		errs = append(errs, errors.New("MAILWISP_LMTP_MAX_SESSIONS must be between 1 and 10000"))
	}
	if c.LMTP.SessionTimeout <= 0 || c.LMTP.DeliveryTimeout <= 0 {
		errs = append(errs, errors.New("MAILWISP_LMTP timeouts must be positive"))
	}
	if c.LMTP.SessionTimeout < c.LMTP.DeliveryTimeout {
		errs = append(errs, errors.New("MAILWISP_LMTP_SESSION_TIMEOUT must not be shorter than MAILWISP_LMTP_DELIVERY_TIMEOUT"))
	}
	if strings.TrimSpace(c.Postgres.DSN) == "" {
		errs = append(errs, errors.New("MAILWISP_POSTGRES_DSN must not be empty"))
	}
	if c.Postgres.MinConnections < 0 {
		errs = append(errs, errors.New("MAILWISP_POSTGRES_MIN_CONNECTIONS must not be negative"))
	}
	if c.Postgres.MaxConnections <= 0 || c.Postgres.MaxConnections > 1000 {
		errs = append(errs, errors.New("MAILWISP_POSTGRES_MAX_CONNECTIONS must be between 1 and 1000"))
	}
	if c.Postgres.MinConnections > c.Postgres.MaxConnections {
		errs = append(errs, errors.New("MAILWISP_POSTGRES_MIN_CONNECTIONS must not exceed MAILWISP_POSTGRES_MAX_CONNECTIONS"))
	}
	if c.Postgres.ConnectTimeout <= 0 {
		errs = append(errs, errors.New("MAILWISP_POSTGRES_CONNECT_TIMEOUT must be positive"))
	}
	if strings.TrimSpace(c.Content.Root) == "" {
		errs = append(errs, errors.New("MAILWISP_CONTENT_ROOT must not be empty"))
	}
	if c.Content.MaxBytes < c.LMTP.MaxMessageBytes {
		errs = append(errs, errors.New("MAILWISP_CONTENT_MAX_BYTES must be at least MAILWISP_LMTP_MAX_MESSAGE_BYTES"))
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

func integer64(name string, fallback int64) int64 {
	raw := value(name, strconv.FormatInt(fallback, 10))
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0
	}
	return parsed
}

func integer32(name string, fallback int32) int32 {
	raw := value(name, strconv.FormatInt(int64(fallback), 10))
	parsed, err := strconv.ParseInt(raw, 10, 32)
	if err != nil {
		return 0
	}
	return int32(parsed)
}

func parseLogLevel(raw string) (slog.Level, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(raw)); err != nil {
		return 0, fmt.Errorf("MAILWISP_LOG_LEVEL: %w", err)
	}
	return level, nil
}
