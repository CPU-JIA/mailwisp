package postgres

import (
	"strings"
	"testing"

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
