// Package archiver turns WhatsApp events into export-shaped chat folders.
package archiver

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ferdousbhai/wacli/internal/archive"
	"github.com/ferdousbhai/wacli/internal/wa"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/encoding/protojson"
)

var debug = os.Getenv("WACLI_DEBUG") != ""

type Archiver struct {
	ar     *archive.Archive
	wa     *wa.Client
	names  *names
	media  *mediaQueue
	status *statusTracker
	logf   func(format string, args ...any)

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup // background goroutines
	// handling is read-locked by every event handler; Close takes it to wait
	// for handlers in flight before saving.
	handling sync.RWMutex
	closed   bool

	added        atomic.Int64
	lastActivity atomic.Int64 // when something was last archived; 0: not yet
	started      time.Time
	namesChanged chan struct{}

	mu          sync.Mutex
	backfill    map[string]chan historyBatch // chat JID → backfill waiting for a page
	seenPayload map[string]bool
	namesWanted map[string]bool // chats flagged NeedsNames since a refresh began
}

type historyBatch struct {
	last *archive.Row // last message of the page, in the phone's order
}

func New(ar *archive.Archive, client *wa.Client, logf func(string, ...any)) (*Archiver, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	a := &Archiver{
		ar:           ar,
		wa:           client,
		names:        newNames(client),
		status:       newStatusTracker(ar.Root()),
		started:      time.Now(),
		logf:         logf,
		ctx:          context.Background(),
		cancel:       func() {},
		namesChanged: make(chan struct{}, 1),
		backfill:     map[string]chan historyBatch{},
		seenPayload:  map[string]bool{},
		namesWanted:  map[string]bool{},
	}
	q, err := newMediaQueue(a)
	if err != nil {
		return nil, err
	}
	a.media = q
	return a, nil
}

// Start begins handling events, downloading media, refreshing names and
// reporting status, until Close.
func (a *Archiver) Start(ctx context.Context) {
	a.ctx, a.cancel = context.WithCancel(ctx)
	a.wa.AddEventHandler(a.handle)
	a.background(a.media.run)
	a.background(a.reportStatus)
	a.background(a.refreshNamesLoop)
	a.background(a.trimLoop)
}

// trimLoop unloads chats nobody touched for a while, so the sync process
// doesn't end up holding the whole archive in memory.
func (a *Archiver) trimLoop(ctx context.Context) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.ar.Trim(10 * time.Minute)
		}
	}
}

func (a *Archiver) background(fn func(context.Context)) {
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		fn(a.ctx)
	}()
}

// Close waits for event handlers in flight, stops background work and saves
// what's pending. Disconnect the client first so no new events arrive.
func (a *Archiver) Close() {
	a.handling.Lock()
	a.closed = true
	a.handling.Unlock()
	a.cancel()
	a.wg.Wait()
	if err := a.ar.FlushAll(); err != nil {
		a.logf("warning: %v", err)
	}
	a.media.flush()
}

func (a *Archiver) Added() int64 { return a.added.Load() }

// Idle reports how long it has been since the last archived change, or
// since the archiver started.
func (a *Archiver) Idle() time.Duration {
	t := a.lastActivity.Load()
	if t == 0 {
		return time.Since(a.started)
	}
	return time.Since(time.Unix(0, t))
}

func (a *Archiver) touch() { a.lastActivity.Store(time.Now().UnixNano()) }

// PendingMedia is the number of attachments still to download.
func (a *Archiver) PendingMedia() int { return a.media.len() }

// WaitMedia blocks until everything downloadable now is downloaded, or ctx
// ends (auth and sync wait for the whole queue).
func (a *Archiver) WaitMedia(ctx context.Context) { a.media.wait(ctx, a.media.keys()) }

