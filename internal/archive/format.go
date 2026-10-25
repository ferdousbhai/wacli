// Package archive stores chats as iOS "Export chat" folders: a _chat.txt plus
// the attached media, kept current as messages arrive.
package archive

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	lrm = "‎" // left-to-right mark iOS puts before generated text
	fsi = "⁨" // first strong isolate, opens a mention name
	pdi = "⁩" // pop directional isolate, closes a mention name

	// EntrySep ends every entry; newlines inside a message are bare "\n".
	EntrySep = "\r\n"

	// DefaultDateFormat matches an iPhone set to an ISO-style region.
	DefaultDateFormat = "2006-01-02, 15:04:05"

	editedMarker = lrm + "<This message was edited>"
	attachOpen   = lrm + "<attached: "

	DeletedByThem = "This message was deleted."
	DeletedByMe   = "You deleted this message."
)

// Entry is one message as it appears in _chat.txt.
type Entry struct {
	Time   time.Time
	Sender string // display name; the chat name for system entries
	Text   string // message text or caption, mentions already rendered

	// System marks generated lines ("You created group …", calls, the
	// encryption notice). Text holds the line without its leading mark.
	System bool

	Attachment string // file name inside the chat folder
	DocLabel   string // "<name> • ‎<n> pages" shown before a document
	Omitted    string // "image omitted" etc. when the media is missing

	Edited  bool
	Deleted string // DeletedByThem or DeletedByMe

	// stamp is the timestamp text as read from the file. Rewriting an entry
	// keeps it, so a time that doesn't exist in today's zone (a DST gap) or
	// was written in another zone isn't shifted.
	stamp string
}

// MakeDeleted turns e into a "deleted" entry, keeping its time and sender.
func (e *Entry) MakeDeleted(text string) {
	*e = Entry{Time: e.Time, Sender: e.Sender, stamp: e.stamp, Deleted: text}
}

// MakeSystem turns e into a system line, keeping its time and sender.
func (e *Entry) MakeSystem(text string) {
	*e = Entry{Time: e.Time, Sender: e.Sender, stamp: e.stamp, System: true, Text: text}
}

// Render formats e as it appears in _chat.txt, without the trailing EntrySep.
func Render(e Entry, dateFormat string) string {
	if dateFormat == "" {
		dateFormat = DefaultDateFormat
	}
	e.Sender = CleanName(e.Sender)
	body := renderBody(e)
	var b strings.Builder
	if e.Deleted == "" && (e.Attachment != "" || e.Omitted != "") {
		b.WriteString(lrm)
	}
	b.WriteString("[")
	if e.stamp != "" {
		b.WriteString(e.stamp)
	} else {
		b.WriteString(e.Time.Format(dateFormat))
	}
	b.WriteString("] ")
	b.WriteString(e.Sender)
	b.WriteString(":")
	if body != "" {
		b.WriteString(" ")
		b.WriteString(body)
	}
	return b.String()
}

// lineBreaks keeps "\r" out of entries, where "\r\n" ends an entry.
var lineBreaks = strings.NewReplacer("\r\n", "\n", "\r", "\n")

func renderBody(e Entry) string {
	e.Text = lineBreaks.Replace(e.Text)
	switch {
	case e.Deleted != "":
		return lrm + e.Deleted
	case e.System:
		return lrm + e.Text
	}
	var parts []string
	if e.Text != "" {
		parts = append(parts, e.Text)
	}
	if e.DocLabel != "" {
		parts = append(parts, e.DocLabel)
	}
	switch {
	case e.Attachment != "":
		parts = append(parts, attachOpen+e.Attachment+">")
	case e.Omitted != "":
		parts = append(parts, lrm+e.Omitted)
	}
	if e.Edited {
		parts = append(parts, editedMarker)
	}
	return strings.Join(parts, " ")
}

// The sender may contain a newline: older files kept contact names verbatim.
var headerRE = regexp.MustCompile(`^\x{200e}?\[([^\]]+)\] ([^:]*):(?: |$)`)

