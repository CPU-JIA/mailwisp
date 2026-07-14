package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"mailwisp/internal/contentstore"
)

// DatabaseRestorer restores one custom-format dump into an empty database and
// exposes the resulting content catalog for reconciliation.
type DatabaseRestorer interface {
	Empty(context.Context) (bool, error)
	Restore(context.Context, io.Reader) (DatabaseMetadata, error)
	contentstore.ContentCatalog
}

// Restore verifies a bundle before mutation, installs content first, restores
// PostgreSQL in one transaction, and then proves cross-store consistency.
func Restore(ctx context.Context, bundleRoot, targetContentRoot string, database DatabaseRestorer) (Manifest, error) {
	if database == nil {
		return Manifest{}, errors.New("database restorer is required")
	}
	bundle, verified, err := openVerifiedBundle(ctx, bundleRoot)
	if err != nil {
		return Manifest{}, err
	}
	defer bundle.Close()
	contentArchive, err := openVerifiedComponent(ctx, bundle, verified.Manifest.Content.ComponentManifest)
	if err != nil {
		return Manifest{}, fmt.Errorf("open verified content archive: %w", err)
	}
	defer contentArchive.Close()
	databaseDump, err := openVerifiedComponent(ctx, bundle, verified.Manifest.Database)
	if err != nil {
		return Manifest{}, fmt.Errorf("open verified database dump: %w", err)
	}
	defer databaseDump.Close()
	empty, err := database.Empty(ctx)
	if err != nil {
		return Manifest{}, fmt.Errorf("inspect restore database: %w", err)
	}
	if !empty {
		return Manifest{}, errors.New("restore database is not empty")
	}
	if strings.TrimSpace(targetContentRoot) == "" {
		return Manifest{}, errors.New("restore content root is required")
	}
	absoluteContentRoot, err := filepath.Abs(targetContentRoot)
	if err != nil {
		return Manifest{}, fmt.Errorf("resolve restore content root: %w", err)
	}
	if _, err := os.Lstat(absoluteContentRoot); err == nil {
		return Manifest{}, errors.New("restore content root already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return Manifest{}, fmt.Errorf("inspect restore content root: %w", err)
	}
	parent := filepath.Dir(absoluteContentRoot)
	parentInfo, err := os.Stat(parent)
	if err != nil {
		return Manifest{}, fmt.Errorf("inspect restore content parent: %w", err)
	}
	if !parentInfo.IsDir() {
		return Manifest{}, errors.New("restore content parent is not a directory")
	}
	stagingRoot, err := partialPath(absoluteContentRoot)
	if err != nil {
		return Manifest{}, err
	}
	_, restoreContentErr := contentstore.RestoreArchive(ctx, stagingRoot, contentArchive, contentstore.ArchiveStats{
		Objects: verified.Manifest.Content.Objects,
		Bytes:   verified.Manifest.Content.UncompressedBytes,
	})
	if restoreContentErr != nil {
		return Manifest{}, restoreContentErr
	}
	contentInstalled := false
	defer func() {
		if !contentInstalled {
			_ = os.RemoveAll(stagingRoot)
		}
	}()
	if err := os.Rename(stagingRoot, absoluteContentRoot); err != nil {
		return Manifest{}, fmt.Errorf("install restored content root: %w", err)
	}
	if err := syncDirectory(parent); err != nil {
		_ = os.Rename(absoluteContentRoot, stagingRoot)
		_ = syncDirectory(parent)
		return Manifest{}, fmt.Errorf("sync restored content parent: %w", err)
	}
	contentInstalled = true

	metadata, restoreDatabaseErr := database.Restore(ctx, databaseDump)
	if restoreDatabaseErr != nil {
		cleanupErr := removeRestoredContent(parent, absoluteContentRoot)
		return Manifest{}, errors.Join(restoreDatabaseErr, cleanupErr)
	}
	if err := validateRestoredMetadata(verified.Manifest, metadata); err != nil {
		return Manifest{}, err
	}
	store, err := contentstore.Open(absoluteContentRoot, contentstore.Options{MaxBytes: 1})
	if err != nil {
		return Manifest{}, fmt.Errorf("open restored content store: %w", err)
	}
	summary, err := store.Reconcile(ctx, database, contentstore.ReconcileOptions{BatchSize: contentstore.DefaultReconcileBatchSize}, nil)
	if err != nil {
		return Manifest{}, fmt.Errorf("reconcile restored backup: %w", err)
	}
	if summary.Unresolved() != 0 {
		return Manifest{}, fmt.Errorf("%w: restored backup has %d unresolved finding(s)", contentstore.ErrInconsistentContent, summary.Unresolved())
	}
	return verified.Manifest, nil
}

func removeRestoredContent(parent, root string) error {
	if err := os.RemoveAll(root); err != nil {
		return fmt.Errorf("remove content after database restore failure: %w", err)
	}
	if err := syncDirectory(parent); err != nil {
		return fmt.Errorf("sync content parent after restore failure: %w", err)
	}
	return nil
}

func validateRestoredMetadata(manifest Manifest, restored DatabaseMetadata) error {
	if majorVersion(manifest.PostgreSQL.ServerVersion) == "" || majorVersion(manifest.PostgreSQL.ServerVersion) != majorVersion(restored.ServerVersion) {
		return errors.New("restored PostgreSQL server major does not match backup")
	}
	if majorVersion(manifest.PostgreSQL.DumpVersion) != majorVersion(restored.DumpVersion) || majorVersion(manifest.PostgreSQL.RestoreVersion) != majorVersion(restored.RestoreVersion) {
		return errors.New("restored PostgreSQL tool major does not match backup")
	}
	if restored.MigrationVersion != manifest.PostgreSQL.MigrationVersion {
		return errors.New("restored migration version does not match backup")
	}
	return nil
}

func majorVersion(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		return ""
	}
	if index := strings.IndexByte(version, '.'); index >= 0 {
		version = version[:index]
	}
	for _, character := range version {
		if character < '0' || character > '9' {
			return ""
		}
	}
	return version
}
