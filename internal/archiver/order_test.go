package archiver

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
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

func TestBackfillCountsOnlyWhatThePagesBrought(t *testing.T) {
	a, _ := newTestArchiver(t)
	a.handle(history(waHistorySync.HistorySync_INITIAL_BOOTSTRAP, dana, "Dana", web(dana, "N1", "", false, t0, text("newest"))))
	a.requestHistory = func(_ context.Context, _ types.MessageInfo, _ int, sent func(types.MessageID)) (types.MessageID, error) {
		sent("REQ")
		a.handle(live(dana, dana, "LIVE", false, time.Now().Unix(), text("meanwhile")))
		page := history(waHistorySync.HistorySync_ON_DEMAND, dana, "Dana", web(dana, "OLD", "", false, t0-60, text("older")))
		page.Notification = &waE2E.HistorySyncNotification{PeerDataRequestSessionID: proto.String("REQ")}
		go a.handle(page)
		return "REQ", nil
	}
	res, err := a.Backfill(context.Background(), dana, 50, 1, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 1 {
		t.Fatalf("added %d, want 1 (the live message isn't backfill's)", res.Added)
	}
}

func TestEditAndDeleteKeepANameHoldingAColon(t *testing.T) {
	a, ar := newTestArchiver(t)
	const bob = "6285555555555@s.whatsapp.net"
	msg := func(id string, ts int64, m *waE2E.Message) *events.Message {
		e := live(group, bob, id, false, ts, m)
		e.Info.PushName = "Bob: CEO"
		return e
	}
	a.handle(msg("B1", t0, text("hello")))
	a.handle(msg("B2", t0+1, text("bye")))
	a.handle(msg("E1", t0+2, &waE2E.Message{EditedMessage: &waE2E.FutureProofMessage{Message: &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type: waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(), Key: &waCommon.MessageKey{RemoteJID: proto.String(group), FromMe: proto.Bool(true), ID: proto.String("B1")},
		EditedMessage: text("hello there"),
	}}}}))
	a.handle(msg("R1", t0+3, &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type: waE2E.ProtocolMessage_REVOKE.Enum(), Key: &waCommon.MessageKey{RemoteJID: proto.String(group), FromMe: proto.Bool(true), ID: proto.String("B2")},
	}}))
	got := chatFile(t, ar, group)
	if !strings.Contains(got, "Bob: CEO: hello there ‎<This message was edited>") || !strings.Contains(got, "Bob: CEO: ‎This message was deleted.") {
		t.Fatalf("got\n%q", got)
	}
}
