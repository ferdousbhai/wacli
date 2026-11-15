// Package accounts keeps track of the WhatsApp accounts wacli is linked to:
// each has a session under the store (<store>/<store dir>/session.db) and a
// folder under the archive (<archive>/<folder>/, holding its chat folders).
//
// The list lives in <store>/accounts.json. Each account folder holds its
// owner's JID in .wacli/account, so a folder renamed by hand (say
// "+15551234567" → "Personal") is found again by that file.
package accounts

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ferdousbhai/wacli/internal/fsutil"
)

const (
	registryFile = "accounts.json"
	metaDir      = ".wacli" // the archive's bookkeeping folder
	idFile       = "account"
	// SessionFile is the whatsmeow store in each account's store folder.
	SessionFile = "session.db"
	// Pending is the store folder a new link pairs in, before its number is
	// known.
	Pending = "pending"
)

// sessionFiles are the session store's files, the database last: while it
// is still in a folder, that folder's copy is the one in use.
var sessionFiles = []string{SessionFile + "-wal", SessionFile + "-shm", SessionFile + "-journal", SessionFile}

// Account is one linked (or once linked) WhatsApp account.
type Account struct {
	JID    string `json:"jid"`    // the account's own JID, 15551234567@s.whatsapp.net
	Folder string `json:"folder"` // its folder in the archive
	Store  string `json:"store"`  // its folder in the store; Key(JID) once settled
	// LegacyStore and LegacyArchive mark a move from the single-account
	// layout in progress: the session, or the chat folders, still (partly)
	// at the top of the store or archive belong to this account.
	LegacyStore   bool `json:"legacy_store,omitempty"`
	LegacyArchive bool `json:"legacy_archive,omitempty"`
}

// Key is the account's number as wacli writes numbers ("+15551234567"):
// the default name of its archive folder and the name of its store folder.
func Key(jid string) string {
	user, _, _ := strings.Cut(jid, "@")
	user, _, _ = strings.Cut(user, ":") // a device JID
	return "+" + user
}

// Label names the account for people: its folder, and its number if the
// folder was renamed.
func (a Account) Label() string {
	if strings.EqualFold(a.Folder, Key(a.JID)) {
		return a.Folder
	}
	return fmt.Sprintf("%s (%s)", a.Folder, Key(a.JID))
}

// Registry is <store>/accounts.json.
type Registry struct {
	StoreDir   string
	ArchiveDir string
	Accounts   []Account
}

type registryJSON struct {
	Accounts []Account `json:"accounts"`
}

// Load reads the registry; a store without one has no accounts yet.
func Load(storeDir, archiveDir string) (*Registry, error) {
	r := &Registry{StoreDir: storeDir, ArchiveDir: archiveDir}
	data, err := os.ReadFile(filepath.Join(storeDir, registryFile))
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	var j registryJSON
	if err := json.Unmarshal(data, &j); err != nil {
		return nil, fmt.Errorf("%s: %w", registryFile, err)
	}
	r.Accounts = j.Accounts
	return r, nil
}

// Save writes the registry atomically.
func (r *Registry) Save() error {
	if err := fsutil.EnsurePrivateDir(r.StoreDir); err != nil {
		return err
	}
	return fsutil.WriteAtomic(filepath.Join(r.StoreDir, registryFile), "", 0o600, func(w *bufio.Writer) error {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(registryJSON{Accounts: r.Accounts})
	})
}

// Find returns the account with this JID.
func (r *Registry) Find(jid string) (*Account, bool) {
	for i := range r.Accounts {
		if r.Accounts[i].JID == jid {
			return &r.Accounts[i], true
		}
	}
	return nil, false
}

// ArchiveRoot is the account's folder in the archive.
func (r *Registry) ArchiveRoot(a Account) string { return filepath.Join(r.ArchiveDir, a.Folder) }

// SessionPath is the account's session store.
func (r *Registry) SessionPath(a Account) string {
	return filepath.Join(r.StoreDir, a.Store, SessionFile)
}

// Add registers a newly linked account, with a free archive folder named
// after its number, pairing in store folder store.
func (r *Registry) Add(jid, store string) *Account {
	r.Accounts = append(r.Accounts, Account{JID: jid, Folder: r.freeFolder(jid), Store: store})
	sort.SliceStable(r.Accounts, func(i, j int) bool { return r.Accounts[i].JID < r.Accounts[j].JID })
	a, _ := r.Find(jid)
	return a
}

