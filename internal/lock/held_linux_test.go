package lock

import (
	"os"
	"path/filepath"
	"testing"
)

// Whoever holds the lock is running, whatever its program is called (a
// renamed binary, `go run`); the PID written in LOCK doesn't decide.
func TestProbeSeesTheHeldLockNotTheName(t *testing.T) {
	dir := t.TempDir()
	l, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	// A PID that isn't running this program: only the lock should count.
	if err := os.WriteFile(filepath.Join(dir, "LOCK"), []byte("pid=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if running, _, _ := Probe(dir); !running {
		t.Fatal("held lock not seen")
	}
	_ = l.Release()
	if running, _, _ := Probe(dir); running {
		t.Fatal("released lock still seen")
	}
}
