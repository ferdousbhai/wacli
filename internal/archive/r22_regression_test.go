package archive

import (
	"strings"
	"testing"
	"time"
)

func TestColonEdgeSenders(t *testing.T) {
	for _, name := range []string{"::Family::", "Bob:", "a::b", "x:"} {
		root := t.TempDir()
		a, _ := Open(Options{Root: root, Location: time.UTC})
		c, _, _ := a.EnsureChat("1@g.us", name, "group", "")
		c.Insert(Row{ID: "A", TS: 100}, text("first"))
		c.Insert(Row{ID: "B", TS: 200}, func(int) Entry { return Entry{Sender: name, Text: "second"} })
		c.Insert(Row{ID: "C", TS: 300}, func(int) Entry { return Entry{Sender: name, System: true, Text: "You created group"} })
		c.Flush()
		before := chatText(t, c)
		a2, _ := Open(Options{Root: root, Location: time.UTC})
		c2, _ := a2.Chat("1@g.us")
		if !c2.Has("B") || !c2.Has("C") || c2.Len() != 3 {
			t.Errorf("%q: len %d problems %v", name, c2.Len(), a2.Problems())
		}
		c2.Update("A", func(e *Entry) bool { e.Text, e.Edited = "first!", true; return true })
		after := chatText(t, c2)
		if strings.Count(after, EntrySep) != strings.Count(before, EntrySep) {
			t.Errorf("%q entries merged:\n%q\n%q", name, before, after)
		}
	}
}

// A collided short document name ("1-2.pdf" → stem "1") matched inside the
// page count: an edit rewrites the line with an extra space.
func TestEditCollidedShortDocKeepsBytes(t *testing.T) {
	a := openTest(t)
	c, _, _ := a.EnsureChat("1@s.whatsapp.net", "Dana", "dm", "")
	label := DocLabel("1.pdf", 1)
	c.Insert(Row{ID: "D", TS: 100}, func(int) Entry {
		return Entry{Sender: "Dana", Text: "Invoice", DocLabel: label, Omitted: "document omitted"}
	})
	writeFileT(t, c.Dir()+"/00000000-1.pdf", "other")
	src := a.TmpDir() + "/x"
	writeFileT(t, src, "pdf")
	name, err := c.PlaceAttachment("D", src, "00000000-1.pdf")
	if err != nil {
		t.Fatal(err)
	}
	c.Flush()
	before := chatText(t, c)
	c.Update("D", func(e *Entry) bool { e.Edited = true; return true })
	after := chatText(t, c)
	want := strings.Replace(before, "\r\n", " "+editedMarker+"\r\n", 1)
	if after != want {
		t.Errorf("placed as %s\nwant %q\ngot  %q", name, want, after)
	}
}

func TestSplitDocLabelEdges(t *testing.T) {
	for _, tc := range []struct{ body, file, text, label string }{
		{"Invoice 1.pdf • ‎1 page", "00000004-1-2.pdf", "Invoice", "1.pdf • ‎1 page"},
		{"scan a.pdf • ‎3 pages", "00000004-a-2.pdf", "scan", "a.pdf • ‎3 pages"},
		{"x", "00000004-", "x", ""},
		{"x", "00000004--2", "x", ""},
	} {
		if text, label := SplitDocLabel(tc.body, tc.file); text != tc.text || label != tc.label {
			t.Errorf("%q: text %q label %q", tc.body, text, label)
		}
	}
}
