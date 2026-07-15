// Package contentstore persists immutable binary content outside the relational
// database while keeping stable, validated references in application state.
package contentstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mailwisp/internal/message"
)

const (
	digestBytes     = sha256.Size
	digestHexLength = digestBytes * 2
	copyBufferBytes = 32 * 1024
)

var (
	// ErrContentTooLarge indicates that an input exceeded the configured byte limit.
	ErrContentTooLarge = message.ErrContentTooLarge
	// ErrInvalidKey indicates that a content key is malformed or unsafe.
	ErrInvalidKey = errors.New("invalid content key")
	// ErrContentCorrupt indicates that stored bytes do not match their reference.
	ErrContentCorrupt = errors.New("stored content does not match reference")
)

// Options configures a local content store.
type Options struct {
	MaxBytes int64
}

// Store persists immutable content beneath one filesystem root.
//
// Staging and final objects intentionally share the same root so that creating
// the final hard link is atomic and cannot cross filesystem boundaries.
type Store struct {
	root        string
	objectsRoot string
	stagingRoot string
	maxBytes    int64
	// putObserver is an unexported deterministic crash-test seam. Production
	// composition never assigns it.
	putObserver func(putStage)
}

type putStage string

const (
	putStageFileSynced   putStage = "file-synced"
	putStageObjectLinked putStage = "object-linked"
)

// Open creates or opens a local content store.
func Open(root string, options Options) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("content store root is required")
	}
	if options.MaxBytes <= 0 {
		return nil, errors.New("content store max bytes must be positive")
	}

	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve content store root: %w", err)
	}

	store := &Store{
		root:        absoluteRoot,
		objectsRoot: filepath.Join(absoluteRoot, "objects", "sha256"),
		stagingRoot: filepath.Join(absoluteRoot, "staging"),
		maxBytes:    options.MaxBytes,
	}

	for _, directory := range []string{store.root, store.objectsRoot, store.stagingRoot} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, fmt.Errorf("create content store directory %q: %w", directory, err)
		}
	}
	if err := syncDirectoryTree(store.root, store.stagingRoot); err != nil {
		return nil, fmt.Errorf("sync content store staging directory: %w", err)
	}
	if err := syncDirectoryTree(store.root, store.objectsRoot); err != nil {
		return nil, fmt.Errorf("sync content store objects directory: %w", err)
	}

	return store, nil
}

// Put streams content into durable immutable storage.
//
// A successful return means the final object file has been synced and linked
// into its content-addressed path. Callers must still commit their database
// reference before acknowledging an external delivery.
func (s *Store) Put(ctx context.Context, source io.Reader) (message.ContentRef, error) {
	if source == nil {
		return message.ContentRef{}, errors.New("content source is required")
	}
	if err := ctx.Err(); err != nil {
		return message.ContentRef{}, err
	}

	staging, err := os.CreateTemp(s.stagingRoot, "content-*.stage")
	if err != nil {
		return message.ContentRef{}, fmt.Errorf("create staging file: %w", err)
	}
	stagingPath := staging.Name()
	keepStaging := false
	defer func() {
		if !keepStaging {
			_ = os.Remove(stagingPath)
		}
	}()

	if err := staging.Chmod(0o600); err != nil {
		_ = staging.Close()
		return message.ContentRef{}, fmt.Errorf("set staging file permissions: %w", err)
	}

	hash := sha256.New()
	limited := io.LimitReader(&contextReader{ctx: ctx, source: source}, s.maxBytes+1)
	written, copyErr := io.CopyBuffer(io.MultiWriter(staging, hash), limited, make([]byte, copyBufferBytes))
	if copyErr != nil {
		_ = staging.Close()
		return message.ContentRef{}, fmt.Errorf("write staging content: %w", copyErr)
	}
	if written > s.maxBytes {
		_ = staging.Close()
		return message.ContentRef{}, fmt.Errorf("%w: maximum %d bytes", ErrContentTooLarge, s.maxBytes)
	}
	if err := ctx.Err(); err != nil {
		_ = staging.Close()
		return message.ContentRef{}, err
	}
	if err := staging.Sync(); err != nil {
		_ = staging.Close()
		return message.ContentRef{}, fmt.Errorf("sync staging content: %w", err)
	}
	s.observePut(putStageFileSynced)
	if err := staging.Close(); err != nil {
		return message.ContentRef{}, fmt.Errorf("close staging content: %w", err)
	}

	digest := hex.EncodeToString(hash.Sum(nil))
	ref := message.ContentRef{Key: "sha256/" + digest, SizeBytes: written}
	destination, err := s.pathForKey(ref.Key)
	if err != nil {
		return message.ContentRef{}, err
	}
	destinationDirectory := filepath.Dir(destination)
	if err := os.MkdirAll(destinationDirectory, 0o700); err != nil {
		return message.ContentRef{}, fmt.Errorf("create object directory: %w", err)
	}
	if err := syncDirectoryTree(s.objectsRoot, destinationDirectory); err != nil {
		return message.ContentRef{}, fmt.Errorf("sync object directory tree: %w", err)
	}

	linkErr := os.Link(stagingPath, destination)
	switch {
	case linkErr == nil:
		s.observePut(putStageObjectLinked)
		if err := syncDirectory(destinationDirectory); err != nil {
			keepStaging = true
			return message.ContentRef{}, fmt.Errorf("sync installed object directory: %w", err)
		}
	case errors.Is(linkErr, os.ErrExist):
		info, statErr := os.Stat(destination)
		if statErr != nil {
			return message.ContentRef{}, fmt.Errorf("inspect existing object: %w", statErr)
		}
		if info.Size() != written {
			return message.ContentRef{}, fmt.Errorf("%w: existing size %d, expected %d", ErrContentCorrupt, info.Size(), written)
		}
		if err := s.Verify(ctx, ref); err != nil {
			return message.ContentRef{}, fmt.Errorf("verify existing object: %w", err)
		}
	default:
		return message.ContentRef{}, fmt.Errorf("install content object: %w", linkErr)
	}

	if err := os.Remove(stagingPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		keepStaging = true
		return message.ContentRef{}, fmt.Errorf("remove installed staging file: %w", err)
	}
	if err := syncDirectory(s.stagingRoot); err != nil {
		return message.ContentRef{}, fmt.Errorf("sync staging directory: %w", err)
	}

	return ref, nil
}

