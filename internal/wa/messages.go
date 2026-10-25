package wa

import (
	"sort"
	"strings"
	"time"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/reflect/protoreflect"
)

type Media struct {
	Type          string
	Caption       string
	Filename      string
	DirectPath    string
	MediaKey      []byte
	FileSHA256    []byte
	FileEncSHA256 []byte
	FileLength    uint64
	PageCount     uint32
}

type Location struct {
	Latitude  float64
	Longitude float64
	Name      string
	Address   string
	IsLive    bool
}

// Poll captures the question + option list extracted from an incoming
// PollCreationMessage (any of V1/V2/V3/V4/V5/V6).
type Poll struct {
	Question string
	Options  []string
}

type ParsedMessage struct {
	Chat          types.JID
	ID            string
	SenderJID     string
	Timestamp     time.Time
	FromMe        bool
	Text          string
	HasButtons    bool // business message with buttons or a list
	Media         *Media
	Location      *Location
	Poll          *Poll
	IsPollVote    bool   // a vote on a poll, not shown in exports
	IsPollAdd     bool   // an option added to a poll, not shown in exports
	IsAlbum       bool   // an album header: shown as an empty message
	VCard         *VCard // a shared contact card
	PushName      string
	MentionedJIDs []string
	ReactionToID  string
	ReactionEmoji string
	Edited        bool
	Revoked       bool
	TargetSender  string // an edit or delete of someone else's message: its sender
	AdminRevoke   bool   // deleted by a group admin, not its sender (history)
	Call          *ParsedCallEvent
	// UnhandledPayload names the populated waE2E.Message field when parsing
	// extracted no content at all. Empty when the message was understood.
	UnhandledPayload string
}

func ParseLiveMessage(evt *events.Message) ParsedMessage {
	msg := ParsedMessage{
		Chat: evt.Info.Chat,
		ID:   evt.Info.ID,
		// UTC, matching ParseHistoryMessage, so parsed messages don't depend
		// on the zone whatsmeow happened to use. The instant is unchanged.
		Timestamp: evt.Info.Timestamp.UTC(),
		FromMe:    evt.Info.IsFromMe,
		PushName:  evt.Info.PushName,
	}
	if s := evt.Info.Sender.String(); s != "" {
		msg.SenderJID = s
	}
	if evt.Info.DeviceSentMeta != nil {
		applyDeviceSentDestination(evt.Info.DeviceSentMeta.DestinationJID, &msg)
	}

	markUnhandledPayload(extractWAProto(evt.Message, &msg), &msg)
	return msg
}

func ParseHistoryMessage(chatJID string, hist *waProto.WebMessageInfo) ParsedMessage {
	var chat types.JID
	if parsed, err := types.ParseJID(chatJID); err == nil {
		chat = parsed
	}

	pm := ParsedMessage{
		Chat:      chat,
		ID:        hist.GetKey().GetID(),
		Timestamp: time.Unix(int64(hist.GetMessageTimestamp()), 0).UTC(),
		FromMe:    hist.GetKey().GetFromMe(),
	}

	sender := strings.TrimSpace(hist.GetParticipant())
	if sender == "" {
		sender = strings.TrimSpace(hist.GetKey().GetParticipant())
	}
	if sender == "" {
		sender = strings.TrimSpace(hist.GetKey().GetRemoteJID())
	}
	pm.SenderJID = sender

	if hist.GetMessage() != nil {
		markUnhandledPayload(extractWAProto(hist.GetMessage(), &pm), &pm)
	}
	return pm
}

// HasContent reports whether parsing produced anything to archive. A message
// without content becomes an empty entry, like a message type iOS can't show.
func (pm ParsedMessage) HasContent() bool {
	return strings.TrimSpace(pm.Text) != "" ||
		pm.Media != nil ||
		pm.Poll != nil ||
		pm.IsPollVote ||
		pm.IsPollAdd ||
		pm.IsAlbum ||
		pm.Call != nil ||
		pm.Location != nil ||
		pm.Revoked ||
		pm.HasButtons ||
		strings.TrimSpace(pm.ReactionToID) != "" ||
		strings.TrimSpace(pm.ReactionEmoji) != ""
}

// markUnhandledPayload records which payload field was present when extraction
// yielded nothing, so an empty entry can be diagnosed instead of silently
// discarding content. Field names come from the protobuf descriptor, so new
// WhatsApp message types are reported without needing a code change here.
func markUnhandledPayload(m *waProto.Message, pm *ParsedMessage) {
	if m == nil || pm == nil || pm.HasContent() {
		return
	}
	var names []string
	m.ProtoReflect().Range(func(fd protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
		switch fd.Name() {
		case "messageContextInfo":
			return true
		}
		names = append(names, string(fd.Name()))
		return true
	})
	if len(names) == 0 {
		return
	}
	sort.Strings(names)
	pm.UnhandledPayload = strings.Join(names, ",")
}

