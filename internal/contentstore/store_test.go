package contentstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mailwisp/internal/message"
)

func TestStorePutReadVerifyDelete(t *testing.T) {
	t.Parallel()

	store := newTestStore(t, 1024)
	content := []byte("Subject: MailWisp\r\n\r\nFast mail. Zero trace.")

	ref, err := store.Put(context.Background(), bytes.NewReader(content))
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	expectedDigest := sha256.Sum256(content)
	expectedKey := "sha256/" + hex.EncodeToString(expectedDigest[:])
	if ref.Key != expectedKey {
		t.Fatalf("Put() key = %q, want %q", ref.Key, expectedKey)
	}
	if ref.SizeBytes != int64(len(content)) {
		t.Fatalf("Put() size = %d, want %d", ref.SizeBytes, len(content))
	}

	file, err := store.OpenContent(ref)
	if err != nil {
		t.Fatalf("OpenContent() error = %v", err)
	}
	got, err := io.ReadAll(file)
	closeErr := file.Close()
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if closeErr != nil {
		t.Fatalf("Close() error = %v", closeErr)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("OpenContent() = %q, want %q", got, content)
	}
	if err := store.Verify(context.Background(), ref); err != nil {
		t.Fatalf("Verify() error = %v", err)
	}

	if err := store.Delete(ref); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if err := store.Delete(ref); err != nil {
		t.Fatalf("Delete() idempotent error = %v", err)
	}
	if _, err := store.OpenContent(ref); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("OpenContent() after delete error = %v, want os.ErrNotExist", err)
	}
}

func TestStoreRejectsOversizedContentAndCleansStaging(t *testing.T) {
	t.Parallel()

	store := newTestStore(t, 4)
	_, err := store.Put(context.Background(), bytes.NewReader([]byte("12345")))
	if !errors.Is(err, ErrContentTooLarge) {
		t.Fatalf("Put() error = %v, want ErrContentTooLarge", err)
	}
	assertDirectoryEmpty(t, store.stagingRoot)
}

func TestStoreConcurrentDuplicatePut(t *testing.T) {
	t.Parallel()

	store := newTestStore(t, 1024)
	content := []byte("same immutable message")
	const workers = 32

	refs := make(chan message.ContentRef, workers)
	errorsChannel := make(chan error, workers)
	var waitGroup sync.WaitGroup
	for range workers {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			ref, err := store.Put(context.Background(), bytes.NewReader(content))
			if err != nil {
				errorsChannel <- err
				return
			}
			refs <- ref
		}()
	}
	waitGroup.Wait()
	close(refs)
	close(errorsChannel)

	for err := range errorsChannel {
		t.Errorf("Put() concurrent error = %v", err)
	}
	var expected message.ContentRef
	for ref := range refs {
		if expected.Key == "" {
			expected = ref
			continue
		}
		if ref != expected {
			t.Errorf("Put() concurrent ref = %+v, want %+v", ref, expected)
		}
	}
	assertDirectoryEmpty(t, store.stagingRoot)
	if count := countRegularFiles(t, store.objectsRoot); count != 1 {
		t.Fatalf("object file count = %d, want 1", count)
	}
}

func TestStoreCancelledContextCleansStaging(t *testing.T) {
	t.Parallel()

	store := newTestStore(t, 1024)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := store.Put(ctx, bytes.NewReader([]byte("ignored")))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Put() error = %v, want context.Canceled", err)
	}
	assertDirectoryEmpty(t, store.stagingRoot)
}

func TestStoreCancellationDuringWriteCleansStaging(t *testing.T) {
	t.Parallel()

	store := newTestStore(t, 1<<20)
	ctx, cancel := context.WithCancel(context.Background())
	reader := &cancelAfterFirstRead{
		source: bytes.NewReader(bytes.Repeat([]byte("x"), 128<<10)),
		cancel: cancel,
	}

	_, err := store.Put(ctx, reader)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Put() error = %v, want context.Canceled", err)
	}
	assertDirectoryEmpty(t, store.stagingRoot)
	if count := countRegularFiles(t, store.objectsRoot); count != 0 {
		t.Fatalf("object file count = %d, want 0", count)
	}
}

func TestStoreSourceFailureCleansStaging(t *testing.T) {
	t.Parallel()

	store := newTestStore(t, 1024)
	sourceErr := errors.New("source failed")
	_, err := store.Put(context.Background(), &errorReader{err: sourceErr})
	if !errors.Is(err, sourceErr) {
		t.Fatalf("Put() error = %v, want source error", err)
	}
	assertDirectoryEmpty(t, store.stagingRoot)
}

