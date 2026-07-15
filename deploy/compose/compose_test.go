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
		{"compose.yaml", []string{"service_completed_successfully", "service_healthy", "POSTGRES_PASSWORD_FILE", "MAILWISP_CREATE_QUOTA_HMAC_KEY_FILE", "create_quota_hmac_key", "postgres_data:/var/lib/postgresql", "internal: true", "read_only: true", "no-new-privileges:true", "cap_drop:", "25:25", "80:80", "443:443"}, []string{"postgres_data:/var/lib/postgresql/data", "latest"}},
		{"benchmark.compose.yaml", []string{"127.0.0.1:${MAILWISP_BENCH_HTTP_PORT:-18080}:8080", "127.0.0.1:${MAILWISP_BENCH_LMTP_PORT:-25250}:2525", "MAILWISP_CREATE_DAILY_LIMIT", "MAILWISP_INBOX_MAX_MESSAGES"}, []string{"0.0.0.0"}},
		{"../../scripts/benchmark-compose.ps1", []string{"Save-BenchmarkDiagnostics", "Assert-LoopbackPortAvailable", "parser-drain.json", "down --volumes --remove-orphans"}, nil},
		{"../../.github/workflows/benchmark.yml", []string{"ubuntu-24.04", "./scripts/benchmark-compose.ps1", "-Concurrency 1,4,16,32", "if: always()", "actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a"}, []string{"@main", "@v"}},
		{"Dockerfile", []string{"golang:1.26.5-alpine3.24@sha256:", "node:22.20.0-alpine3.22@sha256:", "nginx:1.30.3-alpine@sha256:", "USER 65532:65532", "npm@11.15.0", "npm ci", "-trimpath"}, []string{"latest"}},
		{"versions.lock", []string{"MAILWISP_NPM=11.15.0", "MAILWISP_DOCKER_COMPOSE=5.2.0", "MAILWISP_DOCKER_COMPOSE_LINUX_X86_64_SHA256=018f9612ecabc5f2d7aaa53d6f5f44453a87611e2d72c8ef84d7b1eca070e719", "MAILWISP_POSTGRES=postgres:18.4-alpine@sha256:", "MAILWISP_POSTFIX_PACKAGE=3.11.5-r0"}, []string{"latest"}},
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
