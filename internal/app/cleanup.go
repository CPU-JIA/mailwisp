package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"mailwisp/internal/config"
	"mailwisp/internal/contentstore"
	"mailwisp/internal/jobs"
	"mailwisp/internal/postgres"
)

// CleanupSummary reports one complete bounded retention sweep.
type CleanupSummary struct {
	InboxesDeleted int
	ContentDeleted int
	ContentPending int
}

// CleanupExpired deletes expired Inboxes in short transactions and removes
// Raw MIME objects after their final database reference disappears.
func CleanupExpired(ctx context.Context, cfg config.Config, logger *slog.Logger) (summary CleanupSummary, returnError error) {
	if logger == nil {
		return CleanupSummary{}, errors.New("logger is required")
	}
	pool, err := openPostgresPool(ctx, cfg.Postgres)
	if err != nil {
		return CleanupSummary{}, err
	}
	defer pool.Close()
	startupContext, startupCancel := context.WithTimeout(ctx, cfg.Postgres.ConnectTimeout)
	defer startupCancel()
	readiness, err := postgres.NewDeliveryRepository(pool)
	if err != nil {
		return CleanupSummary{}, fmt.Errorf("create cleanup readiness repository: %w", err)
	}
	if err := readiness.Ready(startupContext); err != nil {
		return CleanupSummary{}, fmt.Errorf("verify postgres readiness: %w", err)
	}
	lease, err := postgres.TryAcquireMaintenanceLease(startupContext, cfg.Postgres.DSN)
	if err != nil {
		return CleanupSummary{}, fmt.Errorf("acquire exclusive maintenance lease: %w", err)
	}
	defer func() {
		releaseContext, releaseCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer releaseCancel()
		if err := lease.Release(releaseContext); err != nil {
			returnError = errors.Join(returnError, fmt.Errorf("release exclusive maintenance lease: %w", err))
		}
	}()
	startupCancel()

	repository, err := postgres.NewMailboxRepository(pool)
	if err != nil {
		return CleanupSummary{}, fmt.Errorf("create cleanup repository: %w", err)
	}
	store, err := contentstore.Open(cfg.Content.Root, contentstore.Options{MaxBytes: cfg.Content.MaxBytes, MinFreeBytes: cfg.Content.MinFreeBytes})
	if err != nil {
		return CleanupSummary{}, fmt.Errorf("open cleanup content store: %w", err)
	}
	sweeper, err := jobs.NewRetentionSweeper(repository, store, logger, cfg.Cleanup.BatchSize)
	if err != nil {
		return CleanupSummary{}, fmt.Errorf("create cleanup sweep: %w", err)
	}
	result, err := sweeper.Sweep(ctx)
	summary = CleanupSummary{InboxesDeleted: result.InboxesDeleted, ContentDeleted: result.ContentDeleted, ContentPending: result.ContentPending}
	if err != nil {
		return summary, err
	}
	logger.Info("expired Inbox cleanup complete", "inboxes_deleted", summary.InboxesDeleted, "content_deleted", summary.ContentDeleted, "content_pending", summary.ContentPending)
	return summary, nil
}
