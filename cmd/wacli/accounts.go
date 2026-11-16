package main

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/ferdousbhai/wacli/internal/accounts"
	"github.com/ferdousbhai/wacli/internal/archive"
	"github.com/ferdousbhai/wacli/internal/lock"
	"github.com/ferdousbhai/wacli/internal/sqliteutil"
	_ "github.com/mattn/go-sqlite3"
	"github.com/spf13/cobra"

	"go.mau.fi/whatsmeow/types"
)

// addAccountFlag adds --account, defaulting to WACLI_ACCOUNT.
func addAccountFlag(cmd *cobra.Command, f *rootFlags, usage string) {
	cmd.PersistentFlags().StringVar(&f.account, "account", os.Getenv("WACLI_ACCOUNT"), usage+" (number or account folder name; env WACLI_ACCOUNT)")
}

const (
	oneAccountUsage = "the WhatsApp account to use, needed when several are linked"
	allAccountUsage = "only this WhatsApp account (default: all)"
)

var errOldSync = errors.New("the archive still has the layout from before accounts, and another wacli (an older `sync --follow`?) holds the store, so it can't be moved now; restart the sync with this wacli (systemctl --user restart wacli-sync) and it moves it")

// readRegistry loads the accounts for a command that only reads. If the
// old layout is still to be moved it takes the store lock to do so, unless a
// sync holds it.
func readRegistry(f *rootFlags) (*accounts.Registry, error) {
	reg, err := accounts.Load(f.storeDir, f.archiveDir)
	if err != nil {
		return nil, err
	}
	if reg.NeedsMigration(isLinked) {
		l, err := lock.Acquire(f.storeDir)
		switch {
		case err == nil:
			reg, err = settleRegistry(f) // it follows renames too
			_ = l.Release()
			return reg, err
		case !lock.IsLocked(err):
			return nil, err
		default:
			// A sync holds it: fine if it's only finishing a link, which it
			// does on its own; the old layout it can't be reading for us.
			if p, ok := reg.LegacySession(); ok && isLinked(p) && len(reg.Accounts) == 0 {
				return nil, errOldSync
			}
		}
	}
	_, err = reg.Locate(false)
	return reg, err
}

// openArchive opens account a's archive; readOnly for commands that may
// run next to the sync process that owns the files. A new account's archive
// takes the date format another account records, unless one is given.
func (f *rootFlags) openArchive(reg *accounts.Registry, a accounts.Account, readOnly bool) (*archive.Archive, error) {
	root := reg.ArchiveRoot(a)
	layout := f.dateFormat
	if layout != "" { // "": the archive's own, or the default
		if err := archive.ValidDateFormat(layout); err != nil {
			// Typed, or setting up a new archive (it would decide the
			// format for good): an error. Otherwise, below.
			if f.typedDate || !archive.Exists(root) {
				return nil, err
			}
			// From the environment, maybe a service's: not worth failing a
			// start over, as the archive's own format is used anyway.
			logf("note: ignoring WACLI_DATE_FORMAT: %v", err)
			layout = ""
		}
	}
	if layout == "" && !archive.Exists(root) {
		for _, other := range reg.Accounts {
			if other.JID != a.JID {
				if layout = archive.RecordedDateFormat(reg.ArchiveRoot(other)); layout != "" {
					break
				}
			}
		}
	}
	return archive.Open(archive.Options{Root: root, DateFormat: layout, Location: time.Local, Logf: logf, ReadOnly: readOnly})
}

// sessionJID is the JID a session store is linked to, "" if none. The
// database is checkpointed first, so the move that follows takes one
// self-contained file. Only under the store lock.
func sessionJID(path string) (string, error) {
	db, err := sql.Open("sqlite3", sqliteutil.FileURI(path, "_busy_timeout=5000"))
	if err != nil {
		return "", err
	}
	defer db.Close()
	var jid string
	err = db.QueryRow("SELECT jid FROM whatsmeow_device LIMIT 1").Scan(&jid)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		var n int // no table yet: never linked; any other failure is an error
		if db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='whatsmeow_device'").Scan(&n) == nil && n == 0 {
			return "", nil
		}
		return "", err
	}
	if _, err := db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return "", err
	}
	j, err := types.ParseJID(jid)
	if err != nil {
		return "", fmt.Errorf("device %q: %w", jid, err)
	}
	return j.ToNonAD().String(), nil
}

// isLinked reads the session store without changing it (opening it through
// whatsmeow would create or migrate it, and doctor must only look). One that
// can't be read counts as unlinked: for showing and choosing, never for
// replacing (mayBeLinked).
func isLinked(path string) bool {
	if _, err := os.Stat(path); err != nil {
		return false
	}
	db, err := sql.Open("sqlite3", sqliteutil.FileURI(path, "mode=ro"))
	if err != nil {
		return false
	}
	defer db.Close()
	var n int
	return db.QueryRow("SELECT COUNT(*) FROM whatsmeow_device").Scan(&n) == nil && n > 0
}

// mayBeLinked is whether a session store may still be linked; one that
// can't be read counts as linked, so it is never replaced. Only under the
// store lock, with the store closed.
func mayBeLinked(path string) bool {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return false // and opening it would make it
	}
	jid, err := sessionJID(path)
	return err != nil || jid != ""
}
