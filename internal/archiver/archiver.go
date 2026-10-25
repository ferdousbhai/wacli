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
	ar    *archive.Archive
	wa    *wa.Client
	names *names
	media *mediaQueue
	logf  func(format string, args ...any)

	ctx context.Context

	added        atomic.Int64
	lastActivity atomic.Int64

	waitMu sync.Mutex
	status *statusTracker

	backfill    map[string]chan historyBatch // chat JID → waiting backfill
	seenPayload map[string]bool
}

type historyBatch struct {
	last  *archive.Row // last message of the page, in the phone's order
	count int
}

func New(ar *archive.Archive, client *wa.Client, logf func(string, ...any)) (*Archiver, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	a := &Archiver{
		ar:          ar,
		wa:          client,
		names:       newNames(client),
		logf:        logf,
		ctx:         context.Background(),
		status:      newStatusTracker(),
		backfill:    map[string]chan historyBatch{},
		seenPayload: map[string]bool{},
	}
	q, err := newMediaQueue(a)
	if err != nil {
		return nil, err
	}
	a.media = q
	a.touch()
	return a, nil
}

// Start begins handling events and downloading media until ctx ends.
func (a *Archiver) Start(ctx context.Context) {
	a.ctx = ctx
	a.wa.AddEventHandler(a.handle)
	a.media.start(ctx)
	go a.reportStatus(ctx)
}

func (a *Archiver) Added() int64 { return a.added.Load() }

// Idle reports how long it has been since the last archived change.
func (a *Archiver) Idle() time.Duration {
	return time.Since(time.Unix(0, a.lastActivity.Load()))
}

func (a *Archiver) touch() { a.lastActivity.Store(time.Now().UnixNano()) }

// PendingMedia is the number of attachments not downloaded yet.
func (a *Archiver) PendingMedia() int { return a.media.len() }

// Flush saves pending state; call it before the process exits.
func (a *Archiver) Flush() { a.media.flush() }

// WaitMedia blocks until the media queue is drained or ctx ends.
func (a *Archiver) WaitMedia(ctx context.Context) { a.media.wait(ctx) }

func (a *Archiver) handle(evt any) {
	switch v := evt.(type) {
	case *events.Message:
		a.names.notePush(v.Info.Sender.String(), v.Info.PushName)
		pm := wa.ParseLiveMessage(v)
		a.archiveMessage(a.ctx, pm, nil)
	case *events.HistorySync:
		a.archiveHistory(a.ctx, v.Data)
	case *events.PushName:
		a.names.notePush(v.JID.String(), v.NewPushName)
	case *events.GroupInfo:
		a.archiveGroupInfo(a.ctx, v)
	case *events.MediaRetry:
		a.media.onRetry(v)
	case *events.Connected, *events.Disconnected, *events.LoggedOut:
		a.trackConnection(v)
	}
}

func (a *Archiver) archiveHistory(ctx context.Context, data *waHistorySync.HistorySync) {
	if data == nil {
		return
	}
	for _, p := range data.GetPushnames() {
		a.names.notePush(p.GetID(), p.GetPushname())
	}
	onDemand := data.GetSyncType() == waHistorySync.HistorySync_ON_DEMAND
	before := a.added.Load()
	type delivery struct {
		chat  string
		batch historyBatch
	}
	var deliveries []delivery
	a.ar.BeginBatch()
	defer func() {
		if err := a.ar.EndBatch(); err != nil {
			a.logf("warning: archive write failed: %v", err)
		}
		// A waiting backfill reads the chat, so tell it only once written.
		for _, d := range deliveries {
			a.deliverBackfill(d.chat, d.batch)
		}
	}()
	for _, conv := range data.GetConversations() {
		chatID := strings.TrimSpace(conv.GetID())
		if chatID == "" {
			continue
		}
		hint := conv.GetName()
		if hint == "" {
			hint = conv.GetDisplayName()
		}
		var batch historyBatch
		for _, m := range conv.GetMessages() {
			web := m.GetMessage()
			if web == nil {
				continue
			}
			batch.count++
			a.archiveWeb(ctx, chatID, hint, web)
			// Pages come in the phone's storage order, which is not always
			// time order; the next page starts after the last message here.
			key := web.GetKey()
			sender := web.GetParticipant()
			if sender == "" {
				sender = key.GetParticipant()
			}
			batch.last = &archive.Row{ID: key.GetID(), TS: int64(web.GetMessageTimestamp()), FromMe: key.GetFromMe(), Sender: sender}
		}
		if onDemand {
			deliveries = append(deliveries, delivery{chatID, batch})
		}
	}
	if n := a.added.Load() - before; n > 0 {
		a.logf("history sync (%s): %d new messages", strings.ToLower(data.GetSyncType().String()), n)
	}
}

