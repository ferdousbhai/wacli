//go:build !windows

package lock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func lockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}

func isLockContention(err error) bool {
	return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)
}

// lockHeld reports whether someone holds the flock on path, from Linux's
// /proc/locks: exact, whatever the holder's program is called, and without
// touching the lock. ok is false where that can't be told.
func lockHeld(path string) (held, ok bool) {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return false, false
	}
	data, err := os.ReadFile("/proc/locks")
	if err != nil {
		return false, false
	}
	// "1: FLOCK  ADVISORY  WRITE 4073136 00:2b:123456 0 EOF"
	inode := ":" + strconv.FormatUint(st.Ino, 10)
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) >= 6 && f[1] == "FLOCK" && strings.HasSuffix(f[5], inode) {
			return true, true
		}
	}
	return false, true
}

// processAlive reports whether pid is running the same program as this
// process. Where /proc exists it compares executable names, so a PID reused
// by something else after a crash doesn't count.
func processAlive(pid int) bool {
	if cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)); err == nil {
		exe, _, _ := strings.Cut(string(cmdline), "\x00")
		return filepath.Base(exe) == filepath.Base(os.Args[0])
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
