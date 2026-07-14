package contentstore

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"mailwisp/internal/message"
)

func TestContentArchiveRoundTripAndDeterministicOrder(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "source"), Options{MaxBytes: 1 << 20})
	if err != nil {
		t.Fatalf("Open(source) error = %v", err)
	}
	contents := [][]byte{[]byte("bravo"), []byte("alpha"), []byte{}}
	refs := make([]message.ContentRef, 0, len(contents))
	for _, content := range contents {
		ref, err := store.Put(context.Background(), bytes.NewReader(content))
		if err != nil {
			t.Fatalf("Put() error = %v", err)
		}
		refs = append(refs, ref)
	}

	var first bytes.Buffer
	stats, err := store.WriteArchive(context.Background(), &first)
	if err != nil {
		t.Fatalf("WriteArchive() error = %v", err)
	}
	wantBytes := int64(len("bravo") + len("alpha"))
	if stats.Objects != int64(len(contents)) || stats.Bytes != wantBytes {
		t.Fatalf("WriteArchive() stats = %+v", stats)
	}
	var second bytes.Buffer
	if _, err := store.WriteArchive(context.Background(), &second); err != nil {
		t.Fatalf("WriteArchive(second) error = %v", err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("content archives from unchanged objects are not deterministic")
	}

	target := filepath.Join(t.TempDir(), "restored")
	restored, err := RestoreArchive(context.Background(), target, bytes.NewReader(first.Bytes()), stats)
	if err != nil {
		t.Fatalf("RestoreArchive() error = %v", err)
	}
	if restored != stats {
		t.Fatalf("RestoreArchive() stats = %+v, want %+v", restored, stats)
	}
	restoredStore, err := Open(target, Options{MaxBytes: 1 << 20})
	if err != nil {
		t.Fatalf("Open(restored) error = %v", err)
	}
	for _, ref := range refs {
		if err := restoredStore.Verify(context.Background(), ref); err != nil {
			t.Fatalf("Verify(restored %q) error = %v", ref.Key, err)
		}
	}
}

func TestRestoreArchiveRejectsCorruptionAndRemovesTarget(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "source"), Options{MaxBytes: 1 << 20})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := store.Put(context.Background(), bytes.NewReader([]byte("immutable"))); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	var archive bytes.Buffer
	stats, err := store.WriteArchive(context.Background(), &archive)
	if err != nil {
		t.Fatalf("WriteArchive() error = %v", err)
	}
	corrupt := append([]byte(nil), archive.Bytes()...)
	corrupt[len(corrupt)/2] ^= 0xff
	target := filepath.Join(t.TempDir(), "corrupt")
	if _, err := RestoreArchive(context.Background(), target, bytes.NewReader(corrupt), stats); err == nil {
		t.Fatal("RestoreArchive(corrupt) error = nil")
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("corrupt restore target error = %v, want not exist", err)
	}
}

func TestRestoreArchiveRejectsTrailingData(t *testing.T) {
	t.Parallel()

	source, err := Open(filepath.Join(t.TempDir(), "source"), Options{MaxBytes: 1 << 20})
	if err != nil {
		t.Fatalf("Open(source) error = %v", err)
	}
	if _, err := source.Put(context.Background(), bytes.NewReader([]byte("hello"))); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	var archive bytes.Buffer
	stats, err := source.WriteArchive(context.Background(), &archive)
	if err != nil {
		t.Fatalf("WriteArchive() error = %v", err)
	}
	archive.WriteString("trailing")
	target := filepath.Join(t.TempDir(), "restored")
	if _, err := RestoreArchive(context.Background(), target, bytes.NewReader(archive.Bytes()), stats); err == nil {
		t.Fatal("RestoreArchive(trailing data) error = nil")
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restored target after rejection error = %v, want not exist", err)
	}
}

func TestRestoreArchiveRejectsUnsafeAndDuplicatePaths(t *testing.T) {
	payload := []byte("x")
	digest := sha256.Sum256(payload)
	canonical := archiveObjectPrefix + hex.EncodeToString(digest[:2]) + "/" + hex.EncodeToString(digest[2:4]) + "/" + hex.EncodeToString(digest[:])
	tests := []struct {
		name     string
		entries  []testArchiveEntry
		expected ArchiveStats
	}{
		{
			name:     "path traversal",
			entries:  []testArchiveEntry{{name: archiveObjectPrefix + "../../escape", content: payload}},
			expected: ArchiveStats{Objects: 1, Bytes: 1},
		},
		{
			name: "duplicate object",
			entries: []testArchiveEntry{
				{name: canonical, content: payload},
				{name: canonical, content: payload},
			},
			expected: ArchiveStats{Objects: 2, Bytes: 2},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			archive := buildTestArchive(t, test.entries)
			target := filepath.Join(t.TempDir(), "target")
			if _, err := RestoreArchive(context.Background(), target, bytes.NewReader(archive), test.expected); err == nil {
				t.Fatal("RestoreArchive() error = nil")
			}
			if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed restore target error = %v, want not exist", err)
			}
		})
	}
}

func TestWriteArchiveRejectsCorruptSourceObject(t *testing.T) {
	store, err := Open(t.TempDir(), Options{MaxBytes: 1 << 20})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	ref, err := store.Put(context.Background(), bytes.NewReader([]byte("original")))
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	path, err := store.pathForKey(ref.Key)
	if err != nil {
		t.Fatalf("pathForKey() error = %v", err)
	}
	if err := os.WriteFile(path, []byte("tampered"), 0o600); err != nil {
		t.Fatalf("tamper object: %v", err)
	}
	if _, err := store.WriteArchive(context.Background(), &bytes.Buffer{}); !errors.Is(err, ErrContentCorrupt) {
		t.Fatalf("WriteArchive(corrupt) error = %v, want ErrContentCorrupt", err)
	}
}

type testArchiveEntry struct {
	name    string
	content []byte
}

func buildTestArchive(t *testing.T, entries []testArchiveEntry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range entries {
		if err := tarWriter.WriteHeader(&tar.Header{Name: entry.name, Mode: 0o600, Size: int64(len(entry.content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatalf("WriteHeader() error = %v", err)
		}
		if _, err := tarWriter.Write(entry.content); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatalf("tar Close() error = %v", err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatalf("gzip Close() error = %v", err)
	}
	return buffer.Bytes()
}
