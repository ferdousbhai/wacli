package wa

import (
	"context"
	"fmt"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
)

func (c *Client) SendProtoMessage(ctx context.Context, to types.JID, msg *waProto.Message) (types.MessageID, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return "", fmt.Errorf("not connected")
	}
	resp, err := cli.SendMessage(ctx, to, msg)
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}

func (c *Client) Upload(ctx context.Context, data []byte, mediaType whatsmeow.MediaType) (whatsmeow.UploadResponse, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return whatsmeow.UploadResponse{}, fmt.Errorf("not connected")
	}
	return cli.Upload(ctx, data, mediaType)
}
