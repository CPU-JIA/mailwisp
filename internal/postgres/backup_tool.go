package postgres

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"mailwisp/internal/backup"
	"mailwisp/internal/message"
)

const (
	pgDumpExecutable    = "pg_dump"
	pgRestoreExecutable = "pg_restore"
	restoreServiceName  = "mailwisp_restore"
	maxToolErrorBytes   = 64 << 10
)

const restoreServiceFileContent = "[" + restoreServiceName + "]\napplication_name=mailwisp_restore\n"

var postgresVersionPattern = regexp.MustCompile(`\b([0-9]+(?:\.[0-9]+)+)\b`)

var postgresConnectionEnvironmentNames = map[string]struct{}{
	"PGAPPNAME": {}, "PGCHANNELBINDING": {}, "PGCLIENTENCODING": {},
	"PGCONNECT_TIMEOUT": {}, "PGDATABASE": {}, "PGGSSLIB": {}, "PGGSSENCMODE": {},
	"PGHOST": {}, "PGHOSTADDR": {}, "PGKRBSRVNAME": {},
	"PGLOADBALANCEHOSTS": {}, "PGMAXPROTOCOLVERSION": {}, "PGMINPROTOCOLVERSION": {},
	"PGOPTIONS": {}, "PGPASSFILE": {}, "PGPASSWORD": {},
	"PGPORT": {}, "PGREALM": {}, "PGREQUIREAUTH": {}, "PGREQUIREPEER": {}, "PGREQUIRESSL": {},
	"PGSERVICE": {}, "PGSERVICEFILE": {}, "PGSSLCERT": {},
	"PGSSLCOMPRESSION": {}, "PGSSLCRL": {}, "PGSSLCRLDIR": {}, "PGSSLKEY": {},
	"PGSSLMODE": {}, "PGSSLNEGOTIATION": {}, "PGSSLPASSWORD": {}, "PGSSLROOTCERT": {},
	"PGSSLSNI": {}, "PGSYSCONFDIR": {}, "PGTARGETSESSIONATTRS": {},
	"PGTZ": {}, "PGUSER": {},
}

// BackupTool invokes the official PostgreSQL logical backup tools while using
// the repository as the restored content catalog.
type BackupTool struct {
	pool                  *pgxpool.Pool
	repository            *DeliveryRepository
	connectionEnvironment []environmentVariable
	redactions            []string
}

// NewBackupTool constructs the official PostgreSQL backup adapter.
func NewBackupTool(dsn string, pool *pgxpool.Pool) (*BackupTool, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("postgres DSN is required")
	}
	repository, err := NewDeliveryRepository(pool)
	if err != nil {
		return nil, err
	}
	connectionConfig, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse postgres backup DSN: %w", err)
	}
	connectionEnvironment, err := postgresCommandEnvironment(connectionConfig)
	if err != nil {
		return nil, err
	}
	redactions := []string{dsn}
	if connectionConfig.Password != "" {
		redactions = append(redactions, connectionConfig.Password)
	}
	return &BackupTool{
		pool:                  pool,
		repository:            repository,
		connectionEnvironment: connectionEnvironment,
		redactions:            redactions,
	}, nil
}

// Dump writes one PostgreSQL custom-format dump.
func (t *BackupTool) Dump(ctx context.Context, destination io.Writer) (backup.DatabaseMetadata, error) {
	if destination == nil {
		return backup.DatabaseMetadata{}, errors.New("database dump destination is required")
	}
	metadata, err := t.metadata(ctx, true)
	if err != nil {
		return backup.DatabaseMetadata{}, err
	}
	if err := validateToolMajors(metadata); err != nil {
		return backup.DatabaseMetadata{}, err
	}
	command := exec.CommandContext(ctx, pgDumpExecutable,
		"--format=custom",
		"--no-owner",
		"--no-privileges",
		"--compress=0",
	)
	if err := t.run(command, nil, destination); err != nil {
		return backup.DatabaseMetadata{}, fmt.Errorf("run pg_dump: %w", err)
	}
	return metadata, nil
}

// Empty reports whether the public schema contains no restorable objects.
func (t *BackupTool) Empty(ctx context.Context) (bool, error) {
	var objects int
	if err := t.pool.QueryRow(ctx, `
		SELECT count(*)
		FROM pg_class class
		JOIN pg_namespace namespace ON namespace.oid = class.relnamespace
		WHERE namespace.nspname = 'public'
		  AND class.relkind IN ('r', 'p', 'v', 'm', 'S', 'f')
	`).Scan(&objects); err != nil {
		return false, fmt.Errorf("count restore database objects: %w", err)
	}
	return objects == 0, nil
}

