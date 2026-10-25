package archive

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	ChatFile    = "_chat.txt"
	metaDir     = ".wacli"
	chatsFile   = "chats.json"
	indexDir    = "index"
	folderStart = "WhatsApp Chat - "
)

type Options struct {
	Root       string
	DateFormat string
	Location   *time.Location
	Logf       func(format string, args ...any)
}

// Row is the bookkeeping kept for each entry: what WhatsApp needs to refer to
// a message again (edits, deletes, history anchors).
type Row struct {
	ID     string
	TS     int64 // unix seconds
	FromMe bool
	Sender string // sender JID
}

// ChatInfo is the per-chat record in .wacli/chats.json.
type ChatInfo struct {
	JID    string `json:"-"`
	Folder string `json:"folder"`
	Name   string `json:"name"`
	Kind   string `json:"kind"` // dm | group | broadcast | newsletter
	// Backfill is the oldest message of the newest contiguous run fetched
	// from the phone; older history is requested from here.
	Backfill     *Row `json:"backfill,omitempty"`
	BackfillDone bool `json:"backfill_done,omitempty"`
}

type Archive struct {
	batch   atomic.Int32
	opts    Options
	mu      sync.Mutex
	meta    map[string]*ChatInfo
	chats   map[string]*Chat
	repairs []string // folders whose index was rebuilt this run
}

// BeginBatch holds back file writes until the matching EndBatch, so a history
// sync that inserts thousands of older messages writes each chat once instead
// of once per message. Batches nest.
func (a *Archive) BeginBatch() { a.batch.Add(1) }

// EndBatch writes every chat changed during the batch.
func (a *Archive) EndBatch() error {
	if a.batch.Add(-1) > 0 {
		return nil
	}
	a.mu.Lock()
	chats := make([]*Chat, 0, len(a.chats))
	for _, c := range a.chats {
		chats = append(chats, c)
	}
	a.mu.Unlock()
	var first error
	for _, c := range chats {
		c.mu.Lock()
		if c.dirty {
			if err := c.rewriteLocked(); err != nil && first == nil {
				first = err
			}
			c.dirty = false
		}
		c.mu.Unlock()
	}
	return first
}

func (a *Archive) batching() bool { return a.batch.Load() > 0 }

// Repairs lists chats whose index had to be rebuilt since the archive was
// opened.
func (a *Archive) Repairs() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.repairs...)
}

func Open(opts Options) (*Archive, error) {
	if strings.TrimSpace(opts.Root) == "" {
		return nil, errors.New("archive root is required")
	}
	if opts.DateFormat == "" {
		opts.DateFormat = DefaultDateFormat
	}
	if opts.Location == nil {
		opts.Location = time.Local
	}
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	if err := os.MkdirAll(filepath.Join(opts.Root, metaDir, indexDir), 0o700); err != nil {
		return nil, err
	}
	a := &Archive{opts: opts, meta: map[string]*ChatInfo{}, chats: map[string]*Chat{}}
	data, err := os.ReadFile(a.metaPath(chatsFile))
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(data, &a.meta); err != nil {
			return nil, fmt.Errorf("%s: %w", chatsFile, err)
		}
	}
	for jid, info := range a.meta {
		info.JID = jid
	}
	return a, nil
}

func (a *Archive) Root() string       { return a.opts.Root }
func (a *Archive) DateFormat() string { return a.opts.DateFormat }

func (a *Archive) metaPath(parts ...string) string {
	return filepath.Join(append([]string{a.opts.Root, metaDir}, parts...)...)
}

// Chats lists known chats sorted by folder name.
func (a *Archive) Chats() []ChatInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]ChatInfo, 0, len(a.meta))
	for _, info := range a.meta {
		out = append(out, *info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Folder < out[j].Folder })
	return out
}

func (a *Archive) Info(jid string) (ChatInfo, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	info, ok := a.meta[jid]
	if !ok {
		return ChatInfo{}, false
	}
	return *info, true
}

