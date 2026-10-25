package archive

import (
	"testing"
	"time"
)

func TestEditOfTextStartingWithLRM(t *testing.T) {
	a, _ := Open(Options{Root: t.TempDir(), Location: time.UTC})
	c, _, _ := a.EnsureChat("1@s.whatsapp.net", "Dana", "dm", "")
	c.Insert(Row{ID: "A", TS: 100}, text("‎hello"))
	for _, s := range []string{"‎hello!", "‎hello!!"} {
		c.Update("A", func(e *Entry) bool { e.Text, e.System, e.Edited = s, false, true; return true })
	}
	if got, want := chatText(t, c), "[1970-01-01, 00:01:40] Dana: ‎hello!! ‎<This message was edited>\r\n"; got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

// Parse reads a text starting with U+200E as a system line; rendering it
// again must keep its edited mark.
func TestRoundTripEditedLRMText(t *testing.T) {
	raw := "[1970-01-01, 00:01:40] Dana: ‎hello ‎<This message was edited>"
	e, err := Parse(raw, "", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if got := Render(e, ""); got != raw {
		t.Errorf("got %q", got)
	}
}
