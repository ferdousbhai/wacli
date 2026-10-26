package archiver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
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
	Kind   string    `json:"kind"`   // the message's type per the phone; "" when not known yet
	Filled bool      `json:"filled"` // the line shows something now
}

type RefetchOptions struct {
	Chat        string // one chat's JID; "" for all
	Count       int    // messages per request
	Wait        time.Duration
	MaxRequests int  // 0: no limit; with a limit, the first silence ends the run
	Again       bool // ask about lines already checked too

	chats  map[string]bool // only these chats (the background loop's backlog)
	cursor string          // start after this chat's folder
}

type RefetchResult struct {
	Items []RefetchItem `json:"items"` // the lines asked about
	Left  int           `json:"left"`  // empty lines still to check

	leftChats map[string]bool
	last      string // folder of the last chat asked about
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
	seen   map[string]bool // anchors asked from so far for these lines
}

// refetchWaiter is the request a Refetch is waiting on; archiveHistory hands
// it the reply.
type refetchWaiter struct {
	reqID   string
	chat    string // archive key of the chat asked about
	replies chan historyBatch
}

// noAnswerAfter is how many requests about a line may tell nothing, while
// the phone answers others, before it's recorded as "no answer" (a chat the
// phone no longer has, say).
const noAnswerAfter = 3

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
	if o.Wait <= 0 {
		o.Wait = 30 * time.Second
	}
	jobs, err := a.refetchJobs(ctx, o)
	if err != nil {
		return res, err
	}
	total := 0
	for _, j := range jobs {
		total += len(j.want)
	}
	if o.cursor != "" { // carry on after the chat the last round stopped at
		if i := slices.IndexFunc(jobs, func(j refetchJob) bool { return j.folder > o.cursor }); i > 0 {
			jobs = append(jobs[i:], jobs[:i]...)
		}
	}
	items := map[string]*RefetchItem{}
	var order []string
	resolved := 0
	settle := func(j refetchJob, row archive.Row, kind string, filled bool) {
		it, ok := items[row.ID]
		if !ok {
			it = &RefetchItem{Folder: j.folder, Chat: j.c.JID(), ID: row.ID, Time: time.Unix(row.TS, 0)}
			items[row.ID] = it
			order = append(order, row.ID)
		}
		if kind != "" && it.Kind == "" {
			resolved++
			it.Kind, it.Filled = kind, filled
			a.checked.note(j.c.JID(), row.ID, kind)
		}
	}
	defer func() {
		a.checked.save(a.ar.TmpDir(), a.logf)
		res.leftChats = map[string]bool{}
		for _, id := range order {
			it := items[id]
			res.Items = append(res.Items, *it)
		}
		for _, j := range jobs {
			for _, row := range j.want {
				if it := items[row.ID]; it == nil || it.Kind == "" {
					res.leftChats[j.c.JID()] = true
				}
			}
		}
		for _, it := range items {
			if it.Kind == "" {
				res.leftChats[it.Chat] = true
			}
		}
		res.Left = total - resolved
		if o.Chat == "" && !o.Again {
			a.status.setUncheckedLines(res.Left)
		}
	}()
	silent, sent := 0, 0 // silent: requests in a row the phone didn't reply to at all
	// Silence counts against a line only once a later request is answered:
	// the phone was awake, so it was silent about that chat, not locked.
	var unheard []refetchJob
	struck := map[string]bool{} // one strike a line per run
	strike := func(j refetchJob, rows []archive.Row) {
		var fresh []archive.Row
		for _, row := range rows {
			if it := items[row.ID]; !struck[row.ID] && (it == nil || it.Kind == "") {
				struck[row.ID] = true
				fresh = append(fresh, row)
			}
		}
		a.strike(j, fresh, settle)
	}
	for len(jobs) > 0 && (o.MaxRequests <= 0 || sent < o.MaxRequests) {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		j := jobs[0]
		jobs = jobs[1:]
		sent++
		res.last = j.folder
		got, reply, err := a.requestAround(ctx, j, o.Count, o.Wait)
		var missing []archive.Row
		for _, row := range j.want {
			if ans, ok := got[row.ID]; ok {
				settle(j, row, ans.kind, ans.filled)
			} else {
				missing = append(missing, row)
			}
		}
		rest := j
		rest.want = missing
		switch {
		case errors.Is(err, ErrNoAnswer):
			if len(missing) > 0 {
				unheard = append(unheard, rest)
				jobs = append(jobs, rest) // tried again last
			}
			// WhatsApp stops answering soon after the phone locks: stop
			// rather than mark lines unanswered one timeout at a time.
			if silent++; silent >= 3 || o.MaxRequests > 0 {
				return res, ErrPhoneQuiet
			}
			continue
		case err != nil:
			jobs = append([]refetchJob{rest}, jobs...) // still to do
			return res, err
		}
		silent = 0
		for _, u := range unheard {
			strike(u, u.want)
		}
		unheard = nil
		// What the reply reached back past, the phone doesn't have; older
		// lines (or ones in the same second as its oldest message) need the
		// history before the reply's oldest message.
		var older []archive.Row
		for _, row := range missing {
			if reply.n > 0 && row.TS > reply.oldest {
				settle(j, row, "not on phone", false)
			} else {
				older = append(older, row)
			}
		}
		switch {
		case len(older) == 0:
		case reply.last != nil && !j.seen[reply.last.ID]:
			next := refetchJob{c: j.c, folder: j.folder, anchor: *reply.last, want: older, seen: map[string]bool{reply.last.ID: true}}
			for id := range j.seen {
				next.seen[id] = true
			}
			jobs = append([]refetchJob{next}, jobs...)
		default:
			// An empty reply (the phone didn't know the anchor), or pages
			// going round in a circle.
			strike(j, older)
		}
	}
	return res, nil
}

