package archive

import (
	"os"
	"strings"
	"testing"
	"time"
)

// A file converted to Unix line ends: a message quoting a chat line.
func TestUnixQuotedLineStaysInMessage(t *testing.T) {
	root := t.TempDir()
	a, _ := Open(Options{Root: root, Location: time.UTC})
	c, _, _ := a.EnsureChat("1@s.whatsapp.net", "Dana", "dm", "")
	c.Insert(Row{ID: "A", TS: 100}, text("one"))
	c.Insert(Row{ID: "B", TS: 200}, text("look:\n[2020-01-01, 10:00:00] Bob: hi"))
	c.Insert(Row{ID: "C", TS: 300}, text("three"))
	before := chatText(t, c)
	p := c.Dir() + "/" + ChatFile
	os.WriteFile(p, []byte(strings.ReplaceAll(before, "\r\n", "\n")), 0o600)
	a2, _ := Open(Options{Root: root, Location: time.UTC})
	c2, _ := a2.Chat("1@s.whatsapp.net")
	if got := chatText(t, c2); got != before || c2.Len() != 3 {
		t.Errorf("len %d\nwant %q\ngot  %q\n%v", c2.Len(), before, got, a2.Problems())
	}
}

// First append cut short after an inner newline (not right after it).
func TestTornFirstMultilineMessage(t *testing.T) {
	root := t.TempDir()
	a, _ := Open(Options{Root: root, Location: time.UTC})
	c, _, _ := a.EnsureChat("1@s.whatsapp.net", "Dana", "dm", "")
	c.Insert(Row{ID: "A", TS: 100}, text("line1\nline2"))
	full := chatText(t, c)
	p := c.Dir() + "/" + ChatFile
	os.WriteFile(p, []byte(full[:len(full)-5]), 0o600)
	a2, _ := Open(Options{Root: root, Location: time.UTC})
	c2, _ := a2.Chat("1@s.whatsapp.net")
	if c2.Has("A") {
		e, _ := c2.Get("A")
		t.Errorf("cut message kept its ID (redelivery dropped): %q %v", e.Text, a2.Problems())
	}
}
