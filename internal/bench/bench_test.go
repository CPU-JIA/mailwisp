package bench

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunHTTPScenarios(t *testing.T) {
	t.Parallel()

	var sequence atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/inboxes":
			index := sequence.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
				"inbox":      map[string]any{"address": fmt.Sprintf("bench-%d@example.com", index)},
				"capability": map[string]any{"token": "wisp_cap_v1_benchmark"},
			}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/inboxes/me":
			if r.Header.Get("Authorization") != "Bearer wisp_cap_v1_benchmark" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"address":"bench@example.com"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	for _, scenario := range []string{ScenarioHTTPCreate, ScenarioHTTPRead} {
		report, err := Run(context.Background(), Options{
			Scenario: scenario, BaseURL: server.URL, Domain: "example.com",
			Requests: 20, Concurrency: 4, Timeout: 5 * time.Second,
		})
		if err != nil {
			t.Fatalf("Run(%s) error = %v", scenario, err)
		}
		if report.Succeeded != 20 || report.Failed != 0 || report.Throughput <= 0 || report.Outcomes[expectedHTTPOutcome(scenario)] != 20 {
			t.Fatalf("Run(%s) report = %+v", scenario, report)
		}
		if report.PayloadBytes != 0 {
			t.Fatalf("Run(%s) payload bytes = %d, want 0", scenario, report.PayloadBytes)
		}
	}
}

