package archive

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// archiveSystem reads c.Name(), then checks Has/Find, then Inserts; a Rename
// from the refresh goroutine can land in between.
func TestSystemLineInsertedAfterRenameGetsNewName(t *testing.T) {
	a, _ := Open(Options{Root: t.TempDir(), Location: time.UTC})
	g := "120363000000000001@g.us"
	c, _, _ := a.EnsureChat(g, "120363000000000001", "group", "")
	if _, err := a.Rename(g, "Family", ""); err != nil {
		t.Fatal(err)
	}
	c.Insert(Row{ID: "S", TS: 100, Sender: "2@s.whatsapp.net"}, func(int) Entry {
		return Entry{System: true, Text: "Bob added Carol"} // the chat's name is filled in by Insert
	})
	c.RenameSenders(func(r Row, sender string) (string, bool) {
		if strings.Trim(sender, "0123456789") == "" && r.Sender == "2@s.whatsapp.net" {
			return "Bob", true
		}
		return "", false
	})
	if got := chatText(t, c); !strings.Contains(got, "] Family: ‎Bob added Carol") {
		t.Errorf("got %q", got)
	}
}

func TestRenameMovesDeletedMedia(t *testing.T) {
	a, _ := Open(Options{Root: t.TempDir(), Location: time.UTC})
	c, _, _ := a.EnsureChat("1@s.whatsapp.net", "Dana", "dm", "")
	os.MkdirAll(c.deletedDirLocked(), 0o700)
	os.WriteFile(filepath.Join(c.deletedDirLocked(), "x.jpg"), []byte("x"), 0o600)
	if _, err := a.Rename("1@s.whatsapp.net", "Dana Putri", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(c.deletedDirLocked(), "x.jpg")); err != nil {
		t.Errorf("deleted media left behind: %v", err)
	}
}
