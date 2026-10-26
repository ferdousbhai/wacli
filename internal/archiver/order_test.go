package archiver

import (
	"testing"

	"go.mau.fi/whatsmeow/proto/waHistorySync"
)

// The phone sends history newest first; messages sent in the same second
// (an album and its photos, a quick burst) must still come out in order.
func TestSameSecondHistoryKeepsItsOrder(t *testing.T) {
	a, ar := newTestArchiver(t)
	a.handle(history(waHistorySync.HistorySync_INITIAL_BOOTSTRAP, dana, "Dana",
		web(dana, "M3", "", false, t0+10, text("third")),
		web(dana, "M2", "", false, t0, text("second")),
		web(dana, "M1", "", false, t0, text("first")),
	))
	want := "[2025-10-24, 04:39:33] Dana Putri: first\r\n" +
		"[2025-10-24, 04:39:33] Dana Putri: second\r\n" +
		"[2025-10-24, 04:39:43] Dana Putri: third\r\n"
	if got := chatFile(t, ar, dana); got != want {
		t.Fatalf("got\n%q\nwant\n%q", got, want)
	}
}

// A later page walks further back: its messages go before those of the same
// second already archived, and a live message after them.
func TestOlderPageGoesBeforeTheSameSecond(t *testing.T) {
	a, ar := newTestArchiver(t)
	a.handle(history(waHistorySync.HistorySync_INITIAL_BOOTSTRAP, dana, "Dana",
		web(dana, "M3", "", false, t0+10, text("third")),
		web(dana, "M2", "", false, t0, text("second")),
	))
	a.handle(history(waHistorySync.HistorySync_ON_DEMAND, dana, "Dana",
		web(dana, "M1", "", false, t0, text("first")),
		web(dana, "M0", "", false, t0, text("zeroth")),
	))
	a.handle(live(dana, dana, "M4", false, t0+10, text("fourth")))
	want := "[2025-10-24, 04:39:33] Dana Putri: zeroth\r\n" +
		"[2025-10-24, 04:39:33] Dana Putri: first\r\n" +
		"[2025-10-24, 04:39:33] Dana Putri: second\r\n" +
		"[2025-10-24, 04:39:43] Dana Putri: third\r\n" +
		"[2025-10-24, 04:39:43] Dana Putri: fourth\r\n"
	if got := chatFile(t, ar, dana); got != want {
		t.Fatalf("got\n%q\nwant\n%q", got, want)
	}
}
