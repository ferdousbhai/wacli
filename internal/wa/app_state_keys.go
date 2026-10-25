package wa

import (
	"context"
	"errors"

	"go.mau.fi/whatsmeow/store"
)

// ErrEmptyAppStateKeyShare is returned instead of storing a key the primary
// device shared without key data (the SQL store would fail with a NOT NULL
// error, and an empty key could never decrypt anything).
var ErrEmptyAppStateKeyShare = errors.New("primary device shared the key without key data")

// appStateKeyGuard keeps empty app-state keys out of the session store.
type appStateKeyGuard struct {
	store.AppStateSyncKeyStore
}

func (g *appStateKeyGuard) PutAppStateSyncKey(ctx context.Context, id []byte, key store.AppStateSyncKey) error {
	if len(key.Data) == 0 {
		return ErrEmptyAppStateKeyShare
	}
	return g.AppStateSyncKeyStore.PutAppStateSyncKey(ctx, id, key)
}

func (g *appStateKeyGuard) GetAppStateSyncKey(ctx context.Context, id []byte) (*store.AppStateSyncKey, error) {
	key, err := g.AppStateSyncKeyStore.GetAppStateSyncKey(ctx, id)
	if err == nil && key != nil && len(key.Data) == 0 {
		return nil, ErrEmptyAppStateKeyShare
	}
	return key, err
}

// guardAppStateKeys wraps the device's key store once. Pairing replaces every
// session store on the device, so this runs again after PairSuccess.
func guardAppStateKeys(device *store.Device) {
	if device == nil || device.AppStateKeys == nil {
		return
	}
	if _, ok := device.AppStateKeys.(*appStateKeyGuard); ok {
		return
	}
	device.AppStateKeys = &appStateKeyGuard{AppStateSyncKeyStore: device.AppStateKeys}
}
