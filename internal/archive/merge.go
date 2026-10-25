package archive

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
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
}

// matchWindow absorbs the second WhatsApp sometimes adds between a message's
// own timestamp and the one the phone shows.
const matchWindow = 2 * time.Second

// Merge adds entries from a phone export (parsed from its _chat.txt) that the
// chat doesn't have yet. srcDir holds the export's attachments. Entries match
// when sender and time agree within matchWindow and their content agrees. An
// attachment missing from the export leaves its entry "… omitted".
func (c *Chat) Merge(entries []Entry, srcDir string) (res MergeResult, err error) {
	used := map[string]bool{} // each archived entry matches one imported entry
	var add []int
	for i, in := range entries {
		id, found := c.find(in, used)
		if !found {
			add = append(add, i)
			continue
		}
		used[id] = true
		res.Matched++
		if in.Attachment != "" {
			restored, err := c.restoreMedia(id, in, srcDir)
			if err != nil {
				return res, err
			}
			if restored {
				res.MediaRestored++
			}
		}
	}
	c.Hold()
	defer func() {
		if rerr := c.Release(); err == nil {
			err = rerr
		}
	}()
	for _, i := range add {
		in := entries[i]
		if in.Attachment != "" {
			if name, ok, err := c.importFile(in.Attachment, srcDir); err != nil {
				return res, err
			} else if ok {
				in.Attachment = name
			} else {
				in.Attachment, in.Omitted = "", OmittedText(mediaTypeOf(in.Attachment))
			}
		}
		// The export position keeps identical lines (two "ok"s in one
		// second) apart.
		sum := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s", i, Render(in, c.a.opts.DateFormat))))
		row := Row{ID: "import:" + hex.EncodeToString(sum[:8]), TS: in.Time.Unix()}
		if _, inserted, err := c.Insert(row, func(int) Entry { return in }); err != nil {
			return res, err
		} else if inserted {
			res.Added++
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
	window := int64(matchWindow / time.Second)
	t := in.Time.Unix()
	best, bestD := -1, window+1
	for i := sort.Search(len(c.rows), func(i int) bool { return c.rows[i].TS >= t-window }); i < len(c.rows) && c.rows[i].TS <= t+window; i++ {
		d := c.rows[i].TS - t
		if d < 0 {
			d = -d
		}
		if d >= bestD || used[c.rows[i].ID] {
			continue
		}
		e, err := Parse(c.entries[i], c.a.opts.DateFormat, c.a.opts.Location)
		if err != nil || e.Sender != in.Sender || !sameContent(e, in) {
			continue
		}
		best, bestD = i, d
	}
	if best < 0 {
		return "", false
	}
	return c.rows[best].ID, true
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

func (c *Chat) restoreMedia(id string, in Entry, srcDir string) (bool, error) {
	if e, ok := c.Get(id); !ok || e.Omitted == "" {
		return false, nil
	}
	name, ok, err := c.importFile(in.Attachment, srcDir)
	if err != nil || !ok {
		return false, err
	}
	return c.Update(id, func(e *Entry) bool {
		e.Omitted, e.Attachment = "", name
		return true
	})
}

// importFile copies an export's attachment into the chat folder, keeping its
// name unless the folder already has a file called that. ok is false when the
// export doesn't contain the file.
func (c *Chat) importFile(name, srcDir string) (dst string, ok bool, err error) {
	base := filepath.Base(name)
	if base != name || base == "." || base == ".." {
		return "", false, fmt.Errorf("unsafe attachment name %q", name)
	}
	src, err := os.Open(filepath.Join(srcDir, base))
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	defer src.Close()
	dir := c.Dir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", false, err
	}
	dst = freeName(dir, base)
	out, err := os.OpenFile(filepath.Join(dir, dst), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", false, err
	}
	if _, err := io.Copy(out, src); err != nil {
		out.Close()
		os.Remove(out.Name())
		return "", false, err
	}
	return dst, true, out.Close()
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
