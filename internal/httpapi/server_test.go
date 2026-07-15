package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mailwisp/internal/auth"
	"mailwisp/internal/config"
	"mailwisp/internal/mailbox"
	"mailwisp/internal/message"
)

func TestHealthStates(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := NewServer(config.HTTP{}, logger)

	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	recorder := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("not-ready status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}

	server.SetReady(true)
	recorder = httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("ready status = %d, want %d", recorder.Code, http.StatusOK)
	}
}

func TestReadinessChecksDependencies(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := NewServer(config.HTTP{ReadinessTimeout: time.Second}, logger)
	checker := &readinessStub{}
	server.SetReadinessChecker(checker)
	server.SetReady(true)
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)

	recorder := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("ready dependency status = %d, want %d", recorder.Code, http.StatusOK)
	}

	checker.err = errors.New("postgres unavailable")
	recorder = httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("failed dependency status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
}

func TestLivenessDoesNotDependOnReadiness(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := NewServer(config.HTTP{}, logger)
	request := httptest.NewRequest(http.MethodGet, "/livez", nil)
	recorder := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
}

func TestMetricsUseRoutePatternAndRequireExplicitHandler(t *testing.T) {
	server := NewServer(config.HTTP{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	recorder := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("disabled metrics status = %d", recorder.Code)
	}
	observer := &httpMetricsStub{}
	server.SetMetrics(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "metrics") }), observer)
	recorder = httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "metrics" {
		t.Fatalf("metrics response = %d %q", recorder.Code, recorder.Body.String())
	}
	if observer.route != "GET /metrics" {
		t.Fatalf("observed route = %q", observer.route)
	}
}