func (a *Archiver) handle(evt any) {
	a.handling.RLock()
	defer a.handling.RUnlock()
	if a.closed {
		return
	}
	// Shutdown cancels a.ctx before the connection is closed; events still
	// arriving (or the rest of a history chunk) must be archived properly,
	// not with lookups failing on a cancelled context.
	ctx := context.WithoutCancel(a.ctx)
	switch v := evt.(type) {
	case *events.Message:
		a.names.notePush(v.Info.Sender.String(), v.Info.PushName)
		a.archiveMessage(ctx, nil, wa.ParseLiveMessage(v), "")
	case *events.HistorySync:
		a.archiveHistory(ctx, v.Data)
	case *events.PushName:
		a.names.notePush(v.JID.String(), v.NewPushName)
		a.nameChanged()
	case *events.Contact:
		a.nameChanged()
	case *events.GroupInfo:
		a.archiveGroupInfo(ctx, v)
	case *events.Picture:
		a.archiveGroupIcon(ctx, v)
	case *events.MediaRetry:
		a.media.onRetry(v)
	case *events.Connected:
		a.trackConnection(v)
		a.nameChanged()
		a.media.poke() // downloads wait for the login
	case *events.Disconnected, *events.LoggedOut:
		a.trackConnection(v)
	// whatsmeow doesn't reconnect after these.
	case *events.StreamReplaced:
		a.halt("another client connected with this session", false)
	case *events.ClientOutdated:
		a.halt("WhatsApp rejected this wacli version as outdated; update wacli", false)
	case *events.TemporaryBan:
		a.halt("WhatsApp temporarily banned this account: "+v.String(), false)
	case *events.ConnectFailure:
		a.halt(fmt.Sprintf("WhatsApp refused the connection (%d %s)", int(v.Reason), v.Message), true)
	}
}

// batch collects the writes of one history chunk: every chat it touches is
// held and written once at the end. A nil batch writes through.
type batch struct {
	held map[string]*archive.Chat
}

func (b *batch) use(c *archive.Chat) {
	if b == nil {
		return
	}
	if _, ok := b.held[c.JID()]; !ok {
		c.Hold()
		b.held[c.JID()] = c
	}
}

func (a *Archiver) archiveHistory(ctx context.Context, data *waHistorySync.HistorySync) {
	if data == nil {
		return
	}
	for _, p := range data.GetPushnames() {
		a.names.notePush(p.GetID(), p.GetPushname())
	}
	if len(data.GetPushnames()) > 0 {
		a.nameChanged()
	}
	before := a.added.Load()
	b := &batch{held: map[string]*archive.Chat{}}
	pages := map[string]historyBatch{}
	for _, conv := range data.GetConversations() {
		chatID := strings.TrimSpace(conv.GetID())
		if chatID == "" {
			continue
		}
		hint := conv.GetName()
		if hint == "" {
			hint = conv.GetDisplayName()
		}
		var page historyBatch
		for _, m := range conv.GetMessages() {
			web := m.GetMessage()
			if web == nil {
				continue
			}
			a.archiveWeb(ctx, b, chatID, hint, web)
			// Pages come in the phone's storage order, which is not always
			// time order; the next page starts after the last message here.
			key := web.GetKey()
			sender := web.GetParticipant()
			if sender == "" {
				sender = key.GetParticipant()
			}
			page.last = &archive.Row{ID: key.GetID(), TS: int64(web.GetMessageTimestamp()), FromMe: key.GetFromMe(), Sender: sender}
		}
		pages[chatID] = page
	}
	for _, c := range b.held {
		if err := c.Release(); err != nil {
			a.logf("warning: writing %s: %v", c.Name(), err)
		}
	}
	a.media.flush() // downloads queued by this chunk, before it is acknowledged
	if data.GetSyncType() == waHistorySync.HistorySync_ON_DEMAND {
		// A waiting backfill reads the chat, so tell it only once written.
		for chatID, page := range pages {
			a.deliverBackfill(ctx, chatID, page)
		}
	}
	if n := a.added.Load() - before; n > 0 {
		a.logf("history sync (%s): %d new messages", strings.ToLower(data.GetSyncType().String()), n)
	}
}

func (a *Archiver) archiveWeb(ctx context.Context, b *batch, chatID, hint string, web *waWeb.WebMessageInfo) {
	a.names.notePush(web.GetParticipant(), web.GetPushName())
	if t := web.GetMessageStubType(); t == waWeb.WebMessageInfo_REVOKE || t == waWeb.WebMessageInfo_ADMIN_REVOKE {
		// A message deleted before the history sync: only its key is left.
		pm := wa.ParseHistoryMessage(chatID, web)
		pm.Revoked, pm.AdminRevoke = true, t == waWeb.WebMessageInfo_ADMIN_REVOKE
		a.archiveMessage(ctx, b, pm, hint)
		return
	}
	if web.GetMessageStubType() != 0 {
		a.archiveStub(ctx, b, chatID, hint, web)
		return
	}
	if debug && web.GetMessage() == nil {
		a.logf("debug: bodiless history record: %s", protojson.Format(web))
	}
	a.archiveMessage(ctx, b, wa.ParseHistoryMessage(chatID, web), hint)
}