func (s *Store) observePut(stage putStage) {
	if s.putObserver != nil {
		s.putObserver(stage)
	}
}

// OpenContent opens immutable content for reading after validating its reference.
func (s *Store) OpenContent(ref message.ContentRef) (*os.File, error) {
	relativePath, err := relativePathForKey(ref.Key)
	if err != nil {
		return nil, err
	}
	objectsRoot, err := os.OpenRoot(s.objectsRoot)
	if err != nil {
		return nil, fmt.Errorf("open content objects root: %w", err)
	}
	defer objectsRoot.Close()
	file, err := objectsRoot.Open(relativePath)
	if err != nil {
		return nil, fmt.Errorf("open content object: %w", err)
	}
	return file, nil
}

// Verify checks that stored content matches its SHA-256 key and expected size.
func (s *Store) Verify(ctx context.Context, ref message.ContentRef) error {
	if ref.SizeBytes < 0 {
		return ErrContentCorrupt
	}
	file, err := s.OpenContent(ref)
	if err != nil {
		return err
	}
	defer file.Close()

	hash := sha256.New()
	read, err := io.CopyBuffer(hash, &contextReader{ctx: ctx, source: file}, make([]byte, copyBufferBytes))
	if err != nil {
		return fmt.Errorf("verify content object: %w", err)
	}
	if read != ref.SizeBytes {
		return fmt.Errorf("%w: stored size %d, expected %d", ErrContentCorrupt, read, ref.SizeBytes)
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if ref.Key != "sha256/"+digest {
		return fmt.Errorf("%w: digest mismatch", ErrContentCorrupt)
	}
	return nil
}

// Delete removes a content object. Deleting a missing object is idempotent.
func (s *Store) Delete(ref message.ContentRef) error {
	path, err := s.pathForKey(ref.Key)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("delete content object: %w", err)
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return fmt.Errorf("sync deleted object directory: %w", err)
	}
	return nil
}

// PruneStaging removes abandoned staging files last modified before cutoff.
func (s *Store) PruneStaging(ctx context.Context, cutoff time.Time) (int, error) {
	entries, err := os.ReadDir(s.stagingRoot)
	if err != nil {
		return 0, fmt.Errorf("read staging directory: %w", err)
	}

	removed := 0
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "content-") || !strings.HasSuffix(entry.Name(), ".stage") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return removed, fmt.Errorf("inspect staging file %q: %w", entry.Name(), err)
		}
		if !info.ModTime().Before(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(s.stagingRoot, entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			return removed, fmt.Errorf("remove staging file %q: %w", entry.Name(), err)
		}
		removed++
	}
	if removed > 0 {
		if err := syncDirectory(s.stagingRoot); err != nil {
			return removed, fmt.Errorf("sync pruned staging directory: %w", err)
		}
	}
	return removed, nil
}

func (s *Store) pathForKey(key string) (string, error) {
	relativePath, err := relativePathForKey(key)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.objectsRoot, relativePath), nil
}

func relativePathForKey(key string) (string, error) {
	const prefix = "sha256/"
	if !strings.HasPrefix(key, prefix) {
		return "", ErrInvalidKey
	}
	digest := strings.TrimPrefix(key, prefix)
	if len(digest) != digestHexLength || strings.ToLower(digest) != digest {
		return "", ErrInvalidKey
	}
	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != digestBytes {
		return "", ErrInvalidKey
	}

	return filepath.Join(digest[:2], digest[2:4], digest), nil
}

type contextReader struct {
	ctx    context.Context
	source io.Reader
}

func (r *contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.source.Read(buffer)
}

func syncDirectoryTree(root, leaf string) error {
	current := leaf
	for {
		if err := secureDirectory(current); err != nil {
			return err
		}
		if err := syncDirectory(current); err != nil {
			return err
		}
		if current == root {
			return nil
		}
		parent := filepath.Dir(current)
		if parent == current || !isWithinRoot(root, parent) {
			return errors.New("directory tree escapes content store root")
		}
		current = parent
	}
}

func isWithinRoot(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}
