package wa

import (
	"testing"
	"time"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestParseHistoryMessageTextAndSender(t *testing.T) {
	h := &waProto.WebMessageInfo{
		Key: &waProto.MessageKey{
			ID:          proto.String("msgid"),
			FromMe:      proto.Bool(false),
			Participant: proto.String("sender@s.whatsapp.net"),
		},
		MessageTimestamp: proto.Uint64(uint64(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Unix())),
		Message:          &waProto.Message{Conversation: proto.String("hello")},
	}
	pm := ParseHistoryMessage("123@s.whatsapp.net", h)
	if pm.ID != "msgid" || pm.Text != "hello" {
		t.Fatalf("unexpected parsed msg: %+v", pm)
	}
	if pm.SenderJID != "sender@s.whatsapp.net" {
		t.Fatalf("unexpected sender: %q", pm.SenderJID)
	}
	if pm.Chat.String() != "123@s.whatsapp.net" {
		t.Fatalf("unexpected chat: %q", pm.Chat.String())
	}
}

func TestParseHistoryMessageDeviceSentUsesDestinationChat(t *testing.T) {
	h := &waProto.WebMessageInfo{
		Key: &waProto.MessageKey{
			ID:        proto.String("sent-from-phone"),
			FromMe:    proto.Bool(false),
			RemoteJID: proto.String("me@s.whatsapp.net"),
		},
		MessageTimestamp: proto.Uint64(uint64(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Unix())),
		Message: &waProto.Message{
			DeviceSentMessage: &waProto.DeviceSentMessage{
				DestinationJID: proto.String("15551234567@s.whatsapp.net"),
				Message:        &waProto.Message{Conversation: proto.String("sent elsewhere")},
			},
		},
	}

	pm := ParseHistoryMessage("me@s.whatsapp.net", h)
	if !pm.FromMe {
		t.Fatalf("FromMe = false, want true")
	}
	if pm.Chat.String() != "15551234567@s.whatsapp.net" {
		t.Fatalf("Chat = %q, want destination", pm.Chat.String())
	}
	if pm.ID != "sent-from-phone" || pm.Text != "sent elsewhere" {
		t.Fatalf("unexpected parsed message: %+v", pm)
	}
}

func TestParseLiveMessageDeviceSentMetaUsesDestinationChat(t *testing.T) {
	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:     types.NewJID("me", types.DefaultUserServer),
				Sender:   types.NewJID("me", types.DefaultUserServer),
				IsFromMe: false,
			},
			DeviceSentMeta: &types.DeviceSentMeta{
				DestinationJID: "15551234567@s.whatsapp.net",
			},
			ID:        "live-sent-from-phone",
			Timestamp: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		Message: &waProto.Message{Conversation: proto.String("live sent")},
	}

	pm := ParseLiveMessage(evt)
	if !pm.FromMe {
		t.Fatalf("FromMe = false, want true")
	}
	if pm.Chat.String() != "15551234567@s.whatsapp.net" {
		t.Fatalf("Chat = %q, want destination", pm.Chat.String())
	}
	if pm.Text != "live sent" {
		t.Fatalf("Text = %q", pm.Text)
	}
}

func TestParseHistoryMessageCallLog(t *testing.T) {
	outcome := waProto.CallLogMessage_CONNECTED
	callType := waProto.CallLogMessage_REGULAR
	h := &waProto.WebMessageInfo{
		Key: &waProto.MessageKey{
			ID:     proto.String("call-msg"),
			FromMe: proto.Bool(true),
		},
		MessageTimestamp: proto.Uint64(uint64(time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC).Unix())),
		Message: &waProto.Message{
			CallLogMesssage: &waProto.CallLogMessage{
				IsVideo:      proto.Bool(true),
				CallOutcome:  &outcome,
				DurationSecs: proto.Int64(125),
				CallType:     &callType,
				Participants: []*waProto.CallLogMessage_CallParticipant{{
					JID:         proto.String("456@s.whatsapp.net"),
					CallOutcome: &outcome,
				}},
			},
		},
	}

	pm := ParseHistoryMessage("456@s.whatsapp.net", h)
	if pm.Call == nil {
		t.Fatal("expected call log metadata")
	}
	if pm.Call.EventType != "call_log" || pm.Call.CallID != "call-msg" || pm.Call.MsgID != "call-msg" {
		t.Fatalf("unexpected call identity: %+v", pm.Call)
	}
	if pm.Call.Direction != "outbound" || pm.Call.Media != "video" || pm.Call.Outcome != "connected" || pm.Call.DurationSecs != 125 {
		t.Fatalf("unexpected call details: %+v", pm.Call)
	}
	if len(pm.Call.Participants) != 1 || pm.Call.Participants[0].JID != "456@s.whatsapp.net" {
		t.Fatalf("unexpected participants: %+v", pm.Call.Participants)
	}
}