func TestCanonicalInboxAPIRequiresCapabilityAndReturnsRequestID(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := NewServer(config.HTTP{ReadinessTimeout: time.Second, CreateRatePerMinute: 60, CreateRateBurst: 2}, logger)
	service := &mailboxAPIStub{}
	server.SetMailboxService(service, &authStub{})

	unauthenticated := httptest.NewRequest(http.MethodGet, "/api/v1/inboxes/me", nil)
	recorder := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(recorder, unauthenticated)
	if recorder.Code != http.StatusUnauthorized || recorder.Header().Get("X-Request-ID") == "" {
		t.Fatalf("unauthenticated response = %d, request id = %q", recorder.Code, recorder.Header().Get("X-Request-ID"))
	}

	authenticated := httptest.NewRequest(http.MethodGet, "/api/v1/inboxes/me", nil)
	authenticated.Header.Set("Authorization", "Bearer wisp_cap_v1_test")
	recorder = httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(recorder, authenticated)
	if recorder.Code != http.StatusOK {
		t.Fatalf("authenticated status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data mailbox.Inbox `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Data.Address != "demo@mailwisp.test" {
		t.Fatalf("response Inbox = %+v", envelope.Data)
	}
}

func TestBrowserSessionExchangeRequiresCSRFForMutation(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := NewServer(config.HTTP{ReadinessTimeout: time.Second}, logger)
	server.SetMailboxService(&mailboxAPIStub{}, &authStub{})
	manager, err := auth.NewBrowserSessionManager([]byte(strings.Repeat("k", 32)), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	server.SetBrowserSessions(manager)

	exchange := httptest.NewRequest(http.MethodPost, "/api/v1/session", nil)
	exchange.Header.Set("Authorization", "Bearer wisp_cap_v1_test")
	recorder := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(recorder, exchange)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("exchange status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var sessionCookie *http.Cookie
	for _, cookie := range recorder.Result().Cookies() {
		switch cookie.Name {
		case "__Host-mailwisp_session":
			sessionCookie = cookie
		}
	}
	if sessionCookie == nil || !sessionCookie.HttpOnly || !sessionCookie.Secure {
		t.Fatalf("session Cookie = %+v", sessionCookie)
	}
	var exchangeEnvelope struct {
		Data struct {
			CSRFToken string `json:"csrf_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &exchangeEnvelope); err != nil || exchangeEnvelope.Data.CSRFToken == "" {
		t.Fatalf("exchange CSRF response = %+v, %v", exchangeEnvelope, err)
	}

	get := httptest.NewRequest(http.MethodGet, "/api/v1/session", nil)
	get.AddCookie(sessionCookie)
	recorder = httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(recorder, get)
	if recorder.Code != http.StatusOK {
		t.Fatalf("session GET status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	deleteWithoutCSRF := httptest.NewRequest(http.MethodDelete, "/api/v1/session", nil)
	deleteWithoutCSRF.AddCookie(sessionCookie)
	recorder = httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(recorder, deleteWithoutCSRF)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF status = %d", recorder.Code)
	}

	deleteWithCSRF := httptest.NewRequest(http.MethodDelete, "/api/v1/session", nil)
	deleteWithCSRF.AddCookie(sessionCookie)
	deleteWithCSRF.Header.Set("X-MailWisp-CSRF", exchangeEnvelope.Data.CSRFToken)
	recorder = httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(recorder, deleteWithCSRF)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("session DELETE status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestAttachmentDownloadStreamsOwnedContent(t *testing.T) {
	server := NewServer(config.HTTP{ReadinessTimeout: time.Second}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.SetMailboxService(&mailboxAPIStub{}, &authStub{})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/inboxes/me/messages/018f26e5-8f04-7b44-8ba2-4a8f434dcb12/attachments/2", nil)
	request.Header.Set("Authorization", "Bearer wisp_cap_v1_test")
	recorder := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "attachment bytes" {
		t.Fatalf("attachment response = %d %q", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Content-Type") != "text/plain" || !strings.Contains(recorder.Header().Get("Content-Disposition"), "report.txt") {
		t.Fatalf("attachment headers = %v", recorder.Header())
	}
}

type readinessStub struct {
	err error
}

type httpMetricsStub struct{ route string }

func (s *httpMetricsStub) ObserveHTTPRequest(_, route string, _ int, _ time.Duration) {
	s.route = route
}

type authStub struct{}

func (*authStub) Authenticate(context.Context, string, ...auth.Scope) (auth.Principal, error) {
	scopes, _ := auth.NewScopeSet(auth.ScopeInboxRead, auth.ScopeInboxDelete, auth.ScopeMessageRead, auth.ScopeMessageDelete)
	return auth.Principal{InboxID: message.InboxID("018f26e5-8f04-7b44-8ba2-4a8f434dcb12"), Scopes: scopes, ExpiresAt: time.Now().Add(time.Hour)}, nil
}

type mailboxAPIStub struct{}

func (*mailboxAPIStub) Create(context.Context, mailbox.CreateRequest) (mailbox.CreatedInbox, error) {
	return mailbox.CreatedInbox{}, nil
}
func (*mailboxAPIStub) Get(context.Context, message.InboxID) (mailbox.Inbox, error) {
	return mailbox.Inbox{ID: message.InboxID("018f26e5-8f04-7b44-8ba2-4a8f434dcb12"), Address: "demo@mailwisp.test", Status: "active"}, nil
}
func (*mailboxAPIStub) Delete(context.Context, message.InboxID) error { return nil }
func (*mailboxAPIStub) ListMessages(context.Context, message.InboxID, int) ([]mailbox.MessageSummary, error) {
	return []mailbox.MessageSummary{}, nil
}
func (*mailboxAPIStub) GetMessage(context.Context, message.InboxID, message.MessageID) (mailbox.MessageDetail, error) {
	return mailbox.MessageDetail{}, nil
}
func (*mailboxAPIStub) OpenAttachment(context.Context, message.InboxID, message.MessageID, string) (mailbox.AttachmentSource, error) {
	return mailbox.AttachmentSource{Reader: io.NopCloser(strings.NewReader("attachment bytes")), FileName: "report.txt", ContentType: "text/plain", Size: 16}, nil
}
func (*mailboxAPIStub) DeleteMessage(context.Context, message.InboxID, message.MessageID) error {
	return nil
}

func (s *readinessStub) Ready(context.Context) error {
	return s.err
}
