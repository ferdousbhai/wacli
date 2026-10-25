package archiver

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ferdousbhai/wacli/internal/archive"
	"github.com/ferdousbhai/wacli/internal/sqliteutil"
	"github.com/ferdousbhai/wacli/internal/wa"
	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

const (
	me    = "15550000000@s.whatsapp.net"
	dana  = "6281111111111@s.whatsapp.net"
	group = "120363000000000001@g.us"
)

func newTestArchiver(t *testing.T) (*Archiver, *archive.Archive) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "session.db")
	ctx := context.Background()
	container, err := sqlstore.New(ctx, "sqlite3", sqliteutil.FileURI(path, "_foreign_keys=on"), nil)
	if err != nil {
		t.Fatal(err)
	}
	device := container.NewDevice()
	device.ID = &types.JID{User: "15550000000", Device: 7, Server: types.DefaultUserServer}
	device.PushName = "Alex"
	device.Account = &waAdv.ADVSignedDeviceIdentity{Details: []byte{1}, AccountSignature: make([]byte, 64), AccountSignatureKey: make([]byte, 32), DeviceSignature: make([]byte, 64)}
	if err := device.Save(ctx); err != nil {
		t.Fatal(err)
	}
	if err := device.Contacts.PutContactName(ctx, types.NewJID("6281111111111", types.DefaultUserServer), "Dana", "Dana Putri"); err != nil {
		t.Fatal(err)
	}
	// A profile name whatsmeow kept from an earlier run.
	if _, _, err := device.Contacts.PutPushName(ctx, types.NewJID("6283333333333", types.DefaultUserServer), "Kim"); err != nil {
		t.Fatal(err)
	}
	container.Close()

	client, err := wa.New(wa.Options{StorePath: path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	ar, err := archive.Open(archive.Options{Root: filepath.Join(dir, "WhatsApp"), Location: time.UTC})
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(ar, client, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	a.names.noteGroup(group, "Maya")
	return a, ar
}

func chatFile(t *testing.T, ar *archive.Archive, jid string) string {
	t.Helper()
	c, err := ar.Chat(jid)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(c.Dir(), archive.ChatFile))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func web(chat, id, participant string, fromMe bool, ts int64, msg *waE2E.Message) *waHistorySync.HistorySyncMsg {
	return &waHistorySync.HistorySyncMsg{Message: &waWeb.WebMessageInfo{
		Key:              &waCommon.MessageKey{RemoteJID: proto.String(chat), FromMe: proto.Bool(fromMe), ID: proto.String(id), Participant: optional(participant)},
		MessageTimestamp: proto.Uint64(uint64(ts)),
		Message:          msg,
	}}
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func text(s string) *waE2E.Message { return &waE2E.Message{Conversation: proto.String(s)} }

func history(syncType waHistorySync.HistorySync_HistorySyncType, chat, name string, msgs ...*waHistorySync.HistorySyncMsg) *events.HistorySync {
	return &events.HistorySync{Data: &waHistorySync.HistorySync{
		SyncType:      syncType.Enum(),
		Conversations: []*waHistorySync.Conversation{{ID: proto.String(chat), Name: optional(name), Messages: msgs}},
	}}
}

func live(chat, sender, id string, fromMe bool, ts int64, msg *waE2E.Message) *events.Message {
	c, _ := types.ParseJID(chat)
	s, _ := types.ParseJID(sender)
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: c, Sender: s, IsFromMe: fromMe, IsGroup: c.Server == types.GroupServer},
			ID:            types.MessageID(id),
			Timestamp:     time.Unix(ts, 0),
		},
		Message: msg,
	}
}

const t0 = 1761280773 // 2025-10-24 04:39:33 UTC

func TestHistoryThenLiveMessagesBuildExport(t *testing.T) {
	a, ar := newTestArchiver(t)
	// History arrives newest first and overlapping, like real syncs.
	a.handle(history(waHistorySync.HistorySync_INITIAL_BOOTSTRAP, group, "Maya",
		web(group, "M3", dana, false, t0+300, text("Thank u Alex")),
		web(group, "M1", "", true, t0+60, text("Key points:\n- restructure\n- KITAS")),
	))
	a.handle(history(waHistorySync.HistorySync_RECENT, group, "Maya",
		web(group, "M2", dana, false, t0+120, text("Will send the invoice")),
		web(group, "M1", "", true, t0+60, text("Key points:\n- restructure\n- KITAS")),
	))
	a.handle(live(group, dana, "M4", false, t0+400, text("Hi @15550000000 any update?")))
	a.handle(live(group, me, "M5", true, t0+500, text("Please refund")))

	want := "[2025-10-24, 04:40:33] Alex: Key points:\n- restructure\n- KITAS\r\n" +
		"[2025-10-24, 04:41:33] Dana Putri: Will send the invoice\r\n" +
		"[2025-10-24, 04:44:33] Dana Putri: Thank u Alex\r\n" +
		"[2025-10-24, 04:46:13] Dana Putri: Hi @15550000000 any update?\r\n" +
		"[2025-10-24, 04:47:53] Alex: Please refund\r\n"
	if got := chatFile(t, ar, group); got != want {
		t.Errorf("_chat.txt:\n%s\nwant:\n%s", got, want)
	}
	if c, _ := ar.Chat(group); filepath.Base(c.Dir()) != "WhatsApp Chat - Maya" {
		t.Errorf("folder %s", c.Dir())
	}
}

