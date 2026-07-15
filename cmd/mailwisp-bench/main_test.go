package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRunRejectsInvalidArguments(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	err := run([]string{"-scenario", "unsupported"}, &output)
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("run() error = %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("run() output = %q", output.String())
	}
}

func TestRunWritesFailureReportBeforeReturningError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(server.Close)

	var output bytes.Buffer
	err := run([]string{
		"-scenario", "http-inbox-create",
		"-base-url", server.URL,
		"-requests", "2",
		"-concurrency", "1",
	}, &output)
	if err == nil || !strings.Contains(err.Error(), "2 failed operations") {
		t.Fatalf("run() error = %v", err)
	}
	if !strings.Contains(output.String(), `"failed": 2`) || !strings.Contains(output.String(), `"http_429": 2`) {
		t.Fatalf("run() output = %s", output.String())
	}
}
