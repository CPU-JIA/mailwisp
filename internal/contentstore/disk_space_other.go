//go:build !linux && !windows

package contentstore

import "errors"

func filesystemAvailableBytes(string) (uint64, error) {
	return 0, errors.New("filesystem capacity is unsupported on this platform")
}

func isDiskFull(error) bool { return false }
