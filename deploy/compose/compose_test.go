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
		{"compose.yaml", []string{"service_completed_successfully", "service_healthy", "POSTGRES_PASSWORD_FILE", "MAILWISP_CREATE_QUOTA_HMAC_KEY_FILE", "create_quota_hmac_key", "postgres_data:/var/lib/postgresql", "content_data:/var/lib/mailwisp/content", "MAILWISP_CONTENT_ROOT: /var/lib/mailwisp/content", "maintenance:", "profiles: [\"maintenance\"]", "target: maintenance", "MAILWISP_BACKUP_ROOT_SOURCE", "internal: true", "smtp_ingress", "read_only: true", "no-new-privileges:true", "cap_drop:", "25:25", "80:80", "443:443"}, []string{"postgres_data:/var/lib/postgresql/data", "/var/lib/mailwisp/data", "backup-verifier:", "latest"}},
		{"backup-verifier.compose.yaml", []string{"name: mailwisp-backup-verifier", "backup-verifier:", "target: maintenance", "MAILWISP_BACKUP_ROOT_SOURCE", ":/backups:ro", "network_mode: none", "read_only: true", "no-new-privileges:true", "cap_drop:"}, []string{"environment:", "env_file:", "secrets:", "depends_on:", "content_data", "networks:", "latest"}},
		{"benchmark.compose.yaml", []string{"127.0.0.1:${MAILWISP_BENCH_HTTP_PORT:-18080}:8080", "127.0.0.1:${MAILWISP_BENCH_LMTP_PORT:-25250}:2525", "MAILWISP_CREATE_DAILY_LIMIT", "MAILWISP_INBOX_MAX_MESSAGES"}, []string{"0.0.0.0"}},
		{"production-e2e.compose.yaml", []string{"ports: !override", "MAILWISP_E2E_HTTP_PORT", "MAILWISP_E2E_HTTPS_PORT", "MAILWISP_E2E_SMTP_PORT", "host_ip: 127.0.0.1", "MAILWISP_E2E_CERT_ROOT"}, []string{"0.0.0.0"}},
		{"disaster-recovery.compose.yaml", []string{"volumes: !override", "content_data:/var/lib/mailwisp/content", "disaster_recovery_backup:/backups", "external: true", "MAILWISP_DR_BACKUP_VOLUME"}, []string{"0.0.0.0", "down --volumes", "/var/lib/mailwisp/data", "backup-verifier:"}},
		{"disaster-recovery-verifier.compose.yaml", []string{"backup-verifier:", "volumes: !override", "disaster_recovery_backup:/backups:ro", "external: true", "MAILWISP_DR_BACKUP_VOLUME"}, []string{"0.0.0.0", "down --volumes", "content_data"}},
		{"OPERATIONS.md", []string{"离线一致性备份", "backup-verifier", "Bundle完整性校验", "隔离恢复与切换", "--wait --wait-timeout 120", "重新签发", "恢复`25/tcp`放行", "不得删除或覆盖旧Volume", "版本升级与回滚", "禁止对已前向迁移", "最近一次灾备演练超过30天", "docker system prune"}, nil},
		{"prometheus-alerts.example.yml", []string{"MailWispHTTP5xxBurst", "MailWispLMTPTemporaryFailures", "MailWispStorageAdmissionErrors", "MailWispParserFailures", "MailWispRetentionFailures", "MailWispPostgreSQLPoolSaturation", "mailwisp_postgres_pool_acquired / mailwisp_postgres_pool_max_connections"}, nil},
		{"../../scripts/benchmark-compose.ps1", []string{"Save-BenchmarkDiagnostics", "Invoke-NativeWithRetry", "pinned PostgreSQL image pull", "Assert-LoopbackPortAvailable", "parser-drain.json", "down --volumes --remove-orphans"}, nil},
		{"../../scripts/e2e-compose.ps1", []string{"Protect-E2EFixture", "chmod 0700", "fixture path escaped", "New-E2ECertificate", "Wait-HTTPSReady", "Assert-HTTPRedirect", "Wait-SMTPReady", "Save-E2EDiagnostics", "Assert-NoReparsePoint", "must be a subdirectory", "MAILWISP_DOCKER_COMPOSE", "down --volumes --remove-orphans", "Production E2E cleanup failed", "status = 'passed'", "npm run test:e2e:production"}, nil},
		{"../../scripts/drill-compose-recovery.ps1", []string{"Protect-Fixture", "Assert-NoReparsePoint", "MAILWISP_DR_BACKUP_VOLUME", "mailwisp.managed=disaster-recovery-drill", "ps --all -q postgres", "npm run test:e2e:dr:seed", "stop edge postfix", "-Stopped", "backup-verifier backup verify /backups/recovery-bundle", "Assert-ComposeVolumeOwnership", "Assert-NoUnexpectedComposeVolumes", "redacted-test-address", "source volume destruction", "empty PostgreSQL inspection", "npm run test:e2e:dr:verify", "database_snapshot_match", "cleanup_resources_remaining", "down --volumes --remove-orphans"}, []string{"docker system prune", "docker volume prune"}},
		{"../../scripts/verify.ps1", []string{"MAILWISP_PROMETHEUS_TOOL", "Prometheus alert rules syntax and PromQL validation", "--network none", "--read-only", "--user 65534:65534", "--entrypoint /bin/promtool", "check rules /rules.yml"}, nil},
		{"../../web/playwright.disaster-recovery.config.ts", []string{"MAILWISP_DR_BASE_URL", "MAILWISP_DR_STATE_ROOT", "trace: 'off'", "video: 'off'", "screenshot: 'off'"}, nil},
		{"../../scripts/install-compose-linux.sh", []string{"MAILWISP_DOCKER_COMPOSE", "MAILWISP_DOCKER_COMPOSE_LINUX_X86_64_SHA256", "mktemp", "sha256sum --check --strict", "install -m 0755", "docker compose version --short"}, []string{"latest"}},
		{"../../.github/workflows/benchmark.yml", []string{"ubuntu-24.04", "./scripts/install-compose-linux.sh", "./scripts/benchmark-compose.ps1", "-Concurrency 1,4,16,32", "if: always()", "actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a"}, []string{"@main", "@v"}},
		{"Dockerfile", []string{"# syntax=docker/dockerfile:1.20.0@sha256:26147acbda4f14c5add9946e2fd2ed543fc402884fd75146bd342a7f6271dc1d", "golang:1.26.5-alpine3.24@sha256:", "node:22.20.0-alpine3.22@sha256:", "AS maintenance", "postgresql18-client=18.4-r0", "nginx:1.30.3-alpine@sha256:", "security-headers.conf", "USER 65532:65532", "npm@11.15.0", "npm ci", "-trimpath"}, []string{"# syntax=docker/dockerfile:1.20.0\n", "latest"}},
		{"versions.lock", []string{"MAILWISP_NPM=11.15.0", "MAILWISP_DOCKERFILE_FRONTEND=docker/dockerfile:1.20.0@sha256:26147acbda4f14c5add9946e2fd2ed543fc402884fd75146bd342a7f6271dc1d", "MAILWISP_DOCKER_COMPOSE=5.2.0", "MAILWISP_DOCKER_COMPOSE_LINUX_X86_64_SHA256=018f9612ecabc5f2d7aaa53d6f5f44453a87611e2d72c8ef84d7b1eca070e719", "MAILWISP_POSTGRES=postgres:18.4-alpine@sha256:", "MAILWISP_POSTGRESQL_CLIENT_PACKAGE=18.4-r0", "MAILWISP_PROMETHEUS_TOOL=prom/prometheus:v3.13.1@sha256:3c42b892cf723fa54d2f262c37a0e1f80aa8c8ddb1da7b9b0df9455a35a7f893", "MAILWISP_POSTFIX_PACKAGE=3.11.5-r0"}, []string{"latest"}},
		{"../../.dockerignore", []string{".git", "web/node_modules", "deploy/compose/secrets"}, nil},
		{"postfix/Dockerfile", []string{"alpine:3.24.1@sha256:", "postfix=3.11.5-r0", "--chmod=0755"}, []string{"latest"}},
		{"postfix/entrypoint.sh", []string{"reject_unauth_destination", "smtpd_tls_protocols = >=TLSv1.2", "postfix check", "relay_transport = lmtp:inet:app:2525", "alias_maps ="}, []string{"transport_maps = hash:", "postmap"}},
		{"nginx/default.conf.template", []string{"ssl_protocols TLSv1.2 TLSv1.3", "include /etc/nginx/snippets/mailwisp-security-headers.conf", "proxy_pass http://app:8080", "limit_req zone=mailwisp_create", "api|compat|open_api|user_api", "location = /metrics", "return 404"}, nil},
		{"nginx/security-headers.conf", []string{"Content-Security-Policy", "X-Content-Type-Options", "Cross-Origin-Opener-Policy", "always"}, nil},
		{"../../.github/workflows/verify.yml", []string{"./scripts/install-compose-linux.sh", "if: always()", "production-e2e-${{ github.run_id }}-${{ github.run_attempt }}", "artifacts/production-e2e/result.json", "disaster-recovery-${{ github.run_id }}-${{ github.run_attempt }}", "artifacts/disaster-recovery/result.json", "web/playwright-production-report", "web/test-results-production"}, nil},
		{"../../scripts/build-release.ps1", []string{"OPERATIONS.md", "prometheus-alerts.example.yml", "backup-verifier.compose.yaml"}, nil},
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

