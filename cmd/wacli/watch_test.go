package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ferdousbhai/wacli/internal/archiver"
	"github.com/ferdousbhai/wacli/internal/delegate"
)

type fakeSource struct {
	ch      chan archiver.Message
	since   []archiver.Message
	askedAt int64
}

func (f *fakeSource) Subscribe() (<-chan archiver.Message, func()) { return f.ch, func() {} }
func (f *fakeSource) MessagesSince(since int64) []archiver.Message {
	f.askedAt = since
	return f.since
}

// fakeSync serves one watch the way `sync --follow` does.
func fakeSync(t *testing.T, src watchSource, quit chan struct{}) string {
	t.Helper()
	dir := t.TempDir()
	ln, err := delegate.Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var r request
		if json.NewDecoder(conn).Decode(&r) != nil || r.Op != "watch" {
			return
		}
		serveWatch(context.Background(), quit, src, conn, r.Since)
	}()
	return dir
}

// syncWriter lets the test read what watch printed so far.
type syncWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *syncWriter) lines() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.Split(strings.TrimSpace(w.buf.String()), "\n")
}

func TestWatchPrintsTheContract(t *testing.T) {
	src := &fakeSource{
		ch: make(chan archiver.Message, 8),
		since: []archiver.Message{
			{ID: "C1", Thread: "6281111111111@s.whatsapp.net", Chat: "Dana Putri", At: 1000, From: "Dana Putri", Kind: "text", Text: "while you were away", Ref: "/a/WhatsApp Chat - Dana Putri/_chat.txt"},
		},
	}
	src.ch <- archiver.Message{ID: "C1", Thread: "6281111111111@s.whatsapp.net", At: 1000, Kind: "text"} // caught up already
	src.ch <- archiver.Message{ID: "M2", Thread: "120363000000000001@g.us", At: 2000, FromMe: true, Kind: "text"}
	src.ch <- archiver.Message{ID: "M3", Thread: "120363000000000001@g.us", Chat: "Maya", At: 3000, From: "Kim", Kind: "media", Media: "/a/WhatsApp Chat - Maya/00000003-PHOTO.jpg", Ref: "/a/WhatsApp Chat - Maya/_chat.txt"}
	quit := make(chan struct{})
	dir := fakeSync(t, src, quit)

	var out syncWriter
	done := make(chan error, 1)
	go func() { done <- watch(context.Background(), dir, watchOptions{Since: 500}, &out) }()
	deadline := time.Now().Add(5 * time.Second)
	for len(out.lines()) < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	close(quit) // the sync stops
	err := <-done
	if err == nil || !strings.Contains(err.Error(), "--since 3000") {
		t.Errorf("exit: %v", err)
	}
	if src.askedAt != 500 {
		t.Errorf("catch-up from %d", src.askedAt)
	}
	lines := out.lines()
	if len(lines) != 2 {
		t.Fatalf("printed %q", lines)
	}
	want := `{"source":"whatsapp","id":"C1","thread":"6281111111111@s.whatsapp.net","chat":"Dana Putri","at":1000,"from":"Dana Putri","from_me":false,"kind":"text","text":"while you were away","media":"","ref":"/a/WhatsApp Chat - Dana Putri/_chat.txt"}`
	if lines[0] != want {
		t.Errorf("line:\n%s\nwant:\n%s", lines[0], want)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &m); err != nil || m["id"] != "M3" || m["media"] != "/a/WhatsApp Chat - Maya/00000003-PHOTO.jpg" {
		t.Errorf("second line %q (own message M2 must be skipped)", lines[1])
	}
}

func TestWatchFiltersByChatAndCanIncludeMine(t *testing.T) {
	src := &fakeSource{ch: make(chan archiver.Message, 8)}
	src.ch <- archiver.Message{ID: "A", Thread: "x@g.us", FromMe: true}
	src.ch <- archiver.Message{ID: "B", Thread: "y@g.us"}
	src.ch <- archiver.Message{ID: "C", Thread: "x@g.us"}
	close(src.ch) // then the sync drops us
	dir := fakeSync(t, src, make(chan struct{}))
	var out syncWriter
	err := watch(context.Background(), dir, watchOptions{Thread: "x@g.us", IncludeMine: true}, &out)
	if err == nil || !strings.Contains(err.Error(), "fell behind") {
		t.Errorf("exit: %v", err)
	}
	var ids []string
	for _, l := range out.lines() {
		var m watchLine
		json.Unmarshal([]byte(l), &m)
		ids = append(ids, m.ID)
	}
	if strings.Join(ids, ",") != "A,C" {
		t.Errorf("printed %v", ids)
	}
}

func TestWatchWithoutSyncFails(t *testing.T) {
	err := watch(context.Background(), t.TempDir(), watchOptions{}, &bytes.Buffer{})
	if !errors.Is(err, errNoSync) {
		t.Errorf("got %v", err)
	}
}

func TestWatchAgainstAnOlderSync(t *testing.T) {
	dir := t.TempDir()
	ln, err := delegate.Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() { // what handleConn of an older version answers
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		bufio.NewReader(conn).ReadString('\n')
		json.NewEncoder(conn).Encode(response{Error: `unknown op "watch"`})
	}()
	err = watch(context.Background(), dir, watchOptions{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "older wacli") {
		t.Errorf("got %v", err)
	}
}

func TestWatchStopsOnCancel(t *testing.T) {
	src := &fakeSource{ch: make(chan archiver.Message)}
	dir := fakeSync(t, src, make(chan struct{}))
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := watch(ctx, dir, watchOptions{}, &bytes.Buffer{}); err != nil {
		t.Errorf("Ctrl+C is a clean exit: %v", err)
	}
}
