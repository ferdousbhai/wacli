package fsutil

import (
	"fmt"
	"os"
)

// EnsurePrivateDir creates path if needed and makes it private to the user.
func EnsurePrivateDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("chmod dir: %w", err)
	}
	return nil
}
