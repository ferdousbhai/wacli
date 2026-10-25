package archive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// MergeResult counts what an import changed.
type MergeResult struct {
	Matched       int // already in the chat
	Added         int // new entries
	MediaRestored int // "omitted" entries that got their file
	Skipped       int // dated in the future
}

// matchWindow absorbs the second WhatsApp sometimes adds between a message's
// own timestamp and the one the phone shows.
const matchWindow = 2 * time.Second

// Merge adds entries from a phone export (parsed from its _chat.txt) that the
// chat doesn't have yet. srcDir holds the export's attachments. Entries match
// when sender and time agree within matchWindow and their content agrees. An
// attachment missing from the export leaves its entry "… omitted". It stops
// between entries when ctx ends. With move, srcDir is wacli's own unpacked
// copy and its files are moved into place instead of copied.
func (c *Chat) Merge(ctx context.Context, entries []Entry, srcDir string, move bool) (res MergeResult, err error) {
	c.Hold()
	defer func() {
		if rerr := c.Release(); err == nil {
			err = rerr
		}
	}()
	// Match everything first: the export's own new lines must not match
	// each other (two "ok"s in one second are two messages).
	used := map[string]bool{}
	var add []int
	for i, in := range entries {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		id, found := c.find(in, used)
		if !found {
			// A line dated in the future (a forged export) would sit after
			// every live message, making each one rewrite the chat.
			if in.Time.After(time.Now().Add(24 * time.Hour)) {
				res.Skipped++
				continue
			}
			add = append(add, i)
			continue
		}
		used[id] = true
		res.Matched++
		if e, ok := c.Get(id); ok && e.Omitted != "" && in.Attachment != "" {
			restored, err := c.importFile(id, in.Attachment, srcDir, move)
			if err != nil {
				return res, err
			}
			if restored {
				res.MediaRestored++
			}
		}
	}
	for _, i := range add {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		in := entries[i]
		file := in.Attachment
		if file != "" {
			in.Attachment, in.Omitted = "", OmittedText(mediaTypeOf(file))
		}
		// The export position keeps identical lines apart.
		sum := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s", i, Render(in, c.a.opts.DateFormat))))
		row := Row{ID: "import:" + hex.EncodeToString(sum[:8]), TS: in.Time.Unix()}
		_, inserted, err := c.Insert(row, func(int) Entry { return in })
		if err != nil {
			return res, err
		}
		if !inserted {
			continue
		}
		res.Added++
		if file != "" {
			if _, err := c.importFile(row.ID, file, srcDir, move); err != nil {
				return res, err
			}
		}
	}
	return res, nil
}

// find locates the existing entry, not yet used, that is the same message as
// in, preferring the closest timestamp: two photos sent a second apart must
// not both match the first.
func (c *Chat) find(in Entry, used map[string]bool) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.useLocked(); err != nil {
		return "", false
	}
	if in.System && in.Sender == "" {
		in.Sender = c.info.Name
	}
	window := int64(matchWindow / time.Second)
	bestID, bestD := "", window+1
	for _, t := range []int64{in.Time.Unix(), otherReading(in, c.a.opts.DateFormat).Unix()} {
		c.findAt(in, t, window, used, &bestID, &bestD)
	}
	return bestID, bestID != ""
}

func (c *Chat) findAt(in Entry, t, window int64, used map[string]bool, bestID *string, bestD *int64) {
	consider := func(row Row, raw string) {
		d := row.TS - t
		if d < 0 {
			d = -d
		}
		if d > window || d >= *bestD || used[row.ID] {
			return
		}
		if e, err := Parse(raw, c.a.opts.DateFormat, c.a.opts.Location); err == nil && e.Sender == in.Sender && sameContent(e, in) {
			*bestID, *bestD = row.ID, d
		}
	}
	for i := sort.Search(len(c.rows), func(i int) bool { return c.rows[i].TS >= t-window }); i < len(c.rows) && c.rows[i].TS <= t+window; i++ {
		consider(c.rows[i], c.entries[i])
	}
	// Entries collected during a Hold count too (checked in place: merging
	// them for every lookup would undo what the Hold saves).
	for _, p := range c.pending {
		consider(p.row, p.text)
	}
}

// Find reports whether the chat already has an entry for the same message as
// e (same sender, time within two seconds, same content).
func (c *Chat) Find(e Entry) bool {
	_, ok := c.find(e, nil)
	return ok
}

func sameContent(a, b Entry) bool {
	if a.System != b.System || (a.Deleted != "") != (b.Deleted != "") {
		return false
	}
	media := func(e Entry) bool { return e.Attachment != "" || e.Omitted != "" }
	if media(a) != media(b) {
		return false
	}
	return normalizeText(a.Text) == normalizeText(b.Text) || media(a)
}

var mentionRE = regexp.MustCompile("@\u2068[^\u2069]*\u2069")

// normalizeText ignores mention names, which depend on whose address book
// rendered them ("@⁨Alex⁩" here, "@⁨Saved Messages⁩" on the phone).
func normalizeText(s string) string {
	return strings.TrimSpace(mentionRE.ReplaceAllString(s, "@"))
}

// importFile brings an export's attachment for entry id into the chat folder,
// placed under the chat lock like a download. It reports false when the
// export doesn't contain the file.
func (c *Chat) importFile(id, name, srcDir string, move bool) (bool, error) {
	base := filepath.Base(name)
	if base != name || base == "." || base == ".." {
		return false, fmt.Errorf("unsafe attachment name %q", name)
	}
	src := filepath.Join(srcDir, base)
	// An export holds plain files; a symlink, FIFO or device named like an
	// attachment (in a folder someone sent) is treated as missing.
	if fi, err := os.Lstat(src); err != nil || !fi.Mode().IsRegular() {
		return false, nil
	}
	if !move {
		// Never move the user's own files: copy them to the temp folder.
		in, err := os.Open(src)
		if err != nil {
			return false, err
		}
		defer in.Close()
		tmp, err := os.CreateTemp(c.a.TmpDir(), "import-*")
		if err != nil {
			return false, err
		}
		if _, err = io.Copy(tmp, in); err == nil {
			err = tmp.Close()
		} else {
			tmp.Close()
		}
		if err != nil {
			os.Remove(tmp.Name())
			return false, err
		}
		src = tmp.Name()
	}
	if _, err := c.PlaceAttachment(id, src, base); err != nil {
		if !move {
			os.Remove(src)
		}
		return false, err
	}
	return true, nil
}

// ReadExport parses an unzipped export folder's _chat.txt.
func (a *Archive) ReadExport(dir string) ([]Entry, error) {
	data, err := os.ReadFile(filepath.Join(dir, ChatFile))
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, raw := range SplitEntries(string(data)) {
		e, err := Parse(raw, a.opts.DateFormat, a.opts.Location)
		if err != nil {
			return nil, fmt.Errorf("%s: %w (is --date-format right for this export?)", ChatFile, err)
		}
		out = append(out, e)
	}
	return out, nil
}
