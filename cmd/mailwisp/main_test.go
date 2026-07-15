package main

import "testing"

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
		{name: "repair orphans", arguments: []string{"reconcile", "--repair-orphans"}, want: command{role: "reconcile", repairOrphans: true}},
		{name: "backup", arguments: []string{"backup", "backup-2026-07-15"}, want: command{role: "backup", path: "backup-2026-07-15"}},
		{name: "restore", arguments: []string{"restore", "backup-2026-07-15"}, want: command{role: "restore", path: "backup-2026-07-15"}},
		{name: "backup missing destination", arguments: []string{"backup"}, wantError: true},
		{name: "restore missing bundle", arguments: []string{"restore"}, wantError: true},
		{name: "unknown", arguments: []string{"unknown"}, wantError: true},
		{name: "too many", arguments: []string{"serve", "extra"}, wantError: true},
		{name: "unknown reconcile flag", arguments: []string{"reconcile", "--force"}, wantError: true},
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
