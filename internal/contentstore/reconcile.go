package contentstore

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"mailwisp/internal/message"
)

const (
	// DefaultReconcileBatchSize bounds memory and PostgreSQL query parameters
	// during a maintenance scan.
	DefaultReconcileBatchSize = 512
)

var (
	// ErrInconsistentContent indicates that a completed scan found unresolved
	// missing, corrupt, or orphaned content.
	ErrInconsistentContent = errors.New("content storage is inconsistent")
)

// ContentCatalog exposes the bounded metadata operations required to compare
// PostgreSQL references with immutable objects.
type ContentCatalog interface {
	WalkContentRefs(context.Context, int, func(message.ContentRef) error) error
	ExistingContentKeys(context.Context, []string) (map[string]struct{}, error)
}

// FindingKind identifies one consistency failure.
type FindingKind string

const (
	// FindingMissing means PostgreSQL references an object that does not exist.
	FindingMissing FindingKind = "missing"
	// FindingCorrupt means stored bytes do not match PostgreSQL metadata.
	FindingCorrupt FindingKind = "corrupt"
	// FindingOrphan means an object exists without PostgreSQL metadata.
	FindingOrphan FindingKind = "orphan"
)

// Finding describes one inconsistency without exposing message content.
type Finding struct {
	Kind     FindingKind
	Content  message.ContentRef
	Repaired bool
}

// ReconcileOptions controls one bounded consistency scan.
type ReconcileOptions struct {
	BatchSize     int
	RepairOrphans bool
}

// ReconcileSummary records completed work and unresolved findings.
type ReconcileSummary struct {
	DatabaseRefs    int64
	StoredObjects   int64
	Missing         int64
	Corrupt         int64
	Orphans         int64
	RepairedOrphans int64
}

// Unresolved returns the number of findings that still require action.
func (s ReconcileSummary) Unresolved() int64 {
	return s.Missing + s.Corrupt + s.Orphans - s.RepairedOrphans
}

// Reconcile compares all database references and stored objects using bounded
// batches. Callers must prevent concurrent deliveries before enabling orphan
// repair; the application enforces this with an exclusive maintenance lease.
func (s *Store) Reconcile(
	ctx context.Context,
	catalog ContentCatalog,
	options ReconcileOptions,
	emit func(Finding) error,
) (ReconcileSummary, error) {
	if catalog == nil {
		return ReconcileSummary{}, errors.New("content catalog is required")
	}
	if options.BatchSize <= 0 || options.BatchSize > 10_000 {
		return ReconcileSummary{}, errors.New("reconcile batch size must be between 1 and 10000")
	}
	if emit == nil {
		emit = func(Finding) error { return nil }
	}

	summary := ReconcileSummary{}
	if err := catalog.WalkContentRefs(ctx, options.BatchSize, func(ref message.ContentRef) error {
		summary.DatabaseRefs++
		err := s.Verify(ctx, ref)
		switch {
		case err == nil:
			return nil
		case errors.Is(err, os.ErrNotExist):
			summary.Missing++
			return emit(Finding{Kind: FindingMissing, Content: ref})
		case errors.Is(err, ErrContentCorrupt):
			summary.Corrupt++
			return emit(Finding{Kind: FindingCorrupt, Content: ref})
		default:
			return fmt.Errorf("verify catalog content %q: %w", ref.Key, err)
		}
	}); err != nil {
		return summary, fmt.Errorf("scan content metadata: %w", err)
	}

	objects := make([]storedObject, 0, options.BatchSize)
	flush := func() error {
		if len(objects) == 0 {
			return nil
		}
		keys := make([]string, len(objects))
		for index, object := range objects {
			keys[index] = object.ref.Key
		}
		existing, err := catalog.ExistingContentKeys(ctx, keys)
		if err != nil {
			return fmt.Errorf("find existing content keys: %w", err)
		}
		for _, object := range objects {
			if _, ok := existing[object.ref.Key]; ok {
				continue
			}
			finding := Finding{Kind: FindingOrphan, Content: object.ref}
			summary.Orphans++
			if options.RepairOrphans {
				if err := s.Delete(object.ref); err != nil {
					return fmt.Errorf("delete orphan content %q: %w", object.ref.Key, err)
				}
				finding.Repaired = true
				summary.RepairedOrphans++
			}
			if err := emit(finding); err != nil {
				return fmt.Errorf("emit orphan finding: %w", err)
			}
		}
		objects = objects[:0]
		return nil
	}

	if err := s.walkObjects(ctx, func(object storedObject) error {
		summary.StoredObjects++
		objects = append(objects, object)
		if len(objects) == options.BatchSize {
			return flush()
		}
		return nil
	}); err != nil {
		return summary, fmt.Errorf("scan stored content: %w", err)
	}
	if err := flush(); err != nil {
		return summary, fmt.Errorf("scan stored content batch: %w", err)
	}
	return summary, nil
}

type storedObject struct {
	ref message.ContentRef
}

func (s *Store) walkObjects(ctx context.Context, visit func(storedObject) error) error {
	return filepath.WalkDir(s.objectsRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(s.objectsRoot, path)
		if err != nil {
			return fmt.Errorf("resolve object path %q: %w", path, err)
		}
		if relative == "." {
			return nil
		}
		parts := strings.Split(filepath.ToSlash(relative), "/")
		if entry.IsDir() {
			if len(parts) > 2 || !validDigestPrefix(parts[len(parts)-1]) {
				return fmt.Errorf("invalid content object directory %q", relative)
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return fmt.Errorf("content object %q is not a regular file", relative)
		}
		if len(parts) != 3 {
			return fmt.Errorf("invalid content object path %q", relative)
		}
		digest := parts[2]
		if len(digest) != digestHexLength || parts[0] != digest[:2] || parts[1] != digest[2:4] {
			return fmt.Errorf("non-canonical content object path %q", relative)
		}
		if _, err := relativePathForKey("sha256/" + digest); err != nil {
			return fmt.Errorf("invalid content object path %q: %w", relative, err)
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect content object %q: %w", relative, err)
		}
		return visit(storedObject{ref: message.ContentRef{Key: "sha256/" + digest, SizeBytes: info.Size()}})
	})
}

func validDigestPrefix(value string) bool {
	if len(value) != 2 || strings.ToLower(value) != value {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
