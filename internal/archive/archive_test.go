package archive

import (
	"context"
	"fmt"
	"math/rand"
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
	e, ok := c.Get("y")
	if !ok || e.Attachment != "00000001-PHOTO-1970-01-01-00-00-20.jpg" {
		t.Errorf("entry = %+v", e)
	}
}

func TestUpdateEditsAndDeletes(t *testing.T) {
	a := openTest(t)
	c, _, _ := a.EnsureChat("1@s.whatsapp.net", "Dana", "dm", "")
	c.Insert(Row{ID: "a", TS: 1}, text("thanks"))
	c.Insert(Row{ID: "b", TS: 2}, text("oops"))
	c.Update("a", func(e *Entry) bool { e.Text, e.Edited = "Thank you", true; return true })
	c.Update("b", func(e *Entry) bool { e.Text, e.Deleted = "", DeletedByThem; return true })
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
	res, err := c.Merge(context.Background(), in, src, false)
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
	if res, _ := c.Merge(context.Background(), in, src, false); res.Added != 0 || res.MediaRestored != 0 {
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
	if res, err := c.Merge(context.Background(), in, src, false); err != nil || res.Added != 2 {
		t.Fatalf("first import %+v %v", res, err)
	}
	if res, _ := c.Merge(context.Background(), in, src, false); res.Added != 0 || res.Matched != 2 {
		t.Errorf("second import %+v", res)
	}
}

func TestMergeIgnoresMentionNames(t *testing.T) {
	a := openTest(t)
	c, _, _ := a.EnsureChat("g@g.us", "Maya", "group", "")
	c.Insert(Row{ID: "A", TS: 5}, func(int) Entry { return Entry{Sender: "Maya", Text: "Hi @⁨Alex⁩ soon"} })
	in := []Entry{{Time: time.Unix(5, 0).UTC(), Sender: "Maya", Text: "Hi @⁨Saved Messages⁩ soon"}}
	if res, _ := c.Merge(context.Background(), in, t.TempDir(), false); res.Matched != 1 || res.Added != 0 {
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
	if len(a2.Problems()) != 1 {
		t.Errorf("repairs %v", a2.Problems())
	}
	// It keeps working: appends land and edits find their entry.
	c2.Insert(Row{ID: "E", TS: 500}, text("E"))
	if ok, _ := c2.Update("C", func(e *Entry) bool { e.Text = "C2"; return true }); !ok {
		t.Error("edit after repair failed")
	}
}

func TestReadOnlyNeverWrites(t *testing.T) {
	root := t.TempDir()
	a, _ := Open(Options{Root: root, Location: time.UTC})
	c, _, _ := a.EnsureChat("1@s.whatsapp.net", "Dana", "dm", "")
	c.Insert(Row{ID: "A", TS: 1}, text("one"))
	// An index row ahead of its entry, as between sync's two appends.
	appendFile(c.indexPath(), formatRow(Row{ID: "B", TS: 2}))
	before, _ := os.ReadFile(c.indexPath())

	ro, _ := Open(Options{Root: root, Location: time.UTC, ReadOnly: true})
	c2, err := ro.Chat("1@s.whatsapp.net")
	if err != nil || c2.Len() != 1 {
		t.Fatalf("read-only load: %v len=%d", err, c2.Len())
	}
	if after, _ := os.ReadFile(c.indexPath()); string(after) != string(before) {
		t.Error("read-only open rewrote the index")
	}
	if _, _, err := c2.Insert(Row{ID: "C", TS: 3}, text("x")); err != ErrReadOnly {
		t.Errorf("insert: %v", err)
	}
}

func TestMissingFolderStartsOverAndKeepsWorking(t *testing.T) {
	root := t.TempDir()
	a, _ := Open(Options{Root: root, Location: time.UTC})
	c, _, _ := a.EnsureChat("1@s.whatsapp.net", "Dana", "dm", "")
	c.Insert(Row{ID: "A", TS: 1}, text("one"))
	os.RemoveAll(c.Dir()) // the user moved the folder away

	b, _ := Open(Options{Root: root, Location: time.UTC})
	c2, err := b.Chat("1@s.whatsapp.net")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c2.Insert(Row{ID: "B", TS: 2}, text("two")); err != nil {
		t.Fatal(err)
	}
	if got := chatText(t, c2); got != "[1970-01-01, 00:00:02] Dana: two\r\n" {
		t.Errorf("got %q", got)
	}
	if len(b.Problems()) != 1 {
		t.Errorf("problems %v", b.Problems())
	}
	if m, _ := filepath.Glob(c2.indexPath() + ".orphaned-*"); len(m) != 1 {
		t.Error("old index not kept")
	}
}

func TestFolderDeletedWhileRunningIsRecreated(t *testing.T) {
	a := openTest(t)
	c, _, _ := a.EnsureChat("1@s.whatsapp.net", "Dana", "dm", "")
	c.Insert(Row{ID: "A", TS: 1}, text("one"))
	os.RemoveAll(c.Dir())
	if _, _, err := c.Insert(Row{ID: "B", TS: 2}, text("two")); err != nil {
		t.Fatal(err)
	}
	want := "[1970-01-01, 00:00:01] Dana: one\r\n[1970-01-01, 00:00:02] Dana: two\r\n"
	if got := chatText(t, c); got != want {
		t.Errorf("got %q", got)
	}
}

func TestFailedFlushStaysDirtyUntilAWriteSucceeds(t *testing.T) {
	a := openTest(t)
	c, _, _ := a.EnsureChat("1@s.whatsapp.net", "Dana", "dm", "")
	c.Insert(Row{ID: "B", TS: 2}, text("two"))
	c.Hold()
	c.Insert(Row{ID: "A", TS: 1}, text("one"))
	os.Chmod(c.Dir(), 0o500) // writes fail
	if err := c.Release(); err == nil {
		t.Fatal("expected write failure")
	}
	os.Chmod(c.Dir(), 0o700)
	// The next newest-message write must not take the append fast path on
	// top of the stale file.
	c.Insert(Row{ID: "C", TS: 3}, text("three"))
	want := "[1970-01-01, 00:00:01] Dana: one\r\n[1970-01-01, 00:00:02] Dana: two\r\n[1970-01-01, 00:00:03] Dana: three\r\n"
	if got := chatText(t, c); got != want {
		t.Errorf("got %q", got)
	}
}

func TestInterruptedRenameIsFinishedOnOpen(t *testing.T) {
	root := t.TempDir()
	a, _ := Open(Options{Root: root, Location: time.UTC})
	c, _, _ := a.EnsureChat("1@s.whatsapp.net", "+1555", "dm", "")
	c.Insert(Row{ID: "A", TS: 1}, text("one"))
	// Crash after recording the move, before the folder was renamed.
	a.UpdateInfo("1@s.whatsapp.net", func(ci *ChatInfo) {
		ci.PrevFolder, ci.Folder, ci.Name = ci.Folder, "WhatsApp Chat - Dana", "Dana"
	})
	b, err := Open(Options{Root: root, Location: time.UTC})
	if err != nil {
		t.Fatal(err)
	}
	c2, _ := b.Chat("1@s.whatsapp.net")
	if c2.Len() != 1 || filepath.Base(c2.Dir()) != "WhatsApp Chat - Dana" {
		t.Errorf("dir %s len %d", c2.Dir(), c2.Len())
	}
}

func TestRenameToSameFolderDoesNotMove(t *testing.T) {
	a := openTest(t)
	a.EnsureChat("1@s.whatsapp.net", "Pool\nSPA", "dm", "+1")
	folder, err := a.Rename("1@s.whatsapp.net", "Pool SPA", "+1")
	if err != nil || folder != "WhatsApp Chat - Pool SPA" {
		t.Errorf("folder %q err %v", folder, err)
	}
}

func TestMergeKeepsIdenticalLines(t *testing.T) {
	a := openTest(t)
	c, _, _ := a.EnsureChat("1@s.whatsapp.net", "Dana", "dm", "")
	ok := Entry{Time: time.Unix(5, 0).UTC(), Sender: "Dana", Text: "ok"}
	if res, err := c.Merge(context.Background(), []Entry{ok, ok}, t.TempDir(), false); err != nil || res.Added != 2 {
		t.Fatalf("%+v %v", res, err)
	}
	if res, _ := c.Merge(context.Background(), []Entry{ok, ok}, t.TempDir(), false); res.Added != 0 || res.Matched != 2 {
		t.Errorf("second import %+v", res)
	}
}

func TestMergeMissingFileLeavesItOmittedAndContinues(t *testing.T) {
	a := openTest(t)
	c, _, _ := a.EnsureChat("1@s.whatsapp.net", "Dana", "dm", "")
	in := []Entry{
		{Time: time.Unix(5, 0).UTC(), Sender: "Dana", Attachment: "00000001-PHOTO-1970-01-01-00-00-05.jpg"},
		{Time: time.Unix(6, 0).UTC(), Sender: "Dana", Text: "after"},
	}
	for range 2 { // and again: the second run must not get stuck either
		if _, err := c.Merge(context.Background(), in, t.TempDir(), false); err != nil {
			t.Fatal(err)
		}
	}
	want := "\u200e[1970-01-01, 00:00:05] Dana: \u200eimage omitted\r\n[1970-01-01, 00:00:06] Dana: after\r\n"
	if got := chatText(t, c); got != want {
		t.Errorf("got %q", got)
	}
}

func TestRenameGroupWithColonInName(t *testing.T) {
	a := openTest(t)
	c, _, _ := a.EnsureChat("g@g.us", "Team: Ops", "group", "")
	c.Insert(Row{ID: "S", TS: 1}, func(int) Entry { return Entry{Sender: "Team: Ops", System: true, Text: "You were added"} })
	a.Rename("g@g.us", "Ops", "")
	if got := chatText(t, c); got != "[1970-01-01, 00:00:01] Ops: ‎You were added\r\n" {
		t.Errorf("got %q", got)
	}
}

func TestHeldInsertsAreMergedOnceInOrder(t *testing.T) {
	a := openTest(t)
	c, _, _ := a.EnsureChat("1@s.whatsapp.net", "Dana", "dm", "")
	c.Insert(Row{ID: "new", TS: 1_000_000}, text("newest"))
	start := time.Now()
	c.Hold()
	for i := 20000; i > 0; i-- { // older history, newest first
		c.Insert(Row{ID: fmt.Sprint(i), TS: int64(i)}, text(fmt.Sprint(i)))
	}
	if !c.Has("777") || c.Len() != 20001 {
		t.Fatal("held entries not visible")
	}
	if err := c.Release(); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("20k held inserts took %v", d)
	}
	rows := c.Rows()
	if rows[0].ID != "1" || rows[19999].ID != "20000" || rows[20000].ID != "new" {
		t.Errorf("order: %v %v %v", rows[0], rows[19999], rows[20000])
	}
	if strings.Count(chatText(t, c), "\r\n") != 20001 {
		t.Error("file not written")
	}
}

func TestRewriteKeepsTimestampText(t *testing.T) {
	// 02:30 on 2025-03-09 doesn't exist in New York (clocks jump to 03:00).
	ny, _ := time.LoadLocation("America/New_York")
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "WhatsApp Chat - Dana"), 0o700)
	os.MkdirAll(filepath.Join(root, ".wacli", "index"), 0o700)
	os.WriteFile(filepath.Join(root, ".wacli", "chats.json"), []byte(`{"1@s.whatsapp.net":{"folder":"WhatsApp Chat - Dana","name":"Dana","kind":"dm"}}`), 0o600)
	os.WriteFile(filepath.Join(root, "WhatsApp Chat - Dana", ChatFile), []byte("[2025-03-09, 02:30:00] Dana: hi\r\n"), 0o600)
	os.WriteFile(filepath.Join(root, ".wacli", "index", "1@s.whatsapp.net.tsv"), []byte("A\t1741505400\t0\t\n"), 0o600)
	a, _ := Open(Options{Root: root, Location: ny})
	c, _ := a.Chat("1@s.whatsapp.net")
	c.Update("A", func(e *Entry) bool { e.Edited = true; return true })
	if got := chatText(t, c); got != "[2025-03-09, 02:30:00] Dana: hi \u200e<This message was edited>\r\n" {
		t.Errorf("got %q", got)
	}
}