func TestMentionsRenderLikeIOS(t *testing.T) {
	a, ar := newTestArchiver(t)
	msg := &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
		Text:        proto.String("Hi @6281111111111 ready?"),
		ContextInfo: &waE2E.ContextInfo{MentionedJID: []string{dana}},
	}}
	a.handle(live(group, me, "M1", true, t0, msg))
	if got := chatFile(t, ar, group); !strings.Contains(got, "Hi @⁨Dana Putri⁩ ready?") {
		t.Errorf("got %q", got)
	}
}

func TestEditAndDeleteRewriteTheEntry(t *testing.T) {
	a, ar := newTestArchiver(t)
	a.handle(live(dana, dana, "M1", false, t0, text("Thanks")))
	a.handle(live(dana, dana, "M2", false, t0+10, text("wrong chat")))
	a.handle(live(dana, dana, "E1", false, t0+20, &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type: waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(),
		// Keys are from the sender's side: in a DM, RemoteJID is the recipient.
		Key:           &waCommon.MessageKey{RemoteJID: proto.String(me), FromMe: proto.Bool(true), ID: proto.String("M1")},
		EditedMessage: text("Thank u Alex"),
	}}))
	a.handle(live(dana, dana, "R1", false, t0+30, &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type: waE2E.ProtocolMessage_REVOKE.Enum(),
		Key:  &waCommon.MessageKey{RemoteJID: proto.String(me), FromMe: proto.Bool(true), ID: proto.String("M2")},
	}}))
	want := "[2025-10-24, 04:39:33] Dana Putri: Thank u Alex ‎<This message was edited>\r\n" +
		"[2025-10-24, 04:39:43] Dana Putri: ‎This message was deleted.\r\n"
	if got := chatFile(t, ar, dana); got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
	a.handle(live(group, dana, "G1", false, t0, text("oops")))
	a.handle(live(group, dana, "GR", false, t0+30, &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type: waE2E.ProtocolMessage_REVOKE.Enum(),
		Key:  &waCommon.MessageKey{RemoteJID: proto.String(group), FromMe: proto.Bool(true), ID: proto.String("G1")},
	}}))
	if got := chatFile(t, ar, group); !strings.HasSuffix(got, "Dana Putri: ‎This message was deleted.\r\n") {
		t.Errorf("group: %q", got)
	}
	if len(ar.Chats()) != 2 {
		t.Errorf("chats: %+v", ar.Chats()) // no self-chat from the keys
	}
}

func TestMediaEntryWaitsForDownload(t *testing.T) {
	a, ar := newTestArchiver(t)
	img := &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
		Caption: proto.String("🙏"), DirectPath: proto.String("/v/t62/x"), MediaKey: make([]byte, 32),
		FileSHA256: make([]byte, 32), FileEncSHA256: make([]byte, 32), FileLength: proto.Uint64(10),
	}}
	a.handle(live(group, me, "M1", true, t0, text("transfer done")))
	a.handle(live(group, me, "M2", true, t0+60, img))
	got := chatFile(t, ar, group)
	if !strings.HasSuffix(got, "‎[2025-10-24, 04:40:33] Alex: 🙏 ‎image omitted\r\n") {
		t.Errorf("before download: %q", got)
	}
	if a.PendingMedia() != 1 {
		t.Fatalf("pending = %d", a.PendingMedia())
	}
	// Simulate the worker finishing: file on disk, entry updated.
	c, _ := ar.Chat(group)
	name := "00000001-PHOTO-2025-10-24-04-40-33.jpg"
	if err := os.WriteFile(filepath.Join(c.Dir(), name), []byte("jpg"), 0o600); err != nil {
		t.Fatal(err)
	}
	c.Update("M2", func(e *archive.Entry) bool { e.Omitted, e.Attachment = "", name; return true })
	got = chatFile(t, ar, group)
	if !strings.HasSuffix(got, "‎[2025-10-24, 04:40:33] Alex: 🙏 ‎<attached: "+name+">\r\n") {
		t.Errorf("after download: %q", got)
	}
}

func TestDocumentsKeepNameAndPageCount(t *testing.T) {
	a, ar := newTestArchiver(t)
	doc := &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{
		FileName: proto.String("Restructuring Invoice - PT. Acme Holdings Indonesia.pdf"), PageCount: proto.Uint32(4),
		DirectPath: proto.String("/v/x"), MediaKey: make([]byte, 32), FileSHA256: make([]byte, 32), FileEncSHA256: make([]byte, 32),
	}}
	a.handle(live(group, dana, "D1", false, t0, doc))
	want := "‎[2025-10-24, 04:39:33] Dana Putri: Restructuring Invoice - PT. Acme Holdings Indonesia.pdf • ‎4 pages ‎document omitted\r\n"
	if got := chatFile(t, ar, group); got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
	a.media.mu.Lock()
	var name string
	for _, p := range a.media.items {
		name = p.Name
	}
	a.media.mu.Unlock()
	if name != "00000000-Restructuring Invoice - PT. Acme Holding.pdf" {
		t.Errorf("attachment name %q", name)
	}
}

