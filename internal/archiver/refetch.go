package archiver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ferdousbhai/wacli/internal/archive"
	"github.com/ferdousbhai/wacli/internal/fsutil"
	"github.com/ferdousbhai/wacli/internal/wa"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
)

// RefetchItem is one empty line and what the phone said it is.
type RefetchItem struct {
	Folder string    `json:"folder"`
	Chat   string    `json:"chat"`
	ID     string    `json:"id"`
	Time   time.Time `json:"time"`
	Kind   string    `json:"kind"`   // the message's type per the phone; "" when it didn't answer
	Filled bool      `json:"filled"` // the line shows something now
}

type RefetchOptions struct {
	Chat        string // one chat's JID; "" for all
	Count       int    // messages per request
	Wait        time.Duration
	MaxRequests int  // 0: no limit; with a limit, the first silence ends the run
	Again       bool // ask about lines already checked too
}

type RefetchResult struct {
	Items []RefetchItem `json:"items"` // the lines asked about
	Left  int           `json:"left"`  // empty lines still to check
}

type refetchAnswer struct {
	id, kind string
	filled   bool
}

// refetchJob is one request: the history before anchor, which holds want.
type refetchJob struct {
	c      *archive.Chat
	folder string
	anchor archive.Row
	want   []archive.Row
}

// Refetch asks the phone again for messages archived as empty lines, fills
// in what wacli can show, and records each one's type in refetched.json so
// it isn't asked about again. Phones ignore requests to resend a message a
// linked device already has, so it asks for the history just before the
// message after each empty line, which includes it. Albums are empty lines in
// exports too; asking for them confirms what they are.
func (a *Archiver) Refetch(ctx context.Context, o RefetchOptions) (res RefetchResult, err error) {
	a.refetchRun.Lock()
	defer a.refetchRun.Unlock()
	if o.Count <= 0 {
		o.Count = 50
	}
	jobs, err := a.refetchJobs(o)
	if err != nil {
		return res, err
	}
	defer func() {
		for _, j := range jobs {
			res.Left += len(j.want)
		}
		if o.Chat == "" && !o.Again {
			a.status.setUncheckedLines(res.Left)
		}
	}()
	silent, sent := 0, 0 // silent: requests in a row the phone didn't reply to at all
	for len(jobs) > 0 && (o.MaxRequests <= 0 || sent < o.MaxRequests) {
		j := jobs[0]
		sent++
		got, err := a.requestAround(ctx, j, o.Count, o.Wait)
		if errors.Is(err, ErrNoAnswer) {
			// WhatsApp stops answering soon after the phone locks: stop
			// rather than mark lines unanswered one timeout at a time.
			if silent++; silent >= 3 || o.MaxRequests > 0 {
				return res, ErrPhoneQuiet
			}
			jobs = append(jobs[1:], j) // tried again last
			continue
		}
		silent = 0
		jobs = jobs[1:]
		for _, row := range j.want {
			item := RefetchItem{Folder: j.folder, Chat: j.c.JID(), ID: row.ID, Time: time.Unix(row.TS, 0)}
			ans, ok := got[row.ID]
			if !ok && errors.Is(err, errNotSent) {
				ans = refetchAnswer{id: row.ID, kind: "not on phone"}
			}
			if ans.kind != "" {
				item.Kind, item.Filled = ans.kind, ans.filled
				a.checked.note(j.c.JID(), row.ID, ans.kind)
			}
			res.Items = append(res.Items, item)
		}
		a.checked.save(a.ar.TmpDir(), a.logf)
		if err != nil && !errors.Is(err, errNotSent) {
			return res, err
		}
	}
	return res, nil
}

// refetchJobs groups the empty lines not checked yet into requests, newest
// first: each covers the lines up to count messages before its anchor.
func (a *Archiver) refetchJobs(o RefetchOptions) ([]refetchJob, error) {
	var jobs []refetchJob
	for _, info := range a.ar.Chats() {
		if o.Chat != "" && info.JID != o.Chat {
			continue
		}
		c, err := a.ar.Chat(info.JID)
		if err != nil {
			return nil, err
		}
		rows := c.Rows()
		var blanks []int // positions, oldest first
		for i, row := range rows {
			if strings.Contains(row.ID, ":") || !o.Again && a.checked.has(info.JID, row.ID) {
				continue // ":" IDs are wacli's own; the phone doesn't know them
			}
			if e, ok := c.Get(row.ID); ok && blank(e) {
				blanks = append(blanks, i)
			}
		}
		for len(blanks) > 0 {
			last := blanks[len(blanks)-1]
			anchor, ok := anchorAfter(rows, last)
			if !ok {
				// Nothing newer to anchor on: just after it in time, by an ID
				// the phone doesn't know.
				anchor = archive.Row{ID: "3EB0" + strings.ToUpper(rows[last].ID), TS: rows[last].TS + 1, FromMe: true}
			}
			j := refetchJob{c: c, folder: info.Folder, anchor: anchor}
			for len(blanks) > 0 && blanks[len(blanks)-1] > last-o.Count {
				j.want = append(j.want, rows[blanks[len(blanks)-1]])
				blanks = blanks[:len(blanks)-1]
			}
			jobs = append(jobs, j)
		}
	}
	return jobs, nil
}

