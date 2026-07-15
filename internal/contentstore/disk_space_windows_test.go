package contentstore

import (
	"errors"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWrapStorageErrorMapsWindowsDiskFull(t *testing.T) {
	t.Parallel()

	err := wrapStorageError("write", windows.ERROR_DISK_FULL)
	if !errors.Is(err, ErrInsufficientStorage) || !errors.Is(err, windows.ERROR_DISK_FULL) {
		t.Fatalf("wrapStorageError() = %v", err)
	}
}
