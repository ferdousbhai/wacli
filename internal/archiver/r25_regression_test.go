package archiver

import (
	"context"
	"testing"

	"github.com/ferdousbhai/wacli/internal/archive"
)

// A location (or poll, or call) written before the sender's name was known
// keeps the number / "You" after RefreshNames, while the same sender's text
// messages get the name.
func TestRefreshNamesRenamesLocationSenders(t *testing.T) {
	a, ar := newTestArchiver(t)
	c, _, _ := ar.EnsureChat(group, "Maya", "group", "")
	c.Insert(archive.Row{ID: "T", TS: t0, Sender: dana}, func(int) archive.Entry {
		return archive.Entry{Sender: "+62 811-1111-1111", Text: "hi"}
	})
	c.Insert(archive.Row{ID: "L", TS: t0 + 1, Sender: dana}, func(int) archive.Entry {
		return archive.Entry{Sender: "+62 811-1111-1111", System: true, Text: "Location: https://maps.google.com/?q=1,2"}
	})
	c.Insert(archive.Row{ID: "O", TS: t0 + 2, FromMe: true, Sender: me}, func(int) archive.Entry {
		return archive.Entry{Sender: "You", System: true, Text: "Location: https://maps.google.com/?q=1,2"}
	})
	a.RefreshNames(context.Background(), true)
	want := "[2025-10-24, 04:39:33] Dana Putri: hi\r\n" +
		"[2025-10-24, 04:39:34] Dana Putri: ‎Location: https://maps.google.com/?q=1,2\r\n" +
		"[2025-10-24, 04:39:35] Alex: ‎Location: https://maps.google.com/?q=1,2\r\n"
	if got := chatFile(t, ar, group); got != want {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}
}

// A DM with someone known only by push name: once the name arrives, their
// photos and texts must show the same sender.
func TestRefreshNamesPushNameDMMediaAndText(t *testing.T) {
	a, ar := newTestArchiver(t)
	const kim = "6282222222222@s.whatsapp.net"
	c, _, _ := ar.EnsureChat(kim, "+6282222222222", "dm", "")
	c.Insert(archive.Row{ID: "T", TS: t0, Sender: kim}, func(int) archive.Entry {
		return archive.Entry{Sender: "+6282222222222", Text: "hi"}
	})
	c.Insert(archive.Row{ID: "P", TS: t0 + 1, Sender: kim}, func(int) archive.Entry {
		return archive.Entry{Sender: "+6282222222222", Omitted: "image omitted"}
	})
	a.names.mu.Lock()
	a.names.push["6282222222222"] = "Kim"
	a.names.mu.Unlock()
	a.RefreshNames(context.Background(), true)
	got := chatFile(t, ar, kim)
	want := "[2025-10-24, 04:39:33] ~ Kim: hi\r\n‎[2025-10-24, 04:39:34] ~ Kim: ‎image omitted\r\n"
	if got != want {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}
}
