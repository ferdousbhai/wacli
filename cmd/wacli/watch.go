package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ferdousbhai/wacli/internal/archiver"
	"github.com/ferdousbhai/wacli/internal/delegate"
	"github.com/spf13/cobra"

	"go.mau.fi/whatsmeow/types"
)

func newWatchCmd(f *rootFlags) *cobra.Command {
	var o watchOptions
	var chat string
	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Print new messages as JSON lines while `sync --follow` archives them",
		Long: `Prints one JSON object per line for every new, edited or deleted message
the running ` + "`wacli sync --follow`" + ` archives, once it is on disk:

  {"source":"whatsapp","account":"<account folder>","id":"…",
   "thread":"<chat JID>","chat":"<name>","at":<unix ms>,"from":"<sender>",
   "from_me":false,"kind":"text","text":"…","media":"<file or empty>",
   "ref":"<…/<account folder>/WhatsApp Chat - <name>/_chat.txt>"}

kind is text, media, system, edit or delete; an edit or delete has its own
id (<message id>:edit-<ms>, <message id>:delete-<ms>). Every linked account
is watched unless --account. Your own messages are left out unless
--include-mine. It opens no WhatsApp connection of its own
and exits non-zero when no sync is running or the sync stops.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			o.Account = f.account
			if chat != "" {
				jid, err := resolveWatchedChat(f, chat)
				if err != nil {
					return err
				}
				o.Thread = jid.String()
			}
			ctx, cancel := signalContext()
			defer cancel()
			return watch(ctx, f.storeDir, o, os.Stdout)
		},
	}
	cmd.Flags().Int64Var(&o.Since, "since", 0, "first print archived messages sent since this time (unix ms), to catch up")
	cmd.Flags().StringVar(&chat, "chat", "", "only this chat (a name, number or JID)")
	cmd.Flags().BoolVar(&o.IncludeMine, "include-mine", false, "also print messages you sent")
	addAccountFlag(cmd, f, allAccountUsage)
	return cmd
}

// resolveWatchedChat finds the chat in the watched accounts' archives: the
// first that has it, or a number none has yet.
func resolveWatchedChat(f *rootFlags, chat string) (types.JID, error) {
	reg, err := readRegistry(f)
	if err != nil {
		return types.JID{}, err
	}
	list, err := reg.Filter(f.account)
	if err != nil {
		return types.JID{}, err
	}
	var firstErr error
	var number types.JID
	for _, a := range list {
		ar, err := f.openArchive(reg, a, true)
		if err != nil {
			return types.JID{}, err
		}
		jid, err := resolveChat(ar, chat, nil)
		if err != nil {
			firstErr = cmp.Or(firstErr, err)
			continue
		}
		if _, ok := ar.Info(jid.String()); ok {
			return jid, nil
		}
		number = jid
	}
	if !number.IsEmpty() {
		return number, nil
	}
	return types.JID{}, firstErr
}

type watchOptions struct {
	Account     string // only this account (--account); "" for all
	Since       int64
	Thread      string // only this chat; "" for all
	IncludeMine bool
}

// watchLine is the printed form: the contract shared by every `… watch`.
type watchLine struct {
	Source string `json:"source"`
	archiver.Message
}

// watchFrame is one line from the sync: ready once, then messages, or an
// error (an older sync answers "unknown op").
type watchFrame struct {
	Ready   bool              `json:"ready,omitempty"`
	Message *archiver.Message `json:"message,omitempty"`
	Error   string            `json:"error,omitempty"`
}

var errNoSync = errors.New("no `wacli sync --follow` is running to watch (start it: systemctl --user start wacli-sync)")

func watch(ctx context.Context, storeDir string, o watchOptions, w io.Writer) error {
	conn, err := delegate.Dial(ctx, storeDir)
	if err != nil {
		return fmt.Errorf("%w: %v", errNoSync, err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if err := json.NewEncoder(conn).Encode(request{Op: "watch", Account: o.Account, Since: o.Since}); err != nil {
		return err
	}
	dec := json.NewDecoder(conn)
	enc := json.NewEncoder(w)
	var last int64
	for {
		var fr watchFrame
		if err := dec.Decode(&fr); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return stopped(last, err)
		}
		switch {
		case fr.Error != "":
			if strings.HasPrefix(fr.Error, "unknown op") {
				return errors.New("the running sync is an older wacli without watch; update the wacli it runs, then systemctl --user restart wacli-sync")
			}
			return stopped(last, errors.New(fr.Error))
		case fr.Ready:
			logf("Watching for new messages; Ctrl+C to stop.")
		case fr.Message != nil:
			m := *fr.Message
			last = max(last, m.At)
			if (m.FromMe && !o.IncludeMine) || (o.Thread != "" && m.Thread != o.Thread) {
				continue
			}
			if err := enc.Encode(watchLine{Source: "whatsapp", Message: m}); err != nil {
				return err // stdout closed
			}
		}
	}
}

func stopped(last int64, err error) error {
	if errors.Is(err, io.EOF) {
		err = errors.New("the sync stopped")
	}
	if last == 0 {
		return fmt.Errorf("watch ended: %v", err)
	}
	return fmt.Errorf("watch ended: %v; run again with --since %d to catch up", err, last)
}

// watchSource is the running sync's archivers.
type watchSource interface {
	Subscribe() (<-chan archiver.Message, func())
	MessagesSince(since int64) []archiver.Message
}

// watchSource returns the accounts sel names (all for "") as one source.
func (s *session) watchSource(sel string) (watchSource, error) {
	list, err := s.reg.Filter(sel)
	if err != nil {
		return nil, err
	}
	var src multiSource
	for _, a := range list {
		for _, acct := range s.accounts {
			if acct.JID == a.JID && acct.wa.IsAuthed() {
				src = append(src, acct.arc)
			}
		}
	}
	if len(src) == 0 {
		return nil, errors.New("no linked account to watch")
	}
	return src, nil
}

// multiSource merges several accounts' changes.
type multiSource []watchSource

// Subscribe merges the accounts' streams; it closes when any of them does
// (that subscriber fell behind, or the sync is stopping).
func (m multiSource) Subscribe() (<-chan archiver.Message, func()) {
	out := make(chan archiver.Message)
	done := make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { close(done) }) }
	var wg sync.WaitGroup
	var cancels []func()
	for _, a := range m {
		ch, cancel := a.Subscribe()
		cancels = append(cancels, cancel)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				case msg, ok := <-ch:
					if !ok {
						stop()
						return
					}
					select {
					case out <- msg:
					case <-done:
						return
					}
				}
			}
		}()
	}
	go func() {
		wg.Wait()
		close(out)
	}()
	return out, func() {
		stop()
		for _, c := range cancels {
			c()
		}
	}
}

// MessagesSince is every account's, oldest first.
func (m multiSource) MessagesSince(since int64) []archiver.Message {
	var out []archiver.Message
	for _, a := range m {
		out = append(out, a.MessagesSince(since)...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At < out[j].At })
	return out
}

// serveWatch streams changes to a `wacli watch` until it leaves, the sync
// stops (quit), or it falls too far behind.
func serveWatch(ctx context.Context, quit <-chan struct{}, src watchSource, conn net.Conn, since int64) {
	ch, cancel := src.Subscribe()
	defer cancel()
	enc := json.NewEncoder(conn)
	send := func(fr watchFrame) bool {
		_ = conn.SetWriteDeadline(time.Now().Add(30 * time.Second)) // a client that stops reading
		return enc.Encode(fr) == nil
	}
	if !send(watchFrame{Ready: true}) {
		return
	}
	caught := map[string]bool{} // account/ID printed by the catch-up; live may repeat them
	if since > 0 {
		for _, m := range src.MessagesSince(since) {
			caught[m.Account+"/"+m.ID] = true
			if !send(watchFrame{Message: &m}) {
				return
			}
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-quit:
			return
		case m, ok := <-ch:
			if !ok {
				send(watchFrame{Error: "fell behind, or the sync is stopping"})
				return
			}
			if caught[m.Account+"/"+m.ID] { // two accounts in a chat share IDs
				continue
			}
			if !send(watchFrame{Message: &m}) {
				return
			}
		}
	}
}
