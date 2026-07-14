package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"mailwisp/internal/config"
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

type readinessStub struct {
	err error
}

func (s *readinessStub) Ready(context.Context) error {
	return s.err
}
