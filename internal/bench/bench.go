// Package bench implements bounded black-box capacity scenarios for MailWisp.
package bench

import (
	"bufio"
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// ScenarioHTTPRead measures authenticated Canonical Inbox reads.
	ScenarioHTTPRead = "http-inbox-read"
	// ScenarioHTTPCreate measures Canonical anonymous Inbox creation.
	ScenarioHTTPCreate = "http-inbox-create"
	// ScenarioLMTPDelivery measures one durable LMTP delivery per connection.
	ScenarioLMTPDelivery = "lmtp-delivery"
)

// Options configures one bounded benchmark run.
type Options struct {
	Scenario     string
	BaseURL      string
	LMTPAddress  string
	Domain       string
	Requests     int
	Concurrency  int
	PayloadBytes int
	Timeout      time.Duration
}

// LatencySummary contains deterministic latency percentiles in milliseconds.
type LatencySummary struct {
	Minimum float64 `json:"minimum_ms"`
	Mean    float64 `json:"mean_ms"`
	P50     float64 `json:"p50_ms"`
	P95     float64 `json:"p95_ms"`
	P99     float64 `json:"p99_ms"`
	Maximum float64 `json:"maximum_ms"`
}

// Report is the machine-readable result of one scenario.
type Report struct {
	SchemaVersion int            `json:"schema_version"`
	Scenario      string         `json:"scenario"`
	StartedAt     time.Time      `json:"started_at"`
	Duration      time.Duration  `json:"duration_ns"`
	Requests      int            `json:"requests"`
	Concurrency   int            `json:"concurrency"`
	PayloadBytes  int            `json:"payload_bytes,omitempty"`
	Succeeded     int            `json:"succeeded"`
	Failed        int            `json:"failed"`
	Throughput    float64        `json:"throughput_per_second"`
	Outcomes      map[string]int `json:"outcomes"`
	Latency       LatencySummary `json:"latency"`
}

type operationResult struct {
	duration time.Duration
	outcome  string
	success  bool
}

type inboxCredential struct {
	Address string
	Token   string
}

// Run executes one bounded scenario and returns aggregate results.
func Run(ctx context.Context, options Options) (Report, error) {
	if err := validateOptions(options); err != nil {
		return Report{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, options.Timeout)
	defer cancel()

	client := &http.Client{
		Timeout: minDuration(30*time.Second, options.Timeout),
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			MaxIdleConns:          max(100, options.Concurrency*2),
			MaxIdleConnsPerHost:   max(100, options.Concurrency*2),
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
		},
	}
	defer client.CloseIdleConnections()

	var operation func(context.Context) (string, bool)
	switch options.Scenario {
	case ScenarioHTTPCreate:
		operation = func(ctx context.Context) (string, bool) {
			_, status, err := createInbox(ctx, client, options.BaseURL, options.Domain)
			if err != nil {
				return classifyError(err), false
			}
			return "http_" + strconv.Itoa(status), status == http.StatusCreated
		}
	case ScenarioHTTPRead:
		credential, status, err := createInbox(ctx, client, options.BaseURL, options.Domain)
		if err != nil {
			return Report{}, fmt.Errorf("prepare HTTP read benchmark: %w", err)
		}
		if status != http.StatusCreated {
			return Report{}, fmt.Errorf("prepare HTTP read benchmark: HTTP status %d", status)
		}
		operation = func(ctx context.Context) (string, bool) {
			status, err := readInbox(ctx, client, options.BaseURL, credential.Token)
			if err != nil {
				return classifyError(err), false
			}
			return "http_" + strconv.Itoa(status), status == http.StatusOK
		}
	case ScenarioLMTPDelivery:
		credential, status, err := createInbox(ctx, client, options.BaseURL, options.Domain)
		if err != nil {
			return Report{}, fmt.Errorf("prepare LMTP benchmark: %w", err)
		}
		if status != http.StatusCreated {
			return Report{}, fmt.Errorf("prepare LMTP benchmark: HTTP status %d", status)
		}
		runID, err := newRunID()
		if err != nil {
			return Report{}, fmt.Errorf("prepare LMTP benchmark ID: %w", err)
		}
		benchmarkHeaderBytes := len(benchmarkIDHeader(runID, 0))
		payload := buildMessage(options.PayloadBytes - benchmarkHeaderBytes)
		var deliverySequence atomic.Uint64
		operation = func(ctx context.Context) (string, bool) {
			status, err := deliverLMTP(ctx, options.LMTPAddress, credential.Address, payload, runID, deliverySequence.Add(1), minDuration(30*time.Second, options.Timeout))
			if status != 0 {
				return "lmtp_" + strconv.Itoa(status), err == nil && status == 250
			}
			if err != nil {
				return classifyError(err), false
			}
			return "error_protocol", false
		}
	}

	started := time.Now().UTC()
	results, err := runConcurrent(ctx, options.Requests, options.Concurrency, operation)
	duration := time.Since(started)
	if err != nil {
		return Report{}, err
	}
	report := summarize(options, started, duration, results)
	return report, nil
}

