package config

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
)

// EnvStoreDir is the environment variable that overrides the default store
// directory. This is useful for Docker, CI, and multi-tenant deployments
// where the store path needs to be configured without passing --store on
// every invocation.
const EnvStoreDir = "WACLI_STORE_DIR"

const ConfigFileName = "config.yaml"

var accountNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

type AccountsConfig struct {
	DefaultAccount string                  `yaml:"default_account,omitempty"`
	Accounts       map[string]AccountEntry `yaml:"accounts,omitempty"`
}

type AccountEntry struct {
	Store string `yaml:"store"`
	Label string `yaml:"label,omitempty"`
}

type Account struct {
	Name            string
	Label           string
	ConfiguredStore string
	StoreDir        string
	Default         bool
}

// DefaultStoreDir returns the store directory to use when --store is not
// supplied. It checks WACLI_STORE_DIR first, then falls back to the XDG state
// directory on Linux or ~/.wacli on other platforms.
func DefaultStoreDir() string {
	if dir := os.Getenv(EnvStoreDir); dir != "" {
		return dir
	}
	return DefaultBaseDir()
}

// DefaultBaseDir returns wacli's platform default state root without honoring
// WACLI_STORE_DIR. Account config lives here so a temporary store override
// does not hide the account registry.
func DefaultBaseDir() string {
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
