package archiver

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ferdousbhai/wacli/internal/archive"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

var albumMsg = &waE2E.Message{AlbumMessage: &waE2E.AlbumMessage{ExpectedImageCount: proto.Uint32(2)}}

// unknownMsg is a type wacli can't show: archived as an empty line.
var unknownMsg = &waE2E.Message{RequestPhoneNumberMessage: &waE2E.RequestPhoneNumberMessage{}}

// fakePhone answers on-demand history requests from its own copy of a chat
// (oldest first): the count messages before the anchor, newest first, in a
// reply naming the request, as the phone does.
type fakePhone struct {
	a       *Archiver
	chat    string
	msgs    []*waHistorySync.HistorySyncMsg
	silent  bool
	session func(req string) string // what a reply names; default the request

	mu       sync.Mutex
	requests []types.MessageInfo
}

func (p *fakePhone) install() { p.a.requestHistory = p.request }

func (p *fakePhone) request(_ context.Context, info types.MessageInfo, count int, sent func(types.MessageID)) (types.MessageID, error) {
	p.mu.Lock()
	id := types.MessageID(fmt.Sprintf("REQ%d", len(p.requests)))
	p.requests = append(p.requests, info)
	p.mu.Unlock()
	if sent != nil {
		sent(id)
	}
	if p.silent {
		return id, nil
	}
	end := slices.IndexFunc(p.msgs, func(m *waHistorySync.HistorySyncMsg) bool {
		return m.GetMessage().GetKey().GetID() == string(info.ID)
	})
	if end < 0 { // an anchor it doesn't know: by time
		end = 0
		for end < len(p.msgs) && int64(p.msgs[end].GetMessage().GetMessageTimestamp()) < info.Timestamp.Unix() {
			end++
		}
	}
	page := slices.Clone(p.msgs[max(0, end-count):end])
	slices.Reverse(page)
	session := string(id)
	if p.session != nil {
		session = p.session(session)
	}
	evt := history(waHistorySync.HistorySync_ON_DEMAND, p.chat, "Dana", page...)
	evt.Notification = &waE2E.HistorySyncNotification{PeerDataRequestSessionID: proto.String(session)}
	go p.a.handle(evt)
	return id, nil
}

func (p *fakePhone) anchors() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var ids []string
	for _, r := range p.requests {
		ids = append(ids, string(r.ID))
	}
	return ids
}

// archiveEmptyLines archives msgs as an older wacli did: types it couldn't
// show stay empty lines, so their type isn't recorded.
func archiveEmptyLines(t *testing.T, a *Archiver, msgs ...*waHistorySync.HistorySyncMsg) {
	t.Helper()
	a.handle(history(waHistorySync.HistorySync_INITIAL_BOOTSTRAP, dana, "Dana", msgs...))
	a.checked.kinds = map[string]map[string]string{} // as before this version
}

func kinds(res RefetchResult) map[string]string {
	k := map[string]string{}
	for _, it := range res.Items {
		k[it.ID] = it.Kind
		if it.Filled {
			k[it.ID] += " (filled)"
		}
	}
	return k
}