// extractWAProto returns the leaf message it ultimately extracted from, after
// unwrapping any device-sent, edited or protocol-edit wrappers. Callers use it
// to describe an unhandled payload: naming the wrapper instead of the leaf
// would report `protocolMessage` for every unhandled edit, which is exactly the
// case the diagnostic exists for.
func extractWAProto(m *waProto.Message, pm *ParsedMessage) *waProto.Message {
	if m == nil || pm == nil {
		return nil
	}

	if deviceSent := m.GetDeviceSentMessage(); deviceSent.GetMessage() != nil {
		applyDeviceSentDestination(deviceSent.GetDestinationJID(), pm)
		return extractWAProto(deviceSent.GetMessage(), pm)
	}
	if edited := m.GetEditedMessage().GetMessage(); edited != nil {
		if edited.MessageContextInfo == nil && m.MessageContextInfo != nil {
			edited.MessageContextInfo = m.MessageContextInfo
		}
		pm.Edited = true
		return extractWAProto(edited, pm)
	}
	if replacement, handled := extractProtocolMutation(m, pm); handled {
		if replacement != nil {
			return extractWAProto(replacement, pm)
		}
		return m
	}
	if inner := contentWrapper(m); inner != nil {
		return extractWAProto(inner, pm)
	}
	if comment := m.GetCommentMessage(); comment.GetMessage() != nil {
		return extractWAProto(comment.GetMessage(), pm)
	}
	extractReaction(m, pm)
	extractPlainText(m, pm)
	extractMedia(m, pm)
	extractLocation(m, pm)
	extractContactText(m, pm)
	extractBusinessText(m, pm)
	extractPoll(m, pm)
	extractPollAddOption(m, pm)
	extractPollUpdate(m, pm)
	extractCallLog(m, pm)
	extractAlbum(m, pm)

	if ctx := contextInfoForMessage(m); ctx != nil {
		pm.MentionedJIDs = append([]string(nil), ctx.GetMentionedJID()...)
	}
	return m
}

// applyDeviceSentDestination points one of your own messages, relayed from
// your phone, at the chat it was sent to. Anyone can wrap a message this
// way, so it is honoured only for messages from your own account.
func applyDeviceSentDestination(destination string, pm *ParsedMessage) {
	if pm == nil || !pm.FromMe {
		return
	}
	if chat, err := types.ParseJID(strings.TrimSpace(destination)); err == nil && !chat.IsEmpty() {
		pm.Chat = chat
	}
}

func extractProtocolMutation(m *waProto.Message, pm *ParsedMessage) (*waProto.Message, bool) {
	protocol := m.GetProtocolMessage()
	if protocol == nil {
		return nil, false
	}
	switch protocol.GetType() {
	case waProto.ProtocolMessage_MESSAGE_EDIT:
		applyProtocolKey(protocol.GetKey(), pm)
		pm.Edited = true
		return protocol.GetEditedMessage(), true
	case waProto.ProtocolMessage_REVOKE:
		key := protocol.GetKey()
		if key == nil {
			return nil, false
		}
		applyProtocolKey(key, pm)
		pm.Text = ""
		pm.Media = nil
		pm.Revoked = true
		return nil, true
	default:
		return nil, false
	}
}

// applyProtocolKey points an edit or revoke at the message it changes. The
// key is written from the sender's side (in a DM RemoteJID is the recipient,
// and FromMe means "the sender's own message"), so only its ID is used; the
// chat and sender come from the envelope.
func applyProtocolKey(key *waProto.MessageKey, pm *ParsedMessage) {
	if key == nil || pm == nil {
		return
	}
	if id := strings.TrimSpace(key.GetID()); id != "" {
		pm.ID = id
	}
	if !key.GetFromMe() {
		// Someone else's message (an admin deleting it): its sender.
		pm.TargetSender = strings.TrimSpace(key.GetParticipant())
	}
}

func extractReaction(m *waProto.Message, pm *ParsedMessage) {
	if reaction := m.GetReactionMessage(); reaction != nil {
		pm.ReactionEmoji = reaction.GetText()
		if key := reaction.GetKey(); key != nil {
			pm.ReactionToID = key.GetID()
		}
	} else if encReaction := m.GetEncReactionMessage(); encReaction != nil {
		if key := encReaction.GetTargetMessageKey(); key != nil {
			pm.ReactionToID = key.GetID()
		}
	}
}

