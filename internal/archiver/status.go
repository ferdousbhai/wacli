package archiver

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ferdousbhai/wacli/internal/fsutil"

	"go.mau.fi/whatsmeow/types/events"
)

// Status is .wacli/status.json, written by a running sync so that `wacli
// doctor` (and a person checking on a service) can see what it is doing.
type Status struct {
	PID           int       `json:"pid"`
	StartedAt     time.Time `json:"started_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	Connected     bool      `json:"connected"`
	LastConnected time.Time `json:"last_connected,omitzero"`
	LastArchived  time.Time `json:"last_archived,omitzero"`
	LoggedOut     bool      `json:"logged_out,omitempty"`
	LoggedOutAt   time.Time `json:"logged_out_at,omitzero"`
	NewMessages   int64     `json:"new_messages"`
	PendingMedia  int       `json:"pending_media"`
	Problems      []string  `json:"problems,omitempty"`
}

func StatusPath(archiveRoot string) string {
	return filepath.Join(archiveRoot, ".wacli", "status.json")
}

// ReadStatus returns the last status a sync wrote.
func ReadStatus(archiveRoot string) (Status, error) {
	var s Status
	data, err := os.ReadFile(StatusPath(archiveRoot))
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(data, &s)
}

type statusTracker struct {
	mu        sync.Mutex
	s         Status
	loggedOut chan struct{}
	once      sync.Once
}

func newStatusTracker() *statusTracker {
	now := time.Now()
	return &statusTracker{s: Status{PID: os.Getpid(), StartedAt: now}, loggedOut: make(chan struct{})}
}

func (a *Archiver) trackConnection(evt any) {
	t := a.status
	t.mu.Lock()
	switch evt.(type) {
	case *events.Connected:
		t.s.Connected, t.s.LastConnected = true, time.Now()
	case *events.Disconnected:
		t.s.Connected = false
	case *events.LoggedOut:
		t.s.Connected, t.s.LoggedOut, t.s.LoggedOutAt = false, true, time.Now()
		t.once.Do(func() { close(t.loggedOut) })
	}
	t.mu.Unlock()
	a.writeStatus()
}

// LoggedOut is closed when WhatsApp unlinks this computer.
func (a *Archiver) LoggedOut() <-chan struct{} { return a.status.loggedOut }

func (a *Archiver) writeStatus() {
	t := a.status
	t.mu.Lock()
	defer t.mu.Unlock() // also serializes writers of the file
	t.s.UpdatedAt = time.Now()
	t.s.LastArchived = time.Unix(0, a.lastActivity.Load())
	t.s.NewMessages = a.added.Load()
	t.s.PendingMedia = a.media.len()
	t.s.Problems = a.ar.Problems()
	data, err := json.MarshalIndent(t.s, "", "  ")
	if err != nil {
		return
	}
	_ = fsutil.WriteAtomic(StatusPath(a.ar.Root()), a.ar.TmpDir(), 0o600, func(w *bufio.Writer) error {
		_, err := w.Write(data)
		return err
	})
}

func (a *Archiver) reportStatus(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		a.writeStatus()
		select {
		case <-ctx.Done():
			a.status.mu.Lock()
			a.status.s.Connected = false
			a.status.mu.Unlock()
			a.writeStatus()
			return
		case <-t.C:
		}
	}
}
