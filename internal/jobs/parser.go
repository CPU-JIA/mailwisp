// Package jobs runs bounded background work backed by durable PostgreSQL state.
package jobs

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"sync"
	"time"

	"mailwisp/internal/mail"
	"mailwisp/internal/message"
)

var (
	// ErrStaleParseClaim indicates that a lease token no longer owns its content row.
	ErrStaleParseClaim = errors.New("stale content parse claim")
)

// ParseErrorCode is a stable, non-secret parser outcome stored in PostgreSQL.
type ParseErrorCode string

const (
	ParseErrorMessageTooLarge        ParseErrorCode = "message_too_large"
	ParseErrorHeaderTooLarge         ParseErrorCode = "header_too_large"
	ParseErrorTooManyHeaders         ParseErrorCode = "too_many_headers"
	ParseErrorTooManyParts           ParseErrorCode = "too_many_parts"
	ParseErrorNestingTooDeep         ParseErrorCode = "nesting_too_deep"
	ParseErrorPartTooLarge           ParseErrorCode = "part_too_large"
	ParseErrorDecodedContentTooLarge ParseErrorCode = "decoded_content_too_large"
	ParseErrorInvalidMIME            ParseErrorCode = "invalid_mime"
	ParseErrorContentUnavailable     ParseErrorCode = "content_unavailable"
	ParseErrorContentCorrupt         ParseErrorCode = "content_corrupt"
	ParseErrorTimeout                ParseErrorCode = "parse_timeout"
)

// ParseClaim is one fenced lease over a content-addressed Raw MIME object.
type ParseClaim struct {
	Content    message.ContentRef
	LeaseToken string
	Attempt    int
}

// ParseQueue persists claims and outcomes for content-level parsing.
type ParseQueue interface {
	ClaimContent(context.Context, time.Duration) (ParseClaim, bool, error)
	CompleteContent(context.Context, ParseClaim, int, mail.ParsedMessage, time.Time) error
	RetryContent(context.Context, ParseClaim, ParseErrorCode, time.Time) error
	FailContent(context.Context, ParseClaim, ParseErrorCode, time.Time) error
	ReleaseContent(context.Context, ParseClaim, time.Time) error
}

// RawSource opens immutable Raw MIME by content reference.
type RawSource interface {
	OpenRaw(context.Context, message.ContentRef) (io.ReadCloser, error)
}

// MIMEParser parses untrusted Raw MIME behind explicit resource limits.
type MIMEParser interface {
	Parse(context.Context, io.Reader) (mail.ParsedMessage, error)
}

// ParserOptions configures bounded Parser Worker concurrency and retry behavior.
type ParserOptions struct {
	Workers       int
	PollInterval  time.Duration
	ParseTimeout  time.Duration
	LeaseDuration time.Duration
	MaxAttempts   int
	RetryBase     time.Duration
	RetryMax      time.Duration
}

// ParserWorker drains the durable content parse queue with bounded concurrency.
type ParserWorker struct {
	queue   ParseQueue
	source  RawSource
	parser  MIMEParser
	logger  *slog.Logger
	options ParserOptions
	wake    chan struct{}
	now     func() time.Time
}

// NewParserWorker constructs a durable Parser Worker.
func NewParserWorker(queue ParseQueue, source RawSource, parser MIMEParser, logger *slog.Logger, options ParserOptions) (*ParserWorker, error) {
	if queue == nil {
		return nil, errors.New("content parse queue is required")
	}
	if source == nil {
		return nil, errors.New("raw content source is required")
	}
	if parser == nil {
		return nil, errors.New("MIME parser is required")
	}
	if logger == nil {
		return nil, errors.New("parser worker logger is required")
	}
	if options.Workers <= 0 || options.Workers > 64 {
		return nil, errors.New("parser worker count must be between 1 and 64")
	}
	if options.PollInterval <= 0 || options.ParseTimeout <= 0 || options.LeaseDuration <= 0 {
		return nil, errors.New("parser worker durations must be positive")
	}
	if options.LeaseDuration <= options.ParseTimeout {
		return nil, errors.New("parser lease duration must exceed parse timeout")
	}
	if options.MaxAttempts <= 0 || options.MaxAttempts > 100 {
		return nil, errors.New("parser max attempts must be between 1 and 100")
	}
	if options.RetryBase <= 0 || options.RetryMax < options.RetryBase {
		return nil, errors.New("parser retry bounds are invalid")
	}
	return &ParserWorker{
		queue: queue, source: source, parser: parser, logger: logger, options: options,
		wake: make(chan struct{}, 1),
		now:  time.Now,
	}, nil
}

