package wa

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mdp/qrterminal/v3"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCompanionReg"
	wastore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

type Options struct {
	StorePath string
}

type Client struct {
	opts Options

	mu        sync.Mutex
	client    *whatsmeow.Client
	container *sqlstore.Container
}

func New(opts Options) (*Client, error) {
	if strings.TrimSpace(opts.StorePath) == "" {
		return nil, fmt.Errorf("StorePath is required")
	}
	// Reject paths that could inject SQLite URI parameters.
	if strings.ContainsAny(opts.StorePath, "?#") {
		return nil, fmt.Errorf("StorePath must not contain '?' or '#'")
	}
	c := &Client{opts: opts}
	if err := c.init(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client != nil {
		c.client.Disconnect()
		c.client = nil
	}
	if c.container != nil {
		_ = c.container.Close()
		c.container = nil
	}
}

// Disconnect stops the socket while retaining the session store for reconnects.
func (c *Client) Disconnect() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client != nil {
		c.client.Disconnect()
	}
}

func (c *Client) IsAuthed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.client != nil && c.client.Store != nil && c.client.Store.ID != nil
}

func (c *Client) LinkedJID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client == nil || c.client.Store == nil || c.client.Store.ID == nil {
		return ""
	}
	return c.client.Store.ID.ToNonAD().String()
}

func (c *Client) LinkedLID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client == nil || c.client.Store == nil {
		return ""
	}
	lid := c.client.Store.GetLID()
	if lid.IsEmpty() {
		return ""
	}
	return lid.ToNonAD().String()
}

// IsConnected reports whether the connection is up and logged in: between
// the dial and the server's go-ahead, requests fail.
func (c *Client) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return ready(c.client)
}

// ErrNotConnected is a request made while IsConnected is false.
var ErrNotConnected = errors.New("not connected to WhatsApp")

func ready(cli *whatsmeow.Client) bool {
	return cli != nil && cli.IsConnected() && cli.IsLoggedIn()
}

// HistorySyncLimits shapes the history bundle the primary device pushes when
// a companion links. By default whatsmeow does not ask for a full sync, so the
// primary sends only its recent window (about three months); Days narrows that
// window and Full asks for the full archive instead.
//
// The fields are uint32 because that is what the pairing protobuf carries:
// callers convert once, after checking the value fits, so a request too large
// for the wire cannot quietly wrap into a different one here.
type HistorySyncLimits struct {
	// Days of history to ask for, 0 to leave it to the primary.
	Days uint32
	// Full asks for a full history sync instead of the recent window. With
	// Days set it bounds only the full sync: the recent window the primary
	// sends first keeps its default.
	Full bool
}

// SetHistorySyncLimits applies the limits to the device properties whatsmeow
// sends while pairing. They travel in the pairing handshake, so they only
// affect a device that links after this call: changing them for an already
// linked device does nothing.
func SetHistorySyncLimits(limits HistorySyncLimits) {
	cfg := wastore.DeviceProps.GetHistorySyncConfig()
	if cfg == nil {
		cfg = &waCompanionReg.DeviceProps_HistorySyncConfig{}
		wastore.DeviceProps.HistorySyncConfig = cfg
	}
	wastore.DeviceProps.RequireFullSync = proto.Bool(limits.Full)
	if limits.Days > 0 {
		cfg.FullSyncDaysLimit = proto.Uint32(limits.Days)
		if !limits.Full {
			cfg.RecentSyncDaysLimit = proto.Uint32(limits.Days)
		}
	}
}

type ConnectOptions struct {
	AllowQR         bool
	PairPhoneNumber string
	OnPairCode      func(code string)
	// DetachSocket, when true, connects the websocket with a detached
	// context so it is not closed when the caller's context is cancelled,
	// leaving shutdown to Disconnect. `sync --follow` uses this; other
	// commands keep the default so Ctrl+C also ends the connection.
	DetachSocket bool
}

