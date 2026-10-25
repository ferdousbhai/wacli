package main

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
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
	if jid, err := resolveChat(ar, "2024", nil); err != nil || jid.String() != "120363000000000001@g.us" {
		t.Errorf("got %v %v", jid, err)
	}
	if jid, _ := resolveChat(ar, "+1 (415) 555-1212", nil); jid.String() != "14155551212@s.whatsapp.net" {
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

func TestUnzipLimits(t *testing.T) {
	zpath := filepath.Join(t.TempDir(), "x.zip")
	f, _ := os.Create(zpath)
	zw := zip.NewWriter(f)
	for _, name := range []string{"a", "b"} {
		w, _ := zw.Create(name)
		w.Write([]byte("12345678"))
	}
	zw.Close()
	f.Close()
	defer func(total int64, files int) { maxImportTotal, maxImportFiles = total, files }(maxImportTotal, maxImportFiles)
	maxImportTotal = 10
	if err := unzipFlat(context.Background(), zpath, t.TempDir()); err == nil {
		t.Error("a zip larger than the total was unpacked")
	}
	maxImportTotal, maxImportFiles = 1<<20, 1
	if err := unzipFlat(context.Background(), zpath, t.TempDir()); err == nil {
		t.Error("a zip with too many files was unpacked")
	}
}
