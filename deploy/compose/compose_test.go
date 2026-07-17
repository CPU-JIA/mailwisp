package compose

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
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
		{"release.compose.yaml", []string{"migrate:", "app:", "maintenance:", "edge:", "postfix:", "build: !reset null", "pull_policy: never"}, []string{"latest"}},
		{"backup-verifier.compose.yaml", []string{"name: mailwisp-backup-verifier", "backup-verifier:", "target: maintenance", "MAILWISP_BACKUP_ROOT_SOURCE", ":/backups:ro", "network_mode: none", "read_only: true", "no-new-privileges:true", "cap_drop:"}, []string{"environment:", "env_file:", "secrets:", "depends_on:", "content_data", "networks:", "latest"}},
		{"backup-verifier.release.compose.yaml", []string{"backup-verifier:", "build: !reset null", "pull_policy: never"}, []string{"latest"}},
		{"benchmark.compose.yaml", []string{"127.0.0.1:${MAILWISP_BENCH_HTTP_PORT:-18080}:8080", "127.0.0.1:${MAILWISP_BENCH_LMTP_PORT:-25250}:2525", "MAILWISP_CREATE_DAILY_LIMIT", "MAILWISP_INBOX_MAX_MESSAGES"}, []string{"0.0.0.0"}},
		{"production-e2e.compose.yaml", []string{"ports: !override", "MAILWISP_E2E_HTTP_PORT", "MAILWISP_E2E_HTTPS_PORT", "MAILWISP_E2E_SMTP_PORT", "host_ip: 127.0.0.1", "MAILWISP_E2E_CERT_ROOT"}, []string{"0.0.0.0"}},
		{"disaster-recovery.compose.yaml", []string{"volumes: !override", "content_data:/var/lib/mailwisp/content", "disaster_recovery_backup:/backups", "external: true", "MAILWISP_DR_BACKUP_VOLUME"}, []string{"0.0.0.0", "down --volumes", "/var/lib/mailwisp/data", "backup-verifier:"}},
		{"disaster-recovery-verifier.compose.yaml", []string{"backup-verifier:", "volumes: !override", "disaster_recovery_backup:/backups:ro", "external: true", "MAILWISP_DR_BACKUP_VOLUME"}, []string{"0.0.0.0", "down --volumes", "content_data"}},
		{"OPERATIONS.md", []string{"离线一致性备份", "backup-verifier", "Bundle完整性校验", "隔离恢复与切换", "--wait --wait-timeout 120", "重新签发", "恢复`25/tcp`放行", "不得删除或覆盖旧Volume", "版本升级与回滚", "禁止对已前向迁移", "最近一次灾备演练超过30天", "docker system prune"}, nil},
		{"prometheus-alerts.example.yml", []string{"MailWispHTTP5xxBurst", "MailWispLMTPTemporaryFailures", "MailWispStorageAdmissionErrors", "MailWispParserFailures", "MailWispRetentionFailures", "MailWispPostgreSQLPoolSaturation", "mailwisp_postgres_pool_acquired / mailwisp_postgres_pool_max_connections"}, nil},
		{"../../scripts/benchmark-compose.ps1", []string{"Save-BenchmarkDiagnostics", "Invoke-NativeWithRetry", "pinned PostgreSQL image pull", "Assert-LoopbackPortAvailable", "parser-drain.json", "down --volumes --remove-orphans"}, nil},
		{"../../scripts/e2e-compose.ps1", []string{"Protect-E2EFixture", "chmod 0700", "fixture path escaped", "New-E2ECertificate", "Wait-HTTPSReady", "Assert-HTTPRedirect", "Wait-SMTPReady", "Save-E2EDiagnostics", "Assert-NoReparsePoint", "must be a subdirectory", "MAILWISP_DOCKER_COMPOSE", "ComposeFiles", "ImageTag", "MAILWISP_IMAGE_TAG", "down --volumes --remove-orphans", "Production E2E cleanup failed", "status = 'passed'", "npm run test:e2e:production"}, nil},
		{"../../scripts/drill-compose-recovery.ps1", []string{"Protect-Fixture", "Assert-NoReparsePoint", "MAILWISP_DR_BACKUP_VOLUME", "mailwisp.managed=disaster-recovery-drill", "ps --all -q postgres", "npm run test:e2e:dr:seed", "stop edge postfix", "-Stopped", "backup-verifier backup verify /backups/recovery-bundle", "Assert-ComposeVolumeOwnership", "Assert-NoUnexpectedComposeVolumes", "redacted-test-address", "source volume destruction", "empty PostgreSQL inspection", "npm run test:e2e:dr:verify", "database_snapshot_match", "cleanup_resources_remaining", "down --volumes --remove-orphans"}, []string{"docker system prune", "docker volume prune"}},
		{"../../scripts/verify.ps1", []string{"MAILWISP_PROMETHEUS_TOOL", "Prometheus alert rules syntax and PromQL validation", "--network none", "--read-only", "--user 65534:65534", "--entrypoint /bin/promtool", "check rules /rules.yml"}, nil},
		{"../../web/playwright.disaster-recovery.config.ts", []string{"MAILWISP_DR_BASE_URL", "MAILWISP_DR_STATE_ROOT", "trace: 'off'", "video: 'off'", "screenshot: 'off'"}, nil},
		{"../../scripts/install-compose-linux.sh", []string{"MAILWISP_DOCKER_COMPOSE", "MAILWISP_DOCKER_COMPOSE_LINUX_X86_64_SHA256", "mktemp", "sha256sum --check --strict", "install -m 0755", "docker compose version --short"}, []string{"latest"}},
		{"../../scripts/install-buildx-linux.sh", []string{"MAILWISP_BUILDX", "MAILWISP_BUILDX_LINUX_AMD64_SHA256", "mktemp", "sha256sum --check --strict", "install -m 0755", "docker buildx version"}, []string{"latest"}},
		{"../../.github/workflows/benchmark.yml", []string{"ubuntu-24.04", "./scripts/install-compose-linux.sh", "./scripts/benchmark-compose.ps1", "-Concurrency 1,4,16,32", "if: always()", "actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a"}, []string{"@main", "@v"}},
		{"../../.github/workflows/release.yml", []string{"ubuntu-24.04", "install-buildx-linux.sh", "Run clean-checkout release verification gate", "Build first canonical release", "Build second canonical release", "cmp /tmp/mailwisp-first.tar.gz", "generate-release-sbom.ps1", "scan-release.ps1", "finalize-release.ps1", "verify-release.ps1", "-RunE2E", "Bind runtime verification evidence", "working-directory: artifacts/release/publish", "path: artifacts/release/publish", "sha256sum --check --strict SHA256SUMS", "Recalculate every downloaded release checksum", "actions/attest-build-provenance@0f67c3f4856b2e3261c31976d6725780e5e4c373", "subject-checksums: artifacts/release/publish/SHA256SUMS", "subject-path: artifacts/release/publish/SHA256SUMS", "-maxdepth 1", "subject_count", "MAILWISP_GITHUB_ATTESTATIONS_ENABLED", "Private releases require GitHub Enterprise Cloud Artifact Attestations", "gh release delete-asset", "gh release create", "--draft", "retention-days: 30"}, []string{"@main", "continue-on-error", "private-repository: true", "subject-checksums: artifacts/release/SHA256SUMS", "subject-path: artifacts/release/SHA256SUMS"}},
		{"Dockerfile", []string{"# syntax=docker/dockerfile:1.20.0@sha256:26147acbda4f14c5add9946e2fd2ed543fc402884fd75146bd342a7f6271dc1d", "golang:1.26.5-alpine3.24@sha256:", "node:22.20.0-alpine3.22@sha256:", "FROM scratch AS maintenance-packages", "postgresql18-client-18.4-r0.apk", "apk --no-network --repositories-file /dev/null add", "nginx:1.30.4-alpine@sha256:59d10bca5c674965ef4ff884715000dd60ef5567c36663523f108eec8e4105d4", "security-headers.conf", "USER 65532:65532", "npm@11.15.0", "npm ci", "mailwisp/internal/buildinfo.version", "org.opencontainers.image.version", "org.opencontainers.image.revision", "org.opencontainers.image.created", "-trimpath"}, []string{"# syntax=docker/dockerfile:1.20.0\n", "apk add", "latest"}},
		{"versions.lock", []string{"MAILWISP_NPM=11.15.0", "MAILWISP_DOCKERFILE_FRONTEND=docker/dockerfile:1.20.0@sha256:26147acbda4f14c5add9946e2fd2ed543fc402884fd75146bd342a7f6271dc1d", "MAILWISP_DOCKER_COMPOSE=5.2.0", "MAILWISP_DOCKER_COMPOSE_LINUX_X86_64_SHA256=018f9612ecabc5f2d7aaa53d6f5f44453a87611e2d72c8ef84d7b1eca070e719", "MAILWISP_BUILDX=0.35.0", "MAILWISP_BUILDX_LINUX_AMD64_SHA256=d41ece72044243b4f58b343441ae37446d9c29a7d6b5e11c61847bbcf8f7dfda", "MAILWISP_BUILDKIT_VERSION=0.31.2", "MAILWISP_BUILDKIT_IMAGE=moby/buildkit:v0.31.2@sha256:2f5adac4ecd194d9f8c10b7b5d7bceb5186853db1b26e5abd3a657af0b7e26ec", "MAILWISP_POSTGRES=postgres:18.4-alpine@sha256:", "MAILWISP_POSTGRESQL_CLIENT_PACKAGE=18.4-r0", "MAILWISP_PROMETHEUS_TOOL=prom/prometheus:v3.13.1@sha256:3c42b892cf723fa54d2f262c37a0e1f80aa8c8ddb1da7b9b0df9455a35a7f893", "MAILWISP_SYFT=1.48.0", "MAILWISP_TRIVY=0.72.0", "MAILWISP_POSTFIX_PACKAGE=3.11.5-r0"}, []string{"latest"}},
		{"../../.dockerignore", []string{".git", "web/node_modules", "deploy/compose/secrets"}, nil},
		{"postfix/Dockerfile", []string{"FROM scratch AS postfix-packages", "ADD --checksum=sha256:", "postfix-3.11.5-r0.apk", "ca-certificates-20260611-r0.apk", "alpine:3.24.1@sha256:", "apk --no-network --repositories-file /dev/null add", "org.opencontainers.image.version", "org.opencontainers.image.revision", "org.opencontainers.image.created", "--chmod=0755"}, []string{"apk add --no-cache", "latest"}},
		{"postfix/entrypoint.sh", []string{"reject_unauth_destination", "smtpd_tls_protocols = >=TLSv1.2", "postfix check", "relay_transport = lmtp:inet:app:2525", "alias_maps ="}, []string{"transport_maps = hash:", "postmap"}},
		{"nginx/default.conf.template", []string{"ssl_protocols TLSv1.2 TLSv1.3", "include /etc/nginx/snippets/mailwisp-security-headers.conf", "proxy_pass http://app:8080", "limit_req zone=mailwisp_create", "api|compat|open_api|user_api", "location = /metrics", "return 404"}, nil},
		{"nginx/security-headers.conf", []string{"Content-Security-Policy", "X-Content-Type-Options", "Cross-Origin-Opener-Policy", "always"}, nil},
		{"../../.github/workflows/verify.yml", []string{"./scripts/install-compose-linux.sh", "if: always()", "production-e2e-${{ github.run_id }}-${{ github.run_attempt }}", "artifacts/production-e2e/result.json", "disaster-recovery-${{ github.run_id }}-${{ github.run_attempt }}", "artifacts/disaster-recovery/result.json", "web/playwright-production-report", "web/test-results-production"}, nil},
		{"../../scripts/build-release.ps1", []string{"AllowDirty", "SOURCE_DATE_EPOCH", "MAILWISP_BUILDX", "MAILWISP_BUILDKIT_IMAGE", "isolated release builder", "--no-cache", "mailwisp/internal/buildinfo.version", "--provenance=false", "docker save", "release.json", "SHA256SUMS", "OPERATIONS.md", "prometheus-alerts.example.yml", "release.compose.yaml", "backup-verifier.compose.yaml", "backup-verifier.release.compose.yaml", "COMPOSE_FILE=compose.yaml:release.compose.yaml", "--sort=name", "gzip -n"}, nil},
		{"../../scripts/generate-release-sbom.ps1", []string{"MAILWISP_SYFT", "SPDX-2.3", "Git worktree status", "git -C $repositoryRoot archive", "committed source snapshot", "source SPDX SBOM generation", "binary SPDX SBOM generation", "image SPDX SBOM generation", "sbom-index.json"}, []string{"scan \"dir:$repositoryRoot\""}},
		{"../../scripts/scan-release.ps1", []string{"MAILWISP_TRIVY", "--download-db-only", "max_database_age_hours = 48", "image vulnerability scan", "Metadata.ImageID", "release IaC misconfiguration scan", "--ignorefile", ".trivyignore.yaml", "accepted_risks", "ignore_unfixed = $false", "allowed_findings = 0", "security-index.json"}, []string{"--ignore-unfixed"}},
		{"../../.trivyignore.yaml", []string{"misconfigurations:", "id: AVD-DS-0002", "deploy/compose/postfix/Dockerfile", "expired_at: 2027-01-17", "Postfix master must start as root", "Re-evaluate before the expiry date"}, []string{"vulnerabilities:", "deploy/compose/Dockerfile"}},
		{"../../scripts/finalize-release.ps1", []string{"release-evidence.json", "publish", "HashSet[string]", "OrdinalIgnoreCase", "Release publish asset name is empty, reserved or duplicated", "SHA256SUMS", "blocking_findings", "git_dirty", "release-verification.json", "production-e2e.json", "source_builds_remaining", "verification = $verificationEvidence"}, nil},
		{"../../scripts/release-artifacts.ps1", []string{"Assert-MailWispArtifactPath", "Artifact path escaped", "symbolic link or junction", "Remove-MailWispArtifactDirectory"}, nil},
		{"../../scripts/verify-release.ps1", []string{"Assert-BuildOutput", "Assert-ChecksumManifest", "publish", "-Root $publishRoot -Manifest $outerChecksums -RequireComplete", "archive path validation", "archive metadata listing", "duplicate entry", "symbolic link or reparse point", "load release image archive", "did not disable remote pulls", "Release Compose retained a source build", "prebuilt release production E2E", "source_builds_remaining = 0"}, nil},
		{"../../docs/release-bundle.md", []string{"Canonical Docker Compose部署", "docker load", "--no-build", "Artifact Attestation", "Fail Closed", "SPDX 2.3", "回滚"}, nil},
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

