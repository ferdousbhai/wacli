package archive

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A message written in the repeated hour of a DST fall-back keeps its ID
// when the index has to be realigned after a crash.
func TestRealignKeepsIDInRepeatedDSTHour(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip(err)
	}
	root := t.TempDir()
	a, _ := Open(Options{Root: root, Location: loc})
	c, _, _ := a.EnsureChat("1@s.whatsapp.net", "Dana", "dm", "")
	base := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC).Unix()
	c.Insert(Row{ID: "A", TS: base - 3600}, text("a"))        // 01:00 CEST
	c.Insert(Row{ID: "B", TS: base + 1800}, text("b"))        // 02:30 CEST
	c.Insert(Row{ID: "C", TS: base + 1800 + 3600}, text("c")) // 02:30 CET
	c.Insert(Row{ID: "D", TS: base + 3*3600}, text("d"))      // 04:00 CET
	c.Insert(Row{ID: "E", TS: base + 4*3600}, text("e"))
	// crash between the two appends of F: its row, no entry
	f, _ := os.OpenFile(filepath.Join(root, metaDir, indexDir, indexFileName(c.JID())), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(formatRow(Row{ID: "F", TS: base + 5*3600}))
	f.Close()
	b, _ := Open(Options{Root: root, Location: loc})
	c2, _ := b.Chat(c.JID())
	for _, id := range []string{"A", "B", "C", "D", "E"} {
		if !c2.Has(id) {
			t.Errorf("%s lost its ID after realign; rows=%v", id, c2.Rows())
		}
	}
}

// Importing an export that covers the repeated hour of a DST fall-back must
// not add those messages a second time.
func TestImportMatchesRepeatedDSTHour(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip(err)
	}
	a, _ := Open(Options{Root: t.TempDir(), Location: loc})
	c, _, _ := a.EnsureChat("1@s.whatsapp.net", "Dana", "dm", "")
	base := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC).Unix()
	c.Insert(Row{ID: "B", TS: base + 1800}, text("b")) // 02:30 CEST
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ChatFile), []byte(chatText(t, c)), 0o600)
	in, err := a.ReadExport(dir) // the phone's export has the same line
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Merge(context.Background(), in, dir, false)
	if err != nil || res.Added != 0 {
		t.Errorf("res=%+v err=%v\n%s", res, err, chatText(t, c))
	}
}
