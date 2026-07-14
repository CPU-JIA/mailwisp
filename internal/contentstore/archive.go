package contentstore

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
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
)

const archiveObjectPrefix = "objects/sha256/"

// ArchiveStats describes the immutable objects represented by one archive.
type ArchiveStats struct {
	Objects int64
	Bytes   int64
}

// WriteArchive writes a deterministic-order tar+gzip stream containing only
// canonical immutable objects. Staging files are intentionally excluded.
func (s *Store) WriteArchive(ctx context.Context, destination io.Writer) (ArchiveStats, error) {
	if destination == nil {
		return ArchiveStats{}, errors.New("content archive destination is required")
	}
	gzipWriter, err := gzip.NewWriterLevel(destination, gzip.BestSpeed)
	if err != nil {
		return ArchiveStats{}, fmt.Errorf("create content gzip writer: %w", err)
	}
	gzipWriter.Header.ModTime = time.Unix(0, 0).UTC()
	tarWriter := tar.NewWriter(gzipWriter)
	summary := ArchiveStats{}

	walkErr := s.walkObjects(ctx, func(object storedObject) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := relativePathForKey(object.ref.Key)
		if err != nil {
			return err
		}
		header := &tar.Header{
			Name:       archiveObjectPrefix + filepath.ToSlash(relative),
			Mode:       0o600,
			Size:       object.ref.SizeBytes,
			ModTime:    time.Unix(0, 0).UTC(),
			AccessTime: time.Time{},
			ChangeTime: time.Time{},
			Typeflag:   tar.TypeReg,
			Format:     tar.FormatUSTAR,
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			return fmt.Errorf("write content archive header %q: %w", object.ref.Key, err)
		}
		file, err := s.OpenContent(object.ref)
		if err != nil {
			return err
		}
		hash := sha256.New()
		written, copyErr := io.CopyBuffer(io.MultiWriter(tarWriter, hash), &contextReader{ctx: ctx, source: file}, make([]byte, copyBufferBytes))
		closeErr := file.Close()
		if copyErr != nil || closeErr != nil {
			var archiveErr error
			if copyErr != nil {
				archiveErr = fmt.Errorf("archive content object %q: %w", object.ref.Key, copyErr)
			}
			return errors.Join(archiveErr, closeErr)
		}
		if written != object.ref.SizeBytes {
			return fmt.Errorf("%w: archive source size %d, expected %d", ErrContentCorrupt, written, object.ref.SizeBytes)
		}
		if object.ref.Key != "sha256/"+hex.EncodeToString(hash.Sum(nil)) {
			return fmt.Errorf("%w: archive source digest mismatch for %q", ErrContentCorrupt, object.ref.Key)
		}
		summary.Objects++
		summary.Bytes += written
		return nil
	})
	closeTarErr := tarWriter.Close()
	closeGzipErr := gzipWriter.Close()
	if walkErr != nil || closeTarErr != nil || closeGzipErr != nil {
		return summary, errors.Join(walkErr, closeTarErr, closeGzipErr)
	}
	return summary, nil
}