// UpdateInfo changes a chat's record (name, backfill cursor) and saves it.
func (a *Archive) UpdateInfo(jid string, fn func(*ChatInfo)) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	info, ok := a.meta[jid]
	if !ok {
		return fmt.Errorf("unknown chat %s", jid)
	}
	fn(info)
	return a.saveMetaLocked()
}

func (a *Archive) saveMetaLocked() error {
	data, err := json.MarshalIndent(a.meta, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(a.metaPath(chatsFile), append(data, '\n'))
}

// Chat returns an existing chat.
func (a *Archive) Chat(jid string) (*Chat, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.chatLocked(jid)
}

func (a *Archive) chatLocked(jid string) (*Chat, error) {
	if c, ok := a.chats[jid]; ok {
		return c, nil
	}
	info, ok := a.meta[jid]
	if !ok {
		return nil, fmt.Errorf("unknown chat %s", jid)
	}
	c := &Chat{a: a, info: info}
	if err := c.load(); err != nil {
		return nil, err
	}
	a.chats[jid] = c
	return c, nil
}

// EnsureChat returns the chat, creating its folder on first use. suffix
// disambiguates the folder when another chat already uses the name.
func (a *Archive) EnsureChat(jid, name, kind, suffix string) (*Chat, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.meta[jid]; ok {
		c, err := a.chatLocked(jid)
		return c, false, err
	}
	folder := a.freeFolderLocked(name, suffix, jid)
	if err := os.MkdirAll(filepath.Join(a.opts.Root, folder), 0o700); err != nil {
		return nil, false, err
	}
	a.meta[jid] = &ChatInfo{JID: jid, Folder: folder, Name: name, Kind: kind}
	if err := a.saveMetaLocked(); err != nil {
		return nil, false, err
	}
	c, err := a.chatLocked(jid)
	return c, true, err
}

func (a *Archive) freeFolderLocked(name, suffix, jid string) string {
	base := folderStart + strings.TrimSpace(sanitizeFileName(name))
	if strings.TrimSpace(name) == "" {
		base = folderStart + strings.SplitN(jid, "@", 2)[0]
	}
	taken := func(f string) bool {
		for _, info := range a.meta {
			if strings.EqualFold(info.Folder, f) {
				return true
			}
		}
		_, err := os.Stat(filepath.Join(a.opts.Root, f))
		return err == nil
	}
	if !taken(base) {
		return base
	}
	if suffix != "" {
		if f := fmt.Sprintf("%s (%s)", base, sanitizeFileName(suffix)); !taken(f) {
			return f
		}
	}
	for i := 2; ; i++ {
		if f := fmt.Sprintf("%s (%d)", base, i); !taken(f) {
			return f
		}
	}
}

// Chat is one export folder. Methods are safe for concurrent use.
type Chat struct {
	a       *Archive
	info    *ChatInfo
	mu      sync.Mutex
	rows    []Row
	entries []string
	byID    map[string]int
	dirty   bool // changed during a batch, not written yet
}

func (c *Chat) JID() string  { return c.info.JID }
func (c *Chat) Dir() string  { return filepath.Join(c.a.opts.Root, c.info.Folder) }
func (c *Chat) Name() string { return c.info.Name }

func (c *Chat) chatPath() string  { return filepath.Join(c.Dir(), ChatFile) }
func (c *Chat) indexPath() string { return c.a.metaPath(indexDir, indexFileName(c.info.JID)) }

func indexFileName(jid string) string {
	return strings.NewReplacer("/", "_", ":", "_").Replace(jid) + ".tsv"
}

func (c *Chat) load() error {
	data, err := os.ReadFile(c.chatPath())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	c.entries = SplitEntries(string(data))
	c.rows, err = readIndex(c.indexPath())
	if err != nil {
		return err
	}
	// Appends write the index row first; a crash before the entry leaves one
	// extra row, which is safe to drop.
	if len(c.rows) == len(c.entries)+1 {
		c.rows = c.rows[:len(c.entries)]
		if err := c.writeIndex(); err != nil {
			return err
		}
	}
	if len(c.rows) != len(c.entries) {
		// A crash between writing _chat.txt and its index, or a hand edit.
		// Never refuse the chat: that would drop every new message for it.
		before := len(c.rows)
		c.rows = realign(c.entries, c.rows, c.a.opts.DateFormat, c.a.opts.Location)
		if err := c.writeIndex(); err != nil {
			return err
		}
		c.a.repairs = append(c.a.repairs, c.info.Folder)
		c.a.opts.Logf("repaired index of %s (%d entries, had %d rows)", c.info.Folder, len(c.entries), before)
	}
	c.reindex()
	return nil
}

// realign pairs each entry with its index row. Both are in time order, so a
// row matches the next entry with the same timestamp. Only the seconds within
// a 15-minute block are compared: entries are rendered in whatever zone the
// computer had when they were written, and zone offsets are multiples of 15
// minutes. Entries without a row get one that WhatsApp won't recognize; they
// can no longer be edited or deleted, but the chat keeps working.
func realign(entries []string, rows []Row, dateFormat string, loc *time.Location) []Row {
	const block = 15 * 60
	out := make([]Row, len(entries))
	next := 0
	var lastTS int64
	for i, raw := range entries {
		ts := lastTS
		if e, err := Parse(raw, dateFormat, loc); err == nil {
			ts = e.Time.Unix()
		}
		lastTS = ts
		match := -1
		for k := next; k < len(rows) && k < next+64; k++ {
			if mod(rows[k].TS, block) == mod(ts, block) {
				match = k
				break
			}
		}
		if match < 0 {
			out[i] = Row{ID: fmt.Sprintf("repair:%d:%d", ts, i), TS: ts}
			continue
		}
		out[i] = rows[match]
		next = match + 1
	}
	return out
}

func mod(a, b int64) int64 { return ((a % b) + b) % b }

func (c *Chat) reindex() {
	c.byID = make(map[string]int, len(c.rows))
	for i, r := range c.rows {
		c.byID[r.ID] = i
	}
}

func (c *Chat) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.rows)
}

