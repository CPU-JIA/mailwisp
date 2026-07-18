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
	"reflect"
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
		SourceCommit             string   `json:"source_commit"`
		SourceVersion            string   `json:"source_version"`
		Namespace                string   `json:"mailwisp_namespace"`
		AuthenticationProjection string   `json:"authentication_projection"`
		LegacyPathsDefault       bool     `json:"legacy_paths_default"`
		Endpoints                []string `json:"endpoints"`
		SourceFiles              []struct {
			Path   string `json:"path"`
			SHA256 string `json:"sha256"`
		} `json:"source_files"`
		Limits struct {
			PageItems            int `json:"page_items"`
			ResponsePayloadBytes int `json:"response_payload_bytes"`
		} `json:"limits"`
	}
	expectedEndpoints := []string{
		"GET /open_api/settings", "GET /user_api/open_settings", "POST /api/new_address", "GET /api/settings", "GET /api/mails",
		"GET /api/mail/{id}", "GET /api/parsed_mails", "GET /api/parsed_mail/{id}", "DELETE /api/mails/{id}", "DELETE /api/delete_address",
	}
	if err := json.Unmarshal(fixtureBytes, &fixture); err != nil ||
		fixture.SourceCommit != "99b332345bcf3beff77ed70feaec9d5e10de3590" || fixture.SourceVersion != "v1.10.0" ||
		fixture.Namespace != "/compat/cloudflare-temp" || fixture.AuthenticationProjection != "canonical_opaque_capability_in_jwt_field" ||
		fixture.LegacyPathsDefault || !reflect.DeepEqual(fixture.Endpoints, expectedEndpoints) || len(fixture.SourceFiles) != 10 ||
		fixture.Limits.PageItems != 20 || fixture.Limits.ResponsePayloadBytes != 32<<20 {
		t.Fatalf("Cloudflare Temp Email fixture = %+v, error = %v", fixture, err)
	}
	pinnedSHA256 := func(prefix, suffix string) string { return prefix + suffix }
	expectedSources := map[string]string{
		"worker/src/worker.ts":                    pinnedSHA256("385dcdf7ea4e1da130726220c06a9952", "691f480744972f2a50c5a8e371461ee9"),
		"worker/src/commom_api.ts":                pinnedSHA256("c14af6952167925139ad0a7cd41f2027", "16c3fa7af158cb4482a6c771b6895e69"),
		"worker/src/mails_api/index.ts":           pinnedSHA256("31ef29fdd1fcc712f9b517512edb3dc8", "2f6f7062c4ecb5ed4f25e88041b17531"),
		"worker/src/mails_api/new_address.ts":     pinnedSHA256("806576eb038fd01039b205444ca98a2d", "a5a41e4076b03881cb398a1f741306af"),
		"worker/src/mails_api/mails_crud.ts":      pinnedSHA256("e92b446e6f19e198fc9cbceded702d9a", "657053c8747a5604c972cedd8121f8d0"),
		"worker/src/mails_api/parsed_mail_api.ts": pinnedSHA256("7c7af300f298bbcf82b8d49aacea9e36", "11de289d961e310f69fa45f382cbcd38"),
		"worker/src/user_api/index.ts":            pinnedSHA256("eb551b0174ec9bb654e5d81427fb7b23", "bc2664de73a94d904d680dd30a5a6d9b"),
		"worker/src/user_api/settings.ts":         pinnedSHA256("669b5aaf43e74ac48f8afb173005cde1", "b506b9ccc8552e4439c6a8f0a650fcda"),
		"frontend/src/api/index.js":               pinnedSHA256("bf834cb06a33b0b8978b85ba003ce305", "7b43e51d8fa0d64b9bd2099b0726c0f7"),
		"frontend/src/views/Index.vue":            pinnedSHA256("e6f8fd10a144ae17b11d80449224218b", "7f88afda806451afd34b2f162fda858a"),
	}
	for _, source := range fixture.SourceFiles {
		if expectedSources[source.Path] != source.SHA256 {
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
func (*cloudflareTempMailboxStub) ListMessages(context.Context, message.InboxID, mailbox.CursorPage) (mailbox.CursorMessagePage, error) {
	return mailbox.CursorMessagePage{Items: []mailbox.MessageSummary{}}, nil
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
func (*cloudflareTempMailboxStub) MarkMessageSeen(context.Context, message.InboxID, message.MessageID) error {
	return nil
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
