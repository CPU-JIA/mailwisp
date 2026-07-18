// Package config loads and validates immutable application configuration.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const prefix = "MAILWISP_"

// Config contains all process-level MailWisp settings.
type Config struct {
	HTTP            HTTP
	LMTP            LMTP
	Parser          Parser
	Postgres        Postgres
	Content         Content
	Inbox           Inbox
	CreateQuota     CreateQuota
	Compatibility   Compatibility
	BrowserSession  BrowserSession
	Cleanup         Cleanup
	LogLevel        slog.Level
	ShutdownTimeout time.Duration
}

// HTTP contains public HTTP server limits and timeouts.
type HTTP struct {
	Addr                 string
	ReadHeaderTimeout    time.Duration
	ReadTimeout          time.Duration
	WriteTimeout         time.Duration
	IdleTimeout          time.Duration
	MaxHeaderBytes       int
	ReadinessTimeout     time.Duration
	HeavyReadConcurrency int
	CreateRatePerMinute  int
	CreateRateBurst      int
	TrustedProxyCIDRs    []string
}

// Inbox contains public anonymous Inbox lifecycle settings.
type Inbox struct {
	PublicDomains   []string
	DefaultTTL      time.Duration
	MaxTTL          time.Duration
	MaxMessages     int
	MaxStorageBytes int64
}

// CreateQuota contains persistent anonymous Inbox creation admission settings.
type CreateQuota struct {
	DailyLimit int
	HMACKey    []byte
}

// Compatibility enables isolated third-party HTTP adapters.
type Compatibility struct {
	DuckMailEnabled              bool
	YYDSEnabled                  bool
	CloudflareTempEnabled        bool
	CloudflareLegacyPathsEnabled bool
}

// BrowserSession contains optional same-origin Cookie authentication settings.
type BrowserSession struct {
	Key      []byte
	Lifetime time.Duration
}

