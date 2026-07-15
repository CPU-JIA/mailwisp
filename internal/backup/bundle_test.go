package backup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mailwisp/internal/contentstore"
)

func TestCreateAndVerifyBundle(t *testing.T) {
	store, err := contentstore.Open(filepath.Join(t.TempDir(), "content"), contentstore.Options{MaxBytes: 1 << 20})
	if err != nil {
		t.Fatalf("contentstore.Open() error = %v", err)
	}
	for _, raw := range [][]byte{[]byte("first raw message"), []byte("second raw message")} {
		if _, err := store.Put(context.Background(), bytes.NewReader(raw)); err != nil {
			t.Fatalf("store.Put() error = %v", err)
		}
	}
	parent := t.TempDir()
	destination := filepath.Join(parent, "mailwisp-20260715")
	createdAt := time.Date(2026, 7, 15, 1, 0, 0, 0, time.UTC)
	databaseBytes := []byte("postgres-custom-format-dump")
	dumper := &fakeDatabaseDumper{content: databaseBytes, metadata: testDatabaseMetadata()}

	manifest, err := Create(context.Background(), destination, createdAt, dumper, store)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if manifest.Format != formatName || manifest.Version != formatVersion || !manifest.CreatedAt.Equal(createdAt) {
		t.Fatalf("Create() manifest = %+v", manifest)
	}
	if manifest.Content.Objects != 2 || manifest.Content.UncompressedBytes != int64(len("first raw message")+len("second raw message")) {
		t.Fatalf("Create() content manifest = %+v", manifest.Content)
	}
	verified, err := Verify(context.Background(), destination)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if verified.Manifest.Database != manifest.Database || verified.Manifest.Content != manifest.Content {
		t.Fatalf("Verify() manifest = %+v, want %+v", verified.Manifest, manifest)
	}
	gotDatabase, err := os.ReadFile(verified.DatabasePath)
	if err != nil {
		t.Fatalf("ReadFile(database) error = %v", err)
	}
	if !bytes.Equal(gotDatabase, databaseBytes) {
		t.Fatalf("database dump = %q, want %q", gotDatabase, databaseBytes)
	}
}

func TestVerifyRejectsTamperingAndUnexpectedFiles(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{
			name: "database tamper",
			mutate: func(t *testing.T, root string) {
				t.Helper()
				file, err := os.OpenFile(filepath.Join(root, databaseFileName), os.O_WRONLY|os.O_APPEND, 0)
				if err != nil {
					t.Fatalf("OpenFile(database) error = %v", err)
				}
				if _, err := file.Write([]byte("tamper")); err != nil {
					_ = file.Close()
					t.Fatalf("tamper database: %v", err)
				}
				if err := file.Close(); err != nil {
					t.Fatalf("close tampered database: %v", err)
				}
			},
		},
		{
			name: "unexpected file",
			mutate: func(t *testing.T, root string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(root, "extra"), []byte("unexpected"), 0o600); err != nil {
					t.Fatalf("write unexpected file: %v", err)
				}
			},
		},
		{
			name: "unknown manifest field",
			mutate: func(t *testing.T, root string) {
				t.Helper()
				path := filepath.Join(root, manifestFileName)
				manifest, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("ReadFile(manifest) error = %v", err)
				}
				manifest = bytes.Replace(manifest, []byte("{\n"), []byte("{\n  \"unknown\": true,\n"), 1)
				if err := os.WriteFile(path, manifest, 0o600); err != nil {
					t.Fatalf("WriteFile(manifest) error = %v", err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := createTestBundle(t)
			test.mutate(t, root)
			if _, err := Verify(context.Background(), root); err == nil {
				t.Fatal("Verify(tampered) error = nil")
			}
		})
	}
}

func TestCreateFailureDoesNotPublishBundle(t *testing.T) {
	store, err := contentstore.Open(filepath.Join(t.TempDir(), "content"), contentstore.Options{MaxBytes: 1 << 20})
	if err != nil {
		t.Fatalf("contentstore.Open() error = %v", err)
	}
	parent := t.TempDir()
	destination := filepath.Join(parent, "failed")
	dumper := &fakeDatabaseDumper{content: []byte("partial"), err: errors.New("dump failed")}
	if _, err := Create(context.Background(), destination, time.Now().UTC(), dumper, store); err == nil {
		t.Fatal("Create(failing dump) error = nil")
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed bundle destination error = %v, want not exist", err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("ReadDir(parent) error = %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("partial bundle entries = %v", entries)
	}
}

func createTestBundle(t *testing.T) string {
	t.Helper()
	store, err := contentstore.Open(filepath.Join(t.TempDir(), "content"), contentstore.Options{MaxBytes: 1 << 20})
	if err != nil {
		t.Fatalf("contentstore.Open() error = %v", err)
	}
	if _, err := store.Put(context.Background(), bytes.NewReader([]byte("raw"))); err != nil {
		t.Fatalf("store.Put() error = %v", err)
	}
	root := filepath.Join(t.TempDir(), "bundle")
	if _, err := Create(context.Background(), root, time.Date(2026, 7, 15, 1, 0, 0, 0, time.UTC), &fakeDatabaseDumper{
		content:  []byte("database"),
		metadata: testDatabaseMetadata(),
	}, store); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	return root
}

func testDatabaseMetadata() DatabaseMetadata {
	return DatabaseMetadata{
		ServerVersion:    "18.4",
		DumpVersion:      "18.4",
		RestoreVersion:   "18.4",
		MigrationVersion: 1,
	}
}

type fakeDatabaseDumper struct {
	content  []byte
	metadata DatabaseMetadata
	err      error
}

func (d *fakeDatabaseDumper) Dump(_ context.Context, destination io.Writer) (DatabaseMetadata, error) {
	if len(d.content) > 0 {
		if _, err := destination.Write(d.content); err != nil {
			return DatabaseMetadata{}, err
		}
	}
	if d.err != nil {
		return DatabaseMetadata{}, d.err
	}
	return d.metadata, nil
}
