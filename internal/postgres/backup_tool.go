package postgres

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"mailwisp/internal/backup"
	"mailwisp/internal/message"
)

const (
	pgDumpExecutable    = "pg_dump"
	pgRestoreExecutable = "pg_restore"
	maxToolErrorBytes   = 64 << 10
)

var postgresVersionPattern = regexp.MustCompile(`\b([0-9]+(?:\.[0-9]+)+)\b`)

// BackupTool invokes the official PostgreSQL logical backup tools while using
// the repository as the restored content catalog.
type BackupTool struct {
	dsn        string
	pool       *pgxpool.Pool
	repository *DeliveryRepository
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
	return &BackupTool{dsn: dsn, pool: pool, repository: repository}, nil
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
	command := exec.CommandContext(ctx, pgRestoreExecutable,
		"--single-transaction",
		"--exit-on-error",
		"--no-owner",
		"--no-privileges",
	)
	if err := t.run(command, source, io.Discard); err != nil {
		return backup.DatabaseMetadata{}, fmt.Errorf("run pg_restore: %w", err)
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

func (t *BackupTool) run(command *exec.Cmd, input io.Reader, output io.Writer) error {
	if command == nil {
		return errors.New("postgres command is required")
	}
	command.Env = environmentWith(os.Environ(), "PGDATABASE", t.dsn)
	command.Stdin = input
	command.Stdout = output
	stderr := &boundedBuffer{limit: maxToolErrorBytes}
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		detail := strings.TrimSpace(strings.ReplaceAll(stderr.String(), t.dsn, "<redacted>"))
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
