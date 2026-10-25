package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/ferdousbhai/wacli/internal/delegate"
	"github.com/ferdousbhai/wacli/internal/wa"
)

// request is a unit of work any command can run itself or hand to a running
// `wacli sync --follow`, which owns the WhatsApp connection.
type request struct {
	Op string `json:"op"` // send | backfill | import

	To      string `json:"to,omitempty"`
	Text    string `json:"text,omitempty"`
	File    string `json:"file,omitempty"`
	Caption string `json:"caption,omitempty"`

	Chat     string        `json:"chat,omitempty"`
	All      bool          `json:"all,omitempty"`
	Count    int           `json:"count,omitempty"`
	Requests int           `json:"requests,omitempty"`
	Wait     time.Duration `json:"wait,omitempty"`
}

type response struct {
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

func (r request) run(ctx context.Context, s *session) (any, error) {
	switch r.Op {
	case "send":
		return doSend(ctx, s, r)
	case "backfill":
		return doBackfill(ctx, s, r)
	case "import":
		return doImport(ctx, s.ar, r)
	}
	return nil, fmt.Errorf("unknown op %q", r.Op)
}

// execute runs r in this process, or in the running sync if it holds the store.
func execute(f *rootFlags, r request) (any, error) {
	ctx, cancel := signalContext()
	defer cancel()
	s, err := openSession(f)
	if errors.Is(err, errBusy) {
		return forward(ctx, f, r)
	}
	if err != nil {
		return nil, err
	}
	defer s.close()
	if !s.wa.IsAuthed() {
		return nil, errUnlinked
	}
	if err := s.start(ctx, wa.ConnectOptions{}); err != nil {
		return nil, err
	}
	mark := s.arc.MediaMark()
	res, err := r.run(ctx, s)
	if err == nil {
		s.arc.WaitMediaSince(ctx, mark) // its own files, not the backlog
	}
	return res, err
}

// runLocal runs work that needs the archive but not WhatsApp: here under the
// store lock, or in the running sync so the two never write at once.
func runLocal(f *rootFlags, r request) (any, error) {
	ctx, cancel := signalContext()
	defer cancel()
	s, err := openSession(f)
	if errors.Is(err, errBusy) {
		return forward(ctx, f, r)
	}
	if err != nil {
		return nil, err
	}
	defer s.close()
	return r.run(ctx, s)
}

func forward(ctx context.Context, f *rootFlags, r request) (any, error) {
	conn, err := delegate.Dial(ctx, f.storeDir)
	if err != nil {
		return nil, fmt.Errorf("%w and is not accepting requests (only `wacli sync --follow` does): %v", errBusy, err)
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(r); err != nil {
		return nil, err
	}
	var resp response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return nil, err
	}
	if resp.Error != "" {
		return nil, errors.New(resp.Error)
	}
	return resp.Result, nil
}

func serveIPC(ctx context.Context, f *rootFlags, s *session) (func(), error) {
	ln, err := delegate.Listen(f.storeDir)
	if err != nil {
		return nil, fmt.Errorf("listen for send requests: %w", err)
	}
	var wg sync.WaitGroup
	accepting := make(chan struct{})
	go func() {
		defer close(accepting)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				handleConn(ctx, s, conn)
			}()
		}
	}()
	// stop lets requests in flight finish (they end with ctx) before the
	// session shuts down, so an import isn't cut off halfway.
	return func() {
		ln.Close()
		<-accepting
		wg.Wait()
	}, nil
}

func handleConn(ctx context.Context, s *session, conn net.Conn) {
	defer conn.Close()
	var r request
	dec := json.NewDecoder(conn)
	// A client that connects and says nothing must not hold up shutdown.
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if err := dec.Decode(&r); err != nil {
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	// The client waits silently for the answer; if it goes away (Ctrl+C),
	// stop the work it asked for.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		_, _ = io.Copy(io.Discard, io.MultiReader(dec.Buffered(), conn))
		cancel()
	}()
	res, err := r.run(ctx, s)
	resp := response{Result: res}
	if err != nil {
		resp.Error = err.Error()
	}
	_ = json.NewEncoder(conn).Encode(resp)
}
