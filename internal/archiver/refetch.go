package archiver

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/ferdousbhai/wacli/internal/archive"
	"github.com/ferdousbhai/wacli/internal/wa"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
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

type refetchAnswer struct {
	id, kind string
	filled   bool
}

// Refetch asks the phone to resend the messages archived as empty lines (in
// one chat, or all when chatJID is ""), fills in what wacli can show, and
// reports each message's type. Albums are empty lines in exports too; asking
// for them confirms what they are.
func (a *Archiver) Refetch(ctx context.Context, chatJID string, perRequest int, wait time.Duration) ([]RefetchItem, error) {
	var items []RefetchItem
	var keys []wa.MessageKey
	for _, info := range a.ar.Chats() {
		if chatJID != "" && info.JID != chatJID {
			continue
		}
		c, err := a.ar.Chat(info.JID)
		if err != nil {
			return nil, err
		}
		chat, err := types.ParseJID(info.JID)
		if err != nil {
			continue
		}
		for _, row := range c.Rows() {
			if strings.Contains(row.ID, ":") { // wacli's own IDs: the phone doesn't know them
				continue
			}
			if e, ok := c.Get(row.ID); !ok || !blank(e) {
				continue
			}
			sender, _ := types.ParseJID(row.Sender)
			items = append(items, RefetchItem{Folder: info.Folder, Chat: info.JID, ID: row.ID, Time: time.Unix(row.TS, 0)})
			for _, jid := range a.chatJIDs(ctx, chat) {
				keys = append(keys, wa.MessageKey{Chat: jid, Sender: sender, ID: row.ID, FromMe: row.FromMe})
			}
		}
	}
	if perRequest <= 0 {
		perRequest = 20
	}
	answers := map[string]refetchAnswer{}
	for start := 0; start < len(keys); start += perRequest {
		chunk := keys[start:min(start+perRequest, len(keys))]
		got, err := a.requestResend(ctx, chunk, wait)
		for id, ans := range got {
			answers[id] = ans
		}
		if err != nil {
			return fill(items, answers), err
		}
	}
	return fill(items, answers), nil
}

func fill(items []RefetchItem, answers map[string]refetchAnswer) []RefetchItem {
	for i := range items {
		if ans, ok := answers[items[i].ID]; ok {
			items[i].Kind, items[i].Filled = ans.kind, ans.filled
		}
	}
	return items
}

// chatJIDs is jid and, for a person, their other address: the phone keeps a
// chat under only one of the two.
func (a *Archiver) chatJIDs(ctx context.Context, jid types.JID) []types.JID {
	var alt types.JID
	switch jid.Server {
	case types.DefaultUserServer:
		alt = a.wa.ResolvePNToLID(ctx, jid)
	case types.HiddenUserServer:
		alt = a.wa.ResolveLIDToPN(ctx, jid)
	}
	if alt.IsEmpty() || alt == jid {
		return []types.JID{jid}
	}
	return []types.JID{jid, alt}
}

// requestResend sends one request and collects answers until every message
// in it has one or the phone has been quiet for wait.
func (a *Archiver) requestResend(ctx context.Context, keys []wa.MessageKey, wait time.Duration) (map[string]refetchAnswer, error) {
	want := map[string]bool{}
	for _, k := range keys {
		want[k.ID] = true
	}
	ch := make(chan refetchAnswer, len(keys))
	var reqID types.MessageID
	defer func() {
		a.mu.Lock()
		delete(a.refetch, reqID)
		a.mu.Unlock()
	}()
	err := a.wa.RequestResend(ctx, keys, func(id types.MessageID) {
		reqID = id
		a.mu.Lock()
		a.refetch[id] = ch
		a.mu.Unlock()
	})
	got := map[string]refetchAnswer{}
	if err != nil {
		return got, err
	}
	for len(got) < len(want) {
		select {
		case <-ctx.Done():
			return got, ctx.Err()
		case ans := <-ch:
			if want[ans.id] {
				got[ans.id] = ans
			}
		case <-time.After(wait):
			if len(got) == 0 {
				return got, ErrNoResend
			}
			return got, nil
		}
	}
	return got, nil
}

// ErrNoResend is a resend request the phone didn't answer at all.
var ErrNoResend = errors.New("phone did not answer the resend request (is it online?)")

// refetched archives the phone's answer to a Refetch and reports it. It
// returns false for answers to requests wacli didn't make here.
func (a *Archiver) refetched(ctx context.Context, v *events.Message) bool {
	a.mu.Lock()
	ch, ok := a.refetch[v.UnavailableRequestID]
	a.mu.Unlock()
	if !ok {
		return false
	}
	web := v.SourceWebMsg
	pm := wa.ParseLiveMessage(v)
	kind := describe(pm)
	switch t := web.GetMessageStubType(); {
	case t == waWeb.WebMessageInfo_REVOKE || t == waWeb.WebMessageInfo_ADMIN_REVOKE:
		kind = "deleted"
		a.archiveWeb(ctx, nil, v.Info.Chat.String(), "", web)
	case t != 0:
		kind = "notice: " + strings.ToLower(t.String()) // nothing to fill in
	default:
		a.archiveMessage(ctx, nil, pm, "")
	}
	filled := false
	if c, err := a.knownChat(ctx, v.Info.Chat); err == nil && c != nil {
		e, ok := c.Get(v.Info.ID)
		filled = ok && !blank(e)
	}
	a.logf("resent %s in %s: %s (filled: %v)", v.Info.ID, v.Info.Chat, kind, filled)
	select {
	case ch <- refetchAnswer{id: v.Info.ID, kind: kind, filled: filled}:
	default:
	}
	return true
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
