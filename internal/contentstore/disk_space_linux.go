package contentstore

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

func filesystemAvailableBytes(path string) (uint64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, err
	}
	if stat.Bsize <= 0 {
		return 0, fmt.Errorf("filesystem reported invalid block size %d", stat.Bsize)
	}
	blockSize := uint64(stat.Bsize)
	availableBlocks := stat.Bavail
	if blockSize != 0 && availableBlocks > ^uint64(0)/blockSize {
		return ^uint64(0), nil
	}
	return availableBlocks * blockSize, nil
}

func isDiskFull(err error) bool {
	return errors.Is(err, unix.ENOSPC)
}
