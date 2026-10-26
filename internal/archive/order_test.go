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
	// A name as WhatsApp gave it, line break and all: as the file has it.
	if e, _ := ParseAs(raw, "~\u202fBob:\nCEO", DefaultDateFormat, time.UTC); e.Sender != "~\u202fBob: CEO" {
		t.Fatalf("uncleaned name: %+v", e)
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
	const us = "1/2/06, 3:04:05 PM"
	root := t.TempDir()
	open := func(format string, readOnly bool) *Archive {
		t.Helper()
		a, err := Open(Options{Root: root, DateFormat: format, Location: time.UTC, ReadOnly: readOnly})
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	recorded := func() string {
		data, _ := os.ReadFile(filepath.Join(root, metaDir, formatFile))
		return strings.TrimSpace(string(data))
	}
	// No lines yet: the chosen format may still change.
	open("", false)
	if open(us, false); recorded() != us {
		t.Fatalf("recorded %q", recorded())
	}
	a := open("", false)
	if a.DateFormat() != us {
		t.Fatalf("not the recorded one: %q", a.DateFormat())
	}
	c, _, _ := a.EnsureChat("62@s.whatsapp.net", "Dana", "dm", "")
	c.Insert(Row{ID: "A", TS: 10}, text("a"))
	// Lines exist: another choice (a service without the shell's setting,
	// say) is ignored, not refused.
	for _, ro := range []bool{false, true} {
		if b := open(DefaultDateFormat, ro); b.DateFormat() != us || len(b.Problems()) != 0 {
			t.Fatalf("read-only %v: %q %v", ro, b.DateFormat(), b.Problems())
		}
	}
	if recorded() != us {
		t.Fatalf("record changed: %q", recorded())
	}
}

// An archive from before formats were recorded: its lines decide, read
// leniently (a byte-order mark, a stray first line), and a mismatch is
// reported, never refused.
func TestUnrecordedArchiveIsCheckedAgainstItsLines(t *testing.T) {
	root := t.TempDir()
	a, err := Open(Options{Root: root, Location: time.UTC})
	if err != nil {
		t.Fatal(err)
	}
	c, _, _ := a.EnsureChat("62@s.whatsapp.net", "Dana", "dm", "")
	c.Insert(Row{ID: "A", TS: 10}, text("a"))
	path := filepath.Join(c.Dir(), ChatFile)
	data, _ := os.ReadFile(path)
	os.WriteFile(path, append([]byte("\uFEFFnot a line\r\n"), data...), 0o600)
	formatPath := filepath.Join(root, metaDir, formatFile)
	os.Remove(formatPath)

	b, err := Open(Options{Root: root, DateFormat: "1/2/06, 3:04:05 PM", Location: time.UTC})
	if err != nil || len(b.Problems()) != 1 {
		t.Fatalf("mismatch: %v %v", err, b.Problems())
	}
	if _, err := os.Stat(formatPath); err == nil {
		t.Fatal("a format the lines aren't in was recorded")
	}
	if b, err := Open(Options{Root: root, Location: time.UTC}); err != nil || len(b.Problems()) != 0 {
		t.Fatalf("matching: %v %v", err, b.Problems())
	}
	if data, _ := os.ReadFile(formatPath); strings.TrimSpace(string(data)) != DefaultDateFormat {
		t.Fatalf("recorded %q", data)
	}
}

func TestUnusableRecordedFormatIsIgnored(t *testing.T) {
	root := t.TempDir()
	if _, err := Open(Options{Root: root, Location: time.UTC}); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, metaDir, formatFile), []byte("2006-01-02 15:04\r\n"), 0o600)
	a, err := Open(Options{Root: root, Location: time.UTC, ReadOnly: true})
	if err != nil || a.DateFormat() != DefaultDateFormat || len(a.Problems()) != 1 {
		t.Fatalf("%v %q %v", err, a.DateFormat(), a.Problems())
	}
}

// A photo from a sender whose name holds ": " is deleted while it downloads:
// the finished download must go aside, not into the chat on a revived line.
func TestDownloadOfADeletedPhotoFromAColonNameGoesAside(t *testing.T) {
	a := openTest(t)
	a.SetSenderNames(func(string, Row) string { return "~ Bob: CEO" })
	c, _, _ := a.EnsureChat("120363000000000001@g.us", "Group", "group", "")
	c.Insert(Row{ID: "P", TS: 10, Sender: "6285555555555@s.whatsapp.net"}, func(int) Entry {
		return Entry{Sender: "~ Bob: CEO", Omitted: "image omitted"}
	})
	if _, err := c.MarkDeleted("P", DeletedByThem); err != nil {
		t.Fatal(err)
	}
	before := chatText(t, c)
	src := filepath.Join(t.TempDir(), "photo.jpg")
	os.WriteFile(src, []byte("jpeg"), 0o600)
	c.PlaceAttachment("P", src, "00000001-PHOTO-1970-01-01-00-00-10.jpg")
	if after := chatText(t, c); after != before {
		t.Fatalf("deleted line changed:\n%q\n%q", before, after)
	}
	if _, err := os.Stat(filepath.Join(c.Dir(), "00000001-PHOTO-1970-01-01-00-00-10.jpg")); err == nil {
		t.Fatal("a deleted photo landed in the chat folder")
	}
}

// Before the record, every chat's first line is checked, not just one.
func TestUnrecordedCheckReadsEveryChat(t *testing.T) {
	root := t.TempDir()
	a, _ := Open(Options{Root: root, Location: time.UTC})
	for _, name := range []string{"Ana", "Zed"} {
		c, _, _ := a.EnsureChat(name+"@s.whatsapp.net", name, "dm", "")
		c.Insert(Row{ID: name, TS: 10}, text("hi"))
	}
	z, _ := a.Chat("Zed@s.whatsapp.net")
	os.WriteFile(filepath.Join(z.Dir(), ChatFile), []byte("[1/1/70, 12:00:10 AM] Zed: hi\r\n"), 0o600)
	os.Remove(filepath.Join(root, metaDir, formatFile))
	b, err := Open(Options{Root: root, Location: time.UTC})
	if err != nil || len(b.Problems()) != 1 {
		t.Fatalf("the later chat's format wasn't checked: %v %v", err, b.Problems())
	}
}
