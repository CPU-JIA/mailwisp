package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"mailwisp/internal/abuse"
	"mailwisp/internal/auth"
	"mailwisp/internal/cloudflaretemp"
	"mailwisp/internal/config"
	"mailwisp/internal/mail"
	"mailwisp/internal/mailbox"
	"mailwisp/internal/message"
)

func TestCloudflareTempContractAddressAndRawMailWorkflow(t *testing.T) {
	fixtureBytes, err := os.ReadFile("testdata/cloudflare-temp-contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		SourceCommit string   `json:"source_commit"`
		Namespace    string   `json:"mailwisp_namespace"`
		Endpoints    []string `json:"endpoints"`
		SourceFiles  []struct {
			Path   string `json:"path"`
			SHA256 string `json:"sha256"`
		} `json:"source_files"`
	}
	if err := json.Unmarshal(fixtureBytes, &fixture); err != nil || fixture.SourceCommit != "99b332345bcf3beff77ed70feaec9d5e10de3590" || fixture.Namespace != "/compat/cloudflare-temp" || len(fixture.Endpoints) != 10 || len(fixture.SourceFiles) != 10 {
		t.Fatalf("Cloudflare Temp Email fixture = %+v, error = %v", fixture, err)
	}
	for _, source := range fixture.SourceFiles {
		if source.Path == "" || len(source.SHA256) != 64 {
			t.Fatalf("Cloudflare Temp Email source fixture = %+v", source)
		}
	}
	server := newCloudflareTempTestServer(t, false)

	settings := performCloudflareTempRequest(server, http.MethodGet, "/compat/cloudflare-temp/open_api/settings", "", "")
	if settings.Code != http.StatusOK || !strings.Contains(settings.Body.String(), `"domains":["mailwisp.test"]`) || !strings.Contains(settings.Body.String(), `"enableAddressPassword":false`) {
		t.Fatalf("settings = %d %s", settings.Code, settings.Body.String())
	}
	userSettings := performCloudflareTempRequest(server, http.MethodGet, "/compat/cloudflare-temp/user_api/open_settings", "", "")
	if userSettings.Code != http.StatusOK || !strings.Contains(userSettings.Body.String(), `"enable":false`) || !strings.Contains(userSettings.Body.String(), `"oauth2ClientIDs":[]`) {
		t.Fatalf("user settings = %d %s", userSettings.Code, userSettings.Body.String())
	}
	created := performCloudflareTempRequest(server, http.MethodPost, "/compat/cloudflare-temp/api/new_address", `{"name":"Demo.Name","domain":"mailwisp.test"}`, "")
	if created.Code != http.StatusOK || !strings.Contains(created.Body.String(), `"address":"demoname@mailwisp.test"`) || !strings.Contains(created.Body.String(), `"address_id":42`) || !strings.Contains(created.Body.String(), `"jwt":"created-capability"`) {
		t.Fatalf("created = %d %s", created.Code, created.Body.String())
	}
	quota := server.createQuota.(*createQuotaStub)
	quota.err = abuse.ErrDailyCreateQuotaExceeded
	limited := performCloudflareTempRequest(server, http.MethodPost, "/compat/cloudflare-temp/api/new_address", `{"name":"limited","domain":"mailwisp.test"}`, "")
	if limited.Code != http.StatusTooManyRequests || limited.Body.String() != "Daily address quota exceeded" {
		t.Fatalf("daily quota = %d %s", limited.Code, limited.Body.String())
	}
	quota.err = nil
	listed := performCloudflareTempRequest(server, http.MethodGet, "/compat/cloudflare-temp/api/mails?limit=10&offset=0", "", "Bearer current-capability")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"id":101`) || !strings.Contains(listed.Body.String(), `"raw":"raw-message"`) || !strings.Contains(listed.Body.String(), `"count":1`) {
		t.Fatalf("listed = %d %s", listed.Code, listed.Body.String())
	}
	parsed := performCloudflareTempRequest(server, http.MethodGet, "/compat/cloudflare-temp/api/parsed_mail/101", "", "Bearer current-capability")
	if parsed.Code != http.StatusOK || !strings.Contains(parsed.Body.String(), `"sender":"Sender \u003csender@example.com\u003e"`) || !strings.Contains(parsed.Body.String(), `"subject":"Code"`) {
		t.Fatalf("parsed = %d %s", parsed.Code, parsed.Body.String())
	}
	missing := performCloudflareTempRequest(server, http.MethodGet, "/compat/cloudflare-temp/api/mail/999", "", "Bearer current-capability")
	if missing.Code != http.StatusOK || strings.TrimSpace(missing.Body.String()) != "null" {
		t.Fatalf("missing = %d %s", missing.Code, missing.Body.String())
	}
	unauthorized := performCloudflareTempRequest(server, http.MethodGet, "/compat/cloudflare-temp/api/settings", "", "")
	if unauthorized.Code != http.StatusUnauthorized || unauthorized.Body.String() != "Invalid address credential" {
		t.Fatalf("unauthorized = %d %s", unauthorized.Code, unauthorized.Body.String())
	}
}

func TestCloudflareTempLegacyPathsRequireExplicitEnablement(t *testing.T) {
	disabled := newCloudflareTempTestServer(t, false)
	response := performCloudflareTempRequest(disabled, http.MethodGet, "/open_api/settings", "", "")
	if response.Code != http.StatusNotFound {
		t.Fatalf("legacy disabled status = %d", response.Code)
	}

	enabled := newCloudflareTempTestServer(t, true)
	response = performCloudflareTempRequest(enabled, http.MethodGet, "/open_api/settings", "", "")
	if response.Code != http.StatusOK {
		t.Fatalf("legacy enabled = %d %s", response.Code, response.Body.String())
	}
	invalidPage := performCloudflareTempRequest(enabled, http.MethodGet, "/api/mails?limit=100&offset=0", "", "Bearer current-capability")
	if invalidPage.Code != http.StatusBadRequest || invalidPage.Body.String() != "Invalid limit" {
		t.Fatalf("invalid page = %d %s", invalidPage.Code, invalidPage.Body.String())
	}
}

func TestCloudflareTempLimitedWriterRejectsResponseExpansion(t *testing.T) {
	var destination bytes.Buffer
	limited := &cloudflareTempLimitedWriter{destination: &destination, remaining: 4}
	if _, err := limited.Write([]byte("12345")); !errors.Is(err, errCloudflareTempPayloadTooLarge) {
		t.Fatalf("Write() error = %v", err)
	}
	if destination.Len() != 0 {
		t.Fatalf("destination length = %d", destination.Len())
	}
}

func TestCloudflareTempHeavyReadFailsFastWhenSaturated(t *testing.T) {
	server := newCloudflareTempTestServer(t, false)
	server.cloudflareTempHeavy <- struct{}{}
	server.cloudflareTempHeavy <- struct{}{}
	t.Cleanup(func() {
		<-server.cloudflareTempHeavy
		<-server.cloudflareTempHeavy
	})
	response := performCloudflareTempRequest(server, http.MethodGet, "/compat/cloudflare-temp/api/mails?limit=10&offset=0", "", "Bearer current-capability")
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") != "1" || response.Body.String() != "Cloudflare Temp Email compatibility worker is busy" {
		t.Fatalf("saturated response = %d %s", response.Code, response.Body.String())
	}
}

func newCloudflareTempTestServer(t *testing.T, legacyPaths bool) *Server {
	t.Helper()
	mailboxes := &cloudflareTempMailboxStub{}
	ids := &cloudflareTempIDStub{}
	service, err := cloudflaretemp.NewService(mailboxes, ids, []string{"mailwisp.test"})
	if err != nil {
		t.Fatal(err)
	}
	credentials := &cloudflareTempCredentialStub{}
	server := NewServer(config.HTTP{CreateRatePerMinute: 60, CreateRateBurst: 10}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.SetMailboxService(mailboxes, credentials)
	server.SetCreateQuota(&createQuotaStub{})
	server.SetCloudflareTempService(service, legacyPaths)
	return server
}

func performCloudflareTempRequest(server *Server, method, path, body, authorization string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	recorder := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(recorder, request)
	return recorder
}

type cloudflareTempMailboxStub struct{}

func (*cloudflareTempMailboxStub) Create(_ context.Context, request mailbox.CreateRequest) (mailbox.CreatedInbox, error) {
	now := time.Date(2026, 7, 15, 1, 0, 0, 0, time.UTC)
	return mailbox.CreatedInbox{
		Inbox:      mailbox.Inbox{ID: cloudflareTempInboxID, Address: request.LocalPart + "@mailwisp.test", Status: "active", CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour)},
		Capability: auth.IssuedCapability{InboxID: cloudflareTempInboxID, Plaintext: "created-capability"},
	}, nil
}
func (*cloudflareTempMailboxStub) Get(context.Context, message.InboxID) (mailbox.Inbox, error) {
	now := time.Date(2026, 7, 15, 1, 0, 0, 0, time.UTC)
	return mailbox.Inbox{ID: cloudflareTempInboxID, Address: "demoname@mailwisp.test", Status: "active", CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour)}, nil
}
func (*cloudflareTempMailboxStub) Delete(context.Context, message.InboxID) error { return nil }
func (*cloudflareTempMailboxStub) ListMessages(context.Context, message.InboxID, int) ([]mailbox.MessageSummary, error) {
	return []mailbox.MessageSummary{}, nil
}
func (*cloudflareTempMailboxStub) ListMessagePage(context.Context, message.InboxID, int, int) (mailbox.MessagePage, error) {
	return mailbox.MessagePage{Items: []mailbox.MessageSummary{{ID: cloudflareTempMessageID, EnvelopeSender: "sender@example.com", Subject: "Code", ReceivedAt: time.Date(2026, 7, 15, 2, 0, 0, 0, time.UTC), SizeBytes: 11}}, Total: 1, Unread: 1}, nil
}
func (*cloudflareTempMailboxStub) GetMessage(context.Context, message.InboxID, message.MessageID) (mailbox.MessageDetail, error) {
	return mailbox.MessageDetail{
		MessageSummary:  mailbox.MessageSummary{ID: cloudflareTempMessageID, EnvelopeSender: "sender@example.com", Subject: "Code", ReceivedAt: time.Date(2026, 7, 15, 2, 0, 0, 0, time.UTC), SizeBytes: 11},
		HeaderMessageID: "<message@example.com>", From: []mail.Address{{Name: "Sender", Address: "sender@example.com"}}, Text: "123456", HTMLSource: "<p>123456</p>",
	}, nil
}
func (*cloudflareTempMailboxStub) OpenSource(context.Context, message.InboxID, message.MessageID) (mailbox.RawSource, error) {
	return mailbox.RawSource{Reader: io.NopCloser(strings.NewReader("raw-message")), Size: 11}, nil
}
func (*cloudflareTempMailboxStub) OpenAttachment(context.Context, message.InboxID, message.MessageID, string) (mailbox.AttachmentSource, error) {
	return mailbox.AttachmentSource{}, mailbox.ErrMessageNotFound
}
func (*cloudflareTempMailboxStub) DeleteMessage(context.Context, message.InboxID, message.MessageID) error {
	return nil
}

type cloudflareTempIDStub struct{}

func (*cloudflareTempIDStub) EnsureInboxID(context.Context, message.InboxID) (int64, error) {
	return 42, nil
}
func (*cloudflareTempIDStub) EnsureMessageIDs(_ context.Context, _ message.InboxID, ids []message.MessageID) (map[message.MessageID]int64, error) {
	result := make(map[message.MessageID]int64, len(ids))
	for _, id := range ids {
		result[id] = 101
	}
	return result, nil
}
func (*cloudflareTempIDStub) FindMessageID(_ context.Context, _ message.InboxID, compatibilityID int64) (message.MessageID, error) {
	if compatibilityID != 101 {
		return "", cloudflaretemp.ErrMessageIDNotFound
	}
	return cloudflareTempMessageID, nil
}

type cloudflareTempCredentialStub struct{}

func (*cloudflareTempCredentialStub) Authenticate(context.Context, string, ...auth.Scope) (auth.Principal, error) {
	scopes, _ := auth.NewScopeSet(auth.ScopeInboxRead, auth.ScopeInboxDelete, auth.ScopeMessageRead, auth.ScopeMessageDelete)
	return auth.Principal{InboxID: cloudflareTempInboxID, Scopes: scopes, ExpiresAt: time.Now().Add(time.Hour)}, nil
}

const (
	cloudflareTempInboxID   = message.InboxID("018f26e5-8f04-7b44-8ba2-4a8f434dcb22")
	cloudflareTempMessageID = message.MessageID("018f26e5-8f04-7b44-8ba2-4a8f434dcb23")
)
