package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mailwisp/internal/contentstore"
	"mailwisp/internal/message"
)

func TestRestoreBundleToIndependentEmptyTargets(t *testing.T) {
	bundleRoot, refs := createRestorableBundle(t)
	for _, name := range []string{"first", "second"} {
		t.Run(name, func(t *testing.T) {
			database := &fakeRestoreDatabase{empty: true, metadata: testDatabaseMetadata()}
			contentRoot := filepath.Join(t.TempDir(), "content")
			manifest, err := Restore(context.Background(), bundleRoot, contentRoot, database)
			if err != nil {
				t.Fatalf("Restore() error = %v", err)
			}
			if manifest.Format != formatName || database.empty || len(database.refs) != len(refs) {
				t.Fatalf("restored manifest/database = %+v/%+v", manifest, database)
			}
			store, err := contentstore.Open(contentRoot, contentstore.Options{MaxBytes: 1})
			if err != nil {
				t.Fatalf("contentstore.Open(restored) error = %v", err)
			}
			for _, ref := range refs {
				if err := store.Verify(context.Background(), ref); err != nil {
					t.Fatalf("Verify(restored %q) error = %v", ref.Key, err)
				}
			}
		})
	}
}

func TestRestoreBundleIntoExistingEmptyContentRoot(t *testing.T) {
	bundleRoot, refs := createRestorableBundle(t)
	contentRoot := filepath.Join(t.TempDir(), "mounted-content")
	if err := os.Mkdir(contentRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	database := &fakeRestoreDatabase{empty: true, metadata: testDatabaseMetadata()}
	if _, err := Restore(context.Background(), bundleRoot, contentRoot, database); err != nil {
		t.Fatalf("Restore(existing empty root) error = %v", err)
	}
	store, err := contentstore.Open(contentRoot, contentstore.Options{MaxBytes: 1})
	if err != nil {
		t.Fatalf("contentstore.Open(restored) error = %v", err)
	}
	for _, ref := range refs {
		if err := store.Verify(context.Background(), ref); err != nil {
			t.Fatalf("Verify(restored %q) error = %v", ref.Key, err)
		}
	}
}

func TestRestoreRejectsNonEmptyContentRootBeforeDatabaseMutation(t *testing.T) {
	bundleRoot, _ := createRestorableBundle(t)
	contentRoot := filepath.Join(t.TempDir(), "content")
	if err := os.Mkdir(contentRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(contentRoot, "existing"), []byte("do not overwrite"), 0o600); err != nil {
		t.Fatal(err)
	}
	database := &fakeRestoreDatabase{empty: true, metadata: testDatabaseMetadata()}
	if _, err := Restore(context.Background(), bundleRoot, contentRoot, database); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("Restore(non-empty content root) error = %v, want not-empty error", err)
	}
	if database.restoreCalls != 0 {
		t.Fatalf("database restore calls = %d, want 0", database.restoreCalls)
	}
	content, err := os.ReadFile(filepath.Join(contentRoot, "existing"))
	if err != nil || string(content) != "do not overwrite" {
		t.Fatalf("existing content changed: content=%q error=%v", content, err)
	}
}

func TestRestoreRejectsNonEmptyDatabaseBeforeContentMutation(t *testing.T) {
	bundleRoot, _ := createRestorableBundle(t)
	contentRoot := filepath.Join(t.TempDir(), "content")
	database := &fakeRestoreDatabase{empty: false, metadata: testDatabaseMetadata()}
	if _, err := Restore(context.Background(), bundleRoot, contentRoot, database); err == nil {
		t.Fatal("Restore(non-empty database) error = nil")
	}
	if _, err := os.Stat(contentRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("content root after rejected restore error = %v, want not exist", err)
	}
	if database.restoreCalls != 0 {
		t.Fatalf("database restore calls = %d, want 0", database.restoreCalls)
	}
}

func TestRestoreDatabaseFailurePreservesInstalledContent(t *testing.T) {
	bundleRoot, _ := createRestorableBundle(t)
	contentRoot := filepath.Join(t.TempDir(), "content")
	database := &fakeRestoreDatabase{empty: true, metadata: testDatabaseMetadata(), restoreErr: errors.New("restore failed")}
	if _, err := Restore(context.Background(), bundleRoot, contentRoot, database); err == nil {
		t.Fatal("Restore(database failure) error = nil")
	}
	if _, err := os.Stat(contentRoot); err != nil {
		t.Fatalf("content root after ambiguous database failure error = %v", err)
	}
	if !database.empty {
		t.Fatal("failed single-transaction restore changed fake database")
	}
}