func (c *Chat) Has(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.byID[id]
	return ok
}

// Rows returns a copy of the bookkeeping rows in file order.
func (c *Chat) Rows() []Row {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Row(nil), c.rows...)
}

// Get returns the parsed entry for a message ID and its position.
func (c *Chat) Get(id string) (Entry, int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	i, ok := c.byID[id]
	if !ok {
		return Entry{}, -1, false
	}
	e, err := Parse(c.entries[i], c.a.opts.DateFormat, c.a.opts.Location)
	if err != nil {
		return Entry{}, -1, false
	}
	return e, i, true
}

// Insert adds a message in timestamp order. build receives the entry's
// position (attachment names depend on it). Inserting a known ID is a no-op
// that reports inserted=false.
func (c *Chat) Insert(row Row, build func(pos int) Entry) (pos int, inserted bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if i, ok := c.byID[row.ID]; ok {
		return i, false, nil
	}
	pos = sort.Search(len(c.rows), func(i int) bool { return c.rows[i].TS > row.TS })
	e := build(pos)
	e.Time = time.Unix(row.TS, 0).In(c.a.opts.Location)
	text := Render(e, c.a.opts.DateFormat)
	if pos == len(c.rows) && !c.a.batching() {
		return pos, true, c.appendLocked(row, text)
	}
	c.rows = append(c.rows[:pos], append([]Row{row}, c.rows[pos:]...)...)
	c.entries = append(c.entries[:pos], append([]string{text}, c.entries[pos:]...)...)
	c.reindex()
	return pos, true, c.saveLocked()
}

func (c *Chat) appendLocked(row Row, text string) error {
	if err := appendFile(c.indexPath(), formatRow(row)); err != nil {
		return err
	}
	if err := appendFile(c.chatPath(), text+EntrySep); err != nil {
		return err
	}
	c.rows = append(c.rows, row)
	c.entries = append(c.entries, text)
	c.byID[row.ID] = len(c.rows) - 1
	return nil
}