func (a *Archiver) archiveMessage(ctx context.Context, b *batch, pm wa.ParsedMessage, hint string) {
	if pm.ID == "" || pm.Chat.IsEmpty() || skipChat(pm.Chat) {
		return
	}
	if pm.ReactionToID != "" || pm.ReactionEmoji != "" || pm.IsPollVote || pm.IsPollAdd {
		return
	}
	if !pm.HasContent() && !pm.Edited && invisiblePayload(pm.UnhandledPayload) {
		return
	}
	c, err := a.chat(ctx, pm.Chat, hint)
	if err != nil {
		a.logf("warning: %v", err)
		return
	}
	b.use(c)
	row := archive.Row{ID: pm.ID, TS: pm.Timestamp.Unix(), FromMe: pm.FromMe, Sender: pm.SenderJID}

	switch {
	case pm.Revoked:
		a.applyDelete(ctx, b, c, pm, row)
		return
	case pm.Edited && c.Has(pm.ID):
		if !pm.HasContent() {
			if pm.UnhandledPayload != "" {
				a.unrendered(pm.UnhandledPayload)
			}
			return // an edit wacli can't read mustn't blank the message
		}
		edit := a.entryFor(ctx, pm, "") // a document's caption and label, as when sent
		queued := a.media.name(c.JID(), pm.ID)
		_, err := c.Update(pm.ID, func(e *archive.Entry) bool {
			if e.DocLabel == "" && e.Omitted == "document omitted" && queued != "" {
				e.Text, e.DocLabel = archive.SplitDocLabel(e.Text, queued) // read back, the label is still in the text
			}
			if e.DocLabel != "" {
				edit.DocLabel = e.DocLabel // the file's label doesn't change
			}
			if e.Deleted != "" || (e.Text == edit.Text && e.DocLabel == edit.DocLabel && e.Edited) {
				return false
			}
			e.Text, e.DocLabel, e.System, e.Edited = edit.Text, edit.DocLabel, edit.System, true
			return true
		})
		a.wrote(b, c, err)
		return
	case c.Has(pm.ID):
		a.requeueMedia(c, pm)
		a.fillBlank(ctx, b, c, pm)
		return
	}

	if !pm.HasContent() && pm.UnhandledPayload != "" {
		a.unrendered(pm.UnhandledPayload)
	}
	sender := a.senderName(ctx, pm)
	e := a.entryFor(ctx, pm, sender)
	fallback := a.isFallbackSender(pm, sender)
	if fallback {
		a.needsNames(c.JID()) // before the entry is on disk, so a crash can't lose the flag
	}
	_, inserted, err := c.Insert(row, func(pos int) archive.Entry {
		if pm.Media != nil && pm.Location == nil && archive.Downloadable(pm.Media.Type) {
			e.Omitted = archive.OmittedText(pm.Media.Type)
			name := archive.AttachmentName(pos, pm.Media.Type, pm.Timestamp.In(a.ar.Location()), pm.Media.Filename)
			a.media.add(c, pm, name)
		}
		return e
	})
	if inserted {
		a.added.Add(1)
		// Again after the insert: a refresh pass that read the chat before
		// the entry existed may have cleared the flag.
		if fallback {
			a.needsNames(c.JID())
		}
	}
	a.wrote(b, c, err)
}

// requeueMedia queues a redelivered media message whose entry is still
// "omitted" but whose download isn't queued: a crash between writing the
// entry and saving the queue would otherwise leave it omitted forever.
func (a *Archiver) requeueMedia(c *archive.Chat, pm wa.ParsedMessage) {
	if pm.Media == nil || pm.Location != nil || !archive.Downloadable(pm.Media.Type) || a.media.queued(c.JID(), pm.ID) {
		return
	}
	if e, ok := c.Get(pm.ID); !ok || e.Omitted == "" || e.Deleted != "" {
		return
	}
	a.media.add(c, pm, archive.AttachmentName(c.Position(pm.ID), pm.Media.Type, pm.Timestamp.In(a.ar.Location()), pm.Media.Filename))
	a.media.flush()
}

// wrote finishes a write: outside a history batch it makes sure the change
// is on disk (the chat may be held by a batch on another goroutine) before
// WhatsApp gets its acknowledgement.
func (a *Archiver) wrote(b *batch, c *archive.Chat, err error) {
	if err == nil && b == nil {
		err = c.Flush()
		a.media.flush() // its download, if any, is queued on disk too
	}
	if err != nil {
		a.logf("warning: archive write failed: %v", err)
		return
	}
	a.touch()
}

// unrendered notes, once per type, a message kind written as an empty entry
// because wacli can't render it yet.
func (a *Archiver) unrendered(payload string) {
	a.mu.Lock()
	seen := a.seenPayload[payload]
	a.seenPayload[payload] = true
	a.mu.Unlock()
	if !seen {
		a.logf("note: %s messages are archived as empty entries (not rendered yet)", payload)
	}
}

