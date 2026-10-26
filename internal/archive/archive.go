package archive

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ferdousbhai/wacli/internal/fsutil"
)

const (
	ChatFile    = "_chat.txt"
	metaDir     = ".wacli"
	chatsFile   = "chats.json"
	formatFile  = "date-format" // the layout the archive's lines are written in
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
	// DeletedMedia lists attachments of messages deleted for everyone that
	// still have to leave the chat folder (kept here so a crash can't
	// strand them there).
	DeletedMedia []string `json:"deleted_media,omitempty"`
}

type Archive struct {
	opts     Options
	mu       sync.Mutex
	meta     map[string]*ChatInfo
	chats    map[string]*Chat
	probMu   sync.Mutex
	problems []string // found while loading chats; see Problems

	senderName func(chat string, r Row) string // see SetSenderNames
}

// SetSenderNames gives the archive the display name a row's sender has now,
// so changing an entry (an edit, a delete) keeps a name holding ": " whole.
// Set it before the chats are used; fn runs with a chat locked.
func (a *Archive) SetSenderNames(fn func(chat string, r Row) string) { a.senderName = fn }

func Open(opts Options) (*Archive, error) {
	if strings.TrimSpace(opts.Root) == "" {
		return nil, errors.New("archive root is required")
	}
	chosen := opts.DateFormat // "" lets the archive's recorded format decide
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
		// Temp files left by a crash are removed; nothing else writes here
		// while this process holds the session lock.
		if err := os.RemoveAll(filepath.Join(opts.Root, metaDir, "tmp")); err != nil {
			return nil, err
		}
		for _, d := range []string{indexDir, "tmp"} {
			if err := os.MkdirAll(filepath.Join(opts.Root, metaDir, d), 0o700); err != nil {
				return nil, err
			}
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
	if err := a.settleDateFormat(chosen); err != nil {
		return nil, err
	}
	a.shortenFolders()
	return a, a.finishRenames()
}

// settleDateFormat keeps the archive in one date format: lines in another
// couldn't be read back for edits and deletes. The format is recorded once
// lines exist in it, and from then on it's the one used, whatever chosen
// says (a service may lack the setting a shell had). Before that, chosen (or
// the default) is used and recorded. Nothing here refuses to open: a
// mismatch is noted, or reported as a problem.
func (a *Archive) settleDateFormat(chosen string) error {
	data, err := os.ReadFile(a.metaPath(formatFile))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	stored := strings.TrimSpace(string(data))
	if stored != "" && (chosen == "" || chosen == stored) {
		a.opts.DateFormat = stored
		return nil
	}
	first, hasLines := a.firstEntry()
	if stored != "" && hasLines {
		a.opts.DateFormat = stored
		a.opts.Logf("note: using this archive's --date-format %q, not %q: its lines are written in it", stored, chosen)
		return nil
	}
	// Nothing recorded yet, or no lines to keep consistent: chosen decides.
	if chosen != "" {
		a.opts.DateFormat = chosen
	}
	if hasLines { // written before formats were recorded
		if _, err := Parse(first, a.opts.DateFormat, a.opts.Location); err != nil {
			a.probMu.Lock()
			a.problems = append(a.problems, fmt.Sprintf("existing lines don't read with --date-format %q (%v): pass the format they use", a.opts.DateFormat, err))
			a.probMu.Unlock()
			return nil
		}
	}
	if a.opts.ReadOnly || stored == a.opts.DateFormat {
		return nil
	}
	return a.writeFile(a.metaPath(formatFile), func(w *bufio.Writer) error {
		_, err := w.WriteString(a.opts.DateFormat + "\n")
		return err
	})
}

// firstEntry is the first chat line in the archive (by folder name), if any.
func (a *Archive) firstEntry() (string, bool) {
	folders := make([]string, 0, len(a.meta))
	for _, info := range a.meta {
		folders = append(folders, info.Folder)
	}
	sort.Strings(folders)
	for _, folder := range folders {
		data, err := os.ReadFile(filepath.Join(a.path(folder), ChatFile))
		if err != nil {
			continue
		}
		for _, e := range SplitEntries(string(data)) {
			if headerRE.MatchString(e) {
				return e, true
			}
		}
	}
	return "", false
}

// shortenFolders gives a new folder to a chat whose name was too long for
// one (from versions that didn't shorten names); the folder never existed.
func (a *Archive) shortenFolders() {
	changed := false
	for jid, info := range a.meta {
		if len(info.Folder) > maxFolderBytes && info.PrevFolder == "" {
			info.Folder, changed = a.freeFolderLocked(info.Name, "", jid, ""), true
		}
	}
	if changed && !a.opts.ReadOnly {
		_ = a.saveMetaLocked() // else in memory; tried again next Open
	}
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
			fsutil.Sync(a.opts.Root)
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
	a.probMu.Lock()
	defer a.probMu.Unlock()
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
	before.DeletedMedia = append([]string(nil), info.DeletedMedia...)
	fn(info)
	if reflect.DeepEqual(*info, before) {
		return nil
	}
	return a.saveMetaLocked()
}

func (a *Archive) saveMetaLocked() error {
	data, err := json.MarshalIndent(a.meta, "", "  ")
	if err != nil {
		return err
	}
	return a.writeFile(a.metaPath(chatsFile), func(w *bufio.Writer) error {
		_, err := w.Write(append(data, '\n'))
		return err
	})
}

// Chat returns an existing chat.
func (a *Archive) Chat(jid string) (*Chat, error) {
	a.mu.Lock()
	c, err := a.chatLocked(jid)
	a.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return c, c.ensureLoaded()
}

// chatLocked returns the chat's single Chat value, creating it unloaded. It
// is loaded under its own lock after a.mu is released (ensureLoaded), so the
// lock order stays chat before archive everywhere.
func (a *Archive) chatLocked(jid string) (*Chat, error) {
	if c, ok := a.chats[jid]; ok {
		return c, nil
	}
	info, ok := a.meta[jid]
	if !ok {
		return nil, fmt.Errorf("unknown chat %s", jid)
	}
	c := &Chat{a: a, info: info}
	a.chats[jid] = c
	return c, nil
}

// Trim unloads chats untouched for idle that have nothing unwritten; they
// reload from disk on next use. The Chat values stay, so nothing holding
// one ever sees a stale copy.
func (a *Archive) Trim(idle time.Duration) {
	a.mu.Lock()
	chats := make([]*Chat, 0, len(a.chats))
	for _, c := range a.chats {
		chats = append(chats, c)
	}
	a.mu.Unlock()
	for _, c := range chats {
		c.mu.Lock()
		if c.loaded && !c.dirty && c.hold == 0 && len(c.pending) == 0 && time.Since(c.lastUsed) > idle {
			c.rows, c.entries, c.byID, c.loaded = nil, nil, nil, false
		}
		c.mu.Unlock()
	}
}

func (c *Chat) ensureLoaded() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.useLocked()
}

