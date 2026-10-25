package fsutil

import (
	"fmt"
	"os"
)

func EnsurePrivateDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat dir: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("chmod dir: %w", err)
	}
	return nil
}

// EnsureWritableDir creates path if needed and checks it is a directory;
// creating a file in it (as every caller does next) proves it's writable.
func EnsureWritableDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat dir: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	return nil
}
