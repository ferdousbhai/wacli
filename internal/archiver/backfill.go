package archiver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ferdousbhai/wacli/internal/archive"
	"go.mau.fi/whatsmeow/types"
)

type BackfillResult struct {
	Chat     string    `json:"chat"`
	Folder   string    `json:"folder"`
	Requests int       `json:"requests"`
	Added    int64     `json:"added"`
	Done     bool      `json:"done"`
	Oldest   time.Time `json:"oldest"` // oldest message in the chat folder
}

// Backfill asks the phone for older history of one chat, page by page. It
// walks back from the newest message, and each next request starts after the
// last message of the previous page in the phone's own order. That order is
// not always time order (a "You were added" line can sit among much newer
// messages), so jumping to the oldest timestamp would skip whole stretches.
func (a *Archiver) Backfill(ctx context.Context, chatJID string, count, maxRequests int, wait time.Duration) (res BackfillResult, err error) {
	c, err := a.ar.Chat(chatJID)
	if err != nil {
		return BackfillResult{}, err
	}
	info, _ := a.ar.Info(chatJID)
	res = BackfillResult{Chat: chatJID, Folder: info.Folder, Done: info.BackfillDone}
	if info.BackfillDone {
		return res, nil
	}
	cursor := info.Backfill
	if cursor == nil {
		cursor = newestAnchor(c.Rows())
	}
	if cursor == nil {
		return res, errors.New("chat has no message to start from")
	}
	a.mu.Lock()
	_, busy := a.backfill[chatJID]
	if !busy {
		a.backfill[chatJID] = nil // reserved; requestPage installs the channel
	}
	a.mu.Unlock()
	if busy {
		return res, fmt.Errorf("a backfill of %s is already running", info.Folder)
	}
	defer func() {
		a.mu.Lock()
		delete(a.backfill, chatJID)
		a.mu.Unlock()
	}()
	before := c.Len()
	defer func() {
		res.Added = int64(c.Len() - before)
		if rows := c.Rows(); len(rows) > 0 {
			res.Oldest = time.Unix(rows[0].TS, 0)
		}
	}()

	seen := map[string]bool{cursor.ID: true}
	for res.Requests < maxRequests {
		res.Requests++
		batch, err := a.requestPage(ctx, c, *cursor, count, wait)
		if err != nil {
			return res, err
		}
		if batch.last == nil || seen[batch.last.ID] {
			res.Done = true
			return res, a.ar.UpdateInfo(chatJID, func(ci *archive.ChatInfo) { ci.BackfillDone = true; ci.Backfill = cursor })
		}
		seen[batch.last.ID] = true
		cursor = batch.last
		if err := a.ar.UpdateInfo(chatJID, func(ci *archive.ChatInfo) { ci.Backfill = cursor }); err != nil {
			return res, err
		}
	}
	return res, nil
}

func newestAnchor(rows []archive.Row) *archive.Row {
	for i := len(rows) - 1; i >= 0; i-- {
		// wacli: and import: IDs are ours; the phone doesn't know them.
		if !strings.Contains(rows[i].ID, ":") {
			r := rows[i]
			return &r
		}
	}
	return nil
}

// requestPage sends one on-demand history request. Chats that moved to
// LID addressing only answer under one of their two JIDs, so both are tried.
func (a *Archiver) requestPage(ctx context.Context, c *archive.Chat, anchor archive.Row, count int, wait time.Duration) (historyBatch, error) {
	chat, err := types.ParseJID(c.JID())
	if err != nil {
		return historyBatch{}, err
	}
	candidates := []types.JID{chat}
	switch chat.Server {
	case types.DefaultUserServer:
		if lid := a.wa.ResolvePNToLID(ctx, chat); !lid.IsEmpty() && lid != chat {
			candidates = append(candidates, lid)
		}
	case types.HiddenUserServer:
		if pn := a.wa.ResolveLIDToPN(ctx, chat); !pn.IsEmpty() && pn != chat {
			candidates = append(candidates, pn)
		}
	}
	sender, _ := types.ParseJID(anchor.Sender)
	for _, target := range candidates {
		ch := make(chan historyBatch, 1)
		a.mu.Lock()
		a.backfill[c.JID()] = ch
		a.mu.Unlock()

		info := types.MessageInfo{
			MessageSource: types.MessageSource{Chat: target, Sender: sender, IsFromMe: anchor.FromMe, IsGroup: target.Server == types.GroupServer},
			ID:            anchor.ID,
			Timestamp:     time.Unix(anchor.TS, 0),
		}
		if _, err := a.wa.RequestHistorySyncOnDemand(ctx, info, count); err != nil {
			return historyBatch{}, err
		}
		select {
		case <-ctx.Done():
			return historyBatch{}, ctx.Err()
		case b := <-ch:
			return b, nil
		case <-time.After(wait):
			a.logf("no history answer for %s via %s", c.Name(), target)
		}
	}
	return historyBatch{}, fmt.Errorf("phone did not answer history request for %s (is it online?)", c.Name())
}

func (a *Archiver) deliverBackfill(chatID string, batch historyBatch) {
	jid, err := types.ParseJID(chatID)
	if err != nil {
		return
	}
	key := a.canonical(a.ctx, jid).String()
	a.mu.Lock()
	ch, running := a.backfill[key]
	if running {
		a.backfill[key] = nil // still reserved; each page is delivered once
	}
	a.mu.Unlock()
	if ch != nil {
		ch <- batch
	}
}