func TestSystemStubsAttributedToChat(t *testing.T) {
	a, ar := newTestArchiver(t)
	create := &waHistorySync.HistorySyncMsg{Message: &waWeb.WebMessageInfo{
		Key:                   &waCommon.MessageKey{RemoteJID: proto.String(group), FromMe: proto.Bool(true), ID: proto.String("S1")},
		MessageTimestamp:      proto.Uint64(t0),
		MessageStubType:       waWeb.WebMessageInfo_GROUP_CREATE.Enum(),
		MessageStubParameters: []string{"ACME HOLDINGS"},
	}}
	a.handle(history(waHistorySync.HistorySync_INITIAL_BOOTSTRAP, group, "Maya", create))
	want := "[2025-10-24, 04:39:33] Maya: ‎You created group “ACME HOLDINGS”\r\n"
	if got := chatFile(t, ar, group); got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

// The bug that started this: an old message already on disk made backfill
// stop before the gap in the middle of the chat.
func TestBackfillCursorWalksFromNewest(t *testing.T) {
	a, ar := newTestArchiver(t)
	a.handle(history(waHistorySync.HistorySync_INITIAL_BOOTSTRAP, group, "Maya",
		web(group, "OLD", "", true, t0, text("You were added")),
		web(group, "NEW", "", true, t0+86400*300, text("final notice")),
	))
	c, _ := ar.Chat(group)
	cur := newestAnchor(c.Rows())
	if cur == nil || cur.ID != "NEW" {
		t.Fatalf("anchor = %+v, want NEW (not the stray oldest message)", cur)
	}
	// An on-demand page answering from NEW fills the gap and moves the cursor.
	got := make(chan historyBatch, 1)
	a.backfill[group] = got
	a.handle(history(waHistorySync.HistorySync_ON_DEMAND, group, "Maya",
		web(group, "GAP2", dana, false, t0+7200, text("Invoice attached")),
		web(group, "GAP1", dana, false, t0+3600, text("Thank u")),
	))
	b := <-got
	if b.last == nil || b.last.ID != "GAP1" {
		t.Fatalf("batch last = %+v", b.last)
	}
	if n := c.Len(); n != 4 {
		t.Errorf("entries = %d, want 4", n)
	}
}

func TestInvisibleMessagesAreSkipped(t *testing.T) {
	a, ar := newTestArchiver(t)
	a.handle(history(waHistorySync.HistorySync_ON_DEMAND, group, "Maya",
		web(group, "BODILESS", me, false, t0, nil),
		web(group, "M1", dana, false, t0+1, text("hello")),
	))
	a.handle(live(me, me, "P1", true, t0+2, &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type: waE2E.ProtocolMessage_APP_STATE_SYNC_KEY_SHARE.Enum(),
	}}))
	if got := chatFile(t, ar, group); got != "[2025-10-24, 04:39:34] Dana Putri: hello\r\n" {
		t.Errorf("got %q", got)
	}
	if _, ok := ar.Info(me); ok {
		t.Error("protocol traffic created a self chat folder")
	}
}

func TestRefreshNamesFixesNumberSenders(t *testing.T) {
	a, ar := newTestArchiver(t)
	c, _, _ := ar.EnsureChat(dana, "+6281111111111", "dm", "")
	c.Insert(archive.Row{ID: "M1", TS: t0, Sender: dana}, func(int) archive.Entry {
		return archive.Entry{Sender: "+6281111111111", Text: "U too"}
	})
	a.RefreshNames(context.Background(), true)
	if got := chatFile(t, ar, dana); got != "[2025-10-24, 04:39:33] Dana Putri: U too\r\n" {
		t.Errorf("got %q", got)
	}
	if c2, _ := ar.Chat(dana); filepath.Base(c2.Dir()) != "WhatsApp Chat - Dana Putri" {
		t.Errorf("folder %s", c2.Dir())
	}
}

func TestRefreshNamesFixesOwnName(t *testing.T) {
	a, ar := newTestArchiver(t)
	c, _, _ := ar.EnsureChat(dana, "Dana Putri", "dm", "")
	c.Insert(archive.Row{ID: "M1", TS: t0, FromMe: true, Sender: me}, func(int) archive.Entry {
		return archive.Entry{Sender: "You", Text: "Hello"}
	})
	a.RefreshNames(context.Background(), true)
	if got := chatFile(t, ar, dana); got != "[2025-10-24, 04:39:33] Alex: Hello\r\n" {
		t.Errorf("got %q", got)
	}
}

func TestLocationIsALineNotMedia(t *testing.T) {
	a, ar := newTestArchiver(t)
	loc := &waE2E.Message{LocationMessage: &waE2E.LocationMessage{DegreesLatitude: proto.Float64(48.85), DegreesLongitude: proto.Float64(2.35), Name: proto.String("Old Town")}}
	a.handle(live(dana, dana, "L1", false, t0, loc))
	if got := chatFile(t, ar, dana); got != "[2025-10-24, 04:39:33] Dana Putri: ‎Location: https://maps.google.com/?q=48.850000,2.350000\r\n" {
		t.Errorf("got %q", got)
	}
	if a.PendingMedia() != 0 {
		t.Errorf("location queued as media")
	}
}

func TestHistoryBatchWritesOnce(t *testing.T) {
	a, ar := newTestArchiver(t)
	var msgs []*waHistorySync.HistorySyncMsg
	for i := 50; i > 0; i-- { // newest first, as the phone sends it
		msgs = append(msgs, web(group, fmt.Sprintf("M%02d", i), dana, false, t0+int64(i), text(fmt.Sprint(i))))
	}
	a.handle(history(waHistorySync.HistorySync_FULL, group, "Maya", msgs...))
	got := chatFile(t, ar, group)
	if n := strings.Count(got, "\r\n"); n != 50 {
		t.Fatalf("entries = %d", n)
	}
	if !strings.HasPrefix(got, "[2025-10-24, 04:39:34] Dana Putri: 1\r\n") {
		t.Errorf("order: %q", got[:60])
	}
}

