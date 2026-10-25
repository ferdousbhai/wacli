// Package config resolves where wacli keeps the WhatsApp session.
package config

import (
	"os"
	"path/filepath"
	"runtime"
)

// EnvStoreDir overrides the default store directory.
const EnvStoreDir = "WACLI_STORE_DIR"

// DefaultStoreDir returns the store directory to use when --store is not
// supplied: WACLI_STORE_DIR, else the XDG state directory on Linux (or a
// legacy ~/.wacli that already exists), else ~/.wacli.
func DefaultStoreDir() string {
	if dir := os.Getenv(EnvStoreDir); dir != "" {
		return dir
	}
	xdgStateHome := os.Getenv("XDG_STATE_HOME")
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		if runtime.GOOS == "linux" && xdgStateHome != "" && filepath.IsAbs(xdgStateHome) {
			return filepath.Join(xdgStateHome, "wacli")
		}
		return ".wacli"
	}
	return defaultStoreDirFor(runtime.GOOS, home, xdgStateHome, pathExists)
}

func defaultStoreDirFor(goos, home, xdgStateHome string, exists func(string) bool) string {
	legacy := filepath.Join(home, ".wacli")
	if goos != "linux" {
		return legacy
	}
	if xdgStateHome != "" && filepath.IsAbs(xdgStateHome) {
		return filepath.Join(xdgStateHome, "wacli")
	}
	xdgDefault := filepath.Join(home, ".local", "state", "wacli")
	if exists(legacy) && !exists(xdgDefault) {
		return legacy
	}
	return xdgDefault
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
