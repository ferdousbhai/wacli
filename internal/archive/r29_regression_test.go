package archive

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Text from other people can look like a media line; deleting it must not
// move files.
func TestDeletedTextCantMoveOutsideFile(t *testing.T) {
	top := t.TempDir()
	root := filepath.Join(top, "WhatsApp")
	victim := filepath.Join(top, "victim.txt")
	os.WriteFile(victim, []byte("secret"), 0o600)
	a, _ := Open(Options{Root: root, Location: time.UTC})
	c, _, _ := a.EnsureChat("1@s.whatsapp.net", "Dana", "dm", "")
	c.Insert(Row{ID: "T", TS: 100, Sender: "1@s.whatsapp.net"}, func(int) Entry {
		return Entry{Sender: "Dana", Text: "look ‎<attached: ../../victim.txt>"}
	})
	if _, err := c.MarkDeleted("T", DeletedByThem); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Errorf("file outside the archive moved: %v", err)
	}
}

func TestDeletedTextCantMoveChatFile(t *testing.T) {
	a, _ := Open(Options{Root: t.TempDir(), Location: time.UTC})
	c, _, _ := a.EnsureChat("1@s.whatsapp.net", "Dana", "dm", "")
	c.Insert(Row{ID: "A", TS: 50, Sender: "1@s.whatsapp.net"}, func(int) Entry { return Entry{Sender: "Dana", Text: "hello"} })
	c.Insert(Row{ID: "T", TS: 100, Sender: "1@s.whatsapp.net"}, func(int) Entry {
		return Entry{Sender: "Dana", Text: "‎<attached: _chat.txt>"}
	})
	if _, err := c.MarkDeleted("T", DeletedByThem); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(c.Dir(), ChatFile)); err != nil {
		t.Errorf("_chat.txt moved away: %v", err)
	}
}

func TestIndexKeepsIDsWithControlCharacters(t *testing.T) {
	root := t.TempDir()
	a, _ := Open(Options{Root: root, Location: time.UTC})
	c, _, _ := a.EnsureChat("1@s.whatsapp.net", "Dana", "dm", "")
	odd := "B\nX\t60\t0\\z"
	c.Insert(Row{ID: "A", TS: 50}, func(int) Entry { return Entry{Sender: "Dana", Text: "a"} })
	c.Insert(Row{ID: odd, TS: 100}, func(int) Entry { return Entry{Sender: "Dana", Text: "b"} })
	a2, _ := Open(Options{Root: root, Location: time.UTC})
	c2, _ := a2.Chat("1@s.whatsapp.net")
	if !c2.Has(odd) || len(a2.Problems()) != 0 {
		t.Errorf("ID lost; problems %q", a2.Problems())
	}
}
func TestDeletedMediaLeavesDespiteQuotingText(t *testing.T) {
	a, _ := Open(Options{Root: t.TempDir(), Location: time.UTC})
	c, _, _ := a.EnsureChat("1@s.whatsapp.net", "Dana", "dm", "")
	name := "00000000-PHOTO-1970-01-01-00-01-40.jpg"
	c.Insert(Row{ID: "P", TS: 100}, func(int) Entry { return Entry{Sender: "Dana", Omitted: "image omitted"} })
	src := filepath.Join(a.TmpDir(), "dl")
	os.WriteFile(src, []byte("jpg"), 0o600)
	if _, err := c.PlaceAttachment("P", src, name); err != nil {
		t.Fatal(err)
	}
	c.Flush()
	c.Insert(Row{ID: "T", TS: 200}, func(int) Entry { return Entry{Sender: "Mallory", Text: "‎<attached: " + name + ">"} })
	if _, err := c.MarkDeleted("P", DeletedByThem); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(c.Dir(), name)); err == nil {
		t.Errorf("deleted photo still in the chat folder; DeletedMedia=%v", c.info.DeletedMedia)
	}
}
func TestChatFileDeletedWhileRunningIsRecreated(t *testing.T) {
	a := openTest(t)
	c, _, _ := a.EnsureChat("1@s.whatsapp.net", "Dana", "dm", "")
	c.Insert(Row{ID: "A", TS: 1}, text("one"))
	os.Remove(filepath.Join(c.Dir(), ChatFile))
	if _, _, err := c.Insert(Row{ID: "B", TS: 2}, text("two")); err != nil {
		t.Fatal(err)
	}
	want := "[1970-01-01, 00:00:01] Dana: one\r\n[1970-01-01, 00:00:02] Dana: two\r\n"
	if got := chatText(t, c); got != want {
		t.Errorf("got %q", got)
	}
}
func TestRestartedChatForgetsBackfillProgress(t *testing.T) {
	root := t.TempDir()
	a, _ := Open(Options{Root: root, Location: time.UTC})
	c, _, _ := a.EnsureChat("1@s.whatsapp.net", "Dana", "dm", "")
	c.Insert(Row{ID: "A", TS: 1}, text("one"))
	c.Insert(Row{ID: "B", TS: 2}, text("two"))
	a.UpdateInfo(c.JID(), func(ci *ChatInfo) { ci.Backfill = &Row{ID: "A", TS: 1}; ci.BackfillDone = true })
	os.RemoveAll(c.Dir()) // user deletes the folder while wacli is stopped

	b, err := Open(Options{Root: root, Location: time.UTC})
	if err != nil {
		t.Fatal(err)
	}
	c2, err := b.Chat("1@s.whatsapp.net")
	if err != nil {
		t.Fatal(err)
	}
	if c2.Len() != 0 {
		t.Fatalf("Len=%d", c2.Len())
	}
	info, _ := b.Info(c2.JID())
	if info.BackfillDone || info.Backfill != nil {
		t.Errorf("chat started over but backfill still says done=%v cursor=%v: `wacli backfill` won't refill it", info.BackfillDone, info.Backfill)
	}
}
