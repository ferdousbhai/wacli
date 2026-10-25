package archive

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openTest(t *testing.T) *Archive {
	t.Helper()
	a, err := Open(Options{Root: t.TempDir(), Location: time.UTC})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func text(s string) func(int) Entry {
	return func(int) Entry { return Entry{Sender: "Dana", Text: s} }
}

func chatText(t *testing.T, c *Chat) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(c.Dir(), ChatFile))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestInsertKeepsTimeOrderAndSkipsDuplicates(t *testing.T) {
	a := openTest(t)
	c, created, err := a.EnsureChat("1@s.whatsapp.net", "Dana", "dm", "")
	if err != nil || !created {
		t.Fatalf("EnsureChat: %v created=%v", err, created)
	}
	for _, m := range []struct {
		id string
		ts int64
	}{{"b", 200}, {"c", 300}, {"a", 100}, {"b", 200}} {
		if _, _, err := c.Insert(Row{ID: m.id, TS: m.ts, Sender: "1@s.whatsapp.net"}, text(m.id)); err != nil {
			t.Fatal(err)
		}
	}
	want := "[1970-01-01, 00:01:40] Dana: a\r\n[1970-01-01, 00:03:20] Dana: b\r\n[1970-01-01, 00:05:00] Dana: c\r\n"
	if got := chatText(t, c); got != want {
		t.Errorf("_chat.txt =\n%q\nwant\n%q", got, want)
	}
	if !strings.HasPrefix(filepath.Base(c.Dir()), "WhatsApp Chat - Dana") {
		t.Errorf("folder = %s", c.Dir())
	}
}

func TestInsertPositionFeedsAttachmentName(t *testing.T) {
	a := openTest(t)
	c, _, _ := a.EnsureChat("g@g.us", "Team", "group", "")
	c.Insert(Row{ID: "x", TS: 10}, text("hi"))
	pos, _, err := c.Insert(Row{ID: "y", TS: 20}, func(pos int) Entry {
		return Entry{Sender: "Dana", Attachment: AttachmentName(pos, "image", time.Unix(20, 0).UTC(), "")}
	})
	if err != nil || pos != 1 {
		t.Fatalf("pos=%d err=%v", pos, err)
	}
	e, _, ok := c.Get("y")
	if !ok || e.Attachment != "00000001-PHOTO-1970-01-01-00-00-20.jpg" {
		t.Errorf("entry = %+v", e)
	}
}