func TestStatusTracksConnectionAndLogout(t *testing.T) {
	a, ar := newTestArchiver(t)
	a.handle(&events.Connected{})
	if st, err := ReadStatus(ar.Root()); err != nil || !st.Connected {
		t.Fatalf("status %+v %v", st, err)
	}
	a.handle(&events.LoggedOut{})
	select {
	case <-a.LoggedOut():
	default:
		t.Fatal("LoggedOut not signalled")
	}
	if st, _ := ReadStatus(ar.Root()); !st.LoggedOut || st.Connected {
		t.Errorf("status %+v", st)
	}
}

func TestLiveMessageLandsWhileHistoryHoldsTheChat(t *testing.T) {
	a, ar := newTestArchiver(t)
	a.handle(live(group, dana, "M1", false, t0, text("first")))
	c, _ := ar.Chat(group)
	c.Hold() // a history chunk is being processed on another goroutine
	a.handle(live(group, dana, "M2", false, t0+5, text("live")))
	if got := chatFile(t, ar, group); !strings.Contains(got, "live") {
		t.Errorf("live message only in memory: %q", got)
	}
	c.Release()
}

func TestGroupChangeSeenLiveAndInHistoryIsWrittenOnce(t *testing.T) {
	a, ar := newTestArchiver(t)
	ownJID, _ := types.ParseJID(me)
	danaJID, _ := types.ParseJID(dana)
	a.handle(&events.GroupInfo{JID: mustJID(group), Sender: &danaJID, Timestamp: time.Unix(t0, 0), Join: []types.JID{ownJID}})
	stub := &waHistorySync.HistorySyncMsg{Message: &waWeb.WebMessageInfo{
		Key:                   &waCommon.MessageKey{RemoteJID: proto.String(group), ID: proto.String("S1"), Participant: proto.String(dana)},
		MessageTimestamp:      proto.Uint64(t0),
		MessageStubType:       waWeb.WebMessageInfo_GROUP_PARTICIPANT_ADD.Enum(),
		MessageStubParameters: []string{me},
	}}
	a.handle(history(waHistorySync.HistorySync_RECENT, group, "Maya", stub))
	if got := chatFile(t, ar, group); strings.Count(got, "You were added") != 1 {
		t.Errorf("got %q", got)
	}
}

func TestSecondBackfillOfSameChatIsRefused(t *testing.T) {
	a, _ := newTestArchiver(t)
	a.handle(live(group, dana, "M1", false, t0, text("hi")))
	a.backfill[group] = nil // one is running
	if _, err := a.Backfill(context.Background(), group, 50, 1, time.Second); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Errorf("err = %v", err)
	}
}

func TestChatNamedByNumberIsFlagged(t *testing.T) {
	a, ar := newTestArchiver(t)
	stranger := "6289999999999@s.whatsapp.net"
	a.handle(live(stranger, stranger, "M1", false, t0, text("hello")))
	info, _ := ar.Info(stranger)
	if !info.NeedsNames || info.Name != "+6289999999999" {
		t.Errorf("info %+v", info)
	}
	// A name pass with nothing new keeps the flag (the sender is still a number).
	a.RefreshNames(context.Background(), false)
	if info, _ := ar.Info(stranger); !info.NeedsNames {
		t.Error("flag cleared too early")
	}
}

func mustJID(s string) types.JID {
	j, err := types.ParseJID(s)
	if err != nil {
		panic(err)
	}
	return j
}

func TestFinishedDownloadLeavesQueueOnlyOnceWritten(t *testing.T) {
	a, ar := newTestArchiver(t)
	img := &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
		DirectPath: proto.String("/v/x"), MediaKey: make([]byte, 32), FileSHA256: make([]byte, 32), FileEncSHA256: make([]byte, 32),
	}}
	a.handle(live(group, dana, "M1", false, t0, img))
	c, _ := ar.Chat(group)
	var p *pendingMedia
	for _, it := range a.media.items {
		p = it
	}
	src := filepath.Join(t.TempDir(), "dl")
	os.WriteFile(src, []byte("jpg"), 0o600)
	if _, err := c.PlaceAttachment(p.ID, src, p.Name); err != nil {
		t.Fatal(err)
	}
	a.media.placed[p.Chat] = []string{p.key()}
	a.media.placedKeys[p.key()] = true
	if strings.Contains(chatFile(t, ar, group), "<attached:") {
		t.Fatal("written before settle; expected one write per batch")
	}
	a.media.settle()
	if !strings.Contains(chatFile(t, ar, group), "<attached: "+p.Name+">") || a.PendingMedia() != 0 {
		t.Errorf("after settle: pending=%d file=%q", a.PendingMedia(), chatFile(t, ar, group))
	}
}

func TestPlacedDownloadIsNotHandedOutAgain(t *testing.T) {
	a, _ := newTestArchiver(t)
	img := &waE2E.Message{ImageMessage: &waE2E.ImageMessage{DirectPath: proto.String("/v/x"), MediaKey: make([]byte, 32)}}
	a.handle(live(group, dana, "M1", false, t0, img))
	for k := range a.media.items {
		a.media.placedKeys[k] = true // downloaded; its chat not written yet
	}
	if got := a.media.take(time.Now()); len(got) != 0 {
		t.Errorf("handed out again: %v", got)
	}
}

func TestRedeliveredOmittedMediaIsQueuedAgain(t *testing.T) {
	a, _ := newTestArchiver(t)
	img := &waE2E.Message{ImageMessage: &waE2E.ImageMessage{DirectPath: proto.String("/v/x"), MediaKey: make([]byte, 32)}}
	a.handle(live(group, dana, "M1", false, t0, img))
	for k := range a.media.items { // crash before the queue was saved
		delete(a.media.items, k)
	}
	a.handle(live(group, dana, "M1", false, t0, img)) // WhatsApp delivers it again
	if a.PendingMedia() != 1 {
		t.Errorf("pending = %d, want 1", a.PendingMedia())
	}
}