func TestParseHistoryMessageTopLevelParticipant(t *testing.T) {
	groupJID := "120363001234567890@g.us"
	senderLID := "12345:67@lid"
	h := &waProto.WebMessageInfo{
		Key: &waProto.MessageKey{
			ID:        proto.String("msgid2"),
			FromMe:    proto.Bool(false),
			RemoteJID: proto.String(groupJID),
		},
		Participant:      proto.String(senderLID),
		MessageTimestamp: proto.Uint64(uint64(time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC).Unix())),
		Message:          &waProto.Message{Conversation: proto.String("from lid group")},
	}

	pm := ParseHistoryMessage(groupJID, h)
	if pm.SenderJID != senderLID {
		t.Fatalf("SenderJID = %q, want %q", pm.SenderJID, senderLID)
	}
}

func TestParseHistoryMessageKeyParticipantStillWorks(t *testing.T) {
	sender := "sender@s.whatsapp.net"
	h := &waProto.WebMessageInfo{
		Key: &waProto.MessageKey{
			ID:          proto.String("msgid3"),
			FromMe:      proto.Bool(false),
			RemoteJID:   proto.String("120363001234567890@g.us"),
			Participant: proto.String(sender),
		},
		MessageTimestamp: proto.Uint64(uint64(time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC).Unix())),
		Message:          &waProto.Message{Conversation: proto.String("from regular group")},
	}

	pm := ParseHistoryMessage("120363001234567890@g.us", h)
	if pm.SenderJID != sender {
		t.Fatalf("SenderJID = %q, want %q", pm.SenderJID, sender)
	}
}

func TestParseHistoryMessageUnwrapsProtocolEdit(t *testing.T) {
	groupJID := "120363001234567890@g.us"
	sender := "16048339070@s.whatsapp.net"
	h := &waProto.WebMessageInfo{
		Key: &waProto.MessageKey{
			ID:          proto.String("edit-event-id"),
			FromMe:      proto.Bool(false),
			RemoteJID:   proto.String(groupJID),
			Participant: proto.String(sender),
		},
		MessageTimestamp: proto.Uint64(uint64(time.Date(2026, 5, 15, 19, 30, 25, 0, time.UTC).Unix())),
		Message: &waProto.Message{
			ProtocolMessage: &waProto.ProtocolMessage{
				Type: waProto.ProtocolMessage_MESSAGE_EDIT.Enum(),
				Key: &waProto.MessageKey{
					ID:          proto.String("original-msg-id"),
					FromMe:      proto.Bool(false),
					RemoteJID:   proto.String(groupJID),
					Participant: proto.String(sender),
				},
				EditedMessage: &waProto.Message{Conversation: proto.String("edited body")},
			},
		},
	}

	pm := ParseHistoryMessage(groupJID, h)
	if pm.ID != "original-msg-id" {
		t.Fatalf("ID = %q, want original-msg-id", pm.ID)
	}
	if pm.Text != "edited body" {
		t.Fatalf("Text = %q, want edited body", pm.Text)
	}
	if !pm.Edited {
		t.Fatalf("Edited = false, want true")
	}
	if pm.SenderJID != sender {
		t.Fatalf("SenderJID = %q, want %q", pm.SenderJID, sender)
	}
	if pm.Chat.String() != groupJID {
		t.Fatalf("Chat = %q, want %q", pm.Chat.String(), groupJID)
	}
}

