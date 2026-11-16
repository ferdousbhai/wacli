package accounts

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

const (
	alex = "15551234567@s.whatsapp.net"
	sam  = "15557654321@s.whatsapp.net"
)

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

// tree lists every file under dir with its contents.
func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		data, _ := os.ReadFile(p)
		out[filepath.ToSlash(rel)] = string(data)
		return nil
	})
	return out
}

// legacy lays out a single-account store and archive as wacli 0.26 left
// them: session.db at the top of the store, chat folders and .wacli at the
// top of the archive (one chat mid-rename), and a file of the owner's.
func legacy(t *testing.T) (store, arch string) {
	base := t.TempDir()
	store, arch = filepath.Join(base, "store"), filepath.Join(base, "WhatsApp")
	write(t, filepath.Join(store, "session.db"), "db")
	write(t, filepath.Join(store, "session.db-wal"), "wal")
	write(t, filepath.Join(store, "LOCK"), "")
	write(t, filepath.Join(arch, ".wacli", "chats.json"), `{
		"111@s.whatsapp.net": {"folder": "WhatsApp Chat - Maya", "name": "Maya", "kind": "dm"},
		"222@g.us": {"folder": "WhatsApp Chat - Hiking", "prev_folder": "WhatsApp Chat - Hikes", "name": "Hiking", "kind": "group"}
	}`)
	write(t, filepath.Join(arch, ".wacli", "index", "111@s.whatsapp.net.tsv"), "row\n")
	write(t, filepath.Join(arch, "WhatsApp Chat - Maya", "_chat.txt"), "[1/2/24, 10:00:00] Maya: hi\n")
	write(t, filepath.Join(arch, "WhatsApp Chat - Maya", "00000001-PHOTO.jpg"), "jpg")
	write(t, filepath.Join(arch, "WhatsApp Chat - Hikes", "_chat.txt"), "[1/2/24, 11:00:00] Kim: up?\n")
	write(t, filepath.Join(arch, "notes.txt"), "mine")
	return store, arch
}

func jidOf(want string) func(string) (string, error) {
	return func(string) (string, error) { return want, nil }
}

// noneLinked is the linked callback when no session store is linked.
func noneLinked(string) bool { return false }

var migrated = map[string]string{
	"+15551234567/.wacli/account":                          alex + "\n",
	"+15551234567/.wacli/chats.json":                       "",
	"+15551234567/.wacli/index/111@s.whatsapp.net.tsv":     "row\n",
	"+15551234567/WhatsApp Chat - Maya/_chat.txt":          "[1/2/24, 10:00:00] Maya: hi\n",
	"+15551234567/WhatsApp Chat - Maya/00000001-PHOTO.jpg": "jpg",
	"+15551234567/WhatsApp Chat - Hikes/_chat.txt":         "[1/2/24, 11:00:00] Kim: up?\n",
	"notes.txt": "mine",
}

func checkMigrated(t *testing.T, store, arch string) {
	t.Helper()
	got := tree(t, arch)
	if got["+15551234567/.wacli/chats.json"] == "" {
		t.Errorf("chats.json didn't move: %v", keys(got))
	}
	got["+15551234567/.wacli/chats.json"] = ""
	if !reflect.DeepEqual(got, migrated) {
		t.Errorf("archive:\n%v\nwant\n%v", keys(got), keys(migrated))
	}
	want := map[string]string{"+15551234567/session.db": "db", "+15551234567/session.db-wal": "wal", "LOCK": ""}
	st := tree(t, store)
	delete(st, "accounts.json")
	if !reflect.DeepEqual(st, want) {
		t.Errorf("store: %v", st)
	}
	r, err := Load(store, arch)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Accounts) != 1 || r.Accounts[0] != (Account{JID: alex, Folder: "+15551234567", Store: "+15551234567"}) {
		t.Errorf("registry %+v", r.Accounts)
	}
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestMigrateMovesTheSingleAccountLayoutOnce(t *testing.T) {
	store, arch := legacy(t)
	r, _ := Load(store, arch)
	if !r.NeedsMigration(func(string) bool { return true }) {
		t.Fatal("old layout not noticed")
	}
	notes, err := r.Migrate(jidOf(alex), noneLinked)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 2 || !strings.Contains(notes[1], "moved 2 chat folders") {
		t.Errorf("notes %q", notes)
	}
	checkMigrated(t, store, arch)

	r, _ = Load(store, arch)
	if r.NeedsMigration(func(string) bool { return true }) {
		t.Error("still needs migrating")
	}
	notes, err = r.Migrate(func(string) (string, error) { t.Fatal("session read again"); return "", nil }, noneLinked)
	if err != nil || len(notes) != 0 {
		t.Errorf("second run: %q %v", notes, err)
	}
	checkMigrated(t, store, arch)
}