func TestRefetchFillsLinesAndFollowsTheReplyBack(t *testing.T) {
	a, ar := newTestArchiver(t)
	archiveEmptyLines(t, a,
		web(dana, "A1", "", false, t0, albumMsg),
		web(dana, "X", "", false, t0+6, text("x")),
		web(dana, "U1", "", false, t0+10, unknownMsg),
		web(dana, "N1", "", false, t0+20, text("after")),
	)
	// The phone also has messages the archive lacks, between A1 and X.
	phone := &fakePhone{a: a, chat: dana, msgs: []*waHistorySync.HistorySyncMsg{
		web(dana, "A1", "", false, t0, albumMsg),
		web(dana, "P1", "", false, t0+1, text("p1")),
		web(dana, "P2", "", false, t0+2, text("p2")),
		web(dana, "P3", "", false, t0+3, text("p3")),
		web(dana, "P4", "", false, t0+4, text("p4")),
		web(dana, "X", "", false, t0+6, text("x")),
		web(dana, "U1", "", false, t0+10, text("Now readable")),
		web(dana, "N1", "", false, t0+20, text("after")),
	}}
	phone.install()
	res, err := a.Refetch(context.Background(), RefetchOptions{Count: 3, Wait: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"U1": "text (filled)", "A1": "album"}
	if got := kinds(res); !maps.Equal(got, want) || res.Left != 0 {
		t.Fatalf("got %v left %d, want %v; anchors %v", got, res.Left, want, phone.anchors())
	}
	// A1 was beyond the first reply: asked again from its oldest message.
	if anchors := phone.anchors(); len(anchors) != 3 || anchors[2] != "P2" {
		t.Fatalf("anchors %v", anchors)
	}
	if !strings.Contains(chatFile(t, ar, dana), "Dana Putri: Now readable\r\n") {
		t.Fatal("line not filled in")
	}
	if !a.checked.has(dana, "A1") || !a.checked.has(dana, "U1") {
		t.Fatal("types not recorded")
	}
	if jobs, _ := a.refetchJobs(context.Background(), RefetchOptions{Count: 50}); len(jobs) != 0 {
		t.Fatalf("checked lines asked again: %+v", jobs)
	}
}

func TestRefetchRecordsWhatThePhoneNoLongerHas(t *testing.T) {
	a, _ := newTestArchiver(t)
	archiveEmptyLines(t, a,
		web(dana, "M1", "", false, t0, text("m1")),
		web(dana, "D1", "", false, t0+10, unknownMsg),
		web(dana, "G1", "", false, t0+15, unknownMsg),
		web(dana, "N1", "", false, t0+20, text("after")),
	)
	deleted := web(dana, "D1", "", false, t0+10, nil)
	deleted.Message.MessageStubType = waWeb.WebMessageInfo_REVOKE.Enum()
	(&fakePhone{a: a, chat: dana, msgs: []*waHistorySync.HistorySyncMsg{ // no G1
		web(dana, "M1", "", false, t0, text("m1")),
		deleted,
		web(dana, "N1", "", false, t0+20, text("after")),
	}}).install()
	res, err := a.Refetch(context.Background(), RefetchOptions{Count: 50, Wait: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"D1": "deleted (filled)", "G1": "not on phone"}
	if got := kinds(res); !maps.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestRefetchIgnoresRepliesToOtherRequests(t *testing.T) {
	a, _ := newTestArchiver(t)
	archiveEmptyLines(t, a,
		web(dana, "U1", "", false, t0, unknownMsg),
		web(dana, "N1", "", false, t0+20, text("after")),
	)
	// A reply to someone else's request (a backfill, say) that lacks U1.
	(&fakePhone{a: a, chat: dana, session: func(string) string { return "BACKFILL" }, msgs: []*waHistorySync.HistorySyncMsg{
		web(dana, "N1", "", false, t0+20, text("after")),
	}}).install()
	res, err := a.Refetch(context.Background(), RefetchOptions{Count: 50, Wait: 300 * time.Millisecond, MaxRequests: 5})
	if !errors.Is(err, ErrPhoneQuiet) || res.Left != 1 || a.checked.has(dana, "U1") {
		t.Fatalf("err %v left %d recorded %v", err, res.Left, a.checked.has(dana, "U1"))
	}
}

func TestReplyWithoutSessionIsMatchedByChatUnlessABackfillWaits(t *testing.T) {
	a, _ := newTestArchiver(t)
	w := &refetchWaiter{reqID: "R", chat: dana, replies: make(chan historyBatch, 1)}
	a.refetchWait = w
	if a.deliverRefetch("", group, historyBatch{}) || a.deliverRefetch("OTHER", dana, historyBatch{}) {
		t.Fatal("another chat's or request's reply taken")
	}
	a.backfill[dana] = nil
	if a.deliverRefetch("", dana, historyBatch{}) {
		t.Fatal("a waiting backfill's page taken")
	}
	delete(a.backfill, dana)
	if !a.deliverRefetch("", dana, historyBatch{n: 1}) || !a.deliverRefetch("R", group, historyBatch{}) {
		t.Fatal("own reply refused")
	}
}

func TestLockedPhoneLeavesLinesUncheckedButADeadChatGivesUp(t *testing.T) {
	a, _ := newTestArchiver(t)
	archiveEmptyLines(t, a,
		web(dana, "U1", "", false, t0, unknownMsg),
		web(dana, "N1", "", false, t0+20, text("after")),
	)
	(&fakePhone{a: a, chat: dana, silent: true}).install()
	for range noAnswerAfter + 1 {
		if _, err := a.Refetch(context.Background(), RefetchOptions{Wait: 20 * time.Millisecond}); !errors.Is(err, ErrPhoneQuiet) {
			t.Fatal(err)
		}
	}
	if a.checked.has(dana, "U1") {
		t.Fatal("a locked phone's silence recorded as an answer")
	}
	// Silent about U1's chat while answering about another: it gives up.
	for i := range noAnswerAfter {
		a.refetchStrikes["U1"] = i
	}
	j := refetchJob{c: mustChat(t, a, dana), anchor: archive.Row{ID: "N1"}, want: []archive.Row{{ID: "U1"}}}
	a.strike(j, j.want, func(_ refetchJob, row archive.Row, kind string, _ bool) {
		if kind != "no answer" {
			t.Fatalf("kind %q", kind)
		}
	})
}

func TestRefetchRoundCarriesOnAfterTheLastChat(t *testing.T) {
	a, ar := newTestArchiver(t)
	for _, jid := range []string{dana, group} {
		a.handle(history(waHistorySync.HistorySync_INITIAL_BOOTSTRAP, jid, "Chat", web(jid, "U-"+jid, "", false, t0, unknownMsg), web(jid, "N-"+jid, "", false, t0+20, text("after"))))
	}
	a.checked.kinds = map[string]map[string]string{} // as before this version
	phone := &fakePhone{a: a, silent: true}
	phone.install()
	first, _ := a.Refetch(context.Background(), RefetchOptions{MaxRequests: 1, Wait: 20 * time.Millisecond})
	next, _ := a.Refetch(context.Background(), RefetchOptions{MaxRequests: 1, Wait: 20 * time.Millisecond, cursor: first.last})
	if first.last == next.last {
		t.Fatalf("both rounds asked about %s first (folders %v)", first.last, ar.Chats())
	}
}

func TestNewEmptyLinesAreRecordedAsArchived(t *testing.T) {
	a, _ := newTestArchiver(t)
	a.handle(history(waHistorySync.HistorySync_INITIAL_BOOTSTRAP, dana, "Dana",
		web(dana, "A1", "", false, t0, albumMsg),
		web(dana, "U1", "", false, t0+10, unknownMsg),
	))
	if !a.checked.has(dana, "A1") || !a.checked.has(dana, "U1") {
		t.Fatalf("not recorded: %v", a.checked.kinds)
	}
	if jobs, _ := a.refetchJobs(context.Background(), RefetchOptions{Count: 50}); len(jobs) != 0 {
		t.Fatalf("new lines need no refetch: %+v", jobs)
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

func TestCheckedLinesSurviveARestart(t *testing.T) {
	a, ar := newTestArchiver(t)
	a.checked.note(dana, "A1", "album")
	a.checked.save(ar.TmpDir(), t.Logf)
	if again := loadCheckedLines(ar.Root(), t.Logf); !again.has(dana, "A1") {
		t.Fatalf("record %v", again.kinds)
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
	now := time.Now().Unix()
	other := live(dana, me, "M1", true, now, text("from the desktop app"))
	other.Info.Sender.Device = 3
	a.handle(other)
	a.handle(live(dana, dana, "M2", false, now, text("from Dana")))
	a.handle(live(dana, me, "M3", true, now-3600, text("sent from the phone an hour ago, delivered now")))
	if signalled() {
		t.Fatal("not a sign the phone is in use now")
	}
	a.handle(live(dana, me, "M4", true, now, text("from the phone")))
	if !signalled() {
		t.Fatal("a message sent from the phone is a sign it's in use")
	}
	read := &events.Receipt{MessageSource: types.MessageSource{Chat: mustJID(dana), Sender: mustJID(me), IsFromMe: true}, Type: types.ReceiptTypeRead, Timestamp: time.Now()}
	a.handle(read)
	if !signalled() {
		t.Fatal("a chat read on the phone is a sign it's in use")
	}
}

func mustChat(t *testing.T, a *Archiver, jid string) *archive.Chat {
	t.Helper()
	c, err := a.ar.Chat(jid)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
