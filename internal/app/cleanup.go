package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"mailwisp/internal/config"
	"mailwisp/internal/contentstore"
	"mailwisp/internal/postgres"
)

// CleanupSummary reports one complete bounded retention sweep.
type CleanupSummary struct {
	InboxesDeleted int
	ContentDeleted int
}

// CleanupExpired deletes expired Inboxes in short transactions and removes
// Raw MIME objects after their final database reference disappears.
func CleanupExpired(ctx context.Context, cfg config.Config, logger *slog.Logger) (CleanupSummary, error) {
	if logger == nil {
		return CleanupSummary{}, errors.New("logger is required")
	}
	pool, err := openPostgresPool(ctx, cfg.Postgres)
	if err != nil {
		return CleanupSummary{}, err
	}
	defer pool.Close()
	repository, err := postgres.NewMailboxRepository(pool)
	if err != nil {
		return CleanupSummary{}, fmt.Errorf("create cleanup repository: %w", err)
	}
	store, err := contentstore.Open(cfg.Content.Root, contentstore.Options{MaxBytes: cfg.Content.MaxBytes})
	if err != nil {
		return CleanupSummary{}, fmt.Errorf("open cleanup content store: %w", err)
	}
	var summary CleanupSummary
	for {
		deleted, refs, err := repository.CleanupExpiredInboxes(ctx, cfg.Cleanup.BatchSize)
		if err != nil {
			return summary, err
		}
		summary.InboxesDeleted += deleted
		for _, ref := range refs {
			if err := store.Delete(ref); err != nil {
				return summary, fmt.Errorf("delete expired Raw MIME %q: %w", ref.Key, err)
			}
			summary.ContentDeleted++
		}
		if deleted < cfg.Cleanup.BatchSize {
			break
		}
		if err := ctx.Err(); err != nil {
			return summary, err
		}
	}
	logger.Info("expired Inbox cleanup complete", "inboxes_deleted", summary.InboxesDeleted, "content_deleted", summary.ContentDeleted)
	return summary, nil
}