func TestComposeSMTPIngressIsolation(t *testing.T) {
	content, err := os.ReadFile("compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ReplaceAll(string(content), "\r\n", "\n")
	postgres := composeServiceBlock(t, text, "postgres", "migrate")
	postfix := composeServiceBlock(t, text, "postfix", "certbot")
	if strings.Contains(postgres, "smtp_ingress") {
		t.Fatal("postgres must not join the public SMTP ingress network")
	}
	for _, network := range []string{"- backend", "- smtp_ingress"} {
		if !strings.Contains(postfix, network) {
			t.Fatalf("postfix block missing %q", network)
		}
	}
	if !strings.Contains(text, "networks:\n  backend:\n    internal: true\n  frontend:\n  smtp_ingress:\n") {
		t.Fatal("backend must remain internal while smtp_ingress remains independently publishable")
	}
}

func TestComposeMaintenanceAndVerifierLeastPrivilege(t *testing.T) {
	content, err := os.ReadFile("compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ReplaceAll(string(content), "\r\n", "\n")
	maintenance := composeServiceBlock(t, text, "maintenance", "edge")
	for _, required := range []string{"postgres_password", "content_data:/var/lib/mailwisp/content", ":/backups"} {
		if !strings.Contains(maintenance, required) {
			t.Fatalf("maintenance block missing %q", required)
		}
	}
	for _, forbidden := range []string{"browser_session_key", "create_quota_hmac_key", "env_file:"} {
		if strings.Contains(maintenance, forbidden) {
			t.Fatalf("maintenance block contains overprivileged setting %q", forbidden)
		}
	}

	verifierContent, err := os.ReadFile("backup-verifier.compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	verifier := string(verifierContent)
	for _, required := range []string{"network_mode: none", ":/backups:ro", "read_only: true", "cap_drop:"} {
		if !strings.Contains(verifier, required) {
			t.Fatalf("backup-verifier block missing %q", required)
		}
	}
	for _, forbidden := range []string{"environment:", "env_file:", "secrets:", "depends_on:", "content_data", "networks:"} {
		if strings.Contains(verifier, forbidden) {
			t.Fatalf("backup-verifier block contains overprivileged setting %q", forbidden)
		}
	}
}

func composeServiceBlock(t *testing.T, content, service, nextService string) string {
	t.Helper()
	startMarker := "\n  " + service + ":\n"
	endMarker := "\n  " + nextService + ":\n"
	start := strings.Index(content, startMarker)
	end := strings.Index(content, endMarker)
	if start < 0 || end < 0 || end <= start {
		t.Fatalf("cannot locate %s service block", service)
	}
	return content[start:end]
}