func TestStoreVerifyDetectsTampering(t *testing.T) {
	t.Parallel()

	store := newTestStore(t, 1024)
	ref, err := store.Put(context.Background(), bytes.NewReader([]byte("original")))
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	path, err := store.pathForKey(ref.Key)
	if err != nil {
		t.Fatalf("pathForKey() error = %v", err)
	}
	if err := os.WriteFile(path, []byte("tampered"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := store.Verify(context.Background(), ref); !errors.Is(err, ErrContentCorrupt) {
		t.Fatalf("Verify() error = %v, want ErrContentCorrupt", err)
	}
}

func TestStoreDuplicatePutRejectsCorruptExistingObject(t *testing.T) {
	t.Parallel()

	store := newTestStore(t, 1024)
	content := []byte("original")
	ref, err := store.Put(context.Background(), bytes.NewReader(content))
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	path, err := store.pathForKey(ref.Key)
	if err != nil {
		t.Fatalf("pathForKey() error = %v", err)
	}
	if err := os.WriteFile(path, []byte("tampered"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err = store.Put(context.Background(), bytes.NewReader(content))
	if !errors.Is(err, ErrContentCorrupt) {
		t.Fatalf("Put() duplicate error = %v, want ErrContentCorrupt", err)
	}
	assertDirectoryEmpty(t, store.stagingRoot)
}

func TestStoreRejectsInvalidKeys(t *testing.T) {
	t.Parallel()

	store := newTestStore(t, 1024)
	invalid := []string{
		"",
		"../message",
		"sha256/../message",
		"sha256/ABCDEF",
		"md5/00000000000000000000000000000000",
		"sha256/0000",
	}
	for _, key := range invalid {
		if _, err := store.OpenContent(message.ContentRef{Key: key}); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("OpenContent(%q) error = %v, want ErrInvalidKey", key, err)
		}
	}
}

func TestOpenRawHonorsCanceledContext(t *testing.T) {
	t.Parallel()

	store := newTestStore(t, 1024)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.OpenRaw(ctx, message.ContentRef{Key: "sha256/" + strings.Repeat("a", 64)}); !errors.Is(err, context.Canceled) {
		t.Fatalf("OpenRaw(canceled) error = %v, want context.Canceled", err)
	}
}

func TestStoreVerifyRejectsNegativeSize(t *testing.T) {
	t.Parallel()

	store := newTestStore(t, 1024)
	err := store.Verify(context.Background(), message.ContentRef{
		Key:       "sha256/0000000000000000000000000000000000000000000000000000000000000000",
		SizeBytes: -1,
	})
	if !errors.Is(err, ErrContentCorrupt) {
		t.Fatalf("Verify() error = %v, want ErrContentCorrupt", err)
	}
}

func TestStorePruneStaging(t *testing.T) {
	t.Parallel()

	store := newTestStore(t, 1024)
	oldPath := filepath.Join(store.stagingRoot, "content-old.stage")
	newPath := filepath.Join(store.stagingRoot, "content-new.stage")
	foreignPath := filepath.Join(store.stagingRoot, "foreign.tmp")
	for _, path := range []string{oldPath, newPath, foreignPath} {
		if err := os.WriteFile(path, []byte("staging"), 0o600); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", path, err)
		}
	}
	oldTime := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(oldPath, oldTime, oldTime); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}

	removed, err := store.PruneStaging(context.Background(), time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("PruneStaging() error = %v", err)
	}
	if removed != 1 {
		t.Fatalf("PruneStaging() removed = %d, want 1", removed)
	}
	if _, err := os.Stat(oldPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old staging Stat() error = %v, want os.ErrNotExist", err)
	}
	for _, path := range []string{newPath, foreignPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("preserved staging Stat(%q) error = %v", path, err)
		}
	}
}

func TestOpenValidatesConfiguration(t *testing.T) {
	t.Parallel()

	if _, err := Open("", Options{MaxBytes: 1}); err == nil {
		t.Fatal("Open() with empty root error = nil, want error")
	}
	if _, err := Open(t.TempDir(), Options{}); err == nil {
		t.Fatal("Open() with zero MaxBytes error = nil, want error")
	}
}

func TestStoreSupportsEmptyContent(t *testing.T) {
	t.Parallel()

	store := newTestStore(t, 1024)
	ref, err := store.Put(context.Background(), bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	if ref.SizeBytes != 0 {
		t.Fatalf("Put() size = %d, want 0", ref.SizeBytes)
	}
	if err := store.Verify(context.Background(), ref); err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
}

func BenchmarkStorePutOneMiB(b *testing.B) {
	store := newBenchmarkStore(b, 2<<20)
	content := bytes.Repeat([]byte("mailwisp"), (1<<20)/len("mailwisp"))
	b.SetBytes(int64(len(content)))
	b.ResetTimer()
	for range b.N {
		if _, err := store.Put(context.Background(), bytes.NewReader(content)); err != nil {
			b.Fatalf("Put() error = %v", err)
		}
	}
}

func newTestStore(t *testing.T, maxBytes int64) *Store {
	t.Helper()
	store, err := Open(t.TempDir(), Options{MaxBytes: maxBytes})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	return store
}

func newBenchmarkStore(b *testing.B, maxBytes int64) *Store {
	b.Helper()
	store, err := Open(b.TempDir(), Options{MaxBytes: maxBytes})
	if err != nil {
		b.Fatalf("Open() error = %v", err)
	}
	return store
}

func assertDirectoryEmpty(t *testing.T, path string) {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatalf("ReadDir(%q) error = %v", path, err)
	}
	if len(entries) != 0 {
		t.Fatalf("ReadDir(%q) entries = %d, want 0", path, len(entries))
	}
}

func countRegularFiles(t *testing.T, root string) int {
	t.Helper()
	count := 0
	err := filepath.WalkDir(root, func(_ string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			count++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir() error = %v", err)
	}
	return count
}

type cancelAfterFirstRead struct {
	source io.Reader
	cancel context.CancelFunc
	read   bool
}

func (r *cancelAfterFirstRead) Read(buffer []byte) (int, error) {
	read, err := r.source.Read(buffer)
	if !r.read {
		r.read = true
		r.cancel()
	}
	return read, err
}

type errorReader struct {
	err error
}

func (r *errorReader) Read([]byte) (int, error) {
	return 0, r.err
}
