package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"mailwisp/internal/message"
)

// RetentionRepository deletes one bounded batch of expired Inboxes.
type RetentionRepository interface {
	CleanupExpiredInboxes(context.Context, int) (int, []message.ContentRef, error)
	ListContentDeletions(context.Context, int) ([]message.ContentDeletion, error)
	ContentReferenced(context.Context, string) (bool, error)
	AcknowledgeContentDeletion(context.Context, string, int64) (bool, error)
	CountContentDeletions(context.Context) (int, error)
}

// RetentionContentStore removes Raw MIME behind a delivery/deletion lifecycle fence.
type RetentionContentStore interface {
	DeleteUnreferenced(context.Context, message.ContentRef, func(context.Context, string) (bool, error)) (bool, error)
}

// RetentionOptions defines the bounded schedule and per-sweep deadline.
type RetentionOptions struct {
	BatchSize int
	Interval  time.Duration
	Timeout   time.Duration
}

// RetentionSummary reports one complete sweep.
type RetentionSummary struct {
	InboxesDeleted int
	ContentDeleted int
	ContentPending int
}

// RetentionMetrics observes fixed sweep outcomes and bounded counts.
type RetentionMetrics interface {
	ObserveRetention(result string, inboxes, content, pending int)
}

// Retention runs periodic Inbox expiration without making one failed sweep fatal to the service.
type Retention struct {
	repository RetentionRepository
	content    RetentionContentStore
	logger     *slog.Logger
	options    RetentionOptions
	metrics    RetentionMetrics
}

// SetMetrics enables retention sweep observations.
func (r *Retention) SetMetrics(metrics RetentionMetrics) { r.metrics = metrics }

// NewRetention constructs a bounded retention job.
func NewRetention(repository RetentionRepository, content RetentionContentStore, logger *slog.Logger, options RetentionOptions) (*Retention, error) {
	retention, err := NewRetentionSweeper(repository, content, logger, options.BatchSize)
	if err != nil {
		return nil, err
	}
	if options.Interval <= 0 || options.Timeout <= 0 || options.Timeout >= options.Interval {
		return nil, errors.New("retention timeout must be positive and shorter than interval")
	}
	retention.options.Interval = options.Interval
	retention.options.Timeout = options.Timeout
	return retention, nil
}

// NewRetentionSweeper constructs the shared one-shot cleanup implementation.
func NewRetentionSweeper(repository RetentionRepository, content RetentionContentStore, logger *slog.Logger, batchSize int) (*Retention, error) {
	if repository == nil || content == nil || logger == nil {
		return nil, errors.New("retention repository, content store, and logger are required")
	}
	if batchSize <= 0 || batchSize > 1000 {
		return nil, errors.New("retention batch size must be between 1 and 1000")
	}
	return &Retention{repository: repository, content: content, logger: logger, options: RetentionOptions{BatchSize: batchSize}}, nil
}

// Run executes an immediate sweep and then repeats until cancellation.
func (r *Retention) Run(ctx context.Context) error {
	r.runSweep(ctx)
	ticker := time.NewTicker(r.options.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			r.runSweep(ctx)
		}
	}
}

// Sweep deletes expired Inboxes through bounded transactions, then processes
// one bounded page of durable physical-deletion work. A failed object remains
// queued for the next sweep while unrelated objects can still make progress.
func (r *Retention) Sweep(ctx context.Context) (RetentionSummary, error) {
	var summary RetentionSummary
	for {
		deleted, _, err := r.repository.CleanupExpiredInboxes(ctx, r.options.BatchSize)
		if err != nil {
			return summary, err
		}
		summary.InboxesDeleted += deleted
		if deleted < r.options.BatchSize {
			break
		}
		if err := ctx.Err(); err != nil {
			return summary, err
		}
	}

	deletions, err := r.repository.ListContentDeletions(ctx, r.options.BatchSize)
	if err != nil {
		return summary, err
	}
	var deletionErrors []error
	for _, deletion := range deletions {
		deleted, err := r.content.DeleteUnreferenced(ctx, deletion.Content, r.repository.ContentReferenced)
		if err != nil {
			deletionErrors = append(deletionErrors, fmt.Errorf("delete queued Raw MIME %q: %w", deletion.Content.Key, err))
			continue
		}
		acknowledged, err := r.repository.AcknowledgeContentDeletion(ctx, deletion.Content.Key, deletion.Generation)
		if err != nil {
			deletionErrors = append(deletionErrors, fmt.Errorf("acknowledge queued Raw MIME %q: %w", deletion.Content.Key, err))
			continue
		}
		if deleted && acknowledged {
			summary.ContentDeleted++
		}
	}
	pending, countErr := r.repository.CountContentDeletions(ctx)
	if countErr != nil {
		deletionErrors = append(deletionErrors, fmt.Errorf("count queued Raw MIME deletions: %w", countErr))
	} else {
		summary.ContentPending = pending
	}
	return summary, errors.Join(deletionErrors...)
}

func (r *Retention) runSweep(ctx context.Context) {
	sweepContext, cancel := context.WithTimeout(ctx, r.options.Timeout)
	defer cancel()
	summary, err := r.Sweep(sweepContext)
	if err != nil {
		if r.metrics != nil {
			r.metrics.ObserveRetention("error", summary.InboxesDeleted, summary.ContentDeleted, summary.ContentPending)
		}
		r.logger.ErrorContext(ctx, "retention sweep failed", "error", err)
		return
	}
	if r.metrics != nil {
		r.metrics.ObserveRetention("success", summary.InboxesDeleted, summary.ContentDeleted, summary.ContentPending)
	}
	r.logger.InfoContext(ctx, "retention sweep complete", "inboxes_deleted", summary.InboxesDeleted, "content_deleted", summary.ContentDeleted, "content_pending", summary.ContentPending)
}