func TestParseHistoryMessageUnwrapsTopLevelEdit(t *testing.T) {
	groupJID := "120363001234567890@g.us"
	sender := "16048339070@s.whatsapp.net"
	h := &waProto.WebMessageInfo{
		Key: &waProto.MessageKey{
			ID:          proto.String("original-msg-id"),
			FromMe:      proto.Bool(false),
			RemoteJID:   proto.String(groupJID),
			Participant: proto.String(sender),
		},
		MessageTimestamp: proto.Uint64(uint64(time.Date(2026, 5, 15, 19, 37, 1, 0, time.UTC).Unix())),
		Message: &waProto.Message{
			EditedMessage: &waProto.FutureProofMessage{
				Message: &waProto.Message{Conversation: proto.String("top-level edited body")},
			},
		},
	}

	pm := ParseHistoryMessage(groupJID, h)
	if pm.ID != "original-msg-id" {
		t.Fatalf("ID = %q, want original-msg-id", pm.ID)
	}
	if pm.Text != "top-level edited body" {
		t.Fatalf("Text = %q, want top-level edited body", pm.Text)
	}
	if !pm.Edited {
		t.Fatalf("Edited = false, want true")
	}
	if pm.SenderJID != sender {
		t.Fatalf("SenderJID = %q, want %q", pm.SenderJID, sender)
	}
}

func TestParseLiveMessageImageClonesBytes(t *testing.T) {
	chat, _ := types.ParseJID("123@s.whatsapp.net")
	sender, _ := types.ParseJID("sender@s.whatsapp.net")

	key := []byte{1, 2, 3}
	img := &waProto.ImageMessage{
		Caption:       proto.String("cap"),
		Mimetype:      proto.String("image/jpeg"),
		DirectPath:    proto.String("/direct"),
		MediaKey:      key,
		FileSHA256:    []byte{4},
		FileEncSHA256: []byte{5},
		FileLength:    proto.Uint64(10),
	}
	ev := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:     chat,
				Sender:   sender,
				IsFromMe: false,
			},
			ID:        "mid",
			Timestamp: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
			PushName:  "Sender",
		},
		Message: &waProto.Message{ImageMessage: img},
	}

	pm := ParseLiveMessage(ev)
	if pm.ID != "mid" || pm.Media == nil || pm.Media.Type != "image" {
		t.Fatalf("unexpected parsed: %+v", pm)
	}
	if pm.Text != "cap" {
		t.Fatalf("expected text from caption, got %q", pm.Text)
	}

	// Ensure clone() was used (pm.Media.MediaKey should not alias key).
	key[0] = 9
	if pm.Media.MediaKey[0] == 9 {
		t.Fatalf("expected MediaKey to be cloned")
	}
}

func TestParseLiveMessageReaction(t *testing.T) {
	chat, _ := types.ParseJID("123@s.whatsapp.net")
	sender, _ := types.ParseJID("sender@s.whatsapp.net")

	ev := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:     chat,
				Sender:   sender,
				IsFromMe: false,
			},
			ID:        "mid",
			Timestamp: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
			PushName:  "Sender",
		},
		Message: &waProto.Message{
			ReactionMessage: &waProto.ReactionMessage{
				Text: proto.String("👍"),
				Key:  &waProto.MessageKey{ID: proto.String("orig")},
			},
		},
	}

	pm := ParseLiveMessage(ev)
	if pm.ReactionEmoji != "👍" || pm.ReactionToID != "orig" {
		t.Fatalf("unexpected reaction parse: %+v", pm)
	}
}

func TestParseContactMessageText(t *testing.T) {
	chat, _ := types.ParseJID("123@s.whatsapp.net")
	sender, _ := types.ParseJID("sender@s.whatsapp.net")

	ev := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:     chat,
				Sender:   sender,
				IsFromMe: false,
			},
			ID:        "contact1",
			Timestamp: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		Message: &waProto.Message{
			ContactMessage: &waProto.ContactMessage{
				DisplayName: proto.String("Ada Lovelace"),
				Vcard: proto.String("BEGIN:VCARD\nVERSION:3.0\nFN:Ada Lovelace\n" +
					"TEL;type=CELL;waid=441234567890:+44 1234 567890\nEND:VCARD"),
			},
		},
	}

	pm := ParseLiveMessage(ev)
	if pm.Text != "Contact: Ada Lovelace (+44 1234 567890)" {
		t.Fatalf("unexpected contact text: %q", pm.Text)
	}
}

