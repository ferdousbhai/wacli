package archive

import (
	"os"
	"strings"
	"testing"
	"time"
)

// A caption edit. While the document is omitted the label can't be told
// apart from the caption on disk, so the archiver passes it with the edit;
// once attached, it is read back from the line.
func editDoc(t *testing.T, name string, place bool) string {
	a := openTest(t)
	c, _, _ := a.EnsureChat("1@s.whatsapp.net", "Dana", "dm", "")
	label := DocLabel(name, 3)
	c.Insert(Row{ID: "D", TS: 100}, func(int) Entry {
		return Entry{Sender: "Dana", Text: "old caption", DocLabel: label, Omitted: "document omitted"}
	})
	if place {
		src := a.TmpDir() + "/x"
		writeFileT(t, src, "pdf")
		if _, err := c.PlaceAttachment("D", src, AttachmentName(0, "document", time.Unix(0, 0), name)); err != nil {
			t.Fatal(err)
		}
		c.Flush()
	}
	c.Update("D", func(e *Entry) bool {
		if !place {
			e.DocLabel = label
		}
		e.Text, e.Edited = "new caption", true
		return true
	})
	got := chatText(t, c)
	if !strings.Contains(got, label) {
		t.Errorf("doc label lost on edit:\n%q", got)
	}
	return got
}

func TestEditOmittedDocKeepsLabel(t *testing.T)  { editDoc(t, "report.pdf", false) }
func TestEditAttachedDocKeepsLabel(t *testing.T) { editDoc(t, "report.pdf", true) }
func TestEditLongNamedDocKeepsLabel(t *testing.T) {
	editDoc(t, "Restructuring Invoice - PT. Acme Holding Company.pdf", true)
}

func writeFileT(t *testing.T, p, s string) {
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestParseDocLabelWithShortName(t *testing.T) {
	for _, tc := range []struct {
		name  string
		pages uint32
	}{{"1.pdf", 1}, {"a.pdf", 3}, {"t.txt", 0}, {"page.pdf", 2}} {
		label := DocLabel(tc.name, tc.pages)
		e := Entry{Time: time.Unix(0, 0).UTC(), Sender: "Dana", Text: "cap", DocLabel: label, Attachment: AttachmentName(3, "document", time.Unix(0, 0), tc.name)}
		got, _ := Parse(Render(e, DefaultDateFormat), DefaultDateFormat, time.UTC)
		if got.DocLabel != label || got.Text != "cap" {
			t.Errorf("%s: text %q label %q", tc.name, got.Text, got.DocLabel)
		}
	}
}

func TestEditAttachedShortNamedDocKeepsLabel(t *testing.T) { editDoc(t, "a.pdf", true) }

func TestSplitDocLabel(t *testing.T) {
	for _, tc := range []struct{ body, file, text, label string }{
		{"See attached pages.pdf • ‎3 pages", "00000004-pages.pdf", "See attached", "pages.pdf • ‎3 pages"},
		{"Invoice 1.pdf • ‎1 page", "00000004-1.pdf", "Invoice", "1.pdf • ‎1 page"},
		{"Please sign this document", "00000004-document", "Please sign this document", ""},
		{"Restructuring Invoice - PT. Acme Holding Company.pdf", "00000000-Restructuring Invoice - PT. Acme Holding.pdf", "", "Restructuring Invoice - PT. Acme Holding Company.pdf"},
	} {
		if text, label := SplitDocLabel(tc.body, tc.file); text != tc.text || label != tc.label {
			t.Errorf("%q: text %q label %q", tc.body, text, label)
		}
	}
}
