package archive

import (
	"strings"
	"testing"
)

// A group subject within WhatsApp's 100-character limit can exceed the
// 255-byte file name limit once UTF-8 encoded (here 90 Bengali code points,
// 270 bytes). The chat must still be archived.
func TestLongGroupNameStillArchives(t *testing.T) {
	a := openTest(t)
	name := strings.Repeat("বাং", 30)
	_, _, err := a.EnsureChat("1@g.us", name, "group", "")
	t.Logf("EnsureChat: %v", err)
	c, err := a.Chat("1@g.us") // the record was kept: every later message comes here
	if err != nil {
		t.Fatalf("chat unusable: %v", err)
	}
	if _, _, err := c.Insert(Row{ID: "m", TS: 100}, text("hi")); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if _, err := a.Rename("1@g.us", "short", ""); err != nil {
		t.Fatalf("Rename: %v", err)
	}
}
