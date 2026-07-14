package main

import "testing"

func TestParseRole(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		arguments []string
		want      string
		wantError bool
	}{
		{name: "default serve", want: "serve"},
		{name: "explicit serve", arguments: []string{"serve"}, want: "serve"},
		{name: "migrate", arguments: []string{"migrate"}, want: "migrate"},
		{name: "unknown", arguments: []string{"unknown"}, wantError: true},
		{name: "too many", arguments: []string{"serve", "extra"}, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseRole(test.arguments)
			if test.wantError && err == nil {
				t.Fatal("parseRole() error = nil, want error")
			}
			if !test.wantError && (err != nil || got != test.want) {
				t.Fatalf("parseRole() = %q, %v, want %q", got, err, test.want)
			}
		})
	}
}
