//go:build windows

package lock

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// lockOffset places the locked byte far past the PID text: Windows locks
// are mandatory, and Probe must still be able to read the file.
const lockOffset = 1 << 30

func lockFile(f *os.File) error {
	ol := windows.Overlapped{Offset: lockOffset}
	return windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		1,
		0,
		&ol,
	)
}

func unlockFile(f *os.File) error {
	ol := windows.Overlapped{Offset: lockOffset}
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &ol)
}

func isLockContention(err error) bool {
	return errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_SHARING_VIOLATION)
}

func processAlive(pid int) bool {
	p, err := os.FindProcess(pid) // fails on Windows when there is no such process
	if err != nil {
		return false
	}
	_ = p.Release()
	return true
}
