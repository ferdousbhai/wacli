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
	"time"
)

const (
	ChatFile    = "_chat.txt"
	metaDir     = ".wacli"
	chatsFile   = "chats.json"
	indexDir    = "index"
	folderStart = "WhatsApp Chat - "
)

// ErrReadOnly is returned by writes on an archive opened ReadOnly.
var ErrReadOnly = errors.New("archive opened read-only")

type Options struct {
	Root       string
	DateFormat string
	Location   *time.Location
	Logf       func(format string, args ...any)
	// ReadOnly never writes: repairs happen in memory only. For commands
	// that run next to the sync process that owns the files.
	ReadOnly bool
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
	Kind   string `json:"kind"` // dm | group
	// Backfill is where the next history request starts: the last message of
	// the previous page, in the phone's order.
	Backfill     *Row `json:"backfill,omitempty"`
	BackfillDone bool `json:"backfill_done,omitempty"`
	// NeedsNames marks chats with a name or senders written as a number
	// because the real name wasn't known yet.
	NeedsNames bool `json:"needs_names,omitempty"`
	// PrevFolder is set while a rename is in flight; Open finishes it.
	PrevFolder string `json:"prev_folder,omitempty"`
}

type Archive struct {
	opts     Options
	mu       sync.Mutex
	meta     map[string]*ChatInfo
	chats    map[string]*Chat
	problems []string // found while opening chats; see Problems
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
	if !opts.ReadOnly {
		if err := os.MkdirAll(filepath.Join(opts.Root, metaDir, indexDir), 0o700); err != nil {
			return nil, err
		}
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
	return a, a.finishRenames()
}

// finishRenames completes folder renames a crash interrupted.
func (a *Archive) finishRenames() error {
	changed := false
	for _, info := range a.meta {
		if info.PrevFolder == "" {
			continue
		}
		if !exists(a.path(info.Folder)) && exists(a.path(info.PrevFolder)) {
			if a.opts.ReadOnly {
				info.Folder = info.PrevFolder
				continue
			}
			if err := os.Rename(a.path(info.PrevFolder), a.path(info.Folder)); err != nil {
				return err
			}
		}
		info.PrevFolder = ""
		changed = true
	}
	if changed && !a.opts.ReadOnly {
		return a.saveMetaLocked()
	}
	return nil
}

func (a *Archive) Root() string              { return a.opts.Root }
func (a *Archive) DateFormat() string        { return a.opts.DateFormat }
func (a *Archive) Location() *time.Location  { return a.opts.Location }
func (a *Archive) path(folder string) string { return filepath.Join(a.opts.Root, folder) }
func (a *Archive) metaPath(p ...string) string {
	return filepath.Join(append([]string{a.opts.Root, metaDir}, p...)...)
}

// Problems lists what opening chats found and fixed or worked around.
func (a *Archive) Problems() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.problems...)
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

