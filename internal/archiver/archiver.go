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
	ar *archive.Archive
	wa *wa.Client
	// requestHistory sends an on-demand history request (wa's, or a test's).
	requestHistory func(context.Context, types.MessageInfo, int, func(types.MessageID)) (types.MessageID, error)
	names          *names
	media          *mediaQueue
	status         *statusTracker
	logf           func(format string, args ...any)

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup // background goroutines
	// handling is read-locked by every event handler; Close takes it to wait
	// for handlers in flight before saving.
	handling sync.RWMutex
	closed   bool

	added        atomic.Int64
	onDemand     atomic.Int64 // on-demand history replies received
	lastActivity atomic.Int64 // when something was last archived; 0: not yet
	started      time.Time
	namesChanged chan struct{}
	phoneSeen    chan struct{} // WhatsApp is open on the phone
	refetchRun   sync.Mutex    // one Refetch at a time
	checked      *checkedLines

	mu             sync.Mutex
	backfill       map[string]chan historyBatch  // chat JID → backfill waiting for a page
	backfillReq    map[string]string             // chat JID → its backfill's latest request
	refetch        map[string]chan refetchAnswer // message ID → Refetch waiting for it
	refetchWait    *refetchWaiter                // the request a Refetch waits on
	refetchStrikes map[string]int                // message ID → requests that told nothing
	seenPayload    map[string]bool
	namesWanted    map[string]bool // chats flagged NeedsNames since a refresh began
}

type historyBatch struct {
	last   *archive.Row      // last message of the page, in the phone's order
	n      int               // messages in the page
	oldest int64             // timestamp of its oldest message
	kinds  map[string]string // on demand: message ID → its type
}

