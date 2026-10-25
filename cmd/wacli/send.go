package main

import (
	"context"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/ferdousbhai/wacli/internal/archive"
	"github.com/ferdousbhai/wacli/internal/out"
	"github.com/spf13/cobra"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

func newSendCmd(f *rootFlags) *cobra.Command {
	cmd := &cobra.Command{Use: "send", Short: "Send a message; it is archived like any other"}

	var to, message string
	var escapes bool
	text := &cobra.Command{
		Use:   "text",
		Short: "Send a text message",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if escapes {
				message = strings.NewReplacer(`\n`, "\n", `\r`, "\r", `\t`, "\t").Replace(message)
			}
			res, err := execute(f, request{Op: "send", To: to, Text: message})
			return printResult(f, res, err)
		},
	}
	text.Flags().StringVar(&to, "to", "", "phone number (+14155551212), JID, or chat name")
	text.Flags().StringVar(&message, "message", "", "message text")
	text.Flags().BoolVar(&escapes, "message-escapes", false, `interpret \n, \r and \t in --message`)
	_ = text.MarkFlagRequired("to")
	_ = text.MarkFlagRequired("message")

	var fileTo, path, caption string
	file := &cobra.Command{
		Use:   "file",
		Short: "Send an image, video, audio file or document",
		RunE: func(cmd *cobra.Command, _ []string) error {
			abs, err := filepath.Abs(path)
			if err != nil {
				return err
			}
			res, err := execute(f, request{Op: "send", To: fileTo, File: abs, Caption: caption})
			return printResult(f, res, err)
		},
	}
	file.Flags().StringVar(&fileTo, "to", "", "phone number (+14155551212), JID, or chat name")
	file.Flags().StringVar(&path, "file", "", "file to send")
	file.Flags().StringVar(&caption, "caption", "", "caption")
	_ = file.MarkFlagRequired("to")
	_ = file.MarkFlagRequired("file")

	cmd.AddCommand(text, file)
	return cmd
}

func printResult(f *rootFlags, res any, err error) error {
	if err != nil {
		return err
	}
	if f.asJSON {
		return out.WriteJSON(os.Stdout, res)
	}
	if m, ok := res.(map[string]any); ok {
		fmt.Printf("Sent %v to %v\n", m["id"], m["chat"])
		return nil
	}
	return out.WriteJSON(os.Stdout, res)
}

func doSend(ctx context.Context, s *session, r request) (any, error) {
	to, err := resolveChat(s.ar, r.To)
	if err != nil {
		return nil, err
	}
	var msg *waE2E.Message
	if r.File == "" {
		msg = &waE2E.Message{Conversation: proto.String(r.Text)}
	} else if msg, err = mediaMessage(ctx, s, r.File, r.Caption); err != nil {
		return nil, err
	}
	id, err := s.wa.SendProtoMessage(ctx, to, msg)
	if err != nil {
		return nil, err
	}
	if key := s.arc.RecordSent(ctx, to, id, msg); key != "" && s.oneShot {
		s.arc.WaitMediaKeys(ctx, key)
	}
	return map[string]any{"id": id, "chat": to.String()}, nil
}

func mediaMessage(ctx context.Context, s *session, path, caption string) (*waE2E.Message, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	mimeType := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
	if mimeType == "" {
		mimeType = http.DetectContentType(data)
	}
	kind := whatsmeow.MediaDocument
	switch {
	case strings.HasPrefix(mimeType, "image/") && mimeType != "image/svg+xml":
		kind = whatsmeow.MediaImage
	case strings.HasPrefix(mimeType, "video/"):
		kind = whatsmeow.MediaVideo
	case strings.HasPrefix(mimeType, "audio/"):
		kind = whatsmeow.MediaAudio
	}
	up, err := s.wa.Upload(ctx, data, kind)
	if err != nil {
		return nil, err
	}
	size := proto.Uint64(uint64(len(data)))
	switch kind {
	case whatsmeow.MediaImage:
		return &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
			URL: &up.URL, DirectPath: &up.DirectPath, MediaKey: up.MediaKey, Mimetype: &mimeType,
			FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: size, Caption: optional(caption),
		}}, nil
	case whatsmeow.MediaVideo:
		return &waE2E.Message{VideoMessage: &waE2E.VideoMessage{
			URL: &up.URL, DirectPath: &up.DirectPath, MediaKey: up.MediaKey, Mimetype: &mimeType,
			FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: size, Caption: optional(caption),
		}}, nil
	case whatsmeow.MediaAudio:
		return &waE2E.Message{AudioMessage: &waE2E.AudioMessage{
			URL: &up.URL, DirectPath: &up.DirectPath, MediaKey: up.MediaKey, Mimetype: &mimeType,
			FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: size,
		}}, nil
	}
	name := filepath.Base(path)
	return &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{
		URL: &up.URL, DirectPath: &up.DirectPath, MediaKey: up.MediaKey, Mimetype: &mimeType,
		FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: size,
		FileName: &name, Title: &name, Caption: optional(caption),
	}}, nil
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// resolveChat accepts a phone number, a JID, or the name of an archived chat.
func resolveChat(ar *archive.Archive, to string) (types.JID, error) {
	to = strings.TrimSpace(to)
	if to == "" {
		return types.JID{}, fmt.Errorf("recipient is required")
	}
	if strings.Contains(to, "@") {
		return types.ParseJID(to)
	}
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, to)
	var number types.JID
	if digits != "" && strings.Trim(to, "+0123456789 -()") == "" {
		number = types.NewJID(digits, types.DefaultUserServer)
		if _, ok := ar.Info(number.String()); ok {
			return number, nil
		}
		// Not an archived number: it may be the name of a group ("2024").
	}
	var matches []archive.ChatInfo
	for _, c := range ar.Chats() {
		if strings.EqualFold(c.Name, to) || strings.EqualFold(c.Folder, to) || strings.EqualFold(c.Folder, "WhatsApp Chat - "+to) {
			matches = append(matches, c)
		}
	}
	switch len(matches) {
	case 0:
		if !number.IsEmpty() {
			return number, nil // a number not archived yet
		}
		return types.JID{}, fmt.Errorf("no chat named %q; use a phone number or JID (see `wacli chats`)", to)
	case 1:
		return types.ParseJID(matches[0].JID)
	}
	var opts []string
	for _, m := range matches {
		opts = append(opts, fmt.Sprintf("%s (%s)", m.Folder, m.JID))
	}
	return types.JID{}, fmt.Errorf("%q matches several chats: %s", to, strings.Join(opts, "; "))
}
