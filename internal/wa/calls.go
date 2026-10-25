package wa

import (
	"strings"
	"time"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
)

type ParsedCallParticipant struct {
	JID     string
	Outcome string
}

type ParsedCallEvent struct {
	Chat         types.JID
	SenderJID    string
	CallID       string
	MsgID        string
	EventType    string
	Direction    string
	Media        string
	Outcome      string
	Reason       string
	CallType     string
	DurationSecs int64
	Timestamp    time.Time
	Participants []ParsedCallParticipant
}

type ParsedCallDelete struct {
	Chat      types.JID
	Direction string
}

func extractCallLog(m *waProto.Message, pm *ParsedMessage) {
	call := m.GetCallLogMesssage()
	if call == nil {
		return
	}
	pm.Call = &ParsedCallEvent{
		Chat:         pm.Chat,
		SenderJID:    pm.SenderJID,
		CallID:       pm.ID,
		MsgID:        pm.ID,
		EventType:    "call_log",
		Direction:    directionFromFromMe(pm.FromMe),
		Media:        audioVideo(call.GetIsVideo()),
		Outcome:      callLogMessageOutcome(call.GetCallOutcome()),
		CallType:     callLogMessageType(call.GetCallType()),
		DurationSecs: call.GetDurationSecs(),
		Timestamp:    pm.Timestamp,
		Participants: callLogMessageParticipants(call.GetParticipants()),
	}
}

func directionFromFromMe(fromMe bool) string {
	if fromMe {
		return "outbound"
	}
	return "inbound"
}

func audioVideo(isVideo bool) string {
	if isVideo {
		return "video"
	}
	return "audio"
}

func cleanCallValue(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.ReplaceAll(s, "-", "_")
	s = strings.ReplaceAll(s, " ", "_")
	return s
}

func callLogMessageOutcome(v waProto.CallLogMessage_CallOutcome) string {
	return cleanCallValue(v.String())
}

func callLogMessageType(v waProto.CallLogMessage_CallType) string {
	return cleanCallValue(v.String())
}

func callLogMessageParticipants(participants []*waProto.CallLogMessage_CallParticipant) []ParsedCallParticipant {
	out := make([]ParsedCallParticipant, 0, len(participants))
	for _, p := range participants {
		if p == nil || strings.TrimSpace(p.GetJID()) == "" {
			continue
		}
		out = append(out, ParsedCallParticipant{
			JID:     strings.TrimSpace(p.GetJID()),
			Outcome: callLogMessageOutcome(p.GetCallOutcome()),
		})
	}
	return out
}
