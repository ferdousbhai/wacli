package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsurePrivateDirCreatesAndChmods(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	if err := EnsurePrivateDir(dir); err != nil {
		t.Fatalf("EnsurePrivateDir: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("expected directory")
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("mode = %04o, want 0700", got)
	}
}

func TestEnsurePrivateDirFixesExistingPerms(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("Chmod setup: %v", err)
	}
	if err := EnsurePrivateDir(dir); err != nil {
		t.Fatalf("EnsurePrivateDir: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("mode = %04o, want 0700", got)
	}
}

func TestEnsureWritableDirDoesNotChmodExistingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "shared")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("Chmod setup: %v", err)
	}
	if err := EnsureWritableDir(dir); err != nil {
		t.Fatalf("EnsureWritableDir: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("mode = %04o, want 0755", got)
	}
}

func TestEnsureWritableDirCreatesPrivateDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new")
	if err := EnsureWritableDir(dir); err != nil {
		t.Fatalf("EnsureWritableDir: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("mode = %04o, want 0700", got)
	}
}
