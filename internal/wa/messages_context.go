package wa

import (
	waProto "go.mau.fi/whatsmeow/binary/proto"
)

func contextInfoForMessage(m *waProto.Message) *waProto.ContextInfo {
	if m == nil {
		return nil
	}
	if ext := m.GetExtendedTextMessage(); ext != nil {
		return ext.GetContextInfo()
	}
	if img := m.GetImageMessage(); img != nil {
		return img.GetContextInfo()
	}
	if vid := m.GetVideoMessage(); vid != nil {
		return vid.GetContextInfo()
	}
	if ptv := m.GetPtvMessage(); ptv != nil {
		return ptv.GetContextInfo()
	}
	if aud := m.GetAudioMessage(); aud != nil {
		return aud.GetContextInfo()
	}
	if doc := m.GetDocumentMessage(); doc != nil {
		return doc.GetContextInfo()
	}
	if sticker := m.GetStickerMessage(); sticker != nil {
		return sticker.GetContextInfo()
	}
	if loc := m.GetLocationMessage(); loc != nil {
		return loc.GetContextInfo()
	}
	if live := m.GetLiveLocationMessage(); live != nil {
		return live.GetContextInfo()
	}
	if contact := m.GetContactMessage(); contact != nil {
		return contact.GetContextInfo()
	}
	if contacts := m.GetContactsArrayMessage(); contacts != nil {
		return contacts.GetContextInfo()
	}
	if tmpl := m.GetTemplateMessage(); tmpl != nil {
		return tmpl.GetContextInfo()
	}
	if btn := m.GetButtonsMessage(); btn != nil {
		return btn.GetContextInfo()
	}
	if resp := m.GetButtonsResponseMessage(); resp != nil {
		return resp.GetContextInfo()
	}
	if im := m.GetInteractiveMessage(); im != nil {
		return im.GetContextInfo()
	}
	if resp := m.GetInteractiveResponseMessage(); resp != nil {
		return resp.GetContextInfo()
	}
	if list := m.GetListMessage(); list != nil {
		return list.GetContextInfo()
	}
	if lr := m.GetListResponseMessage(); lr != nil {
		return lr.GetContextInfo()
	}
	if tbr := m.GetTemplateButtonReplyMessage(); tbr != nil {
		return tbr.GetContextInfo()
	}
	if creation := pickPollCreation(m); creation != nil {
		return creation.GetContextInfo()
	}
	if invite := m.GetGroupInviteMessage(); invite != nil {
		return invite.GetContextInfo()
	}
	if alb := m.GetAlbumMessage(); alb != nil {
		return alb.GetContextInfo()
	}
	return nil
}
