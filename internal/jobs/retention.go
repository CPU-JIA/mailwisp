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
}

// RetentionContentStore removes Raw MIME after its final database reference disappears.
type RetentionContentStore interface {
	Delete(message.ContentRef) error
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
}

// RetentionMetrics observes fixed sweep outcomes and bounded counts.
type RetentionMetrics interface {
	ObserveRetention(result string, inboxes, content int)
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
	if repository == nil || content == nil || logger == nil {
		return nil, errors.New("retention repository, content store, and logger are required")
	}
	if options.BatchSize <= 0 || options.BatchSize > 1000 {
		return nil, errors.New("retention batch size must be between 1 and 1000")
	}
	if options.Interval <= 0 || options.Timeout <= 0 || options.Timeout >= options.Interval {
		return nil, errors.New("retention timeout must be positive and shorter than interval")
	}
	return &Retention{repository: repository, content: content, logger: logger, options: options}, nil
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

// Sweep deletes all currently expired Inboxes through bounded transactions.
func (r *Retention) Sweep(ctx context.Context) (RetentionSummary, error) {
	var summary RetentionSummary
	for {
		deleted, refs, err := r.repository.CleanupExpiredInboxes(ctx, r.options.BatchSize)
		if err != nil {
			return summary, err
		}
		summary.InboxesDeleted += deleted
		for _, ref := range refs {
			if err := r.content.Delete(ref); err != nil {
				return summary, fmt.Errorf("delete expired Raw MIME %q: %w", ref.Key, err)
			}
			summary.ContentDeleted++
		}
		if deleted < r.options.BatchSize {
			return summary, nil
		}
		if err := ctx.Err(); err != nil {
			return summary, err
		}
	}
}

func (r *Retention) runSweep(ctx context.Context) {
	sweepContext, cancel := context.WithTimeout(ctx, r.options.Timeout)
	defer cancel()
	summary, err := r.Sweep(sweepContext)
	if err != nil {
		if r.metrics != nil {
			r.metrics.ObserveRetention("error", summary.InboxesDeleted, summary.ContentDeleted)
		}
		r.logger.ErrorContext(ctx, "retention sweep failed", "error", err)
		return
	}
	if r.metrics != nil {
		r.metrics.ObserveRetention("success", summary.InboxesDeleted, summary.ContentDeleted)
	}
	r.logger.InfoContext(ctx, "retention sweep complete", "inboxes_deleted", summary.InboxesDeleted, "content_deleted", summary.ContentDeleted)
}
