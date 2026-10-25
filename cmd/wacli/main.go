// Command wacli keeps WhatsApp chats as iPhone-style export folders.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ferdousbhai/wacli/internal/archive"
	"github.com/ferdousbhai/wacli/internal/config"
	"github.com/ferdousbhai/wacli/internal/out"
	"github.com/spf13/cobra"
)

const version = "0.26.0-archive"

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

func main() {
	if err := newRoot().Execute(); err != nil {
		if errors.Is(err, errUnlinked) {
			os.Exit(exitUnlinked)
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
	}
	root.PersistentFlags().StringVar(&f.storeDir, "store", envOr("WACLI_STORE_DIR", config.DefaultStoreDir()), "directory holding the WhatsApp session (session.db)")
	root.PersistentFlags().StringVar(&f.archiveDir, "archive", envOr("WACLI_ARCHIVE_DIR", defaultArchiveDir()), "directory holding the chat folders")
	root.PersistentFlags().StringVar(&f.dateFormat, "date-format", envOr("WACLI_DATE_FORMAT", archive.DefaultDateFormat), "Go time layout for entry timestamps, matching your phone's region")
	root.PersistentFlags().BoolVar(&f.asJSON, "json", false, "print machine-readable JSON")

	root.AddCommand(
		newAuthCmd(f),
		newSyncCmd(f),
		newBackfillCmd(f),
		newChatsCmd(f),
		newImportCmd(f),
		newSendCmd(f),
		newDoctorCmd(f),
		newLogoutCmd(f),
	)
	wrapErrors(root, f)
	return root
}

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
		}
		return err
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
	return archive.Open(archive.Options{Root: f.archiveDir, DateFormat: f.dateFormat, Location: time.Local, Logf: logf, ReadOnly: readOnly})
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}
