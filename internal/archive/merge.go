package archive

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// MergeResult counts what an import changed.
type MergeResult struct {
	Matched       int `json:"matched"`        // already in the chat
	Added         int `json:"added"`          // new entries
	MediaRestored int `json:"media_restored"` // "omitted" entries that got their file
}

// matchWindow absorbs the second WhatsApp sometimes adds between a message's
// own timestamp and the one the phone shows.
const matchWindow = 2 * time.Second

// Merge adds entries from a phone export (parsed from its _chat.txt) that the
// chat doesn't have yet. srcDir holds the export's attachments. Entries match
// when sender and time agree within matchWindow and their content agrees.
func (c *Chat) Merge(entries []Entry, srcDir string) (MergeResult, error) {
	var res MergeResult
	used := map[string]bool{} // each archived entry matches one imported entry
	for _, in := range entries {
		id, pos, found := c.find(in, used)
		if found {
			used[id] = true
			res.Matched++
			if in.Attachment == "" {
				continue
			}
			restored, err := c.restoreMedia(id, pos, in, srcDir)
			if err != nil {
				return res, err
			}
			if restored {
				res.MediaRestored++
			}
			continue
		}
		if in.Attachment != "" {
			name, err := c.copyAttachment(in.Attachment, srcDir)
			if err != nil {
				return res, err
			}
			in.Attachment = name
		}
		sum := sha256.Sum256([]byte(Render(in, c.a.opts.DateFormat)))
		row := Row{ID: "import:" + hex.EncodeToString(sum[:8]), TS: in.Time.Unix()}
		entry := in
		if _, inserted, err := c.Insert(row, func(int) Entry { return entry }); err != nil {
			return res, err
		} else if inserted {
			used[row.ID] = true
			res.Added++
		}
	}
	return res, nil
}

// find locates the existing entry, not yet used, that is the same message as
// in, preferring the closest timestamp: two photos sent a second apart must
// not both match the first.
func (c *Chat) find(in Entry, used map[string]bool) (string, int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	best, bestD := -1, matchWindow+1
	for i, row := range c.rows {
		d := time.Duration(row.TS-in.Time.Unix()) * time.Second
		if d < 0 {
			d = -d
		}
		if d > matchWindow || d >= bestD || used[row.ID] {
			continue
		}
		e, err := Parse(c.entries[i], c.a.opts.DateFormat, c.a.opts.Location)
		if err != nil || e.Sender != in.Sender || !sameContent(e, in) {
			continue
		}
		best, bestD = i, d
	}
	if best < 0 {
		return "", -1, false
	}
	return c.rows[best].ID, best, true
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

func (c *Chat) restoreMedia(id string, pos int, in Entry, srcDir string) (bool, error) {
	e, _, ok := c.Get(id)
	if !ok || e.Omitted == "" {
		return false, nil
	}
	name, err := c.copyAttachment(in.Attachment, srcDir)
	if err != nil {
		return false, err
	}
	return c.Update(id, func(e *Entry, _ int) bool {
		e.Omitted, e.Attachment = "", name
		return true
	})
}

// copyAttachment copies an export's file into the chat folder, keeping its
// name unless another file already has it.
func (c *Chat) copyAttachment(name, srcDir string) (string, error) {
	base := filepath.Base(name)
	if base != name || base == "." || base == ".." {
		return "", fmt.Errorf("unsafe attachment name %q", name)
	}
	src, err := os.Open(filepath.Join(srcDir, base))
	if err != nil {
		return "", err
	}
	defer src.Close()
	dst := base
	for i := 2; ; i++ {
		if _, err := os.Stat(filepath.Join(c.Dir(), dst)); os.IsNotExist(err) {
			break
		}
		ext := filepath.Ext(base)
		dst = fmt.Sprintf("%s-%d%s", strings.TrimSuffix(base, ext), i, ext)
	}
	out, err := os.OpenFile(filepath.Join(c.Dir(), dst), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, src); err != nil {
		out.Close()
		return "", err
	}
	return dst, out.Close()
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