// Restore reads one custom-format dump and restores it in a single database
// transaction. The target database must be empty.
func (t *BackupTool) Restore(ctx context.Context, source io.Reader) (backup.DatabaseMetadata, error) {
	if source == nil {
		return backup.DatabaseMetadata{}, errors.New("database dump source is required")
	}
	empty, err := t.Empty(ctx)
	if err != nil {
		return backup.DatabaseMetadata{}, err
	}
	if !empty {
		return backup.DatabaseMetadata{}, errors.New("restore database is not empty")
	}
	metadata, err := t.metadata(ctx, false)
	if err != nil {
		return backup.DatabaseMetadata{}, err
	}
	if err := validateToolMajors(metadata); err != nil {
		return backup.DatabaseMetadata{}, err
	}
	serviceFile, err := createRestoreServiceFile()
	if err != nil {
		return backup.DatabaseMetadata{}, err
	}
	command := exec.CommandContext(ctx, pgRestoreExecutable,
		"--dbname=service="+restoreServiceName,
		"--single-transaction",
		"--exit-on-error",
		"--no-owner",
		"--no-privileges",
	)
	restoreErr := t.run(command, source, io.Discard, environmentVariable{name: "PGSERVICEFILE", value: serviceFile})
	removeServiceErr := os.Remove(serviceFile)
	if restoreErr != nil {
		return backup.DatabaseMetadata{}, errors.Join(
			wrapError("run pg_restore", restoreErr),
			wrapError("remove temporary postgres service file", removeServiceErr),
		)
	}
	// The file contains only a fixed non-secret selector. Cleanup failure after
	// pg_restore commits must not turn success into a destructive restore error.
	if err := t.repository.Ready(ctx); err != nil {
		return backup.DatabaseMetadata{}, fmt.Errorf("verify restored postgres schema: %w", err)
	}
	migrationVersion, err := t.migrationVersion(ctx)
	if err != nil {
		return backup.DatabaseMetadata{}, err
	}
	metadata.MigrationVersion = migrationVersion
	return metadata, nil
}

// WalkContentRefs delegates restored content metadata traversal.
func (t *BackupTool) WalkContentRefs(ctx context.Context, batchSize int, visit func(message.ContentRef) error) error {
	return t.repository.WalkContentRefs(ctx, batchSize, visit)
}

// Ready verifies that the MailWisp schema required for backup is available.
func (t *BackupTool) Ready(ctx context.Context) error {
	return t.repository.Ready(ctx)
}

// ExistingContentKeys delegates bounded content-key lookup.
func (t *BackupTool) ExistingContentKeys(ctx context.Context, keys []string) (map[string]struct{}, error) {
	return t.repository.ExistingContentKeys(ctx, keys)
}

func (t *BackupTool) metadata(ctx context.Context, includeMigration bool) (backup.DatabaseMetadata, error) {
	var serverRaw string
	if err := t.pool.QueryRow(ctx, "SHOW server_version").Scan(&serverRaw); err != nil {
		return backup.DatabaseMetadata{}, fmt.Errorf("read postgres server version: %w", err)
	}
	serverVersion, err := parsePostgreSQLVersion(serverRaw)
	if err != nil {
		return backup.DatabaseMetadata{}, fmt.Errorf("parse postgres server version: %w", err)
	}
	dumpVersion, err := pgDumpVersion(ctx)
	if err != nil {
		return backup.DatabaseMetadata{}, err
	}
	restoreVersion, err := pgRestoreVersion(ctx)
	if err != nil {
		return backup.DatabaseMetadata{}, err
	}
	metadata := backup.DatabaseMetadata{
		ServerVersion:  serverVersion,
		DumpVersion:    dumpVersion,
		RestoreVersion: restoreVersion,
	}
	if includeMigration {
		migrationVersion, err := t.migrationVersion(ctx)
		if err != nil {
			return backup.DatabaseMetadata{}, err
		}
		metadata.MigrationVersion = migrationVersion
	}
	return metadata, nil
}

func (t *BackupTool) migrationVersion(ctx context.Context) (int64, error) {
	var version int64
	if err := t.pool.QueryRow(ctx, `
		SELECT COALESCE(max(version_id) FILTER (WHERE is_applied), 0)
		FROM goose_db_version
	`).Scan(&version); err != nil {
		return 0, fmt.Errorf("read database migration version: %w", err)
	}
	return version, nil
}

