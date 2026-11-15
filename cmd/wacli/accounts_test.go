package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ferdousbhai/wacli/internal/accounts"
	"github.com/ferdousbhai/wacli/internal/archive"
	"github.com/ferdousbhai/wacli/internal/archiver"
	"github.com/ferdousbhai/wacli/internal/delegate"
	"github.com/ferdousbhai/wacli/internal/lock"
	"github.com/ferdousbhai/wacli/internal/sqliteutil"
	"github.com/ferdousbhai/wacli/internal/wa"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// fakeSession makes a session store linked to device (whatsmeow's table,
// enough of it), with the row still in the WAL.
func fakeSession(t *testing.T, path, device string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", sqliteutil.FileURI(path, "_journal_mode=WAL"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, q := range []string{"PRAGMA wal_autocheckpoint=0", "CREATE TABLE whatsmeow_device (jid TEXT PRIMARY KEY)"} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if device != "" {
		if _, err := db.Exec("INSERT INTO whatsmeow_device VALUES (?)", device); err != nil {
			t.Fatal(err)
		}
	}
}

func testFlags(t *testing.T) *rootFlags {
	base := t.TempDir()
	return &rootFlags{storeDir: filepath.Join(base, "store"), archiveDir: filepath.Join(base, "WhatsApp")}
}

