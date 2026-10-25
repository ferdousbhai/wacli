package archiver

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ferdousbhai/wacli/internal/archive"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

const e2eNotice = "Messages and calls are end-to-end encrypted. Only people in this chat can read, listen to, or share them."

// archiveStub renders the system messages iOS includes in exports. Stub types
// exports don't show are skipped.
func (a *Archiver) archiveStub(ctx context.Context, b *batch, chatID, hint string, web *waWeb.WebMessageInfo) {
	chatJID, err := types.ParseJID(chatID)
	if err != nil || skipChat(chatJID) {
		return
	}
	key := web.GetKey()
	actorJID := web.GetParticipant()
	if actorJID == "" {
		actorJID = key.GetParticipant()
	}
	params := web.GetMessageStubParameters()
	actor := func() string {
		if key.GetFromMe() {
			return "You"
		}
		if j, err := types.ParseJID(actorJID); err == nil && !j.IsEmpty() {
			if a.names.isOwn(j) {
				return "You"
			}
			return a.names.contact(ctx, j)
		}
		return ""
	}
	people := func() string {
		var out []string
		for _, p := range params {
			j, err := types.ParseJID(p)
			if err != nil {
				continue
			}
			if a.names.isOwn(j) {
				out = append(out, "you")
			} else {
				out = append(out, a.names.contact(ctx, j))
			}
		}
		return strings.Join(out, ", ")
	}
	first := func() string {
		if len(params) > 0 {
			return params[0]
		}
		return ""
	}

	var text string
	switch web.GetMessageStubType() {
	case waWeb.WebMessageInfo_E2E_ENCRYPTED:
		text = e2eNotice
	case waWeb.WebMessageInfo_GROUP_CREATE:
		text = fmt.Sprintf("%s created group “%s”", actor(), first())
	case waWeb.WebMessageInfo_GROUP_CHANGE_SUBJECT:
		text = fmt.Sprintf("%s changed the group name to “%s”", actor(), first())
	case waWeb.WebMessageInfo_GROUP_PARTICIPANT_ADD:
		if p := people(); p == "you" {
			text = "You were added"
		} else {
			text = fmt.Sprintf("%s added %s", actor(), p)
		}
	case waWeb.WebMessageInfo_GROUP_PARTICIPANT_INVITE:
		text = joinedByLink(people())
	case waWeb.WebMessageInfo_GROUP_CHANGE_ICON:
		text = fmt.Sprintf("%s changed this group's icon", actor())
	case waWeb.WebMessageInfo_GROUP_CHANGE_DESCRIPTION:
		text = fmt.Sprintf("%s changed the group description", actor())
	case waWeb.WebMessageInfo_GROUP_PARTICIPANT_REMOVE:
		text = fmt.Sprintf("%s removed %s", actor(), people())
	case waWeb.WebMessageInfo_GROUP_PARTICIPANT_LEAVE:
		text = fmt.Sprintf("%s left", people())
	case waWeb.WebMessageInfo_CALL_MISSED_VOICE:
		text = "Missed voice call, ‎Tap to call back"
	case waWeb.WebMessageInfo_CALL_MISSED_VIDEO:
		text = "Missed video call, ‎Tap to call back"
	default:
		return
	}
	ts := time.Unix(int64(web.GetMessageTimestamp()), 0)
	a.archiveSystem(ctx, b, chatJID, hint, key.GetID(), ts, key.GetFromMe(), actorJID, text)
}

// archiveSystem writes a system line, attributed to the chat as exports do.
// A group change seen live (made-up ID) and again later in history (real ID)
// is written once.
func (a *Archiver) archiveSystem(ctx context.Context, b *batch, chatJID types.JID, hint, id string, ts time.Time, fromMe bool, sender, text string) {
	if id == "" {
		return
	}
	c, err := a.chat(ctx, chatJID, hint)
	if err != nil {
		a.logf("warning: %v", err)
		return
	}
	b.use(c)
	e := archive.Entry{Time: ts, Sender: c.Name(), System: true, Text: text}
	if c.Has(id) || c.Find(e) {
		return
	}
	row := archive.Row{ID: id, TS: ts.Unix(), FromMe: fromMe, Sender: sender}
	_, inserted, err := c.Insert(row, func(int) archive.Entry { return e })
	if inserted {
		a.added.Add(1)
	}
	a.wrote(b, c, err)
}

// archiveGroupInfo records live group changes that exports show.
func (a *Archiver) archiveGroupInfo(ctx context.Context, v *events.GroupInfo) {
	actor := "Someone"
	if v.Sender != nil {
		if a.names.isOwn(*v.Sender) {
			actor = "You"
		} else {
			actor = a.names.contact(ctx, *v.Sender)
		}
	}
	id := func(kind string) string {
		// Group notifications carry no message ID; this one is stable for
		// the same change seen twice.
		return fmt.Sprintf("wacli:%s:%d", kind, v.Timestamp.Unix())
	}
	person := func(j types.JID) string {
		if a.names.isOwn(j) {
			return "you"
		}
		return a.names.contact(ctx, j)
	}
	if v.Name != nil {
		a.names.noteGroup(v.JID.String(), v.Name.Name)
		a.nameChanged()
		a.archiveSystem(ctx, nil, v.JID, v.Name.Name, id("subject"), v.Timestamp, actor == "You", "",
			fmt.Sprintf("%s changed the group name to “%s”", actor, v.Name.Name))
	}
	if v.Topic != nil {
		a.archiveSystem(ctx, nil, v.JID, "", id("topic"), v.Timestamp, actor == "You", "",
			fmt.Sprintf("%s changed the group description", actor))
	}
	for _, j := range v.Join {
		text := fmt.Sprintf("%s added %s", actor, person(j))
		switch {
		case v.JoinReason == "invite":
			text = joinedByLink(person(j))
		case a.names.isOwn(j):
			text = "You were added"
		}
		a.archiveSystem(ctx, nil, v.JID, "", id("join:"+j.User), v.Timestamp, false, "", text)
	}
	for _, j := range v.Leave { // whatsmeow puts removals here too
		text := fmt.Sprintf("%s left", capitalize(person(j)))
		if v.Sender != nil && v.Sender.User != j.User {
			text = fmt.Sprintf("%s removed %s", actor, person(j))
		}
		a.archiveSystem(ctx, nil, v.JID, "", id("leave:"+j.User), v.Timestamp, false, "", text)
	}
}

// joinedByLink is the line for someone joining through the invite link.
func joinedByLink(who string) string {
	return capitalize(who) + " joined using this group's invite link"
}

// capitalize turns "you" into "You" at the start of a line.
func capitalize(who string) string {
	if who == "you" {
		return "You"
	}
	return who
}