func (c *Client) Connect(ctx context.Context, opts ConnectOptions) error {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil {
		return fmt.Errorf("whatsapp client is not initialized")
	}

	if cli.IsConnected() {
		return nil
	}

	authed := cli.Store != nil && cli.Store.ID != nil
	if !authed && !opts.AllowQR && opts.PairPhoneNumber == "" {
		return fmt.Errorf("not authenticated; run `wacli auth`")
	}

	var qrChan <-chan whatsmeow.QRChannelItem
	if !authed {
		ch, err := cli.GetQRChannel(ctx)
		if err != nil {
			return fmt.Errorf("get QR channel: %w", err)
		}
		qrChan = ch
	}

	// When DetachSocket is set (sync), connect with a detached context that
	// has a 30-second dial timeout. The dial is bounded so a hanging connect
	// cannot outlive SIGTERM indefinitely; after the dial succeeds the timer
	// is stopped so the websocket stays open until Disconnect()/Close().
	// Other commands keep the caller's context, so Ctrl+C ends the
	// connection too.
	connCtx := ctx
	var dialCancel context.CancelFunc
	var dialTimer *time.Timer
	var dialStop func() bool
	if opts.DetachSocket {
		// Use a detached context so the websocket survives signal
		// cancellation, but also cancel the dial if the caller's
		// context fires (SIGTERM) so an in-flight
		// connect cannot outlive caller cancellation.
		connCtx, dialCancel = context.WithCancel(context.Background())
		dialTimer = time.AfterFunc(30*time.Second, dialCancel)
		dialStop = context.AfterFunc(ctx, dialCancel)
	}
	if err := cli.ConnectContext(connCtx); err != nil {
		if dialCancel != nil {
			dialCancel()
		}
		if dialTimer != nil {
			dialTimer.Stop()
		}
		if dialStop != nil {
			dialStop()
		}
		return err
	}
	// Dial succeeded — stop the timer and unregister the caller
	// cancellation hook so the websocket stays alive until Close().
	if dialTimer != nil {
		dialTimer.Stop()
	}
	if dialStop != nil {
		dialStop()
	}

	if authed {
		return nil
	}

	// Wait for QR flow to succeed or fail.
	pairCodeRequested := false
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case evt, ok := <-qrChan:
			if !ok {
				return fmt.Errorf("QR channel closed")
			}
			switch {
			case evt.Event == whatsmeow.QRChannelEventCode:
				if opts.PairPhoneNumber != "" {
					if pairCodeRequested {
						continue
					}
					code, err := cli.PairPhone(ctx, opts.PairPhoneNumber, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
					if err != nil {
						return fmt.Errorf("pair phone: %w", err)
					}
					pairCodeRequested = true
					if opts.OnPairCode != nil {
						opts.OnPairCode(code)
					}
				} else {
					qrterminal.GenerateHalfBlock(evt.Code, qrterminal.M, os.Stdout)
				}
			case evt == whatsmeow.QRChannelSuccess:
				return nil
			default:
				if err := qrChannelEventError(evt); err != nil {
					return err
				}
			}
		}
	}
}

func qrChannelEventError(evt whatsmeow.QRChannelItem) error {
	switch {
	case evt == whatsmeow.QRChannelTimeout:
		return fmt.Errorf("QR code timed out; run `wacli auth` again to get a new code")
	case evt == whatsmeow.QRChannelClientOutdated:
		return fmt.Errorf("WhatsApp client outdated; update wacli and try again")
	case evt == whatsmeow.QRChannelScannedWithoutMultidevice:
		return fmt.Errorf("QR scanned, but multi-device is not enabled on the phone")
	case evt == whatsmeow.QRChannelErrUnexpectedEvent:
		return fmt.Errorf("unexpected QR pairing state; run `wacli auth` again")
	case evt.Event == whatsmeow.QRChannelEventPasskeyRequest:
		return fmt.Errorf("WhatsApp requires passkey verification, which wacli cannot safely complete yet; preserve any existing authenticated store and check for an updated wacli release")
	case evt.Event == whatsmeow.QRChannelEventPasskeyResponse:
		return fmt.Errorf("WhatsApp requires passkey confirmation, which wacli cannot safely complete yet; preserve any existing authenticated store and check for an updated wacli release")
	case evt.Event == whatsmeow.QRChannelEventError:
		if evt.Error != nil {
			return fmt.Errorf("QR pairing failed: %w", evt.Error)
		}
		return fmt.Errorf("QR pairing failed")
	default:
		return fmt.Errorf("unsupported QR pairing state %q; update wacli and try again", evt.Event)
	}
}

func (c *Client) AddEventHandler(handler func(any)) uint32 {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil {
		return 0
	}
	return cli.AddEventHandler(handler)
}

func (c *Client) RequestHistorySyncOnDemand(ctx context.Context, lastKnown types.MessageInfo, count int) (types.MessageID, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if !ready(cli) {
		return "", ErrNotConnected
	}
	if count <= 0 {
		count = 50
	}
	if lastKnown.Chat.IsEmpty() || strings.TrimSpace(string(lastKnown.ID)) == "" || lastKnown.Timestamp.IsZero() {
		return "", fmt.Errorf("invalid last known message info")
	}

	ownID := types.JID{}
	if cli.Store != nil && cli.Store.ID != nil {
		ownID = cli.Store.ID.ToNonAD()
	}
	if ownID.IsEmpty() {
		return "", fmt.Errorf("not authenticated; run `wacli auth`")
	}

	msg := cli.BuildHistorySyncRequest(&lastKnown, count)
	resp, err := cli.SendMessage(ctx, ownID, msg, whatsmeow.SendRequestExtra{Peer: true})
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}

func (c *Client) GetGroupInfo(ctx context.Context, jid types.JID) (*types.GroupInfo, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if !ready(cli) {
		return nil, ErrNotConnected
	}
	return cli.GetGroupInfo(ctx, jid)
}

// sendInitialAvailablePresence reports whether it sent the presence.
func sendInitialAvailablePresence(ctx context.Context, cli *whatsmeow.Client) bool {
	// Whatsmeow recommends this once after connect so the server records the linked-device pushname.
	if cli == nil || cli.Store == nil || strings.TrimSpace(cli.Store.PushName) == "" {
		return false
	}
	if err := cli.SendPresence(ctx, types.PresenceAvailable); err != nil {
		fmt.Fprintf(os.Stderr, "warn: failed to send initial available presence: %v\n", err)
		return false
	}
	return true
}

func (c *Client) Logout(ctx context.Context) error {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil {
		return fmt.Errorf("not initialized")
	}
	return cli.Logout(ctx)
}

// OwnPushName is the profile name of the linked account.
func (c *Client) OwnPushName() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client == nil || c.client.Store == nil {
		return ""
	}
	return c.client.Store.PushName
}
