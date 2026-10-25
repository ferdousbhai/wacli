package archiver

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/ferdousbhai/wacli/internal/archive"
	"github.com/ferdousbhai/wacli/internal/wa"
	"go.mau.fi/whatsmeow/types"
)

// names resolves the display names iOS puts in exports: the address-book
// name for contacts, the profile name for yourself, the subject for groups.
type names struct {
	wa *wa.Client

	mu       sync.Mutex
	push     map[string]string    // user → push name seen on messages
	groups   map[string]string    // group JID → subject
	groupErr map[string]time.Time // group JID → last failed lookup
}

func newNames(c *wa.Client) *names {
	return &names{wa: c, push: map[string]string{}, groups: map[string]string{}, groupErr: map[string]time.Time{}}
}

func (n *names) notePush(jid, name string) {
	name = strings.TrimSpace(name)
	if name == "" || jid == "" {
		return
	}
	n.mu.Lock()
	n.push[userOf(jid)] = name
	n.mu.Unlock()
}

func (n *names) noteGroup(jid, subject string) {
	if strings.TrimSpace(subject) == "" {
		return
	}
	n.mu.Lock()
	n.groups[jid] = subject
	n.mu.Unlock()
}

// ownFallback is written for your own messages until your profile name has
// synced to this link; RefreshNames replaces it later.
const ownFallback = "You"

// pushPrefix marks a profile name of someone not in your contacts, as iOS
// writes it ("~ Name", with a narrow no-break space).
const pushPrefix = "~\u202f"

func (n *names) own() string {
	if name := n.wa.OwnPushName(); name != "" {
		return name
	}
	return ownFallback
}

func (n *names) isOwn(jid types.JID) bool {
	u := jid.ToNonAD().String()
	return u != "" && (u == n.wa.LinkedJID() || u == n.wa.LinkedLID())
}

// contact returns the name a phone shows for jid.
func (n *names) contact(ctx context.Context, jid types.JID) string {
	if jid.IsEmpty() {
		return ""
	}
	if n.isOwn(jid) {
		return n.own()
	}
	pn := jid
	if jid.Server == types.HiddenUserServer {
		pn = n.wa.ResolveLIDToPN(ctx, jid)
	}
	for _, candidate := range []types.JID{pn, jid} {
		if candidate.IsEmpty() {
			continue
		}
		info, err := n.wa.GetContact(ctx, candidate.ToNonAD())
		if err != nil || !info.Found {
			continue
		}
		for _, s := range []string{info.FullName, info.FirstName, info.BusinessName} {
			if s = strings.TrimSpace(s); s != "" {
				return s
			}
		}
	}
	n.mu.Lock()
	push := n.push[pn.User]
	if push == "" {
		push = n.push[jid.User]
	}
	n.mu.Unlock()
	if push != "" {
		return pushPrefix + push
	}
	if pn.Server == types.DefaultUserServer {
		if pn.User == "0" {
			return "WhatsApp" // the service account
		}
		return "+" + pn.User
	}
	return jid.User
}

// IsFallbackName reports names made from a number or ID because no real name
// was known yet.
func IsFallbackName(name, jid string) bool {
	name = strings.TrimPrefix(strings.TrimSpace(strings.Map(func(r rune) rune {
		if archive.IsBidiMark(r) || r == ' ' || r == '-' || r == '\u2011' {
			return -1
		}
		return r
	}, name)), "+")
	if name == "" || name == userOf(jid) {
		return true
	}
	for _, r := range name {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func (n *names) group(ctx context.Context, jid types.JID) string {
	n.mu.Lock()
	name := n.groups[jid.String()]
	failed, ok := n.groupErr[jid.String()]
	n.mu.Unlock()
	if name != "" {
		return name
	}
	// Groups you left can't be looked up; don't ask again every pass.
	if ok && time.Since(failed) < 30*time.Minute || !n.wa.IsConnected() {
		return "" // offline isn't a failure: ask again once connected
	}
	info, err := n.wa.GetGroupInfo(ctx, jid)
	if err != nil || info == nil {
		n.mu.Lock()
		n.groupErr[jid.String()] = time.Now()
		n.mu.Unlock()
		return ""
	}
	n.noteGroup(jid.String(), info.Name)
	return info.Name
}

// chat names a chat the way the phone's chat list does.
func (n *names) chat(ctx context.Context, jid types.JID, hint string) string {
	if jid.Server == types.GroupServer {
		if hint != "" {
			n.noteGroup(jid.String(), hint)
			return hint
		}
		if name := n.group(ctx, jid); name != "" {
			return name
		}
		return jid.User
	}
	if name := n.contact(ctx, jid); name != "" {
		return strings.TrimPrefix(name, pushPrefix)
	}
	return hint
}

// mentions replaces "@<number>" in text with iOS-style "@⁨Name⁩".
func (n *names) mentions(ctx context.Context, text string, jids []string) string {
	for _, raw := range jids {
		jid, err := types.ParseJID(raw)
		if err != nil || jid.User == "" {
			continue
		}
		token := "@" + jid.User
		if !strings.Contains(text, token) {
			continue
		}
		name := strings.TrimPrefix(n.contact(ctx, jid), pushPrefix)
		text = strings.ReplaceAll(text, token, "@⁨"+name+"⁩")
	}
	return text
}

func userOf(jid string) string {
	user, _, _ := strings.Cut(jid, "@")
	user, _, _ = strings.Cut(user, ":")
	return user
}