func TestUpdateEditsAndDeletes(t *testing.T) {
	a := openTest(t)
	c, _, _ := a.EnsureChat("1@s.whatsapp.net", "Dana", "dm", "")
	c.Insert(Row{ID: "a", TS: 1}, text("thanks"))
	c.Insert(Row{ID: "b", TS: 2}, text("oops"))
	c.Update("a", func(e *Entry, _ int) bool { e.Text, e.Edited = "Thank you", true; return true })
	c.Update("b", func(e *Entry, _ int) bool { e.Text, e.Deleted = "", DeletedByThem; return true })
	want := "[1970-01-01, 00:00:01] Dana: Thank you ‎<This message was edited>\r\n" +
		"[1970-01-01, 00:00:02] Dana: ‎This message was deleted.\r\n"
	if got := chatText(t, c); got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestReopenRestoresIndexAndDropsTornAppend(t *testing.T) {
	root := t.TempDir()
	a, _ := Open(Options{Root: root, Location: time.UTC})
	c, _, _ := a.EnsureChat("1@s.whatsapp.net", "Dana", "dm", "")
	c.Insert(Row{ID: "a", TS: 1}, text("one"))
	// Simulate a crash after the index row was written but before the entry.
	if err := appendFile(c.indexPath(), formatRow(Row{ID: "torn", TS: 2})); err != nil {
		t.Fatal(err)
	}
	b, err := Open(Options{Root: root, Location: time.UTC})
	if err != nil {
		t.Fatal(err)
	}
	c2, err := b.Chat("1@s.whatsapp.net")
	if err != nil {
		t.Fatal(err)
	}
	if c2.Len() != 1 || !c2.Has("a") || c2.Has("torn") {
		t.Errorf("rows = %+v", c2.Rows())
	}
}

func TestFolderCollisionUsesSuffix(t *testing.T) {
	a := openTest(t)
	g, _, _ := a.EnsureChat("g@g.us", "Maya", "group", "")
	d, _, _ := a.EnsureChat("62@s.whatsapp.net", "Maya", "dm", "+62 811")
	if filepath.Base(g.Dir()) != "WhatsApp Chat - Maya" || filepath.Base(d.Dir()) != "WhatsApp Chat - Maya (+62 811)" {
		t.Errorf("folders %q %q", g.Dir(), d.Dir())
	}
}

func TestMergeAddsMissingAndRestoresMedia(t *testing.T) {
	a := openTest(t)
	c, _, _ := a.EnsureChat("g@g.us", "Maya", "group", "")
	c.Insert(Row{ID: "A", TS: 100}, text("hello"))
	c.Insert(Row{ID: "B", TS: 300}, func(int) Entry {
		return Entry{Sender: "Alex", Text: "🙏", Omitted: "image omitted"}
	})

	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "00000040-PHOTO-1970-01-01-00-05-01.jpg"), []byte("jpg"), 0o600)
	os.WriteFile(filepath.Join(src, "00000039-Invoice.pdf"), []byte("pdf"), 0o600)
	in := []Entry{
		{Time: time.Unix(101, 0).UTC(), Sender: "Dana", Text: "hello"}, // 1s off: same message
		{Time: time.Unix(200, 0).UTC(), Sender: "Dana", DocLabel: "Invoice.pdf • ‎4 pages", Attachment: "00000039-Invoice.pdf"},
		{Time: time.Unix(301, 0).UTC(), Sender: "Alex", Text: "🙏", Attachment: "00000040-PHOTO-1970-01-01-00-05-01.jpg"},
	}
	res, err := c.Merge(in, src)
	if err != nil {
		t.Fatal(err)
	}
	if res != (MergeResult{Matched: 2, Added: 1, MediaRestored: 1}) {
		t.Errorf("result %+v", res)
	}
	want := "[1970-01-01, 00:01:40] Dana: hello\r\n" +
		"‎[1970-01-01, 00:03:20] Dana: Invoice.pdf • ‎4 pages ‎<attached: 00000039-Invoice.pdf>\r\n" +
		"‎[1970-01-01, 00:05:00] Alex: 🙏 ‎<attached: 00000040-PHOTO-1970-01-01-00-05-01.jpg>\r\n"
	if got := chatText(t, c); got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
	for _, f := range []string{"00000039-Invoice.pdf", "00000040-PHOTO-1970-01-01-00-05-01.jpg"} {
		if _, err := os.Stat(filepath.Join(c.Dir(), f)); err != nil {
			t.Error(err)
		}
	}
	// Importing again changes nothing.
	if res, _ := c.Merge(in, src); res.Added != 0 || res.MediaRestored != 0 {
		t.Errorf("second import %+v", res)
	}
}

func TestRenameMovesFolderAndSystemLines(t *testing.T) {
	a := openTest(t)
	c, _, _ := a.EnsureChat("g@g.us", "120363", "group", "")
	c.Insert(Row{ID: "S", TS: 1}, func(int) Entry { return Entry{Sender: "120363", System: true, Text: "You were added"} })
	c.Insert(Row{ID: "A", TS: 2}, text("hi"))
	folder, err := a.Rename("g@g.us", "Maya", "group")
	if err != nil || folder != "WhatsApp Chat - Maya" {
		t.Fatalf("folder %q err %v", folder, err)
	}
	want := "[1970-01-01, 00:00:01] Maya: ‎You were added\r\n[1970-01-01, 00:00:02] Dana: hi\r\n"
	if got := chatText(t, c); got != want {
		t.Errorf("got %q", got)
	}
	if _, err := os.Stat(filepath.Join(a.Root(), "WhatsApp Chat - 120363")); !os.IsNotExist(err) {
		t.Error("old folder still there")
	}
	// Appends after the rename land in the new folder.
	c.Insert(Row{ID: "B", TS: 3}, text("again"))
	if !strings.Contains(chatText(t, c), "again") {
		t.Error("append went elsewhere")
	}
}

