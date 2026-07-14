//go:build !windows

package contentstore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenSecuresContentRootVisibleToGroupOrOthers(t *testing.T) {
	root := filepath.Join(t.TempDir(), "content")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	if _, err := Open(root, Options{MaxBytes: 1024}); err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if permissions := info.Mode().Perm(); permissions != 0o700 {
		t.Fatalf("content root permissions = %o, want 700", permissions)
	}
}
