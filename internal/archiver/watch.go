package archiver

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/ferdousbhai/wacli/internal/archive"
)

// Message is one archived message, or a change to one, as `wacli watch`
// prints it. Its files are on disk when it is published.
type Message struct {
	ID     string `json:"id"`     // the message ID; an edit or delete adds ":edit-<ms>" / ":delete-<ms>"
	Thread string `json:"thread"` // the chat's JID (its archive key)
	Chat   string `json:"chat"`   // the chat's name
	At     int64  `json:"at"`     // unix ms: when it was sent, edited or deleted
	From   string `json:"from"`   // sender name as written in _chat.txt
	FromMe bool   `json:"from_me"`
	Kind   string `json:"kind"` // text | media | system | edit | delete
	Text   string `json:"text"`
	Media  string `json:"media"` // absolute path of the attachment, or ""
	Ref    string `json:"ref"`   // absolute path of the chat's _chat.txt
}

// mediaWait is how long a new media message waits for its download before
// it is published without the file.
var mediaWait = time.Minute

const watchBuffer = 1024

// watchers fans out live changes to `wacli watch` subscribers.
type watchers struct {
	mu      sync.Mutex
	subs    map[chan Message]bool
	waiting map[string]*time.Timer // chat|id → a new media message waiting for its file
	closed  bool
}

func newWatchers() *watchers {
	return &watchers{subs: map[chan Message]bool{}, waiting: map[string]*time.Timer{}}
}

// Subscribe returns live changes from now on, until cancel. The channel is
// closed if the subscriber falls too far behind (or the archiver closes).
func (a *Archiver) Subscribe() (<-chan Message, func()) {
	w := a.watch
	ch := make(chan Message, watchBuffer)
	w.mu.Lock()
	if w.closed {
		close(ch)
	} else {
		w.subs[ch] = true
	}
	w.mu.Unlock()
	return ch, func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.subs[ch] {
			delete(w.subs, ch)
			close(ch)
		}
	}
}

func (w *watchers) watched() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.subs) > 0
}

func (w *watchers) send(m Message) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for ch := range w.subs {
		select {
		case ch <- m:
		default: // never hold up archiving: drop the subscriber
			delete(w.subs, ch)
			close(ch)
		}
	}
}

func (w *watchers) close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	for ch := range w.subs {
		close(ch)
	}
	w.subs = map[chan Message]bool{}
	for _, t := range w.waiting {
		t.Stop()
	}
	w.waiting = map[string]*time.Timer{}
}

// notify publishes a live change (b nil) once it is on disk: change is ""
// for a new message, "edit" or "delete". A new message whose attachment is
// still downloading waits for the file (see mediaDone), up to mediaWait.
func (a *Archiver) notify(b *batch, c *archive.Chat, id, change string, at time.Time) {
	if b != nil || !a.watch.watched() {
		return
	}
	if change == "" && a.media.queued(c.JID(), id) {
		key := c.JID() + "|" + id
		jid := c.JID()
		a.watch.mu.Lock()
		if _, ok := a.watch.waiting[key]; !ok && !a.watch.closed {
			a.watch.waiting[key] = time.AfterFunc(mediaWait, func() { a.mediaDone(jid, id) })
		}
		a.watch.mu.Unlock()
		if !a.media.queued(jid, id) {
			a.mediaDone(jid, id) // its download finished meanwhile
		}
		return
	}
	m, ok := messageFor(c, id)
	if !ok {
		return
	}
	switch change {
	case "edit", "delete":
		m.ID += ":" + change + "-" + strconv.FormatInt(at.UnixMilli(), 10)
		m.Kind, m.At = change, at.UnixMilli()
		if change == "delete" {
			m.Text, m.Media = "", ""
		}
	case "":
		if m.Kind == "delete" || m.Kind == "" {
			return
		}
	}
	a.watch.send(m)
}

// mediaDone publishes a new media message that waited for its download,
// once the file is in place or the download stopped trying for now.
func (a *Archiver) mediaDone(chat, id string) {
	key := chat + "|" + id
	a.watch.mu.Lock()
	t, ok := a.watch.waiting[key]
	delete(a.watch.waiting, key)
	a.watch.mu.Unlock()
	if !ok {
		return
	}
	t.Stop()
	c, err := a.ar.Chat(chat)
	if err != nil {
		return
	}
	if m, ok := messageFor(c, id); ok && m.Kind != "delete" && m.Kind != "" {
		a.watch.send(m)
	}
}

// messageFor describes the archived message id as it is now. Kind is ""
// for an empty line (an album header, a kind iOS can't show).
func messageFor(c *archive.Chat, id string) (Message, bool) {
	row, ok := c.Row(id)
	if !ok {
		return Message{}, false
	}
	e, ok := c.Get(id)
	if !ok {
		return Message{}, false
	}
	dir := c.Dir()
	m := Message{
		ID: id, Thread: c.JID(), Chat: c.Name(), At: row.TS * 1000,
		From: e.Sender, FromMe: row.FromMe, Text: e.Text,
		Ref: filepath.Join(dir, archive.ChatFile),
	}
	switch {
	case e.Deleted != "":
		m.Kind, m.Text = "delete", ""
	case e.Attachment != "":
		m.Kind, m.Media = "media", filepath.Join(dir, e.Attachment)
	case e.Omitted != "":
		m.Kind = "media"
	case e.System:
		m.Kind = "system"
	case e.Text != "":
		m.Kind = "text"
	}
	return m, true
}

// MessagesSince returns archived messages sent at or after since (unix ms),
// oldest first, as they are now: deleted messages and empty lines are left
// out, and edited ones come once, with their current text.
func (a *Archiver) MessagesSince(since int64) []Message {
	sec := since / 1000
	var out []Message
	for _, info := range a.ar.Chats() {
		st, err := os.Stat(filepath.Join(a.ar.Root(), info.Folder, archive.ChatFile))
		if err != nil || st.ModTime().Unix() < sec {
			continue // not written since then
		}
		c, err := a.ar.Chat(info.JID)
		if err != nil {
			continue
		}
		for _, r := range c.Rows() {
			if r.TS < sec {
				continue
			}
			if m, ok := messageFor(c, r.ID); ok && m.Kind != "delete" && m.Kind != "" {
				out = append(out, m)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At < out[j].At })
	return out
}