func TestWorkflowActionsUseImmutableCommitSHA(t *testing.T) {
	paths, err := filepath.Glob("../../.github/workflows/*.yml")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no GitHub Actions workflows found")
	}
	usesLine := regexp.MustCompile(`^\s*-?\s*uses:\s*([^\s#]+)(?:\s+#.*)?$`)
	immutable := regexp.MustCompile(`^[^@]+@[0-9a-f]{40}$`)
	for _, path := range paths {
		content := readComposeFile(t, path)
		for lineNumber, line := range strings.Split(content, "\n") {
			matches := usesLine.FindStringSubmatch(line)
			if len(matches) == 0 {
				continue
			}
			if !immutable.MatchString(matches[1]) {
				t.Errorf("%s:%d action is not pinned to a full commit SHA: %s", path, lineNumber+1, matches[1])
			}
		}
	}
}

func TestReleaseComposeDisablesEverySourceBuild(t *testing.T) {
	tests := []struct {
		name        string
		basePath    string
		overlayPath string
		expected    []string
	}{
		{
			name:        "canonical",
			basePath:    "compose.yaml",
			overlayPath: "release.compose.yaml",
			expected:    []string{"app", "edge", "maintenance", "migrate", "postfix"},
		},
		{
			name:        "backup-verifier",
			basePath:    "backup-verifier.compose.yaml",
			overlayPath: "backup-verifier.release.compose.yaml",
			expected:    []string{"backup-verifier"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base := readComposeFile(t, test.basePath)
			overlay := readComposeFile(t, test.overlayPath)

			baseBuilds := composeServicesWithField(t, base, "build:", false)
			if !slices.Equal(baseBuilds, test.expected) {
				t.Fatalf("%s source build services = %v, want %v", test.basePath, baseBuilds, test.expected)
			}
			if strings.Contains(base, "build: !reset") {
				t.Fatalf("%s must retain local source builds", test.basePath)
			}

			resets := composeServicesWithField(t, overlay, "build: !reset null", true)
			if !slices.Equal(resets, baseBuilds) {
				t.Fatalf("%s resets %v, want every source build %v", test.overlayPath, resets, baseBuilds)
			}
			for service, fields := range composeServiceFields(t, overlay) {
				if !slices.Equal(fields, []string{"build: !reset null", "pull_policy: never"}) {
					t.Fatalf("%s service %s fields = %v, want build reset and fail-closed pull policy", test.overlayPath, service, fields)
				}
			}
			if strings.Contains(strings.ToLower(overlay), "latest") {
				t.Fatalf("%s must not contain a floating latest image", test.overlayPath)
			}
		})
	}
}

