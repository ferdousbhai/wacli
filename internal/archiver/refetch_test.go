package archiver

import (
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// resent is the phone's answer to a resend request, as whatsmeow delivers it.
func resent(req types.MessageID, chat, sender, id string, fromMe bool, ts int64, msg *waE2E.Message, stub waWeb.WebMessageInfo_StubType) *events.Message {
	evt := live(chat, sender, id, fromMe, ts, msg)
	evt.UnavailableRequestID = req
	evt.SourceWebMsg = &waWeb.WebMessageInfo{
		Key:              &waCommon.MessageKey{RemoteJID: proto.String(chat), FromMe: proto.Bool(fromMe), ID: proto.String(id)},
		MessageTimestamp: proto.Uint64(uint64(ts)),
		Message:          msg,
	}
	if stub != 0 {
		evt.SourceWebMsg.MessageStubType = stub.Enum()
	}
	return evt
}

func TestRefetchAnswersFillEmptyLinesAndNameTheirType(t *testing.T) {
	a, ar := newTestArchiver(t)
	unknown := &waE2E.Message{RequestPhoneNumberMessage: &waE2E.RequestPhoneNumberMessage{}}
	album := &waE2E.Message{AlbumMessage: &waE2E.AlbumMessage{ExpectedImageCount: proto.Uint32(2)}}
	a.handle(history(waHistorySync.HistorySync_INITIAL_BOOTSTRAP, dana, "Dana",
		web(dana, "U1", "", false, t0, unknown),
		web(dana, "A1", "", false, t0+10, album),
		web(dana, "D1", "", false, t0+20, unknown),
	))
	c, _ := ar.Chat(dana)
	for _, id := range []string{"U1", "A1", "D1"} {
		if e, _ := c.Get(id); !blank(e) {
			t.Fatalf("%s should start as an empty line: %+v", id, e)
		}
	}

	ch := make(chan refetchAnswer, 8)
	a.refetch["REQ"] = ch
	a.handle(resent("REQ", dana, dana, "U1", false, t0, text("Now readable"), 0))
	a.handle(resent("REQ", dana, dana, "A1", false, t0+10, album, 0))
	a.handle(resent("REQ", dana, dana, "D1", false, t0+20, nil, waWeb.WebMessageInfo_REVOKE))
	got := map[string]refetchAnswer{}
	for range 3 {
		ans := <-ch
		got[ans.id] = ans
	}
	if ans := got["U1"]; ans.kind != "text" || !ans.filled {
		t.Errorf("U1: %+v", ans)
	}
	if ans := got["A1"]; ans.kind != "album" || ans.filled {
		t.Errorf("A1: %+v", ans)
	}
	if ans := got["D1"]; ans.kind != "deleted" || !ans.filled {
		t.Errorf("D1: %+v", ans)
	}
	file := chatFile(t, ar, dana)
	if !strings.Contains(file, "Dana Putri: Now readable\r\n") || !strings.Contains(file, "Dana Putri: ‎This message was deleted.\r\n") {
		t.Fatalf("lines not filled in:\n%q", file)
	}
}

func TestResendAnswerToSomeoneElsesRequestIsArchivedAsUsual(t *testing.T) {
	a, ar := newTestArchiver(t)
	a.handle(resent("NOT-OURS", dana, dana, "M1", false, t0, text("Hello"), 0))
	if file := chatFile(t, ar, dana); !strings.Contains(file, "Dana Putri: Hello\r\n") {
		t.Fatalf("message lost:\n%q", file)
	}
}
