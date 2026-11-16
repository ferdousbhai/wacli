package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ferdousbhai/wacli/internal/accounts"
	"github.com/ferdousbhai/wacli/internal/archive"
	"github.com/ferdousbhai/wacli/internal/archiver"
	"github.com/ferdousbhai/wacli/internal/fsutil"
	"github.com/ferdousbhai/wacli/internal/lock"
	"github.com/ferdousbhai/wacli/internal/wa"

	"go.mau.fi/whatsmeow/types"
)

// session holds the store and the accounts a command works with. Only one
// process holds it at a time (the store LOCK); others talk to it over IPC.
type session struct {
	f        *rootFlags
	lock     *lock.Lock
	reg      *accounts.Registry
	accounts []*account
}

// account is one WhatsApp account: its connection writing into its archive.
type account struct {
	accounts.Account
	wa  *wa.Client
	ar  *archive.Archive
	arc *archiver.Archiver
}

var errBusy = errors.New("another wacli is running (the wacli-sync service? `systemctl --user stop wacli-sync` first)")

// picker chooses the accounts a command opens.
type picker func(*accounts.Registry) ([]accounts.Account, error)

// one opens the account sel names (which may be "" with one linked).
func one(sel string) picker {
	return func(r *accounts.Registry) ([]accounts.Account, error) {
		a, err := selectAccount(r, sel)
		if err != nil {
			return nil, err
		}
		if !openable(r, a) {
			return nil, fmt.Errorf("%s: %w", a.Label(), errUnlinked)
		}
		return []accounts.Account{a}, nil
	}
}

// every opens all accounts that can be opened.
func every(r *accounts.Registry) ([]accounts.Account, error) {
	all, err := r.Filter("")
	if errors.Is(err, accounts.ErrNone) {
		return nil, errUnlinked
	}
	var list []accounts.Account
	for _, a := range all {
		if openable(r, a) {
			list = append(list, a)
		}
	}
	return list, nil
}

// openable: a logged-out account is opened only while its folder is kept
// (opening it would make it again), for an import into it.
func openable(r *accounts.Registry, a accounts.Account) bool {
	return isLinked(r.SessionPath(a)) || archive.Exists(r.ArchiveRoot(a))
}

// openSession takes the store lock, moves the single-account layout into
// accounts (or finishes such a move), follows renamed account folders and
// opens the accounts pick chooses (none if pick is nil).
func openSession(f *rootFlags, pick picker) (*session, error) {
	l, err := lock.Acquire(f.storeDir)
	if err != nil {
		if lock.IsLocked(err) {
			return nil, errBusy
		}
		return nil, err
	}
	s := &session{f: f, lock: l}
	if s.reg, err = settleRegistry(f); err != nil {
		s.close()
		return nil, err
	}
	if pick == nil {
		return s, nil
	}
	list, err := pick(s.reg)
	if err != nil {
		s.close()
		return nil, err
	}
	for _, a := range list {
		acct, err := openAccount(f, s.reg, a, nil)
		if err != nil {
			s.close()
			return nil, fmt.Errorf("account %s: %w", a.Label(), err)
		}
		s.accounts = append(s.accounts, acct)
	}
	return s, nil
}

// settleRegistry loads the accounts with the store lock held: it migrates
// the old layout and saves folders found renamed, logging what it did.
func settleRegistry(f *rootFlags) (*accounts.Registry, error) {
	reg, err := accounts.Load(f.storeDir, f.archiveDir)
	if err != nil {
		return nil, err
	}
	notes, err := reg.Migrate(sessionJID, mayBeLinked)
	if len(notes) > 0 {
		logf("wacli now keeps one folder per WhatsApp account: %s", strings.Join(notes, "; "))
	}
	if err != nil {
		return nil, fmt.Errorf("moving into account folders: %w (run wacli again to finish)", err)
	}
	notes, err = reg.Locate(true)
	for _, n := range notes {
		logf("%s", n)
	}
	return reg, err
}

// openAccount opens a's archive, claiming its folder, and its session
// (client, unless given) and archiver; the caller starts it.
func openAccount(f *rootFlags, reg *accounts.Registry, a accounts.Account, client *wa.Client) (*account, error) {
	acct := &account{Account: a, wa: client}
	var err error
	if acct.ar, err = f.openArchive(reg, a, false); err != nil {
		return nil, err
	}
	if err := accounts.Claim(acct.ar.Root(), a.JID); err != nil {
		return nil, err
	}
	if acct.wa == nil {
		path := reg.SessionPath(a)
		if err := fsutil.EnsurePrivateDir(filepath.Dir(path)); err != nil {
			return nil, err
		}
		if acct.wa, err = wa.New(wa.Options{StorePath: path}); err != nil {
			return nil, err
		}
	}
	if acct.arc, err = archiver.New(acct.ar, acct.wa, accountLog(a)); err != nil {
		acct.wa.Close()
		return nil, err
	}
	return acct, nil
}

// accountLog prefixes messages with the account's folder.
func accountLog(a accounts.Account) func(string, ...any) {
	return func(format string, args ...any) { logf("%s: "+format, append([]any{a.Folder}, args...)...) }
}

// selectAccount is reg.Select, telling linked accounts by their session.
func selectAccount(reg *accounts.Registry, sel string) (accounts.Account, error) {
	a, err := reg.Select(sel, isLinked)
	if errors.Is(err, accounts.ErrNone) {
		err = errUnlinked
	}
	return a, err
}

