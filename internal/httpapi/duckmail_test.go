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
	"mailwisp/internal/duckmail"
	"mailwisp/internal/mailbox"
	"mailwisp/internal/message"
)

func TestDuckMailContractAccountTokenAndDomains(t *testing.T) {
	fixtureBytes, err := os.ReadFile("testdata/duckmail-contract.json")
	if err != nil {
		t.Fatalf("read DuckMail contract fixture: %v", err)
	}
	var fixture struct {
		Source    string   `json:"source"`
		Namespace string   `json:"mailwisp_namespace"`
		Endpoints []string `json:"endpoints"`
	}
	if err := json.Unmarshal(fixtureBytes, &fixture); err != nil || fixture.Source == "" || fixture.Namespace != "/compat/duckmail" || len(fixture.Endpoints) != 10 {
		t.Fatalf("DuckMail fixture = %+v, error = %v", fixture, err)
	}
	repository := &duckHTTPRepositoryStub{}
	mailboxes := &duckHTTPMailboxStub{}
	service, err := duckmail.NewService(repository, mailboxes, &duckHTTPIssuerStub{}, duckmail.Options{PublicDomains: []string{"mailwisp.test"}, DefaultTTL: time.Hour, MaxTTL: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(config.HTTP{CreateRatePerMinute: 60, CreateRateBurst: 10}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	quota := &createQuotaStub{}
	server.SetMailboxService(mailboxes, &authStub{})
	server.SetCreateQuota(quota)
	server.SetDuckMailService(service)

	domains := performDuckRequest(server, http.MethodGet, "/compat/duckmail/domains", "", "")
	if domains.Code != http.StatusOK || !strings.Contains(domains.Body.String(), `"hydra:member"`) {
		t.Fatalf("domains = %d %s", domains.Code, domains.Body.String())
	}
	account := performDuckRequest(server, http.MethodPost, "/compat/duckmail/accounts", `{"address":"demo@mailwisp.test","password":"secret-password"}`, "")
	if account.Code != http.StatusCreated || !strings.Contains(account.Body.String(), `"authType":"email"`) {
		t.Fatalf("account = %d %s", account.Code, account.Body.String())
	}
	quota.err = abuse.ErrDailyCreateQuotaExceeded
	limited := performDuckRequest(server, http.MethodPost, "/compat/duckmail/accounts", `{"address":"limited@mailwisp.test","password":"secret-password"}`, "")
	if limited.Code != http.StatusTooManyRequests {
		t.Fatalf("daily quota = %d %s", limited.Code, limited.Body.String())
	}
	quota.err = nil
	token := performDuckRequest(server, http.MethodPost, "/compat/duckmail/token", `{"address":"demo@mailwisp.test","password":"secret-password"}`, "")
	if token.Code != http.StatusOK || !strings.Contains(token.Body.String(), `"token":"duckmail-http-token"`) {
		t.Fatalf("token = %d %s", token.Code, token.Body.String())
	}
	messages := performDuckRequest(server, http.MethodGet, "/compat/duckmail/messages", "", "Bearer duckmail-http-token")
	if messages.Code != http.StatusOK {
		t.Fatalf("messages = %d %s", messages.Code, messages.Body.String())
	}
	var collection map[string]json.RawMessage
	if err := json.Unmarshal(messages.Body.Bytes(), &collection); err != nil || collection["hydra:totalItems"] == nil {
		t.Fatalf("messages collection = %s, error = %v", messages.Body.String(), err)
	}
}

func TestDuckMailCompatibilityDisabledUsesDuckErrorEnvelope(t *testing.T) {
	server := NewServer(config.HTTP{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	response := performDuckRequest(server, http.MethodGet, "/compat/duckmail/domains", "", "")
	if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), `"error":"Not Found"`) || strings.Contains(response.Body.String(), "request_id") {
		t.Fatalf("disabled response = %d %s", response.Code, response.Body.String())
	}
}

func performDuckRequest(server *Server, method, path, body, authorization string) *httptest.ResponseRecorder {
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

type duckHTTPRepositoryStub struct{ account duckmail.NewAccount }

func (r *duckHTTPRepositoryStub) CreateAccount(_ context.Context, account duckmail.NewAccount) (mailbox.Inbox, error) {
	r.account = account
	return mailbox.Inbox{ID: message.InboxID("018f26e5-8f04-7b44-8ba2-4a8f434dcb12"), Address: account.Address, Status: "active", CreatedAt: account.CreatedAt, ExpiresAt: account.ExpiresAt}, nil
}
func (r *duckHTTPRepositoryStub) FindAccountByAddress(_ context.Context, address string) (duckmail.Account, error) {
	if r.account.Address != address {
		return duckmail.Account{}, duckmail.ErrLoginFailed
	}
	return duckmail.Account{Inbox: mailbox.Inbox{ID: message.InboxID("018f26e5-8f04-7b44-8ba2-4a8f434dcb12"), Address: address, Status: "active", CreatedAt: r.account.CreatedAt, ExpiresAt: r.account.ExpiresAt}, PasswordHash: r.account.PasswordHash}, nil
}

type duckHTTPIssuerStub struct{}

func (*duckHTTPIssuerStub) Issue(_ context.Context, inboxID message.InboxID, scopes auth.ScopeSet, expiresAt time.Time) (auth.IssuedCapability, error) {
	return auth.IssuedCapability{InboxID: inboxID, Plaintext: "duckmail-http-token", Scopes: scopes, ExpiresAt: expiresAt}, nil
}

type duckHTTPMailboxStub struct{}

func (*duckHTTPMailboxStub) Create(context.Context, mailbox.CreateRequest) (mailbox.CreatedInbox, error) {
	return mailbox.CreatedInbox{}, nil
}
func (*duckHTTPMailboxStub) Get(_ context.Context, inboxID message.InboxID) (mailbox.Inbox, error) {
	return mailbox.Inbox{ID: inboxID, Address: "demo@mailwisp.test", Status: "active", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}, nil
}
func (*duckHTTPMailboxStub) Delete(context.Context, message.InboxID) error { return nil }
func (*duckHTTPMailboxStub) ListMessages(context.Context, message.InboxID, int) ([]mailbox.MessageSummary, error) {
	return []mailbox.MessageSummary{}, nil
}
func (*duckHTTPMailboxStub) ListMessagePage(context.Context, message.InboxID, int, int) (mailbox.MessagePage, error) {
	return mailbox.MessagePage{Items: []mailbox.MessageSummary{}, Total: 0}, nil
}
func (*duckHTTPMailboxStub) GetMessage(context.Context, message.InboxID, message.MessageID) (mailbox.MessageDetail, error) {
	return mailbox.MessageDetail{}, nil
}
func (*duckHTTPMailboxStub) OpenAttachment(context.Context, message.InboxID, message.MessageID, string) (mailbox.AttachmentSource, error) {
	return mailbox.AttachmentSource{}, mailbox.ErrMessageNotFound
}
func (*duckHTTPMailboxStub) DeleteMessage(context.Context, message.InboxID, message.MessageID) error {
	return nil
}
func (*duckHTTPMailboxStub) MarkMessageSeen(context.Context, message.InboxID, message.MessageID) error {
	return nil
}
func (*duckHTTPMailboxStub) OpenSource(context.Context, message.InboxID, message.MessageID) (mailbox.RawSource, error) {
	return mailbox.RawSource{Reader: io.NopCloser(strings.NewReader("raw")), Size: 3}, nil
}