// EnsureChat returns the chat, creating its folder on first use. suffix
// disambiguates the folder when another chat already uses the name.
func (a *Archive) EnsureChat(jid, name, kind, suffix string) (*Chat, bool, error) {
	a.mu.Lock()
	_, existed := a.meta[jid]
	if !existed {
		if a.opts.ReadOnly {
			a.mu.Unlock()
			return nil, false, ErrReadOnly
		}
		// Record first, then create the folder (as Rename does): a crash in
		// between leaves a record whose folder is made on first write, not
		// an unclaimed folder that pushes the chat to another name.
		folder := a.freeFolderLocked(name, suffix, jid, "")
		a.meta[jid] = &ChatInfo{JID: jid, Folder: folder, Name: name, Kind: kind}
		if err := a.saveMetaLocked(); err != nil {
			delete(a.meta, jid)
			a.mu.Unlock()
			return nil, false, err
		}
		if err := os.MkdirAll(a.path(folder), 0o700); err != nil {
			a.mu.Unlock()
			return nil, false, err
		}
	}
	c, err := a.chatLocked(jid)
	a.mu.Unlock()
	if err != nil {
		return nil, false, err
	}
	return c, !existed, c.ensureLoaded()
}

// freeFolderLocked picks a folder name for name that no other chat or file
// uses; own is the chat's current folder, which doesn't count as taken.
func (a *Archive) freeFolderLocked(name, suffix, jid, own string) string {
	// Short enough for a file system's 255-byte names, with a suffix.
	base := folderStart + strings.TrimSpace(truncateBytes(sanitizeFileName(name), folderNameBytes))
	if base == folderStart { // nothing usable in a file name: ".", marks only
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
		if err == nil {
			fsutil.Sync(a.opts.Root) // before anything records the new name as done
			// Deleted media follows (best effort: a leftover only splits it).
			_ = os.Rename(a.metaPath("deleted", oldFolder), a.metaPath("deleted", folder))
		}
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
	a.mu.Unlock()

	// The system lines are written before the new name is recorded, even
	// during a Hold: once chats.json has it, no later pass renames them.
	if err := c.useLocked(); err != nil {
		return "", err
	}
	c.mergeLocked()
	changed := false
	for i, raw := range c.entries {
		for _, old := range []string{oldName, CleanName(oldName)} {
			if renamed, ok := renameSystemSender(raw, old, name); ok {
				c.entries[i], changed = renamed, true
				break
			}
		}
	}
	if changed || c.dirty {
		c.dirty = true
		if err := c.flushLocked(); err != nil {
			return "", err
		}
	}
	a.mu.Lock()
	info.Name = name
	err = a.saveMetaLocked()
	a.mu.Unlock()
	return folder, err
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
	hold    int // >0 while a batch of writes is being collected
	// heldOlder: history entries merged while held (a Flush mid-batch);
	// the batch's later entries of the same second go after them.
	heldOlder map[string]bool
	dirty     bool // memory has changes the files don't
	// pending holds entries inserted during a Hold until they are merged.
	pending     []pendingEntry
	pendingByID map[string]int
	pendingTS   []int64   // pending timestamps, oldest first
	missing     bool      // read found no _chat.txt
	torn        bool      // read found a last line cut short
	loaded      bool      // rows and entries are in memory (see Trim)
	lastUsed    time.Time // for Trim
}

// useLocked loads the chat if Trim unloaded it, and notes the use.
func (c *Chat) useLocked() error {
	c.lastUsed = time.Now()
	if c.loaded {
		return nil
	}
	if err := c.load(); err != nil {
		return err
	}
	c.loaded = true
	return nil
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
	c.a.probMu.Lock()
	c.a.problems = append(c.a.problems, msg)
	c.a.probMu.Unlock()
	c.a.opts.Logf("%s", msg)
}

func (c *Chat) load() error {
	err := c.read()
	if err == nil && c.a.opts.ReadOnly && len(c.rows) != len(c.entries) {
		// A reader can catch sync between its two writes; look again
		// before calling it a problem.
		time.Sleep(100 * time.Millisecond)
		err = c.read()
	}
	if err != nil {
		return err
	}
	return c.reconcile()
}

// read loads _chat.txt and the index as they are on disk.
func (c *Chat) read() error {
	data, err := os.ReadFile(c.chatPath())
	missing := errors.Is(err, os.ErrNotExist)
	if err != nil && !missing {
		return err
	}
	c.missing = missing
	// A last line cut short (power loss, full disk mid-append) would get
	// the next append glued onto it; reconcile rewrites such files whole.
	c.torn = len(data) > 0 && !bytes.HasSuffix(data, []byte(EntrySep))
	if c.rows, err = readIndex(c.indexPath()); err != nil {
		return err
	}
	c.entries = SplitEntries(string(data))
	sep := []byte(EntrySep)
	// Converted to Unix line endings: no append to tell apart.
	unix := !bytes.Contains(data, sep) && bytes.HasSuffix(data, []byte("\n"))
	if c.torn && !unix {
		// Part of an append (one write of a whole line, after its index
		// row). Cut before its final "\n" the line is whole. A fragment
		// without its header would be glued onto the message before it:
		// drop it. A line with its header is kept, but if it has a row its
		// ID is not trusted: if it was cut short, the redelivered message
		// lands whole beside it.
		cut := 0 // where the last line starts
		if i := bytes.LastIndex(data, sep); i >= 0 {
			cut = i + len(sep)
		}
		switch tail := bytes.TrimPrefix(data[cut:], []byte("\ufeff")); {
		case bytes.HasSuffix(data, []byte("\r")):
			data = append(data, '\n')
			c.entries = SplitEntries(string(data))
		case !headerRE.Match(tail):
			data = data[:cut]
			c.entries = SplitEntries(string(data))
		case len(c.rows) >= len(c.entries):
			c.rows = c.rows[:len(c.rows)-1]
		}
	}
	if unix && len(c.rows) > 0 && len(c.entries) > len(c.rows) {
		// Every line that looks like a header started an entry, also one
		// quoted inside a message. If every row pairs and only the extra
		// lines don't, those lines belong to the message before them.
		if match := c.match(c.entries); paired(match) == len(c.rows) {
			var entries []string
			for i, e := range c.entries {
				if i > 0 && match[i] < 0 {
					entries[len(entries)-1] += "\n" + e
					continue
				}
				entries = append(entries, e)
			}
			c.entries = entries
		}
	}
	c.torn = c.torn || indexTorn(c.indexPath())
	if len(c.rows) > len(c.entries) {
		// Line endings mixed by an edit can merge entries; splitting at
		// every header line recovers them. But extra rows also come from a
		// crash between the two appends, and then a header-like line pasted
		// inside a message must stay put: the line split is used only if
		// strictly more rows pair up with it.
		if alt := SplitLines(string(data)); len(alt) > len(c.entries) && len(alt) <= len(c.rows) {
			if paired(c.match(alt)) > paired(c.match(c.entries)) {
				c.entries, c.rows, c.torn = alt, c.realign(alt), true // torn: both files rewritten
			}
		}
	}
	return nil
}

// paired counts the entries matchRows paired with a row.
func paired(match []int) int {
	n := 0
	for _, k := range match {
		if k >= 0 {
			n++
		}
	}
	return n
}

func (c *Chat) match(entries []string) []int {
	return matchRows(entries, c.rows, c.a.opts.DateFormat, c.a.opts.Location)
}

func (c *Chat) realign(entries []string) []Row {
	return realign(entries, c.rows, c.a.opts.DateFormat, c.a.opts.Location)
}

// realign gives each entry its row (see matchRows). Entries without one get
// a row WhatsApp won't recognize: they can no longer be edited or deleted,
// but the chat keeps working.
func realign(entries []string, rows []Row, dateFormat string, loc *time.Location) []Row {
	out := make([]Row, len(entries))
	var lastTS int64
	for i, k := range matchRows(entries, rows, dateFormat, loc) {
		if e, err := Parse(entries[i], dateFormat, loc); err == nil {
			lastTS = e.Time.Unix()
		}
		out[i] = Row{ID: fmt.Sprintf("repair:%d:%d", lastTS, i), TS: lastTS}
		if k >= 0 {
			out[i] = rows[k]
		}
	}
	return out
}

// indexTorn reports whether the index file's last row lacks its newline.
func indexTorn(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() == 0 {
		return false
	}
	last := make([]byte, 1)
	_, err = f.ReadAt(last, info.Size()-1)
	return err == nil && last[0] != '\n'
}

// reconcile repairs what read found: a missing _chat.txt, an index out of
// step, deleted media a crash left in the folder.
func (c *Chat) reconcile() error {
	if c.missing && len(c.rows) > 0 {
		// The folder was moved or deleted outside wacli. Keep the old index
		// aside and start the chat over, so new messages still land.
		c.problem("%s: _chat.txt was missing; started it again (old index kept as %s.orphaned-<time>)", c.info.Folder, filepath.Base(c.indexPath()))
		if !c.a.opts.ReadOnly {
			if err := os.Rename(c.indexPath(), fmt.Sprintf("%s.orphaned-%d", c.indexPath(), time.Now().Unix())); err != nil {
				return err
			}
			if err := os.MkdirAll(c.dirLocked(), 0o700); err != nil {
				return err
			}
			// Its backfill starts over too: the history it reached is gone.
			c.a.mu.Lock()
			c.info.Backfill, c.info.BackfillDone = nil, false
			err := c.a.saveMetaLocked()
			c.a.mu.Unlock()
			if err != nil {
				return err
			}
		}
		c.rows = nil
	}
	if len(c.rows) != len(c.entries) {
		// A crash between writing _chat.txt and its index, a failed write,
		// or a hand edit. Never refuse the chat: new messages must land.
		before := len(c.rows)
		c.rows = c.realign(c.entries)
		c.problem("%s: index rebuilt (%d entries, had %d rows)", c.info.Folder, len(c.entries), before)
		if !c.a.opts.ReadOnly {
			if err := c.writeIndex(); err != nil {
				return err
			}
		}
	}
	c.reindex()
	if c.torn {
		c.problem("%s: last line cut short (crash, full disk) or line endings changed; files rewritten", c.info.Folder)
		c.torn = false
		if !c.a.opts.ReadOnly {
			c.dirty = true
			if err := c.flushLocked(); err != nil {
				return err
			}
		}
	}
	if !c.a.opts.ReadOnly {
		c.moveDeletedLocked()
	}
	return nil
}

// matchRows pairs each entry with its index row and returns the row's
// position for each entry, or -1. Both are in time order, and
// an entry shows the time in whatever zone the computer had when it was
// written, so an entry takes the nearest following row at the same instant
// or at the zone shift the previous pair matched at. When neither exists the
// zone changed (a trip, a daylight-saving night): a row a whole number of
// 15-minute steps away (at most 26 hours) is taken as the new shift if the
// next two entries pair at that shift too. Before any shift is known, the
// first row is also accepted at such a step unconfirmed. Rows without entries (a crash
// between writing the index and _chat.txt) can come in a run as long as a
// history batch, so searches reach past them.
func matchRows(entries []string, rows []Row, dateFormat string, loc *time.Location) []int {
	const block, maxShift = 15 * 60, 26 * 3600
	tss := make([]int64, len(entries))
	alts := make([]int64, len(entries)) // the other reading of a time a DST fall-back repeats
	var lastTS int64
	for i, raw := range entries {
		ts := lastTS
		if e, err := Parse(raw, dateFormat, loc); err == nil {
			ts = e.Time.Unix()
			alts[i] = otherReading(e, dateFormat).Unix()
		} else {
			alts[i] = ts
		}
		tss[i], lastTS = ts, ts
	}
	reach := 64 + max(0, len(rows)-len(entries))
	isStep := func(d int64) bool { return d%block == 0 && d >= -maxShift && d <= maxShift }
	// find returns the first row from `from` (within limit) at shift d.
	find := func(from, limit, i int, d int64) int {
		for k := from; k < min(len(rows), from+limit); k++ {
			if rows[k].TS-tss[i] == d || rows[k].TS-alts[i] == d {
				return k
			}
		}
		return -1
	}
	out := make([]int, len(entries))
	next := 0
	offset, known := int64(0), false
	// confirmed reports whether entry i can start a new shift d at row k:
	// the next entry must not still pair at the current shift (then the
	// zone didn't change and entry i is the odd one out, e.g. a hand-added
	// line), and the two entries after i must pair at d after row k (just
	// the next one at the end of the chat).
	confirmed := func(k, i int, d int64) bool {
		if find(next, reach, i+1, 0) >= 0 || (known && find(next, reach, i+1, offset) >= 0) {
			return false
		}
		k2 := find(k+1, reach, i+1, d)
		if k2 < 0 {
			return false
		}
		return i+2 >= len(tss) || find(k2+1, reach, i+2, d) >= 0
	}
	for i, ts := range tss {
		match := find(next, reach, i, 0)
		if known && offset != 0 {
			if m := find(next, reach, i, offset); m >= 0 && (match < 0 || m < match) {
				match = m
			}
		}
		for k := next; match < 0 && i+1 < len(tss) && k < min(len(rows), next+reach); k++ {
			if d := rows[k].TS - ts; isStep(d) && confirmed(k, i, d) {
				match = k // a new shift, confirmed by the entries after it
			}
		}
		if match < 0 && !known && next < len(rows) && isStep(rows[next].TS-ts) {
			match = next // the whole chat starts shifted; nothing to confirm with
		}
		out[i] = match
		if match < 0 {
			continue
		}
		offset, known = rows[match].TS-ts, true
		if rows[match].TS == alts[i] {
			offset = 0 // paired at the other reading of a repeated hour
		}
		next = match + 1
	}
	return out
}

func (c *Chat) reindex() {
	c.byID = make(map[string]int, len(c.rows))
	for i, r := range c.rows {
		c.byID[r.ID] = i
	}
}

// pendingEntry is a message inserted during a Hold, merged in at the end.
type pendingEntry struct {
	row   Row
	text  string
	older bool // goes before merged entries of the same second
}

// mergeLocked merges the entries inserted during a Hold into place: one sort
// and one pass, instead of shifting the whole chat for each older message.
func (c *Chat) mergeLocked() {
	if len(c.pending) == 0 {
		return
	}
	sort.SliceStable(c.pending, func(i, j int) bool { return c.pending[i].row.TS < c.pending[j].row.TS })
	rows := make([]Row, 0, len(c.rows)+len(c.pending))
	entries := make([]string, 0, cap(rows))
	i := 0
	for _, p := range c.pending {
		for ; i < len(c.rows) && (c.rows[i].TS < p.row.TS || c.rows[i].TS == p.row.TS && (!p.older || c.heldOlder[c.rows[i].ID])); i++ {
			rows, entries = append(rows, c.rows[i]), append(entries, c.entries[i])
		}
		rows, entries = append(rows, p.row), append(entries, p.text)
		if p.older && c.hold > 0 {
			if c.heldOlder == nil {
				c.heldOlder = map[string]bool{}
			}
			c.heldOlder[p.row.ID] = true
		}
	}
	c.rows, c.entries = append(rows, c.rows[i:]...), append(entries, c.entries[i:]...)
	c.pending, c.pendingByID, c.pendingTS = nil, nil, nil
	c.reindex()
}

// lookupLocked finds an entry by message ID, among merged or pending ones.
func (c *Chat) lookupLocked(id string) (text *string, ok bool) {
	if i, ok := c.byID[id]; ok {
		return &c.entries[i], true
	}
	if i, ok := c.pendingByID[id]; ok {
		return &c.pending[i].text, true
	}
	return nil, false
}

func (c *Chat) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.useLocked(); err != nil {
		return 0
	}
	return len(c.rows) + len(c.pending)
}