// account returns the open account sel names ("" with one linked).
func (s *session) account(sel string) (*account, error) {
	a, err := selectAccount(s.reg, sel)
	if err != nil {
		return nil, err
	}
	for _, acct := range s.accounts {
		if acct.JID == a.JID {
			return acct, nil
		}
	}
	return nil, fmt.Errorf("%s: %w", a.Label(), errUnlinked) // logged out, its folder deleted
}

// linked is the open accounts that have a session.
func (s *session) linked() []*account {
	var out []*account
	for _, a := range s.accounts {
		if a.wa.IsAuthed() {
			out = append(out, a)
		}
	}
	return out
}

// start registers the archiver before connecting so the first history sync
// after login is not missed.
func (a *account) start(ctx context.Context, opts wa.ConnectOptions) error {
	a.arc.Start(ctx)
	return a.wa.Connect(ctx, opts)
}

// close shuts down in the order that loses nothing: no new events, finish
// the ones in flight and save, then let go of the sessions; a new link's
// session moves to its own store folder; then the lock goes.
func (s *session) close() {
	for _, a := range s.accounts {
		a.wa.Disconnect()
	}
	for _, a := range s.accounts {
		a.arc.Close()
	}
	for _, a := range s.accounts {
		a.wa.Close()
	}
	for _, a := range s.accounts {
		if err := s.reg.Settle(a.JID, mayBeLinked); err != nil {
			logf("warning: %v (wacli finishes it next time)", err)
		}
	}
	if s.lock != nil {
		_ = s.lock.Release()
	}
}

// waitLoggedIn waits for the server to accept the login, which comes a
// moment after Connect returns; requests sent before it fail.
func (a *account) waitLoggedIn(ctx context.Context) error {
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	timeout := time.After(30 * time.Second)
	for !a.wa.IsConnected() {
		if !a.wa.IsAuthed() {
			return errUnlinked // e.g. removed on the phone; whatsmeow forgot the session
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-a.arc.LoggedOut():
			return errUnlinked
		case <-a.arc.Halted():
			return haltErr(a.arc.Halt())
		case <-timeout:
			return wa.ErrNotConnected
		case <-t.C:
		}
	}
	return nil
}

// ended is the error a running account stopped with, if it did.
func (a *account) ended() error {
	select {
	case <-a.arc.LoggedOut():
		return errUnlinked
	case <-a.arc.Halted():
		return haltErr(a.arc.Halt())
	default:
		return nil
	}
}

// runUntilIdle returns once nothing new has been archived for idle in any
// of the accounts and the media that can be downloaded right now is on
// disk. An account WhatsApp ends is left out (and said so); the error is
// returned when it was the last. It returns the accounts still running.
func runUntilIdle(ctx context.Context, live []*account, idle time.Duration) ([]*account, error) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	// Idle counts from the login: linking, and a server busy reconnecting,
	// can take minutes before the first message.
	start := map[*account]time.Time{}
	quiet := func() bool {
		for _, a := range live {
			if s, ok := start[a]; !ok || min(a.arc.Idle(), time.Since(s)) < idle {
				return false
			}
		}
		return true
	}
	var last int64
	for i := 0; ; i++ {
		select {
		case <-ctx.Done():
			return live, nil
		case <-t.C:
		}
		var err error
		if live, err = dropEnded(live); err != nil {
			return nil, err
		}
		var n int64
		pending := 0
		for _, a := range live {
			if _, ok := start[a]; !ok && a.wa.IsConnected() {
				start[a] = time.Now()
			}
			n += a.arc.Added()
			pending += a.arc.PendingMedia()
		}
		if i%10 == 0 && n != last {
			fmt.Fprintf(os.Stderr, "\rsaved %d messages, %d attachments pending   ", n, pending)
			last = n
		}
		if quiet() {
			for _, a := range live {
				a.arc.WaitMedia(ctx)
			}
			if quiet() {
				fmt.Fprintln(os.Stderr)
				return live, nil
			}
		}
	}
}

// dropEnded leaves out accounts WhatsApp ended, saying so; with none left
// it returns why the last one ended.
func dropEnded(live []*account) ([]*account, error) {
	var kept []*account
	var last error
	for _, a := range live {
		if err := a.ended(); err != nil {
			if len(live) > 1 {
				logf("%s: %v; the other accounts carry on", a.Label(), err)
			}
			a.wa.Disconnect()
			last = err
			continue
		}
		kept = append(kept, a)
	}
	if len(kept) == 0 {
		return nil, last
	}
	return kept, nil
}

func (a *account) summary() map[string]any {
	return map[string]any{
		"account":       a.Folder,
		"dir":           a.ar.Root(),
		"chats":         len(a.ar.Chats()),
		"new_messages":  a.arc.Added(),
		"pending_media": a.arc.PendingMedia(),
	}
}

// haltErr is what a sync exits with when WhatsApp ends the connection.
func haltErr(h archiver.Halt) error {
	if h.Retry {
		return errors.New(h.Reason)
	}
	return fmt.Errorf("%w: %s", errHalted, h.Reason)
}

// lidOf maps a number to its hidden ID from stored mappings (no network).
func (a *account) lidOf(ctx context.Context) func(types.JID) types.JID {
	return func(j types.JID) types.JID { return a.wa.LookupPNToLID(ctx, j) }
}
