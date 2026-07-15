package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"mailwisp/internal/mail"
	"mailwisp/internal/message"
)

func TestParserWorkerCompletesContent(t *testing.T) {
	t.Parallel()

	claim := testParseClaim(1)
	parsedAt := time.Date(2026, 7, 15, 6, 0, 0, 0, time.UTC)
	parsed := mail.ParsedMessage{Subject: "Fast mail", Text: "Zero trace."}
	var completed bool
	queue := &parseQueueStub{
		claim: func(context.Context, time.Duration) (ParseClaim, bool, error) {
			return claim, true, nil
		},
		complete: func(_ context.Context, gotClaim ParseClaim, revision int, got mail.ParsedMessage, gotAt time.Time) error {
			completed = true
			if gotClaim != claim || revision != mail.ParserRevision || got.Subject != parsed.Subject || !gotAt.Equal(parsedAt) {
				t.Fatalf("CompleteContent() = %+v/%d/%+v/%s", gotClaim, revision, got, gotAt)
			}
			return nil
		},
	}
	worker := newTestParserWorker(t, queue, &rawSourceStub{}, &mimeParserStub{parse: func(context.Context, io.Reader) (mail.ParsedMessage, error) {
		return parsed, nil
	}})
	worker.now = func() time.Time { return parsedAt }

	worked, err := worker.processNext(context.Background())
	if err != nil || !worked || !completed {
		t.Fatalf("processNext() = %v, %v, completed=%v", worked, err, completed)
	}
}

func TestParserWorkerRecordsPermanentParserFailure(t *testing.T) {
	t.Parallel()

	claim := testParseClaim(1)
	var failedCode ParseErrorCode
	queue := &parseQueueStub{
		claim: func(context.Context, time.Duration) (ParseClaim, bool, error) { return claim, true, nil },
		fail: func(_ context.Context, _ ParseClaim, code ParseErrorCode, _ time.Time) error {
			failedCode = code
			return nil
		},
	}
	worker := newTestParserWorker(t, queue, &rawSourceStub{}, &mimeParserStub{parse: func(context.Context, io.Reader) (mail.ParsedMessage, error) {
		return mail.ParsedMessage{}, mail.ErrTooManyParts
	}})

	worked, err := worker.processNext(context.Background())
	if err != nil || !worked || failedCode != ParseErrorTooManyParts {
		t.Fatalf("processNext() = %v, %v, failed=%q", worked, err, failedCode)
	}
}

func TestParserWorkerRetriesTransientSourceFailureWithBackoff(t *testing.T) {
	t.Parallel()

	claim := testParseClaim(3)
	now := time.Date(2026, 7, 15, 6, 30, 0, 0, time.UTC)
	var retryCode ParseErrorCode
	var availableAt time.Time
	queue := &parseQueueStub{
		claim: func(context.Context, time.Duration) (ParseClaim, bool, error) { return claim, true, nil },
		retry: func(_ context.Context, _ ParseClaim, code ParseErrorCode, at time.Time) error {
			retryCode, availableAt = code, at
			return nil
		},
	}
	sourceErr := errors.New("storage temporarily unavailable")
	worker := newTestParserWorker(t, queue, &rawSourceStub{open: func(context.Context, message.ContentRef) (io.ReadCloser, error) {
		return nil, sourceErr
	}}, &mimeParserStub{})
	worker.options.MaxAttempts = 5
	worker.now = func() time.Time { return now }

	worked, err := worker.processNext(context.Background())
	if err != nil || !worked {
		t.Fatalf("processNext() = %v, %v", worked, err)
	}
	if retryCode != ParseErrorContentUnavailable || !availableAt.Equal(now.Add(400*time.Millisecond)) {
		t.Fatalf("retry = %q at %s", retryCode, availableAt)
	}
}

func TestParserWorkerFailsTransientErrorAtAttemptLimit(t *testing.T) {
	t.Parallel()

	claim := testParseClaim(3)
	var failedCode ParseErrorCode
	queue := &parseQueueStub{
		claim: func(context.Context, time.Duration) (ParseClaim, bool, error) { return claim, true, nil },
		fail: func(_ context.Context, _ ParseClaim, code ParseErrorCode, _ time.Time) error {
			failedCode = code
			return nil
		},
	}
	worker := newTestParserWorker(t, queue, &rawSourceStub{open: func(context.Context, message.ContentRef) (io.ReadCloser, error) {
		return nil, errors.New("missing")
	}}, &mimeParserStub{})

	worked, err := worker.processNext(context.Background())
	if err != nil || !worked || failedCode != ParseErrorContentUnavailable {
		t.Fatalf("processNext() = %v, %v, failed=%q", worked, err, failedCode)
	}
}