// anchorAfter is the first message after position i the phone knows.
func anchorAfter(rows []archive.Row, i int) (archive.Row, bool) {
	for _, r := range rows[i+1:] {
		if !strings.Contains(r.ID, ":") {
			return r, true
		}
	}
	return archive.Row{}, false
}

// requestAround asks for the history before j.anchor and collects the
// answers for j.want, trying the chat's other address when the first is
// silent. The messages are archived by archiveHistory as they arrive.
func (a *Archiver) requestAround(ctx context.Context, j refetchJob, count int, wait time.Duration) (map[string]refetchAnswer, error) {
	chat, err := types.ParseJID(j.c.JID())
	if err != nil {
		return nil, err
	}
	ch := make(chan refetchAnswer, len(j.want))
	a.mu.Lock()
	for _, row := range j.want {
		a.refetch[row.ID] = ch
	}
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		for _, row := range j.want {
			delete(a.refetch, row.ID)
		}
		a.mu.Unlock()
	}()
	sender, _ := types.ParseJID(j.anchor.Sender)
	got := map[string]refetchAnswer{}
	for _, target := range a.chatJIDs(ctx, chat) {
		info := types.MessageInfo{
			MessageSource: types.MessageSource{Chat: target, Sender: sender, IsFromMe: j.anchor.FromMe, IsGroup: target.Server == types.GroupServer},
			ID:            j.anchor.ID,
			Timestamp:     time.Unix(j.anchor.TS, 0),
		}
		replies := a.onDemand.Load()
		if _, err := a.wa.RequestHistorySyncOnDemand(ctx, info, count); err != nil {
			return got, err
		}
		replied, err := a.collect(ctx, ch, got, len(j.want), replies, wait)
		if err != nil || len(got) == len(j.want) {
			return got, err
		}
		if replied {
			// The reply is in and archived: what it lacked, the phone doesn't have.
			return got, errNotSent
		}
	}
	return got, ErrNoAnswer
}

// collect gathers answers into got until it has want of them, a reply to the
// request (counted by onDemand past replies) is in, or wait passes. Answers
// are sent before a reply is counted, so none is left behind.
func (a *Archiver) collect(ctx context.Context, ch chan refetchAnswer, got map[string]refetchAnswer, want int, replies int64, wait time.Duration) (replied bool, err error) {
	deadline := time.After(wait)
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for len(got) < want {
		select {
		case <-ctx.Done():
			return replied, ctx.Err()
		case ans := <-ch:
			got[ans.id] = ans
		case <-tick.C:
			if a.onDemand.Load() == replies {
				continue
			}
			for {
				select {
				case ans := <-ch:
					got[ans.id] = ans
				default:
					return true, nil
				}
			}
		case <-deadline:
			return false, nil
		}
	}
	return a.onDemand.Load() != replies, nil
}

// errNotSent is a reply to a Refetch request without the messages asked for:
// the phone no longer has them.
var errNotSent = errors.New("the phone's reply didn't include the messages")

// ErrPhoneQuiet ends a Refetch whose requests the phone stopped answering.
var ErrPhoneQuiet = errors.New("the phone stopped answering (WhatsApp pauses when the phone locks); the sync carries on whenever you use WhatsApp on the phone")

// chatJIDs is jid and, for a person, their other address: the phone keeps a
// chat under only one of the two, these days mostly the hidden one, so that
// is tried first.
func (a *Archiver) chatJIDs(ctx context.Context, jid types.JID) []types.JID {
	switch jid.Server {
	case types.DefaultUserServer:
		if lid := a.wa.ResolvePNToLID(ctx, jid); !lid.IsEmpty() && lid != jid {
			return []types.JID{lid, jid}
		}
	case types.HiddenUserServer:
		if pn := a.wa.ResolveLIDToPN(ctx, jid); !pn.IsEmpty() && pn != jid {
			return []types.JID{jid, pn}
		}
	}
	return []types.JID{jid}
}

// phoneActive notes a sign that WhatsApp is open on the phone (a message
// sent or read there), when it answers requests at once and without asking
// the user to open it.
func (a *Archiver) phoneActive() {
	select {
	case a.phoneSeen <- struct{}{}:
	default:
	}
}

