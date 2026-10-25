package archive

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func roundTrip(t *testing.T, content string) {
	t.Helper()
	entries := SplitEntries(content)
	if len(entries) == 0 {
		t.Fatal("no entries")
	}
	var out string
	for _, raw := range entries {
		e, err := Parse(raw, DefaultDateFormat, time.UTC)
		if err != nil {
			t.Fatal(err)
		}
		got := Render(e, DefaultDateFormat)
		if got != raw {
			t.Errorf("round trip changed entry\n got: %q\nwant: %q", got, raw)
		}
		out += got + EntrySep
	}
	if out != content {
		t.Error("round trip changed file bytes")
	}
}

func TestRoundTripFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", ChatFile))
	if err != nil {
		t.Fatal(err)
	}
	roundTrip(t, string(data))
}

// WACLI_REFERENCE_EXPORT points at an unzipped iPhone export to check the
// renderer against a real one. Real exports hold private data and are never
// committed.
func TestRoundTripReferenceExport(t *testing.T) {
	dir := os.Getenv("WACLI_REFERENCE_EXPORT")
	if dir == "" {
		t.Skip("WACLI_REFERENCE_EXPORT not set")
	}
	data, err := os.ReadFile(filepath.Join(dir, ChatFile))
	if err != nil {
		t.Fatal(err)
	}
	roundTrip(t, string(data))
}

func TestParseFields(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", ChatFile))
	if err != nil {
		t.Fatal(err)
	}
	entries := SplitEntries(string(data))
	parse := func(i int) Entry {
		e, err := Parse(entries[i], DefaultDateFormat, time.UTC)
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	if e := parse(0); !e.System || e.Sender != "Acme Co" {
		t.Errorf("system entry: %+v", e)
	}
	if e := parse(2); e.Text != "Hi @⁨Sam Lee⁩ nice to e-meet.\n\nReady for the call?" {
		t.Errorf("multiline text: %q", e.Text)
	}
	if e := parse(4); e.Text != "" || e.Sender != "Sam Lee" {
		t.Errorf("empty entry: %+v", e)
	}
	if e := parse(6); e.Attachment != "00000006-Company Profile.pdf" || e.DocLabel != "Company Profile.pdf • ‎8 pages" {
		t.Errorf("document: %+v", e)
	}
	if e := parse(8); e.Text != "🙏" || e.Attachment != "00000008-PHOTO-2025-10-25-04-22-21.jpg" {
		t.Errorf("captioned photo: %+v", e)
	}
	if e := parse(9); !e.Edited || e.Text != "Thank u Alex" {
		t.Errorf("edited: %+v", e)
	}
	if e := parse(10); e.Deleted != DeletedByThem {
		t.Errorf("deleted: %+v", e)
	}
	if e := parse(12); e.Omitted != "image omitted" {
		t.Errorf("omitted: %+v", e)
	}
	want := time.Date(2025, 10, 24, 4, 58, 4, 0, time.UTC)
	if e := parse(2); !e.Time.Equal(want) {
		t.Errorf("time = %v, want %v", e.Time, want)
	}
}

func TestAttachmentName(t *testing.T) {
	ts := time.Date(2025, 10, 24, 5, 33, 9, 0, time.UTC)
	cases := []struct {
		pos       int
		mediaType string
		doc       string
		want      string
	}{
		{12, "image", "", "00000012-PHOTO-2025-10-24-05-33-09.jpg"},
		{3, "audio", "", "00000003-AUDIO-2025-10-24-05-33-09.opus"},
		{15, "document", "Acme Company Profile.pdf", "00000015-Acme Company Profile.pdf"},
		{39, "document", "Restructuring Invoice - PT. Acme Holdings Indonesia.pdf", "00000039-Restructuring Invoice - PT. Acme Holding.pdf"},
		{1, "document", "../../etc/passwd", "00000001-_.._etc_passwd"},
	}
	for _, c := range cases {
		if got := AttachmentName(c.pos, c.mediaType, ts, c.doc); got != c.want {
			t.Errorf("AttachmentName(%d,%q,%q) = %q, want %q", c.pos, c.mediaType, c.doc, got, c.want)
		}
	}
}

func TestDocLabel(t *testing.T) {
	if got := DocLabel("a.pdf", 8); got != "a.pdf • ‎8 pages" {
		t.Errorf("got %q", got)
	}
	if got := DocLabel("a.pdf", 1); got != "a.pdf • ‎1 page" {
		t.Errorf("got %q", got)
	}
	if got := DocLabel("a.zip", 0); got != "a.zip" {
		t.Errorf("got %q", got)
	}
}

func TestCarriageReturnsStayInsideEntries(t *testing.T) {
	e := Entry{Time: time.Unix(0, 0).UTC(), Sender: "Dana", Text: "line one\r\nline two\rthree"}
	got := Render(e, DefaultDateFormat)
	if got != "[1970-01-01, 00:00:00] Dana: line one\nline two\nthree" {
		t.Errorf("render %q", got)
	}
	// Files written before the fix: a bare "\r\n" inside a message.
	old := "[1970-01-01, 00:00:00] Dana: line one\r\nline two\r\n[1970-01-01, 00:00:01] Sam: hi\r\n"
	if got := SplitEntries(old); len(got) != 2 || got[0] != "[1970-01-01, 00:00:00] Dana: line one\nline two" {
		t.Errorf("split %q", got)
	}
}
