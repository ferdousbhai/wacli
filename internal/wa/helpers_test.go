package wa

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"

	"github.com/ferdousbhai/wacli/internal/sqliteutil"
)

func newPairedSessionStore(t *testing.T) string {
	t.Helper()
	return newPairedSessionStoreAt(t, filepath.Join(t.TempDir(), "session.db"))
}

func newPairedSessionStoreAt(t *testing.T, path string) string {
	t.Helper()
	ctx := context.Background()
	container, err := sqlstore.New(ctx, "sqlite3", sqliteutil.FileURI(path, "_foreign_keys=on"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer container.Close()
	device := container.NewDevice()
	device.ID = &types.JID{User: "15550000000", Device: 7, Server: types.DefaultUserServer}
	device.Account = &waAdv.ADVSignedDeviceIdentity{
		Details:             []byte{1},
		AccountSignature:    make([]byte, 64),
		AccountSignatureKey: make([]byte, 32),
		DeviceSignature:     make([]byte, 64),
	}
	if err := device.Save(ctx); err != nil {
		t.Fatal(err)
	}
	return path
}

// memKeyState is an in-memory AppStateKeyStateStore.
type memKeyState struct {
	mu sync.Mutex
	m  map[string]bool
}