func validateOptions(options Options) error {
	if options.Scenario != ScenarioHTTPRead && options.Scenario != ScenarioHTTPCreate && options.Scenario != ScenarioLMTPDelivery {
		return errors.New("benchmark scenario is unsupported")
	}
	if options.Requests <= 0 || options.Requests > 10_000_000 {
		return errors.New("benchmark requests must be between 1 and 10000000")
	}
	if options.Concurrency <= 0 || options.Concurrency > 1024 || options.Concurrency > options.Requests {
		return errors.New("benchmark concurrency must be positive, at most 1024, and not exceed requests")
	}
	if options.Timeout <= 0 || options.Timeout > 24*time.Hour {
		return errors.New("benchmark timeout must be between 1ns and 24h")
	}
	if strings.TrimSpace(options.BaseURL) == "" || strings.TrimSpace(options.Domain) == "" {
		return errors.New("benchmark base URL and domain are required")
	}
	if options.Scenario == ScenarioLMTPDelivery {
		if strings.TrimSpace(options.LMTPAddress) == "" {
			return errors.New("benchmark LMTP address is required")
		}
		if options.PayloadBytes < 512 || options.PayloadBytes > 25<<20 {
			return errors.New("benchmark LMTP payload must be between 512 bytes and 25 MiB")
		}
	}
	return nil
}