// archiveWeb stores one history message and returns its bookkeeping row,
// whether or not it was new, so backfill can move its cursor.
func (a *Archiver) archiveWeb(ctx context.Context, chatID, hint string, web *waWeb.WebMessageInfo) (archive.Row, bool) {
	a.names.notePush(web.GetParticipant(), web.GetPushName())
	if stub := web.GetMessageStubType(); stub != 0 {
		return a.archiveStub(ctx, chatID, hint, web)
	}
	if debug && web.GetMessage() == nil {
		a.logf("debug: bodiless history record: %s", protojson.Format(web))
	}
	pm := wa.ParseHistoryMessage(chatID, web)
	return a.archiveMessage(ctx, pm, &hint)
}

func (a *Archiver) archiveMessage(ctx context.Context, pm wa.ParsedMessage, hint *string) (archive.Row, bool) {
	if pm.ID == "" || pm.Chat.IsEmpty() || skipChat(pm.Chat) {
		return archive.Row{}, false
	}
	if pm.ReactionToID != "" || pm.ReactionEmoji != "" || pm.PollVote != nil || pm.PollAdd != nil {
		return archive.Row{}, false
	}
	if !pm.HasContent() && !pm.Edited && invisiblePayload(pm.UnhandledPayload) {
		return archive.Row{}, false
	}
	chatHint := ""
	if hint != nil {
		chatHint = *hint
	}
	c, err := a.chat(ctx, pm.Chat, chatHint)
	if err != nil {
		a.logf("warning: %v", err)
		return archive.Row{}, false
	}
	row := archive.Row{ID: pm.ID, TS: pm.Timestamp.Unix(), FromMe: pm.FromMe, Sender: pm.SenderJID}

	if pm.Revoked {
		a.applyDelete(ctx, c, pm, row)
		return row, true
	}
	if pm.Edited && c.Has(pm.ID) {
		text := a.names.mentions(ctx, pm.Text, pm.MentionedJIDs)
		_, err := c.Update(pm.ID, func(e *archive.Entry, _ int) bool {
			if e.Deleted != "" || (e.Text == text && e.Edited) {
				return false
			}
			e.Text, e.Edited = text, true
			return true
		})
		a.changed(err)
		return row, true
	}
	if c.Has(pm.ID) {
		return row, true
	}

	if !pm.HasContent() && pm.UnhandledPayload != "" {
		a.unrendered(pm.UnhandledPayload)
	}
	sender := a.senderName(ctx, pm)
	_, inserted, err := c.Insert(row, func(pos int) archive.Entry {
		e := a.entryFor(ctx, pm, sender)
		if pm.Media != nil && pm.Location == nil && archive.Downloadable(pm.Media.Type) {
			e.Omitted = archive.OmittedText(pm.Media.Type)
			name := archive.AttachmentName(pos, pm.Media.Type, pm.Timestamp.In(a.ar.Location()), pm.Media.Filename)
			a.media.add(c, pm, name)
		}
		return e
	})
	if inserted {
		a.added.Add(1)
	}
	a.changed(err)
	return row, true
}

// unrendered notes, once per type, a message kind written as an empty entry
// because wacli can't render it yet.
func (a *Archiver) unrendered(payload string) {
	a.waitMu.Lock()
	seen := a.seenPayload[payload]
	a.seenPayload[payload] = true
	a.waitMu.Unlock()
	if !seen {
		a.logf("note: %s messages are archived as empty entries (not rendered yet)", payload)
	}
}

