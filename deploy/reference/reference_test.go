package reference

import (
	"os"
	"strings"
	"testing"
)

func TestReferenceProfileSecurityContract(t *testing.T) {
	tests := []struct {
		path     string
		required []string
	}{
		{"versions.lock", []string{"MAILWISP_GO=1.26.5", "MAILWISP_POSTGRESQL=18.4", "MAILWISP_POSTFIX=3.11.5", "MAILWISP_NGINX=1.30.4", "MAILWISP_CERTBOT=5.6.0"}},
		{"README.md", []string{"MAILWISP_CREATE_QUOTA_HMAC_KEY_FILE=/etc/mailwisp/create-quota-hmac-key", "MAILWISP_HEAVY_READ_CONCURRENCY=4", "root:mailwisp 0640", "MAILWISP_BROWSER_SESSION_KEY=<base64-encoded-32-byte-secret>", "openssl rand -base64 32", "Secure `__Host-` Cookie"}},
		{"systemd/mailwisp.service", []string{"NoNewPrivileges=true", "ProtectSystem=strict", "MemoryDenyWriteExecute=true", "ReadWritePaths=/var/lib/mailwisp", "Restart=on-failure"}},
		{"systemd/mailwisp-cleanup.timer", []string{"OnUnitActiveSec=5min", "RandomizedDelaySec=30s", "Persistent=true"}},
		{"nginx/mailwisp.conf.example", []string{"ssl_protocols TLSv1.2 TLSv1.3", "include /etc/nginx/snippets/mailwisp-security-headers.conf", "proxy_pass http://127.0.0.1:8080", "limit_req zone=mailwisp_create", "api|compat|open_api|user_api", "location = /metrics", "return 404"}},
		{"nginx/mailwisp-security-headers.conf.example", []string{"Content-Security-Policy", "Strict-Transport-Security", "X-Content-Type-Options", "Cross-Origin-Opener-Policy"}},
		{"postfix/main.cf.example", []string{"relay_domains = MAILWISP_PUBLIC_DOMAINS", "relay_transport = lmtp:inet:127.0.0.1:2525", "reject_unauth_destination, reject_unverified_recipient", "address_verify_relay_transport = lmtp:inet:127.0.0.1:2525", "address_verify_map = lmdb:$data_directory/verify_cache", "address_verify_positive_expire_time = 2s", "address_verify_negative_expire_time = 2s", "address_verify_poll_delay = 1s", "address_verify_poll_count = 3", "unverified_recipient_reject_code = 550", "smtpd_tls_protocols = >=TLSv1.2", "message_size_limit = 26214400"}},
		{"certbot/reload-mail-services.sh", []string{"RENEWED_LINEAGE", "nginx -t", "postfix check", "systemctl reload nginx", "systemctl reload postfix"}},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			content, err := os.ReadFile(test.path)
			if err != nil {
				t.Fatalf("read %s: %v", test.path, err)
			}
			text := string(content)
			if strings.Contains(strings.ToLower(text), "latest") {
				t.Fatalf("%s contains a floating latest version", test.path)
			}
			for _, required := range test.required {
				if !strings.Contains(text, required) {
					t.Errorf("%s missing %q", test.path, required)
				}
			}
			if test.path == "nginx/mailwisp.conf.example" && strings.Count(text, "include /etc/nginx/snippets/mailwisp-security-headers.conf;") != 3 {
				t.Error("Nginx server, asset, and SPA locations must all include the shared security headers")
			}
			if test.path == "postfix/main.cf.example" && strings.Contains(text, "transport_maps") {
				t.Error("Host-native Postfix must use the canonical relay transport without a per-domain map")
			}
		})
	}
}
