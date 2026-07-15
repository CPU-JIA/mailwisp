//go:build !windows

package backup

import (
	"fmt"
	"os"
)

func syncDirectory(path string) error {
	root, err := os.OpenRoot(path)
	if err != nil {
		return fmt.Errorf("open directory root %q: %w", path, err)
	}
	defer root.Close()
	directory, err := root.Open(".")
	if err != nil {
		return fmt.Errorf("open directory %q: %w", path, err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync directory %q: %w", path, err)
	}
	return nil
}
