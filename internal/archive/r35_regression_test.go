package archive

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func r35realign(t *testing.T, zone string, fallUTC time.Time, step int64, dateFormat string) {
	loc, err := time.LoadLocation(zone)
	if err != nil {
		t.Skip(err)
	}
	root := t.TempDir()
	a, _ := Open(Options{Root: root, Location: loc, DateFormat: dateFormat})
	c, _, _ := a.EnsureChat("1@s.whatsapp.net", "Dana", "dm", "")
	base := fallUTC.Unix()
	c.Insert(Row{ID: "A", TS: base - 3600}, text("a"))
	c.Insert(Row{ID: "B", TS: base - step/2}, text("b"))
	c.Insert(Row{ID: "C", TS: base + step/2}, text("c")) // same wall time as B
	c.Insert(Row{ID: "D", TS: base + 3*3600}, text("d"))
	c.Insert(Row{ID: "E", TS: base + 4*3600}, text("e"))
	f, _ := os.OpenFile(filepath.Join(root, metaDir, indexDir, indexFileName(c.JID())), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(formatRow(Row{ID: "F", TS: base + 5*3600}))
	f.Close()
	b, _ := Open(Options{Root: root, Location: loc, DateFormat: dateFormat})
	c2, _ := b.Chat(c.JID())
	for _, id := range []string{"A", "B", "C", "D", "E"} {
		if !c2.Has(id) {
			t.Errorf("%s: %s lost its ID after realign; rows=%v\n%s", zone, id, c2.Rows(), chatText(t, c2))
		}
	}
}

func TestDSTRealignNewYork(t *testing.T) {
	r35realign(t, "America/New_York", time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC), 3600, "")
}
func TestDSTRealignSantiago(t *testing.T) {
	// Chile falls back first Sunday of April 2026: 00:00 -03 -> 23:00 -04 at 03:00 UTC
	r35realign(t, "America/Santiago", time.Date(2026, 4, 5, 3, 0, 0, 0, time.UTC), 3600, "")
}
func TestDSTRealignLordHowe(t *testing.T) {
	// 2026-04-05 02:00 +11 -> 01:30 +10:30 at 15:00 UTC Apr 4
	r35realign(t, "Australia/Lord_Howe", time.Date(2026, 4, 4, 15, 0, 0, 0, time.UTC), 1800, "")
}
func TestDSTRealignSydney(t *testing.T) {
	r35realign(t, "Australia/Sydney", time.Date(2026, 4, 4, 16, 0, 0, 0, time.UTC), 3600, "")
}
func TestDSTRealignBerlinDotted(t *testing.T) {
	r35realign(t, "Europe/Berlin", time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC), 3600, "02.01.06, 15:04:05")
}
func TestDSTRealignBerlin12h(t *testing.T) {
	r35realign(t, "Europe/Berlin", time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC), 3600, "1/2/06, 3:04:05 PM")
}

func TestDSTImportNewYork(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip(err)
	}
	a, _ := Open(Options{Root: t.TempDir(), Location: loc})
	c, _, _ := a.EnsureChat("1@s.whatsapp.net", "Dana", "dm", "")
	base := time.Date(2025, 11, 2, 6, 0, 0, 0, time.UTC).Unix()
	c.Insert(Row{ID: "C", TS: base + 1800}, text("c")) // 01:30 EST (second 01:30)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ChatFile), []byte(chatText(t, c)), 0o600)
	in, err := a.ReadExport(dir)
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Merge(context.Background(), in, dir, false)
	t.Logf("res=%+v", res)
	if err != nil || res.Added != 0 || res.Matched != 1 {
		t.Errorf("res=%+v err=%v\n%s", res, err, chatText(t, c))
	}
}