func (a *Archiver) senderName(ctx context.Context, pm wa.ParsedMessage) string {
	if pm.FromMe {
		return a.names.own()
	}
	if pm.PushName != "" {
		a.names.notePush(pm.SenderJID, pm.PushName)
	}
	sender, err := types.ParseJID(pm.SenderJID)
	if err != nil || sender.IsEmpty() {
		sender = pm.Chat
	}
	return a.names.contact(ctx, sender)
}

func (a *Archiver) isFallbackSender(pm wa.ParsedMessage, sender string) bool {
	if pm.FromMe {
		return sender == ownFallback
	}
	return IsFallbackName(sender, pm.SenderJID)
}

func (a *Archiver) needsNames(jid string) {
	a.mu.Lock()
	a.namesWanted[jid] = true // tells a refresh pass in flight not to clear it
	a.mu.Unlock()
	if err := a.ar.UpdateInfo(jid, func(ci *archive.ChatInfo) { ci.NeedsNames = true }); err != nil {
		a.logf("warning: %v", err)
	}
}

func (a *Archiver) entryFor(ctx context.Context, pm wa.ParsedMessage, sender string) archive.Entry {
	e := archive.Entry{Sender: sender, Text: a.names.mentions(ctx, pm.Text, pm.MentionedJIDs), Edited: pm.Edited}
	switch {
	case pm.Location != nil:
		e.System = true
		e.Text = fmt.Sprintf("Location: https://maps.google.com/?q=%.6f,%.6f", pm.Location.Latitude, pm.Location.Longitude)
	case pm.Media != nil && pm.Media.Type == "document":
		e.Text = strings.TrimSpace(pm.Media.Caption)
		if e.Text == pm.Media.Filename {
			e.Text = ""
		}
		e.DocLabel = archive.DocLabel(pm.Media.Filename, pm.Media.PageCount)
	case pm.Media != nil:
		e.Text = a.names.mentions(ctx, pm.Media.Caption, pm.MentionedJIDs)
	case pm.Call != nil:
		e.System, e.Text = true, callText(pm.Call)
	case pm.Poll != nil:
		e.System = true
		lines := []string{"POLL:", pm.Poll.Question}
		for _, opt := range pm.Poll.Options {
			lines = append(lines, "OPTION: "+opt)
		}
		e.Text = strings.Join(lines, "\n‎")
	case pm.IsAlbum:
		e.Text = "" // iOS shows an album header as an empty message
	}
	return e
}

func callText(call *wa.ParsedCallEvent) string {
	kind := "Voice call"
	if call.Media == "video" {
		kind = "Video call"
	}
	if call.DurationSecs <= 0 {
		return "Missed " + strings.ToLower(kind) + ", ‎Tap to call back"
	}
	s := kind + ", ‎‎" + formatDuration(call.DurationSecs)
	if n := len(call.Participants); n > 1 {
		s += fmt.Sprintf(" • ‎%d joined", n)
	}
	return s
}

func formatDuration(secs int64) string {
	switch {
	case secs < 60:
		return fmt.Sprintf("%d sec", secs)
	case secs < 3600:
		return fmt.Sprintf("%d min", secs/60)
	default:
		h, m := secs/3600, (secs%3600)/60
		if m == 0 {
			return fmt.Sprintf("%d hr", h)
		}
		return fmt.Sprintf("%d hr %d min", h, m)
	}
}

func (a *Archiver) applyDelete(ctx context.Context, b *batch, c *archive.Chat, pm wa.ParsedMessage, row archive.Row) {
	deletedByMe := pm.FromMe && !pm.AdminRevoke
	// The deleted message's own sender: an admin may delete others'.
	if pm.TargetSender != "" {
		if j, err := types.ParseJID(pm.TargetSender); err == nil && !j.IsEmpty() {
			pm.SenderJID, pm.FromMe, pm.PushName = pm.TargetSender, a.names.isOwn(j), ""
			row.Sender, row.FromMe = pm.SenderJID, pm.FromMe
		}
	}
	text := archive.DeletedByThem
	if deletedByMe && pm.FromMe {
		text = archive.DeletedByMe
	}
	a.media.forget(c.JID(), pm.ID)
	existed, err := c.MarkDeleted(pm.ID, text)
	if existed || err != nil {
		a.wrote(b, c, err)
		return
	}
	// The original never reached us; keep the tombstone at deletion time.
	sender := a.senderName(ctx, pm)
	fallback := a.isFallbackSender(pm, sender)
	if fallback {
		a.needsNames(c.JID())
	}
	_, inserted, err := c.Insert(row, func(int) archive.Entry {
		return archive.Entry{Sender: sender, Deleted: text}
	})
	if inserted {
		a.added.Add(1)
		if fallback {
			a.needsNames(c.JID()) // as in archiveMessage
		}
	}
	a.wrote(b, c, err)
}

