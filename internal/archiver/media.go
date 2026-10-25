package archiver

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/ferdousbhai/wacli/internal/fsutil"

	"github.com/ferdousbhai/wacli/internal/archive"
	"github.com/ferdousbhai/wacli/internal/wa"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// pendingMedia is an attachment whose entry says "… omitted" until the file
// is downloaded. Kept in .wacli/pending-media.json so restarts resume.
type pendingMedia struct {
	Chat      string    `json:"chat"`    // archive chat JID
	WAChat    string    `json:"wa_chat"` // chat JID as WhatsApp addressed it
	ID        string    `json:"id"`
	Sender    string    `json:"sender"`
	FromMe    bool      `json:"from_me"`
	TS        int64     `json:"ts"`
	Name      string    `json:"name"` // file name in the chat folder
	Type      string    `json:"type"`
	Direct    string    `json:"direct_path"`
	Key       []byte    `json:"media_key"`
	SHA256    []byte    `json:"sha256"`
	EncSHA256 []byte    `json:"enc_sha256"`
	Length    uint64    `json:"length"`
	Attempts  int       `json:"attempts"`
	NextTry   time.Time `json:"next_try"`
	Gone      bool      `json:"gone,omitempty"` // phone no longer has it
}

func (p pendingMedia) key() string { return p.Chat + "|" + p.ID }

type mediaQueue struct {
	a    *Archiver
	path string

	mu      sync.Mutex
	items   map[string]*pendingMedia
	active  map[string]bool
	wake    chan struct{}
	retries map[string]chan *events.MediaRetry
	dirty   bool
	// placed holds downloads moved into their chat folder whose entry isn't
	// on disk yet; settle writes those chats once and then dequeues them.
	placed     map[string][]string // chat JID → item keys
	placedKeys map[string]bool     // the same keys; due skips them

	diskFullLogged bool
}

func diskFull(err error) bool {
	return errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT) || errors.Is(err, syscall.EROFS)
}

const mediaWorkers = 8

func newMediaQueue(a *Archiver) (*mediaQueue, error) {
	q := &mediaQueue{
		a:          a,
		path:       filepath.Join(a.ar.Root(), ".wacli", "pending-media.json"),
		items:      map[string]*pendingMedia{},
		active:     map[string]bool{},
		wake:       make(chan struct{}, 1),
		retries:    map[string]chan *events.MediaRetry{},
		placed:     map[string][]string{},
		placedKeys: map[string]bool{},
	}
	data, err := os.ReadFile(q.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if len(data) > 0 {
		var list []*pendingMedia
		if err := json.Unmarshal(data, &list); err != nil {
			// Don't let a damaged queue stop sync: keep it for inspection
			// and start empty (those entries stay "omitted").
			aside := fmt.Sprintf("%s.corrupt-%d", q.path, time.Now().Unix())
			a.logf("warning: download queue unreadable (%v); moved to %s", err, aside)
			if err := os.Rename(q.path, aside); err != nil {
				return nil, err
			}
			list = nil
		}
		for _, p := range list {
			if p.Gone {
				continue // from versions that kept these
			}
			q.items[p.key()] = p // NextTry kept: a restart doesn't ask the phone again early
		}
	}
	return q, nil
}

func (q *mediaQueue) add(c *archive.Chat, pm wa.ParsedMessage, name string) {
	m := pm.Media
	p := &pendingMedia{
		Chat: c.JID(), WAChat: pm.Chat.String(), ID: pm.ID, Sender: pm.SenderJID, FromMe: pm.FromMe,
		TS: pm.Timestamp.Unix(), Name: name, Type: m.Type, Direct: m.DirectPath, Key: m.MediaKey,
		SHA256: m.FileSHA256, EncSHA256: m.FileEncSHA256, Length: m.FileLength,
	}
	q.mu.Lock()
	q.items[p.key()] = p
	q.saveLocked()
	q.mu.Unlock()
	q.poke()
}

func (q *mediaQueue) forget(chat, id string) {
	q.mu.Lock()
	delete(q.items, chat+"|"+id)
	q.saveLocked()
	q.mu.Unlock()
}

func (q *mediaQueue) len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

func (q *mediaQueue) poke() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// saveLocked marks the queue for saving; the saver writes it at most every
// couple of seconds (the queue can hold thousands of items).
func (q *mediaQueue) saveLocked() { q.dirty = true }

// flush writes the queue if it changed. On failure it stays marked, so the
// next flush tries again.
func (q *mediaQueue) flush() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.dirty {
		return
	}
	if err := q.writeLocked(); err != nil {
		q.a.logf("warning: saving the download queue: %v", err)
		return
	}
	q.dirty = false
}