// Notify wakes one worker after a durable delivery. Notifications are hints;
// PostgreSQL remains the queue and polling recovers missed notifications.
func (w *ParserWorker) Notify() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Run owns all Parser Worker goroutines until the context is canceled.
func (w *ParserWorker) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("parser worker context is required")
	}
	var workers sync.WaitGroup
	workers.Add(w.options.Workers)
	for workerID := range w.options.Workers {
		go func() {
			defer workers.Done()
			w.runWorker(ctx, workerID)
		}()
	}
	workers.Wait()
	return nil
}

func (w *ParserWorker) runWorker(ctx context.Context, workerID int) {
	for {
		worked, err := w.processNext(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			w.logger.Error("content parser worker iteration failed", "worker_id", workerID, "error", err)
		}
		if ctx.Err() != nil {
			return
		}
		if worked {
			continue
		}

		timer := time.NewTimer(w.idleDelay())
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-w.wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (w *ParserWorker) processNext(ctx context.Context) (bool, error) {
	claim, found, err := w.queue.ClaimContent(ctx, w.options.LeaseDuration)
	if err != nil || !found {
		return false, err
	}

	parseContext, cancel := context.WithTimeout(ctx, w.options.ParseTimeout)
	parsed, parseErr := w.parseClaim(parseContext, claim)
	parseContextErr := parseContext.Err()
	cancel()

	if ctx.Err() != nil {
		w.releaseAfterCancellation(claim)
		return true, ctx.Err()
	}
	if parseErr == nil {
		finishContext, finishCancel := context.WithTimeout(ctx, 5*time.Second)
		defer finishCancel()
		if err := w.queue.CompleteContent(finishContext, claim, mail.ParserRevision, parsed, w.now().UTC()); err != nil {
			return true, fmt.Errorf("complete parsed content: %w", err)
		}
		w.logger.Debug("content parsed", "content_key", claim.Content.Key, "attempt", claim.Attempt)
		return true, nil
	}

	code, permanent := classifyParseError(parseErr, parseContextErr)
	finishContext, finishCancel := context.WithTimeout(ctx, 5*time.Second)
	defer finishCancel()
	if permanent || claim.Attempt >= w.options.MaxAttempts {
		if err := w.queue.FailContent(finishContext, claim, code, w.now().UTC()); err != nil {
			return true, fmt.Errorf("record content parse failure: %w", err)
		}
		w.logger.Warn("content parsing failed", "content_key", claim.Content.Key, "attempt", claim.Attempt, "code", code)
		return true, nil
	}
	availableAt := w.now().UTC().Add(w.retryDelay(claim.Attempt))
	if err := w.queue.RetryContent(finishContext, claim, code, availableAt); err != nil {
		return true, fmt.Errorf("schedule content parse retry: %w", err)
	}
	w.logger.Warn("content parsing scheduled for retry", "content_key", claim.Content.Key, "attempt", claim.Attempt, "code", code, "available_at", availableAt)
	return true, nil
}

func (w *ParserWorker) idleDelay() time.Duration {
	jitterLimit := w.options.PollInterval / 5
	if jitterLimit <= 0 {
		return w.options.PollInterval
	}
	jitter, err := cryptorand.Int(cryptorand.Reader, big.NewInt(int64(jitterLimit)+1))
	if err != nil {
		return w.options.PollInterval
	}
	return w.options.PollInterval + time.Duration(jitter.Int64())
}

func (w *ParserWorker) parseClaim(ctx context.Context, claim ParseClaim) (mail.ParsedMessage, error) {
	source, err := w.source.OpenRaw(ctx, claim.Content)
	if err != nil {
		return mail.ParsedMessage{}, fmt.Errorf("open Raw MIME: %w", &rawSourceError{err: err})
	}
	verifier := newRawVerifier(&rawSourceReader{source: source}, claim.Content)
	parsed, parseErr := w.parser.Parse(ctx, verifier)
	if parseErr == nil {
		parseErr = verifier.finish(ctx)
	}
	closeErr := source.Close()
	if parseErr != nil {
		return mail.ParsedMessage{}, parseErr
	}
	if closeErr != nil {
		return mail.ParsedMessage{}, fmt.Errorf("close Raw MIME: %w", &rawSourceError{err: closeErr})
	}
	return parsed, nil
}

func (w *ParserWorker) releaseAfterCancellation(claim ParseClaim) {
	releaseContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.queue.ReleaseContent(releaseContext, claim, w.now().UTC()); err != nil && !errors.Is(err, ErrStaleParseClaim) {
		w.logger.Error("release canceled content parse claim", "error", err)
	}
}

func (w *ParserWorker) retryDelay(attempt int) time.Duration {
	delay := w.options.RetryBase
	for current := 1; current < attempt && delay < w.options.RetryMax; current++ {
		if delay > w.options.RetryMax/2 {
			return w.options.RetryMax
		}
		delay *= 2
	}
	if delay > w.options.RetryMax {
		return w.options.RetryMax
	}
	return delay
}

func classifyParseError(err, contextErr error) (ParseErrorCode, bool) {
	if errors.Is(contextErr, context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return ParseErrorTimeout, false
	}
	switch {
	case errors.Is(err, mail.ErrMessageTooLarge):
		return ParseErrorMessageTooLarge, true
	case errors.Is(err, mail.ErrHeaderTooLarge):
		return ParseErrorHeaderTooLarge, true
	case errors.Is(err, mail.ErrTooManyHeaders):
		return ParseErrorTooManyHeaders, true
	case errors.Is(err, mail.ErrTooManyParts):
		return ParseErrorTooManyParts, true
	case errors.Is(err, mail.ErrNestingTooDeep):
		return ParseErrorNestingTooDeep, true
	case errors.Is(err, mail.ErrPartTooLarge):
		return ParseErrorPartTooLarge, true
	case errors.Is(err, mail.ErrDecodedContentTooLarge):
		return ParseErrorDecodedContentTooLarge, true
	}
	var sourceError *rawSourceError
	if errors.As(err, &sourceError) {
		return ParseErrorContentUnavailable, false
	}
	var corruptError *rawContentCorruptError
	if errors.As(err, &corruptError) {
		return ParseErrorContentCorrupt, true
	}
	return ParseErrorInvalidMIME, true
}

type rawSourceError struct {
	err error
}

func (e *rawSourceError) Error() string { return e.err.Error() }
func (e *rawSourceError) Unwrap() error { return e.err }

type rawSourceReader struct {
	source io.Reader
}

func (r *rawSourceReader) Read(buffer []byte) (int, error) {
	read, err := r.source.Read(buffer)
	if err != nil && !errors.Is(err, io.EOF) {
		return read, &rawSourceError{err: err}
	}
	return read, err
}

type rawContentCorruptError struct {
	detail string
}

func (e *rawContentCorruptError) Error() string { return e.detail }

type rawVerifier struct {
	source   io.Reader
	expected message.ContentRef
	hash     hashWriter
	read     int64
	verified bool
	err      error
}

type hashWriter interface {
	Write([]byte) (int, error)
	Sum([]byte) []byte
}

func newRawVerifier(source io.Reader, expected message.ContentRef) *rawVerifier {
	return &rawVerifier{source: source, expected: expected, hash: sha256.New()}
}

func (r *rawVerifier) Read(buffer []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	read, err := r.source.Read(buffer)
	if read > 0 {
		r.read += int64(read)
		_, _ = r.hash.Write(buffer[:read])
	}
	if errors.Is(err, io.EOF) {
		r.err = r.verify()
		if r.err != nil {
			return read, r.err
		}
	}
	return read, err
}

func (r *rawVerifier) finish(ctx context.Context) error {
	if r.verified {
		return r.err
	}
	buffer := make([]byte, 32<<10)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, err := r.Read(buffer)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func (r *rawVerifier) verify() error {
	if r.verified {
		return r.err
	}
	r.verified = true
	if r.read != r.expected.SizeBytes {
		return &rawContentCorruptError{detail: fmt.Sprintf("Raw MIME size %d does not match expected size %d", r.read, r.expected.SizeBytes)}
	}
	digest := hex.EncodeToString(r.hash.Sum(nil))
	if r.expected.Key != "sha256/"+digest {
		return &rawContentCorruptError{detail: "Raw MIME digest does not match its content key"}
	}
	return nil
}