func TestParseContactsArrayMessageText(t *testing.T) {
	chat, _ := types.ParseJID("123@s.whatsapp.net")
	sender, _ := types.ParseJID("sender@s.whatsapp.net")

	ev := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:     chat,
				Sender:   sender,
				IsFromMe: false,
			},
			ID:        "contacts1",
			Timestamp: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		Message: &waProto.Message{
			ContactsArrayMessage: &waProto.ContactsArrayMessage{
				DisplayName: proto.String("2 contacts"),
				Contacts: []*waProto.ContactMessage{
					{
						DisplayName: proto.String("Ada Lovelace"),
						Vcard:       proto.String("BEGIN:VCARD\nFN:Ada Lovelace\nTEL:+44 1234\nEND:VCARD"),
					},
					{
						DisplayName: proto.String("Grace Hopper"),
						Vcard:       proto.String("BEGIN:VCARD\nFN:Grace Hopper\nTEL:+1 555\nEND:VCARD"),
					},
				},
			},
		},
	}

	pm := ParseLiveMessage(ev)
	want := "Contacts:\nContact: Ada Lovelace (+44 1234)\nContact: Grace Hopper (+1 555)"
	if pm.Text != want {
		t.Fatalf("unexpected contacts text: %q", pm.Text)
	}
}

func TestParseButtonsResponseMessage(t *testing.T) {
	chat, _ := types.ParseJID("123@s.whatsapp.net")
	sender, _ := types.ParseJID("user@s.whatsapp.net")

	ev := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat: chat, Sender: sender, IsFromMe: true,
			},
			ID:        "btnresp1",
			Timestamp: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		Message: &waProto.Message{
			ButtonsResponseMessage: &waProto.ButtonsResponseMessage{
				Response: &waProto.ButtonsResponseMessage_SelectedDisplayText{
					SelectedDisplayText: "Option A",
				},
			},
		},
	}

	pm := ParseLiveMessage(ev)
	if pm.Text != "Option A" {
		t.Fatalf("unexpected buttons response text: %q", pm.Text)
	}
}

func TestParseInteractiveMessage(t *testing.T) {
	chat, _ := types.ParseJID("123@s.whatsapp.net")
	sender, _ := types.ParseJID("biz@s.whatsapp.net")

	ev := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat: chat, Sender: sender, IsFromMe: false,
			},
			ID:        "interactive1",
			Timestamp: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		Message: &waProto.Message{
			InteractiveMessage: &waProto.InteractiveMessage{
				Header: &waProto.InteractiveMessage_Header{
					Title:    proto.String("Welcome"),
					Subtitle: proto.String("sub"),
				},
				Body: &waProto.InteractiveMessage_Body{
					Text: proto.String("Browse our catalog"),
				},
				Footer: &waProto.InteractiveMessage_Footer{
					Text: proto.String("Terms apply"),
				},
			},
		},
	}

	pm := ParseLiveMessage(ev)
	if pm.Text != "Welcome\nBrowse our catalog\n[Terms apply]" {
		t.Fatalf("unexpected interactive text: %q", pm.Text)
	}
}

func TestParseListResponseMessage(t *testing.T) {
	chat, _ := types.ParseJID("123@s.whatsapp.net")
	sender, _ := types.ParseJID("user@s.whatsapp.net")

	ev := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat: chat, Sender: sender, IsFromMe: true,
			},
			ID:        "listresp1",
			Timestamp: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		Message: &waProto.Message{
			ListResponseMessage: &waProto.ListResponseMessage{
				Title: proto.String("Item B"),
			},
		},
	}

	pm := ParseLiveMessage(ev)
	if pm.Text != "Item B" {
		t.Fatalf("unexpected list response text: %q", pm.Text)
	}
}