// chat returns the archive chat for jid, creating its folder on first use.
func (a *Archiver) chat(ctx context.Context, jid types.JID, hint string) (*archive.Chat, error) {
	jid = a.canonical(ctx, jid)
	key := jid.String()
	if _, ok := a.ar.Info(key); ok {
		return a.ar.Chat(key)
	}
	name := archive.CleanName(a.names.chat(ctx, jid, hint))
	kind, suffix := chatKind(jid)
	c, created, err := a.ar.EnsureChat(key, name, kind, suffix)
	if err == nil && created {
		a.logf("new chat: %s", filepath.Base(c.Dir()))
		if IsFallbackName(name, key) {
			a.needsNames(key)
		}
	}
	return c, err
}

// chatKind returns the chat's kind and the suffix that tells its folder apart
// from another chat's with the same name (a number for people; groups get
// "(2)", "(3)", …).
func chatKind(jid types.JID) (kind, suffix string) {
	switch jid.Server {
	case types.GroupServer:
		return "group", ""
	case types.DefaultUserServer:
		return "dm", "+" + jid.User
	}
	return "dm", ""
}

// canonical keys DMs by phone number when WhatsApp tells us the mapping, so
// one person never ends up in two folders.
func (a *Archiver) canonical(ctx context.Context, jid types.JID) types.JID {
	jid = jid.ToNonAD()
	if jid.Server == types.HiddenUserServer {
		if pn := a.wa.ResolveLIDToPN(ctx, jid); !pn.IsEmpty() && pn.Server == types.DefaultUserServer {
			return pn.ToNonAD()
		}
	}
	return jid
}

// invisiblePayload reports content-free messages a phone never shows: records
// with no body at all, and protocol traffic between your own devices. Other
// unknown payloads stay as empty entries, as iOS writes them.
func invisiblePayload(payload string) bool {
	if payload == "" {
		return true
	}
	for _, field := range strings.Split(payload, ",") {
		switch field {
		case "protocolMessage", "senderKeyDistributionMessage", "fastRatchetKeySenderKeyDistributionMessage", "keepInChatMessage", "encReactionMessage", "pinInChatMessage":
		default:
			return false
		}
	}
	return true
}

func skipChat(jid types.JID) bool {
	switch jid.Server {
	case types.BroadcastServer, types.NewsletterServer:
		return true
	}
	return false
}

// RecordSent archives a message this device just sent; WhatsApp does not
// echo it back as an event. data is the attachment as uploaded (nil for
// text): it is the file, so it goes straight into the chat folder instead of
// being downloaded back.
func (a *Archiver) RecordSent(ctx context.Context, to types.JID, id types.MessageID, msg *waE2E.Message, data []byte) {
	ctx = context.WithoutCancel(ctx) // it was sent: archive it even if the client left
	own, _ := types.ParseJID(a.wa.LinkedJID())
	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: to, Sender: own, IsFromMe: true, IsGroup: to.Server == types.GroupServer},
			ID:            id,
			Timestamp:     time.Now(),
		},
		Message: msg,
	}
	chat := a.canonical(ctx, to).String()
	if data != nil {
		a.media.reserve(chat, string(id)) // no worker downloads our own upload
	}
	a.archiveMessage(ctx, nil, wa.ParseLiveMessage(evt), "")
	if data != nil {
		if err := a.media.placeOwn(chat, string(id), data); err != nil {
			a.logf("warning: archiving the sent file (it will be downloaded instead): %v", err)
		}
	}
}

// fillBlank gives an entry archived empty (by a version that couldn't read
// its kind) the text of the redelivered message. Media isn't filled in.
func (a *Archiver) fillBlank(ctx context.Context, b *batch, c *archive.Chat, pm wa.ParsedMessage) {
	if pm.Media != nil || pm.Edited || !pm.HasContent() {
		return
	}
	fill := a.entryFor(ctx, pm, "")
	if fill.Text == "" {
		return
	}
	_, err := c.Update(pm.ID, func(e *archive.Entry) bool {
		if e.Text != "" || e.System || e.Omitted != "" || e.Attachment != "" || e.Deleted != "" {
			return false
		}
		e.Text, e.System = fill.Text, fill.System
		return true
	})
	a.wrote(b, c, err)
}