// freeFolder is Key(jid), or "Key (2)" … when that name is taken by a folder
// that isn't this account's, or by another account.
func (r *Registry) freeFolder(jid string) string {
	base := Key(jid)
	for n := 1; ; n++ {
		name := base
		if n > 1 {
			name = fmt.Sprintf("%s (%d)", base, n)
		}
		taken := false
		for _, a := range r.Accounts {
			if a.JID != jid && strings.EqualFold(a.Folder, name) {
				taken = true
			}
		}
		path := filepath.Join(r.ArchiveDir, name)
		if id, _ := readID(path); !taken && (id == jid || !exists(path)) {
			return name
		}
	}
}

// Claim marks root as jid's account folder.
func Claim(root, jid string) error {
	dir := filepath.Join(root, metaDir)
	if id, _ := readID(root); id == jid {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return fsutil.WriteAtomic(filepath.Join(dir, idFile), "", 0o600, func(w *bufio.Writer) error {
		_, err := w.WriteString(jid + "\n")
		return err
	})
}

// readID is the JID root's .wacli/account names ("" without one).
func readID(root string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, metaDir, idFile))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// Locate finds each account's folder: the registered one if it's the
// account's (its .wacli/account names it, or names nobody yet), else the
// folder in the archive whose .wacli/account names it (renamed by hand).
// A registered folder another account claims is given up for a new one.
// It returns what changed, for the caller to log, and saves when save.
func (r *Registry) Locate(save bool) ([]string, error) {
	var notes []string
	claims := r.claims()
	for i := range r.Accounts {
		a := &r.Accounts[i]
		if a.LegacyArchive {
			continue // the move puts it in place
		}
		id, _ := readID(filepath.Join(r.ArchiveDir, a.Folder))
		if id == a.JID {
			continue
		}
		if found, ok := claims[a.JID]; ok {
			notes = append(notes, fmt.Sprintf("account %s: its folder %q is now %q", Key(a.JID), a.Folder, found))
			a.Folder = found
			continue
		}
		if id == "" {
			continue // not made yet, or made before it was claimed
		}
		old := a.Folder
		a.Folder = r.freeFolder(a.JID)
		notes = append(notes, fmt.Sprintf("account %s: %q belongs to %s, so its chats go to %q", Key(a.JID), old, Key(id), a.Folder))
	}
	if len(notes) > 0 && save {
		return notes, r.Save()
	}
	return notes, nil
}

// claims maps each JID to the archive folder that claims it (the first by
// name if a copy claims it too).
func (r *Registry) claims() map[string]string {
	out := map[string]string{}
	entries, err := os.ReadDir(r.ArchiveDir)
	if err != nil {
		return out
	}
	for _, e := range entries { // sorted by name
		if !e.IsDir() {
			continue
		}
		if id, err := readID(filepath.Join(r.ArchiveDir, e.Name())); err == nil && id != "" {
			if _, ok := out[id]; !ok {
				out[id] = e.Name()
			}
		}
	}
	return out
}

// ErrNone is returned when no account has been linked yet.
var ErrNone = errors.New("no WhatsApp account is linked")

// Filter returns the accounts sel names (all of them for ""): a folder
// name, a number in any spelling, or a JID.
func (r *Registry) Filter(sel string) ([]Account, error) {
	sel = strings.TrimSpace(sel)
	if len(r.Accounts) == 0 {
		return nil, ErrNone
	}
	if sel == "" {
		return r.Accounts, nil
	}
	digits := strings.Map(func(c rune) rune {
		if c >= '0' && c <= '9' {
			return c
		}
		return -1
	}, sel)
	number := digits != "" && strings.Trim(sel, "+0123456789 -()") == ""
	for _, a := range r.Accounts {
		user := strings.TrimPrefix(Key(a.JID), "+")
		if strings.EqualFold(a.Folder, sel) || sel == a.JID || number && digits == user {
			return []Account{a}, nil
		}
	}
	return nil, fmt.Errorf("no account %q; the accounts are %s", sel, r.list())
}

// Select returns the one account sel names; sel may be empty only when
// there is a single account.
func (r *Registry) Select(sel string) (Account, error) {
	list, err := r.Filter(sel)
	if err != nil {
		return Account{}, err
	}
	if len(list) > 1 {
		return Account{}, fmt.Errorf("%d WhatsApp accounts are linked; choose one with --account (or WACLI_ACCOUNT): %s", len(list), r.list())
	}
	return list[0], nil
}

func (r *Registry) list() string {
	var names []string
	for _, a := range r.Accounts {
		names = append(names, a.Label())
	}
	return strings.Join(names, ", ")
}

// LegacyArchive reports whether chat folders of the single-account layout
// sit at the top of the archive.
func (r *Registry) LegacyArchive() bool {
	return exists(filepath.Join(r.ArchiveDir, metaDir, "chats.json"))
}

// LegacySession is the single-account layout's session store, if there.
func (r *Registry) LegacySession() (string, bool) {
	p := filepath.Join(r.StoreDir, SessionFile)
	return p, exists(p)
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
