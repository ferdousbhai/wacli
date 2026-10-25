package wa

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow"
	waStore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

func TestNewEnablesRetryMessageStore(t *testing.T) {
	c, err := New(Options{StorePath: filepath.Join(t.TempDir(), "session.db")})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()

	if c.client == nil {
		t.Fatal("expected whatsmeow client")
	}
	if !c.client.UseRetryMessageStore {
		t.Fatal("expected retry message store to be enabled")
	}
	if _, ok := c.client.Log.(*whatsmeowLogger); !ok {
		t.Fatalf("client logger = %T, want *whatsmeowLogger", c.client.Log)
	}
	if got := c.LinkedJID(); got != "" {
		t.Fatalf("LinkedJID before auth = %q", got)
	}
}

func TestResolvePNToLIDUsesOwnLIDWithoutCache(t *testing.T) {
	pn := types.NewJID("15551234567", types.DefaultUserServer)
	lid := types.NewJID("999123456789", types.HiddenUserServer)
	cli := &whatsmeow.Client{Store: &waStore.Device{ID: &pn, LID: lid, LIDMigrationTimestamp: 1}}

	got := resolvePNToLID(context.Background(), cli, pn, true)
	if got != lid {
		t.Fatalf("resolved = %s, want %s", got, lid)
	}
}

func TestResolvePNToLIDPublicUsesOwnLIDWithoutCache(t *testing.T) {
	pn := types.NewJID("15551234567", types.DefaultUserServer)
	lid := types.NewJID("999123456789", types.HiddenUserServer)
	c := &Client{client: &whatsmeow.Client{Store: &waStore.Device{ID: &pn, LID: lid, LIDMigrationTimestamp: 1}}}

	got := c.ResolvePNToLID(context.Background(), pn)
	if got != lid {
		t.Fatalf("resolved = %s, want %s", got, lid)
	}
}

func TestQRChannelEventError(t *testing.T) {
	cases := []struct {
		name string
		evt  whatsmeow.QRChannelItem
		want string
	}{
		{name: "timeout", evt: whatsmeow.QRChannelTimeout, want: "QR code timed out"},
		{name: "client outdated", evt: whatsmeow.QRChannelClientOutdated, want: "WhatsApp client outdated"},
		{name: "multidevice disabled", evt: whatsmeow.QRChannelScannedWithoutMultidevice, want: "multi-device is not enabled"},
		{name: "unexpected state", evt: whatsmeow.QRChannelErrUnexpectedEvent, want: "unexpected QR pairing state"},
		{name: "pair error", evt: whatsmeow.QRChannelItem{Event: whatsmeow.QRChannelEventError, Error: errors.New("bad code")}, want: "bad code"},
		{name: "passkey request", evt: whatsmeow.QRChannelItem{Event: whatsmeow.QRChannelEventPasskeyRequest}, want: "requires passkey verification"},
		{name: "passkey confirmation", evt: whatsmeow.QRChannelItem{Event: whatsmeow.QRChannelEventPasskeyResponse}, want: "requires passkey confirmation"},
		{name: "unknown event", evt: whatsmeow.QRChannelItem{Event: "future-pairing-step"}, want: "unsupported QR pairing state"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			err := qrChannelEventError(tt.evt)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}
