package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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

type readinessStub struct {
	err error
}

type authStub struct{}

func (*authStub) Authenticate(context.Context, string, ...auth.Scope) (auth.Principal, error) {
	return auth.Principal{InboxID: message.InboxID("018f26e5-8f04-7b44-8ba2-4a8f434dcb12")}, nil
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
func (*mailboxAPIStub) DeleteMessage(context.Context, message.InboxID, message.MessageID) error {
	return nil
}

func (s *readinessStub) Ready(context.Context) error {
	return s.err
}