func TestPlaceAttachmentUsesFreeNameCurrentFolderAndDeletion(t *testing.T) {
	a := openTest(t)
	c, _, _ := a.EnsureChat("1@s.whatsapp.net", "+1", "dm", "")
	for _, id := range []string{"A", "B", "D"} {
		c.Insert(Row{ID: id, TS: 1}, func(int) Entry { return Entry{Sender: "Dana", Omitted: "document omitted"} })
	}
	a.Rename("1@s.whatsapp.net", "Dana", "") // renamed while downloading
	tmp := t.TempDir()
	put := func(id string) (string, error) {
		src := filepath.Join(tmp, id)
		os.WriteFile(src, []byte(id), 0o600)
		return c.PlaceAttachment(id, src, "00000000-invoice.pdf")
	}
	n1, _ := put("A")
	n2, _ := put("B")
	if n1 != "00000000-invoice.pdf" || n2 != "00000000-invoice-2.pdf" {
		t.Errorf("names %q %q", n1, n2)
	}
	if got, _ := os.ReadFile(filepath.Join(a.Root(), "WhatsApp Chat - Dana", n1)); string(got) != "A" {
		t.Error("first file overwritten or misplaced")
	}
	c.Update("D", func(e *Entry) bool { e.Deleted = DeletedByThem; return true })
	if n, _ := put("D"); n != "" {
		t.Error("deleted entry got a file")
	}
	if _, err := os.Stat(filepath.Join(a.Root(), ".wacli", "deleted", "WhatsApp Chat - Dana", "00000000-invoice.pdf")); err != nil {
		t.Error("deleted message's file not set aside")
	}
	c.Flush()
	if !strings.Contains(chatText(t, c), "<attached: 00000000-invoice-2.pdf>") {
		t.Error("entry not updated")
	}
}

