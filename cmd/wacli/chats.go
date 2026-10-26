package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ferdousbhai/wacli/internal/sqliteutil"
	_ "github.com/mattn/go-sqlite3"

	"github.com/ferdousbhai/wacli/internal/archiver"
	"github.com/ferdousbhai/wacli/internal/lock"
	"github.com/ferdousbhai/wacli/internal/out"
	"github.com/ferdousbhai/wacli/internal/wa"
	"github.com/spf13/cobra"
)

type chatRow struct {
	Name     string    `json:"name"`
	JID      string    `json:"jid"`
	Kind     string    `json:"kind"`
	Path     string    `json:"path"`
	Messages int       `json:"messages"`
	Last     time.Time `json:"last"`
	Complete bool      `json:"history_complete"`
}

func newChatsCmd(f *rootFlags) *cobra.Command {
	var query string
	cmd := &cobra.Command{
		Use:   "chats",
		Short: "List archived chats and where their folders are",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ar, err := f.openArchive(true)
			if err != nil {
				return err
			}
			var rows []chatRow
			for _, info := range ar.Chats() {
				if query != "" && !strings.Contains(strings.ToLower(info.Name+" "+info.Folder+" "+info.JID), strings.ToLower(query)) {
					continue
				}
				c, err := ar.Chat(info.JID)
				if err != nil {
					logf("skipping %s: %v", info.Folder, err) // one bad chat mustn't hide the rest
					continue
				}
				r := chatRow{Name: info.Name, JID: info.JID, Kind: info.Kind, Path: c.Dir(), Messages: c.Len(), Complete: info.BackfillDone}
				if all := c.Rows(); len(all) > 0 {
					r.Last = time.Unix(all[len(all)-1].TS, 0)
				}
				rows = append(rows, r)
			}
			if f.asJSON {
				return out.WriteJSON(os.Stdout, rows)
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(w, "LAST\tMESSAGES\tNAME\tJID\tFOLDER")
			for _, r := range rows {
				fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\n", r.Last.Format("2006-01-02 15:04"), r.Messages, r.Name, r.JID, filepath.Base(r.Path))
			}
			return w.Flush()
		},
	}
	cmd.PersistentFlags().StringVar(&query, "query", "", "filter by name, folder or JID")
	// Accept the old `wacli chats list` spelling.
	cmd.AddCommand(&cobra.Command{Use: "list", Hidden: true, RunE: cmd.RunE})
	return cmd
}

