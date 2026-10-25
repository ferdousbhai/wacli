package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ferdousbhai/wacli/internal/archive"
	"github.com/ferdousbhai/wacli/internal/archiver"
	"github.com/ferdousbhai/wacli/internal/lock"
	"github.com/ferdousbhai/wacli/internal/wa"
)

// session is a connected WhatsApp client writing into the archive. Only one
// process holds it at a time (the store LOCK); others talk to it over IPC.
type session struct {
	lock *lock.Lock
	wa   *wa.Client
	ar   *archive.Archive
	arc  *archiver.Archiver
}

var errBusy = errors.New("another wacli is running")

func openSession(f *rootFlags) (*session, error) {
	l, err := lock.Acquire(f.storeDir)
	if err != nil {
		if lock.IsLocked(err) {
			return nil, errBusy
		}
		return nil, err
	}
	s := &session{lock: l}
	if s.ar, err = f.openArchive(); err != nil {
		s.close()
		return nil, err
	}
	if s.wa, err = wa.New(wa.Options{StorePath: filepath.Join(f.storeDir, "session.db")}); err != nil {
		s.close()
		return nil, err
	}
	if s.arc, err = archiver.New(s.ar, s.wa, logf); err != nil {
		s.close()
		return nil, err
	}
	return s, nil
}

// start registers the archiver before connecting so the first history sync
// after login is not missed.
func (s *session) start(ctx context.Context, opts wa.ConnectOptions) error {
	s.arc.Start(ctx)
	if err := s.wa.Connect(ctx, opts); err != nil {
		return err
	}
	// Contact names and group subjects sync after the first history batches;
	// keep renaming chats that were still named by number.
	go func() {
		t := time.NewTicker(2 * time.Minute)
		defer t.Stop()
		for {
			s.arc.RefreshNames(ctx)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	return nil
}

func (s *session) close() {
	if s.arc != nil {
		s.arc.Flush()
	}
	if s.wa != nil {
		s.wa.Close()
	}
	if s.lock != nil {
		_ = s.lock.Release()
	}
}

// runUntilIdle returns once nothing new has been archived for idle and the
// media that can be downloaded right now is on disk.
func (s *session) runUntilIdle(ctx context.Context, idle time.Duration) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	last := int64(-1)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if n := s.arc.Added(); n != last && n > 0 && n%500 < 50 {
			fmt.Fprintf(os.Stderr, "\rarchived %d messages, %d attachments pending   ", n, s.arc.PendingMedia())
			last = n
		}
		if s.arc.Idle() >= idle {
			s.arc.WaitMedia(ctx)
			if s.arc.Idle() >= idle {
				fmt.Fprintln(os.Stderr)
				return
			}
		}
	}
}

func (s *session) summary() map[string]any {
	return map[string]any{
		"archive":       s.ar.Root(),
		"chats":         len(s.ar.Chats()),
		"new_messages":  s.arc.Added(),
		"pending_media": s.arc.PendingMedia(),
	}
}
