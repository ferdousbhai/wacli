package archive

import (
	"strings"
	"testing"
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