func TestRunHTTPCreateRecordsFailureOutcome(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(server.Close)

	report, err := Run(context.Background(), Options{
		Scenario: ScenarioHTTPCreate, BaseURL: server.URL, Domain: "example.com",
		Requests: 3, Concurrency: 2, Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Succeeded != 0 || report.Failed != 3 || report.Outcomes["http_429"] != 3 {
		t.Fatalf("HTTP failure report = %+v", report)
	}
}

func TestRunLMTPDelivery(t *testing.T) {
	t.Parallel()

	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"inbox":{"address":"bench@example.com"},"capability":{"token":"wisp_cap_v1_benchmark"}}}`))
	}))
	t.Cleanup(httpServer.Close)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	var handlers sync.WaitGroup
	handlers.Add(4)
	serverErrors := make(chan error, 4)
	go func() {
		for index := 0; index < 4; index++ {
			connection, err := listener.Accept()
			if err != nil {
				serverErrors <- err
				for remaining := index; remaining < 4; remaining++ {
					handlers.Done()
				}
				return
			}
			go func() {
				defer handlers.Done()
				serverErrors <- serveBenchmarkLMTP(connection)
			}()
		}
	}()

	report, err := Run(context.Background(), Options{
		Scenario: ScenarioLMTPDelivery, BaseURL: httpServer.URL, LMTPAddress: listener.Addr().String(), Domain: "example.com",
		Requests: 4, Concurrency: 2, PayloadBytes: 1024, Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	handlers.Wait()
	for range 4 {
		if err := <-serverErrors; err != nil {
			t.Fatal(err)
		}
	}
	if report.Succeeded != 4 || report.Outcomes["lmtp_250"] != 4 {
		t.Fatalf("LMTP report = %+v", report)
	}
}

func TestRunLMTPDeliveryRecordsTemporaryFailure(t *testing.T) {
	t.Parallel()

	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"inbox":{"address":"bench@example.com"},"capability":{"token":"wisp_cap_v1_benchmark"}}}`))
	}))
	t.Cleanup(httpServer.Close)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	serverError := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverError <- acceptErr
			return
		}
		serverError <- serveBenchmarkLMTPWithFinalResponse(connection, "451 4.3.0 temporarily unavailable\r\n")
	}()

	report, err := Run(context.Background(), Options{
		Scenario: ScenarioLMTPDelivery, BaseURL: httpServer.URL, LMTPAddress: listener.Addr().String(), Domain: "example.com",
		Requests: 1, Concurrency: 1, PayloadBytes: 512, Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := <-serverError; err != nil {
		t.Fatal(err)
	}
	if report.Succeeded != 0 || report.Failed != 1 || report.Outcomes["lmtp_451"] != 1 {
		t.Fatalf("LMTP temporary failure report = %+v", report)
	}
}

func TestRunLMTPDeliveryRecordsGreetingRejection(t *testing.T) {
	t.Parallel()

	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"inbox":{"address":"bench@example.com"},"capability":{"token":"wisp_cap_v1_benchmark"}}}`))
	}))
	t.Cleanup(httpServer.Close)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	serverError := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverError <- acceptErr
			return
		}
		defer connection.Close()
		_, writeErr := io.WriteString(connection, "421 4.3.2 too many sessions\r\n")
		serverError <- writeErr
	}()

	report, err := Run(context.Background(), Options{
		Scenario: ScenarioLMTPDelivery, BaseURL: httpServer.URL, LMTPAddress: listener.Addr().String(), Domain: "example.com",
		Requests: 1, Concurrency: 1, PayloadBytes: 512, Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := <-serverError; err != nil {
		t.Fatal(err)
	}
	if report.Succeeded != 0 || report.Failed != 1 || report.Outcomes["lmtp_421"] != 1 {
		t.Fatalf("LMTP greeting rejection report = %+v", report)
	}
}

func TestRunConcurrentReportsTimeoutBeforeAllRequestsStart(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := runConcurrent(ctx, 10, 2, func(ctx context.Context) (string, bool) {
		<-ctx.Done()
		return classifyError(ctx.Err()), false
	})
	if err == nil || !strings.Contains(err.Error(), "of 10 requests") || !strings.Contains(err.Error(), context.DeadlineExceeded.Error()) {
		t.Fatalf("runConcurrent() error = %v", err)
	}
}

func TestValidationAndLatencySummary(t *testing.T) {
	t.Parallel()

	if _, err := Run(context.Background(), Options{}); err == nil {
		t.Fatal("Run(empty options) error = nil")
	}
	values := []time.Duration{time.Millisecond, 2 * time.Millisecond, 3 * time.Millisecond, 4 * time.Millisecond, 100 * time.Millisecond}
	summary := summarizeLatency(values)
	if summary.P50 != 3 || summary.P95 != 100 || summary.P99 != 100 || summary.Mean != 22 {
		t.Fatalf("latency summary = %+v", summary)
	}
	for _, size := range []int{512, 1024, 8192} {
		header := benchmarkIDHeader("0123456789abcdef0123456789abcdef", 1)
		message := buildMessage(size - len(header))
		if len(header)+len(message) != size {
			t.Fatalf("benchmark message %d size = %d", size, len(header)+len(message))
		}
		if !bytes.HasSuffix(message, []byte("\r\n")) {
			t.Fatalf("benchmark message %d does not end with CRLF", size)
		}
	}
	if first, second := string(benchmarkIDHeader("0123456789abcdef0123456789abcdef", 1)), string(benchmarkIDHeader("0123456789abcdef0123456789abcdef", 2)); first == second || len(first) != len(second) {
		t.Fatalf("benchmark headers = %q / %q", first, second)
	}
}

func expectedHTTPOutcome(scenario string) string {
	if scenario == ScenarioHTTPCreate {
		return "http_201"
	}
	return "http_200"
}

func serveBenchmarkLMTP(connection net.Conn) error {
	return serveBenchmarkLMTPWithFinalResponse(connection, "250 2.0.0 delivered\r\n")
}

func serveBenchmarkLMTPWithFinalResponse(connection net.Conn, finalResponse string) error {
	defer connection.Close()
	reader := bufio.NewReader(connection)
	writer := bufio.NewWriter(connection)
	if _, err := writer.WriteString("220 benchmark LMTP ready\r\n"); err != nil {
		return err
	}
	if err := writer.Flush(); err != nil {
		return err
	}
	steps := []struct {
		prefix   string
		response string
	}{
		{"LHLO ", "250-benchmark\r\n250 SIZE 26214400\r\n"},
		{"MAIL FROM:", "250 2.1.0 sender ok\r\n"},
		{"RCPT TO:", "250 2.1.5 recipient ok\r\n"},
		{"DATA", "354 send message\r\n"},
	}
	declaredSize := 0
	for _, step := range steps {
		line, err := reader.ReadString('\n')
		if err != nil {
			return err
		}
		if !strings.HasPrefix(strings.TrimRight(line, "\r\n"), step.prefix) {
			return fmt.Errorf("LMTP command %q does not start with %q", line, step.prefix)
		}
		if step.prefix == "MAIL FROM:" {
			sizeIndex := strings.LastIndex(line, " SIZE=")
			if sizeIndex < 0 {
				return fmt.Errorf("LMTP MAIL command omitted SIZE: %q", line)
			}
			declaredSize, err = strconv.Atoi(strings.TrimSpace(line[sizeIndex+6:]))
			if err != nil {
				return fmt.Errorf("parse LMTP SIZE: %w", err)
			}
		}
		if _, err := writer.WriteString(step.response); err != nil {
			return err
		}
		if err := writer.Flush(); err != nil {
			return err
		}
	}
	receivedSize := 0
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return err
		}
		if line == ".\r\n" {
			break
		}
		receivedSize += len(line)
	}
	if receivedSize != declaredSize {
		return fmt.Errorf("LMTP message size = %d, declared %d", receivedSize, declaredSize)
	}
	if _, err := writer.WriteString(finalResponse); err != nil {
		return err
	}
	return writer.Flush()
}
