package wa

import (
	"strings"

	waProto "go.mau.fi/whatsmeow/binary/proto"
)

// extractBusinessText gives business messages (templates, buttons, lists,
// interactive messages and the replies to them) the text a phone shows.
func extractBusinessText(m *waProto.Message, pm *ParsedMessage) {
	setText := func(parts ...string) {
		if pm.Text == "" {
			pm.Text = joinNonEmpty(parts...)
		}
	}
	if tmpl := m.GetTemplateMessage(); tmpl != nil {
		if h := hydratedTemplate(tmpl); h != nil {
			setText(h.GetHydratedTitleText(), h.GetHydratedContentText(), footer(h.GetHydratedFooterText()))
			pm.HasButtons = pm.HasButtons || len(h.GetHydratedButtons()) > 0
		} else if im := tmpl.GetInteractiveMessageTemplate(); im != nil {
			setText(interactiveText(im))
			pm.HasButtons = pm.HasButtons || len(im.GetNativeFlowMessage().GetButtons()) > 0
		}
	}
	if btn := m.GetButtonsMessage(); btn != nil {
		setText(btn.GetText(), btn.GetContentText(), footer(btn.GetFooterText()))
		pm.HasButtons = pm.HasButtons || len(btn.GetButtons()) > 0
	}
	if resp := m.GetButtonsResponseMessage(); resp != nil {
		setText(resp.GetSelectedDisplayText())
	}
	if im := m.GetInteractiveMessage(); im != nil {
		setText(interactiveText(im))
		pm.HasButtons = pm.HasButtons || len(im.GetNativeFlowMessage().GetButtons()) > 0
	}
	if resp := m.GetInteractiveResponseMessage(); resp != nil {
		setText(resp.GetBody().GetText())
	}
	if list := m.GetListMessage(); list != nil {
		setText(list.GetTitle(), list.GetDescription())
		pm.HasButtons = pm.HasButtons || list.GetButtonText() != "" || len(list.GetSections()) > 0
	}
	if lr := m.GetListResponseMessage(); lr != nil {
		setText(lr.GetTitle())
		setText(lr.GetSingleSelectReply().GetSelectedRowID())
	}
	if tbr := m.GetTemplateButtonReplyMessage(); tbr != nil {
		setText(tbr.GetSelectedDisplayText())
	}
}

func hydratedTemplate(tmpl *waProto.TemplateMessage) *waProto.TemplateMessage_HydratedFourRowTemplate {
	if h := tmpl.GetHydratedFourRowTemplate(); h != nil {
		return h
	}
	return tmpl.GetHydratedTemplate()
}

func interactiveText(im *waProto.InteractiveMessage) string {
	return joinNonEmpty(im.GetHeader().GetTitle(), im.GetBody().GetText(), footer(im.GetFooter().GetText()))
}

// footer renders a footer line the way the text is laid out: in brackets.
func footer(s string) string {
	if s = strings.TrimSpace(s); s == "" {
		return ""
	}
	return "[" + s + "]"
}

func joinNonEmpty(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "\n")
}