func (a *Archiver) changed(err error) {
	if err != nil {
		a.logf("warning: archive write failed: %v", err)
		return
	}
	a.touch()
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
	case strings.HasPrefix(pm.Text, "[Album"):
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

func (a *Archiver) applyDelete(ctx context.Context, c *archive.Chat, pm wa.ParsedMessage, row archive.Row) {
	text := archive.DeletedByThem
	if pm.FromMe {
		text = archive.DeletedByMe
	}
	if !c.Has(pm.ID) {
		// The original never reached us; keep the tombstone at deletion time.
		_, inserted, err := c.Insert(row, func(int) archive.Entry {
			return archive.Entry{Sender: a.senderName(ctx, pm), Deleted: text}
		})
		if inserted {
			a.added.Add(1)
		}
		a.changed(err)
		return
	}
	var attachment string
	_, err := c.Update(pm.ID, func(e *archive.Entry, _ int) bool {
		if e.Deleted != "" {
			return false
		}
		attachment = e.Attachment
		*e = archive.Entry{Time: e.Time, Sender: e.Sender, Deleted: text}
		return true
	})
	a.changed(err)
	a.media.forget(c.JID(), pm.ID)
	if attachment != "" {
		// Exports don't contain media of deleted messages; keep it aside.
		dst := filepath.Join(a.ar.Root(), ".wacli", "deleted", filepath.Base(c.Dir()))
		if err := os.MkdirAll(dst, 0o700); err == nil {
			_ = os.Rename(filepath.Join(c.Dir(), attachment), filepath.Join(dst, attachment))
		}
	}
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
	}
	return c, err
}

func chatKind(jid types.JID) (kind, suffix string) {
	switch jid.Server {
	case types.GroupServer:
		return "group", "group"
	case types.DefaultUserServer:
		return "dm", "+" + jid.User
	}
	return "dm", ""
}

// RefreshNames renames chats still named by number or ID once WhatsApp has
// told us their real name (contacts and group subjects arrive after the first
// history batches on a new link).
func (a *Archiver) RefreshNames(ctx context.Context) int {
	renamed := 0
	resolved := map[string]string{} // sender JID → name, for this pass
	for _, info := range a.ar.Chats() {
		if ctx.Err() != nil {
			break
		}
		a.refreshSenders(ctx, info.JID, resolved)
		if strings.IndexFunc(info.Folder, archive.IsBidiMark) >= 0 || archive.CleanName(info.Name) != info.Name {
			// Folders made before marks and line breaks were cleaned out.
			jid, _ := types.ParseJID(info.JID)
			_, suffix := chatKind(jid)
			if folder, err := a.ar.Rename(info.JID, archive.CleanName(info.Name), suffix); err == nil {
				a.logf("renamed %s → %s", info.Folder, folder)
				renamed++
			}
		}
		if !IsFallbackName(info.Name, info.JID) {
			continue
		}
		jid, err := types.ParseJID(info.JID)
		if err != nil {
			continue
		}
		name := a.names.chat(ctx, jid, "")
		if IsFallbackName(name, info.JID) || name == info.Name {
			continue
		}
		_, suffix := chatKind(jid)
		folder, err := a.ar.Rename(info.JID, name, suffix)
		if err != nil {
			a.logf("warning: rename %s: %v", info.Folder, err)
			continue
		}
		a.logf("renamed %s → %s", info.Folder, folder)
		renamed++
	}
	return renamed
}

// refreshSenders replaces senders written as a number before the contact's
// name was known.
func (a *Archiver) refreshSenders(ctx context.Context, chatJID string, resolved map[string]string) {
	c, err := a.ar.Chat(chatJID)
	if err != nil {
		return
	}
	own := a.names.own()
	n, err := c.RenameSenders(func(row archive.Row, sender string) (string, bool) {
		if clean := archive.CleanName(sender); clean != sender {
			return clean, true // re-rendering also cleans it
		}
		if row.FromMe {
			// Written as "You" before the profile name synced to this link.
			return own, sender == "You" && own != "You"
		}
		if row.Sender == "" || !IsFallbackName(sender, row.Sender) {
			return "", false
		}
		name, ok := resolved[row.Sender]
		if !ok {
			if jid, err := types.ParseJID(row.Sender); err == nil {
				name = a.names.contact(ctx, jid)
			}
			resolved[row.Sender] = name
		}
		if name == "" || IsFallbackName(strings.TrimPrefix(name, "~\u202f"), row.Sender) {
			return "", false
		}
		return name, true
	})
	if err != nil {
		a.logf("warning: %v", err)
	} else if n > 0 {
		a.logf("named %d senders in %s", n, filepath.Base(c.Dir()))
	}
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
// echo it back as an event.
func (a *Archiver) RecordSent(ctx context.Context, to types.JID, id types.MessageID, msg *waE2E.Message) {
	own, _ := types.ParseJID(a.wa.LinkedJID())
	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: to, Sender: own, IsFromMe: true, IsGroup: to.Server == types.GroupServer},
			ID:            id,
			Timestamp:     time.Now(),
		},
		Message: msg,
	}
	a.archiveMessage(ctx, wa.ParseLiveMessage(evt), nil)
}
