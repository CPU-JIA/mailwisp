package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"mailwisp/internal/config"
	"mailwisp/internal/contentstore"
	"mailwisp/internal/postgres"
)

// ReconcileContent runs one exclusive, bounded content consistency scan.
func ReconcileContent(ctx context.Context, cfg config.Config, logger *slog.Logger, repairOrphans bool) (returnError error) {
	if logger == nil {
		return errors.New("logger is required")
	}
	pool, err := openPostgresPool(ctx, cfg.Postgres)
	if err != nil {
		return err
	}
	defer pool.Close()

	startupContext, startupCancel := context.WithTimeout(ctx, cfg.Postgres.ConnectTimeout)
	defer startupCancel()
	repository, err := postgres.NewDeliveryRepository(pool)
	if err != nil {
		return fmt.Errorf("create delivery repository: %w", err)
	}
	if err := repository.Ready(startupContext); err != nil {
		return fmt.Errorf("verify postgres readiness: %w", err)
	}
	lease, err := postgres.TryAcquireMaintenanceLease(startupContext, cfg.Postgres.DSN)
	if err != nil {
		return fmt.Errorf("acquire exclusive maintenance lease: %w", err)
	}
	defer func() {
		releaseContext, releaseCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer releaseCancel()
		if err := lease.Release(releaseContext); err != nil {
			returnError = errors.Join(returnError, fmt.Errorf("release exclusive maintenance lease: %w", err))
		}
	}()
	startupCancel()

	store, err := contentstore.Open(cfg.Content.Root, contentstore.Options{MaxBytes: cfg.Content.MaxBytes})
	if err != nil {
		return fmt.Errorf("open content store: %w", err)
	}
	summary, err := store.Reconcile(ctx, repository, contentstore.ReconcileOptions{
		BatchSize:     contentstore.DefaultReconcileBatchSize,
		RepairOrphans: repairOrphans,
	}, func(finding contentstore.Finding) error {
		logger.Warn("content consistency finding",
			"kind", finding.Kind,
			"content_key", finding.Content.Key,
			"size_bytes", finding.Content.SizeBytes,
			"repaired", finding.Repaired,
		)
		return nil
	})
	if err != nil {
		return fmt.Errorf("reconcile content storage: %w", err)
	}
	logger.Info("content reconciliation complete",
		"database_refs", summary.DatabaseRefs,
		"stored_objects", summary.StoredObjects,
		"missing", summary.Missing,
		"corrupt", summary.Corrupt,
		"orphans", summary.Orphans,
		"repaired_orphans", summary.RepairedOrphans,
		"unresolved", summary.Unresolved(),
	)
	if summary.Unresolved() > 0 {
		return fmt.Errorf("%w: %d unresolved finding(s)", contentstore.ErrInconsistentContent, summary.Unresolved())
	}
	return nil
}