func TestMergeKeepsPhotosSentASecondApart(t *testing.T) {
	a := openTest(t)
	c, _, _ := a.EnsureChat("g@g.us", "Maya", "group", "")
	src := t.TempDir()
	in := []Entry{
		{Time: time.Unix(9, 0).UTC(), Sender: "Sam", Attachment: "00000012-PHOTO-1970-01-01-00-00-09.jpg"},
		{Time: time.Unix(10, 0).UTC(), Sender: "Sam", Attachment: "00000013-PHOTO-1970-01-01-00-00-10.jpg"},
	}
	for _, e := range in {
		os.WriteFile(filepath.Join(src, e.Attachment), []byte("x"), 0o600)
	}
	if res, err := c.Merge(in, src); err != nil || res.Added != 2 {
		t.Fatalf("first import %+v %v", res, err)
	}
	if res, _ := c.Merge(in, src); res.Added != 0 || res.Matched != 2 {
		t.Errorf("second import %+v", res)
	}
}

func TestMergeIgnoresMentionNames(t *testing.T) {
	a := openTest(t)
	c, _, _ := a.EnsureChat("g@g.us", "Maya", "group", "")
	c.Insert(Row{ID: "A", TS: 5}, func(int) Entry { return Entry{Sender: "Maya", Text: "Hi @⁨Alex⁩ soon"} })
	in := []Entry{{Time: time.Unix(5, 0).UTC(), Sender: "Maya", Text: "Hi @⁨Saved Messages⁩ soon"}}
	if res, _ := c.Merge(in, t.TempDir()); res.Matched != 1 || res.Added != 0 {
		t.Errorf("%+v", res)
	}
}

func TestFolderNamesDropDirectionMarks(t *testing.T) {
	a := openTest(t)
	c, _, _ := a.EnsureChat("62@s.whatsapp.net", "‪+62012345678901‬", "dm", "")
	if got := filepath.Base(c.Dir()); got != "WhatsApp Chat - +62012345678901" {
		t.Errorf("folder %q", got)
	}
}

func TestNamesWithLineBreaks(t *testing.T) {
	a := openTest(t)
	c, _, _ := a.EnsureChat("62@s.whatsapp.net", "Sunny Pool & Spa\nNorth", "dm", "")
	if got := filepath.Base(c.Dir()); got != "WhatsApp Chat - Sunny Pool & Spa North" {
		t.Errorf("folder %q", got)
	}
	c.Insert(Row{ID: "A", TS: 1}, func(int) Entry { return Entry{Sender: "Sunny Pool & Spa\nNorth", Text: "hi"} })
	if got := chatText(t, c); got != "[1970-01-01, 00:00:01] Sunny Pool & Spa North: hi\r\n" {
		t.Errorf("got %q", got)
	}
	// Files written before the fix still read back entry by entry.
	old := "[1970-01-01, 00:00:01] Pool\nSPA: hi\r\n[1970-01-01, 00:00:02] Pool\nSPA: again\r\n"
	if got := SplitEntries(old); len(got) != 2 {
		t.Errorf("split %q", got)
	}
}

func TestLoadRepairsMisalignedIndex(t *testing.T) {
	root := t.TempDir()
	a, _ := Open(Options{Root: root, Location: time.UTC})
	c, _, _ := a.EnsureChat("1@s.whatsapp.net", "Dana", "dm", "")
	for i, id := range []string{"A", "B", "C", "D"} {
		c.Insert(Row{ID: id, TS: int64(100 * (i + 1))}, text(id))
	}
	// A crash mid-rewrite: _chat.txt has the inserted entry, the index not.
	rows, _ := readIndex(c.indexPath())
	rows = append(rows[:1], rows[2:]...) // lose B's row
	var b strings.Builder
	for _, r := range rows {
		b.WriteString(formatRow(r))
	}
	os.WriteFile(c.indexPath(), []byte(b.String()), 0o600)

	// Reopen in a different zone: the realignment must not care.
	loc, _ := time.LoadLocation("Asia/Makassar")
	a2, _ := Open(Options{Root: root, Location: loc})
	c2, err := a2.Chat("1@s.whatsapp.net")
	if err != nil {
		t.Fatal(err)
	}
	got := c2.Rows()
	if len(got) != 4 || got[0].ID != "A" || !strings.HasPrefix(got[1].ID, "repair:") || got[2].ID != "C" || got[3].ID != "D" {
		t.Errorf("rows %+v", got)
	}
	if len(a2.Repairs()) != 1 {
		t.Errorf("repairs %v", a2.Repairs())
	}
	// It keeps working: appends land and edits find their entry.
	c2.Insert(Row{ID: "E", TS: 500}, text("E"))
	if ok, _ := c2.Update("C", func(e *Entry, _ int) bool { e.Text = "C2"; return true }); !ok {
		t.Error("edit after repair failed")
	}
}
