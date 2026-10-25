package wa

import (
	"testing"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	waStore "go.mau.fi/whatsmeow/store"
	"google.golang.org/protobuf/proto"
)

// Message kinds whose text the archive shows, parsed live and from history.
func TestContentKindsYieldText(t *testing.T) {
	invite := func(caption, name string) *waProto.Message {
		return &waProto.Message{GroupInviteMessage: &waE2E.GroupInviteMessage{Caption: proto.String(caption), GroupName: proto.String(name), GroupJID: proto.String("120363000001@g.us")}}
	}
	body := &waProto.Message{ExtendedTextMessage: &waProto.ExtendedTextMessage{Text: proto.String("searchable body")}}
	for _, tc := range []struct {
		name string
		m    *waProto.Message
		want string
	}{
		{"ephemeral", &waProto.Message{EphemeralMessage: &waE2E.FutureProofMessage{Message: body}}, "searchable body"},
		{"view once", &waProto.Message{ViewOnceMessageV2: &waE2E.FutureProofMessage{Message: body}}, "searchable body"},
		{"document with caption", &waProto.Message{DocumentWithCaptionMessage: &waE2E.FutureProofMessage{Message: body}}, "searchable body"},
		{"invite caption", invite("join us", "Readers"), "join us"},
		{"invite name", invite("", "Readers"), "Group invite: Readers"},
		{"associated child", &waProto.Message{AssociatedChildMessage: &waE2E.FutureProofMessage{Message: body}}, "searchable body"},
		{"group status mention", &waProto.Message{GroupStatusMentionMessage: &waE2E.FutureProofMessage{Message: body}}, "searchable body"},
		{"comment", &waProto.Message{CommentMessage: &waE2E.CommentMessage{TargetMessageKey: &waCommon.MessageKey{ID: proto.String("T")}, Message: &waProto.Message{Conversation: proto.String("a comment")}}}, "a comment"},
	} {
		for _, pm := range []ParsedMessage{ParseLiveMessage(liveEvent(tc.m)), ParseHistoryMessage("15555550123@s.whatsapp.net", &waProto.WebMessageInfo{Message: tc.m})} {
			if pm.Text != tc.want || pm.UnhandledPayload != "" {
				t.Errorf("%s: text=%q unhandled=%q", tc.name, pm.Text, pm.UnhandledPayload)
			}
		}
	}
	wrapped := &waProto.Message{AssociatedChildMessage: &waE2E.FutureProofMessage{Message: &waProto.Message{ImageMessage: &waProto.ImageMessage{Caption: proto.String("cap"), DirectPath: proto.String("/m")}}}}
	if pm := ParseLiveMessage(liveEvent(wrapped)); pm.Media == nil || pm.Media.DirectPath != "/m" {
		t.Errorf("wrapped media lost: %+v", pm.Media)
	}
}

func TestSetHistorySyncLimits(t *testing.T) {
	previous, previousFull := waStore.DeviceProps.HistorySyncConfig, waStore.DeviceProps.RequireFullSync
	t.Cleanup(func() {
		waStore.DeviceProps.HistorySyncConfig, waStore.DeviceProps.RequireFullSync = previous, previousFull
	})
	waStore.DeviceProps.HistorySyncConfig = nil
	SetHistorySyncLimits(HistorySyncLimits{Full: true})
	cfg := waStore.DeviceProps.GetHistorySyncConfig()
	if !waStore.DeviceProps.GetRequireFullSync() || cfg.FullSyncDaysLimit != nil || cfg.RecentSyncDaysLimit != nil {
		t.Fatalf("full, unbounded: %v", cfg)
	}
	waStore.DeviceProps.HistorySyncConfig = nil
	SetHistorySyncLimits(HistorySyncLimits{Full: true, Days: 3650})
	cfg = waStore.DeviceProps.GetHistorySyncConfig()
	if cfg.GetFullSyncDaysLimit() != 3650 || cfg.RecentSyncDaysLimit != nil {
		t.Fatalf("full with days: %v", cfg)
	}
}

func TestBusinessMessagesYieldText(t *testing.T) {
	for _, tc := range []struct {
		name    string
		m       *waProto.Message
		text    string
		buttons bool
	}{
		{"template", &waProto.Message{TemplateMessage: &waProto.TemplateMessage{HydratedTemplate: &waProto.TemplateMessage_HydratedFourRowTemplate{
			Title:               &waProto.TemplateMessage_HydratedFourRowTemplate_HydratedTitleText{HydratedTitleText: "Order"},
			HydratedContentText: proto.String("Shipped"), HydratedFooterText: proto.String("Shop"),
			HydratedButtons: []*waProto.HydratedTemplateButton{{}},
		}}}, "Order\nShipped\n[Shop]", true},
		{"buttons", &waProto.Message{ButtonsMessage: &waProto.ButtonsMessage{ContentText: proto.String("Pick one"), Buttons: []*waProto.ButtonsMessage_Button{{}}}}, "Pick one", true},
		{"interactive", &waProto.Message{InteractiveMessage: &waProto.InteractiveMessage{Body: &waProto.InteractiveMessage_Body{Text: proto.String("Hello")}}}, "Hello", false},
		{"list", &waProto.Message{ListMessage: &waProto.ListMessage{Title: proto.String("Menu"), ButtonText: proto.String("Open")}}, "Menu", true},
		{"list reply", &waProto.Message{ListResponseMessage: &waProto.ListResponseMessage{Title: proto.String("Pizza")}}, "Pizza", false},
		{"button reply", &waProto.Message{ButtonsResponseMessage: &waProto.ButtonsResponseMessage{Response: &waProto.ButtonsResponseMessage_SelectedDisplayText{SelectedDisplayText: "Yes"}}}, "Yes", false},
	} {
		pm := ParseLiveMessage(liveEvent(tc.m))
		if pm.Text != tc.text || pm.HasButtons != tc.buttons || !pm.HasContent() {
			t.Errorf("%s: text=%q buttons=%v", tc.name, pm.Text, pm.HasButtons)
		}
	}
}

func TestPollTrafficIsFlagged(t *testing.T) {
	vote := ParseLiveMessage(liveEvent(&waProto.Message{PollUpdateMessage: &waE2E.PollUpdateMessage{}}))
	add := ParseLiveMessage(liveEvent(&waProto.Message{PollAddOptionMessage: &waE2E.PollAddOptionMessage{}}))
	if !vote.IsPollVote || !add.IsPollAdd {
		t.Errorf("vote=%v add=%v", vote.IsPollVote, add.IsPollAdd)
	}
}