// Cleanup contains bounded retention settings.
type Cleanup struct {
	BatchSize int
	Interval  time.Duration
	Timeout   time.Duration
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

// Parser contains bounded background MIME parsing settings.
type Parser struct {
	Workers       int
	PollInterval  time.Duration
	ParseTimeout  time.Duration
	LeaseDuration time.Duration
	MaxAttempts   int
	RetryBase     time.Duration
	RetryMax      time.Duration
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
	Root         string
	MaxBytes     int64
	MinFreeBytes int64
}

// Load reads configuration from the process environment and validates it.
func Load() (Config, error) {
	logLevel, err := parseLogLevel(value("LOG_LEVEL", "info"))
	if err != nil {
		return Config{}, err
	}
	duckMailEnabled, err := parseBoolean("DUCKMAIL_ENABLED", false)
	if err != nil {
		return Config{}, err
	}
	yydsEnabled, err := parseBoolean("YYDS_ENABLED", false)
	if err != nil {
		return Config{}, err
	}
	cloudflareTempEnabled, err := parseBoolean("CLOUDFLARE_TEMP_ENABLED", false)
	if err != nil {
		return Config{}, err
	}
	cloudflareLegacyPathsEnabled, err := parseBoolean("CLOUDFLARE_LEGACY_PATHS_ENABLED", false)
	if err != nil {
		return Config{}, err
	}
	postgresDSN, err := postgresDSNWithPasswordFile(value("POSTGRES_DSN", ""), strings.TrimSpace(os.Getenv(prefix+"POSTGRES_PASSWORD_FILE")))
	if err != nil {
		return Config{}, err
	}
	browserSessionKey, err := secretKeyFromEnvironment("BROWSER_SESSION_KEY")
	if err != nil {
		return Config{}, err
	}
	createQuotaKey, err := secretKeyFromEnvironment("CREATE_QUOTA_HMAC_KEY")
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		HTTP: HTTP{
			Addr:                 value("HTTP_ADDR", ":8080"),
			ReadHeaderTimeout:    duration("READ_HEADER_TIMEOUT", 5*time.Second),
			ReadTimeout:          duration("READ_TIMEOUT", 10*time.Second),
			WriteTimeout:         duration("WRITE_TIMEOUT", 15*time.Second),
			IdleTimeout:          duration("IDLE_TIMEOUT", 60*time.Second),
			MaxHeaderBytes:       integer("MAX_HEADER_BYTES", 1<<20),
			ReadinessTimeout:     duration("READINESS_TIMEOUT", 2*time.Second),
			HeavyReadConcurrency: integer("HEAVY_READ_CONCURRENCY", 4),
			CreateRatePerMinute:  integer("CREATE_RATE_PER_MINUTE", 12),
			CreateRateBurst:      integer("CREATE_RATE_BURST", 4),
			TrustedProxyCIDRs:    commaSeparated("TRUSTED_PROXY_CIDRS", "127.0.0.1/32,::1/128"),
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
		Parser: Parser{
			Workers:       integer("PARSER_WORKERS", 2),
			PollInterval:  duration("PARSER_POLL_INTERVAL", time.Second),
			ParseTimeout:  duration("PARSER_TIMEOUT", 30*time.Second),
			LeaseDuration: duration("PARSER_LEASE_DURATION", time.Minute),
			MaxAttempts:   integer("PARSER_MAX_ATTEMPTS", 5),
			RetryBase:     duration("PARSER_RETRY_BASE", 5*time.Second),
			RetryMax:      duration("PARSER_RETRY_MAX", 5*time.Minute),
		},
		Postgres: Postgres{
			DSN:            postgresDSN,
			MinConnections: integer32("POSTGRES_MIN_CONNECTIONS", 1),
			MaxConnections: integer32("POSTGRES_MAX_CONNECTIONS", 10),
			ConnectTimeout: duration("POSTGRES_CONNECT_TIMEOUT", 5*time.Second),
		},
		Content: Content{
			Root:         value("CONTENT_ROOT", "./data/content"),
			MaxBytes:     integer64("CONTENT_MAX_BYTES", 25<<20),
			MinFreeBytes: integer64("CONTENT_MIN_FREE_BYTES", 1<<30),
		},
		Inbox: Inbox{
			PublicDomains:   commaSeparated("PUBLIC_DOMAINS", "mailwisp.local"),
			DefaultTTL:      duration("INBOX_DEFAULT_TTL", 24*time.Hour),
			MaxTTL:          duration("INBOX_MAX_TTL", 7*24*time.Hour),
			MaxMessages:     integer("INBOX_MAX_MESSAGES", 500),
			MaxStorageBytes: integer64("INBOX_MAX_STORAGE_BYTES", 256<<20),
		},
		CreateQuota: CreateQuota{
			DailyLimit: integer("CREATE_DAILY_LIMIT", 100),
			HMACKey:    createQuotaKey,
		},
		Compatibility: Compatibility{
			DuckMailEnabled:              duckMailEnabled,
			YYDSEnabled:                  yydsEnabled,
			CloudflareTempEnabled:        cloudflareTempEnabled,
			CloudflareLegacyPathsEnabled: cloudflareLegacyPathsEnabled,
		},
		BrowserSession: BrowserSession{
			Key:      browserSessionKey,
			Lifetime: duration("BROWSER_SESSION_LIFETIME", 12*time.Hour),
		},
		Cleanup: Cleanup{
			BatchSize: integer("CLEANUP_BATCH_SIZE", 100),
			Interval:  duration("CLEANUP_INTERVAL", 5*time.Minute),
			Timeout:   duration("CLEANUP_TIMEOUT", 2*time.Minute),
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
	if c.HTTP.HeavyReadConcurrency <= 0 || c.HTTP.HeavyReadConcurrency > 128 {
		errs = append(errs, errors.New("MAILWISP_HEAVY_READ_CONCURRENCY must be between 1 and 128"))
	}
	if c.HTTP.CreateRatePerMinute <= 0 || c.HTTP.CreateRatePerMinute > 10000 {
		errs = append(errs, errors.New("MAILWISP_CREATE_RATE_PER_MINUTE must be between 1 and 10000"))
	}
	if c.HTTP.CreateRateBurst <= 0 || c.HTTP.CreateRateBurst > c.HTTP.CreateRatePerMinute {
		errs = append(errs, errors.New("MAILWISP_CREATE_RATE_BURST must be positive and not exceed MAILWISP_CREATE_RATE_PER_MINUTE"))
	}
	for _, cidr := range c.HTTP.TrustedProxyCIDRs {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			errs = append(errs, fmt.Errorf("MAILWISP_TRUSTED_PROXY_CIDRS contains invalid CIDR %q", cidr))
		}
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
	if c.Parser.Workers <= 0 || c.Parser.Workers > 64 {
		errs = append(errs, errors.New("MAILWISP_PARSER_WORKERS must be between 1 and 64"))
	}
	if c.Parser.PollInterval < 100*time.Millisecond || c.Parser.PollInterval > time.Minute {
		errs = append(errs, errors.New("MAILWISP_PARSER_POLL_INTERVAL must be between 100ms and 1m"))
	}
	if c.Parser.ParseTimeout <= 0 || c.Parser.ParseTimeout > 5*time.Minute {
		errs = append(errs, errors.New("MAILWISP_PARSER_TIMEOUT must be between 1ns and 5m"))
	}
	if c.Parser.LeaseDuration <= c.Parser.ParseTimeout || c.Parser.LeaseDuration > 10*time.Minute {
		errs = append(errs, errors.New("MAILWISP_PARSER_LEASE_DURATION must exceed parser timeout and be at most 10m"))
	}
	if c.Parser.MaxAttempts <= 0 || c.Parser.MaxAttempts > 100 {
		errs = append(errs, errors.New("MAILWISP_PARSER_MAX_ATTEMPTS must be between 1 and 100"))
	}
	if c.Parser.RetryBase <= 0 || c.Parser.RetryMax < c.Parser.RetryBase || c.Parser.RetryMax > 24*time.Hour {
		errs = append(errs, errors.New("MAILWISP_PARSER_RETRY_BASE and MAILWISP_PARSER_RETRY_MAX must define a positive range up to 24h"))
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
	if c.Content.MaxBytes < c.LMTP.MaxMessageBytes || c.Content.MaxBytes > 1<<50 {
		errs = append(errs, errors.New("MAILWISP_CONTENT_MAX_BYTES must be at least MAILWISP_LMTP_MAX_MESSAGE_BYTES and at most 1125899906842624"))
	}
	if c.Content.MinFreeBytes < c.LMTP.MaxMessageBytes || c.Content.MinFreeBytes > 1<<50 {
		errs = append(errs, errors.New("MAILWISP_CONTENT_MIN_FREE_BYTES must be at least MAILWISP_LMTP_MAX_MESSAGE_BYTES and at most 1125899906842624"))
	}
	if len(c.Inbox.PublicDomains) == 0 {
		errs = append(errs, errors.New("MAILWISP_PUBLIC_DOMAINS must contain at least one domain"))
	}
	seenDomains := make(map[string]struct{}, len(c.Inbox.PublicDomains))
	for _, domain := range c.Inbox.PublicDomains {
		if !validDomain(domain) {
			errs = append(errs, fmt.Errorf("MAILWISP_PUBLIC_DOMAINS contains invalid domain %q", domain))
			continue
		}
		if _, exists := seenDomains[domain]; exists {
			errs = append(errs, fmt.Errorf("MAILWISP_PUBLIC_DOMAINS contains duplicate domain %q", domain))
		}
		seenDomains[domain] = struct{}{}
	}
	if c.Inbox.DefaultTTL <= 0 || c.Inbox.MaxTTL <= 0 || c.Inbox.DefaultTTL > c.Inbox.MaxTTL {
		errs = append(errs, errors.New("MAILWISP_INBOX_DEFAULT_TTL and MAILWISP_INBOX_MAX_TTL must define a positive ordered range"))
	}
	if c.Inbox.MaxMessages <= 0 || c.Inbox.MaxMessages > 1_000_000 {
		errs = append(errs, errors.New("MAILWISP_INBOX_MAX_MESSAGES must be between 1 and 1000000"))
	}
	if c.Inbox.MaxStorageBytes < c.LMTP.MaxMessageBytes || c.Inbox.MaxStorageBytes > 1<<50 {
		errs = append(errs, errors.New("MAILWISP_INBOX_MAX_STORAGE_BYTES must be at least MAILWISP_LMTP_MAX_MESSAGE_BYTES and at most 1125899906842624"))
	}
	if c.CreateQuota.DailyLimit <= 0 || c.CreateQuota.DailyLimit > 1_000_000 {
		errs = append(errs, errors.New("MAILWISP_CREATE_DAILY_LIMIT must be between 1 and 1000000"))
	}
	if len(c.CreateQuota.HMACKey) != 0 && len(c.CreateQuota.HMACKey) != 32 {
		errs = append(errs, errors.New("MAILWISP_CREATE_QUOTA_HMAC_KEY must decode to exactly 32 bytes"))
	}
	if c.Compatibility.CloudflareLegacyPathsEnabled && !c.Compatibility.CloudflareTempEnabled {
		errs = append(errs, errors.New("MAILWISP_CLOUDFLARE_LEGACY_PATHS_ENABLED requires MAILWISP_CLOUDFLARE_TEMP_ENABLED"))
	}
	if len(c.BrowserSession.Key) != 0 && len(c.BrowserSession.Key) != 32 {
		errs = append(errs, errors.New("MAILWISP_BROWSER_SESSION_KEY must decode to exactly 32 bytes"))
	}
	if c.BrowserSession.Lifetime <= 0 || c.BrowserSession.Lifetime > 7*24*time.Hour {
		errs = append(errs, errors.New("MAILWISP_BROWSER_SESSION_LIFETIME must be between 1ns and 168h"))
	}
	if c.ShutdownTimeout <= 0 {
		errs = append(errs, errors.New("MAILWISP_SHUTDOWN_TIMEOUT must be positive"))
	}
	if c.Cleanup.BatchSize <= 0 || c.Cleanup.BatchSize > 1000 {
		errs = append(errs, errors.New("MAILWISP_CLEANUP_BATCH_SIZE must be between 1 and 1000"))
	}
	if c.Cleanup.Interval < 0 || c.Cleanup.Interval > 24*time.Hour {
		errs = append(errs, errors.New("MAILWISP_CLEANUP_INTERVAL must be between 0 and 24h"))
	}
	if c.Cleanup.Interval > 0 && (c.Cleanup.Timeout <= 0 || c.Cleanup.Timeout >= c.Cleanup.Interval) {
		errs = append(errs, errors.New("MAILWISP_CLEANUP_TIMEOUT must be positive and shorter than MAILWISP_CLEANUP_INTERVAL"))
	}
	return errors.Join(errs...)
}

func value(name, fallback string) string {
	if raw := strings.TrimSpace(os.Getenv(prefix + name)); raw != "" {
		return raw
	}
	return fallback
}

func commaSeparated(name, fallback string) []string {
	raw := value(name, fallback)
	parts := strings.Split(raw, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		if normalized := strings.ToLower(strings.TrimSpace(part)); normalized != "" {
			values = append(values, normalized)
		}
	}
	return values
}

func validDomain(domain string) bool {
	if len(domain) < 3 || len(domain) > 253 || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return false
	}
	labels := strings.Split(domain, ".")
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
				return false
			}
		}
	}
	return true
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

func parseBoolean(name string, fallback bool) (bool, error) {
	raw := value(name, strconv.FormatBool(fallback))
	parsed, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("MAILWISP_%s: %w", name, err)
	}
	return parsed, nil
}