func (q *mediaQueue) writeLocked() error {
	list := make([]*pendingMedia, 0, len(q.items))
	for _, p := range q.items {
		list = append(list, p)
	}
	return fsutil.WriteAtomic(q.path, q.a.ar.TmpDir(), 0o600, func(w *bufio.Writer) error {
		return json.NewEncoder(w).Encode(list)
	})
}

func (q *mediaQueue) run(ctx context.Context) {
	var wg sync.WaitGroup
	jobs := make(chan *pendingMedia)
	for range mediaWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range jobs {
				q.download(ctx, p)
				q.mu.Lock()
				delete(q.active, p.key())
				q.mu.Unlock()
				q.poke()
			}
		}()
	}
	save := time.NewTicker(time.Second)
	defer save.Stop()
	check := time.NewTicker(30 * time.Second)
	defer check.Stop()
	defer q.flush()
	defer q.settle()
	defer wg.Wait()
	defer close(jobs)
	for {
		for _, p := range q.due() {
			// A backlog takes a while to hand out; keep writing finished
			// downloads and the queue meanwhile.
			for sent := false; !sent; {
				select {
				case jobs <- p:
					sent = true
				case <-save.C:
					q.settle()
					q.flush()
				case <-ctx.Done():
					return
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-save.C:
			q.settle()
			q.flush()
		case <-q.wake:
		case <-check.C:
		}
	}
}

// tmpDir holds downloads in progress; opening the archive clears it.
func (q *mediaQueue) tmpDir() string { return filepath.Join(q.a.ar.TmpDir(), "media") }

// settle writes the chats that received files and only then takes those
// files off the queue, so a crash can't leave an entry "omitted" with
// nothing left to fetch it.
func (q *mediaQueue) settle() {
	q.mu.Lock()
	placed := q.placed
	q.placed = map[string][]string{}
	q.mu.Unlock()
	for jid, keys := range placed {
		c, err := q.a.ar.Chat(jid)
		if err == nil {
			err = c.Flush()
		}
		q.mu.Lock()
		if err != nil {
			q.placed[jid] = append(q.placed[jid], keys...) // try again next time
		} else {
			for _, k := range keys {
				delete(q.items, k)
				delete(q.placedKeys, k)
			}
			q.saveLocked()
		}
		q.mu.Unlock()
		if err != nil {
			q.a.logf("warning: %v", err)
		}
	}
}

func (q *mediaQueue) due() []*pendingMedia {
	if !q.a.wa.IsConnected() {
		return nil
	}
	return q.take(time.Now())
}

// take marks and returns the items ready to download at now: not running,
// not already placed and waiting for their chat to be written, not backing off.
func (q *mediaQueue) take(now time.Time) []*pendingMedia {
	q.mu.Lock()
	defer q.mu.Unlock()
	var out []*pendingMedia
	for k, p := range q.items {
		if q.active[k] || q.placedKeys[k] || now.Before(p.NextTry) {
			continue
		}
		q.active[k] = true
		cp := *p
		out = append(out, &cp)
	}
	return out
}

// keys returns the queued items' keys.
func (q *mediaQueue) keys() map[string]bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make(map[string]bool, len(q.items))
	for k := range q.items {
		out[k] = true
	}
	return out
}