func New(ar *archive.Archive, client *wa.Client, logf func(string, ...any)) (*Archiver, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	a := &Archiver{
		ar:             ar,
		wa:             client,
		requestHistory: client.RequestHistorySyncOnDemand,
		names:          newNames(client),
		status:         newStatusTracker(ar.Root()),
		started:        time.Now(),
		logf:           logf,
		ctx:            context.Background(),
		cancel:         func() {},
		namesChanged:   make(chan struct{}, 1),
		phoneSeen:      make(chan struct{}, 1),
		checked:        loadCheckedLines(ar.Root(), logf),
		backfill:       map[string]chan historyBatch{},
		backfillReq:    map[string]string{},
		refetch:        map[string]chan refetchAnswer{},
		refetchStrikes: map[string]int{},
		seenPayload:    map[string]bool{},
		namesWanted:    map[string]bool{},
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
	a.checked.save(a.ar.TmpDir(), a.logf)
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
		if v.Info.IsFromMe && v.Info.Sender.Device == 0 {
			a.phoneActive(v.Info.Timestamp) // sent from the phone
		}
		a.archiveMessage(ctx, nil, wa.ParseLiveMessage(v), "")
	case *events.Receipt:
		if v.IsFromMe && v.Sender.Device == 0 && (v.Type == types.ReceiptTypeRead || v.Type == types.ReceiptTypeReadSelf) {
			a.phoneActive(v.Timestamp) // a chat read on the phone
		}
	case *events.HistorySync:
		a.archiveHistory(ctx, v.Data, v.Notification.GetPeerDataRequestSessionID())
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

// archiveHistory archives a history chunk. session is the ID of the
// on-demand request it answers, when the phone says.
func (a *Archiver) archiveHistory(ctx context.Context, data *waHistorySync.HistorySync, session string) {
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
	type wanted struct {
		ch     chan refetchAnswer
		chatID string
		web    *waWeb.WebMessageInfo
	}
	var refetched []wanted
	for _, conv := range data.GetConversations() {
		chatID := strings.TrimSpace(conv.GetID())
		if chatID == "" {
			continue
		}
		// The phone pairs the chat's hidden ID with its number here; stored
		// first, so its messages land in one folder.
		lid, pn := conv.GetLidJID(), conv.GetPnJID()
		if lid == "" && strings.HasSuffix(chatID, "@lid") {
			lid = chatID
		} else if pn == "" && !strings.HasSuffix(chatID, "@g.us") {
			pn = chatID
		}
		a.wa.StoreLIDMapping(ctx, lid, pn)
		hint := conv.GetName()
		if hint == "" {
			hint = conv.GetDisplayName()
		}
		if msgs := conv.GetMessages(); debug && data.GetSyncType() == waHistorySync.HistorySync_ON_DEMAND && len(msgs) > 0 {
			first, last := msgs[0].GetMessage(), msgs[len(msgs)-1].GetMessage()
			a.logf("debug: on-demand history for %s: %d messages, %s %d … %s %d", chatID, len(msgs),
				first.GetKey().GetID(), first.GetMessageTimestamp(), last.GetKey().GetID(), last.GetMessageTimestamp())
		}
		var page historyBatch
		for _, m := range conv.GetMessages() {
			web := m.GetMessage()
			if web == nil {
				continue
			}
			a.archiveWeb(ctx, b, chatID, hint, web)
			if ch := a.refetchWanted(web.GetKey().GetID()); ch != nil {
				refetched = append(refetched, wanted{ch, chatID, web})
			}
			// Pages come in the phone's storage order, which is not always
			// time order; the next page starts after the last message here.
			key := web.GetKey()
			sender := web.GetParticipant()
			if sender == "" {
				sender = key.GetParticipant()
			}
			page.last = &archive.Row{ID: key.GetID(), TS: int64(web.GetMessageTimestamp()), FromMe: key.GetFromMe(), Sender: sender}
			if ts := page.last.TS; page.n == 0 || ts < page.oldest {
				page.oldest = ts
			}
			page.n++
			if data.GetSyncType() == waHistorySync.HistorySync_ON_DEMAND {
				if page.kinds == nil {
					page.kinds = map[string]string{}
				}
				page.kinds[key.GetID()] = webKind(chatID, web)
			}
		}
		pages[chatID] = page
	}
	for _, c := range b.held {
		if err := c.Release(); err != nil {
			a.logf("warning: writing %s: %v", c.Name(), err)
		}
	}
	a.media.flush() // downloads queued by this chunk, before it is acknowledged
	for _, w := range refetched {
		a.answerRefetch(ctx, w.ch, w.chatID, w.web)
	}
	if data.GetSyncType() == waHistorySync.HistorySync_ON_DEMAND {
		a.onDemand.Add(1)
		// A waiting backfill reads the chat, so tell it only once written.
		for chatID, page := range pages {
			if jid, err := types.ParseJID(chatID); err == nil && a.deliverRefetch(session, a.chatKey(ctx, jid).String(), page) {
				continue
			}
			a.deliverBackfill(ctx, chatID, session, page)
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
	if pm.Call != nil && !pm.FromMe {
		return // call records come from your own phone; others' are forged
	}
	// Live edits, deletes and redeliveries name a message by ID only; the
	// phone checked history ones, but live, anyone in the chat could name
	// someone else's message. Checked before a folder is made for it.
	c, err := a.knownChat(ctx, pm.Chat)
	if err == nil && b == nil && !a.mayArchiveLive(ctx, c, pm) {
		a.logf("ignored %s in %s: it can't be checked as its sender's", pm.ID, pm.Chat)
		return
	}
	if err == nil && c == nil {
		c, err = a.chat(ctx, pm.Chat, hint)
	}
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
		a.requeueMedia(ctx, b, c, pm)
		a.fillBlank(ctx, b, c, pm)
		a.refillVCard(b, c, pm)
		return
	}

	sender := a.senderName(ctx, pm)
	e := a.entryFor(ctx, pm, sender)
	fallback := a.isFallbackSender(pm, sender)
	if fallback {
		a.needsNames(c.JID()) // before the entry is on disk, so a crash can't lose the flag
	}
	var vcf string
	_, inserted, err := c.Insert(row, func(pos int) archive.Entry {
		switch {
		case pm.Media != nil && pm.Location == nil && archive.Downloadable(pm.Media.Type):
			e.Omitted = archive.OmittedText(pm.Media.Type)
			name := archive.AttachmentName(pos, pm.Media.Type, pm.Timestamp.In(a.ar.Location()), pm.Media.Filename)
			a.media.add(c, pm, name)
		case pm.VCard != nil:
			// Exports attach a shared contact as a .vcf; the card is in the message.
			e.Omitted = contactOmitted
			vcf = archive.AttachmentName(pos, "document", pm.Timestamp, contactFileName(pm.VCard.Name))
		}
		return e
	})
	if inserted && vcf != "" && err == nil {
		err = a.placeVCard(c, pm.ID, vcf, pm.VCard.Data)
	}
	if inserted && blank(e) {
		// Its type is known now, so refetch needn't ask the phone about it.
		kind := describe(pm)
		if kind != "no content" {
			a.checked.note(c.JID(), pm.ID, kind)
		}
		if !pm.IsAlbum { // albums are empty lines in exports too
			a.logf("note: %s in %s archived as an empty line: %s", pm.ID, c.Name(), kind)
		}
	}
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
func (a *Archiver) requeueMedia(ctx context.Context, b *batch, c *archive.Chat, pm wa.ParsedMessage) {
	if pm.Media == nil || pm.Location != nil || !archive.Downloadable(pm.Media.Type) || a.media.queued(c.JID(), pm.ID) {
		return
	}
	e, ok := c.Get(pm.ID)
	if !ok || e.Deleted != "" || e.Attachment != "" {
		return
	}
	if e.Omitted == "" {
		// Archived empty by a version that couldn't read this media kind
		// (e.g. video notes): fill it in as omitted, then download.
		if e.Text != "" || e.System {
			return
		}
		fill := a.entryFor(ctx, pm, "")
		_, err := c.Update(pm.ID, func(e *archive.Entry) bool {
			if e.Text != "" || e.System || e.Omitted != "" || e.Attachment != "" || e.Deleted != "" {
				return false
			}
			e.Text, e.DocLabel, e.Omitted = fill.Text, fill.DocLabel, archive.OmittedText(pm.Media.Type)
			return true
		})
		a.wrote(b, c, err)
		if err != nil {
			return
		}
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

// unrendered notes, once per type, an edit wacli can't read: the message
// keeps its earlier text.
func (a *Archiver) unrendered(payload string) {
	a.mu.Lock()
	seen := a.seenPayload[payload]
	a.seenPayload[payload] = true
	a.mu.Unlock()
	if !seen {
		a.logf("note: edits sent as %s can't be read yet; those messages keep their earlier text", payload)
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
		e.Text = a.names.mentions(ctx, e.Text, pm.MentionedJIDs)
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
	case pm.VCard != nil:
		e.Text = "" // the card is attached as a file
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
	if c, err := a.knownChat(ctx, jid); c != nil || err != nil {
		return c, err
	}
	jid = a.chatKey(ctx, jid)
	key := jid.String()
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
		case "protocolMessage", "senderKeyDistributionMessage", "fastRatchetKeySenderKeyDistributionMessage", "keepInChatMessage", "encReactionMessage", "pinInChatMessage",
			"encEventResponseMessage", "secretEncryptedMessage": // event RSVPs, encrypted edits
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
	chat := a.chatKey(ctx, to).String()
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

// mayArchiveLive vets a live message, which names its target by an ID the
// sender chose. IDs with ":" are wacli's own (group changes, imports,
// repairs); no WhatsApp message has one. A change to a message not
// archived yet can't be checked: an edit is dropped (the original comes
// with history), and a delete is kept as the sender's own or a group
// admin's.
// c is nil for a chat not archived yet.
func (a *Archiver) mayArchiveLive(ctx context.Context, c *archive.Chat, pm wa.ParsedMessage) bool {
	switch {
	case strings.Contains(pm.ID, ":"):
		return false
	case c != nil && c.Has(pm.ID):
		return a.mayChange(ctx, c, pm)
	case pm.Edited:
		return false
	case pm.Revoked && pm.TargetSender != "":
		// Someone else's message: only an admin may delete it.
		return pm.FromMe || a.isAdmin(ctx, pm.Chat, pm.SenderJID)
	}
	return true
}

// knownChat returns the archived chat for jid, or nil; it never makes one.
func (a *Archiver) knownChat(ctx context.Context, jid types.JID) (*archive.Chat, error) {
	key := a.chatKey(ctx, jid).String()
	if _, ok := a.ar.Info(key); !ok {
		return nil, nil
	}
	return a.ar.Chat(key)
}

// chatKey is the key of jid's chat: by phone number when WhatsApp tells us
// the mapping, but a chat already archived under the other one (its hidden
// ID, before the mapping was known) keeps its key, so one person never ends
// up in two folders.
func (a *Archiver) chatKey(ctx context.Context, jid types.JID) types.JID {
	jid = jid.ToNonAD()
	key := a.canonical(ctx, jid)
	if _, ok := a.ar.Info(key.String()); ok {
		return key
	}
	for _, alt := range []types.JID{jid, a.wa.LookupPNToLID(ctx, key).ToNonAD()} {
		if _, ok := a.ar.Info(alt.String()); ok && !alt.IsEmpty() {
			return alt
		}
	}
	return key
}

// mayChange reports whether pm may change the archived message with its
// ID: it comes from that message's sender, or deletes it as a group admin.
func (a *Archiver) mayChange(ctx context.Context, c *archive.Chat, pm wa.ParsedMessage) bool {
	row, ok := c.Row(pm.ID)
	if !ok || (row.Sender == "" && !row.FromMe) {
		return true // nothing to compare with (imported from an export)
	}
	if pm.Revoked && pm.TargetSender != "" { // an admin deleting someone else's
		if !row.FromMe && !a.samePerson(ctx, row.Sender, pm.TargetSender) {
			return false
		}
		return pm.FromMe || a.isAdmin(ctx, pm.Chat, pm.SenderJID) // your own phone checked you are one
	}
	if row.FromMe || pm.FromMe {
		return row.FromMe == pm.FromMe
	}
	return a.samePerson(ctx, row.Sender, pm.SenderJID)
}

func (a *Archiver) samePerson(ctx context.Context, x, y string) bool {
	jx, err1 := types.ParseJID(x)
	jy, err2 := types.ParseJID(y)
	if err1 != nil || err2 != nil || jx.IsEmpty() || jy.IsEmpty() {
		return false
	}
	return a.canonical(ctx, jx).User == a.canonical(ctx, jy).User
}

func (a *Archiver) isAdmin(ctx context.Context, chat types.JID, sender string) bool {
	who, err := types.ParseJID(sender)
	if err != nil || who.IsEmpty() || chat.Server != types.GroupServer {
		return false
	}
	info, err := a.wa.GetGroupInfo(ctx, chat)
	if err != nil || info == nil {
		return false // unknown: keep the message
	}
	user := a.canonical(ctx, who).User
	for _, p := range info.Participants {
		if (p.IsAdmin || p.IsSuperAdmin) && (p.JID.User == who.User || p.LID.User == who.User || p.PhoneNumber.User == user || p.JID.User == user) {
			return true
		}
	}
	return false
}

const contactOmitted = "Contact card omitted"

func contactFileName(name string) string {
	if name = strings.TrimSpace(name); name == "" {
		name = "Contact"
	}
	return name + ".vcf"
}

// refillVCard writes the card of a redelivered contact whose file a failed
// write or a crash left missing: the card is in the message.
func (a *Archiver) refillVCard(b *batch, c *archive.Chat, pm wa.ParsedMessage) {
	if pm.VCard == nil {
		return
	}
	if e, ok := c.Get(pm.ID); ok && e.Omitted == contactOmitted && e.Attachment == "" && e.Deleted == "" {
		name := archive.AttachmentName(c.Position(pm.ID), "document", pm.Timestamp, contactFileName(pm.VCard.Name))
		a.wrote(b, c, a.placeVCard(c, pm.ID, name, pm.VCard.Data))
	}
}

// placeVCard writes a shared contact's card next to the chat as its file.
func (a *Archiver) placeVCard(c *archive.Chat, id, name, data string) error {
	f, err := os.CreateTemp(a.ar.TmpDir(), "vcard-*")
	if err != nil {
		return err
	}
	_, err = f.WriteString(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		_, err = c.PlaceAttachment(id, f.Name(), name)
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}
