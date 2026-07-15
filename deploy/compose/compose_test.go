package compose

import (
	"os"
	"strings"
	"testing"
)

func TestComposeProductionContract(t *testing.T) {
	tests := []struct {
		path      string
		required  []string
		forbidden []string
	}{
		{"compose.yaml", []string{"service_completed_successfully", "service_healthy", "POSTGRES_PASSWORD_FILE", "MAILWISP_CREATE_QUOTA_HMAC_KEY_FILE", "create_quota_hmac_key", "internal: true", "read_only: true", "no-new-privileges:true", "cap_drop:", "25:25", "80:80", "443:443"}, []string{"latest"}},
		{"Dockerfile", []string{"golang:1.26.5-alpine3.24@sha256:", "node:22.20.0-alpine3.22@sha256:", "nginx:1.30.3-alpine@sha256:", "USER 65532:65532", "npm@11.15.0", "npm ci", "-trimpath"}, []string{"latest"}},
		{"versions.lock", []string{"MAILWISP_NPM=11.15.0", "MAILWISP_POSTGRES=postgres:18.4-alpine@sha256:", "MAILWISP_POSTFIX_PACKAGE=3.11.5-r0"}, []string{"latest"}},
		{"../../.dockerignore", []string{".git", "web/node_modules", "deploy/compose/secrets"}, nil},
		{"postfix/Dockerfile", []string{"alpine:3.24.1@sha256:", "postfix=3.11.5-r0", "--chmod=0755"}, []string{"latest"}},
		{"postfix/entrypoint.sh", []string{"reject_unauth_destination", "smtpd_tls_protocols = >=TLSv1.2", "postfix check", "lmtp:inet:app:2525"}, nil},
		{"nginx/default.conf.template", []string{"ssl_protocols TLSv1.2 TLSv1.3", "Content-Security-Policy", "proxy_pass http://app:8080", "limit_req zone=mailwisp_create", "api|compat|open_api|user_api", "location = /metrics", "return 404"}, nil},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			content, err := os.ReadFile(test.path)
			if err != nil {
				t.Fatal(err)
			}
			text := string(content)
			for _, required := range test.required {
				if !strings.Contains(text, required) {
					t.Errorf("%s missing %q", test.path, required)
				}
			}
			for _, forbidden := range test.forbidden {
				if strings.Contains(strings.ToLower(text), forbidden) {
					t.Errorf("%s contains forbidden %q", test.path, forbidden)
				}
			}
		})
	}
}