func TestParserWorkerRetriesRawReadFailure(t *testing.T) {
	t.Parallel()

	claim := testParseClaim(1)
	var retryCode ParseErrorCode
	queue := &parseQueueStub{
		claim: func(context.Context, time.Duration) (ParseClaim, bool, error) { return claim, true, nil },
		retry: func(_ context.Context, _ ParseClaim, code ParseErrorCode, _ time.Time) error {
			retryCode = code
			return nil
		},
	}
	worker := newTestParserWorker(t, queue, &rawSourceStub{open: func(context.Context, message.ContentRef) (io.ReadCloser, error) {
		return io.NopCloser(&jobsErrorReader{err: errors.New("read failed")}), nil
	}}, &mimeParserStub{parse: func(_ context.Context, source io.Reader) (mail.ParsedMessage, error) {
		_, err := io.ReadAll(source)
		return mail.ParsedMessage{}, err
	}})
	worked, err := worker.processNext(context.Background())
	if err != nil || !worked || retryCode != ParseErrorContentUnavailable {
		t.Fatalf("processNext() = %v, %v, retry=%q", worked, err, retryCode)
	}
}

func TestParserWorkerRetriesParseTimeout(t *testing.T) {
	t.Parallel()

	claim := testParseClaim(1)
	var retryCode ParseErrorCode
	queue := &parseQueueStub{
		claim: func(context.Context, time.Duration) (ParseClaim, bool, error) { return claim, true, nil },
		retry: func(_ context.Context, _ ParseClaim, code ParseErrorCode, _ time.Time) error {
			retryCode = code
			return nil
		},
	}
	worker := newTestParserWorker(t, queue, &rawSourceStub{}, &mimeParserStub{parse: func(ctx context.Context, _ io.Reader) (mail.ParsedMessage, error) {
		<-ctx.Done()
		return mail.ParsedMessage{}, ctx.Err()
	}})
	worker.options.ParseTimeout = 20 * time.Millisecond
	worked, err := worker.processNext(context.Background())
	if err != nil || !worked || retryCode != ParseErrorTimeout {
		t.Fatalf("processNext() = %v, %v, retry=%q", worked, err, retryCode)
	}
}

func TestParserWorkerRejectsCorruptRawContentBeforeCompletion(t *testing.T) {
	t.Parallel()

	claim := testParseClaim(1)
	claim.Content.SizeBytes++
	var failedCode ParseErrorCode
	queue := &parseQueueStub{
		claim: func(context.Context, time.Duration) (ParseClaim, bool, error) { return claim, true, nil },
		fail: func(_ context.Context, _ ParseClaim, code ParseErrorCode, _ time.Time) error {
			failedCode = code
			return nil
		},
	}
	worker := newTestParserWorker(t, queue, &rawSourceStub{}, &mimeParserStub{})
	worked, err := worker.processNext(context.Background())
	if err != nil || !worked || failedCode != ParseErrorContentCorrupt {
		t.Fatalf("processNext() = %v, %v, failed=%q", worked, err, failedCode)
	}
}

func TestParserWorkerReleasesCanceledClaimWithoutFailure(t *testing.T) {
	t.Parallel()

	claim := testParseClaim(1)
	released := make(chan struct{}, 1)
	queue := &parseQueueStub{
		claim: func(context.Context, time.Duration) (ParseClaim, bool, error) { return claim, true, nil },
		release: func(context.Context, ParseClaim, time.Time) error {
			released <- struct{}{}
			return nil
		},
	}
	worker := newTestParserWorker(t, queue, &rawSourceStub{}, &mimeParserStub{parse: func(ctx context.Context, _ io.Reader) (mail.ParsedMessage, error) {
		<-ctx.Done()
		return mail.ParsedMessage{}, ctx.Err()
	}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := worker.processNext(ctx)
		done <- err
	}()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("processNext() error = %v", err)
	}
	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("canceled claim was not released")
	}
}