// refetchLoop checks empty lines a few at a time while the phone is in use,
// so the backlog clears with no one having to keep WhatsApp open for it.
func (a *Archiver) refetchLoop(ctx context.Context) {
	const perRound, pause = 5, 2 * time.Minute
	jobs, err := a.refetchJobs(RefetchOptions{Count: 50})
	if err != nil {
		a.logf("warning: finding empty lines: %v", err)
	}
	left := 0
	for _, j := range jobs {
		left += len(j.want)
	}
	a.status.setUncheckedLines(left)
	if left == 0 {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.phoneSeen:
		}
		res, err := a.Refetch(ctx, RefetchOptions{MaxRequests: perRound, Wait: 20 * time.Second})
		if len(res.Items) > 0 || err != nil && !errors.Is(err, ErrPhoneQuiet) {
			a.logf("refetch: checked %d empty lines, %d left (%v)", len(res.Items), res.Left, err)
		}
		if res.Left == 0 && err == nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(pause):
		}
		select { // a sign seen during the pause is stale
		case <-a.phoneSeen:
		default:
		}
	}
}

// refetchWanted returns the channel of a Refetch waiting for message id.
func (a *Archiver) refetchWanted(id string) chan refetchAnswer {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.refetch[id]
}

// answerRefetch tells a waiting Refetch what a message from history is and
// whether its line shows something now. Called once the chunk is written.
func (a *Archiver) answerRefetch(ctx context.Context, ch chan refetchAnswer, chatID string, web *waWeb.WebMessageInfo) {
	id := web.GetKey().GetID()
	kind := webKind(chatID, web)
	filled := false
	if jid, err := types.ParseJID(chatID); err == nil {
		if c, err := a.knownChat(ctx, jid); err == nil && c != nil {
			e, ok := c.Get(id)
			filled = ok && !blank(e)
		}
	}
	a.logf("refetched %s in %s: %s (filled: %v)", id, chatID, kind, filled)
	select {
	case ch <- refetchAnswer{id: id, kind: kind, filled: filled}:
	default:
	}
}

func webKind(chatID string, web *waWeb.WebMessageInfo) string {
	switch t := web.GetMessageStubType(); {
	case t == waWeb.WebMessageInfo_REVOKE || t == waWeb.WebMessageInfo_ADMIN_REVOKE:
		return "deleted"
	case t != 0:
		return "notice: " + strings.ToLower(t.String())
	case web.GetMessage() == nil:
		return "no content"
	}
	return describe(wa.ParseHistoryMessage(chatID, web))
}

// blank reports a line with nothing after the sender's name.
func blank(e archive.Entry) bool {
	return e.Text == "" && !e.System && e.Omitted == "" && e.Attachment == "" && e.Deleted == "" && e.DocLabel == ""
}

// describe names a message's type, for the log and Refetch.
func describe(pm wa.ParsedMessage) string {
	switch {
	case pm.IsAlbum:
		return "album"
	case pm.Revoked:
		return "deleted"
	case pm.Media != nil:
		return pm.Media.Type
	case pm.Location != nil:
		return "location"
	case pm.Poll != nil:
		return "poll"
	case pm.VCard != nil:
		return "contact card"
	case pm.Call != nil:
		return "call"
	case pm.HasButtons:
		return "buttons"
	case strings.TrimSpace(pm.Text) != "":
		return "text"
	case pm.UnhandledPayload != "":
		return pm.UnhandledPayload
	}
	return "no content"
}

// checkedLines is .wacli/refetched.json: per chat, the empty lines whose
// type the phone has told (album, not on phone, a type wacli can't show),
// so they aren't asked about again.
type checkedLines struct {
	path  string
	mu    sync.Mutex
	kinds map[string]map[string]string // chat JID → message ID → kind
	dirty bool
}

func loadCheckedLines(root string, logf func(string, ...any)) *checkedLines {
	l := &checkedLines{path: filepath.Join(root, ".wacli", "refetched.json"), kinds: map[string]map[string]string{}}
	data, err := os.ReadFile(l.path)
	if err == nil {
		if err := json.Unmarshal(data, &l.kinds); err != nil {
			logf("warning: %s unreadable (%v); empty lines will be checked again", l.path, err)
			l.kinds = map[string]map[string]string{}
		}
	}
	return l
}

func (l *checkedLines) has(chat, id string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.kinds[chat][id]
	return ok
}

func (l *checkedLines) note(chat, id, kind string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.kinds[chat] == nil {
		l.kinds[chat] = map[string]string{}
	}
	l.kinds[chat][id] = kind
	l.dirty = true
}

func (l *checkedLines) save(tmpDir string, logf func(string, ...any)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.dirty {
		return
	}
	err := fsutil.WriteAtomic(l.path, tmpDir, 0o600, func(w *bufio.Writer) error {
		return json.NewEncoder(w).Encode(l.kinds)
	})
	if err != nil {
		logf("warning: saving %s: %v", l.path, err)
		return
	}
	l.dirty = false
}