func TestCorruptQueueFileDoesNotStopSync(t *testing.T) {
	a, ar := newTestArchiver(t)
	os.WriteFile(a.media.path, []byte("{torn"), 0o600)
	if _, err := New(ar, a.wa, t.Logf); err != nil {
		t.Fatalf("New: %v", err)
	}
	if m, _ := filepath.Glob(a.media.path + ".corrupt-*"); len(m) != 1 {
		t.Error("damaged queue not kept aside")
	}
}

func TestSentFileGoesStraightIntoTheFolder(t *testing.T) {
	a, ar := newTestArchiver(t)
	msg := &waE2E.Message{ImageMessage: &waE2E.ImageMessage{DirectPath: proto.String("/v/x"), MediaKey: make([]byte, 32)}}
	a.RecordSent(context.Background(), mustJID(dana), "S1", msg, []byte("jpeg bytes"))
	if a.PendingMedia() != 0 {
		t.Errorf("sent file queued for download")
	}
	got := chatFile(t, ar, dana)
	if !strings.Contains(got, "<attached: ") {
		t.Fatalf("entry %q", got)
	}
	c, _ := ar.Chat(dana)
	e, _ := c.Get("S1")
	if b, err := os.ReadFile(filepath.Join(c.Dir(), e.Attachment)); err != nil || string(b) != "jpeg bytes" {
		t.Errorf("file %q %v", b, err)
	}
}

func TestReservedDownloadIsNotHandedOut(t *testing.T) {
	a, _ := newTestArchiver(t)
	a.media.reserve(group, "S1") // a sent file is about to be placed
	img := &waE2E.Message{ImageMessage: &waE2E.ImageMessage{DirectPath: proto.String("/v/x"), MediaKey: make([]byte, 32)}}
	a.handle(live(group, me, "S1", true, t0, img))
	if got := a.media.take(time.Now()); len(got) != 0 {
		t.Errorf("worker would download our own upload: %v", got)
	}
}

func TestEventsDuringShutdownAreStillArchivedProperly(t *testing.T) {
	a, ar := newTestArchiver(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // SIGTERM arrived; the connection isn't closed yet
	a.ctx = ctx
	a.handle(live(dana, dana, "M1", false, t0, text("late")))
	if got := chatFile(t, ar, dana); got != "[2025-10-24, 04:39:33] Dana Putri: late\r\n" {
		t.Errorf("got %q", got)
	}
}

func TestRenameDuringHoldIsOnDiskBeforeFlagClears(t *testing.T) {
	a, ar := newTestArchiver(t)
	c, _, _ := ar.EnsureChat(dana, "Dana Putri", "dm", "")
	c.Insert(archive.Row{ID: "M1", TS: t0, Sender: dana}, func(int) archive.Entry {
		return archive.Entry{Sender: "+6281111111111", Text: "U too"}
	})
	a.needsNames(dana)
	c.Hold() // a history chunk in progress
	a.RefreshNames(context.Background(), false)
	ar2, err := archive.Open(archive.Options{Root: ar.Root(), Location: time.UTC}) // as after a crash
	if err != nil {
		t.Fatal(err)
	}
	info, _ := ar2.Info(dana)
	c2, _ := ar2.Chat(dana)
	data, _ := os.ReadFile(filepath.Join(c2.Dir(), archive.ChatFile))
	if !info.NeedsNames && strings.Contains(string(data), "+6281111111111") {
		t.Errorf("flag cleared but the file still has the number: %q", data)
	}
}

func TestUnrecoverableDisconnectsHalt(t *testing.T) {
	for _, tc := range []struct {
		evt   any
		retry bool
	}{
		{&events.StreamReplaced{}, false},
		{&events.ClientOutdated{}, false},
		{&events.TemporaryBan{Expire: time.Hour}, false},
		{&events.ConnectFailure{Reason: events.ConnectFailureReason(499)}, true},
	} {
		a, ar := newTestArchiver(t)
		a.handle(tc.evt)
		select {
		case <-a.Halted():
			if h := a.Halt(); h.Retry != tc.retry || h.Reason == "" {
				t.Errorf("%T: %+v", tc.evt, h)
			}
		default:
			t.Fatalf("%T didn't halt", tc.evt)
		}
		// The reason outlives the process until a connection succeeds.
		b, _ := New(ar, nil, nil)
		if b.status.s.Halted == "" {
			t.Errorf("%T: reason not carried over", tc.evt)
		}
		b.trackConnection(&events.Connected{})
		if st, _ := ReadStatus(ar.Root()); st.Halted != "" {
			t.Errorf("%T: reason kept after connecting: %q", tc.evt, st.Halted)
		}
	}
}

func TestMediaGivesUpAfterThreeTriesAndKeepsItsScheduleAcrossRestarts(t *testing.T) {
	a, ar := newTestArchiver(t)
	p := &pendingMedia{Chat: dana, ID: "M1", Name: "00000001-PHOTO-1970-01-01-00-00-00.jpg"}
	a.media.items[p.key()] = p
	a.media.failed(p, fmt.Errorf("no answer"), true)
	a.media.flush()
	b, _ := New(ar, nil, nil) // a restart
	q := b.media.items[p.key()]
	if q == nil || time.Until(q.NextTry) < 10*time.Minute {
		t.Fatalf("schedule lost on restart: %+v", q)
	}
	b.media.failed(q, fmt.Errorf("no answer"), true)
	b.media.failed(q, fmt.Errorf("no answer"), true)
	if b.media.len() != 0 {
		t.Errorf("still queued after three tries")
	}
}

func edit(id string, m *waE2E.Message) *waE2E.Message {
	return &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type:          waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(),
		Key:           &waCommon.MessageKey{RemoteJID: proto.String(group), ID: proto.String(id)},
		EditedMessage: m,
	}}
}