// TestMigrateInterruptedAnywhereFinishesLater stops the migration at every
// rename in turn (as a crash or power cut would), then runs it again.
func TestMigrateInterruptedAnywhereFinishesLater(t *testing.T) {
	defer func() { rename = os.Rename }()
	for stopAt := 0; ; stopAt++ {
		store, arch := legacy(t)
		n := 0
		rename = func(from, to string) error {
			if n == stopAt {
				return errors.New("power cut")
			}
			n++
			return os.Rename(from, to)
		}
		r, _ := Load(store, arch)
		_, err := r.Migrate(jidOf(alex), noneLinked)
		rename = os.Rename
		if err == nil {
			if stopAt < 4 {
				t.Fatalf("only %d renames", stopAt)
			}
			checkMigrated(t, store, arch)
			return
		}
		r, _ = Load(store, arch)
		if !r.NeedsMigration(func(string) bool { return false }) {
			t.Fatalf("stop %d: unfinished move not noticed", stopAt)
		}
		// The session is read once, before anything moves.
		if _, err := r.Migrate(func(string) (string, error) { return "", errors.New("read again") }, noneLinked); err != nil {
			t.Fatalf("stop %d: resume: %v", stopAt, err)
		}
		checkMigrated(t, store, arch)
	}
}

func TestUnlinkedLegacySessionStaysAndItsChatsGoToTheNextLink(t *testing.T) {
	store, arch := legacy(t)
	r, _ := Load(store, arch)
	if r.NeedsMigration(func(string) bool { return false }) {
		t.Error("an unlinked session has no account to move into")
	}
	if notes, err := r.Migrate(jidOf(""), noneLinked); err != nil || len(notes) != 0 {
		t.Fatalf("%q %v", notes, err)
	}
	if !r.LegacyArchive() {
		t.Fatal("chat folders moved without an account")
	}
	// `wacli auth` links the first account: it adopts them.
	r.Add(alex, Pending)
	notes, err := r.AdoptLegacyArchive(alex)
	if err != nil || len(notes) != 1 {
		t.Fatalf("%q %v", notes, err)
	}
	if got := tree(t, arch); got["+15551234567/WhatsApp Chat - Maya/_chat.txt"] == "" || got["+15551234567/.wacli/account"] != alex+"\n" {
		t.Errorf("not adopted: %v", keys(got))
	}
	// A second account adopts nothing.
	write(t, filepath.Join(arch, ".wacli", "chats.json"), "{}")
	r.Add(sam, Pending)
	if notes, _ := r.AdoptLegacyArchive(sam); len(notes) != 0 {
		t.Errorf("second account adopted %q", notes)
	}
}