func TestHeldInsertPositionsCountPendingEntries(t *testing.T) {
	a := openTest(t)
	c, _, _ := a.EnsureChat("1@s.whatsapp.net", "Dana", "dm", "")
	var got []int
	c.Hold()
	for _, ts := range []int64{30, 10, 20} {
		pos, _, _ := c.Insert(Row{ID: fmt.Sprint(ts), TS: ts}, text("x"))
		got = append(got, pos)
	}
	c.Release()
	if fmt.Sprint(got) != "[0 0 1]" {
		t.Errorf("positions %v, want [0 0 1]", got)
	}
}

func TestFindSeesEntriesHeldByABatch(t *testing.T) {
	a := openTest(t)
	c, _, _ := a.EnsureChat("1@s.whatsapp.net", "Dana", "dm", "")
	c.Hold() // a history chunk is being processed
	c.Insert(Row{ID: "A", TS: 5}, text("hello"))
	if !c.Find(Entry{Time: time.Unix(5, 0).UTC(), Sender: "Dana", Text: "hello"}) {
		t.Error("held entry not found")
	}
	res, _ := c.Merge(context.Background(), []Entry{{Time: time.Unix(5, 0).UTC(), Sender: "Dana", Text: "hello"}}, t.TempDir(), false)
	c.Release()
	if res.Added != 0 || strings.Count(chatText(t, c), "hello") != 1 {
		t.Errorf("duplicate: %+v %q", res, chatText(t, c))
	}
}

