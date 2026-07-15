package httpapi

import (
	"context"
	"encoding/json"
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
	"mailwisp/internal/config"
	"mailwisp/internal/mailbox"
	"mailwisp/internal/message"
	"mailwisp/internal/yyds"
)

func TestYYDSContractTemporaryInboxAndMessages(t *testing.T) {
	fixtureBytes, err := os.ReadFile("testdata/yyds-contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		SourceSHA string   `json:"source_sha256"`
		Namespace string   `json:"mailwisp_namespace"`
		Endpoints []string `json:"endpoints"`
	}
	if err := json.Unmarshal(fixtureBytes, &fixture); err != nil || fixture.SourceSHA == "" || fixture.Namespace != "/compat/yyds/v1" || len(fixture.Endpoints) != 12 {
		t.Fatalf("YYDS fixture = %+v, error = %v", fixture, err)
	}
	mailboxes := &yydsHTTPMailboxStub{}
	credentials := &yydsCredentialStub{}
	service, err := yyds.NewService(mailboxes, credentials, []string{"mailwisp.test"})
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(config.HTTP{CreateRatePerMinute: 60, CreateRateBurst: 10}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	quota := &createQuotaStub{}
	server.SetMailboxService(mailboxes, credentials)
	server.SetCreateQuota(quota)
	server.SetYYDSService(service)

	domains := performYYDSRequest(server, http.MethodGet, "/compat/yyds/v1/domains", "", "")
	if domains.Code != http.StatusOK || !strings.Contains(domains.Body.String(), `"success":true`) || !strings.Contains(domains.Body.String(), `"domain":"mailwisp.test"`) {
		t.Fatalf("domains = %d %s", domains.Code, domains.Body.String())
	}
	account := performYYDSRequest(server, http.MethodPost, "/compat/yyds/v1/accounts", `{"localPart":"demo","domain":"mailwisp.test"}`, "")
	if account.Code != http.StatusCreated || !strings.Contains(account.Body.String(), `"token":"created-token"`) {
		t.Fatalf("account = %d %s", account.Code, account.Body.String())
	}
	legacy := performYYDSRequest(server, http.MethodPost, "/compat/yyds/v1/inboxes", `{"address":"legacy","domain":"mailwisp.test"}`, "")
	if legacy.Code != http.StatusCreated || !strings.Contains(legacy.Body.String(), `"address":"legacy@mailwisp.test"`) {
		t.Fatalf("legacy account = %d %s", legacy.Code, legacy.Body.String())
	}
	quota.err = abuse.ErrDailyCreateQuotaExceeded
	limited := performYYDSRequest(server, http.MethodPost, "/compat/yyds/v1/accounts", `{"localPart":"limited","domain":"mailwisp.test"}`, "")
	if limited.Code != http.StatusTooManyRequests || !strings.Contains(limited.Body.String(), `"errorCode":"daily_quota_exceeded"`) {
		t.Fatalf("daily quota = %d %s", limited.Code, limited.Body.String())
	}
	quota.err = nil
	wildcard := performYYDSRequest(server, http.MethodPost, "/compat/yyds/v1/accounts", `{"subdomainLabel":"child"}`, "")
	if wildcard.Code != http.StatusBadRequest || !strings.Contains(wildcard.Body.String(), `"errorCode":"wildcard_rule_unavailable"`) {
		t.Fatalf("wildcard account = %d %s", wildcard.Code, wildcard.Body.String())
	}
	messages := performYYDSRequest(server, http.MethodGet, "/compat/yyds/v1/messages?limit=50", "", "Bearer current-token")
	if messages.Code != http.StatusOK || !strings.Contains(messages.Body.String(), `"total":12`) || !strings.Contains(messages.Body.String(), `"unreadCount":7`) {
		t.Fatalf("messages = %d %s", messages.Code, messages.Body.String())
	}
	rotated := performYYDSRequest(server, http.MethodPost, "/compat/yyds/v1/token", `{"address":"demo@mailwisp.test","password":"ignored"}`, "Bearer current-token")
	if rotated.Code != http.StatusOK || !strings.Contains(rotated.Body.String(), `"token":"rotated-token"`) {
		t.Fatalf("token = %d %s", rotated.Code, rotated.Body.String())
	}
}

func TestYYDSDisabledAndAuthenticationUseStableEnvelope(t *testing.T) {
	server := NewServer(config.HTTP{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	disabled := performYYDSRequest(server, http.MethodGet, "/compat/yyds/v1/domains", "", "")
	if disabled.Code != http.StatusNotFound || !strings.Contains(disabled.Body.String(), `"success":false`) || !strings.Contains(disabled.Body.String(), `"errorCode":"request_failed"`) {
		t.Fatalf("disabled = %d %s", disabled.Code, disabled.Body.String())
	}
}

func performYYDSRequest(server *Server, method, path, body, authorization string) *httptest.ResponseRecorder {
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

type yydsHTTPMailboxStub struct{}

func (*yydsHTTPMailboxStub) Create(_ context.Context, request mailbox.CreateRequest) (mailbox.CreatedInbox, error) {
	now := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	return mailbox.CreatedInbox{Inbox: mailbox.Inbox{ID: yydsInboxID, Address: request.LocalPart + "@" + request.Domain, Status: "active", CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour)}, Capability: auth.IssuedCapability{Plaintext: "created-token"}}, nil
}
func (*yydsHTTPMailboxStub) Get(context.Context, message.InboxID) (mailbox.Inbox, error) {
	now := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	return mailbox.Inbox{ID: yydsInboxID, Address: "demo@mailwisp.test", Status: "active", CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour)}, nil
}
func (*yydsHTTPMailboxStub) Delete(context.Context, message.InboxID) error { return nil }
func (*yydsHTTPMailboxStub) ListMessages(context.Context, message.InboxID, int) ([]mailbox.MessageSummary, error) {
	return []mailbox.MessageSummary{}, nil
}
func (*yydsHTTPMailboxStub) ListMessagePage(context.Context, message.InboxID, int, int) (mailbox.MessagePage, error) {
	return mailbox.MessagePage{Items: []mailbox.MessageSummary{{ID: yydsMessageID, EnvelopeSender: "sender@example.com", Subject: "Code", ReceivedAt: time.Date(2026, 7, 15, 1, 0, 0, 0, time.UTC), SizeBytes: 100}}, Total: 12, Unread: 7}, nil
}
func (*yydsHTTPMailboxStub) GetMessage(context.Context, message.InboxID, message.MessageID) (mailbox.MessageDetail, error) {
	return mailbox.MessageDetail{MessageSummary: mailbox.MessageSummary{ID: yydsMessageID, Subject: "Code"}, Text: "123456"}, nil
}
func (*yydsHTTPMailboxStub) DeleteMessage(context.Context, message.InboxID, message.MessageID) error {
	return nil
}
func (*yydsHTTPMailboxStub) MarkMessageSeen(context.Context, message.InboxID, message.MessageID) error {
	return nil
}
func (*yydsHTTPMailboxStub) OpenSource(context.Context, message.InboxID, message.MessageID) (mailbox.RawSource, error) {
	return mailbox.RawSource{Reader: io.NopCloser(strings.NewReader("raw")), Size: 3}, nil
}
func (*yydsHTTPMailboxStub) OpenAttachment(context.Context, message.InboxID, message.MessageID, string) (mailbox.AttachmentSource, error) {
	return mailbox.AttachmentSource{Reader: io.NopCloser(strings.NewReader("file")), FileName: "file.txt", ContentType: "text/plain", Size: 4}, nil
}

type yydsCredentialStub struct{}

func (*yydsCredentialStub) Authenticate(context.Context, string, ...auth.Scope) (auth.Principal, error) {
	scopes, _ := auth.NewScopeSet(auth.ScopeInboxRead, auth.ScopeInboxDelete, auth.ScopeMessageRead, auth.ScopeMessageUpdate, auth.ScopeMessageDelete)
	return auth.Principal{InboxID: yydsInboxID, Scopes: scopes, ExpiresAt: time.Now().Add(time.Hour)}, nil
}
func (*yydsCredentialStub) Rotate(context.Context, string) (auth.IssuedCapability, error) {
	return auth.IssuedCapability{InboxID: yydsInboxID, Plaintext: "rotated-token"}, nil
}

const (
	yydsInboxID   = message.InboxID("018f26e5-8f04-7b44-8ba2-4a8f434dcb12")
	yydsMessageID = message.MessageID("018f26e5-8f04-7b44-8ba2-4a8f434dcb13")
)
