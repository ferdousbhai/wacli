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
	if s.ar, err = f.openArchive(false); err != nil {
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
	return s.wa.Connect(ctx, opts)
}

// close shuts down in the order that loses nothing: no new events, finish
// the ones in flight and save, then let go of the session and the lock.
func (s *session) close() {
	if s.wa != nil {
		s.wa.Disconnect()
	}
	if s.arc != nil {
		s.arc.Close()
	}
	if s.wa != nil {
		s.wa.Close()
	}
	if s.lock != nil {
		_ = s.lock.Release()
	}
}

// runUntilIdle returns once nothing new has been archived for idle and the
// media that can be downloaded right now is on disk, or with the error to
// exit with if WhatsApp ends the session.
func (s *session) runUntilIdle(ctx context.Context, idle time.Duration) error {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	start := time.Now() // linking can take minutes before the first message
	quiet := func() bool { return min(s.arc.Idle(), time.Since(start)) >= idle }
	var last int64
	for i := 0; ; i++ {
		select {
		case <-ctx.Done():
			return nil
		case <-s.arc.LoggedOut():
			return errUnlinked
		case h := <-s.arc.Halted():
			return haltErr(h)
		case <-t.C:
		}
		if n := s.arc.Added(); i%10 == 0 && n != last {
			fmt.Fprintf(os.Stderr, "\rarchived %d messages, %d attachments pending   ", n, s.arc.PendingMedia())
			last = n
		}
		if quiet() {
			s.arc.WaitMedia(ctx)
			if quiet() {
				fmt.Fprintln(os.Stderr)
				return nil
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

// haltErr is what a sync exits with when WhatsApp ends the connection.
func haltErr(h archiver.Halt) error {
	if h.Retry {
		return errors.New(h.Reason)
	}
	return fmt.Errorf("%w: %s", errHalted, h.Reason)
}