func TestParserWorkerBoundsConcurrentParses(t *testing.T) {
	claims := make(chan ParseClaim, 8)
	for attempt := 1; attempt <= 6; attempt++ {
		claims <- testParseClaim(attempt)
	}
	close(claims)
	var completed atomic.Int32
	queue := &parseQueueStub{
		claim: func(ctx context.Context, _ time.Duration) (ParseClaim, bool, error) {
			select {
			case claim, ok := <-claims:
				return claim, ok, nil
			case <-ctx.Done():
				return ParseClaim{}, false, ctx.Err()
			}
		},
		complete: func(context.Context, ParseClaim, int, mail.ParsedMessage, time.Time) error {
			completed.Add(1)
			return nil
		},
	}
	var active atomic.Int32
	var maximum atomic.Int32
	parser := &mimeParserStub{parse: func(context.Context, io.Reader) (mail.ParsedMessage, error) {
		current := active.Add(1)
		for {
			observed := maximum.Load()
			if current <= observed || maximum.CompareAndSwap(observed, current) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		active.Add(-1)
		return mail.ParsedMessage{}, nil
	}}
	worker := newTestParserWorker(t, queue, &rawSourceStub{}, parser)
	worker.options.Workers = 2
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	deadline := time.After(2 * time.Second)
	for completed.Load() != 6 {
		select {
		case <-deadline:
			t.Fatalf("completed = %d, want 6", completed.Load())
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := maximum.Load(); got != 2 {
		t.Fatalf("maximum concurrent parses = %d, want 2", got)
	}
}

func TestNewParserWorkerValidatesDependenciesAndOptions(t *testing.T) {
	t.Parallel()

	validQueue := &parseQueueStub{}
	validSource := &rawSourceStub{}
	validParser := &mimeParserStub{}
	validLogger := slog.New(slog.NewTextHandler(io.Discard, nil))
	validOptions := testParserOptions()
	if _, err := NewParserWorker(validQueue, validSource, validParser, validLogger, validOptions); err != nil {
		t.Fatalf("NewParserWorker(valid) error = %v", err)
	}
	if _, err := NewParserWorker(nil, validSource, validParser, validLogger, validOptions); err == nil {
		t.Fatal("NewParserWorker(nil queue) error = nil")
	}
	invalid := validOptions
	invalid.LeaseDuration = invalid.ParseTimeout
	if _, err := NewParserWorker(validQueue, validSource, validParser, validLogger, invalid); err == nil {
		t.Fatal("NewParserWorker(short lease) error = nil")
	}
}

func newTestParserWorker(t *testing.T, queue ParseQueue, source RawSource, parser MIMEParser) *ParserWorker {
	t.Helper()
	worker, err := NewParserWorker(queue, source, parser, slog.New(slog.NewTextHandler(io.Discard, nil)), testParserOptions())
	if err != nil {
		t.Fatalf("NewParserWorker() error = %v", err)
	}
	return worker
}

func testParserOptions() ParserOptions {
	return ParserOptions{
		Workers:       1,
		PollInterval:  10 * time.Millisecond,
		ParseTimeout:  time.Second,
		LeaseDuration: 2 * time.Second,
		MaxAttempts:   3,
		RetryBase:     100 * time.Millisecond,
		RetryMax:      time.Second,
	}
}

func testParseClaim(attempt int) ParseClaim {
	digest := sha256.Sum256([]byte(testRawContent))
	return ParseClaim{
		Content:    message.ContentRef{Key: "sha256/" + hex.EncodeToString(digest[:]), SizeBytes: int64(len(testRawContent))},
		LeaseToken: uuid.NewString(),
		Attempt:    attempt,
	}
}

type parseQueueStub struct {
	claim    func(context.Context, time.Duration) (ParseClaim, bool, error)
	complete func(context.Context, ParseClaim, int, mail.ParsedMessage, time.Time) error
	retry    func(context.Context, ParseClaim, ParseErrorCode, time.Time) error
	fail     func(context.Context, ParseClaim, ParseErrorCode, time.Time) error
	release  func(context.Context, ParseClaim, time.Time) error
}

func (s *parseQueueStub) ClaimContent(ctx context.Context, lease time.Duration) (ParseClaim, bool, error) {
	if s.claim == nil {
		return ParseClaim{}, false, nil
	}
	return s.claim(ctx, lease)
}

func (s *parseQueueStub) CompleteContent(ctx context.Context, claim ParseClaim, revision int, parsed mail.ParsedMessage, at time.Time) error {
	if s.complete == nil {
		return errors.New("unexpected CompleteContent call")
	}
	return s.complete(ctx, claim, revision, parsed, at)
}

func (s *parseQueueStub) RetryContent(ctx context.Context, claim ParseClaim, code ParseErrorCode, at time.Time) error {
	if s.retry == nil {
		return errors.New("unexpected RetryContent call")
	}
	return s.retry(ctx, claim, code, at)
}

func (s *parseQueueStub) FailContent(ctx context.Context, claim ParseClaim, code ParseErrorCode, at time.Time) error {
	if s.fail == nil {
		return errors.New("unexpected FailContent call")
	}
	return s.fail(ctx, claim, code, at)
}

func (s *parseQueueStub) ReleaseContent(ctx context.Context, claim ParseClaim, at time.Time) error {
	if s.release == nil {
		return errors.New("unexpected ReleaseContent call")
	}
	return s.release(ctx, claim, at)
}

type rawSourceStub struct {
	open func(context.Context, message.ContentRef) (io.ReadCloser, error)
}

func (s *rawSourceStub) OpenRaw(ctx context.Context, ref message.ContentRef) (io.ReadCloser, error) {
	if s.open != nil {
		return s.open(ctx, ref)
	}
	return io.NopCloser(strings.NewReader(testRawContent)), nil
}

type mimeParserStub struct {
	parse func(context.Context, io.Reader) (mail.ParsedMessage, error)
}

func (s *mimeParserStub) Parse(ctx context.Context, source io.Reader) (mail.ParsedMessage, error) {
	if s.parse != nil {
		return s.parse(ctx, source)
	}
	return mail.ParsedMessage{}, nil
}

const testRawContent = "Subject: test\r\n\r\nbody"

type jobsErrorReader struct {
	err error
}

func (r *jobsErrorReader) Read([]byte) (int, error) {
	return 0, r.err
}
