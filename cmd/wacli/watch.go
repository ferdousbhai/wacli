package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/ferdousbhai/wacli/internal/archiver"
	"github.com/ferdousbhai/wacli/internal/delegate"
	"github.com/spf13/cobra"
)

func newWatchCmd(f *rootFlags) *cobra.Command {
	var o watchOptions
	var chat string
	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Print new messages as JSON lines while `sync --follow` archives them",
		Long: `Prints one JSON object per line for every new, edited or deleted message
the running ` + "`wacli sync --follow`" + ` archives, once it is on disk:

  {"source":"whatsapp","id":"…","thread":"<chat JID>","chat":"<name>",
   "at":<unix ms>,"from":"<sender>","from_me":false,"kind":"text",
   "text":"…","media":"<file or empty>","ref":"<…/_chat.txt>"}

kind is text, media, system, edit or delete; an edit or delete has its own
id (<message id>:edit-<ms>, <message id>:delete-<ms>). Your own messages are
left out unless --include-mine. It opens no WhatsApp connection of its own
and exits non-zero when no sync is running or the sync stops.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if chat != "" {
				ar, err := f.openArchive(true)
				if err != nil {
					return err
				}
				jid, err := resolveChat(ar, chat, nil)
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
	return cmd
}

type watchOptions struct {
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
	if err := json.NewEncoder(conn).Encode(request{Op: "watch", Since: o.Since}); err != nil {
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

// watchSource is the running sync's archiver.
type watchSource interface {
	Subscribe() (<-chan archiver.Message, func())
	MessagesSince(since int64) []archiver.Message
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
	caught := map[string]bool{} // printed by the catch-up; live may repeat them
	if since > 0 {
		for _, m := range src.MessagesSince(since) {
			caught[m.ID] = true
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
			if caught[m.ID] {
				continue
			}
			if !send(watchFrame{Message: &m}) {
				return
			}
		}
	}
}
