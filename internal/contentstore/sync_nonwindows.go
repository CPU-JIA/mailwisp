//go:build !windows

package contentstore

import (
	"fmt"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

func secureDirectory(path string) error {
	if err := unix.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("secure content store directory %q: %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("inspect directory %q permissions: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("content store path %q: %w", path, fs.ErrInvalid)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("content store directory %q grants group or other permissions", path)
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open directory %q for sync: %w", path, err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync directory %q: %w", path, err)
	}
	return nil
}
