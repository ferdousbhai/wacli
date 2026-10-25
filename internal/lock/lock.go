package lock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ferdousbhai/wacli/internal/fsutil"
)

type Lock struct {
	path string
	f    *os.File
}

var ErrLocked = errors.New("store locked")

func Acquire(storeDir string) (*Lock, error) {
	if err := fsutil.EnsurePrivateDir(storeDir); err != nil {
		return nil, fmt.Errorf("create store dir: %w", err)
	}
	path := filepath.Join(storeDir, "LOCK")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}

	if err := lockFile(f); err != nil {
		_, _ = f.Seek(0, 0)
		b, _ := os.ReadFile(path)
		_ = f.Close()
		if !isLockContention(err) {
			return nil, fmt.Errorf("lock file: %w", err)
		}
		info := strings.TrimSpace(string(b))
		if info != "" {
			return nil, fmt.Errorf("store is locked (another wacli is running?): %w: %w (%s)", ErrLocked, err, info)
		}
		return nil, fmt.Errorf("store is locked (another wacli is running?): %w: %w", ErrLocked, err)
	}

	_ = f.Truncate(0)
	_, _ = f.Seek(0, 0)
	_, _ = fmt.Fprintf(f, "pid=%d\nacquired_at=%s\n", os.Getpid(), time.Now().Format(time.RFC3339Nano))
	_ = f.Sync()

	return &Lock{path: path, f: f}, nil
}

func IsLocked(err error) bool {
	return errors.Is(err, ErrLocked)
}

func Probe(storeDir string) (bool, string, error) {
	path := filepath.Join(storeDir, "LOCK")
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return false, "", nil
		}
		if !errors.Is(err, os.ErrPermission) {
			return false, "", fmt.Errorf("open lock file: %w", err)
		}
		f, err = os.OpenFile(path, os.O_RDONLY, 0)
		if err != nil {
			return false, "", fmt.Errorf("open lock file: %w", err)
		}
	}
	defer f.Close()

	if err := lockFile(f); err != nil {
		if !isLockContention(err) {
			return false, "", fmt.Errorf("lock file: %w", err)
		}
		b, _ := os.ReadFile(path)
		return true, strings.TrimSpace(string(b)), nil
	}
	_ = unlockFile(f)
	return false, "", nil
}

func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	_ = unlockFile(l.f)
	err := l.f.Close()
	l.f = nil
	return err
}