func TestParseTemplateButtonReplyMessage(t *testing.T) {
	chat, _ := types.ParseJID("123@s.whatsapp.net")
	sender, _ := types.ParseJID("user@s.whatsapp.net")

	ev := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat: chat, Sender: sender, IsFromMe: true,
			},
			ID:        "tbreply1",
			Timestamp: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		Message: &waProto.Message{
			TemplateButtonReplyMessage: &waProto.TemplateButtonReplyMessage{
				SelectedDisplayText: proto.String("Book now"),
			},
		},
	}

	pm := ParseLiveMessage(ev)
	if pm.Text != "Book now" {
		t.Fatalf("unexpected template button reply text: %q", pm.Text)
	}
}

func TestParseLiveMessageRevokeTargetsOriginalID(t *testing.T) {
	chat := types.NewJID("15551234567", types.DefaultUserServer)
	ev := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:     chat,
				Sender:   chat,
				IsFromMe: true,
			},
			ID:        "revoke-event",
			Timestamp: time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC),
		},
		Message: &waProto.Message{
			ProtocolMessage: &waProto.ProtocolMessage{
				Type: waProto.ProtocolMessage_REVOKE.Enum(),
				Key: &waProto.MessageKey{
					ID:        proto.String("original"),
					FromMe:    proto.Bool(true),
					RemoteJID: proto.String(chat.String()),
				},
			},
		},
	}

	pm := ParseLiveMessage(ev)
	if !pm.Revoked || pm.ID != "original" || pm.Chat != chat || !pm.FromMe {
		t.Fatalf("unexpected revoked parse: %+v", pm)
	}
}

func TestParseLiveMessagePollCreationV3(t *testing.T) {
	chat, _ := types.ParseJID("15551112222@s.whatsapp.net")
	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: chat},
			ID:            "POLL-1",
			Timestamp:     time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC),
		},
		Message: &waProto.Message{
			PollCreationMessageV3: &waProto.PollCreationMessage{
				Name: proto.String("Pizza?"),
				Options: []*waProto.PollCreationMessage_Option{
					{OptionName: proto.String("Yes")},
					{OptionName: proto.String("No")},
					{OptionName: proto.String("Maybe")},
				},
				SelectableOptionsCount: proto.Uint32(2),
			},
		},
	}
	pm := ParseLiveMessage(evt)
	if pm.Poll == nil {
		t.Fatalf("expected Poll set, got nil; pm=%+v", pm)
	}
	if pm.Poll.Question != "Pizza?" {
		t.Fatalf("question = %q", pm.Poll.Question)
	}
	if got, want := pm.Poll.Options, []string{"Yes", "No", "Maybe"}; !equalStrings(got, want) {
		t.Fatalf("options = %v want %v", got, want)
	}
	if pm.Poll.SelectableCount != 2 {
		t.Fatalf("selectable = %d", pm.Poll.SelectableCount)
	}
	if pm.Text != "Poll: Pizza?" {
		t.Fatalf("text = %q", pm.Text)
	}
}

func TestParseLiveMessagePollCreationV1(t *testing.T) {
	chat, _ := types.ParseJID("15551112222@s.whatsapp.net")
	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: chat},
			ID:            "POLL-V1",
			Timestamp:     time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC),
		},
		Message: &waProto.Message{
			PollCreationMessage: &waProto.PollCreationMessage{
				Name:    proto.String("Hello?"),
				Options: []*waProto.PollCreationMessage_Option{{OptionName: proto.String("a")}, {OptionName: proto.String("b")}},
			},
		},
	}
	pm := ParseLiveMessage(evt)
	if pm.Poll == nil || pm.Poll.Question != "Hello?" {
		t.Fatalf("v1 poll not parsed: %+v", pm)
	}
}

func TestParseLiveMessagePollCreationV6(t *testing.T) {
	chat, _ := types.ParseJID("15551112222@s.whatsapp.net")
	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: chat},
			ID:            "POLL-V6",
			Timestamp:     time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC),
		},
		Message: &waProto.Message{
			PollCreationMessageV6: &waProto.PollCreationMessage{
				Name:    proto.String("V6?"),
				Options: []*waProto.PollCreationMessage_Option{{OptionName: proto.String("a")}, {OptionName: proto.String("b")}},
			},
		},
	}
	pm := ParseLiveMessage(evt)
	if pm.Poll == nil || pm.Poll.Question != "V6?" {
		t.Fatalf("v6 poll not parsed: %+v", pm)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