func decodeSecretKey(name string) ([]byte, error) {
	raw := strings.TrimSpace(os.Getenv(prefix + name))
	if raw == "" {
		return nil, nil
	}
	for _, encoding := range []*base64.Encoding{base64.RawURLEncoding.Strict(), base64.StdEncoding.Strict()} {
		decoded, err := encoding.DecodeString(raw)
		if err == nil {
			if len(decoded) != 32 {
				return nil, fmt.Errorf("MAILWISP_%s must decode to exactly 32 bytes", name)
			}
			return decoded, nil
		}
	}
	return nil, fmt.Errorf("MAILWISP_%s must be base64 or base64url", name)
}

func secretKeyFromEnvironment(name string) ([]byte, error) {
	keyFile := strings.TrimSpace(os.Getenv(prefix + name + "_FILE"))
	keyValue := strings.TrimSpace(os.Getenv(prefix + name))
	if keyFile != "" && keyValue != "" {
		return nil, fmt.Errorf("MAILWISP_%s and MAILWISP_%s_FILE must not both be configured", name, name)
	}
	if keyFile == "" {
		return decodeSecretKey(name)
	}
	raw, err := readConfiguredFile(keyFile, 4096)
	if err != nil {
		return nil, fmt.Errorf("read MAILWISP_%s_FILE: %w", name, err)
	}
	if len(raw) > 4096 {
		return nil, fmt.Errorf("MAILWISP_%s_FILE must not exceed 4096 bytes", name)
	}
	value := strings.TrimSpace(string(raw))
	if value == "" {
		return nil, fmt.Errorf("MAILWISP_%s_FILE must not be empty", name)
	}
	for _, encoding := range []*base64.Encoding{base64.RawURLEncoding.Strict(), base64.StdEncoding.Strict()} {
		decoded, decodeErr := encoding.DecodeString(value)
		if decodeErr == nil {
			if len(decoded) != 32 {
				return nil, fmt.Errorf("MAILWISP_%s_FILE must decode to exactly 32 bytes", name)
			}
			return decoded, nil
		}
	}
	return nil, fmt.Errorf("MAILWISP_%s_FILE must contain base64 or base64url", name)
}