// Update rewrites the entry for id. fn returns false to leave it unchanged.
func (c *Chat) Update(id string, fn func(e *Entry, pos int) bool) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	i, ok := c.byID[id]
	if !ok {
		return false, nil
	}
	e, err := Parse(c.entries[i], c.a.opts.DateFormat, c.a.opts.Location)
	if err != nil {
		return false, err
	}
	if !fn(&e, i) {
		return false, nil
	}
	c.entries[i] = Render(e, c.a.opts.DateFormat)
	return true, c.saveLocked()
}

// saveLocked writes the chat now, or at the end of the current batch.
func (c *Chat) saveLocked() error {
	if c.a.batching() {
		c.dirty = true
		return nil
	}
	return c.rewriteLocked()
}

func (c *Chat) rewriteLocked() error {
	var b strings.Builder
	for _, e := range c.entries {
		b.WriteString(e)
		b.WriteString(EntrySep)
	}
	if err := writeFileAtomic(c.chatPath(), []byte(b.String())); err != nil {
		return err
	}
	return c.writeIndex()
}

func (c *Chat) writeIndex() error {
	var b strings.Builder
	for _, r := range c.rows {
		b.WriteString(formatRow(r))
	}
	return writeFileAtomic(c.indexPath(), []byte(b.String()))
}

func formatRow(r Row) string {
	from := "0"
	if r.FromMe {
		from = "1"
	}
	return strings.Join([]string{r.ID, strconv.FormatInt(r.TS, 10), from, r.Sender}, "\t") + "\n"
}

func readIndex(path string) ([]Row, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var rows []Row
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		parts := strings.Split(sc.Text(), "\t")
		if len(parts) != 4 {
			return nil, fmt.Errorf("%s: malformed row %q", path, sc.Text())
		}
		ts, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		rows = append(rows, Row{ID: parts[0], TS: ts, FromMe: parts[2] == "1", Sender: parts[3]})
	}
	return rows, sc.Err()
}

func appendFile(path, s string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(s); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (a *Archive) Location() *time.Location { return a.opts.Location }

// Rename gives a chat a new name and moves its folder to match. System
// entries, which exports attribute to the chat itself, follow the new name.
// The chat stays locked throughout, so no write lands in the old folder.
func (a *Archive) Rename(jid, name, suffix string) (string, error) {
	c, err := a.Chat(jid)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	a.mu.Lock()
	info := a.meta[jid]
	oldName, oldFolder := info.Name, info.Folder
	delete(a.meta, jid) // its own folder doesn't count as taken
	folder := a.freeFolderLocked(name, suffix, jid)
	a.meta[jid] = info
	if strings.EqualFold(folder, oldFolder) {
		folder = oldFolder
	}
	if folder != oldFolder {
		if err := os.Rename(filepath.Join(a.opts.Root, oldFolder), filepath.Join(a.opts.Root, folder)); err != nil {
			a.mu.Unlock()
			return "", err
		}
	}
	info.Name, info.Folder = name, folder
	err = a.saveMetaLocked()
	a.mu.Unlock()
	if err != nil {
		return "", err
	}

	changed := false
	for i, raw := range c.entries {
		e, err := Parse(raw, a.opts.DateFormat, a.opts.Location)
		if err != nil || !e.System || CleanName(e.Sender) != CleanName(oldName) {
			continue
		}
		e.Sender = name
		c.entries[i] = Render(e, a.opts.DateFormat)
		changed = true
	}
	if changed {
		return folder, c.saveLocked()
	}
	return folder, nil
}

// RenameSenders rewrites the sender name of entries for which name returns a
// new one; it gets each entry's bookkeeping row and current sender name.
func (c *Chat) RenameSenders(name func(row Row, sender string) (string, bool)) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for i, raw := range c.entries {
		e, err := Parse(raw, c.a.opts.DateFormat, c.a.opts.Location)
		if err != nil || e.System {
			continue
		}
		if newName, ok := name(c.rows[i], e.Sender); ok && newName != e.Sender {
			e.Sender = newName
			c.entries[i] = Render(e, c.a.opts.DateFormat)
			n++
		}
	}
	if n == 0 {
		return 0, nil
	}
	return n, c.saveLocked()
}
