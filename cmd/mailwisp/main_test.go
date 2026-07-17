package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"mailwisp/internal/buildinfo"
)

func TestParseCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		arguments []string
		want      command
		wantError bool
	}{
		{name: "default serve", want: command{role: "serve"}},
		{name: "explicit serve", arguments: []string{"serve"}, want: command{role: "serve"}},
		{name: "migrate", arguments: []string{"migrate"}, want: command{role: "migrate"}},
		{name: "reconcile", arguments: []string{"reconcile"}, want: command{role: "reconcile"}},
		{name: "cleanup", arguments: []string{"cleanup"}, want: command{role: "cleanup"}},
		{name: "version", arguments: []string{"version"}, want: command{role: "version"}},
		{name: "version JSON", arguments: []string{"version", "--json"}, want: command{role: "version", asJSON: true}},
		{name: "repair orphans", arguments: []string{"reconcile", "--repair-orphans"}, want: command{role: "reconcile", repairOrphans: true}},
		{name: "backup", arguments: []string{"backup", "backup-2026-07-15"}, want: command{role: "backup", path: "backup-2026-07-15"}},
		{name: "verify backup", arguments: []string{"backup", "verify", "backup-2026-07-15"}, want: command{role: "backup-verify", path: "backup-2026-07-15"}},
		{name: "restore", arguments: []string{"restore", "backup-2026-07-15"}, want: command{role: "restore", path: "backup-2026-07-15"}},
		{name: "backup missing destination", arguments: []string{"backup"}, wantError: true},
		{name: "restore missing bundle", arguments: []string{"restore"}, wantError: true},
		{name: "verify missing bundle", arguments: []string{"backup", "verify"}, wantError: true},
		{name: "unknown", arguments: []string{"unknown"}, wantError: true},
		{name: "too many", arguments: []string{"serve", "extra"}, wantError: true},
		{name: "unknown reconcile flag", arguments: []string{"reconcile", "--force"}, wantError: true},
		{name: "unknown version flag", arguments: []string{"version", "--yaml"}, wantError: true},
		{name: "version extra argument", arguments: []string{"version", "--json", "extra"}, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseCommand(test.arguments)
			if test.wantError && err == nil {
				t.Fatal("parseCommand() error = nil, want error")
			}
			if !test.wantError && (err != nil || got != test.want) {
				t.Fatalf("parseCommand() = %+v, %v, want %+v", got, err, test.want)
			}
		})
	}
}

func TestRunVersionDoesNotLoadConfiguration(t *testing.T) {
	t.Setenv("MAILWISP_LOG_LEVEL", "deliberately-invalid")

	var output bytes.Buffer
	if err := run([]string{"version"}, &output); err != nil {
		t.Fatalf("run(version) error = %v", err)
	}
	want := "MailWisp dev (commit unknown, built unknown)\n"
	if got := output.String(); got != want {
		t.Fatalf("run(version) output = %q, want %q", got, want)
	}
}

func TestRunVersionJSONDoesNotLoadConfiguration(t *testing.T) {
	t.Setenv("MAILWISP_LOG_LEVEL", "deliberately-invalid")

	var output bytes.Buffer
	if err := run([]string{"version", "--json"}, &output); err != nil {
		t.Fatalf("run(version --json) error = %v", err)
	}
	var got buildinfo.Info
	if err := json.Unmarshal(output.Bytes(), &got); err != nil {
		t.Fatalf("decode version JSON %q: %v", output.String(), err)
	}
	want := buildinfo.Current()
	if got != want {
		t.Fatalf("run(version --json) = %+v, want %+v", got, want)
	}
}