func TestPostfixAPKInputsAreImmutable(t *testing.T) {
	assertLockedAPKInputs(t, "postfix/Dockerfile", "Postfix", []string{
		"ca-certificates-20260611-r0.apk",
		"gdbm-1.26-r0.apk",
		"icu-data-en-78.1-r0.apk",
		"icu-libs-78.1-r0.apk",
		"libgcc-15.2.0-r5.apk",
		"libsasl-2.1.28-r9.apk",
		"libstdc++-15.2.0-r5.apk",
		"lmdb-0.9.35-r0.apk",
		"postfix-3.11.5-r0.apk",
	})
}

func TestMaintenanceAPKInputsAreImmutable(t *testing.T) {
	assertLockedAPKInputs(t, "Dockerfile", "Maintenance", []string{
		"libncursesw-6.6_p20260516-r0.apk",
		"libpq-18.4-r0.apk",
		"lz4-libs-1.10.0-r1.apk",
		"ncurses-terminfo-base-6.6_p20260516-r0.apk",
		"postgresql-common-1.3-r0.apk",
		"postgresql18-client-18.4-r0.apk",
		"readline-8.3.3-r1.apk",
		"zstd-libs-1.5.7-r2.apk",
	})
}

func assertLockedAPKInputs(t *testing.T, file, subject string, want []string) {
	t.Helper()
	content := readComposeFile(t, file)
	var got []string
	checksums := make(map[string]struct{})
	for _, line := range strings.Split(content, "\n") {
		if !strings.HasPrefix(line, "ADD --checksum=sha256:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 4 || !regexp.MustCompile(`^--checksum=sha256:[a-f0-9]{64}$`).MatchString(fields[1]) {
			t.Fatalf("invalid immutable APK ADD: %s", line)
		}
		const repository = "https://dl-cdn.alpinelinux.org/alpine/v3.24/main/x86_64/"
		if !strings.HasPrefix(fields[2], repository) {
			t.Fatalf("APK source is not the locked Alpine 3.24 repository: %s", fields[2])
		}
		name := strings.TrimPrefix(fields[2], repository)
		if fields[3] != "/"+name {
			t.Fatalf("APK destination %s does not preserve source name %s", fields[3], name)
		}
		if _, exists := checksums[fields[1]]; exists {
			t.Fatalf("duplicate APK checksum: %s", fields[1])
		}
		checksums[fields[1]] = struct{}{}
		got = append(got, name)
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("locked %s APKs = %v, want %v", subject, got, want)
	}
	if !strings.Contains(content, "apk --no-network --repositories-file /dev/null add /tmp/mailwisp-apks/*.apk") {
		t.Fatalf("%s must install only the checksummed local APK set", subject)
	}
}

func readComposeFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(content), "\r\n", "\n")
}

func composeServicesWithField(t *testing.T, content, field string, exact bool) []string {
	t.Helper()
	fields := composeServiceFields(t, content)
	services := make([]string, 0, len(fields))
	for service, serviceFields := range fields {
		for _, candidate := range serviceFields {
			matches := strings.HasPrefix(candidate, field)
			if exact {
				matches = candidate == field
			}
			if matches {
				services = append(services, service)
				break
			}
		}
	}
	slices.Sort(services)
	return services
}

func composeServiceFields(t *testing.T, content string) map[string][]string {
	t.Helper()
	services := make(map[string][]string)
	inServices := false
	currentService := ""
	for _, line := range strings.Split(content, "\n") {
		if line == "services:" {
			inServices = true
			continue
		}
		if !inServices {
			continue
		}
		if line != "" && line[0] != ' ' {
			break
		}
		if strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "    ") && strings.HasSuffix(line, ":") {
			currentService = strings.TrimSuffix(strings.TrimSpace(line), ":")
			services[currentService] = nil
			continue
		}
		if currentService != "" && strings.HasPrefix(line, "    ") && !strings.HasPrefix(line, "      ") {
			services[currentService] = append(services[currentService], strings.TrimSpace(line))
		}
	}
	if len(services) == 0 {
		t.Fatal("compose file has no services")
	}
	return services
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
