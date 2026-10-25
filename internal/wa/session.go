package wa

import (
	"context"
	"database/sql"
	"fmt"
	"os"

	"github.com/ferdousbhai/wacli/internal/sqliteutil"
	_ "github.com/mattn/go-sqlite3"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types/events"
)

func (c *Client) init() (err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	ctx := context.Background()
	dbLog := newWhatsmeowLogger("Database", "ERROR", os.Stderr)
	if err := sqliteutil.ChmodFiles(c.opts.StorePath, 0o600); err != nil {
		return err
	}
	container, err := sqlstore.New(ctx, "sqlite3", sqliteutil.FileURI(c.opts.StorePath, "_foreign_keys=on"), dbLog)
	if err != nil {
		return fmt.Errorf("open whatsmeow store: %w", err)
	}
	defer func() {
		if err != nil {
			_ = container.Close()
		}
	}()
	if err := sqliteutil.ChmodFiles(c.opts.StorePath, 0o600); err != nil {
		return err
	}

	deviceStore, err := container.GetFirstDevice(ctx)
	if err != nil {
		if err == sql.ErrNoRows {
			deviceStore = container.NewDevice()
		} else {
			return fmt.Errorf("get device store: %w", err)
		}
	}

	logger := newWhatsmeowLogger("Client", "ERROR", os.Stderr)
	c.client = whatsmeow.NewClient(deviceStore, logger)
	// Contact names from a full app-state sync arrive as events too, which is
	// what tells the archiver to rename chats still shown as numbers.
	c.client.EmitAppStateEventsOnFullSync = true
	// Persist recently-sent messages so whatsmeow can answer retry-receipts
	// across process restarts. Without this, recipients whose Signal session
	// has not been freshly bootstrapped (typically other linked devices) see
	// "Waiting for this message" indefinitely because whatsmeow can't find the
	// original plaintext to re-encrypt when the retry arrives.
	c.client.UseRetryMessageStore = true
	cli := c.client
	guardAppStateKeys(deviceStore)
	cli.AddEventHandler(func(evt any) {
		if _, ok := evt.(*events.PairSuccess); ok {
			// Pairing's Device.Save replaced the stores.
			guardAppStateKeys(cli.Store)
		}
	})
	// Let whatsmeow own bounded primary-device retries and cancellation for
	// eligible decryption failures.
	c.client.AutomaticMessageRerequestFromPhone = true
	c.container = container
	return nil
}
