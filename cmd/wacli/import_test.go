package main

import (
	"context"
	"testing"
	"time"

	"github.com/ferdousbhai/wacli/internal/archive"
	"github.com/ferdousbhai/wacli/internal/delegate"
)

func TestExportChatName(t *testing.T) {
	for in, want := range map[string]string{
		"/x/WhatsApp Chat - Maya.zip":   "Maya",
		"/x/WhatsApp Chat - Dr. Smith":  "Dr. Smith",
		"/x/WhatsApp Chat - Acme Inc./": "Acme Inc.",
		"WhatsApp Chat - Dr. Smith.ZIP": "Dr. Smith",
	} {
		if got := exportChatName(in); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
}

func TestNumberLikeGroupNameIsFound(t *testing.T) {
	ar, err := archive.Open(archive.Options{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ar.EnsureChat("120363000000000001@g.us", "2024", "group", "")
	if jid, err := resolveChat(ar, "2024"); err != nil || jid.String() != "120363000000000001@g.us" {
		t.Errorf("got %v %v", jid, err)
	}
	if jid, _ := resolveChat(ar, "+1 (415) 555-1212"); jid.String() != "14155551212@s.whatsapp.net" {
		t.Errorf("number: %v", jid)
	}
}

func TestForwardStopsOnCancel(t *testing.T) {
	dir := t.TempDir()
	ln, err := delegate.Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			time.Sleep(5 * time.Second) // a long backfill
			conn.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := forward(ctx, &rootFlags{storeDir: dir}, request{Op: "backfill"}); err == nil {
		t.Fatal("expected cancellation")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("forward returned after %v", d)
	}
}