func parseLogLevel(raw string) (slog.Level, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(raw)); err != nil {
		return 0, fmt.Errorf("MAILWISP_LOG_LEVEL: %w", err)
	}
	return level, nil
}

func postgresDSNWithPasswordFile(dsn, passwordFile string) (string, error) {
	if passwordFile == "" {
		return dsn, nil
	}
	passwordBytes, err := readConfiguredFile(passwordFile, 4096)
	if err != nil {
		return "", fmt.Errorf("read MAILWISP_POSTGRES_PASSWORD_FILE: %w", err)
	}
	password := strings.TrimSpace(string(passwordBytes))
	if password == "" || len(passwordBytes) > 4096 || strings.ContainsRune(password, '\x00') {
		return "", errors.New("MAILWISP_POSTGRES_PASSWORD_FILE must contain 1 to 4096 non-NUL bytes")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Scheme == "" || parsed.User == nil || parsed.User.Username() == "" {
		return "", errors.New("MAILWISP_POSTGRES_DSN must be a URL with a username when password file is configured")
	}
	parsed.User = url.UserPassword(parsed.User.Username(), password)
	return parsed.String(), nil
}

func readConfiguredFile(path string, maxBytes int64) ([]byte, error) {
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(filepath.Dir(absolute))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	file, err := root.Open(filepath.Base(absolute))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(io.LimitReader(file, maxBytes+1))
}
