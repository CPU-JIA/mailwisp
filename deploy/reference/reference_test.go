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
		{"README.md", []string{"MAILWISP_CREATE_QUOTA_HMAC_KEY_FILE=/etc/mailwisp/create-quota-hmac-key", "root:mailwisp 0640", "MAILWISP_BROWSER_SESSION_KEY=<base64-encoded-32-byte-secret>", "openssl rand -base64 32", "Secure `__Host-` Cookie"}},
		{"systemd/mailwisp.service", []string{"NoNewPrivileges=true", "ProtectSystem=strict", "MemoryDenyWriteExecute=true", "ReadWritePaths=/var/lib/mailwisp", "Restart=on-failure"}},
		{"systemd/mailwisp-cleanup.timer", []string{"OnUnitActiveSec=5min", "RandomizedDelaySec=30s", "Persistent=true"}},
		{"nginx/mailwisp.conf.example", []string{"ssl_protocols TLSv1.2 TLSv1.3", "Content-Security-Policy", "proxy_pass http://127.0.0.1:8080", "limit_req zone=mailwisp_create", "api|compat|open_api|user_api", "location = /metrics", "return 404"}},
		{"postfix/main.cf.example", []string{"transport_maps = hash:/etc/postfix/mailwisp_transport", "reject_unauth_destination", "smtpd_tls_protocols = >=TLSv1.2", "message_size_limit = 26214400"}},
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
		})
	}
}
