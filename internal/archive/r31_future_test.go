//go:build !windows

package archive

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func inode(t *testing.T, p string) uint64 {
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return st.Sys().(*syscall.Stat_t).Ino
}

// One imported line dated in the future must not turn every later live
// message into a full rewrite of the chat.
func TestFutureImportedEntryKeepsAppends(t *testing.T) {
	a := openTest(t)
	c, _, _ := a.EnsureChat("1@s.whatsapp.net", "Dana", "dm", "")
	c.Insert(Row{ID: "A", TS: 100}, text("hi"))
	in := []Entry{{Time: time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC), Sender: "Mallory", Text: "from the future"}}
	if _, err := c.Merge(context.Background(), in, t.TempDir(), false); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(c.Dir(), ChatFile)
	before := inode(t, p)
	c.Insert(Row{ID: "B", TS: time.Now().Unix()}, text("live"))
	if inode(t, p) != before {
		t.Errorf("live message rewrote the whole chat instead of appending")
	}
}