// UpdateInfo changes a chat's record (backfill cursor, flags) and saves it.
func (a *Archive) UpdateInfo(jid string, fn func(*ChatInfo)) error {
	if a.opts.ReadOnly {
		return ErrReadOnly
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	info, ok := a.meta[jid]
	if !ok {
		return fmt.Errorf("unknown chat %s", jid)
	}
	before := *info
	fn(info)
	if *info == before {
		return nil
	}
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
	if a.opts.ReadOnly {
		return nil, false, ErrReadOnly
	}
	folder := a.freeFolderLocked(name, suffix, jid, "")
	if err := os.MkdirAll(a.path(folder), 0o700); err != nil {
		return nil, false, err
	}
	a.meta[jid] = &ChatInfo{JID: jid, Folder: folder, Name: name, Kind: kind}
	if err := a.saveMetaLocked(); err != nil {
		return nil, false, err
	}
	c, err := a.chatLocked(jid)
	return c, true, err
}

// freeFolderLocked picks a folder name for name that no other chat or file
// uses; own is the chat's current folder, which doesn't count as taken.
func (a *Archive) freeFolderLocked(name, suffix, jid, own string) string {
	base := folderStart + strings.TrimSpace(sanitizeFileName(name))
	if strings.TrimSpace(name) == "" {
		base = folderStart + strings.SplitN(jid, "@", 2)[0]
	}
	taken := func(f string) bool {
		if strings.EqualFold(f, own) {
			return false
		}
		for _, info := range a.meta {
			if strings.EqualFold(info.Folder, f) {
				return true
			}
		}
		return exists(a.path(f))
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

// Rename gives a chat a new name and moves its folder to match. System
// entries, which exports attribute to the chat itself, follow the new name.
// The chat stays locked throughout, so no write lands in the old folder.
func (a *Archive) Rename(jid, name, suffix string) (string, error) {
	if a.opts.ReadOnly {
		return "", ErrReadOnly
	}
	c, err := a.Chat(jid)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	a.mu.Lock()
	info := a.meta[jid]
	oldName, oldFolder := info.Name, info.Folder
	folder := a.freeFolderLocked(name, suffix, jid, oldFolder)
	if folder != oldFolder {
		// Record the move first: if it's interrupted, Open finishes it.
		info.Folder, info.PrevFolder = folder, oldFolder
		if err := a.saveMetaLocked(); err != nil {
			info.Folder, info.PrevFolder = oldFolder, ""
			a.mu.Unlock()
			return "", err
		}
		err := os.Rename(a.path(oldFolder), a.path(folder))
		if errors.Is(err, os.ErrNotExist) {
			err = os.MkdirAll(a.path(folder), 0o700)
		}
		if err != nil {
			info.Folder, info.PrevFolder = oldFolder, ""
			_ = a.saveMetaLocked()
			a.mu.Unlock()
			return "", err
		}
		info.PrevFolder = ""
	}
	info.Name = name
	err = a.saveMetaLocked()
	a.mu.Unlock()
	if err != nil {
		return "", err
	}

	changed := false
	for i, raw := range c.entries {
		for _, old := range []string{oldName, CleanName(oldName)} {
			if renamed, ok := renameSystemSender(raw, old, name); ok {
				c.entries[i], changed = renamed, true
				break
			}
		}
	}
	if changed {
		return folder, c.saveLocked()
	}
	return folder, nil
}

// Chat is one export folder. Methods are safe for concurrent use; the
// in-memory entries are authoritative and the files follow them.
type Chat struct {
	a       *Archive
	info    *ChatInfo // Folder and Name change only under mu (and a.mu)
	mu      sync.Mutex
	rows    []Row
	entries []string
	byID    map[string]int
	hold    int  // >0 while a batch of writes is being collected
	dirty   bool // memory has changes the files don't
}

func (c *Chat) JID() string { return c.info.JID }

func (c *Chat) Dir() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dirLocked()
}

func (c *Chat) Name() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.info.Name
}

func (c *Chat) dirLocked() string { return c.a.path(c.info.Folder) }
func (c *Chat) chatPath() string  { return filepath.Join(c.dirLocked(), ChatFile) }
func (c *Chat) indexPath() string { return c.a.metaPath(indexDir, indexFileName(c.info.JID)) }
func indexFileName(jid string) string {
	return strings.NewReplacer("/", "_", ":", "_").Replace(jid) + ".tsv"
}

func (c *Chat) problem(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	c.a.problems = append(c.a.problems, msg) // load runs under a.mu
	c.a.opts.Logf("%s", msg)
}

func (c *Chat) load() error {
	data, err := os.ReadFile(c.chatPath())
	missing := errors.Is(err, os.ErrNotExist)
	if err != nil && !missing {
		return err
	}
	c.entries = SplitEntries(string(data))
	if c.rows, err = readIndex(c.indexPath()); err != nil {
		return err
	}
	if missing && len(c.rows) > 0 {
		// The folder was moved or deleted outside wacli. Keep the old index
		// aside and start the chat over, so new messages still land.
		c.problem("%s: _chat.txt was missing; started it again (old index kept as %s.orphaned)", c.info.Folder, filepath.Base(c.indexPath()))
		if !c.a.opts.ReadOnly {
			if err := os.Rename(c.indexPath(), fmt.Sprintf("%s.orphaned-%d", c.indexPath(), time.Now().Unix())); err != nil {
				return err
			}
			if err := os.MkdirAll(c.dirLocked(), 0o700); err != nil {
				return err
			}
		}
		c.rows = nil
	}
	if len(c.rows) != len(c.entries) {
		// A crash between writing _chat.txt and its index, a failed write,
		// or a hand edit. Never refuse the chat: new messages must land.
		before := len(c.rows)
		c.rows = realign(c.entries, c.rows, c.a.opts.DateFormat, c.a.opts.Location)
		c.problem("%s: index rebuilt (%d entries, had %d rows)", c.info.Folder, len(c.entries), before)
		if !c.a.opts.ReadOnly {
			if err := c.writeIndex(); err != nil {
				return err
			}
		}
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

// Hold collects writes in memory until the matching Release, so inserting
// thousands of older messages writes the chat once. Holds nest.
func (c *Chat) Hold() {
	c.mu.Lock()
	c.hold++
	c.mu.Unlock()
}

// Release ends a Hold and writes what it collected.
func (c *Chat) Release() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hold--
	if c.hold > 0 || !c.dirty {
		return nil
	}
	return c.flushLocked()
}

// Flush writes pending changes now, even during a Hold. Writers that need
// their change on disk before acknowledging it (live messages, finished
// downloads) call it.
func (c *Chat) Flush() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.dirty {
		return nil
	}
	return c.flushLocked()
}

// Insert adds a message in timestamp order. build receives the entry's
// position (attachment names depend on it) and runs with the chat locked.
// Inserting a known ID is a no-op that reports inserted=false.
func (c *Chat) Insert(row Row, build func(pos int) Entry) (pos int, inserted bool, err error) {
	if c.a.opts.ReadOnly {
		return 0, false, ErrReadOnly
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if i, ok := c.byID[row.ID]; ok {
		return i, false, nil
	}
	pos = sort.Search(len(c.rows), func(i int) bool { return c.rows[i].TS > row.TS })
	e := build(pos)
	e.Time = time.Unix(row.TS, 0).In(c.a.opts.Location)
	text := Render(e, c.a.opts.DateFormat)
	if pos == len(c.rows) && c.hold == 0 && !c.dirty {
		return pos, true, c.appendLocked(row, text)
	}
	c.rows = append(c.rows[:pos], append([]Row{row}, c.rows[pos:]...)...)
	c.entries = append(c.entries[:pos], append([]string{text}, c.entries[pos:]...)...)
	c.reindex()
	return pos, true, c.saveLocked()
}

// appendLocked is the fast path for the newest message: two appends. If
// either fails, the files are rewritten from memory instead, so a partial
// append can't leave the index and _chat.txt out of step.
func (c *Chat) appendLocked(row Row, text string) error {
	err := appendFile(c.indexPath(), formatRow(row))
	if err == nil {
		err = appendFile(c.chatPath(), text+EntrySep)
	}
	c.rows = append(c.rows, row)
	c.entries = append(c.entries, text)
	c.byID[row.ID] = len(c.rows) - 1
	if err != nil {
		c.dirty = true
		return c.flushLocked()
	}
	return nil
}

// Update rewrites the entry for id. fn returns false to leave it unchanged.
func (c *Chat) Update(id string, fn func(e *Entry, pos int) bool) (bool, error) {
	if c.a.opts.ReadOnly {
		return false, ErrReadOnly
	}
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

// RenameSenders rewrites the sender name of entries for which name returns a
// new one; it gets each entry's bookkeeping row and current sender name.
func (c *Chat) RenameSenders(name func(row Row, sender string) (string, bool)) (int, error) {
	if c.a.opts.ReadOnly {
		return 0, ErrReadOnly
	}
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

// saveLocked writes the chat now, or at Release during a Hold.
func (c *Chat) saveLocked() error {
	c.dirty = true
	if c.hold > 0 {
		return nil
	}
	return c.flushLocked()
}

// flushLocked rewrites both files from memory. On failure the chat stays
// dirty, so the next write tries again with everything.
func (c *Chat) flushLocked() error {
	if err := os.MkdirAll(c.dirLocked(), 0o700); err != nil {
		return err
	}
	var b strings.Builder
	for _, e := range c.entries {
		b.WriteString(e)
		b.WriteString(EntrySep)
	}
	if err := writeFileAtomic(c.chatPath(), []byte(b.String())); err != nil {
		return err
	}
	if err := c.writeIndex(); err != nil {
		return err
	}
	c.dirty = false
	return nil
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

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
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
