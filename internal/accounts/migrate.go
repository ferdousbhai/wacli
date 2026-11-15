package accounts

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ferdousbhai/wacli/internal/archive"
	"github.com/ferdousbhai/wacli/internal/fsutil"
)

// NeedsMigration reports whether the single-account layout (session.db at
// the top of the store) is still to be moved, or a move is unfinished.
// linked tells, without changing it, whether a session store is linked: an
// unlinked one has no number to move it under.
func (r *Registry) NeedsMigration(linked func(path string) bool) bool {
	for _, a := range r.Accounts {
		if a.LegacyStore || a.LegacyArchive || a.Store != Key(a.JID) {
			return true
		}
	}
	p, ok := r.LegacySession()
	return ok && linked(p)
}

// Migrate moves the single-account layout into an account: the session at
// the top of the store into <store>/<number>/, the chat folders and .wacli
// at the top of the archive into <archive>/<number>/. jidOf reads the
// linked JID of a session store ("" if unlinked); it may checkpoint it, so
// it is called only before anything moves. Every step is a rename, recorded
// in the registry first, so an interrupted migration finishes on the next
// call, and a finished one does nothing. It also finishes a pairing that
// ended before its session reached its store folder (see Settle). Run it
// under the store lock. It returns what it moved, to log.
func (r *Registry) Migrate(jidOf func(path string) (string, error), unlinked func(path string) bool) ([]string, error) {
	if p, ok := r.LegacySession(); ok && !r.legacyInProgress() {
		jid, err := jidOf(p)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", p, err)
		}
		if jid != "" {
			if _, dup := r.Find(jid); dup {
				return nil, fmt.Errorf("%s and %s both hold a session for %s; remove the one you don't use", p, filepath.Join(r.StoreDir, Key(jid)), Key(jid))
			}
			a := r.Add(jid, Key(jid))
			a.LegacyStore = true
			a.LegacyArchive = r.LegacyArchive() && r.legacyArchiveOwner() == ""
			if err := r.Save(); err != nil {
				return nil, err
			}
		}
	}
	var notes []string
	for i := range r.Accounts {
		a := &r.Accounts[i]
		if a.LegacyStore {
			if err := r.moveLegacyStore(*a); err != nil {
				return notes, err
			}
			notes = append(notes, fmt.Sprintf("moved the session into %s", filepath.Join(r.StoreDir, a.Store)))
			a.LegacyStore = false
			if err := r.Save(); err != nil {
				return notes, err
			}
		}
		if a.LegacyArchive {
			n, err := r.moveLegacyArchive(*a)
			if err != nil {
				return notes, err
			}
			notes = append(notes, movedNote(n, r.ArchiveRoot(*a)))
			a.LegacyArchive = false
			if err := r.Save(); err != nil {
				return notes, err
			}
		}
		if a.Store != Key(a.JID) {
			if err := r.Settle(a.JID, unlinked); err != nil {
				return notes, err
			}
		}
	}
	return notes, nil
}

func (r *Registry) legacyInProgress() bool {
	for _, a := range r.Accounts {
		if a.LegacyStore {
			return true
		}
	}
	return false
}

// legacyArchiveOwner is the account already moving the top-level chat
// folders, if one is.
func (r *Registry) legacyArchiveOwner() string {
	for _, a := range r.Accounts {
		if a.LegacyArchive {
			return a.JID
		}
	}
	return ""
}

// AdoptLegacyArchive gives the chat folders at the top of the archive to
// jid, the first account linked after the single-account session was
// unlinked (as relinking used to append to them). Only when it is the only
// account: with others, nobody can tell whose they are.
func (r *Registry) AdoptLegacyArchive(jid string) ([]string, error) {
	a, ok := r.Find(jid)
	if !ok || len(r.Accounts) != 1 || !r.LegacyArchive() || r.legacyArchiveOwner() != "" {
		return nil, nil
	}
	a.LegacyArchive = true // recorded first: Migrate finishes an interrupted move
	if err := r.Save(); err != nil {
		return nil, err
	}
	n, err := r.moveLegacyArchive(*a)
	if err != nil {
		return nil, err
	}
	a.LegacyArchive = false
	return []string{movedNote(n, r.ArchiveRoot(*a))}, r.Save()
}

