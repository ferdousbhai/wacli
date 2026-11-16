package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ferdousbhai/wacli/internal/accounts"
	"github.com/ferdousbhai/wacli/internal/fsutil"
	"github.com/ferdousbhai/wacli/internal/out"
	"github.com/ferdousbhai/wacli/internal/wa"
	"github.com/spf13/cobra"

	"go.mau.fi/whatsmeow/types/events"
)

func newAuthCmd(f *rootFlags) *cobra.Command {
	var phone string
	var days int
	var idle time.Duration
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Link a WhatsApp account to this computer and archive its full history",
		Long: `Shows a QR code to scan in WhatsApp → Linked devices (or prints a pairing
code with --phone), asks the phone for its full history, and archives it with
all media into a folder named after the account's number. Accounts linked
before keep working: run it once per account. Linking an account that was
logged out again picks up its folder.

Stop a running sync first (systemctl --user stop wacli-sync) and start it
again afterwards, so it serves the new account too.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if days < 0 || uint64(days) > math.MaxUint32 {
				return fmt.Errorf("--history-days must be between 0 and %d", uint32(math.MaxUint32))
			}
			wa.SetHistorySyncLimits(wa.HistorySyncLimits{Full: true, Days: uint32(days)})
			ctx, cancel := signalContext()
			defer cancel()
			return runAuth(ctx, f, phone, idle)
		},
	}
	cmd.Flags().StringVar(&phone, "phone", "", "pair with a code for this phone number instead of a QR code")
	cmd.Flags().IntVar(&days, "history-days", 0, "days of history to request (0 = everything the phone has)")
	cmd.Flags().DurationVar(&idle, "idle-exit", 3*time.Minute, "finish after this long without new history")
	return cmd
}

var errAuthBusy = errors.New("a wacli sync is running; stop it (systemctl --user stop wacli-sync), run `wacli auth`, then start it again (systemctl --user start wacli-sync) to serve the new account too")

func runAuth(ctx context.Context, f *rootFlags, phone string, idle time.Duration) error {
	s, err := openSession(f, nil)
	if errors.Is(err, errBusy) {
		return errAuthBusy
	}
	if err != nil {
		return err
	}
	defer s.close()
	dir := filepath.Join(f.storeDir, accounts.Pending)
	if err := fsutil.EnsurePrivateDir(dir); err != nil {
		return err
	}
	client, err := wa.New(wa.Options{StorePath: filepath.Join(dir, accounts.SessionFile)})
	if err != nil {
		return err
	}
	l := &linking{ctx: ctx, s: s, client: client}
	if client.IsAuthed() { // paired before, but stopped before it was recorded
		l.paired(client.LinkedJID())
	}
	client.AddEventHandler(l.handle)
	opts := wa.ConnectOptions{AllowQR: true, PairPhoneNumber: phone, OnPairCode: func(code string) {
		fmt.Fprintf(os.Stderr, "Enter this code in WhatsApp → Linked devices → Link with phone number: %s\n", code)
	}}
	err = client.Connect(ctx, opts)
	acct, lerr := l.result()
	if acct == nil {
		defer client.Close()
	}
	if err != nil {
		return err
	}
	if lerr != nil {
		if errors.Is(lerr, errAlreadyLinked) {
			unlinkExtra(ctx, client)
		}
		return lerr
	}
	if acct == nil {
		return errors.New("WhatsApp didn't confirm the link; run `wacli auth` again")
	}
	fmt.Fprintf(os.Stderr, "Linked %s. Archiving history into %s (keep the phone online)…\n", accounts.Key(acct.JID), acct.ar.Root())
	if _, err := runUntilIdle(ctx, []*account{acct}, idle); err != nil {
		return err
	}
	return report(f, acct.summary())
}

var errAlreadyLinked = errors.New("this account is linked already")

// unlinkExtra removes the second link to an account that was linked
// already, so the phone doesn't list it.
func unlinkExtra(ctx context.Context, client *wa.Client) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for !client.IsConnected() && ctx.Err() == nil {
		time.Sleep(200 * time.Millisecond)
	}
	if err := client.Logout(ctx); err != nil {
		logf("couldn't remove the extra link (%v): remove it in WhatsApp → Linked devices", err)
	}
}

// linking archives a new link once it has paired: only then is its number,
// so its folder, known. Events until then are kept and handed over.
type linking struct {
	ctx    context.Context
	s      *session
	client *wa.Client

	mu    sync.Mutex
	queue []any
	acct  *account
	err   error
}

func (l *linking) handle(evt any) {
	if ps, ok := evt.(*events.PairSuccess); ok {
		l.paired(ps.ID.ToNonAD().String())
	}
	l.mu.Lock()
	acct, failed := l.acct, l.err != nil
	if acct == nil && !failed {
		l.queue = append(l.queue, evt)
	}
	l.mu.Unlock()
	if acct != nil {
		acct.arc.Handle(evt)
	}
}

func (l *linking) result() (*account, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.acct, l.err
}

// paired records the account jid, opens its archive and starts archiving.
// Called from a whatsmeow event handler: it must not add handlers.
func (l *linking) paired(jid string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.acct != nil || l.err != nil {
		return
	}
	reg := l.s.reg
	if a, ok := reg.Find(jid); ok {
		if a.Store != accounts.Pending && mayBeLinked(reg.SessionPath(*a)) {
			// Or removed on the phone while no sync ran to hear of it.
			l.err = fmt.Errorf("%w as %s, so this extra link is removed; if you removed it on the phone, run `wacli logout --account %s` first, then `wacli auth` again", errAlreadyLinked, a.Label(), accounts.Key(a.JID))
			return
		}
		a.Store = accounts.Pending // relinking one that was logged out
	} else {
		reg.Add(jid, accounts.Pending)
	}
	if l.err = reg.Save(); l.err != nil {
		return
	}
	notes, err := reg.AdoptLegacyArchive(jid)
	for _, n := range notes {
		logf("%s", n)
	}
	if l.err = err; err != nil {
		return
	}
	a, _ := reg.Find(jid)
	acct, err := openAccount(l.s.f, reg, *a, l.client)
	if l.err = err; err != nil {
		return
	}
	l.s.accounts = append(l.s.accounts, acct)
	acct.arc.Run(l.ctx)
	for _, evt := range l.queue {
		acct.arc.Handle(evt)
	}
	l.queue, l.acct = nil, acct
}

func newSyncCmd(f *rootFlags) *cobra.Command {
	var follow bool
	var idle time.Duration
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Archive new messages of every linked account (keep running with --follow)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := openSession(f, every)
			if err != nil {
				return err
			}
			defer s.close()
			ctx, cancel := signalContext()
			defer cancel()
			live := s.linked()
			if len(live) == 0 {
				return errUnlinked
			}
			for _, a := range s.accounts {
				if !a.wa.IsAuthed() {
					logf("%s is not linked (logged out); `wacli auth` links it again", a.Label())
				}
			}
			for _, a := range live {
				if err := a.start(ctx, wa.ConnectOptions{DetachSocket: follow}); err != nil {
					return fmt.Errorf("%s: %w", a.Label(), err)
				}
			}
			if !follow {
				if live, err = runUntilIdle(ctx, live, idle); err != nil {
					return err
				}
				var sums []map[string]any
				for _, a := range live {
					sums = append(sums, a.summary())
				}
				return report(f, sums...)
			}
			stop, err := serveIPC(ctx, f, s)
			if err != nil {
				return err
			}
			defer stop()
			for _, a := range live {
				a.arc.RefetchInBackground()
				fmt.Fprintf(os.Stderr, "Archiving %s into %s; Ctrl+C to stop.\n", accounts.Key(a.JID), a.ar.Root())
			}
			return runFollowing(ctx, live)
		},
	}
	cmd.Flags().BoolVar(&follow, "follow", false, "keep running and archive messages as they arrive")
	cmd.Flags().DurationVar(&idle, "idle-exit", 30*time.Second, "without --follow, finish after this long without new messages")
	return cmd
}

// runFollowing runs until Ctrl+C, or until WhatsApp has ended every account. An
// account unlinked or stopped for good is left out while the others go on;
// one that a restart may fix restarts the sync.
func runFollowing(ctx context.Context, live []*account) error {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		for _, a := range live {
			if err := a.ended(); err != nil && !errors.Is(err, errUnlinked) && !errors.Is(err, errHalted) {
				return fmt.Errorf("%s: %w", a.Label(), err)
			}
		}
		var err error
		if live, err = dropEnded(live); err != nil {
			return err
		}
	}
}

func report(f *rootFlags, data ...map[string]any) error {
	if f.asJSON {
		if len(data) == 1 {
			return out.WriteJSON(os.Stdout, data[0])
		}
		return out.WriteJSON(os.Stdout, data)
	}
	for _, d := range data {
		fmt.Printf("Account: %v\nArchive: %v\nChats: %v\nNew messages: %v\nAttachments still pending: %v\n",
			d["account"], d["archive"], d["chats"], d["new_messages"], d["pending_media"])
	}
	return nil
}
