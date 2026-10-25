package archive

import (
	"strings"
	"testing"
	"time"
)

func TestRenameDuringHoldThenCrash(t *testing.T) {
	root := t.TempDir()
	a, _ := Open(Options{Root: root, Location: time.UTC})
	g := "120363000000000001@g.us"
	c, _, _ := a.EnsureChat(g, "120363000000000001", "group", "")
	c.Insert(Row{ID: "S", TS: 100, Sender: "2@s.whatsapp.net"}, func(int) Entry {
		return Entry{Sender: "120363000000000001", System: true, Text: "Bob added Carol"}
	})
	c.Hold()
	if _, err := a.Rename(g, "Family", ""); err != nil {
		t.Fatal(err)
	}
	// crash: no Release
	a2, _ := Open(Options{Root: root, Location: time.UTC})
	c2, _ := a2.Chat(g)
	t.Logf("after restart: name=%q file=%q", c2.Name(), chatText(t, c2))
	// archiver's refreshSenders callback: fallback (all digits) senders get the row sender's name
	c2.RenameSenders(func(r Row, sender string) (string, bool) {
		if strings.Trim(sender, "0123456789") == "" && r.Sender == "2@s.whatsapp.net" {
			return "Bob", true
		}
		return "", false
	})
	if got := chatText(t, c2); !strings.Contains(got, "] Family: ‎Bob added Carol") {
		t.Errorf("got %q", got)
	}
}