// Row returns the bookkeeping row of the entry for id.
func (c *Chat) Row(id string) (Row, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.useLocked() != nil {
		return Row{}, false
	}
	if i, ok := c.byID[id]; ok {
		return c.rows[i], true
	}
	if i, ok := c.pendingByID[id]; ok {
		return c.pending[i].row, true
	}
	return Row{}, false
}

func (c *Chat) Has(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.useLocked(); err != nil {
		c.a.opts.Logf("warning: %v", err)
		return false
	}
	_, ok := c.lookupLocked(id)
	return ok
}

// Rows returns a copy of the bookkeeping rows in file order.
func (c *Chat) Rows() []Row {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.useLocked(); err != nil {
		return nil
	}
	c.mergeLocked()
	return append([]Row(nil), c.rows...)
}

// Get returns the parsed entry for a message ID.
func (c *Chat) Get(id string) (Entry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.useLocked(); err != nil {
		return Entry{}, false
	}
	text, ok := c.lookupLocked(id)
	if !ok {
		return Entry{}, false
	}
	e, err := Parse(*text, c.a.opts.DateFormat, c.a.opts.Location)
	return e, err == nil
}

// Hold collects writes in memory until the matching Release, so inserting
// thousands of older messages writes the chat once. Holds nest.
func (c *Chat) Hold() {
	c.mu.Lock()
	c.hold++
	c.mu.Unlock()
}