func TestRenamedAccountFolderIsFollowed(t *testing.T) {
	base := t.TempDir()
	store, arch := filepath.Join(base, "store"), filepath.Join(base, "WhatsApp")
	r, _ := Load(store, arch)
	r.Add(alex, "+15551234567")
	r.Add(sam, "+15557654321")
	for _, a := range r.Accounts {
		if err := Claim(r.ArchiveRoot(a), a.JID); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}
	// The owner renames one, and makes a folder of their own under the old name.
	if err := os.Rename(filepath.Join(arch, "+15551234567"), filepath.Join(arch, "Personal")); err != nil {
		t.Fatal(err)
	}
	os.Mkdir(filepath.Join(arch, "+15551234567"), 0o700)

	r, _ = Load(store, arch)
	notes, err := r.Locate(false)
	if err != nil || len(notes) != 1 || !strings.Contains(notes[0], `is now "Personal"`) {
		t.Fatalf("%q %v", notes, err)
	}
	if again, _ := Load(store, arch); again.Accounts[0].Folder != "+15551234567" {
		t.Error("a read-only Locate saved")
	}
	r, _ = Load(store, arch)
	if _, err := r.Locate(true); err != nil {
		t.Fatal(err)
	}
	r, _ = Load(store, arch)
	a, _ := r.Find(alex)
	if a.Folder != "Personal" || a.Label() != "Personal (+15551234567)" {
		t.Errorf("account %+v", a)
	}
	for _, sel := range []string{"personal", "+1 555 123 4567", "15551234567", alex} {
		if got, err := r.Select(sel, noneLinked); err != nil || got.JID != alex {
			t.Errorf("%q: %v %v", sel, got, err)
		}
	}
	if notes, _ := r.Locate(true); len(notes) != 0 {
		t.Errorf("settled, yet %q", notes)
	}
}

func TestFolderOfAnotherAccountIsNotTaken(t *testing.T) {
	base := t.TempDir()
	store, arch := filepath.Join(base, "store"), filepath.Join(base, "WhatsApp")
	r, _ := Load(store, arch)
	// The owner named a folder after Sam's number before Sam was linked,
	// for Alex's chats.
	r.Add(alex, "+15551234567")
	r.Accounts[0].Folder = "+15557654321"
	Claim(r.ArchiveRoot(r.Accounts[0]), alex)
	if a := r.Add(sam, Pending); a.Folder != "+15557654321 (2)" {
		t.Errorf("sam got %q", a.Folder)
	}
	// A folder not wacli's isn't used either.
	os.MkdirAll(filepath.Join(arch, "+15550000000"), 0o700)
	if a := r.Add("15550000000@s.whatsapp.net", Pending); a.Folder != "+15550000000 (2)" {
		t.Errorf("got %q", a.Folder)
	}
}

// An account folder moved elsewhere and linked back under a new name.
func TestRenamedFolderBehindASymlinkIsFollowed(t *testing.T) {
	base := t.TempDir()
	arch, elsewhere := filepath.Join(base, "WhatsApp"), filepath.Join(base, "big", "Personal")
	r, _ := Load(filepath.Join(base, "store"), arch)
	r.Add(alex, "+15551234567")
	if err := Claim(elsewhere, alex); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(arch, 0o700)
	if err := os.Symlink(elsewhere, filepath.Join(arch, "Personal")); err != nil {
		t.Skip(err)
	}
	if _, err := r.Locate(false); err != nil || r.Accounts[0].Folder != "Personal" {
		t.Errorf("got %q %v", r.Accounts[0].Folder, err)
	}
}

// A store set up again (a new computer, the archive copied over) links an
// account into the folder that is already its, renamed or not.
func TestNewLinkFindsItsKeptFolder(t *testing.T) {
	base := t.TempDir()
	arch := filepath.Join(base, "WhatsApp")
	if err := Claim(filepath.Join(arch, "Personal"), alex); err != nil {
		t.Fatal(err)
	}
	r, _ := Load(filepath.Join(base, "store"), arch)
	if a := r.Add(alex, Pending); a.Folder != "Personal" {
		t.Errorf("got %q", a.Folder)
	}
}

