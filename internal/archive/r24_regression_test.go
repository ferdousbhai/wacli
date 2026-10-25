package archive

import (
	"os"
	"strings"
	"testing"
	"time"
)

// A chat whose index was rebuilt once (a hand-added line got a "repair:" row)
// is later converted to Unix line ends: a quoted chat line must stay inside
// its message, as it does for a chat without repair rows.
func TestUnixQuotedLineWithRepairRow(t *testing.T) {
	root := t.TempDir()
	a, _ := Open(Options{Root: root, Location: time.UTC})
	c, _, _ := a.EnsureChat("1@s.whatsapp.net", "Dana", "dm", "")
	c.Insert(Row{ID: "A", TS: 100}, text("one"))
	c.Insert(Row{ID: "B", TS: 200}, text("look:\n[1970-01-01, 00:00:10] Bob: hi"))
	c.Insert(Row{ID: "C", TS: 300}, text("three"))
	p := c.Dir() + "/" + ChatFile
	data := chatText(t, c)
	// A line added by hand: the next load gives it a repair row.
	os.WriteFile(p, []byte(data+"[1970-01-01, 00:05:50] Dana: added\r\n"), 0o600)
	a1, _ := Open(Options{Root: root, Location: time.UTC})
	c1, _ := a1.Chat("1@s.whatsapp.net")
	before := chatText(t, c1)
	if c1.Len() != 4 {
		t.Fatalf("setup: len %d", c1.Len())
	}
	os.WriteFile(p, []byte(strings.ReplaceAll(before, "\r\n", "\n")), 0o600)
	a2, _ := Open(Options{Root: root, Location: time.UTC})
	c2, _ := a2.Chat("1@s.whatsapp.net")
	if got := chatText(t, c2); got != before || c2.Len() != 4 {
		t.Errorf("len %d\nwant %q\ngot  %q\n%v", c2.Len(), before, got, a2.Problems())
	}
}
