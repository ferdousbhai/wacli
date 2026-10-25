package wa

import (
	"testing"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waE2E"
)

func TestCommentKeepsUnhandledLeafDiagnostic(t *testing.T) {
	m := &waProto.Message{CommentMessage: &waE2E.CommentMessage{Message: &waProto.Message{StickerSyncRmrMessage: &waProto.StickerSyncRMRMessage{Filehash: []string{"synthetic"}}}}}
	if got := ParseLiveMessage(liveEvent(m)).UnhandledPayload; got != "stickerSyncRmrMessage" {
		t.Fatalf("unhandled leaf = %q", got)
	}
	if got := ParseLiveMessage(liveEvent(&waProto.Message{CommentMessage: &waE2E.CommentMessage{}})).UnhandledPayload; got != "commentMessage" {
		t.Fatalf("empty comment diagnostic = %q", got)
	}
}
