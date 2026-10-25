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

	got := resolvePNToLID(context.Background(), cli, pn)
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

func TestSetHistorySyncLimits(t *testing.T) {
	previous := waStore.DeviceProps.HistorySyncConfig
	previousFull := waStore.DeviceProps.RequireFullSync
	t.Cleanup(func() {
		waStore.DeviceProps.HistorySyncConfig = previous
		waStore.DeviceProps.RequireFullSync = previousFull
	})

	waStore.DeviceProps.HistorySyncConfig = nil
	SetHistorySyncLimits(HistorySyncLimits{})
	cfg := waStore.DeviceProps.GetHistorySyncConfig()
	if cfg == nil {
		t.Fatal("expected a history sync config")
	}
	if cfg.FullSyncDaysLimit != nil || cfg.RecentSyncDaysLimit != nil || cfg.InitialSyncMaxMessagesPerChat != nil || cfg.FullSyncSizeMbLimit != nil {
		t.Fatalf("zero limits set fields: %v", cfg)
	}
	if waStore.DeviceProps.GetRequireFullSync() {
		t.Fatal("zero limits asked for a full sync")
	}

	SetHistorySyncLimits(HistorySyncLimits{Days: 3, MaxPerChat: 50})
	cfg = waStore.DeviceProps.GetHistorySyncConfig()
	if cfg.GetFullSyncDaysLimit() != 3 || cfg.GetRecentSyncDaysLimit() != 3 {
		t.Fatalf("days = %d/%d, want 3/3", cfg.GetFullSyncDaysLimit(), cfg.GetRecentSyncDaysLimit())
	}
	if cfg.GetInitialSyncMaxMessagesPerChat() != 50 {
		t.Fatalf("per chat = %d, want 50", cfg.GetInitialSyncMaxMessagesPerChat())
	}
	if waStore.DeviceProps.GetRequireFullSync() {
		t.Fatal("a bounded window asked for a full sync")
	}

	waStore.DeviceProps.HistorySyncConfig = nil
	SetHistorySyncLimits(HistorySyncLimits{Full: true})
	cfg = waStore.DeviceProps.GetHistorySyncConfig()
	if !waStore.DeviceProps.GetRequireFullSync() {
		t.Fatal("full history did not ask for a full sync")
	}
	if cfg.FullSyncDaysLimit != nil || cfg.RecentSyncDaysLimit != nil {
		t.Fatalf("full history without days set a bound: %v", cfg)
	}

	waStore.DeviceProps.HistorySyncConfig = nil
	SetHistorySyncLimits(HistorySyncLimits{Full: true, Days: 3650})
	cfg = waStore.DeviceProps.GetHistorySyncConfig()
	if !waStore.DeviceProps.GetRequireFullSync() || cfg.GetFullSyncDaysLimit() != 3650 {
		t.Fatalf("full = %v, days = %d, want true, 3650", waStore.DeviceProps.GetRequireFullSync(), cfg.GetFullSyncDaysLimit())
	}
	if cfg.RecentSyncDaysLimit != nil {
		t.Fatalf("full history bounded the recent window: %d", cfg.GetRecentSyncDaysLimit())
	}
	if cfg.FullSyncSizeMbLimit != nil {
		t.Fatalf("full history without a size set one: %d", cfg.GetFullSyncSizeMbLimit())
	}

	waStore.DeviceProps.HistorySyncConfig = nil
	SetHistorySyncLimits(HistorySyncLimits{Full: true, Days: 7300, SizeMB: 2048})
	cfg = waStore.DeviceProps.GetHistorySyncConfig()
	if cfg.GetFullSyncSizeMbLimit() != 2048 || cfg.GetFullSyncDaysLimit() != 7300 {
		t.Fatalf("size = %d MB, days = %d, want 2048, 7300", cfg.GetFullSyncSizeMbLimit(), cfg.GetFullSyncDaysLimit())
	}
}