// rename is os.Rename; tests stop it partway to interrupt a migration.
var rename = os.Rename

// moveFile renames from to to unless it was moved already.
func moveFile(from, to string) (bool, error) {
	if !exists(from) {
		return false, nil
	}
	if exists(to) {
		return false, fmt.Errorf("can't move %s: %s exists", from, to)
	}
	if err := rename(from, to); err != nil {
		return false, err
	}
	return true, nil
}

func (r *Registry) moveLegacyStore(a Account) error {
	dir := filepath.Join(r.StoreDir, a.Store)
	if err := fsutil.EnsurePrivateDir(dir); err != nil {
		return err
	}
	for _, name := range sessionFiles { // the database last
		if _, err := moveFile(filepath.Join(r.StoreDir, name), filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	fsutil.Sync(r.StoreDir)
	fsutil.Sync(dir)
	return nil
}

// moveLegacyArchive moves the chat folders, then .wacli (whose chats.json
// lists them, so it goes last), into the account's folder.
func (r *Registry) moveLegacyArchive(a Account) (int, error) {
	dst := r.ArchiveRoot(a)
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return 0, err
	}
	top := filepath.Join(r.ArchiveDir, metaDir)
	n := 0
	if exists(top) {
		folders, err := archive.Folders(r.ArchiveDir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return 0, err
		}
		for _, f := range folders {
			moved, err := moveFile(filepath.Join(r.ArchiveDir, f), filepath.Join(dst, f))
			if err != nil {
				return n, err
			}
			if moved {
				n++
			}
		}
		if _, err := moveFile(top, filepath.Join(dst, metaDir)); err != nil {
			return n, err
		}
	}
	fsutil.Sync(r.ArchiveDir)
	fsutil.Sync(dst)
	return n, Claim(dst, a.JID)
}

// Settle moves the session jid paired in (its Store, Pending for a new
// link) into its own store folder, Key(jid), once the session is closed. A
// session already there is replaced only if unlinked (relinking an account
// that was logged out); its files are removed. Each step can be repeated, so
// an interrupted Settle finishes on the next.
func (r *Registry) Settle(jid string, unlinked func(path string) bool) error {
	a, ok := r.Find(jid)
	if !ok {
		return fmt.Errorf("account %s isn't registered", Key(jid))
	}
	key := Key(jid)
	if a.Store == key {
		return nil
	}
	from, to := filepath.Join(r.StoreDir, a.Store), filepath.Join(r.StoreDir, key)
	if exists(filepath.Join(from, SessionFile)) {
		if old := filepath.Join(to, SessionFile); exists(old) {
			if !unlinked(old) {
				return fmt.Errorf("%s is still linked; log it out first (`wacli logout --account %s`)", old, key)
			}
			for _, name := range sessionFiles { // the database last: it marks the old session as still there
				if err := os.Remove(filepath.Join(to, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
					return err
				}
			}
		}
		if err := fsutil.EnsurePrivateDir(to); err != nil {
			return err
		}
		for _, name := range sessionFiles {
			if _, err := moveFile(filepath.Join(from, name), filepath.Join(to, name)); err != nil {
				return err
			}
		}
		fsutil.Sync(to)
	}
	_ = os.Remove(from) // empty now; left if something else is in it
	fsutil.Sync(r.StoreDir)
	a.Store = key
	return r.Save()
}

func movedNote(n int, dst string) string {
	if n == 1 {
		return "moved 1 chat folder and .wacli into " + dst
	}
	return fmt.Sprintf("moved %d chat folders and .wacli into %s", n, dst)
}