// Parse reads one entry. Times are interpreted in loc.
func Parse(raw, dateFormat string, loc *time.Location) (Entry, error) {
	if dateFormat == "" {
		dateFormat = DefaultDateFormat
	}
	m := headerRE.FindStringSubmatchIndex(raw)
	if m == nil {
		return Entry{}, fmt.Errorf("not a chat entry: %.40q", raw)
	}
	ts, err := time.ParseInLocation(dateFormat, raw[m[2]:m[3]], loc)
	if err != nil {
		return Entry{}, err
	}
	e := Entry{Time: ts, Sender: raw[m[4]:m[5]], stamp: raw[m[2]:m[3]]}
	body := raw[m[1]:]

	if rest, ok := strings.CutSuffix(body, " "+editedMarker); ok {
		e.Edited, body = true, rest
	} else if rest, ok := strings.CutSuffix(body, editedMarker); ok {
		e.Edited, body = true, rest
	}
	switch body {
	case lrm + DeletedByThem:
		e.Deleted = DeletedByThem
		return e, nil
	case lrm + DeletedByMe:
		e.Deleted = DeletedByMe
		return e, nil
	}
	if i := strings.LastIndex(body, attachOpen); i >= 0 && strings.HasSuffix(body, ">") {
		e.Attachment = body[i+len(attachOpen) : len(body)-1]
		body = strings.TrimSuffix(body[:i], " ")
		if !isGeneratedMediaName(e.Attachment) {
			// Documents: "<name> • ‎<n> pages" (or just "<name>") before the file.
			label := filepath.Base(e.Attachment)
			if _, name, ok := strings.Cut(label, "-"); ok {
				label = name
			}
			if j := strings.LastIndex(body, label); j >= 0 {
				e.DocLabel = body[j:]
				body = strings.TrimSuffix(body[:j], " ")
			}
		}
		e.Text = body
		return e, nil
	}
	for _, kind := range omittedKinds {
		if rest, ok := strings.CutSuffix(body, lrm+kind); ok {
			e.Omitted = kind
			e.Text = strings.TrimSuffix(rest, " ")
			return e, nil
		}
	}
	if strings.HasPrefix(body, lrm) {
		e.System = true
		e.Text = strings.TrimPrefix(body, lrm)
		return e, nil
	}
	e.Text = body
	return e, nil
}

var omittedKinds = []string{
	"image omitted", "video omitted", "audio omitted", "sticker omitted",
	"GIF omitted", "document omitted", "Contact card omitted",
}

var generatedNameRE = regexp.MustCompile(`^\d{8}-(PHOTO|VIDEO|AUDIO|STICKER|GIF)-\d{4}-\d{2}-\d{2}-\d{2}-\d{2}-\d{2}\.`)

func isGeneratedMediaName(name string) bool { return generatedNameRE.MatchString(name) }

// renameSystemSender returns raw with its sender replaced, if raw is a system
// line attributed to oldSender. It matches the known name rather than parsing
// the line, so a chat name containing ": " works too.
func renameSystemSender(raw, oldSender, newSender string) (string, bool) {
	i := strings.Index(raw, "] ")
	if i < 0 || oldSender == "" {
		return "", false
	}
	rest := raw[i+2:]
	if !strings.HasPrefix(rest, oldSender+": "+lrm) {
		return "", false
	}
	return raw[:i+2] + CleanName(newSender) + rest[len(oldSender):], true
}

// SplitEntries splits _chat.txt content into raw entries. A piece that does
// not start with an entry header continues the previous entry: a message text
// that itself contained "\r\n" (files written before Render normalized it).
// A file without any "\r\n" (converted to Unix line ends by a script, an
// editor or git) is split on "\n" instead; load rewrites it.
func SplitEntries(content string) []string {
	sep := EntrySep
	if !strings.Contains(content, EntrySep) {
		sep = "\n"
	}
	return splitOn(content, sep)
}

// SplitLines splits content into entries at every line that starts with an
// entry header, whatever the line ends: for files whose endings were mixed
// by an edit, when SplitEntries finds fewer entries than the index has rows.
func SplitLines(content string) []string {
	return splitOn(strings.ReplaceAll(content, EntrySep, "\n"), "\n")
}