func TestRestoreDatabaseFailurePreservesInstalledContentInExistingEmptyRoot(t *testing.T) {
	bundleRoot, refs := createRestorableBundle(t)
	contentRoot := filepath.Join(t.TempDir(), "mounted-content")
	if err := os.Mkdir(contentRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	database := &fakeRestoreDatabase{empty: true, metadata: testDatabaseMetadata(), restoreErr: errors.New("restore failed")}
	if _, err := Restore(context.Background(), bundleRoot, contentRoot, database); err == nil {
		t.Fatal("Restore(database failure into existing empty root) error = nil")
	}
	if !database.empty {
		t.Fatal("failed single-transaction restore changed fake database")
	}
	store, err := contentstore.Open(contentRoot, contentstore.Options{MaxBytes: 1})
	if err != nil {
		t.Fatalf("contentstore.Open(installed) error = %v", err)
	}
	for _, ref := range refs {
		if err := store.Verify(context.Background(), ref); err != nil {
			t.Fatalf("Verify(installed %q) error = %v", ref.Key, err)
		}
	}
}

func TestRestoreDetectsCrossStoreInconsistency(t *testing.T) {
	bundleRoot, refs := createRestorableBundle(t)
	missing := message.ContentRef{Key: "sha256/" + strings.Repeat("f", 64), SizeBytes: 20}
	database := &fakeRestoreDatabase{
		empty:        true,
		metadata:     testDatabaseMetadata(),
		overrideRefs: append(append([]message.ContentRef(nil), refs...), missing),
	}
	contentRoot := filepath.Join(t.TempDir(), "content")
	_, err := Restore(context.Background(), bundleRoot, contentRoot, database)
	if !errors.Is(err, contentstore.ErrInconsistentContent) {
		t.Fatalf("Restore(inconsistent) error = %v, want ErrInconsistentContent", err)
	}
	if _, statErr := os.Stat(contentRoot); statErr != nil {
		t.Fatalf("content root after committed inconsistent restore error = %v", statErr)
	}
}

func createRestorableBundle(t *testing.T) (string, []message.ContentRef) {
	t.Helper()
	store, err := contentstore.Open(filepath.Join(t.TempDir(), "source-content"), contentstore.Options{MaxBytes: 1 << 20})
	if err != nil {
		t.Fatalf("contentstore.Open(source) error = %v", err)
	}
	var refs []message.ContentRef
	for _, raw := range [][]byte{[]byte("first"), []byte("second")} {
		ref, err := store.Put(context.Background(), bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("store.Put() error = %v", err)
		}
		refs = append(refs, ref)
	}
	databaseDump, err := json.Marshal(refs)
	if err != nil {
		t.Fatalf("json.Marshal(refs) error = %v", err)
	}
	bundleRoot := filepath.Join(t.TempDir(), "bundle")
	if _, err := Create(context.Background(), bundleRoot, testUTC(), &fakeDatabaseDumper{
		content:  databaseDump,
		metadata: testDatabaseMetadata(),
	}, store); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	return bundleRoot, refs
}

func testUTC() time.Time {
	return time.Date(2026, 7, 15, 1, 0, 0, 0, time.UTC)
}

type fakeRestoreDatabase struct {
	empty        bool
	metadata     DatabaseMetadata
	restoreErr   error
	overrideRefs []message.ContentRef
	refs         []message.ContentRef
	restoreCalls int
}

func (d *fakeRestoreDatabase) Empty(context.Context) (bool, error) {
	return d.empty, nil
}

func (d *fakeRestoreDatabase) Restore(_ context.Context, source io.Reader) (DatabaseMetadata, error) {
	d.restoreCalls++
	if d.restoreErr != nil {
		return DatabaseMetadata{}, d.restoreErr
	}
	var refs []message.ContentRef
	if err := json.NewDecoder(source).Decode(&refs); err != nil {
		return DatabaseMetadata{}, err
	}
	if d.overrideRefs != nil {
		refs = append([]message.ContentRef(nil), d.overrideRefs...)
	}
	d.refs = refs
	d.empty = false
	return d.metadata, nil
}

func (d *fakeRestoreDatabase) WalkContentRefs(_ context.Context, batchSize int, visit func(message.ContentRef) error) error {
	for start := 0; start < len(d.refs); start += batchSize {
		end := start + batchSize
		if end > len(d.refs) {
			end = len(d.refs)
		}
		for _, ref := range d.refs[start:end] {
			if err := visit(ref); err != nil {
				return err
			}
		}
	}
	return nil
}

func (d *fakeRestoreDatabase) ExistingContentKeys(_ context.Context, keys []string) (map[string]struct{}, error) {
	existing := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		for _, ref := range d.refs {
			if ref.Key == key {
				existing[key] = struct{}{}
				break
			}
		}
	}
	return existing, nil
}
