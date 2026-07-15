package contentstore

import (
	"errors"
	"testing"

	"golang.org/x/sys/unix"
)

func TestWrapStorageErrorMapsLinuxDiskFull(t *testing.T) {
	t.Parallel()

	err := wrapStorageError("write", unix.ENOSPC)
	if !errors.Is(err, ErrInsufficientStorage) || !errors.Is(err, unix.ENOSPC) {
		t.Fatalf("wrapStorageError() = %v", err)
	}
}