func TestTrimUnloadsAndReloadsTransparently(t *testing.T) {
	a := openTest(t)
	c, _, _ := a.EnsureChat("1@s.whatsapp.net", "Dana", "dm", "")
	c.Insert(Row{ID: "A", TS: 1}, text("one"))
	a.Trim(0)
	c.mu.Lock()
	loaded := c.loaded
	c.mu.Unlock()
	if loaded {
		t.Fatal("not unloaded")
	}
	c.Insert(Row{ID: "B", TS: 2}, text("two"))
	if !c.Has("A") || c.Len() != 2 {
		t.Error("reload lost entries")
	}
	c.Hold()
	c.Insert(Row{ID: "C", TS: 3}, text("three"))
	a.Trim(0) // must not drop unwritten entries
	c.Release()
	if !strings.Contains(chatText(t, c), "three") {
		t.Error("held entry lost to Trim")
	}
}

func TestPlaceAttachmentReusesFileACrashLeft(t *testing.T) {
	a := openTest(t)
	c, _, _ := a.EnsureChat("1@s.whatsapp.net", "Dana", "dm", "")
	c.Insert(Row{ID: "A", TS: 1}, func(int) Entry { return Entry{Sender: "Dana", Omitted: "image omitted"} })
	data := []byte("photo")
	os.WriteFile(filepath.Join(c.Dir(), "00000000-PHOTO.jpg"), data, 0o600) // placed before the crash
	src := filepath.Join(t.TempDir(), "again")
	os.WriteFile(src, data, 0o600)
	name, err := c.PlaceAttachment("A", src, "00000000-PHOTO.jpg")
	if err != nil || name != "00000000-PHOTO.jpg" {
		t.Errorf("name %q err %v", name, err)
	}
	if m, _ := filepath.Glob(filepath.Join(c.Dir(), "*-2.jpg")); len(m) != 0 {
		t.Error("made a duplicate")
	}
}