func TestUpgradeMovesTheArchiveIntoTheAccountFolder(t *testing.T) {
	f := testFlags(t)
	fakeSession(t, filepath.Join(f.storeDir, "session.db"), "15551234567:7@s.whatsapp.net")
	old, err := archive.Open(archive.Options{Root: f.archiveDir, DateFormat: "02/01/2006, 15:04:05", Location: time.UTC})
	if err != nil {
		t.Fatal(err)
	}
	c, _, err := old.EnsureChat("14155550100@s.whatsapp.net", "Maya", "dm", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Insert(archive.Row{ID: "M1", TS: 1700000000}, func(int) archive.Entry {
		return archive.Entry{Time: time.Unix(1700000000, 0).UTC(), Sender: "Maya", Text: "hello"}
	}); err != nil {
		t.Fatal(err)
	}
	if err := old.FlushAll(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(c.Dir(), archive.ChatFile))
	if err != nil {
		t.Fatal(err)
	}

	// The first command after the upgrade, even a read-only one, moves it.
	reg, err := readRegistry(f)
	if err != nil {
		t.Fatal(err)
	}
	a, err := reg.Select("")
	if err != nil || a.JID != "15551234567@s.whatsapp.net" || a.Folder != "+15551234567" {
		t.Fatalf("%+v %v", a, err)
	}
	if !isLinked(reg.SessionPath(a)) {
		t.Error("the moved session lost its device (its WAL)")
	}
	for _, gone := range []string{filepath.Join(f.storeDir, "session.db"), filepath.Join(f.archiveDir, ".wacli"), filepath.Join(f.archiveDir, "WhatsApp Chat - Maya")} {
		if _, err := os.Stat(gone); err == nil {
			t.Errorf("%s still there", gone)
		}
	}
	ar, err := f.openArchive(reg, a, true)
	if err != nil {
		t.Fatal(err)
	}
	if ar.DateFormat() != "02/01/2006, 15:04:05" || len(ar.Chats()) != 1 {
		t.Errorf("format %q, chats %v", ar.DateFormat(), ar.Chats())
	}
	after, err := os.ReadFile(filepath.Join(f.archiveDir, "+15551234567", "WhatsApp Chat - Maya", archive.ChatFile))
	if err != nil || string(after) != string(before) {
		t.Errorf("_chat.txt changed: %q → %q (%v)", before, after, err)
	}
	// A new account's archive takes the same date format.
	reg.Add("15557654321@s.whatsapp.net", "+15557654321")
	b, _ := reg.Find("15557654321@s.whatsapp.net")
	ar2, err := f.openArchive(reg, *b, false)
	if err != nil || ar2.DateFormat() != "02/01/2006, 15:04:05" {
		t.Errorf("second account: %v %v", ar2, err)
	}
}

func TestOldSyncHoldingTheStoreBlocksTheMove(t *testing.T) {
	f := testFlags(t)
	fakeSession(t, filepath.Join(f.storeDir, "session.db"), "15551234567:7@s.whatsapp.net")
	l, err := lock.Acquire(f.storeDir) // an older sync
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if _, err := readRegistry(f); !errors.Is(err, errOldSync) {
		t.Errorf("got %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.storeDir, "session.db")); err != nil {
		t.Errorf("moved under another's lock: %v", err)
	}
}

func TestSessionJIDOfAnUnlinkedStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.db")
	fakeSession(t, path, "")
	if jid, err := sessionJID(path); err != nil || jid != "" {
		t.Errorf("%q %v", jid, err)
	}
	empty := filepath.Join(t.TempDir(), "session.db")
	os.WriteFile(empty, nil, 0o600)
	if jid, err := sessionJID(empty); err != nil || jid != "" {
		t.Errorf("empty: %q %v", jid, err)
	}
}

// TestNewLinkArchivesIntoItsNumbersFolder drives a pairing's events: what
// comes before PairSuccess waits, and the session ends up in its folder.
func TestNewLinkArchivesIntoItsNumbersFolder(t *testing.T) {
	f := testFlags(t)
	s, err := openSession(f, nil)
	if err != nil {
		t.Fatal(err)
	}
	pending := filepath.Join(f.storeDir, accounts.Pending, accounts.SessionFile)
	os.MkdirAll(filepath.Dir(pending), 0o700)
	client, err := wa.New(wa.Options{StorePath: pending})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l := &linking{ctx: ctx, s: s, client: client}
	l.handle(&events.Connected{}) // before the number is known: kept
	l.handle(&events.PairSuccess{ID: types.NewADJID("15551234567", 0, 9)})
	acct, err := l.result()
	if err != nil || acct == nil {
		t.Fatalf("%v %v", acct, err)
	}
	root := filepath.Join(f.archiveDir, "+15551234567")
	if acct.ar.Root() != root {
		t.Errorf("archive %s", acct.ar.Root())
	}
	st, err := archiver.ReadStatus(root)
	if err != nil || !st.Connected {
		t.Errorf("the kept event wasn't handed over: %+v %v", st, err)
	}
	s.close()

	if _, err := os.Stat(filepath.Join(f.storeDir, "+15551234567", accounts.SessionFile)); err != nil {
		t.Errorf("session not moved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.storeDir, accounts.Pending)); err == nil {
		t.Error("pending folder left")
	}
	reg, _ := accounts.Load(f.storeDir, f.archiveDir)
	if len(reg.Accounts) != 1 || reg.Accounts[0].Store != "+15551234567" {
		t.Errorf("registry %+v", reg.Accounts)
	}
}

func TestLinkingAnAccountTwiceIsRefused(t *testing.T) {
	f := testFlags(t)
	fakeSession(t, filepath.Join(f.storeDir, "+15551234567", accounts.SessionFile), "15551234567:7@s.whatsapp.net")
	reg, _ := accounts.Load(f.storeDir, f.archiveDir)
	reg.Add("15551234567@s.whatsapp.net", "+15551234567")
	reg.Save()
	s, err := openSession(f, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	l := &linking{ctx: context.Background(), s: s}
	l.handle(&events.PairSuccess{ID: types.NewADJID("15551234567", 0, 9)})
	if acct, err := l.result(); acct != nil || !errors.Is(err, errAlreadyLinked) {
		t.Errorf("%v %v", acct, err)
	}
}

func TestWatchMergesAccounts(t *testing.T) {
	personal := &fakeSource{ch: make(chan archiver.Message, 4), since: []archiver.Message{{Account: "Personal", ID: "P0", At: 300}}}
	work := &fakeSource{ch: make(chan archiver.Message, 4), since: []archiver.Message{{Account: "+15557654321", ID: "W0", At: 200}}}
	personal.ch <- archiver.Message{Account: "Personal", ID: "P1", At: 1000}
	work.ch <- archiver.Message{Account: "+15557654321", ID: "W1", At: 1001}
	src := multiSource{personal, work}
	if got := src.MessagesSince(100); len(got) != 2 || got[0].ID != "W0" {
		t.Errorf("catch-up %v", got)
	}
	ch, cancel := src.Subscribe()
	seen := map[string]bool{}
	for range 2 {
		select {
		case m := <-ch:
			seen[m.ID] = true
		case <-time.After(5 * time.Second):
			t.Fatal("nothing merged")
		}
	}
	if !seen["P1"] || !seen["W1"] {
		t.Errorf("merged %v", seen)
	}
	close(work.ch) // one falls behind: the merged stream ends
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("stream went on")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stream didn't end")
	}
	cancel()
}

func TestWatchAccountFilterIsSentToTheSync(t *testing.T) {
	dir := t.TempDir()
	got := make(chan request, 1)
	ln := listenOnce(t, dir, func(r request, enc *json.Encoder) {
		got <- r
		enc.Encode(watchFrame{Error: "no account \"Work\""})
	})
	defer ln()
	err := watch(context.Background(), dir, watchOptions{Account: "Work"}, &syncWriter{})
	if err == nil || !strings.Contains(err.Error(), `no account "Work"`) {
		t.Errorf("got %v", err)
	}
	if r := <-got; r.Account != "Work" {
		t.Errorf("sent %+v", r)
	}
}

// listenOnce answers one IPC request with serve.
func listenOnce(t *testing.T, dir string, serve func(request, *json.Encoder)) func() {
	t.Helper()
	ln, err := delegate.Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var r request
		if json.NewDecoder(conn).Decode(&r) == nil {
			serve(r, json.NewEncoder(conn))
		}
	}()
	return func() { ln.Close() }
}