func (t *BackupTool) run(command *exec.Cmd, input io.Reader, output io.Writer, extraEnvironment ...environmentVariable) error {
	if command == nil {
		return errors.New("postgres command is required")
	}
	connectionEnvironment := make([]environmentVariable, 0, len(t.connectionEnvironment)+len(extraEnvironment))
	connectionEnvironment = append(connectionEnvironment, t.connectionEnvironment...)
	connectionEnvironment = append(connectionEnvironment, extraEnvironment...)
	command.Env = environmentWithPostgresConnection(os.Environ(), connectionEnvironment)
	command.Stdin = input
	command.Stdout = output
	stderr := &boundedBuffer{limit: maxToolErrorBytes}
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		detail := stderr.String()
		for _, secret := range t.redactions {
			detail = strings.ReplaceAll(detail, secret, "<redacted>")
		}
		detail = strings.TrimSpace(detail)
		if detail == "" {
			return err
		}
		return fmt.Errorf("%w: %s", err, detail)
	}
	return nil
}

func pgDumpVersion(ctx context.Context) (string, error) {
	return commandVersion(exec.CommandContext(ctx, pgDumpExecutable, "--version"), pgDumpExecutable)
}

func pgRestoreVersion(ctx context.Context) (string, error) {
	return commandVersion(exec.CommandContext(ctx, pgRestoreExecutable, "--version"), pgRestoreExecutable)
}

func commandVersion(command *exec.Cmd, name string) (string, error) {
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("read %s version: %w", name, err)
	}
	version, err := parsePostgreSQLVersion(string(output))
	if err != nil {
		return "", fmt.Errorf("parse %s version: %w", name, err)
	}
	return version, nil
}

func parsePostgreSQLVersion(value string) (string, error) {
	match := postgresVersionPattern.FindStringSubmatch(value)
	if len(match) != 2 {
		return "", errors.New("PostgreSQL version not found")
	}
	return match[1], nil
}

func validateToolMajors(metadata backup.DatabaseMetadata) error {
	serverMajor, err := postgreSQLMajor(metadata.ServerVersion)
	if err != nil {
		return fmt.Errorf("invalid postgres server version %q: %w", metadata.ServerVersion, err)
	}
	dumpMajor, err := postgreSQLMajor(metadata.DumpVersion)
	if err != nil {
		return fmt.Errorf("invalid pg_dump version %q: %w", metadata.DumpVersion, err)
	}
	restoreMajor, err := postgreSQLMajor(metadata.RestoreVersion)
	if err != nil {
		return fmt.Errorf("invalid pg_restore version %q: %w", metadata.RestoreVersion, err)
	}
	if serverMajor != dumpMajor || serverMajor != restoreMajor {
		return fmt.Errorf("postgres server/pg_dump/pg_restore major mismatch: %s/%s/%s", metadata.ServerVersion, metadata.DumpVersion, metadata.RestoreVersion)
	}
	return nil
}

func postgreSQLMajor(version string) (string, error) {
	parts := strings.Split(version, ".")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", errors.New("major version not found")
	}
	if parts[0] == "9" {
		return parts[0] + "." + parts[1], nil
	}
	return parts[0], nil
}

func createRestoreServiceFile() (string, error) {
	file, err := os.CreateTemp("", "mailwisp-pgservice-*.conf")
	if err != nil {
		return "", fmt.Errorf("create temporary postgres service file: %w", err)
	}
	path := file.Name()
	complete := false
	defer func() {
		if !complete {
			_ = file.Close()
			_ = os.Remove(path)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return "", fmt.Errorf("restrict temporary postgres service file: %w", err)
	}
	if _, err := io.WriteString(file, restoreServiceFileContent); err != nil {
		return "", fmt.Errorf("write temporary postgres service file: %w", err)
	}
	if err := file.Sync(); err != nil {
		return "", fmt.Errorf("sync temporary postgres service file: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close temporary postgres service file: %w", err)
	}
	complete = true
	return path, nil
}

func wrapError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}

type environmentVariable struct {
	name  string
	value string
}