func TestMergeStopsWhenCancelled(t *testing.T) {
	a := openTest(t)
	c, _, _ := a.EnsureChat("1@s.whatsapp.net", "Dana", "dm", "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Merge(ctx, []Entry{{Time: time.Unix(1, 0).UTC(), Sender: "Dana", Text: "x"}}, t.TempDir(), false); err == nil {
		t.Error("cancelled import ran")
	}
}

func TestMakeDeletedKeepsTimestampText(t *testing.T) {
	e, _ := Parse("[2025-03-09, 02:30:00] Dana: hi", DefaultDateFormat, time.UTC)
	e.MakeDeleted(DeletedByThem)
	if got := Render(e, DefaultDateFormat); got != "[2025-03-09, 02:30:00] Dana: ‎This message was deleted." {
		t.Errorf("got %q", got)
	}
}

func TestNoTempFilesInChatFolders(t *testing.T) {
	a := openTest(t)
	c, _, _ := a.EnsureChat("1@s.whatsapp.net", "Dana", "dm", "")
	c.Insert(Row{ID: "B", TS: 2}, text("b"))
	c.Insert(Row{ID: "A", TS: 1}, text("a")) // full rewrite
	entries, _ := os.ReadDir(c.Dir())
	for _, e := range entries {
		if e.Name() != ChatFile {
			t.Errorf("unexpected %s in chat folder", e.Name())
		}
	}
}

func TestTornIndexRowIsRepairedNotFatal(t *testing.T) {
	root := t.TempDir()
	a, _ := Open(Options{Root: root, Location: time.UTC})
	c, _, _ := a.EnsureChat("1@s.whatsapp.net", "Dana", "dm", "")
	c.Insert(Row{ID: "A", TS: 100}, text("a"))
	c.Insert(Row{ID: "B", TS: 200}, text("b"))
	idx, _ := os.ReadFile(c.indexPath())
	os.WriteFile(c.indexPath(), idx[:len(idx)-5], 0o600) // crash mid-append
	b, _ := Open(Options{Root: root, Location: time.UTC})
	c2, err := b.Chat("1@s.whatsapp.net")
	if err != nil {
		t.Fatalf("chat refused: %v", err)
	}
	if _, _, err := c2.Insert(Row{ID: "C", TS: 300}, text("c")); err != nil || c2.Len() != 3 {
		t.Errorf("len %d err %v", c2.Len(), err)
	}
}

func TestReleaseWritesEvenUnderAnotherHold(t *testing.T) {
	a := openTest(t)
	c, _, _ := a.EnsureChat("1@s.whatsapp.net", "Dana", "dm", "")
	c.Hold() // an import
	c.Hold() // a history chunk
	c.Insert(Row{ID: "A", TS: 1}, text("from history"))
	c.Release() // the chunk is acknowledged now: it must be on disk
	if !strings.Contains(chatText(t, c), "from history") {
		t.Error("acknowledged entry only in memory")
	}
	c.Release()
}

func TestFindDuringHoldDoesNotMerge(t *testing.T) {
	a := openTest(t)
	c, _, _ := a.EnsureChat("1@s.whatsapp.net", "Dana", "dm", "")
	c.Hold()
	c.Insert(Row{ID: "A", TS: 5}, text("x"))
	c.Find(Entry{Time: time.Unix(5, 0).UTC(), Sender: "Dana", Text: "x"})
	c.mu.Lock()
	pending := len(c.pending)
	c.mu.Unlock()
	if pending != 1 {
		t.Error("Find merged the held entries")
	}
	c.Release()
}

func TestDeletedMediaMovesOnlyAfterWrite(t *testing.T) {
	a := openTest(t)
	c, _, _ := a.EnsureChat("1@s.whatsapp.net", "Dana", "dm", "")
	c.Insert(Row{ID: "A", TS: 1}, func(int) Entry { return Entry{Sender: "Dana", Attachment: "00000000-PHOTO.jpg"} })
	os.WriteFile(filepath.Join(c.Dir(), "00000000-PHOTO.jpg"), []byte("x"), 0o600)
	c.Hold()
	if ok, err := c.MarkDeleted("A", DeletedByThem); !ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if _, err := os.Stat(filepath.Join(c.Dir(), "00000000-PHOTO.jpg")); err != nil {
		t.Error("moved before the entry was written")
	}
	c.Release()
	if _, err := os.Stat(filepath.Join(a.Root(), ".wacli", "deleted", "WhatsApp Chat - Dana", "00000000-PHOTO.jpg")); err != nil {
		t.Error("not moved after the write")
	}
	if !strings.Contains(chatText(t, c), "This message was deleted.") {
		t.Error("entry not deleted")
	}
}

