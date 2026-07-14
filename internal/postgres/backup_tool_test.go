package postgres

import (
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"mailwisp/internal/backup"
)

func TestParsePostgreSQLVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		value     string
		want      string
		wantError bool
	}{
		{name: "server", value: "18.4 (Debian 18.4-1.pgdg130+1)", want: "18.4"},
		{name: "pg dump", value: "pg_dump (PostgreSQL) 18.4", want: "18.4"},
		{name: "legacy", value: "PostgreSQL 9.6.24", want: "9.6.24"},
		{name: "missing", value: "PostgreSQL unknown", wantError: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := parsePostgreSQLVersion(test.value)
			if test.wantError {
				if err == nil {
					t.Fatal("parsePostgreSQLVersion() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("parsePostgreSQLVersion() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("parsePostgreSQLVersion() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestValidateToolMajors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		metadata  backup.DatabaseMetadata
		wantError bool
	}{
		{
			name: "postgres 18 match",
			metadata: backup.DatabaseMetadata{
				ServerVersion: "18.4", DumpVersion: "18.3", RestoreVersion: "18.4",
			},
		},
		{
			name: "legacy match",
			metadata: backup.DatabaseMetadata{
				ServerVersion: "9.6.24", DumpVersion: "9.6.22", RestoreVersion: "9.6.24",
			},
		},
		{
			name: "dump mismatch",
			metadata: backup.DatabaseMetadata{
				ServerVersion: "18.4", DumpVersion: "17.8", RestoreVersion: "18.4",
			},
			wantError: true,
		},
		{
			name: "invalid version",
			metadata: backup.DatabaseMetadata{
				ServerVersion: "18", DumpVersion: "18.4", RestoreVersion: "18.4",
			},
			wantError: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := validateToolMajors(test.metadata)
			if test.wantError && err == nil {
				t.Fatal("validateToolMajors() error = nil, want error")
			}
			if !test.wantError && err != nil {
				t.Fatalf("validateToolMajors() error = %v", err)
			}
		})
	}
}

func TestEnvironmentWithReplacesOneVariable(t *testing.T) {
	t.Parallel()

	environment := []string{"PATH=/bin", "PGDATABASE=old", "pgdatabase=duplicate", "EMPTY="}
	got := environmentWith(environment, "PGDATABASE", "secret")
	want := []string{"PATH=/bin", "EMPTY=", "PGDATABASE=secret"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("environmentWith() = %#v, want %#v", got, want)
	}
}

func TestEnvironmentWithPostgresConnectionRemovesAmbientOverrides(t *testing.T) {
	t.Parallel()

	environment := []string{
		"PATH=/bin",
		"PGDATABASE=ambient",
		"PGSERVICE=unsafe",
		"PGSSLROOTCERT=/ambient/root.crt",
	}
	variables := []environmentVariable{
		{name: "PGHOST", value: "127.0.0.1"},
		{name: "PGDATABASE", value: "mailwisp"},
	}
	got := environmentWithPostgresConnection(environment, variables)
	want := []string{"PATH=/bin", "PGHOST=127.0.0.1", "PGDATABASE=mailwisp"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("environmentWithPostgresConnection() = %#v, want %#v", got, want)
	}
}

func TestPostgresCommandEnvironmentUsesParsedConnectionFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		dsn  string
	}{
		{
			name: "url",
			dsn:  "postgres://mailwisp:secret@127.0.0.1:55432/mailwisp_test?sslmode=disable&connect_timeout=7",
		},
		{
			name: "keyword value",
			dsn:  "host=127.0.0.1 port=55432 dbname=mailwisp_test user=mailwisp password=secret sslmode=disable connect_timeout=7",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			config, err := pgx.ParseConfig(test.dsn)
			if err != nil {
				t.Fatalf("pgx.ParseConfig() error = %v", err)
			}
			variables, err := postgresCommandEnvironment(config)
			if err != nil {
				t.Fatalf("postgresCommandEnvironment() error = %v", err)
			}
			got := environmentVariableMap(variables)
			want := map[string]string{
				"PGHOST":            "127.0.0.1",
				"PGPORT":            "55432",
				"PGDATABASE":        "mailwisp_test",
				"PGUSER":            "mailwisp",
				"PGPASSWORD":        "secret",
				"PGSSLMODE":         "disable",
				"PGCONNECT_TIMEOUT": "7",
			}
			for name, value := range want {
				if got[name] != value {
					t.Errorf("environment %s = %q, want %q", name, got[name], value)
				}
			}
			for _, variable := range variables {
				if strings.Contains(variable.value, "postgres://") {
					t.Fatalf("environment %s contains raw DSN", variable.name)
				}
			}
		})
	}
}

func TestPostgresCommandEnvironmentRejectsUnsupportedRouting(t *testing.T) {
	t.Parallel()

	tests := []string{
		"postgres://mailwisp:secret@first:5432,second:5432/mailwisp?sslmode=disable",
		"postgres://mailwisp:secret@127.0.0.1:5432/mailwisp?sslmode=disable&target_session_attrs=read-write",
	}
	for _, dsn := range tests {
		config, err := pgx.ParseConfig(dsn)
		if err != nil {
			t.Fatalf("pgx.ParseConfig(%q) error = %v", dsn, err)
		}
		if _, err := postgresCommandEnvironment(config); err == nil {
			t.Fatalf("postgresCommandEnvironment(%q) error = nil", dsn)
		}
	}
}

func TestCreateRestoreServiceFileContainsNoConnectionData(t *testing.T) {
	t.Parallel()

	path, err := createRestoreServiceFile()
	if err != nil {
		t.Fatalf("createRestoreServiceFile() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(service file) error = %v", err)
	}
	if string(content) != restoreServiceFileContent {
		t.Fatalf("service file = %q, want %q", content, restoreServiceFileContent)
	}
	for _, secret := range []string{"host", "port", "dbname", "user", "password", "sslmode"} {
		if strings.Contains(strings.ToLower(string(content)), secret+"=") {
			t.Fatalf("service file contains connection field %q", secret)
		}
	}
}

func TestBoundedBuffer(t *testing.T) {
	t.Parallel()

	buffer := &boundedBuffer{limit: 5}
	first := []byte("abc")
	second := []byte("defgh")
	if written, err := buffer.Write(first); err != nil || written != len(first) {
		t.Fatalf("first Write() = %d, %v", written, err)
	}
	if written, err := buffer.Write(second); err != nil || written != len(second) {
		t.Fatalf("second Write() = %d, %v", written, err)
	}
	if got := buffer.String(); got != "abcde" {
		t.Fatalf("boundedBuffer.String() = %q, want %q", got, "abcde")
	}
}

func environmentVariableMap(variables []environmentVariable) map[string]string {
	result := make(map[string]string, len(variables))
	for _, variable := range variables {
		result[variable.name] = variable.value
	}
	return result
}
