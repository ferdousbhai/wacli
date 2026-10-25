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
	album := func(i, v uint32) *waProto.Message {
		return &waProto.Message{AlbumMessage: &waE2E.AlbumMessage{ExpectedImageCount: proto.Uint32(i), ExpectedVideoCount: proto.Uint32(v)}}
	}
	invite := func(caption, name string) *waProto.Message {
		return &waProto.Message{GroupInviteMessage: &waE2E.GroupInviteMessage{Caption: proto.String(caption), GroupName: proto.String(name), GroupJID: proto.String("120363000001@g.us")}}
	}
	body := &waProto.Message{ExtendedTextMessage: &waProto.ExtendedTextMessage{Text: proto.String("searchable body")}}
	for _, tc := range []struct {
		name string
		m    *waProto.Message
		want string
	}{
		{"album", album(2, 3), "[Album: 2 images, 3 videos]"},
		{"album empty", album(0, 0), "[Album]"},
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
