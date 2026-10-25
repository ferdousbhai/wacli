package archive

import (
	"strings"
	"testing"
	"time"
)

// A sender (contact or group) name with a colon not followed by a space.
func TestColonInSenderSurvivesReload(t *testing.T) {
	root := t.TempDir()
	a, _ := Open(Options{Root: root, Location: time.UTC})
	c, _, _ := a.EnsureChat("1@g.us", "Team:Alpha", "group", "")
	c.Insert(Row{ID: "A", TS: 100}, text("first"))
	c.Insert(Row{ID: "B", TS: 200}, func(int) Entry { return Entry{Sender: "Dr.A:Smith", Text: "second"} })
	c.Insert(Row{ID: "C", TS: 300}, func(int) Entry { return Entry{Sender: "Team:Alpha", System: true, Text: "You created group"} })
	before := chatText(t, c)

	a2, _ := Open(Options{Root: root, Location: time.UTC})
	c2, err := a2.Chat("1@g.us")
	if err != nil {
		t.Fatal(err)
	}
	if !c2.Has("B") || !c2.Has("C") {
		t.Errorf("IDs lost: problems %v", a2.Problems())
	}
	if _, err := c2.Update("A", func(e *Entry) bool { e.Text, e.Edited = "first!", true; return true }); err != nil {
		t.Fatal(err)
	}
	after := chatText(t, c2)
	if strings.Count(after, EntrySep) != strings.Count(before, EntrySep) {
		t.Errorf("entries merged:\nbefore %q\nafter  %q", before, after)
	}
	if e, ok := c2.Get("B"); !ok || e.Sender != "Dr.A:Smith" {
		t.Errorf("B: %+v %v", e, ok)
	}
}

// A sender name containing ": " loses its tail when the message is deleted.

// A document whose file got a "-2" from freeName keeps its label on an edit.
func TestEditCollidedDocKeepsLabel(t *testing.T) {
	a := openTest(t)
	c, _, _ := a.EnsureChat("1@s.whatsapp.net", "Dana", "dm", "")
	label := DocLabel("report.pdf", 3)
	for _, id := range []string{"D1", "D2"} {
		c.Insert(Row{ID: id, TS: 100}, func(int) Entry {
			return Entry{Sender: "Dana", Text: "cap", DocLabel: label, Omitted: "document omitted"}
		})
		src := a.TmpDir() + "/x" + id
		writeFileT(t, src, "pdf"+id)
		name, err := c.PlaceAttachment(id, src, "00000000-report.pdf")
		t.Logf("%s placed as %s", id, name)
		if err != nil {
			t.Fatal(err)
		}
	}
	c.Flush()
	e, _ := c.Get("D2")
	if e.DocLabel != label || e.Text != "cap" {
		t.Errorf("D2 parsed text %q label %q", e.Text, e.DocLabel)
	}
}

// A document name whose extension is long once encoded can't be placed.
func TestLongExtensionDocName(t *testing.T) {
	name := AttachmentName(3, "document", time.Unix(0, 0), "v1."+strings.Repeat("বাং", 30))
	if len(name) > 255 {
		t.Errorf("attachment name %d bytes", len(name))
	}
}
