package archiver

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ferdousbhai/wacli/internal/archive"
	"go.mau.fi/whatsmeow/types"
)

// nameChanged schedules a refresh of chats still showing numbers.
func (a *Archiver) nameChanged() {
	select {
	case a.namesChanged <- struct{}{}:
	default:
	}
}

// refreshNamesLoop runs RefreshNames once at start (for archives written
// before chats were flagged) and then whenever WhatsApp sends names, after a
// quiet moment so a burst of contact updates leads to one pass.
func (a *Archiver) refreshNamesLoop(ctx context.Context) {
	// Archives written before chats were flagged get one full pass.
	scanned := filepath.Join(a.ar.Root(), ".wacli", "names-scanned")
	_, err := os.Stat(scanned)
	a.RefreshNames(ctx, err != nil)
	if err != nil && ctx.Err() == nil {
		_ = os.WriteFile(scanned, nil, 0o600)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.namesChanged:
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(15 * time.Second):
		}
		a.RefreshNames(ctx, false)
	}
}

// RefreshNames renames chats and senders still shown as a number (or as
// "You") once WhatsApp has told us the real name; contacts and group subjects
// arrive after the first history batches on a new link. Only chats flagged
// NeedsNames are read, unless all is set.
func (a *Archiver) RefreshNames(ctx context.Context, all bool) {
	resolved := map[string]string{} // sender JID → name, for this pass
	for _, info := range a.ar.Chats() {
		if ctx.Err() != nil {
			return
		}
		jid, err := types.ParseJID(info.JID)
		if err != nil {
			continue
		}
		_, suffix := chatKind(jid)
		name := info.Name
		if IsFallbackName(name, info.JID) {
			if n := archive.CleanName(a.names.chat(ctx, jid, "")); !IsFallbackName(n, info.JID) {
				name = n
			}
		}
		// CleanName and the mark check also fix chats named before names
		// and folders were cleaned.
		name = archive.CleanName(name)
		if name != info.Name || strings.IndexFunc(info.Folder, archive.IsBidiMark) >= 0 {
			if folder, err := a.ar.Rename(info.JID, name, suffix); err != nil {
				a.logf("warning: rename %s: %v", info.Folder, err)
			} else if folder != info.Folder {
				a.logf("renamed %s → %s", info.Folder, folder)
			}
		}
		if !all && !info.NeedsNames {
			continue
		}
		a.mu.Lock()
		delete(a.namesWanted, info.JID)
		a.mu.Unlock()
		left := a.refreshSenders(ctx, info.JID, resolved)
		done := left == 0 && !IsFallbackName(name, info.JID)
		err = a.ar.UpdateInfo(info.JID, func(ci *archive.ChatInfo) {
			// Decided under the record's lock: a number-named message that
			// arrived during this pass (namesWanted) keeps the flag set.
			a.mu.Lock()
			rewanted := a.namesWanted[info.JID]
			a.mu.Unlock()
			ci.NeedsNames = !done || rewanted
		})
		if err != nil {
			a.logf("warning: %v", err)
		}
	}
}

// refreshSenders replaces senders written before their name was known and
// returns how many still have no name.
func (a *Archiver) refreshSenders(ctx context.Context, chatJID string, resolved map[string]string) (left int) {
	c, err := a.ar.Chat(chatJID)
	if err != nil {
		return 0
	}
	own := a.names.own()
	n, err := c.RenameSenders(func(row archive.Row, sender string) (string, bool) {
		if clean := archive.CleanName(sender); clean != sender {
			return clean, true
		}
		if row.FromMe {
			if sender != ownFallback {
				return "", false
			}
			if own == ownFallback {
				left++
				return "", false
			}
			return own, true
		}
		if row.Sender == "" || !IsFallbackName(sender, row.Sender) {
			return "", false
		}
		name, ok := resolved[row.Sender]
		if !ok {
			if jid, err := types.ParseJID(row.Sender); err == nil {
				name = a.names.contact(ctx, jid)
			}
			resolved[row.Sender] = name
		}
		if name == "" || IsFallbackName(strings.TrimPrefix(name, pushPrefix), row.Sender) {
			left++
			return "", false
		}
		return name, true
	})
	if err != nil {
		a.logf("warning: %v", err)
	} else if n > 0 {
		a.logf("named %d senders in %s", n, filepath.Base(c.Dir()))
	}
	return left
}