// RestoreArchive extracts a content archive into a new root and verifies every
// object against its canonical SHA-256 path. The target must not exist.
func RestoreArchive(ctx context.Context, targetRoot string, source io.Reader, expected ArchiveStats) (ArchiveStats, error) {
	if strings.TrimSpace(targetRoot) == "" {
		return ArchiveStats{}, errors.New("content restore root is required")
	}
	if source == nil {
		return ArchiveStats{}, errors.New("content archive source is required")
	}
	if expected.Objects < 0 || expected.Bytes < 0 {
		return ArchiveStats{}, errors.New("content archive expected stats must not be negative")
	}
	absoluteRoot, err := filepath.Abs(targetRoot)
	if err != nil {
		return ArchiveStats{}, fmt.Errorf("resolve content restore root: %w", err)
	}
	if _, err := os.Lstat(absoluteRoot); err == nil {
		return ArchiveStats{}, errors.New("content restore root already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return ArchiveStats{}, fmt.Errorf("inspect content restore root: %w", err)
	}
	if err := os.Mkdir(absoluteRoot, 0o700); err != nil {
		return ArchiveStats{}, fmt.Errorf("create content restore root: %w", err)
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(absoluteRoot)
		}
	}()
	root, err := os.OpenRoot(absoluteRoot)
	if err != nil {
		return ArchiveStats{}, fmt.Errorf("open content restore root: %w", err)
	}
	defer root.Close()

	bufferedSource := bufio.NewReader(source)
	gzipReader, err := gzip.NewReader(bufferedSource)
	if err != nil {
		return ArchiveStats{}, fmt.Errorf("open content gzip archive: %w", err)
	}
	gzipReader.Multistream(false)
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	summary := ArchiveStats{}
	for {
		if err := ctx.Err(); err != nil {
			return summary, err
		}
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return summary, fmt.Errorf("read content archive header: %w", err)
		}
		if header.Typeflag != tar.TypeReg || header.Size < 0 {
			return summary, fmt.Errorf("content archive entry %q is not a regular file", header.Name)
		}
		key, objectRelative, err := archiveEntryReference(header.Name)
		if err != nil {
			return summary, err
		}
		relative := filepath.Join("objects", "sha256", objectRelative)
		if summary.Objects >= expected.Objects || header.Size > expected.Bytes-summary.Bytes {
			return summary, errors.New("content archive exceeds manifest statistics")
		}
		parent := filepath.Dir(relative)
		if err := root.MkdirAll(parent, 0o700); err != nil {
			return summary, fmt.Errorf("create restored object directory: %w", err)
		}
		file, err := root.OpenFile(relative, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return summary, fmt.Errorf("create restored content object %q: %w", key, err)
		}
		hash := sha256.New()
		written, copyErr := io.CopyBuffer(io.MultiWriter(file, hash), &contextReader{ctx: ctx, source: io.LimitReader(tarReader, header.Size)}, make([]byte, copyBufferBytes))
		syncErr := file.Sync()
		closeErr := file.Close()
		if copyErr != nil || syncErr != nil || closeErr != nil {
			var restoreErr error
			if copyErr != nil {
				restoreErr = fmt.Errorf("restore content object %q: %w", key, copyErr)
			}
			return summary, errors.Join(restoreErr, syncErr, closeErr)
		}
		if written != header.Size {
			return summary, fmt.Errorf("content archive object %q size %d, expected %d", key, written, header.Size)
		}
		if key != "sha256/"+hex.EncodeToString(hash.Sum(nil)) {
			return summary, fmt.Errorf("%w: restored object digest mismatch for %q", ErrContentCorrupt, key)
		}
		if err := syncDirectoryTree(absoluteRoot, filepath.Join(absoluteRoot, parent)); err != nil {
			return summary, fmt.Errorf("sync restored object directory: %w", err)
		}
		summary.Objects++
		summary.Bytes += written
	}
	if summary != expected {
		return summary, fmt.Errorf("content archive statistics = %+v, expected %+v", summary, expected)
	}
	if err := gzipReader.Close(); err != nil {
		return summary, fmt.Errorf("close content gzip archive: %w", err)
	}
	if _, err := bufferedSource.ReadByte(); !errors.Is(err, io.EOF) {
		if err != nil {
			return summary, fmt.Errorf("inspect content archive trailing data: %w", err)
		}
		return summary, errors.New("content archive contains trailing data")
	}
	if err := syncDirectory(absoluteRoot); err != nil {
		return summary, fmt.Errorf("sync content restore root: %w", err)
	}
	complete = true
	return summary, nil
}

func archiveEntryReference(name string) (string, string, error) {
	if strings.Contains(name, "\\") || !strings.HasPrefix(name, archiveObjectPrefix) || filepath.IsAbs(name) {
		return "", "", fmt.Errorf("invalid content archive path %q", name)
	}
	relativeSlash := strings.TrimPrefix(name, archiveObjectPrefix)
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(relativeSlash)))
	if clean != relativeSlash || clean == "." || strings.HasPrefix(clean, "../") {
		return "", "", fmt.Errorf("invalid content archive path %q", name)
	}
	parts := strings.Split(relativeSlash, "/")
	if len(parts) != 3 {
		return "", "", fmt.Errorf("invalid content archive path %q", name)
	}
	key := "sha256/" + parts[2]
	expected, err := relativePathForKey(key)
	if err != nil || filepath.ToSlash(expected) != relativeSlash {
		return "", "", fmt.Errorf("non-canonical content archive path %q", name)
	}
	return key, expected, nil
}
