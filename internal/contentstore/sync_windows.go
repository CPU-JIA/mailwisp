//go:build windows

package contentstore

func secureDirectory(string) error {
	return nil
}

// Windows does not expose the same portable directory fsync semantics as the
// Linux production target. Object files are still flushed and installed with
// an atomic hard link; Linux integration tests must prove directory durability.
func syncDirectory(string) error {
	return nil
}
