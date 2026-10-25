package main

import (
	"fmt"
	"os"
	"time"

	"github.com/ferdousbhai/wacli/internal/out"
	"github.com/ferdousbhai/wacli/internal/wa"
	"github.com/spf13/cobra"
)

func newAuthCmd(f *rootFlags) *cobra.Command {
	var phone string
	var days int
	var idle time.Duration
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Link this computer to WhatsApp and archive your full history",
		Long: `Shows a QR code to scan in WhatsApp → Linked devices (or prints a pairing
code with --phone), asks the phone for its full history, and archives it with
all media. Linking again replaces the previous link.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			wa.SetHistorySyncLimits(wa.HistorySyncLimits{Full: true, Days: uint32(days)})
			s, err := openSession(f)
			if err != nil {
				return err
			}
			defer s.close()
			ctx, cancel := signalContext()
			defer cancel()
			if s.wa.IsAuthed() {
				fmt.Fprintln(os.Stderr, "Already linked; syncing. Run `wacli logout` first to link again with full history.")
			}
			opts := wa.ConnectOptions{AllowQR: true, PairPhoneNumber: phone, OnPairCode: func(code string) {
				fmt.Fprintf(os.Stderr, "Enter this code in WhatsApp → Linked devices → Link with phone number: %s\n", code)
			}}
			if err := s.start(ctx, opts); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "Linked. Archiving history into %s (keep the phone online)…\n", s.ar.Root())
			s.runUntilIdle(ctx, idle)
			return report(f, s.summary())
		},
	}
	cmd.Flags().StringVar(&phone, "phone", "", "pair with a code for this phone number instead of a QR code")
	cmd.Flags().IntVar(&days, "history-days", 0, "days of history to request (0 = everything the phone has)")
	cmd.Flags().DurationVar(&idle, "idle-exit", 3*time.Minute, "finish after this long without new history")
	return cmd
}

func newSyncCmd(f *rootFlags) *cobra.Command {
	var follow bool
	var idle time.Duration
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Archive new messages (keep running with --follow)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := openSession(f)
			if err != nil {
				return err
			}
			defer s.close()
			ctx, cancel := signalContext()
			defer cancel()
			if !s.wa.IsAuthed() {
				return errUnlinked
			}
			if err := s.start(ctx, wa.ConnectOptions{DetachSocket: follow}); err != nil {
				return err
			}
			if !follow {
				s.runUntilIdle(ctx, idle)
				return report(f, s.summary())
			}
			stop, err := serveIPC(ctx, f, s)
			if err != nil {
				return err
			}
			defer stop()
			fmt.Fprintf(os.Stderr, "Archiving into %s; Ctrl+C to stop.\n", s.ar.Root())
			select {
			case <-ctx.Done():
				return nil
			case <-s.arc.LoggedOut():
				return errUnlinked
			}
		},
	}
	cmd.Flags().BoolVar(&follow, "follow", false, "keep running and archive messages as they arrive")
	cmd.Flags().DurationVar(&idle, "idle-exit", 30*time.Second, "without --follow, finish after this long without new messages")
	return cmd
}

func report(f *rootFlags, data map[string]any) error {
	if f.asJSON {
		return out.WriteJSON(os.Stdout, data)
	}
	fmt.Printf("Archive: %v\nChats: %v\nNew messages: %v\nAttachments still pending: %v\n",
		data["archive"], data["chats"], data["new_messages"], data["pending_media"])
	return nil
}
