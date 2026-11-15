package archiver

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"google.golang.org/protobuf/proto"
)

// next returns the next published message, failing if none comes.
func next(t *testing.T, ch <-chan Message) Message {
	t.Helper()
	select {
	case m := <-ch:
		return m
	case <-time.After(5 * time.Second):
		t.Fatal("nothing published")
		return Message{}
	}
}

func none(t *testing.T, ch <-chan Message) {
	t.Helper()
	select {
	case m := <-ch:
		t.Fatalf("unexpected %+v", m)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestWatchPublishesLiveMessagesEditsAndDeletesOnceOnDisk(t *testing.T) {
	a, ar := newTestArchiver(t)
	ch, cancel := a.Subscribe()
	defer cancel()

	// History (here an initial sync) is not news.
	a.handle(history(waHistorySync.HistorySync_INITIAL_BOOTSTRAP, group, "Maya", web(group, "H1", dana, false, t0-60, text("old"))))
	none(t, ch)

	a.handle(live(dana, dana, "M1", false, t0, text("Thanks")))
	m := next(t, ch)
	c, _ := ar.Chat(dana)
	want := Message{ID: "M1", Thread: dana, Chat: c.Name(), At: t0 * 1000, From: "Dana Putri", Kind: "text", Text: "Thanks", Ref: filepath.Join(c.Dir(), "_chat.txt")}
	if m != want {
		t.Fatalf("got  %+v\nwant %+v", m, want)
	}
	if data, err := os.ReadFile(m.Ref); err != nil || len(data) == 0 {
		t.Errorf("ref not on disk when published: %v", err)
	}

	edit := &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type:          waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(),
		Key:           &waCommon.MessageKey{RemoteJID: proto.String(me), FromMe: proto.Bool(true), ID: proto.String("M1")},
		EditedMessage: text("Thank u Alex"),
	}}
	a.handle(live(dana, dana, "E1", false, t0+20, edit))
	if m := next(t, ch); m.ID != "M1:edit-1761280793000" || m.Kind != "edit" || m.Text != "Thank u Alex" || m.At != (t0+20)*1000 {
		t.Errorf("edit: %+v", m)
	}
	a.handle(live(dana, dana, "E1", false, t0+20, edit)) // redelivered: nothing changed
	none(t, ch)

	revoke := &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type: waE2E.ProtocolMessage_REVOKE.Enum(),
		Key:  &waCommon.MessageKey{RemoteJID: proto.String(me), FromMe: proto.Bool(true), ID: proto.String("M1")},
	}}
	a.handle(live(dana, dana, "R1", false, t0+30, revoke))
	if m := next(t, ch); m.ID != "M1:delete-1761280803000" || m.Kind != "delete" || m.Text != "" {
		t.Errorf("delete: %+v", m)
	}
	a.handle(live(dana, dana, "R1", false, t0+30, revoke))
	none(t, ch)

	a.handle(live(group, me, "S1", true, t0+40, text("sent from the phone")))
	if m := next(t, ch); !m.FromMe || m.From != "Alex" || m.Thread != group {
		t.Errorf("own: %+v", m)
	}
}

func TestWatchMediaWaitsForItsFile(t *testing.T) {
	a, ar := newTestArchiver(t)
	ch, cancel := a.Subscribe()
	defer cancel()
	img := &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
		Caption: proto.String("receipt"), DirectPath: proto.String("/v/x"), MediaKey: make([]byte, 32),
		FileSHA256: make([]byte, 32), FileEncSHA256: make([]byte, 32),
	}}
	a.handle(live(group, dana, "M1", false, t0, img))
	none(t, ch) // still downloading

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
	a.media.settle()
	m := next(t, ch)
	if m.Kind != "media" || m.Text != "receipt" || m.Media != filepath.Join(c.Dir(), p.Name) {
		t.Fatalf("media: %+v", m)
	}
	if _, err := os.Stat(m.Media); err != nil {
		t.Errorf("file not on disk when published: %v", err)
	}
}

func TestWatchMediaIsPublishedWithoutTheFileAfterAWhile(t *testing.T) {
	defer func(d time.Duration) { mediaWait = d }(mediaWait)
	mediaWait = 10 * time.Millisecond
	a, _ := newTestArchiver(t)
	ch, cancel := a.Subscribe()
	defer cancel()
	img := &waE2E.Message{ImageMessage: &waE2E.ImageMessage{DirectPath: proto.String("/v/x"), MediaKey: make([]byte, 32)}}
	a.handle(live(group, dana, "M1", false, t0, img))
	if m := next(t, ch); m.ID != "M1" || m.Kind != "media" || m.Media != "" {
		t.Errorf("got %+v", m)
	}
}

func TestWatchSentFileComesWithItsPath(t *testing.T) {
	a, _ := newTestArchiver(t)
	ch, cancel := a.Subscribe()
	defer cancel()
	msg := &waE2E.Message{ImageMessage: &waE2E.ImageMessage{DirectPath: proto.String("/v/x"), MediaKey: make([]byte, 32)}}
	a.RecordSent(context.Background(), mustJID(dana), "S1", msg, []byte("jpeg bytes"))
	if m := next(t, ch); m.ID != "S1" || !m.FromMe || m.Media == "" {
		t.Errorf("got %+v", m)
	}
}

func TestWatchSlowSubscriberIsDroppedNotWaitedFor(t *testing.T) {
	a, _ := newTestArchiver(t)
	ch, cancel := a.Subscribe()
	defer cancel()
	for i := range watchBuffer + 1 {
		a.watch.send(Message{ID: string(rune('a' + i%26))})
	}
	n := 0
	for range ch {
		n++
	}
	if n != watchBuffer {
		t.Errorf("read %d before the close, want %d", n, watchBuffer)
	}
}

func TestMessagesSinceCatchesUpOldestFirst(t *testing.T) {
	a, _ := newTestArchiver(t)
	a.handle(live(group, dana, "G1", false, t0, text("first")))
	a.handle(live(dana, dana, "D1", false, t0+10, text("second")))
	a.handle(live(dana, dana, "D2", false, t0+20, text("gone")))
	a.handle(live(dana, dana, "R1", false, t0+30, &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type: waE2E.ProtocolMessage_REVOKE.Enum(),
		Key:  &waCommon.MessageKey{RemoteJID: proto.String(me), FromMe: proto.Bool(true), ID: proto.String("D2")},
	}}))
	a.handle(history(waHistorySync.HistorySync_RECENT, group, "Maya", web(group, "H1", dana, false, t0-3600, text("older"))))

	got := a.MessagesSince((t0 - 1) * 1000)
	var ids []string
	for _, m := range got {
		ids = append(ids, m.ID)
	}
	if len(ids) != 2 || ids[0] != "G1" || ids[1] != "D1" {
		t.Errorf("ids %v", ids)
	}
	if got := a.MessagesSince((t0+10)*1000 + 500); len(got) != 1 || got[0].ID != "D1" {
		t.Errorf("same second as since: %+v", got) // at is whole seconds
	}
}
