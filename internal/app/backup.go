package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	backuppkg "mailwisp/internal/backup"
	"mailwisp/internal/config"
	"mailwisp/internal/contentstore"
	"mailwisp/internal/postgres"
)

// CreateBackup creates one verified PostgreSQL and content bundle while
// deliveries are excluded by the maintenance lease.
func CreateBackup(ctx context.Context, cfg config.Config, logger *slog.Logger, destination string) (manifest backuppkg.Manifest, returnError error) {
	if logger == nil {
		return backuppkg.Manifest{}, errors.New("logger is required")
	}
	pool, err := openPostgresPool(ctx, cfg.Postgres)
	if err != nil {
		return backuppkg.Manifest{}, err
	}
	defer pool.Close()

	startupContext, startupCancel := context.WithTimeout(ctx, cfg.Postgres.ConnectTimeout)
	defer startupCancel()
	tool, err := postgres.NewBackupTool(cfg.Postgres.DSN, pool)
	if err != nil {
		return backuppkg.Manifest{}, fmt.Errorf("create postgres backup tool: %w", err)
	}
	if err := tool.Ready(startupContext); err != nil {
		return backuppkg.Manifest{}, fmt.Errorf("verify postgres readiness: %w", err)
	}
	lease, err := postgres.TryAcquireMaintenanceLease(startupContext, cfg.Postgres.DSN)
	if err != nil {
		return backuppkg.Manifest{}, fmt.Errorf("acquire exclusive maintenance lease: %w", err)
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
		return backuppkg.Manifest{}, fmt.Errorf("open content store: %w", err)
	}
	summary, err := store.Reconcile(ctx, tool, contentstore.ReconcileOptions{
		BatchSize: contentstore.DefaultReconcileBatchSize,
	}, func(finding contentstore.Finding) error {
		logger.Warn("backup blocked by content consistency finding",
			"kind", finding.Kind,
			"content_key", finding.Content.Key,
			"size_bytes", finding.Content.SizeBytes,
		)
		return nil
	})
	if err != nil {
		return backuppkg.Manifest{}, fmt.Errorf("reconcile content before backup: %w", err)
	}
	if summary.Unresolved() != 0 {
		return backuppkg.Manifest{}, fmt.Errorf("%w: backup has %d unresolved finding(s)", contentstore.ErrInconsistentContent, summary.Unresolved())
	}

	manifest, err = backuppkg.Create(ctx, destination, time.Now().UTC(), tool, store)
	if err != nil {
		return backuppkg.Manifest{}, fmt.Errorf("create backup bundle: %w", err)
	}
	logger.Info("backup bundle complete",
		"destination", destination,
		"created_at", manifest.CreatedAt,
		"database_bytes", manifest.Database.SizeBytes,
		"content_objects", manifest.Content.Objects,
		"content_bytes", manifest.Content.UncompressedBytes,
	)
	return manifest, nil
}

// RestoreBackup restores one verified bundle into an empty PostgreSQL database
// and a content root that does not yet exist.
func RestoreBackup(ctx context.Context, cfg config.Config, logger *slog.Logger, bundleRoot string) (manifest backuppkg.Manifest, returnError error) {
	if logger == nil {
		return backuppkg.Manifest{}, errors.New("logger is required")
	}
	pool, err := openPostgresPool(ctx, cfg.Postgres)
	if err != nil {
		return backuppkg.Manifest{}, err
	}
	defer pool.Close()

	startupContext, startupCancel := context.WithTimeout(ctx, cfg.Postgres.ConnectTimeout)
	defer startupCancel()
	lease, err := postgres.TryAcquireMaintenanceLease(startupContext, cfg.Postgres.DSN)
	if err != nil {
		return backuppkg.Manifest{}, fmt.Errorf("acquire exclusive maintenance lease: %w", err)
	}
	defer func() {
		releaseContext, releaseCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer releaseCancel()
		if err := lease.Release(releaseContext); err != nil {
			returnError = errors.Join(returnError, fmt.Errorf("release exclusive maintenance lease: %w", err))
		}
	}()
	startupCancel()

	tool, err := postgres.NewBackupTool(cfg.Postgres.DSN, pool)
	if err != nil {
		return backuppkg.Manifest{}, fmt.Errorf("create postgres restore tool: %w", err)
	}
	manifest, err = backuppkg.Restore(ctx, bundleRoot, cfg.Content.Root, tool)
	if err != nil {
		return backuppkg.Manifest{}, fmt.Errorf("restore backup bundle: %w", err)
	}
	logger.Info("backup restore complete",
		"bundle", bundleRoot,
		"content_root", cfg.Content.Root,
		"created_at", manifest.CreatedAt,
		"content_objects", manifest.Content.Objects,
	)
	return manifest, nil
}