func TestPosition(t *testing.T) {
	a := openTest(t)
	c, _, _ := a.EnsureChat("1@s.whatsapp.net", "Dana", "dm", "")
	c.Insert(Row{ID: "B", TS: 2}, text("b"))
	c.Insert(Row{ID: "A", TS: 1}, text("a"))
	if c.Position("B") != 1 || c.Position("A") != 0 || c.Position("X") != -1 {
		t.Errorf("positions %d %d %d", c.Position("A"), c.Position("B"), c.Position("X"))
	}
}

func TestSameFileSentTwiceGetsTwoFiles(t *testing.T) {
	a := openTest(t)
	c, _, _ := a.EnsureChat("1@s.whatsapp.net", "Dana", "dm", "")
	omitted := func(int) Entry { return Entry{Sender: "Dana", Omitted: "document omitted"} }
	c.Insert(Row{ID: "NEW", TS: 2}, omitted)
	c.Insert(Row{ID: "OLD", TS: 1}, omitted) // older, arrives later, same number
	put := func(id string) string {
		src := filepath.Join(t.TempDir(), id)
		os.WriteFile(src, []byte("same pdf"), 0o600)
		name, err := c.PlaceAttachment(id, src, "00000000-doc.pdf")
		if err != nil {
			t.Fatal(err)
		}
		return name
	}
	if a1, a2 := put("NEW"), put("OLD"); a1 == a2 {
		t.Fatalf("both entries point at %s", a1)
	}
	c.MarkDeleted("NEW", DeletedByThem)
	e, _ := c.Get("OLD")
	if _, err := os.Stat(filepath.Join(c.Dir(), e.Attachment)); err != nil {
		t.Errorf("deleting one took the other's file: %v", err)
	}
}

func TestDeletedMediaLeftByACrashIsMovedOnLoad(t *testing.T) {
	root := t.TempDir()
	a, _ := Open(Options{Root: root, Location: time.UTC})
	c, _, _ := a.EnsureChat("1@s.whatsapp.net", "Dana", "dm", "")
	c.Insert(Row{ID: "A", TS: 1}, func(int) Entry { return Entry{Sender: "Dana", Attachment: "00000000-PHOTO.jpg"} })
	os.WriteFile(filepath.Join(c.Dir(), "00000000-PHOTO.jpg"), []byte("x"), 0o600)
	// Crash between writing the "deleted" entry and moving the file.
	c.Update("A", func(e *Entry) bool { e.MakeDeleted(DeletedByThem); return true })
	a.UpdateInfo("1@s.whatsapp.net", func(ci *ChatInfo) { ci.DeletedMedia = []string{"00000000-PHOTO.jpg"} })

	b, _ := Open(Options{Root: root, Location: time.UTC})
	if _, err := b.Chat("1@s.whatsapp.net"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "WhatsApp Chat - Dana", "00000000-PHOTO.jpg")); !os.IsNotExist(err) {
		t.Error("deleted media still in the chat folder")
	}
	if info, _ := b.Info("1@s.whatsapp.net"); len(info.DeletedMedia) != 0 {
		t.Errorf("list not cleared: %v", info.DeletedMedia)
	}
}

func TestRealignNeverHandsOutAnotherMessagesID(t *testing.T) {
	// Crash after _chat.txt got older message b, before the index did.
	entries := []string{
		"[1970-01-01, 00:16:40] Dana: a",
		"[1970-01-01, 00:18:20] Dana: b",
		"[1970-01-01, 00:33:20] Dana: c",
	}
	rows := []Row{{ID: "a", TS: 1000}, {ID: "c", TS: 2000}}
	got := realign(entries, rows, DefaultDateFormat, time.UTC)
	if got[0].ID != "a" || !strings.HasPrefix(got[1].ID, "repair:") || got[2].ID != "c" {
		t.Errorf("rows %+v", got)
	}
}