func TestCaptionEditOfAnOmittedDocumentKeepsItsLabel(t *testing.T) {
	a, ar := newTestArchiver(t)
	doc := &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{
		FileName: proto.String("Invoice.pdf"), PageCount: proto.Uint32(4), Caption: proto.String("first"),
		DirectPath: proto.String("/v/x"), MediaKey: make([]byte, 32), FileSHA256: make([]byte, 32), FileEncSHA256: make([]byte, 32),
	}}
	a.handle(live(group, dana, "D1", false, t0, doc))
	a.handle(live(group, dana, "E1", false, t0+5, edit("D1", text("second"))))
	want := "‎[2025-10-24, 04:39:33] Dana Putri: second Invoice.pdf • ‎4 pages ‎document omitted ‎<This message was edited>\r\n"
	if got := chatFile(t, ar, group); got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestUnreadableEditLeavesTheMessage(t *testing.T) {
	a, ar := newTestArchiver(t)
	a.handle(live(group, dana, "M1", false, t0, text("hello")))
	a.handle(live(group, dana, "E1", false, t0+5, edit("M1", &waE2E.Message{})))
	if got := chatFile(t, ar, group); got != "[2025-10-24, 04:39:33] Dana Putri: hello\r\n" {
		t.Errorf("got %q", got)
	}
}

func TestWrappedHistoryMessagesAndAlbumLikeText(t *testing.T) {
	a, ar := newTestArchiver(t)
	wrapped := &waE2E.Message{EphemeralMessage: &waE2E.FutureProofMessage{Message: text("disappearing")}}
	a.handle(history(waHistorySync.HistorySync_INITIAL_BOOTSTRAP, group, "Maya",
		web(group, "H1", dana, false, t0, wrapped)))
	a.handle(live(group, dana, "M2", false, t0+5, text("[Album of the year] is out")))
	got := chatFile(t, ar, group)
	if !strings.Contains(got, "Dana Putri: disappearing\r\n") || !strings.Contains(got, "[Album of the year] is out") {
		t.Errorf("got %q", got)
	}
}

func TestRedeliveryFillsAnEntryArchivedEmpty(t *testing.T) {
	a, ar := newTestArchiver(t)
	c, _, _ := ar.EnsureChat(group, "Maya", "group", "")
	c.Insert(archive.Row{ID: "H1", TS: t0, Sender: dana}, func(int) archive.Entry { return archive.Entry{Sender: "Dana Putri"} })
	wrapped := &waE2E.Message{EphemeralMessage: &waE2E.FutureProofMessage{Message: text("disappearing")}}
	a.handle(history(waHistorySync.HistorySync_INITIAL_BOOTSTRAP, group, "Maya", web(group, "H1", dana, false, t0, wrapped)))
	if got := chatFile(t, ar, group); got != "[2025-10-24, 04:39:33] Dana Putri: disappearing\r\n" {
		t.Errorf("got %q", got)
	}
}

func revoke(id, participant string) *waE2E.Message {
	key := &waCommon.MessageKey{RemoteJID: proto.String(group), ID: proto.String(id)}
	if participant == "" {
		key.FromMe = proto.Bool(true)
	} else {
		key.Participant = proto.String(participant)
	}
	return &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{Type: waE2E.ProtocolMessage_REVOKE.Enum(), Key: key}}
}

func TestAdminDeletesKeepTheSender(t *testing.T) {
	a, ar := newTestArchiver(t)
	a.handle(live(group, dana, "G1", false, t0, text("spam")))
	a.handle(live(group, me, "R1", true, t0+10, revoke("G1", dana))) // you, as admin
	// Dana deletes yours, never seen: kept out, as her being an admin
	// can't be checked offline.
	a.handle(live(group, dana, "R2", false, t0+20, revoke("G2", me)))
	if got := chatFile(t, ar, group); got != "[2025-10-24, 04:39:33] Dana Putri: ‎This message was deleted.\r\n" {
		t.Errorf("got %q", got)
	}
}

func TestDeletedMessagesFromHistory(t *testing.T) {
	a, ar := newTestArchiver(t)
	stub := web(group, "H1", dana, false, t0, nil)
	stub.Message.MessageStubType = waWeb.WebMessageInfo_REVOKE.Enum()
	a.handle(history(waHistorySync.HistorySync_INITIAL_BOOTSTRAP, group, "Maya", stub))
	if got := chatFile(t, ar, group); got != "[2025-10-24, 04:39:33] Dana Putri: ‎This message was deleted.\r\n" {
		t.Errorf("got %q", got)
	}
}