func newBackfillCmd(f *rootFlags) *cobra.Command {
	r := request{Op: "backfill"}
	cmd := &cobra.Command{
		Use:   "backfill",
		Short: "Ask the phone for older history of a chat (or --all)",
		Long: `Walks back through a chat's history from its newest message, 50 messages per
request, until the phone has nothing older. Gaps in the middle of a chat are
filled too. Progress is saved, so it can be stopped and resumed. The phone must
be online.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if r.Chat == "" && !r.All {
				return fmt.Errorf("pass --chat or --all")
			}
			res, err := execute(f, r)
			if err != nil {
				return err
			}
			if f.asJSON {
				return out.WriteJSON(os.Stdout, res)
			}
			data, _ := json.Marshal(res)
			var results []archiver.BackfillResult
			_ = json.Unmarshal(data, &results)
			for _, b := range results {
				state := "more available, run again"
				if b.Done {
					state = "complete"
				}
				fmt.Printf("%s: +%d messages, back to %s (%s)\n", b.Folder, b.Added, b.Oldest.Format("2006-01-02"), state)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&r.Chat, "chat", "", "chat JID, phone number or name")
	cmd.Flags().BoolVar(&r.All, "all", false, "every chat whose history is not complete yet")
	cmd.Flags().IntVar(&r.Count, "count", 50, "messages per request")
	cmd.Flags().IntVar(&r.Requests, "requests", 1000, "maximum requests per chat")
	cmd.Flags().DurationVar(&r.Wait, "wait", time.Minute, "how long to wait for each answer from the phone")
	return cmd
}

func doBackfill(ctx context.Context, s *session, r request) (any, error) {
	var jids []string
	if r.All {
		for _, c := range s.ar.Chats() {
			if !c.BackfillDone {
				jids = append(jids, c.JID)
			}
		}
	} else {
		jid, err := resolveChat(s.ar, r.Chat, s.lidOf(ctx))
		if err != nil {
			return nil, err
		}
		jids = []string{jid.String()}
	}
	var results []archiver.BackfillResult
	unanswered, answered := 0, false
	for _, jid := range jids {
		if err := s.waitLoggedIn(ctx); err != nil { // rides out a short reconnect
			return results, err
		}
		res, err := s.arc.Backfill(ctx, jid, r.Count, r.Requests, r.Wait)
		if left := r.Requests - (res.Requests - 1); errors.Is(err, wa.ErrNotConnected) && left > 0 && s.waitLoggedIn(ctx) == nil {
			// Dropped mid-chat and back: carry on from its saved cursor.
			more, merr := s.arc.Backfill(ctx, jid, r.Count, left, r.Wait)
			more.Added, more.Requests = more.Added+res.Added, more.Requests+res.Requests-1 // that one wasn't sent
			res, err = more, merr
		}
		results = append(results, res)
		switch {
		case err == nil || res.Requests > 1: // some page came back
			answered = true
		case errors.Is(err, archiver.ErrNoAnswer):
			unanswered++
		}
		if err != nil {
			// The first three chats silent: the phone is offline, not the
			// chats. Without a connection nothing can be asked.
			offline := unanswered >= 3 && !answered || errors.Is(err, wa.ErrNotConnected)
			if r.All && ctx.Err() == nil && !offline {
				logf("%s: %v", res.Folder, err)
				continue
			}
			return results, err
		}
		if r.All {
			logf("%s: +%d", res.Folder, res.Added)
		}
	}
	return results, nil
}

func newRefetchCmd(f *rootFlags) *cobra.Command {
	r := request{Op: "refetch"}
	cmd := &cobra.Command{
		Use:   "refetch",
		Short: "Ask the phone again for messages archived as empty lines",
		Long: `Finds lines with nothing after the sender's name (in one chat, or all) and
asks your phone for the history around them again. The phone sends it to wacli
only; nothing goes to the chat. Lines wacli can show are filled in, and each
message's type is reported and remembered, so a line is asked about once
(--again asks about all of them). Album headers are empty lines in iPhone
exports too, so they stay empty.

A running sync does this by itself, a few requests at a time, whenever
WhatsApp is in use on the phone: it answers only while open.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := execute(f, r)
			if err != nil {
				return err
			}
			if f.asJSON {
				return out.WriteJSON(os.Stdout, res)
			}
			data, _ := json.Marshal(res)
			var rr refetchReply
			_ = json.Unmarshal(data, &rr)
			kinds := map[string]int{}
			filled := 0
			for _, it := range rr.Items {
				kind := it.Kind
				if kind == "" {
					kind = "no answer"
				}
				kinds[kind]++
				if it.Filled {
					filled++
					kind += " (filled in)"
				}
				if it.Kind != "album" {
					fmt.Printf("%s  %s  %s: %s\n", it.Time.Format("2006-01-02 15:04"), it.Folder, it.ID, kind)
				}
			}
			fmt.Printf("%d empty lines checked, %d filled in, %d left to check.", len(rr.Items), filled, rr.Left)
			for _, k := range slices.Sorted(maps.Keys(kinds)) {
				fmt.Printf(" %s: %d.", k, kinds[k])
			}
			fmt.Println()
			if rr.Stopped != "" {
				fmt.Fprintln(os.Stderr, "Stopped: "+rr.Stopped)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&r.Chat, "chat", "", "chat JID, phone number or name (default: all chats)")
	cmd.Flags().BoolVar(&r.Again, "again", false, "ask about lines already checked too")
	cmd.Flags().IntVar(&r.Count, "count", 50, "messages per request")
	cmd.Flags().DurationVar(&r.Wait, "wait", 30*time.Second, "how long to wait for the phone's answer to each request")
	return cmd
}

// refetchReply is a Refetch result, and why it stopped early if it did.
type refetchReply struct {
	archiver.RefetchResult
	Stopped string `json:"stopped,omitempty"`
}

func doRefetch(ctx context.Context, s *session, r request) (any, error) {
	o := archiver.RefetchOptions{Count: r.Count, Wait: r.Wait, Again: r.Again}
	if r.Chat != "" {
		jid, err := resolveChat(s.ar, r.Chat, s.lidOf(ctx))
		if err != nil {
			return nil, err
		}
		o.Chat = jid.String()
	}
	if err := s.waitLoggedIn(ctx); err != nil {
		return nil, err
	}
	res, err := s.arc.Refetch(ctx, o)
	if err != nil && (len(res.Items) > 0 || errors.Is(err, archiver.ErrPhoneQuiet)) {
		// What was learnt is recorded; show it with why the run stopped.
		return refetchReply{res, err.Error()}, nil
	}
	return refetchReply{RefetchResult: res}, err
}

func newDoctorCmd(f *rootFlags) *cobra.Command {
	var check bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Show link status, sync health and where the archive lives",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ar, err := f.openArchive(true)
			if err != nil {
				return err
			}
			running, _, _ := lock.Probe(f.storeDir)
			if !running {
				// The lock can be out of sight (doctor in a sandbox or
				// container); a sync reports status every 30s.
				// Only if its process is out of sight: one that's visible
				// without the lock held has crashed.
				st, err := archiver.ReadStatus(ar.Root())
				_, visible := os.Stat(fmt.Sprintf("/proc/%d", st.PID))
				if err == nil && !st.Stopped && time.Since(st.UpdatedAt) < 90*time.Second && visible != nil {
					running = true
				}
			}
			info := map[string]any{
				"archive":      ar.Root(),
				"store":        f.storeDir,
				"sync_running": running,
				"chats":        len(ar.Chats()),
				"date_format":  ar.DateFormat(),
			}
			complete := 0
			for _, c := range ar.Chats() {
				if c.BackfillDone {
					complete++
				}
			}
			info["chats_history_complete"] = complete
			if !running {
				info["linked"] = isLinked(filepath.Join(f.storeDir, "session.db"))
			}
			var problems []string
			st, stErr := archiver.ReadStatus(ar.Root())
			if !running && info["linked"] == true && st.Halted == "" && !st.LoggedOut {
				problems = append(problems, "sync is not running, so the archive isn't being updated (`systemctl --user start wacli-sync`)")
			}
			if !running && info["linked"] == false && !st.LoggedOut {
				problems = append(problems, "not linked to WhatsApp yet: run `wacli auth`, then start the sync")
			}
			if stErr == nil {
				info["connected"] = running && st.Connected
				if !st.LastArchived.IsZero() {
					info["last_archived"] = st.LastArchived.Format(time.RFC3339)
				}
				info["status_updated"] = st.UpdatedAt.Format(time.RFC3339)
				info["pending_media"] = st.PendingMedia
				if running {
					info["empty_lines_to_check"] = st.EmptyLines
				}
				if st.Halted != "" && !running {
					problems = append(problems, "sync stopped at "+st.HaltedAt.Format(time.RFC3339)+": "+st.Halted)
				}
				if st.LoggedOut {
					problems = append(problems, "WhatsApp unlinked this computer at "+st.LoggedOutAt.Format(time.RFC3339)+"; run `wacli auth`")
				}
				if running && time.Since(st.UpdatedAt) > 2*time.Minute {
					problems = append(problems, "sync is running but hasn't reported for "+time.Since(st.UpdatedAt).Round(time.Second).String())
				}
				if running && !st.Connected {
					problems = append(problems, "sync is running but not connected to WhatsApp")
				}
				problems = append(problems, st.Problems...)
			}
			if check {
				// Read-only: what a repair would do is reported, not done.
				for _, c := range ar.Chats() {
					if _, err := ar.Chat(c.JID); err != nil {
						problems = append(problems, c.Folder+": "+err.Error())
					}
				}
				problems = append(problems, ar.Problems()...)
			}
			if sp, err := defaultSkillPaths(); err == nil {
				info["agent_skill"] = string(sp.state())
				// Optional: only wacli's own copy gone stale is a problem.
				if st := sp.state(); st == skillOutdated {
					problems = append(problems, "agent skill "+string(st)+": run `wacli skill install`")
				}
			}
			info["problems"] = problems
			if f.asJSON {
				return out.WriteJSON(os.Stdout, info)
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			for _, k := range []string{"archive", "store", "linked", "sync_running", "connected", "last_archived", "status_updated", "chats", "chats_history_complete", "pending_media", "empty_lines_to_check", "date_format", "agent_skill"} {
				if v, ok := info[k]; ok {
					fmt.Fprintf(w, "%s\t%v\n", strings.ToUpper(k), v)
				}
			}
			if err := w.Flush(); err != nil {
				return err
			}
			if len(problems) == 0 {
				fmt.Println("No problems found.")
			}
			for _, p := range problems {
				fmt.Println("PROBLEM:", p)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "also read every chat and verify its files (changes nothing)")
	return cmd
}

// isLinked reads the session store without changing it: opening it through
// whatsmeow would create or migrate it, and doctor must only look.
func isLinked(path string) bool {
	if _, err := os.Stat(path); err != nil {
		return false
	}
	db, err := sql.Open("sqlite3", sqliteutil.FileURI(path, "mode=ro"))
	if err != nil {
		return false
	}
	defer db.Close()
	var n int
	return db.QueryRow("SELECT COUNT(*) FROM whatsmeow_device").Scan(&n) == nil && n > 0
}

func newLogoutCmd(f *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Unlink this computer from WhatsApp (chat folders are kept)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := openSession(f)
			if err != nil {
				return err
			}
			defer s.close()
			ctx, cancel := signalContext()
			defer cancel()
			if err := s.wa.Connect(ctx, wa.ConnectOptions{}); err != nil {
				return err
			}
			if err := s.waitLoggedIn(ctx); errors.Is(err, errUnlinked) {
				fmt.Fprintln(os.Stderr, "Already unlinked. Chat folders in", s.ar.Root(), "are untouched.")
				return nil
			} else if err != nil {
				return err
			}
			if err := s.wa.Logout(ctx); err != nil {
				return err
			}
			fmt.Fprintln(os.Stderr, "Unlinked. Chat folders in", s.ar.Root(), "are untouched.")
			return nil
		},
	}
}
