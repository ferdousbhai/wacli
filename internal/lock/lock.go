package lock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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

// Probe reports whether another process holds the lock, without taking it
// (taking it, even briefly, would make a wacli starting at that moment fail).
// It reads the PID the holder wrote and checks that process is alive.
func Probe(storeDir string) (bool, string, error) {
	b, err := os.ReadFile(filepath.Join(storeDir, "LOCK"))
	if os.IsNotExist(err) {
		return false, "", nil
	}
	if err != nil {
		return false, "", fmt.Errorf("read lock file: %w", err)
	}
	info := strings.TrimSpace(string(b))
	var pid int
	for _, line := range strings.Split(info, "\n") {
		if v, ok := strings.CutPrefix(line, "pid="); ok {
			pid, _ = strconv.Atoi(v)
		}
	}
	if pid <= 0 || !processAlive(pid) {
		return false, "", nil
	}
	return true, info, nil
}

func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	_ = l.f.Truncate(0) // no stale PID for Probe
	_ = unlockFile(l.f)
	err := l.f.Close()
	l.f = nil
	return err
}
