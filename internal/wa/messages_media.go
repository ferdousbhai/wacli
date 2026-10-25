package wa

import (
	"strings"

	waProto "go.mau.fi/whatsmeow/binary/proto"
)

func extractMedia(m *waProto.Message, pm *ParsedMessage) {
	if img := m.GetImageMessage(); img != nil {
		if pm.Text == "" {
			pm.Text = img.GetCaption()
		}
		pm.Media = &Media{
			Type:          "image",
			Caption:       img.GetCaption(),
			DirectPath:    img.GetDirectPath(),
			MediaKey:      clone(img.GetMediaKey()),
			FileSHA256:    clone(img.GetFileSHA256()),
			FileEncSHA256: clone(img.GetFileEncSHA256()),
			FileLength:    img.GetFileLength(),
		}
	}

	vid := m.GetVideoMessage()
	if vid == nil {
		vid = m.GetPtvMessage() // a video note (round video)
	}
	if vid != nil {
		if pm.Text == "" {
			pm.Text = vid.GetCaption()
		}
		mediaType := "video"
		if vid.GetGifPlayback() {
			mediaType = "gif"
		}
		pm.Media = &Media{
			Type:          mediaType,
			Caption:       vid.GetCaption(),
			DirectPath:    vid.GetDirectPath(),
			MediaKey:      clone(vid.GetMediaKey()),
			FileSHA256:    clone(vid.GetFileSHA256()),
			FileEncSHA256: clone(vid.GetFileEncSHA256()),
			FileLength:    vid.GetFileLength(),
		}
	}

	if aud := m.GetAudioMessage(); aud != nil {
		pm.Media = &Media{
			Type:          "audio",
			Caption:       pm.Text,
			DirectPath:    aud.GetDirectPath(),
			MediaKey:      clone(aud.GetMediaKey()),
			FileSHA256:    clone(aud.GetFileSHA256()),
			FileEncSHA256: clone(aud.GetFileEncSHA256()),
			FileLength:    aud.GetFileLength(),
		}
	}

	if doc := m.GetDocumentMessage(); doc != nil {
		if pm.Text == "" {
			pm.Text = doc.GetCaption()
		}
		pm.Media = &Media{
			Type:          "document",
			Caption:       doc.GetCaption(),
			Filename:      doc.GetFileName(),
			PageCount:     doc.GetPageCount(),
			DirectPath:    doc.GetDirectPath(),
			MediaKey:      clone(doc.GetMediaKey()),
			FileSHA256:    clone(doc.GetFileSHA256()),
			FileEncSHA256: clone(doc.GetFileEncSHA256()),
			FileLength:    doc.GetFileLength(),
		}
	}

	if sticker := m.GetStickerMessage(); sticker != nil {
		pm.Media = &Media{
			Type:          "sticker",
			DirectPath:    sticker.GetDirectPath(),
			MediaKey:      clone(sticker.GetMediaKey()),
			FileSHA256:    clone(sticker.GetFileSHA256()),
			FileEncSHA256: clone(sticker.GetFileEncSHA256()),
			FileLength:    sticker.GetFileLength(),
		}
	}
}

func extractLocation(m *waProto.Message, pm *ParsedMessage) {
	if loc := m.GetLocationMessage(); loc != nil {
		pm.Location = &Location{
			Latitude:  loc.GetDegreesLatitude(),
			Longitude: loc.GetDegreesLongitude(),
			Name:      strings.TrimSpace(loc.GetName()),
			Address:   strings.TrimSpace(loc.GetAddress()),
		}
		pm.Media = &Media{Type: "location", Caption: strings.TrimSpace(loc.GetName())}
		return
	}
	if live := m.GetLiveLocationMessage(); live != nil {
		pm.Location = &Location{
			Latitude:  live.GetDegreesLatitude(),
			Longitude: live.GetDegreesLongitude(),
			Name:      strings.TrimSpace(live.GetCaption()),
			IsLive:    true,
		}
		pm.Media = &Media{Type: "live_location", Caption: strings.TrimSpace(live.GetCaption())}
	}
}
