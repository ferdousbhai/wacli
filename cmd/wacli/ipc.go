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
	Op      string `json:"op"`                // send | backfill | refetch | import | watch
	Account string `json:"account,omitempty"` // --account: a number or account folder; "" with one account (all, for watch)

	To      string `json:"to,omitempty"`
	Text    string `json:"text,omitempty"`
	File    string `json:"file,omitempty"`
	Caption string `json:"caption,omitempty"`

	Chat     string        `json:"chat,omitempty"`
	All      bool          `json:"all,omitempty"`
	Again    bool          `json:"again,omitempty"`
	Count    int           `json:"count,omitempty"`
	Requests int           `json:"requests,omitempty"`
	Wait     time.Duration `json:"wait,omitempty"`

	Since int64 `json:"since,omitempty"` // watch: unix ms to catch up from
}

type response struct {
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

func (r request) run(ctx context.Context, s *session) (any, error) {
	switch r.Op {
	case "send", "backfill", "refetch", "import":
	default:
		return nil, fmt.Errorf("unknown op %q", r.Op)
	}
	a, err := s.account(r.Account)
	if err != nil {
		return nil, err
	}
	switch r.Op {
	case "send":
		return doSend(ctx, a, r)
	case "backfill":
		return doBackfill(ctx, a, r)
	case "refetch":
		return doRefetch(ctx, a, r)
	}
	return doImport(ctx, a.ar, r, a.lidOf(ctx))
}

// execute runs r in this process, or in the running sync if it holds the store.
func execute(f *rootFlags, r request) (any, error) {
	ctx, cancel := signalContext()
	defer cancel()
	s, err := openSession(f, one(r.Account))
	if errors.Is(err, errBusy) {
		return forward(ctx, f, r)
	}
	if err != nil {
		return nil, err
	}
	defer s.close()
	a := s.accounts[0]
	if !a.wa.IsAuthed() {
		return nil, fmt.Errorf("%s: %w", a.Label(), errUnlinked)
	}
	if err := a.start(ctx, wa.ConnectOptions{}); err != nil {
		return nil, err
	}
	return r.run(ctx, s)
}

// runLocal runs work that needs the archive but not WhatsApp: here under the
// store lock, or in the running sync so the two never write at once.
func runLocal(f *rootFlags, r request) (any, error) {
	ctx, cancel := signalContext()
	defer cancel()
	s, err := openSession(f, one(r.Account))
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
		return nil, fmt.Errorf("another wacli is running and is not accepting requests (only `wacli sync --follow` does): %v", err)
	}
	defer conn.Close()
	// Ctrl+C closes the connection, which tells the sync to stop the work.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if err := json.NewEncoder(conn).Encode(r); err != nil {
		return nil, err
	}
	var resp response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
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
	quit := make(chan struct{}) // ends watch streams, which never finish by themselves
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
				handleConn(ctx, s, conn, quit)
			}()
		}
	}()
	// stop lets requests in flight finish (they end with ctx) before the
	// session shuts down, so an import isn't cut off halfway.
	return func() {
		ln.Close()
		<-accepting
		close(quit)
		wg.Wait()
	}, nil
}

func handleConn(ctx context.Context, s *session, conn net.Conn, quit <-chan struct{}) {
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
	if r.Op == "watch" {
		src, err := s.watchSource(r.Account)
		if err != nil {
			_ = json.NewEncoder(conn).Encode(watchFrame{Error: err.Error()})
			return
		}
		serveWatch(ctx, quit, src, conn, r.Since)
		return
	}
	res, err := r.run(ctx, s)
	resp := response{Result: res}
	if err != nil {
		resp.Error = err.Error()
	}
	_ = json.NewEncoder(conn).Encode(resp)
}