// wait returns once none of the given items is still queued (downloaded and
// written, given up, or backing off after a failure), or ctx ends.
func (q *mediaQueue) wait(ctx context.Context, keys map[string]bool) {
	for {
		busy := false
		q.mu.Lock()
		now := time.Now()
		for k := range keys {
			if p, ok := q.items[k]; ok && !now.Before(p.NextTry) {
				busy = true
			}
		}
		q.mu.Unlock()
		if !busy || !q.a.wa.IsConnected() {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (q *mediaQueue) download(ctx context.Context, p *pendingMedia) {
	c, err := q.a.ar.Chat(p.Chat)
	if err != nil {
		q.forget(p.Chat, p.ID)
		return
	}
	if e, ok := c.Get(p.ID); !ok || e.Attachment != "" || e.Deleted != "" {
		// Gone, deleted, or already restored by `wacli import`.
		q.forget(p.Chat, p.ID)
		return
	}
	if !archive.Downloadable(p.Type) {
		// Queued by older versions (locations); nothing to download.
		q.dropUndownloadable(c, p)
		return
	}
	// A stalled transfer must not hold a worker forever: allow two minutes
	// plus time for the file at a slow 32 KB/s (and the phone's answer).
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute+time.Duration(p.Length/(32<<10))*time.Second)
	defer cancel()
	// Download outside the chat folder: a rename may move the folder
	// meanwhile, and PlaceAttachment puts the file wherever it is then.
	tmp := filepath.Join(q.tmpDir(), fmt.Sprintf("%x.part", sha256.Sum256([]byte(p.key()))))
	askedPhone := p.Direct == ""
	if askedPhone {
		// No server copy to fetch; only the phone can upload it.
		err = q.retryFromPhone(ctx, p, tmp)
	} else if _, err = q.a.wa.DownloadMediaToFile(ctx, p.Direct, p.EncSHA256, p.SHA256, p.Key, p.Length, p.Type, tmp); err != nil && isExpired(err) {
		askedPhone = true
		err = q.retryFromPhone(ctx, p, tmp)
	}
	if err != nil {
		if q.a.ctx.Err() != nil || !q.a.wa.IsConnected() {
			return // shutting down or offline: not the file's fault; stays queued
		}
		q.failed(p, err, askedPhone)
		return
	}
	if _, err := c.PlaceAttachment(p.ID, tmp, p.Name); err != nil {
		_ = os.Remove(tmp)
		q.failed(p, err, askedPhone)
		return
	}
	q.mu.Lock()
	q.placed[p.Chat] = append(q.placed[p.Chat], p.key())
	q.placedKeys[p.key()] = true
	q.diskFullLogged = false // space again: a later full disk is logged anew
	q.mu.Unlock()
	q.a.touch()
}

func (q *mediaQueue) dropUndownloadable(c *archive.Chat, p *pendingMedia) {
	_, err := c.Update(p.ID, func(e *archive.Entry) bool {
		if e.Omitted == "" {
			return false
		}
		text := "Location"
		if e.Text != "" {
			text += ": " + e.Text
		}
		e.MakeSystem(text)
		return true
	})
	if err != nil {
		q.a.logf("warning: %v", err)
	}
	q.forget(p.Chat, p.ID)
}

// reserve keeps workers away from a download that is about to be satisfied
// from local bytes (placeOwn releases it).
func (q *mediaQueue) reserve(chat, id string) {
	q.mu.Lock()
	q.active[chat+"|"+id] = true
	q.mu.Unlock()
}

// placeOwn puts a file this device sent into its chat folder, in place of
// the download its entry queued, and writes the entry.
func (q *mediaQueue) placeOwn(chat, id string, data []byte) (err error) {
	key := chat + "|" + id
	defer func() {
		q.mu.Lock()
		delete(q.active, key) // reserved by reserve
		q.mu.Unlock()
		if err != nil {
			q.poke() // fall back to downloading it
		}
	}()
	q.mu.Lock()
	p, ok := q.items[key]
	q.mu.Unlock()
	if !ok {
		return nil // not a downloadable attachment
	}
	c, err := q.a.ar.Chat(chat)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(q.a.ar.TmpDir(), "sent-*")
	if err != nil {
		return err
	}
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Close()
	} else {
		tmp.Close()
	}
	if err == nil {
		_, err = c.PlaceAttachment(id, tmp.Name(), p.Name)
	}
	if err == nil {
		err = c.Flush()
	}
	if err != nil {
		os.Remove(tmp.Name())
		return err
	}
	q.forget(chat, id)
	return nil
}

// name is the file name a queued download will get, or "".
func (q *mediaQueue) name(chat, id string) string {
	q.mu.Lock()
	defer q.mu.Unlock()
	if p, ok := q.items[chat+"|"+id]; ok {
		return p.Name
	}
	return ""
}

// queued reports whether a download for this message is queued or running.
func (q *mediaQueue) queued(chat, id string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	_, ok := q.items[chat+"|"+id]
	return ok
}

// retryAfter is the wait before each retry; after the last one wacli gives
// up. Most retries ask the phone, so they are few.
var retryAfter = []time.Duration{15 * time.Minute, 2 * time.Hour}

// failed counts a try, unless the disk is full and the phone wasn't asked
// (each ask shows on the phone, so those stay limited).
func (q *mediaQueue) failed(p *pendingMedia, err error, askedPhone bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	cur, ok := q.items[p.key()]
	if !ok {
		return
	}
	if diskFull(err) && !askedPhone {
		// Not the file's fault: wait for space without using up its tries.
		if !q.diskFullLogged {
			q.a.logf("disk full or read-only (%v); downloads paused", err)
			q.diskFullLogged = true
		}
		cur.NextTry = time.Now().Add(15 * time.Minute)
		q.saveLocked()
		return
	}
	q.diskFullLogged = false
	cur.Attempts++
	switch {
	case p.Gone:
		// The entry stays "… omitted", as in an export without that file.
		q.a.logf("media no longer on the phone, giving up: %s", p.Name)
		delete(q.items, p.key())
	case cur.Attempts > len(retryAfter):
		q.a.logf("media download failed %d times (%v), giving up: %s", cur.Attempts, err, p.Name)
		delete(q.items, p.key())
	default:
		cur.NextTry = time.Now().Add(retryAfter[cur.Attempts-1])
		if cur.Attempts == 1 {
			q.a.logf("media download failed (%v), will retry: %s", err, p.Name)
		}
	}
	q.saveLocked()
}

// retryFromPhone asks the phone to re-upload expired media.
func (q *mediaQueue) retryFromPhone(ctx context.Context, p *pendingMedia, target string) error {
	chat, err := types.ParseJID(p.WAChat)
	if err != nil {
		return err
	}
	info := &types.MessageInfo{
		MessageSource: types.MessageSource{Chat: chat, IsFromMe: p.FromMe, IsGroup: chat.Server == types.GroupServer},
		ID:            p.ID,
		Timestamp:     time.Unix(p.TS, 0),
	}
	if s, err := types.ParseJID(p.Sender); err == nil {
		info.Sender = s
	}
	ch := make(chan *events.MediaRetry, 1)
	q.mu.Lock()
	q.retries[p.ID] = ch
	q.mu.Unlock()
	defer func() {
		q.mu.Lock()
		delete(q.retries, p.ID)
		q.mu.Unlock()
	}()
	if err := q.a.wa.SendMediaRetryReceipt(ctx, info, p.Key); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(60 * time.Second):
		return errors.New("phone did not answer media retry")
	case evt := <-ch:
		path, code, err := wa.DecryptMediaRetry(evt, p.Key)
		if err != nil {
			return err
		}
		if code == wa.MediaRetryNotFound {
			p.Gone = true
			return errors.New("media no longer on phone")
		}
		if code != wa.MediaRetrySuccess || path == "" {
			return errors.New("phone could not re-upload media")
		}
		_, err = q.a.wa.DownloadRetriedMediaToFile(ctx, path, p.SHA256, p.Key, p.Length, p.Type, target)
		return err
	}
}

func (q *mediaQueue) onRetry(evt *events.MediaRetry) {
	q.mu.Lock()
	ch := q.retries[string(evt.MessageID)]
	q.mu.Unlock()
	if ch != nil {
		select {
		case ch <- evt:
		default:
		}
	}
}

func isExpired(err error) bool {
	return errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith403) ||
		errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith404) ||
		errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith410)
}