func TestDeletionSurvivesAFailedMetaSave(t *testing.T) {
	a := openTest(t)
	c, _, _ := a.EnsureChat("1@s.whatsapp.net", "Dana", "dm", "")
	c.Insert(Row{ID: "A", TS: 1}, func(int) Entry { return Entry{Sender: "Dana", Attachment: "x.jpg"} })
	os.Chmod(a.TmpDir(), 0o500) // every save fails
	if _, err := c.MarkDeleted("A", DeletedByThem); err == nil {
		t.Fatal("expected a save error")
	}
	a.Trim(0) // must not drop the unwritten deletion
	os.Chmod(a.TmpDir(), 0o700)
	if e, _ := c.Get("A"); e.Deleted == "" {
		t.Fatal("deletion lost")
	}
	c.Flush()
	if !strings.Contains(chatText(t, c), "This message was deleted.") {
		t.Error("deletion never written")
	}
}

func TestCrashBetweenIndexAndChatWriteDoesNotDuplicate(t *testing.T) {
	root := t.TempDir()
	a, _ := Open(Options{Root: root, Location: time.UTC})
	c, _, _ := a.EnsureChat("1@s.whatsapp.net", "Dana", "dm", "")
	c.Insert(Row{ID: "a", TS: 1000}, text("a"))
	c.Insert(Row{ID: "c", TS: 2000}, text("c"))
	before, _ := os.ReadFile(c.chatPath())
	c.Insert(Row{ID: "b", TS: 1100}, text("b")) // full rewrite: index, then _chat.txt
	os.WriteFile(c.chatPath(), before, 0o600)   // crash before the _chat.txt rename

	b, _ := Open(Options{Root: root, Location: time.UTC})
	c2, _ := b.Chat("1@s.whatsapp.net")
	c2.Insert(Row{ID: "b", TS: 1100}, text("b")) // WhatsApp delivers it again
	if got := chatText(t, c2); strings.Count(got, "Dana: b") != 1 {
		t.Errorf("got %q", got)
	}
	if c2.Position("c") != 2 {
		t.Errorf("c at %d", c2.Position("c"))
	}
}

func TestRealignAfterCrashInLargeBatch(t *testing.T) {
	// A held batch of 100 older messages: index written, _chat.txt not.
	var rows []Row
	for i := range 100 {
		rows = append(rows, Row{ID: fmt.Sprintf("hist%d", i), TS: int64(1000 + i)})
	}
	var entries []string
	for i := range 5 {
		ts := int64(100000 + i*60)
		rows = append(rows, Row{ID: fmt.Sprintf("old%d", i), TS: ts})
		entries = append(entries, Render(Entry{Time: time.Unix(ts, 0).UTC(), Sender: "Dana", Text: "x"}, DefaultDateFormat))
	}
	got := realign(entries, rows, DefaultDateFormat, time.UTC)
	for i, r := range got {
		if r.ID != fmt.Sprintf("old%d", i) {
			t.Errorf("entry %d got %s", i, r.ID)
		}
	}
}

// Entries written in another zone, with index-only rows from a crash spread
// through a long chat: every entry keeps its own ID except unavoidable
// same-second ties, and none is left without one.
func TestRealignZoneShiftWithIndexOnlyRows(t *testing.T) {
	wrote, _ := time.LoadLocation("Asia/Jakarta") // UTC+7
	now, _ := time.LoadLocation("Asia/Dhaka")     // UTC+6
	r := rand.New(rand.NewSource(1))
	var rows []Row
	var entries, truth []string
	ts := int64(1_700_000_000)
	for i := range 5000 {
		ts += int64(r.Intn(40))
		id := fmt.Sprintf("m%d", i)
		rows = append(rows, Row{ID: id, TS: ts})
		if i > 0 && i%10 == 0 {
			continue // index-only row
		}
		entries = append(entries, Render(Entry{Time: time.Unix(ts, 0).In(wrote), Sender: "Dana", Text: "x"}, DefaultDateFormat))
		truth = append(truth, id)
	}
	wrong, repaired := 0, 0
	for i, g := range realign(entries, rows, DefaultDateFormat, now) {
		switch {
		case g.ID == truth[i]:
		case strings.HasPrefix(g.ID, "repair:"):
			repaired++
		default:
			wrong++
		}
	}
	if repaired != 0 || wrong > 25 {
		t.Errorf("wrong IDs %d, repair IDs %d (of %d)", wrong, repaired, len(entries))
	}
}