func TestAdminDeletedMessageFromHistory(t *testing.T) {
	a, ar := newTestArchiver(t)
	stub := web(group, "H1", dana, false, t0, nil)
	stub.Message.MessageStubType = waWeb.WebMessageInfo_ADMIN_REVOKE.Enum()
	mine := web(group, "H2", "", true, t0+10, nil)
	mine.Message.MessageStubType = waWeb.WebMessageInfo_ADMIN_REVOKE.Enum()
	a.handle(history(waHistorySync.HistorySync_INITIAL_BOOTSTRAP, group, "Maya", stub, mine))
	want := "[2025-10-24, 04:39:33] Dana Putri: ‎This message was deleted.\r\n" +
		"[2025-10-24, 04:39:43] Alex: ‎This message was deleted.\r\n"
	if got := chatFile(t, ar, group); got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestGroupRemovalsAndInviteJoins(t *testing.T) {
	a, ar := newTestArchiver(t)
	a.handle(live(group, dana, "M1", false, t0, text("hi")))
	g, _ := types.ParseJID(group)
	d, _ := types.ParseJID(dana)
	sam, _ := types.ParseJID("6282222222222@s.whatsapp.net")
	at := time.Unix(t0, 0)
	a.handle(&events.GroupInfo{JID: g, Sender: &d, Timestamp: at.Add(time.Second), Leave: []types.JID{sam}})
	a.handle(&events.GroupInfo{JID: g, Sender: &sam, Timestamp: at.Add(2 * time.Second), Leave: []types.JID{sam}})
	a.handle(&events.GroupInfo{JID: g, Sender: &sam, Timestamp: at.Add(3 * time.Second), Join: []types.JID{sam}, JoinReason: "invite"})
	got := chatFile(t, ar, group)
	for _, want := range []string{"Dana Putri removed +6282222222222\r\n", "+6282222222222 left\r\n", "+6282222222222 joined using this group's invite link\r\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func TestYouLeftAndLiveIconChange(t *testing.T) {
	a, ar := newTestArchiver(t)
	leave := web(group, "S1", "", true, t0, nil)
	leave.Message.MessageStubType = waWeb.WebMessageInfo_GROUP_PARTICIPANT_LEAVE.Enum()
	leave.Message.MessageStubParameters = []string{me}
	a.handle(history(waHistorySync.HistorySync_INITIAL_BOOTSTRAP, group, "Maya", leave))
	g, _ := types.ParseJID(group)
	m, _ := types.ParseJID(me)
	d, _ := types.ParseJID(dana)
	a.handle(&events.GroupInfo{JID: g, Sender: &m, Timestamp: time.Unix(t0, 0), Leave: []types.JID{m}})
	a.handle(&events.Picture{JID: g, Author: d, Timestamp: time.Unix(t0+60, 0), PictureID: "1"})
	want := "[2025-10-24, 04:39:33] Maya: ‎You left\r\n[2025-10-24, 04:40:33] Maya: ‎Dana Putri changed this group's icon\r\n"
	if got := chatFile(t, ar, group); got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestStoredProfileNameAfterRestart(t *testing.T) {
	a, ar := newTestArchiver(t)
	a.handle(live(group, "6283333333333@s.whatsapp.net", "G1", false, t0, text("hello")))
	if got := chatFile(t, ar, group); got != "[2025-10-24, 04:39:33] ~\u202fKim: hello\r\n" {
		t.Errorf("got %q", got)
	}
}

func TestDiskFullPausesWithoutUsingTries(t *testing.T) {
	a, _ := newTestArchiver(t)
	p := &pendingMedia{Chat: dana, ID: "M1", Name: "00000001-PHOTO-1970-01-01-00-00-00.jpg"}
	a.media.items[p.key()] = p
	a.media.failed(p, fmt.Errorf("write: %w", syscall.ENOSPC), false)
	if q := a.media.items[p.key()]; q == nil || q.Attempts != 0 || time.Until(q.NextTry) < 10*time.Minute {
		t.Errorf("got %+v", q)
	}
}

func TestLastArchivedIsUnsetUntilSomethingIsArchived(t *testing.T) {
	a, ar := newTestArchiver(t)
	a.writeStatus()
	if st, _ := ReadStatus(ar.Root()); !st.LastArchived.IsZero() {
		t.Errorf("last archived %v before anything was", st.LastArchived)
	}
}

func TestLastArchivedSurvivesARestart(t *testing.T) {
	a, ar := newTestArchiver(t)
	a.handle(live(group, dana, "M1", false, t0, text("hi")))
	a.writeStatus()
	b, _ := New(ar, nil, nil)
	b.writeStatus()
	if st, _ := ReadStatus(ar.Root()); st.LastArchived.IsZero() {
		t.Error("last archived lost on restart")
	}
}

func TestOthersCantRewriteMessages(t *testing.T) {
	a, ar := newTestArchiver(t)
	mallory := "6284444444444@s.whatsapp.net"
	a.handle(live(group, dana, "D1", false, t0, text("hello")))
	a.handle(live(group, me, "A1", true, t0+1, text("ok")))
	a.handle(live(group, dana, "D2", false, t0+2, text("bye")))
	a.handle(live(group, mallory, "X1", false, t0+10, edit("D1", text("I owe Mallory $5000"))))
	a.handle(live(group, mallory, "X2", false, t0+11, edit("A1", text("I agree"))))
	a.handle(live(group, mallory, "X3", false, t0+12, revoke("D2", "")))   // claims to be the sender's own delete
	a.handle(live(group, mallory, "X4", false, t0+13, revoke("D2", dana))) // not an admin
	got := chatFile(t, ar, group)
	if strings.Contains(got, "Mallory") || strings.Contains(got, "agree") || strings.Contains(got, "deleted") {
		t.Errorf("forged change archived: %q", got)
	}
	a.handle(live(group, dana, "E1", false, t0+20, edit("D1", text("hello there"))))
	if got := chatFile(t, ar, group); !strings.Contains(got, "hello there") {
		t.Errorf("the sender's own edit was refused: %q", got)
	}
}

func TestLiveForgeriesOfUnarchivedOrWacliMessages(t *testing.T) {
	a, ar := newTestArchiver(t)
	mallory := "6284444444444@s.whatsapp.net"
	g, _ := types.ParseJID(group)
	d, _ := types.ParseJID(dana)
	a.handle(&events.GroupInfo{JID: g, Sender: &d, Timestamp: time.Unix(t0, 0), Name: &types.GroupName{Name: "Maya"}})
	before := chatFile(t, ar, group)
	a.handle(live(group, mallory, "X1", false, t0+5, edit(fmt.Sprintf("wacli:subject:%d", t0), text("forged"))))
	a.handle(live(group, mallory, "X2", false, t0+6, edit("OLD1", text("Dana never said this"))))
	a.handle(live(group, mallory, "X3", false, t0+7, revoke("OLD2", dana)))
	a.handle(live(group, mallory, "X4", false, t0+8, &waE2E.Message{CallLogMesssage: &waE2E.CallLogMessage{IsVideo: proto.Bool(true), DurationSecs: proto.Int64(7300)}}))
	got := chatFile(t, ar, group)
	if got != before {
		t.Errorf("forgeries archived: %q", got)
	}
	if _, ok := ar.Info("6284444444444@s.whatsapp.net"); ok || len(ar.Chats()) != 1 {
		t.Errorf("a rejected message made a chat: %+v", ar.Chats())
	}
	for _, forged := range []string{"forged", "never said", "deleted", "call"} {
		if strings.Contains(got, forged) {
			t.Errorf("%q archived: %q", forged, got)
		}
	}
}

func TestMappingLearnedLaterKeepsOneFolder(t *testing.T) {
	a, ar := newTestArchiver(t)
	lid := "99887766554433@lid"
	pn := "6285555555555@s.whatsapp.net"
	a.handle(live(lid, lid, "L1", false, t0, text("first")))
	a.wa.StoreLIDMapping(context.Background(), lid, pn)
	a.handle(live(pn, pn, "L2", false, t0+5, text("second")))
	if n := len(ar.Chats()); n != 1 {
		t.Errorf("%d chats: %+v", n, ar.Chats())
	}
}

func TestHiddenIDChatGetsItsNumberOnceKnown(t *testing.T) {
	a, ar := newTestArchiver(t)
	lid := "99887766554433@lid"
	a.handle(live(lid, lid, "L1", false, t0, text("first")))
	a.wa.StoreLIDMapping(context.Background(), lid, "6285555555555@s.whatsapp.net")
	a.RefreshNames(context.Background(), true)
	info, _ := ar.Info(lid)
	if info.Name != "+6285555555555" || !strings.Contains(chatFile(t, ar, lid), "+6285555555555: first") {
		t.Errorf("name %q, file %q", info.Name, chatFile(t, ar, lid))
	}
}

func TestHiddenIDSenderWithOnlyANumberStaysFlagged(t *testing.T) {
	a, ar := newTestArchiver(t)
	lid := "99887766554433@lid"
	a.handle(live(group, lid, "G1", false, t0, text("hi")))
	a.wa.StoreLIDMapping(context.Background(), lid, "6285555555555@s.whatsapp.net")
	a.RefreshNames(context.Background(), false)
	a.names.notePush(lid, "Rina")
	a.RefreshNames(context.Background(), false)
	if got := chatFile(t, ar, group); !strings.Contains(got, "Rina: hi") {
		t.Errorf("got %q", got)
	}
}

func TestGivenUpMediaIsNotRequeuedByARedelivery(t *testing.T) {
	a, _ := newTestArchiver(t)
	img := &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
		DirectPath: proto.String("/v/x"), MediaKey: make([]byte, 32),
		FileSHA256: make([]byte, 32), FileEncSHA256: make([]byte, 32), FileLength: proto.Uint64(10),
	}}
	a.handle(history(waHistorySync.HistorySync_INITIAL_BOOTSTRAP, group, "Maya", web(group, "M1", dana, false, t0, img)))
	for _, p := range a.media.items {
		p.Gone = true
		a.media.failed(p, fmt.Errorf("gone"), true)
	}
	a.handle(history(waHistorySync.HistorySync_ON_DEMAND, group, "Maya", web(group, "M1", dana, false, t0, img)))
	if n := a.media.len(); n != 0 {
		t.Errorf("given-up media queued again: %d", n)
	}
}

func TestVideoNotesAreArchivedAndBlankOnesRepaired(t *testing.T) {
	a, ar := newTestArchiver(t)
	ptv := &waE2E.Message{PtvMessage: &waE2E.VideoMessage{
		DirectPath: proto.String("/v/x"), MediaKey: make([]byte, 32),
		FileSHA256: make([]byte, 32), FileEncSHA256: make([]byte, 32), FileLength: proto.Uint64(10),
	}}
	a.handle(live(group, dana, "V1", false, t0, ptv))
	if got := chatFile(t, ar, group); !strings.Contains(got, "video omitted") || a.PendingMedia() != 1 {
		t.Errorf("video note: %q, pending %d", got, a.PendingMedia())
	}
	// One archived empty by an older version is filled in on redelivery.
	c, _ := ar.Chat(group)
	c.Insert(archive.Row{ID: "V2", TS: t0 + 60, Sender: dana}, func(int) archive.Entry { return archive.Entry{Sender: "Dana Putri"} })
	a.handle(history(waHistorySync.HistorySync_ON_DEMAND, group, "Maya", web(group, "V2", dana, false, t0+60, ptv)))
	if got := chatFile(t, ar, group); strings.Count(got, "video omitted") != 2 || a.PendingMedia() != 2 {
		t.Errorf("blank video note not repaired: %q, pending %d", got, a.PendingMedia())
	}
}
