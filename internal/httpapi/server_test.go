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
	"strings"
	"testing"
	"time"

	"mailwisp/internal/abuse"
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

func TestCanonicalCreateUsesPersistentDailyQuota(t *testing.T) {
	t.Parallel()

	server := NewServer(config.HTTP{CreateRatePerMinute: 60, CreateRateBurst: 10}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	mailboxes := &mailboxAPIStub{}
	quota := &createQuotaStub{}
	server.SetMailboxService(mailboxes, &authStub{})
	server.SetCreateQuota(quota)

	request := httptest.NewRequest(http.MethodPost, "/api/v1/inboxes", strings.NewReader(`{"domain":"mailwisp.test"}`))
	request.RemoteAddr = "192.0.2.10:1234"
	recorder := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated || recorder.Header().Get("RateLimit-Limit") != "100" || quota.clientAddress != "192.0.2.10" || mailboxes.creates != 1 {
		t.Fatalf("create response=%d headers=%v client=%q creates=%d", recorder.Code, recorder.Header(), quota.clientAddress, mailboxes.creates)
	}

	quota.err = abuse.ErrDailyCreateQuotaExceeded
	request = httptest.NewRequest(http.MethodPost, "/api/v1/inboxes", strings.NewReader(`{}`))
	request.RemoteAddr = "192.0.2.10:1235"
	recorder = httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusTooManyRequests || recorder.Header().Get("Retry-After") == "" || mailboxes.creates != 1 {
		t.Fatalf("quota response=%d headers=%v creates=%d body=%s", recorder.Code, recorder.Header(), mailboxes.creates, recorder.Body.String())
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

func TestRawSourceDownloadStreamsOwnedRFC822(t *testing.T) {
	server := NewServer(config.HTTP{ReadinessTimeout: time.Second}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.SetMailboxService(&mailboxAPIStub{}, &authStub{})
	messageID := "018f26e5-8f04-7b44-8ba2-4a8f434dcb13"
	request := httptest.NewRequest(http.MethodGet, "/api/v1/inboxes/me/messages/"+messageID+"/source", nil)
	request.Header.Set("Authorization", "Bearer wisp_cap_v1_test")
	recorder := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "Subject: failed\r\n\r\nraw bytes" {
		t.Fatalf("Raw Source response = %d %q", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Content-Type") != "message/rfc822" || recorder.Header().Get("Cache-Control") != "no-store" || recorder.Header().Get("Content-Length") != "28" {
		t.Fatalf("Raw Source headers = %v", recorder.Header())
	}
	if !strings.Contains(recorder.Header().Get("Content-Disposition"), messageID+".eml") || recorder.Header().Get("ETag") != "" {
		t.Fatalf("Raw Source disposition/correlation headers = %v", recorder.Header())
	}
}

func TestCanonicalMessageSeenMutationUsesUpdateScope(t *testing.T) {
	service := &mailboxAPIStub{}
	server := NewServer(config.HTTP{ReadinessTimeout: time.Second}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.SetMailboxService(service, &authStub{})
	messageID := "018f26e5-8f04-7b44-8ba2-4a8f434dcb13"
	request := httptest.NewRequest(http.MethodPatch, "/api/v1/inboxes/me/messages/"+messageID, strings.NewReader(`{"seen":true}`))
	request.Header.Set("Authorization", "Bearer wisp_cap_v1_test")
	recorder := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent || service.seenMessage != message.MessageID(messageID) {
		t.Fatalf("message seen response = %d %s, message = %q", recorder.Code, recorder.Body.String(), service.seenMessage)
	}

	invalid := httptest.NewRequest(http.MethodPatch, "/api/v1/inboxes/me/messages/"+messageID, strings.NewReader(`{"seen":false}`))
	invalid.Header.Set("Authorization", "Bearer wisp_cap_v1_test")
	recorder = httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(recorder, invalid)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), `"code":"invalid_request"`) {
		t.Fatalf("invalid seen response = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestCanonicalHeavyReadReturnsExplicitBackpressure(t *testing.T) {
	service := &mailboxAPIStub{}
	server := NewServer(config.HTTP{ReadinessTimeout: time.Second, HeavyReadConcurrency: 1}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.SetMailboxService(service, &authStub{})
	server.heavyReads <- struct{}{}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/inboxes/me/messages/018f26e5-8f04-7b44-8ba2-4a8f434dcb13/source", nil)
	request.Header.Set("Authorization", "Bearer wisp_cap_v1_test")
	recorder := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(recorder, request)
	<-server.heavyReads
	if recorder.Code != http.StatusServiceUnavailable || recorder.Header().Get("Retry-After") != "1" || !strings.Contains(recorder.Body.String(), `"code":"service_busy"`) || service.sourceOpens != 0 {
		t.Fatalf("heavy read response = %d %s headers=%v opens=%d", recorder.Code, recorder.Body.String(), recorder.Header(), service.sourceOpens)
	}
}

func TestUnmappedHTTPErrorLogsRootCauseWithoutLeakingIt(t *testing.T) {
	var logs bytes.Buffer
	service := &mailboxAPIStub{getError: errors.New("database connection detail")}
	server := NewServer(config.HTTP{ReadinessTimeout: time.Second}, slog.New(slog.NewJSONHandler(&logs, nil)))
	server.SetMailboxService(service, &authStub{})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/inboxes/me", nil)
	request.Header.Set("Authorization", "Bearer wisp_cap_v1_test")
	recorder := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusInternalServerError || strings.Contains(recorder.Body.String(), "database connection detail") {
		t.Fatalf("internal error response = %d %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(logs.String(), "database connection detail") || !strings.Contains(logs.String(), "request_id") {
		t.Fatalf("internal error log = %s", logs.String())
	}
}

func TestMessageListPreservesDataArrayAndReturnsNextCursor(t *testing.T) {
	before := mailbox.MessageCursor{ReceivedAt: time.Date(2026, 7, 16, 2, 0, 0, 123456000, time.UTC), ID: "018f26e5-8f04-7b44-8ba2-4a8f434dcb12"}
	next := mailbox.MessageCursor{ReceivedAt: time.Date(2026, 7, 16, 1, 0, 0, 654321000, time.UTC), ID: "018f26e5-8f04-7b44-8ba2-4a8f434dcb13"}
	encodedBefore, err := encodeMessageCursor(before)
	if err != nil {
		t.Fatal(err)
	}
	service := &mailboxAPIStub{listResult: mailbox.CursorMessagePage{
		Items: []mailbox.MessageSummary{{ID: next.ID, ReceivedAt: next.ReceivedAt}},
		Next:  &next,
	}}
	server := NewServer(config.HTTP{ReadinessTimeout: time.Second}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.SetMailboxService(service, &authStub{})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/inboxes/me/messages?limit=2&cursor="+encodedBefore, nil)
	request.Header.Set("Authorization", "Bearer wisp_cap_v1_test")
	recorder := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("message list status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if service.listRequest.Limit != 2 || service.listRequest.Before == nil || service.listRequest.Before.ID != before.ID || !service.listRequest.Before.ReceivedAt.Equal(before.ReceivedAt) {
		t.Fatalf("message list request = %+v", service.listRequest)
	}
	var response struct {
		Data       []mailbox.MessageSummary `json:"data"`
		Pagination struct {
			NextCursor string `json:"next_cursor"`
		} `json:"pagination"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	decodedNext, err := decodeMessageCursor(response.Pagination.NextCursor)
	if err != nil || len(response.Data) != 1 || decodedNext == nil || decodedNext.ID != next.ID || !decodedNext.ReceivedAt.Equal(next.ReceivedAt) {
		t.Fatalf("message list response = %+v, decoded = %+v, error = %v", response, decodedNext, err)
	}

	invalid := httptest.NewRequest(http.MethodGet, "/api/v1/inboxes/me/messages?cursor=tampered", nil)
	invalid.Header.Set("Authorization", "Bearer wisp_cap_v1_test")
	recorder = httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(recorder, invalid)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), `"code":"invalid_pagination"`) {
		t.Fatalf("invalid cursor response = %d %s", recorder.Code, recorder.Body.String())
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
	scopes, _ := auth.NewScopeSet(auth.ScopeInboxRead, auth.ScopeInboxDelete, auth.ScopeMessageRead, auth.ScopeMessageUpdate, auth.ScopeMessageDelete)
	return auth.Principal{InboxID: message.InboxID("018f26e5-8f04-7b44-8ba2-4a8f434dcb12"), Scopes: scopes, ExpiresAt: time.Now().Add(time.Hour)}, nil
}

type mailboxAPIStub struct {
	creates     int
	listRequest mailbox.CursorPage
	listResult  mailbox.CursorMessagePage
	getError    error
	seenMessage message.MessageID
	sourceOpens int
}

func (s *mailboxAPIStub) Create(context.Context, mailbox.CreateRequest) (mailbox.CreatedInbox, error) {
	s.creates++
	return mailbox.CreatedInbox{Inbox: mailbox.Inbox{Address: "demo@mailwisp.test"}, Capability: auth.IssuedCapability{Plaintext: "created-token"}}, nil
}

func (s *mailboxAPIStub) Get(context.Context, message.InboxID) (mailbox.Inbox, error) {
	if s.getError != nil {
		return mailbox.Inbox{}, s.getError
	}
	return mailbox.Inbox{ID: message.InboxID("018f26e5-8f04-7b44-8ba2-4a8f434dcb12"), Address: "demo@mailwisp.test", Status: "active"}, nil
}
func (*mailboxAPIStub) Delete(context.Context, message.InboxID) error { return nil }
func (s *mailboxAPIStub) ListMessages(_ context.Context, _ message.InboxID, request mailbox.CursorPage) (mailbox.CursorMessagePage, error) {
	s.listRequest = request
	if s.listResult.Items == nil {
		s.listResult.Items = []mailbox.MessageSummary{}
	}
	return s.listResult, nil
}
func (*mailboxAPIStub) GetMessage(context.Context, message.InboxID, message.MessageID) (mailbox.MessageDetail, error) {
	return mailbox.MessageDetail{}, nil
}

func (s *mailboxAPIStub) OpenSource(context.Context, message.InboxID, message.MessageID) (mailbox.RawSource, error) {
	s.sourceOpens++
	return mailbox.RawSource{Reader: io.NopCloser(strings.NewReader("Subject: failed\r\n\r\nraw bytes")), Size: 28}, nil
}
func (*mailboxAPIStub) OpenAttachment(context.Context, message.InboxID, message.MessageID, string) (mailbox.AttachmentSource, error) {
	return mailbox.AttachmentSource{Reader: io.NopCloser(strings.NewReader("attachment bytes")), FileName: "report.txt", ContentType: "text/plain", Size: 16}, nil
}
func (s *mailboxAPIStub) MarkMessageSeen(_ context.Context, _ message.InboxID, messageID message.MessageID) error {
	s.seenMessage = messageID
	return nil
}
func (*mailboxAPIStub) DeleteMessage(context.Context, message.InboxID, message.MessageID) error {
	return nil
}

func (s *readinessStub) Ready(context.Context) error {
	return s.err
}

type createQuotaStub struct {
	clientAddress string
	err           error
}

func (s *createQuotaStub) Consume(_ context.Context, clientAddress string) (abuse.Decision, error) {
	s.clientAddress = clientAddress
	decision := abuse.Decision{Limit: 100, Remaining: 99, ResetAt: time.Now().Add(time.Hour)}
	if errors.Is(s.err, abuse.ErrDailyCreateQuotaExceeded) {
		decision.Remaining = 0
	}
	return decision, s.err
}
