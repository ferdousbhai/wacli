package archive

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func ids(c *Chat) string {
	var out []string
	for _, r := range c.Rows() {
		out = append(out, r.ID)
	}
	return strings.Join(out, " ")
}

// A history batch is archived oldest first; a merge in the middle of it (a
// live message's Flush, a finished download, a scan reading Rows) must not
// put its later entries of the same second before its earlier ones.
func TestOlderEntriesKeepOrderAcrossAMergeMidHold(t *testing.T) {
	for _, merge := range []struct {
		name string
		do   func(*Chat)
	}{
		{"Flush", func(c *Chat) { _ = c.Flush() }},
		{"Rows", func(c *Chat) { c.Rows() }},
	} {
		t.Run(merge.name, func(t *testing.T) {
			a := openTest(t)
			c, _, _ := a.EnsureChat("62@s.whatsapp.net", "Dana", "dm", "")
			// Already archived from a newer page: E in the same second.
			c.Insert(Row{ID: "E", TS: 10}, text("e"))
			c.Hold()
			c.InsertOlder(Row{ID: "A", TS: 10}, text("a"))
			c.InsertOlder(Row{ID: "B", TS: 10}, text("b"))
			merge.do(c)
			c.InsertOlder(Row{ID: "C", TS: 10}, text("c"))
			c.InsertOlder(Row{ID: "D", TS: 10}, text("d"))
			if err := c.Release(); err != nil {
				t.Fatal(err)
			}
			if got := ids(c); got != "A B C D E" {
				t.Fatalf("got %s", got)
			}
			// The next batch walks further back: before all of them.
			c.Hold()
			c.InsertOlder(Row{ID: "Z", TS: 10}, text("z"))
			c.Release()
			if got := ids(c); got != "Z A B C D E" {
				t.Fatalf("next batch: got %s", got)
			}
		})
	}
}

// An import can hold the chat across history batches; each batch still walks
// further back than the one before.
func TestNextHistoryBatchGoesBeforeTheLastWhileAnImportHolds(t *testing.T) {
	a := openTest(t)
	c, _, _ := a.EnsureChat("62@s.whatsapp.net", "Dana", "dm", "")
	c.Insert(Row{ID: "E", TS: 10}, text("e"))
	c.Hold() // an import
	c.HoldOlder()
	c.InsertOlder(Row{ID: "B", TS: 10}, text("b"))
	c.Release()
	c.HoldOlder()
	c.InsertOlder(Row{ID: "A", TS: 10}, text("a"))
	c.Release()
	c.Release()
	if got := ids(c); got != "A B E" {
		t.Fatalf("got %s", got)
	}
}

func TestParseAsKeepsANameHoldingAColon(t *testing.T) {
	raw := "[2025-10-24, 04:39:33] ~ Bob: CEO: hello: all"
	e, err := ParseAs(raw, "~ Bob: CEO", DefaultDateFormat, time.UTC)
	if err != nil || e.Sender != "~ Bob: CEO" || e.Text != "hello: all" {
		t.Fatalf("%+v %v", e, err)
	}
	if got := Render(e, DefaultDateFormat); got != raw {
		t.Fatalf("render %q", got)
	}
	// Another sender, or none known: as Parse.
	for _, sender := range []string{"", "Dana", "~ Bob: Sales"} {
		if e, _ := ParseAs(raw, sender, DefaultDateFormat, time.UTC); e.Sender != "~ Bob" {
			t.Fatalf("%q: %+v", sender, e)
		}
	}
	media := "‎[2025-10-24, 04:39:33] ~ Bob: CEO: ‎<attached: 00000001-PHOTO-x.jpg>"
	if e, _ := ParseAs(media, "~ Bob: CEO", DefaultDateFormat, time.UTC); e.Attachment != "00000001-PHOTO-x.jpg" || e.Text != "" {
		t.Fatalf("media %+v", e)
	}
}

func TestArchiveKeepsOneDateFormat(t *testing.T) {
	root := t.TempDir()
	open := func(format string, readOnly bool) (*Archive, error) {
		return Open(Options{Root: root, DateFormat: format, Location: time.UTC, ReadOnly: readOnly})
	}
	a, err := open("", false)
	if err != nil {
		t.Fatal(err)
	}
	c, _, _ := a.EnsureChat("62@s.whatsapp.net", "Dana", "dm", "")
	c.Insert(Row{ID: "A", TS: 10}, text("a"))
	const us = "1/2/06, 3:04:05 PM"
	if _, err := open(us, false); err == nil || !strings.Contains(err.Error(), DefaultDateFormat) {
		t.Fatalf("another format accepted: %v", err)
	}
	ro, err := open(us, true)
	if err != nil || ro.DateFormat() != DefaultDateFormat || len(ro.Problems()) != 1 {
		t.Fatalf("read-only: %v %q %v", err, ro.DateFormat(), ro.Problems())
	}
	// An archive from before the format was recorded: its lines decide.
	if err := os.Remove(filepath.Join(root, metaDir, formatFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := open(us, false); err == nil {
		t.Fatal("lines in another format accepted")
	}
	if _, err := open(DefaultDateFormat, false); err != nil {
		t.Fatal(err)
	}
}