func splitOn(content, sep string) []string {
	content = strings.TrimPrefix(content, "\ufeff") // a BOM an editor added
	if rest, ok := strings.CutSuffix(content, sep); ok {
		content = rest // a "\n" before it belongs to the last message
	} else {
		content = strings.TrimSuffix(content, "\n") // e.g. a line added with echo
	}
	if content == "" {
		return nil
	}
	var out []string
	for _, piece := range strings.Split(content, sep) {
		if len(out) > 0 && !headerRE.MatchString(piece) {
			out[len(out)-1] += "\n" + piece
			continue
		}
		out = append(out, piece)
	}
	return out
}

// mediaKinds maps wacli media types to the iOS file label and extension.
var mediaKinds = map[string]struct{ label, ext, omitted string }{
	"image":   {"PHOTO", ".jpg", "image omitted"},
	"video":   {"VIDEO", ".mp4", "video omitted"},
	"gif":     {"GIF", ".mp4", "GIF omitted"},
	"audio":   {"AUDIO", ".opus", "audio omitted"},
	"sticker": {"STICKER", ".webp", "sticker omitted"},
}

// mediaTypeOf maps a generated attachment name back to its media type;
// anything else is a document.
func mediaTypeOf(name string) string {
	m := generatedNameRE.FindStringSubmatch(name)
	if m == nil {
		return "document"
	}
	for t, k := range mediaKinds {
		if k.label == m[1] {
			return t
		}
	}
	return "document"
}

// AttachmentName returns the file name iOS gives an attachment that is the
// pos-th entry (0-based) of its chat. Documents keep their own name.
func AttachmentName(pos int, mediaType string, ts time.Time, docName string) string {
	if k, ok := mediaKinds[mediaType]; ok {
		return fmt.Sprintf("%08d-%s-%s%s", pos, k.label, ts.Format("2006-01-02-15-04-05"), k.ext)
	}
	name := sanitizeFileName(docName)
	if name == "" {
		name = "document"
	}
	return fmt.Sprintf("%08d-%s", pos, truncateName(name, 40))
}

// Downloadable reports media types that have a file to download.
func Downloadable(mediaType string) bool {
	_, ok := mediaKinds[mediaType]
	return ok || mediaType == "document"
}

// OmittedText is what iOS writes when a media file is not attached.
func OmittedText(mediaType string) string {
	if k, ok := mediaKinds[mediaType]; ok {
		return k.omitted
	}
	return "document omitted"
}

// DocLabel is the text iOS shows before an attached document.
func DocLabel(name string, pages uint32) string {
	if pages == 0 {
		return name
	}
	unit := "pages"
	if pages == 1 {
		unit = "page"
	}
	return fmt.Sprintf("%s • %s%d %s", name, lrm, pages, unit)
}

// CleanName puts a contact or group name on one line: a sender or folder
// name must not contain a line break.
func CleanName(name string) string {
	return strings.Join(strings.FieldsFunc(name, func(r rune) bool {
		return r == '\n' || r == '\r' || r == '\t'
	}), " ")
}

func sanitizeFileName(name string) string {
	name = CleanName(name)
	name = strings.Map(func(r rune) rune {
		switch {
		case r == '/' || r == '\\' || r == 0:
			return '_'
		case IsBidiMark(r):
			return -1 // invisible in a path, impossible to type
		}
		return r
	}, strings.TrimSpace(name))
	return strings.TrimLeft(name, ".")
}

// IsBidiMark reports invisible direction marks, which iOS puts around phone
// numbers and before generated text.
func IsBidiMark(r rune) bool {
	return r == 0x200e || r == 0x200f || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069)
}

// truncateName shortens the stem to max runes, keeping the extension, as iOS
// does ("Restructuring Invoice - PT. Acme Holding.pdf").
func truncateName(name string, max int) string {
	ext := filepath.Ext(name)
	stem := []rune(strings.TrimSuffix(name, ext))
	if len(stem) <= max {
		return name
	}
	return string(stem[:max]) + ext
}
