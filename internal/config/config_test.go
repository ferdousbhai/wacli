package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDefaultStoreDir(t *testing.T) {
	t.Run("env var overrides default", func(t *testing.T) {
		t.Setenv(EnvStoreDir, "/custom/store/path")
		got := DefaultStoreDir()
		if got != "/custom/store/path" {
			t.Errorf("DefaultStoreDir() = %q, want %q", got, "/custom/store/path")
		}
	})

	t.Run("falls back to platform default when env unset", func(t *testing.T) {
		t.Setenv(EnvStoreDir, "")
		t.Setenv("XDG_STATE_HOME", "")
		got := DefaultStoreDir()
		home, _ := os.UserHomeDir()
		want := defaultStoreDirFor(runtime.GOOS, home, "", pathExists)
		if got != want {
			t.Errorf("DefaultStoreDir() = %q, want %q", got, want)
		}
	})

	t.Run("env var constant is WACLI_STORE_DIR", func(t *testing.T) {
		if EnvStoreDir != "WACLI_STORE_DIR" {
			t.Errorf("EnvStoreDir = %q, want %q", EnvStoreDir, "WACLI_STORE_DIR")
		}
	})
}

func TestDefaultStoreDirFor(t *testing.T) {
	t.Run("uses XDG_STATE_HOME on linux", func(t *testing.T) {
		got := defaultStoreDirFor("linux", "/home/alice", "/state", func(string) bool { return false })
		want := filepath.Join("/state", "wacli")
		if got != want {
			t.Fatalf("defaultStoreDirFor = %q, want %q", got, want)
		}
	})

	t.Run("uses XDG default on linux", func(t *testing.T) {
		got := defaultStoreDirFor("linux", "/home/alice", "", func(string) bool { return false })
		want := filepath.Join("/home/alice", ".local", "state", "wacli")
		if got != want {
			t.Fatalf("defaultStoreDirFor = %q, want %q", got, want)
		}
	})

	t.Run("ignores relative XDG_STATE_HOME on linux", func(t *testing.T) {
		got := defaultStoreDirFor("linux", "/home/alice", "state", func(string) bool { return false })
		want := filepath.Join("/home/alice", ".local", "state", "wacli")
		if got != want {
			t.Fatalf("defaultStoreDirFor = %q, want %q", got, want)
		}
	})

	t.Run("keeps existing legacy linux store", func(t *testing.T) {
		got := defaultStoreDirFor("linux", "/home/alice", "", func(path string) bool {
			return path == filepath.Join("/home/alice", ".wacli")
		})
		want := filepath.Join("/home/alice", ".wacli")
		if got != want {
			t.Fatalf("defaultStoreDirFor = %q, want %q", got, want)
		}
	})

	t.Run("uses XDG when both linux stores exist", func(t *testing.T) {
		got := defaultStoreDirFor("linux", "/home/alice", "", func(string) bool { return true })
		want := filepath.Join("/home/alice", ".local", "state", "wacli")
		if got != want {
			t.Fatalf("defaultStoreDirFor = %q, want %q", got, want)
		}
	})

	t.Run("keeps home dotdir outside linux", func(t *testing.T) {
		got := defaultStoreDirFor("darwin", "/Users/alice", "/state", func(string) bool { return false })
		want := filepath.Join("/Users/alice", ".wacli")
		if got != want {
			t.Fatalf("defaultStoreDirFor = %q, want %q", got, want)
		}
	})
}