func runConcurrent(ctx context.Context, requests, concurrency int, operation func(context.Context) (string, bool)) ([]operationResult, error) {
	jobs := make(chan struct{})
	results := make(chan operationResult, requests)
	var workers sync.WaitGroup
	workers.Add(concurrency)
	for range concurrency {
		go func() {
			defer workers.Done()
			for range jobs {
				started := time.Now()
				outcome, success := operation(ctx)
				results <- operationResult{duration: time.Since(started), outcome: outcome, success: success}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for range requests {
			select {
			case jobs <- struct{}{}:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		workers.Wait()
		close(results)
	}()

	collected := make([]operationResult, 0, requests)
	for result := range results {
		collected = append(collected, result)
	}
	if len(collected) != requests {
		return nil, fmt.Errorf("benchmark stopped after %d of %d requests: %w", len(collected), requests, ctx.Err())
	}
	return collected, nil
}

func summarize(options Options, started time.Time, duration time.Duration, results []operationResult) Report {
	latencies := make([]time.Duration, 0, len(results))
	outcomes := make(map[string]int)
	succeeded := 0
	for _, result := range results {
		latencies = append(latencies, result.duration)
		outcomes[result.outcome]++
		if result.success {
			succeeded++
		}
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	payloadBytes := 0
	if options.Scenario == ScenarioLMTPDelivery {
		payloadBytes = options.PayloadBytes
	}
	return Report{
		SchemaVersion: 1,
		Scenario:      options.Scenario,
		StartedAt:     started,
		Duration:      duration,
		Requests:      len(results),
		Concurrency:   options.Concurrency,
		PayloadBytes:  payloadBytes,
		Succeeded:     succeeded,
		Failed:        len(results) - succeeded,
		Throughput:    float64(len(results)) / duration.Seconds(),
		Outcomes:      outcomes,
		Latency:       summarizeLatency(latencies),
	}
}

func summarizeLatency(values []time.Duration) LatencySummary {
	if len(values) == 0 {
		return LatencySummary{}
	}
	var total time.Duration
	for _, value := range values {
		total += value
	}
	return LatencySummary{
		Minimum: milliseconds(values[0]),
		Mean:    milliseconds(total / time.Duration(len(values))),
		P50:     milliseconds(percentile(values, 50)),
		P95:     milliseconds(percentile(values, 95)),
		P99:     milliseconds(percentile(values, 99)),
		Maximum: milliseconds(values[len(values)-1]),
	}
}

func percentile(values []time.Duration, percent int) time.Duration {
	index := (percent*len(values) + 99) / 100
	if index < 1 {
		index = 1
	}
	return values[index-1]
}

func milliseconds(value time.Duration) float64 { return float64(value) / float64(time.Millisecond) }

func createInbox(ctx context.Context, client *http.Client, baseURL, domain string) (inboxCredential, int, error) {
	body, err := json.Marshal(map[string]any{"domain": domain, "ttl_seconds": 3600})
	if err != nil {
		return inboxCredential{}, 0, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/api/v1/inboxes", bytes.NewReader(body))
	if err != nil {
		return inboxCredential{}, 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return inboxCredential{}, 0, err
	}
	defer response.Body.Close()
	content, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return inboxCredential{}, response.StatusCode, err
	}
	if response.StatusCode != http.StatusCreated {
		return inboxCredential{}, response.StatusCode, nil
	}
	var envelope struct {
		Data struct {
			Inbox struct {
				Address string `json:"address"`
			} `json:"inbox"`
			Capability struct {
				Token string `json:"token"`
			} `json:"capability"`
		} `json:"data"`
	}
	if err := json.Unmarshal(content, &envelope); err != nil {
		return inboxCredential{}, response.StatusCode, fmt.Errorf("decode create response: %w", err)
	}
	if envelope.Data.Inbox.Address == "" || envelope.Data.Capability.Token == "" {
		return inboxCredential{}, response.StatusCode, errors.New("create response omitted Inbox address or capability")
	}
	return inboxCredential{Address: envelope.Data.Inbox.Address, Token: envelope.Data.Capability.Token}, response.StatusCode, nil
}

func readInbox(ctx context.Context, client *http.Client, baseURL, token string) (int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/api/v1/inboxes/me", nil)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	_, err = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	return response.StatusCode, err
}

func buildMessage(targetBytes int) []byte {
	header := []byte("From: benchmark@example.net\r\nTo: benchmark@example.com\r\nSubject: MailWisp capacity benchmark\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n")
	if targetBytes <= len(header)+2 {
		return header
	}
	message := append([]byte(nil), header...)
	remaining := targetBytes - len(message)
	line := bytes.Repeat([]byte{'a'}, 76)
	for remaining > 78 {
		contentBytes := len(line)
		if remaining-78 == 1 {
			contentBytes--
		}
		message = append(message, line[:contentBytes]...)
		message = append(message, '\r', '\n')
		remaining -= contentBytes + 2
	}
	message = append(message, line[:remaining-2]...)
	message = append(message, '\r', '\n')
	return message
}

func newRunID() (string, error) {
	value := make([]byte, 16)
	if _, err := cryptorand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func benchmarkIDHeader(runID string, sequence uint64) []byte {
	return fmt.Appendf(nil, "X-MailWisp-Benchmark-ID: %s-%016x\r\n", runID, sequence)
}

func deliverLMTP(ctx context.Context, address, recipient string, message []byte, runID string, sequence uint64, timeout time.Duration) (int, error) {
	dialer := net.Dialer{Timeout: timeout}
	connection, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return 0, err
	}
	defer connection.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	} else {
		_ = connection.SetDeadline(time.Now().Add(timeout))
	}
	reader := bufio.NewReader(connection)
	writer := bufio.NewWriter(connection)
	status, err := expectResponse(reader, 220)
	if err != nil {
		return status, err
	}
	if err := sendCommand(writer, "LHLO benchmark"); err != nil {
		return 0, err
	}
	status, err = expectResponse(reader, 250)
	if err != nil {
		return status, err
	}
	benchmarkHeader := benchmarkIDHeader(runID, sequence)
	wireBytes := len(benchmarkHeader) + len(message)
	if !bytes.HasSuffix(message, []byte("\r\n")) {
		wireBytes += 2
	}
	if err := sendCommand(writer, fmt.Sprintf("MAIL FROM:<benchmark@example.net> SIZE=%d", wireBytes)); err != nil {
		return 0, err
	}
	status, err = expectResponse(reader, 250)
	if err != nil {
		return status, err
	}
	if err := sendCommand(writer, "RCPT TO:<"+recipient+">"); err != nil {
		return 0, err
	}
	status, err = expectResponse(reader, 250)
	if err != nil {
		return status, err
	}
	if err := sendCommand(writer, "DATA"); err != nil {
		return 0, err
	}
	status, err = expectResponse(reader, 354)
	if err != nil {
		return status, err
	}
	if _, err := writer.Write(benchmarkHeader); err != nil {
		return 0, err
	}
	if _, err := writer.Write(message); err != nil {
		return 0, err
	}
	if !bytes.HasSuffix(message, []byte("\r\n")) {
		if _, err := writer.WriteString("\r\n"); err != nil {
			return 0, err
		}
	}
	if _, err := writer.WriteString(".\r\n"); err != nil {
		return 0, err
	}
	if err := writer.Flush(); err != nil {
		return 0, err
	}
	status, err = expectResponse(reader, 250)
	if err != nil {
		return status, err
	}
	if sendCommand(writer, "QUIT") == nil {
		_, _ = expectResponse(reader, 221)
	}
	return status, nil
}

func sendCommand(writer *bufio.Writer, command string) error {
	if _, err := writer.WriteString(command + "\r\n"); err != nil {
		return err
	}
	return writer.Flush()
}

func expectResponse(reader *bufio.Reader, expected int) (int, error) {
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return 0, err
		}
		if len(line) < 4 {
			return 0, errors.New("LMTP response is too short")
		}
		status, err := strconv.Atoi(line[:3])
		if err != nil {
			return 0, errors.New("LMTP response status is invalid")
		}
		if status != expected {
			return status, fmt.Errorf("LMTP status %d, expected %d", status, expected)
		}
		if line[3] == ' ' {
			return status, nil
		}
		if line[3] != '-' {
			return status, errors.New("LMTP response separator is invalid")
		}
	}
}

func classifyError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "error_timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "error_canceled"
	}
	var netError net.Error
	if errors.As(err, &netError) {
		return "error_network"
	}
	return "error_protocol"
}

func minDuration(first, second time.Duration) time.Duration {
	if first < second {
		return first
	}
	return second
}