// strike counts a request about rows that told nothing, and records them as
// "no answer" once that has happened noAnswerAfter times.
func (a *Archiver) strike(j refetchJob, rows []archive.Row, settle func(refetchJob, archive.Row, string, bool)) {
	for _, row := range rows {
		a.mu.Lock()
		a.refetchStrikes[row.ID]++
		n := a.refetchStrikes[row.ID]
		a.mu.Unlock()
		kind := ""
		if n >= noAnswerAfter {
			kind = "no answer"
		}
		settle(j, row, kind, false)
	}
}

// refetchJobs groups the empty lines not checked yet into requests: per
// chat in folder order, newest first, each covering the lines up to half a
// request's worth of messages before its anchor (the phone's history holds
// messages the archive doesn't, which push older lines out of the reply).
func (a *Archiver) refetchJobs(ctx context.Context, o RefetchOptions) ([]refetchJob, error) {
	var jobs []refetchJob
	for _, info := range a.ar.Chats() {
		if o.Chat != "" && info.JID != o.Chat || o.chats != nil && !o.chats[info.JID] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
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
			j := refetchJob{c: c, folder: info.Folder, anchor: anchor, seen: map[string]bool{anchor.ID: true}}
			for len(blanks) > 0 && blanks[len(blanks)-1] > last-max(o.Count/2, 1) {
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

// requestAround asks for the history before j.anchor, trying the chat's
// other address when the first is silent, and returns the answers for
// j.want and the reply. archiveHistory archives the messages and sends
// their answers before handing over the reply.
func (a *Archiver) requestAround(ctx context.Context, j refetchJob, count int, wait time.Duration) (map[string]refetchAnswer, *historyBatch, error) {
	chat, err := types.ParseJID(j.c.JID())
	if err != nil {
		return nil, nil, err
	}
	answers := make(chan refetchAnswer, len(j.want))
	a.mu.Lock()
	for _, row := range j.want {
		a.refetch[row.ID] = answers
	}
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		for _, row := range j.want {
			delete(a.refetch, row.ID)
		}
		a.refetchWait = nil
		a.mu.Unlock()
	}()
	got := map[string]refetchAnswer{}
	drain := func() {
		for {
			select {
			case ans := <-answers:
				got[ans.id] = ans
			default:
				return
			}
		}
	}
	sender, _ := types.ParseJID(j.anchor.Sender)
	for _, target := range a.chatJIDs(ctx, chat) {
		info := types.MessageInfo{
			MessageSource: types.MessageSource{Chat: target, Sender: sender, IsFromMe: j.anchor.FromMe, IsGroup: target.Server == types.GroupServer},
			ID:            j.anchor.ID,
			Timestamp:     time.Unix(j.anchor.TS, 0),
		}
		w := &refetchWaiter{chat: j.c.JID(), replies: make(chan historyBatch, 1)}
		_, err := a.requestHistory(ctx, info, count, func(id types.MessageID) {
			a.mu.Lock()
			w.reqID = string(id)
			a.refetchWait = w
			a.mu.Unlock()
		})
		if err != nil {
			return got, nil, err
		}
		select {
		case <-ctx.Done():
			return got, nil, ctx.Err()
		case reply := <-w.replies:
			drain()
			// Lines in the reply that weren't waited for when it was
			// archived (it came late, for an earlier request) are answered
			// from it too.
			for _, row := range j.want {
				if kind, ok := reply.kinds[row.ID]; ok && got[row.ID].kind == "" {
					e, found := j.c.Get(row.ID)
					got[row.ID] = refetchAnswer{id: row.ID, kind: kind, filled: found && !blank(e)}
				}
			}
			return got, &reply, nil
		case <-time.After(wait):
		}
		drain() // another request's reply may have brought some
	}
	return got, nil, ErrNoAnswer
}

// deliverRefetch hands an on-demand reply to the Refetch waiting for it:
// matched by the request ID the phone echoes or, when it doesn't, by chat
// unless a backfill of that chat is waiting too.
func (a *Archiver) deliverRefetch(session, chatKey string, batch historyBatch) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	w := a.refetchWait
	switch {
	case w == nil:
		return false
	case session != "":
		if session != w.reqID {
			return false
		}
	case chatKey != w.chat:
		return false
	default:
		if _, busy := a.backfill[chatKey]; busy {
			return false
		}
	}
	select {
	case w.replies <- batch:
	default:
	}
	return true
}

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
// sent or read there just now), when it answers requests at once and
// without asking the user to open it. Old events, delivered after a
// reconnect, aren't signs.
func (a *Archiver) phoneActive(at time.Time) {
	if time.Since(at) > 2*time.Minute {
		return
	}
	select {
	case a.phoneSeen <- struct{}{}:
	default:
	}
}

// RefetchInBackground has a running sync check the empty lines left from
// before (see Refetch) a few at a time while the phone is in use, so the
// backlog clears with no one keeping WhatsApp open for it. New empty lines
// don't need it: their type is recorded as they're archived.
func (a *Archiver) RefetchInBackground() { a.background(a.refetchLoop) }

func (a *Archiver) refetchLoop(ctx context.Context) {
	const perRound, pause, rescan = 5, 2 * time.Minute, time.Hour
	var backlog map[string]bool
	var scanned time.Time
	cursor := ""
	for {
		if backlog == nil || len(backlog) == 0 && time.Since(scanned) > rescan {
			// At start, and now and then for lines archived without a type
			// (a message with no content).
			jobs, err := a.refetchJobs(ctx, RefetchOptions{Count: 50})
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				a.logf("warning: finding empty lines: %v", err)
			}
			backlog, scanned = map[string]bool{}, time.Now()
			left := 0
			for _, j := range jobs {
				backlog[j.c.JID()] = true
				left += len(j.want)
			}
			a.status.setUncheckedLines(left)
		}
		select {
		case <-ctx.Done():
			return
		case <-a.phoneSeen:
		}
		if len(backlog) == 0 {
			continue
		}
		res, err := a.Refetch(ctx, RefetchOptions{MaxRequests: perRound, chats: backlog, cursor: cursor})
		if ctx.Err() != nil {
			return
		}
		if res.leftChats != nil { // nil: the round failed before asking
			cursor, backlog = res.last, res.leftChats
		}
		answered := 0
		for _, it := range res.Items {
			if it.Kind != "" {
				answered++
			}
		}
		if answered > 0 || err != nil && !errors.Is(err, ErrPhoneQuiet) {
			a.logf("refetch: %d empty lines checked, %d left (err: %v)", answered, res.Left, err)
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
// type is known (album, not on phone, a type wacli can't show), so they
// aren't asked about again.
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
