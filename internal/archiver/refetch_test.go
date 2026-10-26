package archiver

import (
	"strings"
	"testing"

	"github.com/ferdousbhai/wacli/internal/archive"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestRefetchedHistoryFillsEmptyLinesAndNamesTheirType(t *testing.T) {
	a, ar := newTestArchiver(t)
	unknown := &waE2E.Message{RequestPhoneNumberMessage: &waE2E.RequestPhoneNumberMessage{}}
	album := &waE2E.Message{AlbumMessage: &waE2E.AlbumMessage{ExpectedImageCount: proto.Uint32(2)}}
	a.handle(history(waHistorySync.HistorySync_INITIAL_BOOTSTRAP, dana, "Dana",
		web(dana, "U1", "", false, t0, unknown),
		web(dana, "A1", "", false, t0+10, album),
		web(dana, "D1", "", false, t0+20, unknown),
		web(dana, "N1", "", false, t0+30, text("after")),
	))
	c, _ := ar.Chat(dana)
	for _, id := range []string{"U1", "A1", "D1"} {
		if e, _ := c.Get(id); !blank(e) {
			t.Fatalf("%s should start as an empty line: %+v", id, e)
		}
	}

	ch := make(chan refetchAnswer, 8)
	for _, id := range []string{"U1", "A1", "D1"} {
		a.refetch[id] = ch
	}
	deleted := web(dana, "D1", "", false, t0+20, nil)
	deleted.Message.MessageStubType = waWeb.WebMessageInfo_REVOKE.Enum()
	a.handle(history(waHistorySync.HistorySync_ON_DEMAND, dana, "Dana",
		web(dana, "U1", "", false, t0, text("Now readable")),
		web(dana, "A1", "", false, t0+10, album),
		deleted,
	))
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

func TestAnchorAfterSkipsWaclisOwnIDs(t *testing.T) {
	rows := []archive.Row{{ID: "E1"}, {ID: "wacli:x"}, {ID: "import:y"}, {ID: "M2"}}
	if r, ok := anchorAfter(rows, 0); !ok || r.ID != "M2" {
		t.Fatalf("anchor %v %v", r, ok)
	}
	if _, ok := anchorAfter(rows, 3); ok {
		t.Fatal("the newest message has nothing after it")
	}
}

func TestCheckedLinesAreRememberedAndNotAskedAgain(t *testing.T) {
	a, ar := newTestArchiver(t)
	album := &waE2E.Message{AlbumMessage: &waE2E.AlbumMessage{ExpectedImageCount: proto.Uint32(2)}}
	a.handle(history(waHistorySync.HistorySync_INITIAL_BOOTSTRAP, dana, "Dana",
		web(dana, "A1", "", false, t0, album),
		web(dana, "A2", "", false, t0+10, album),
		web(dana, "N1", "", false, t0+20, text("after")),
	))
	jobs, err := a.refetchJobs(RefetchOptions{Count: 50})
	if err != nil || len(jobs) != 1 || len(jobs[0].want) != 2 || jobs[0].anchor.ID != "N1" {
		t.Fatalf("jobs %+v %v", jobs, err)
	}
	a.checked.note(dana, "A1", "album")
	a.checked.save(ar.TmpDir(), t.Logf)

	again := loadCheckedLines(ar.Root(), t.Logf) // after a restart
	if !again.has(dana, "A1") || again.has(dana, "A2") {
		t.Fatalf("record %v", again.kinds)
	}
	a.checked = again
	jobs, _ = a.refetchJobs(RefetchOptions{Count: 50})
	if len(jobs) != 1 || len(jobs[0].want) != 1 || jobs[0].want[0].ID != "A2" {
		t.Fatalf("checked line asked again: %+v", jobs)
	}
	if jobs, _ = a.refetchJobs(RefetchOptions{Count: 50, Again: true}); len(jobs[0].want) != 2 {
		t.Fatalf("--again: %+v", jobs)
	}
}

func TestPhoneUseStartsARefetchRound(t *testing.T) {
	a, _ := newTestArchiver(t)
	signalled := func() bool {
		select {
		case <-a.phoneSeen:
			return true
		default:
			return false
		}
	}
	other := live(dana, me, "M1", true, t0, text("from the desktop app"))
	other.Info.Sender.Device = 3
	a.handle(other)
	a.handle(live(dana, dana, "M2", false, t0+1, text("from Dana")))
	if signalled() {
		t.Fatal("not the phone")
	}
	a.handle(live(dana, me, "M3", true, t0+2, text("from the phone")))
	if !signalled() {
		t.Fatal("a message sent from the phone is a sign it's in use")
	}
	read := &events.Receipt{MessageSource: types.MessageSource{Chat: mustJID(dana), Sender: mustJID(me), IsFromMe: true}, Type: types.ReceiptTypeRead}
	a.handle(read)
	if !signalled() {
		t.Fatal("a chat read on the phone is a sign it's in use")
	}
}