func postgresCommandEnvironment(config *pgx.ConnConfig) ([]environmentVariable, error) {
	if config == nil {
		return nil, errors.New("postgres connection config is required")
	}
	endpoints := []string{net.JoinHostPort(config.Host, strconv.Itoa(int(config.Port)))}
	for _, fallback := range config.Fallbacks {
		endpoint := net.JoinHostPort(fallback.Host, strconv.Itoa(int(fallback.Port)))
		found := false
		for _, existing := range endpoints {
			if existing == endpoint {
				found = true
				break
			}
		}
		if !found {
			endpoints = append(endpoints, endpoint)
		}
	}
	if len(endpoints) != 1 {
		return nil, errors.New("postgres backup tool requires a single-host DSN")
	}
	if config.ValidateConnect != nil {
		return nil, errors.New("postgres backup tool does not support target_session_attrs")
	}
	sslMode, sslRootCert, sslSNI, err := postgresCommandTLS(config)
	if err != nil {
		return nil, err
	}
	variables := []environmentVariable{
		{name: "PGHOST", value: config.Host},
		{name: "PGPORT", value: strconv.Itoa(int(config.Port))},
		{name: "PGDATABASE", value: config.Database},
		{name: "PGUSER", value: config.User},
		{name: "PGPASSWORD", value: config.Password},
		{name: "PGSSLMODE", value: sslMode},
	}
	if config.ConnectTimeout > 0 {
		seconds := (config.ConnectTimeout + time.Second - 1) / time.Second
		variables = append(variables, environmentVariable{name: "PGCONNECT_TIMEOUT", value: strconv.FormatInt(int64(seconds), 10)})
	}
	if sslRootCert != "" {
		variables = append(variables, environmentVariable{name: "PGSSLROOTCERT", value: sslRootCert})
	}
	if sslSNI != "" {
		variables = append(variables, environmentVariable{name: "PGSSLSNI", value: sslSNI})
	}
	if config.SSLNegotiation != "" {
		variables = append(variables, environmentVariable{name: "PGSSLNEGOTIATION", value: config.SSLNegotiation})
	}
	if config.ChannelBinding != "" {
		variables = append(variables, environmentVariable{name: "PGCHANNELBINDING", value: config.ChannelBinding})
	}
	if config.RequireAuth != "" {
		variables = append(variables, environmentVariable{name: "PGREQUIREAUTH", value: config.RequireAuth})
	}
	if config.MinProtocolVersion != "" {
		variables = append(variables, environmentVariable{name: "PGMINPROTOCOLVERSION", value: config.MinProtocolVersion})
	}
	if config.MaxProtocolVersion != "" {
		variables = append(variables, environmentVariable{name: "PGMAXPROTOCOLVERSION", value: config.MaxProtocolVersion})
	}
	return variables, nil
}

func postgresCommandTLS(config *pgx.ConnConfig) (mode, rootCert, sni string, err error) {
	if config.TLSConfig != nil {
		if config.TLSConfig.RootCAs != nil || len(config.TLSConfig.Certificates) != 0 {
			return "", "", "", errors.New("postgres backup tool does not yet support custom TLS certificate files")
		}
	}
	hasTLSFallback := false
	hasPlainFallback := false
	for _, fallback := range config.Fallbacks {
		if fallback.Host != config.Host || fallback.Port != config.Port {
			continue
		}
		if fallback.TLSConfig == nil {
			hasPlainFallback = true
		} else {
			hasTLSFallback = true
		}
	}
	switch {
	case config.TLSConfig == nil && hasTLSFallback:
		mode = "allow"
	case config.TLSConfig == nil:
		mode = "disable"
	case hasPlainFallback:
		mode = "prefer"
	case !config.TLSConfig.InsecureSkipVerify:
		mode = "verify-full"
		rootCert = "system"
	case config.TLSConfig.VerifyPeerCertificate != nil:
		mode = "verify-ca"
		rootCert = "system"
	default:
		mode = "require"
	}
	if config.TLSConfig != nil && config.TLSConfig.ServerName == "" && net.ParseIP(config.Host) == nil {
		sni = "0"
	}
	return mode, rootCert, sni, nil
}

func environmentWith(environment []string, name, value string) []string {
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		entryName, _, found := strings.Cut(entry, "=")
		if found && strings.EqualFold(entryName, name) {
			continue
		}
		result = append(result, entry)
	}
	return append(result, name+"="+value)
}

func environmentWithPostgresConnection(environment []string, variables []environmentVariable) []string {
	result := make([]string, 0, len(environment)+len(variables))
	for _, entry := range environment {
		entryName, _, found := strings.Cut(entry, "=")
		if found {
			if _, reserved := postgresConnectionEnvironmentNames[strings.ToUpper(entryName)]; reserved {
				continue
			}
		}
		result = append(result, entry)
	}
	for _, variable := range variables {
		result = append(result, variable.name+"="+variable.value)
	}
	return result
}

type boundedBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func (b *boundedBuffer) Write(data []byte) (int, error) {
	accepted := len(data)
	remaining := b.limit - b.buffer.Len()
	if remaining > 0 {
		if remaining < len(data) {
			data = data[:remaining]
		}
		_, _ = b.buffer.Write(data)
	}
	return accepted, nil
}

func (b *boundedBuffer) String() string {
	return b.buffer.String()
}