// HoldOlder is Hold for a history batch. Each batch walks further back than
// the last, so what earlier batches merged while held now counts as already
// archived: its entries of the same second go before them (see InsertOlder),
// even while another Hold, an import's say, keeps the chat held.
func (c *Chat) HoldOlder() {
	c.mu.Lock()
	c.hold++
	c.heldOlder = nil
	c.mu.Unlock()
}

// Release ends a Hold and writes what's unwritten, even if another Hold on
// the chat continues: whoever releases may acknowledge its messages next.
func (c *Chat) Release() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hold--
	if c.hold == 0 {
		defer func() { c.heldOlder = nil }()
	}
	if !c.dirty {
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

// Insert adds a message in timestamp order, after entries of the same
// second. build receives the position the entry is expected at and runs with
// the chat locked. Inserting a known ID is a no-op that reports
// inserted=false.
func (c *Chat) Insert(row Row, build func(pos int) Entry) (pos int, inserted bool, err error) {
	return c.insert(row, build, false)
}

// InsertOlder is Insert for a message from history, which the phone sends
// walking back in time: it goes before entries already merged in the same
// second. Messages of one history chunk must come oldest first; each goes
// after the ones pending before it.
func (c *Chat) InsertOlder(row Row, build func(pos int) Entry) (pos int, inserted bool, err error) {
	return c.insert(row, build, true)
}

func (c *Chat) insert(row Row, build func(pos int) Entry, older bool) (pos int, inserted bool, err error) {
	if c.a.opts.ReadOnly {
		return 0, false, ErrReadOnly
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.useLocked(); err != nil {
		return 0, false, err
	}
	if _, ok := c.lookupLocked(row.ID); ok {
		return 0, false, nil
	}
	pos = sort.Search(len(c.rows), func(i int) bool {
		return c.rows[i].TS > row.TS || older && c.rows[i].TS == row.TS && !c.heldOlder[c.rows[i].ID]
	})
	// Entries collected during a Hold count too: those up to its second.
	// History is archived oldest first, so this is usually an append.
	k := sort.Search(len(c.pendingTS), func(i int) bool { return c.pendingTS[i] > row.TS })
	pos += k
	e := build(pos)
	if e.System && e.Sender == "" {
		e.Sender = c.info.Name // read under the lock a Rename takes
	}
	e.Time, e.stamp = time.Unix(row.TS, 0).In(c.a.opts.Location), ""
	text := Render(e, c.a.opts.DateFormat)
	switch {
	case c.hold > 0:
		if c.pendingByID == nil {
			c.pendingByID = map[string]int{}
		}
		c.pendingByID[row.ID] = len(c.pending)
		c.pending = append(c.pending, pendingEntry{row, text, older})
		c.pendingTS = append(c.pendingTS, 0)
		copy(c.pendingTS[k+1:], c.pendingTS[k:])
		c.pendingTS[k] = row.TS
		c.dirty = true
		return pos, true, nil
	case pos == len(c.rows) && !c.dirty:
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
	if err == nil && len(c.entries) > 0 && !exists(c.chatPath()) {
		// _chat.txt was deleted under us: never start a one-line file under
		// the rows of earlier entries; the rewrite below restores it.
		err = os.ErrNotExist
	}
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
// The entry keeps its original timestamp text.
func (c *Chat) Update(id string, fn func(e *Entry) bool) (bool, error) {
	if c.a.opts.ReadOnly {
		return false, ErrReadOnly
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.useLocked(); err != nil {
		return false, err
	}
	changed, err := c.updateLocked(id, fn)
	if err != nil || !changed {
		return changed, err
	}
	return true, c.saveLocked()
}

func (c *Chat) updateLocked(id string, fn func(e *Entry) bool) (bool, error) {
	text, ok := c.lookupLocked(id)
	if !ok {
		return false, nil
	}
	sender := ""
	if name := c.a.senderName; name != nil {
		if i, ok := c.byID[id]; ok {
			sender = name(c.info.JID, c.rows[i])
		} else if i, ok := c.pendingByID[id]; ok {
			sender = name(c.info.JID, c.pending[i].row)
		}
	}
	e, err := ParseAs(*text, sender, c.a.opts.DateFormat, c.a.opts.Location)
	if err != nil {
		return false, err
	}
	if !fn(&e) {
		return false, nil
	}
	*text = Render(e, c.a.opts.DateFormat)
	return true, nil
}

// PlaceAttachment moves a downloaded file into the chat folder as it is now
// (a rename may have moved it since the download started), under want or a
// free variant of it, and points the entry at it. The change is written by
// the next Flush, so many finished downloads cost one write. If the entry
// was deleted meanwhile the file goes to .wacli/deleted; if it already has a
// file (an import restored it) the download is dropped.
//
// If want already holds the same content, it's a file a crash left after the
// move; that one is kept instead of adding a copy.
func (c *Chat) PlaceAttachment(id, src, want string) (string, error) {
	if c.a.opts.ReadOnly {
		return "", ErrReadOnly
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.useLocked(); err != nil {
		return "", err
	}
	text, ok := c.lookupLocked(id)
	if !ok {
		return "", os.Remove(src)
	}
	e, err := Parse(*text, c.a.opts.DateFormat, c.a.opts.Location)
	if err != nil {
		return "", err
	}
	switch {
	case e.Deleted != "":
		deletedDir := c.deletedDirLocked()
		if err := os.MkdirAll(deletedDir, 0o700); err != nil {
			return "", err
		}
		return "", os.Rename(src, filepath.Join(deletedDir, freeName(deletedDir, filepath.Base(want))))
	case e.Attachment != "":
		return "", os.Remove(src)
	}
	dir := c.dirLocked()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	name := filepath.Base(want)
	if !c.referencedLocked(name) && sameFile(filepath.Join(dir, name), src) {
		if err := os.Remove(src); err != nil {
			return "", err
		}
	} else {
		name = freeName(dir, name)
		fsutil.Sync(src) // its data on disk before the entry says "attached"
		if err := os.Rename(src, filepath.Join(dir, name)); err != nil {
			return "", err
		}
	}
	e.Omitted, e.Attachment = "", name
	*text = Render(e, c.a.opts.DateFormat)
	c.dirty = true
	return name, nil
}

// referencedLocked reports whether any entry has name as its attachment.
func (c *Chat) referencedLocked(name string) bool {
	tag := attachOpen + name + ">"
	// Only a media line owns a file; a text may quote the tag.
	owns := func(raw string) bool {
		if !strings.HasPrefix(raw, lrm) || !strings.Contains(raw, tag) {
			return false
		}
		e, err := Parse(raw, c.a.opts.DateFormat, c.a.opts.Location)
		return err == nil && e.Attachment == name
	}
	for _, e := range c.entries {
		if owns(e) {
			return true
		}
	}
	for _, p := range c.pending {
		if owns(p.text) {
			return true
		}
	}
	return false
}

// sameFile reports whether files a and b both exist with equal content.
func sameFile(a, b string) bool {
	ia, err1 := os.Stat(a)
	ib, err2 := os.Stat(b)
	if err1 != nil || err2 != nil || ia.Size() != ib.Size() {
		return false
	}
	sa, ok := fileSHA256(a)
	if !ok {
		return false
	}
	sb, ok := fileSHA256(b)
	return ok && bytes.Equal(sa, sb)
}

func fileSHA256(path string) ([]byte, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, false
	}
	return h.Sum(nil), true
}

// deletedDirLocked is where media of messages deleted for everyone is kept:
// outside the chat folder, which stays identical to an export.
func (c *Chat) deletedDirLocked() string { return c.a.metaPath("deleted", c.info.Folder) }

// MarkDeleted turns the entry for id into "deleted" (text is DeletedByThem or
// DeletedByMe), keeping its time. Its media leaves the chat folder (exports
// don't contain it) for .wacli/deleted, but only once the entry saying
// "deleted" is on disk, so a crash can't leave an attachment pointing at
// nothing. It reports whether the entry existed.
func (c *Chat) MarkDeleted(id, text string) (bool, error) {
	if c.a.opts.ReadOnly {
		return false, ErrReadOnly
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.useLocked(); err != nil {
		return false, err
	}
	var file string
	changed, err := c.updateLocked(id, func(e *Entry) bool {
		if e.Deleted != "" {
			return false
		}
		if ownFile(e.Attachment) {
			file = e.Attachment
		}
		e.MakeDeleted(text)
		return true
	})
	if err != nil || !changed {
		_, exists := c.lookupLocked(id)
		return exists, err
	}
	var metaErr error
	if file != "" {
		// Recorded before the entry is written, moved after (moveDeletedLocked).
		c.a.mu.Lock()
		c.info.DeletedMedia = append(c.info.DeletedMedia, file)
		metaErr = c.a.saveMetaLocked()
		c.a.mu.Unlock()
	}
	// The entry changed in memory: it must be written (or stay dirty) even
	// if saving chats.json failed.
	if err := c.saveLocked(); err != nil {
		return true, err
	}
	return true, metaErr
}

// moveDeletedLocked moves media of deleted entries out of the chat folder,
// once nothing on disk refers to it any more. It runs after every write and
// when a chat loads, so a crash in between only delays it.
func (c *Chat) moveDeletedLocked() {
	if len(c.info.DeletedMedia) == 0 || c.dirty {
		return
	}
	dst := c.deletedDirLocked()
	var left []string
	for _, f := range c.info.DeletedMedia {
		if !ownFile(f) {
			continue // never a path out of the chat folder, nor its _chat.txt
		}
		if c.referencedLocked(f) {
			left = append(left, f) // the "deleted" entry isn't written yet
			continue
		}
		if err := os.MkdirAll(dst, 0o700); err != nil {
			left = append(left, f)
			continue
		}
		err := os.Rename(filepath.Join(c.dirLocked(), f), filepath.Join(dst, freeName(dst, f)))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			left = append(left, f)
		}
	}
	if len(left) == len(c.info.DeletedMedia) {
		return
	}
	fsutil.Sync(dst)
	fsutil.Sync(c.dirLocked())
	c.a.mu.Lock()
	c.info.DeletedMedia = left
	_ = c.a.saveMetaLocked() // if this fails, the next pass finds the files gone
	c.a.mu.Unlock()
}

// Position returns the entry's 0-based position in the chat, or -1.
func (c *Chat) Position(id string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.useLocked() != nil {
		return -1
	}
	c.mergeLocked()
	if i, ok := c.byID[id]; ok {
		return i
	}
	return -1
}

// freeName returns name, or name-2, name-3… if dir already has that file.
func freeName(dir, name string) string {
	ext := filepath.Ext(name)
	dst := name
	for i := 2; exists(filepath.Join(dir, dst)); i++ {
		dst = fmt.Sprintf("%s-%d%s", strings.TrimSuffix(name, ext), i, ext)
	}
	return dst
}

// RenameSenders rewrites the sender name of entries for which name returns a
// new one; it gets each entry's bookkeeping row and current sender name.
func (c *Chat) RenameSenders(name func(row Row, sender string) (string, bool)) (int, error) {
	if c.a.opts.ReadOnly {
		return 0, ErrReadOnly
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.useLocked(); err != nil {
		return 0, err
	}
	c.mergeLocked()
	n := 0
	for i, raw := range c.entries {
		e, err := Parse(raw, c.a.opts.DateFormat, c.a.opts.Location)
		// System lines are attributed to the chat (Rename keeps them in
		// step); a location, poll or call is the sender's own.
		if err != nil || (e.System && (e.Sender == c.info.Name || e.Sender == CleanName(c.info.Name))) {
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
	c.mergeLocked()
	if err := os.MkdirAll(c.dirLocked(), 0o700); err != nil {
		return err
	}
	// Index first, as appends do: a crash in between leaves a row with no
	// entry, which realign drops and WhatsApp's redelivery fills in. The
	// other order would leave an entry without its ID, and the redelivered
	// message would be added twice.
	if err := c.writeIndex(); err != nil {
		return err
	}
	err := c.a.writeFile(c.chatPath(), func(w *bufio.Writer) error {
		for _, e := range c.entries {
			w.WriteString(e)
			w.WriteString(EntrySep)
		}
		return nil
	})
	if err != nil {
		return err
	}
	c.dirty = false
	c.moveDeletedLocked()
	return nil
}

func (c *Chat) writeIndex() error {
	return c.a.writeFile(c.indexPath(), func(w *bufio.Writer) error {
		for _, r := range c.rows {
			w.WriteString(formatRow(r))
		}
		return nil
	})
}

// FlushAll writes every chat with unwritten changes (a failed write leaves
// them in memory); call it before exiting.
func (a *Archive) FlushAll() error {
	a.mu.Lock()
	chats := make([]*Chat, 0, len(a.chats))
	for _, c := range a.chats {
		chats = append(chats, c)
	}
	a.mu.Unlock()
	var first error
	for _, c := range chats {
		if err := c.Flush(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// ownFile reports whether name is a file of the chat folder that a message
// may own: a plain name, not _chat.txt.
func ownFile(name string) bool {
	return name != "" && name != "." && name != ".." && name != ChatFile && filepath.Base(name) == name
}

// IDs and JIDs come from other people's clients; these keep a row on one
// line whatever they hold.
var (
	escapeField   = strings.NewReplacer("\\", "\\\\", "\t", "\\t", "\n", "\\n", "\r", "\\r")
	unescapeField = strings.NewReplacer("\\\\", "\\", "\\t", "\t", "\\n", "\n", "\\r", "\r")
)

func formatRow(r Row) string {
	from := "0"
	if r.FromMe {
		from = "1"
	}
	return strings.Join([]string{escapeField.Replace(r.ID), strconv.FormatInt(r.TS, 10), from, escapeField.Replace(r.Sender)}, "\t") + "\n"
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
		// A row cut short by a crash or a full disk is skipped: the count
		// then disagrees with _chat.txt and load realigns the index,
		// instead of refusing the chat.
		parts := strings.Split(sc.Text(), "\t")
		if len(parts) != 4 {
			continue
		}
		ts, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			continue
		}
		rows = append(rows, Row{ID: unescapeField.Replace(parts[0]), TS: ts, FromMe: parts[2] == "1", Sender: unescapeField.Replace(parts[3])})
	}
	return rows, sc.Err()
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func appendFile(path, s string) error {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		defer fsutil.Sync(filepath.Dir(path)) // a new file: its directory entry too
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, err = f.WriteString(s)
	if err == nil {
		err = f.Sync() // on disk before WhatsApp is told, and before the next write
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// writeFile replaces path atomically, with its temp file in .wacli/tmp so a
// crash can't leave one in a chat folder.
func (a *Archive) writeFile(path string, fill func(w *bufio.Writer) error) error {
	return fsutil.WriteAtomic(path, a.TmpDir(), 0o600, fill)
}

// TmpDir is where wacli keeps temp files: inside the archive, so renames
// into place stay on one filesystem.
func (a *Archive) TmpDir() string { return a.metaPath("tmp") }
