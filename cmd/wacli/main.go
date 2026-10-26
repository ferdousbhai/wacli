// Command wacli keeps WhatsApp chats as iPhone-style export folders.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"syscall"
	"time"

	"github.com/ferdousbhai/wacli/internal/archive"
	"github.com/ferdousbhai/wacli/internal/config"
	"github.com/ferdousbhai/wacli/internal/out"
	"github.com/spf13/cobra"
)

const version = "0.26.0"

type rootFlags struct {
	storeDir   string
	archiveDir string
	dateFormat string
	asJSON     bool
}

// errUnlinked exits with exitUnlinked so a service manager stops restarting
// (systemd: RestartPreventExitStatus=3) until someone runs `wacli auth`.
var errUnlinked = errors.New("this computer is not linked to WhatsApp (or was unlinked); run `wacli auth`")

const exitUnlinked = 3

// errHalted exits with exitHalted: WhatsApp ended the connection in a way a
// restart wouldn't fix (another client took over, a ban, an outdated client).
var errHalted = errors.New("sync stopped")

const exitHalted = 4

func main() {
	refreshSkill()
	if err := newRoot().Execute(); err != nil {
		if !errors.As(err, new(printedError)) {
			// Cobra's own: an unknown command, a bad flag or argument.
			_ = out.WriteError(os.Stderr, slices.Contains(os.Args[1:], "--json") || slices.Contains(os.Args[1:], "--json=true"), err)
		}
		if errors.Is(err, errUnlinked) {
			os.Exit(exitUnlinked)
		}
		if errors.Is(err, errHalted) {
			os.Exit(exitHalted)
		}
		os.Exit(1)
	}
}

func newRoot() *cobra.Command {
	f := &rootFlags{}
	root := &cobra.Command{
		Use:   "wacli",
		Short: "Keep WhatsApp chats as iPhone-style export folders",
		Long: `wacli links to WhatsApp as a companion device and keeps every chat as a
folder shaped like an iPhone "Export chat → Attach Media" export:

  ~/WhatsApp/WhatsApp Chat - <name>/_chat.txt  (+ media files)

Read the folders with any tool: cat, grep -r, tail -f, an editor.`,
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
		// Absolute paths: the session store is opened by URI, which a
		// relative path breaks, and IPC names its socket after the store.
		PersistentPreRunE: func(*cobra.Command, []string) error {
			for _, p := range []*string{&f.storeDir, &f.archiveDir} {
				if *p == "" {
					continue // left to the "required" error
				}
				abs, err := filepath.Abs(*p)
				if err != nil {
					return err
				}
				*p = abs
			}
			return nil
		},
	}
	root.PersistentFlags().StringVar(&f.storeDir, "store", config.DefaultStoreDir(), "directory holding the WhatsApp session (session.db)")
	root.PersistentFlags().StringVar(&f.archiveDir, "archive", envOr("WACLI_ARCHIVE_DIR", defaultArchiveDir()), "directory holding the chat folders")
	root.PersistentFlags().StringVar(&f.dateFormat, "date-format", envOr("WACLI_DATE_FORMAT", archive.DefaultDateFormat), "Go time layout for entry timestamps, matching your phone's region")
	root.PersistentFlags().BoolVar(&f.asJSON, "json", false, "print machine-readable JSON")

	root.AddCommand(
		newAuthCmd(f),
		newSyncCmd(f),
		newBackfillCmd(f),
		newRefetchCmd(f),
		newChatsCmd(f),
		newImportCmd(f),
		newSendCmd(f),
		newDoctorCmd(f),
		newLogoutCmd(f),
		newSkillCmd(),
	)
	wrapErrors(root, f)
	return root
}

// printedError is a command error wrapErrors already printed.
type printedError struct{ error }

func (e printedError) Unwrap() error { return e.error }

// wrapErrors prints command errors once, as JSON when asked.
func wrapErrors(c *cobra.Command, f *rootFlags) {
	for _, sub := range c.Commands() {
		wrapErrors(sub, f)
	}
	if c.RunE == nil {
		return
	}
	run := c.RunE
	c.RunE = func(cmd *cobra.Command, args []string) error {
		err := run(cmd, args)
		if err != nil {
			_ = out.WriteError(os.Stderr, f.asJSON, err)
			return printedError{err}
		}
		return nil
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func defaultArchiveDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "WhatsApp"
	}
	return filepath.Join(home, "WhatsApp")
}

// openArchive opens the archive; readOnly for commands that may run next to
// the sync process that owns the files.
func (f *rootFlags) openArchive(readOnly bool) (*archive.Archive, error) {
	if _, err := time.Parse(f.dateFormat, time.Now().Format(f.dateFormat)); err != nil {
		return nil, fmt.Errorf("--date-format: %w", err)
	}
	if time.Unix(1, 0).UTC().Format(f.dateFormat) == time.Unix(0, 0).UTC().Format(f.dateFormat) {
		return nil, fmt.Errorf("--date-format %q has no seconds; iPhone exports always show them", f.dateFormat)
	}
	return archive.Open(archive.Options{Root: f.archiveDir, DateFormat: f.dateFormat, Location: time.Local, Logf: logf, ReadOnly: readOnly})
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}