func TestSelectNeedsAChoiceWithSeveralAccounts(t *testing.T) {
	r := &Registry{}
	if _, err := r.Select("", noneLinked); !errors.Is(err, ErrNone) {
		t.Errorf("none: %v", err)
	}
	r.Accounts = []Account{{JID: alex, Folder: "Personal"}}
	if a, err := r.Select("", noneLinked); err != nil || a.JID != alex {
		t.Errorf("one: %v %v", a, err)
	}
	r.Accounts = append(r.Accounts, Account{JID: sam, Folder: "+15557654321", Store: "+15557654321"})
	r.Accounts[0].Store = "+15551234567"
	linked := map[string]bool{}
	isLinked := func(p string) bool { return linked[filepath.Base(filepath.Dir(p))] }
	if _, err := r.Select("", isLinked); !errors.Is(err, ErrNone) {
		t.Errorf("both logged out: %v", err)
	}
	linked["+15557654321"] = true
	if a, err := r.Select("", isLinked); err != nil || a.JID != sam {
		t.Errorf("one still linked: %v %v", a, err)
	}
	linked["+15551234567"] = true
	_, err := r.Select("", isLinked)
	if err == nil || !strings.Contains(err.Error(), "--account") || !strings.Contains(err.Error(), "Personal (+15551234567), +15557654321") {
		t.Errorf("several: %v", err)
	}
	if a, err := r.Select("personal", isLinked); err != nil || a.JID != alex {
		t.Errorf("chosen: %v %v", a, err)
	}
	if _, err := r.Select("Work", isLinked); err == nil || !strings.Contains(err.Error(), "Personal (+15551234567)") {
		t.Errorf("unknown: %v", err)
	}
	if all, err := r.Filter(""); err != nil || len(all) != 2 {
		t.Errorf("filter: %v %v", all, err)
	}
}

func TestSettleMovesANewLinkIntoItsStoreFolder(t *testing.T) {
	defer func() { rename = os.Rename }()
	for stopAt := 0; ; stopAt++ {
		base := t.TempDir()
		store, arch := filepath.Join(base, "store"), filepath.Join(base, "WhatsApp")
		// Relinking Alex, logged out before: the old session is unlinked.
		write(t, filepath.Join(store, "+15551234567", "session.db"), "old")
		write(t, filepath.Join(store, "+15551234567", "session.db-shm"), "old-shm")
		write(t, filepath.Join(store, Pending, "session.db"), "new")
		write(t, filepath.Join(store, Pending, "session.db-wal"), "new-wal")
		r, _ := Load(store, arch)
		r.Add(alex, Pending)
		r.Save()
		n := 0
		rename = func(from, to string) error {
			if n == stopAt {
				return errors.New("power cut")
			}
			n++
			return os.Rename(from, to)
		}
		err := r.Settle(alex, noneLinked)
		rename = os.Rename
		if err != nil {
			r, _ = Load(store, arch)
			if !r.NeedsMigration(func(string) bool { return false }) {
				t.Fatal("unsettled link not noticed")
			}
			if _, err := r.Migrate(jidOf(""), noneLinked); err != nil {
				t.Fatalf("stop %d: %v", stopAt, err)
			}
		}
		st := tree(t, store)
		delete(st, "accounts.json")
		if want := map[string]string{"+15551234567/session.db": "new", "+15551234567/session.db-wal": "new-wal"}; !reflect.DeepEqual(st, want) {
			t.Fatalf("stop %d: %v", stopAt, st)
		}
		if r, _ := Load(store, arch); r.Accounts[0].Store != "+15551234567" {
			t.Fatalf("stop %d: %+v", stopAt, r.Accounts)
		}
		if err == nil {
			return
		}
	}
}

func TestSettleKeepsALinkedSession(t *testing.T) {
	base := t.TempDir()
	store := filepath.Join(base, "store")
	write(t, filepath.Join(store, "+15551234567", "session.db"), "linked")
	write(t, filepath.Join(store, Pending, "session.db"), "new")
	r, _ := Load(store, filepath.Join(base, "WhatsApp"))
	r.Add(alex, Pending)
	if err := r.Settle(alex, func(string) bool { return true }); err == nil {
		t.Error("replaced a linked session")
	}
	if tree(t, store)["+15551234567/session.db"] != "linked" {
		t.Error("linked session touched")
	}
}
