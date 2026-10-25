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