func extractPoll(m *waProto.Message, pm *ParsedMessage) {
	creation := pickPollCreation(m)
	if creation == nil {
		return
	}
	question := strings.TrimSpace(creation.GetName())
	options := make([]string, 0, len(creation.GetOptions()))
	for _, opt := range creation.GetOptions() {
		options = append(options, opt.GetOptionName())
	}
	pm.Poll = &Poll{
		Question: question,
		Options:  options,
	}
	if pm.Text == "" && question != "" {
		pm.Text = "Poll: " + question
	}
}

// pickPollCreation returns the inner PollCreationMessage from any of the
// known V1/V2/V3/V4/V5/V6 fields, including FutureProofMessage wrappers.
func pickPollCreation(m *waProto.Message) *waProto.PollCreationMessage {
	if m == nil {
		return nil
	}
	if c := m.GetPollCreationMessage(); c != nil {
		return c
	}
	if c := m.GetPollCreationMessageV2(); c != nil {
		return c
	}
	if c := m.GetPollCreationMessageV3(); c != nil {
		return c
	}
	if c := m.GetPollCreationMessageV5(); c != nil {
		return c
	}
	if c := m.GetPollCreationMessageV6(); c != nil {
		return c
	}
	if fp := m.GetPollCreationMessageV4(); fp != nil {
		if inner := fp.GetMessage(); inner != nil {
			if c := inner.GetPollCreationMessage(); c != nil {
				return c
			}
			if c := inner.GetPollCreationMessageV2(); c != nil {
				return c
			}
			if c := inner.GetPollCreationMessageV3(); c != nil {
				return c
			}
			if c := inner.GetPollCreationMessageV5(); c != nil {
				return c
			}
			if c := inner.GetPollCreationMessageV6(); c != nil {
				return c
			}
		}
	}
	return nil
}

func extractPollUpdate(m *waProto.Message, pm *ParsedMessage) {
	if m.GetPollUpdateMessage() != nil {
		pm.IsPollVote = true
	}
}

func extractPollAddOption(m *waProto.Message, pm *ParsedMessage) {
	secret := m.GetSecretEncryptedMessage()
	if m.GetPollAddOptionMessage() != nil || secret.GetSecretEncType() == waE2E.SecretEncryptedMessage_POLL_ADD_OPTION && secret != nil {
		pm.IsPollAdd = true
	}
}

func extractPlainText(m *waProto.Message, pm *ParsedMessage) {
	switch {
	case m.GetConversation() != "":
		pm.Text = m.GetConversation()
	case m.GetExtendedTextMessage() != nil:
		pm.Text = m.GetExtendedTextMessage().GetText()
	case m.GetGroupInviteMessage() != nil:
		pm.Text = groupInviteText(m.GetGroupInviteMessage())
	}
}

func clone(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out
}

func extractAlbum(m *waProto.Message, pm *ParsedMessage) {
	album := m.GetAlbumMessage()
	if album == nil {
		return
	}
	pm.IsAlbum = true // its photos and videos follow as messages of their own
}

// VCard is a shared contact: exports attach it as a .vcf file.
type VCard struct {
	Name string
	Data string
}

// These envelopes retain the outer identity while carrying ordinary content.
// whatsmeow unwraps some of them for live messages, but not in history
// syncs or inside edits.
func contentWrapper(m *waProto.Message) *waProto.Message {
	for _, w := range []*waE2E.FutureProofMessage{
		m.GetAssociatedChildMessage(),
		m.GetGroupStatusMentionMessage(),
		m.GetEphemeralMessage(),
		m.GetViewOnceMessage(),
		m.GetViewOnceMessageV2(),
		m.GetViewOnceMessageV2Extension(),
		m.GetDocumentWithCaptionMessage(),
		m.GetBotInvokeMessage(),
		m.GetLottieStickerMessage(),
		m.GetSpoilerMessage(),
		m.GetBotForwardedMessage(),
		m.GetAudioStickerMessage(),
	} {
		if inner := w.GetMessage(); inner != nil {
			return inner
		}
	}
	return nil
}

func groupInviteText(invite *waE2E.GroupInviteMessage) string {
	if caption := invite.GetCaption(); strings.TrimSpace(caption) != "" {
		return caption
	}
	if name := strings.TrimSpace(invite.GetGroupName()); name != "" {
		return "Group invite: " + name
	}
	return "[Group invite]"
}
