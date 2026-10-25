package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

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
			ar, err := f.openArchive()
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
					return err
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
		jid, err := resolveChat(s.ar, r.Chat)
		if err != nil {
			return nil, err
		}
		jids = []string{jid.String()}
	}
	var results []archiver.BackfillResult
	for _, jid := range jids {
		res, err := s.arc.Backfill(ctx, jid, r.Count, r.Requests, r.Wait)
		results = append(results, res)
		if err != nil {
			if r.All {
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

func newDoctorCmd(f *rootFlags) *cobra.Command {
	var check bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Show link status, sync health and where the archive lives",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ar, err := f.openArchive()
			if err != nil {
				return err
			}
			running, _, _ := lock.Probe(f.storeDir)
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
				if c, err := wa.New(wa.Options{StorePath: filepath.Join(f.storeDir, "session.db")}); err == nil {
					info["linked"] = c.IsAuthed()
					c.Close()
				}
			}
			var problems []string
			if st, err := archiver.ReadStatus(ar.Root()); err == nil {
				info["connected"] = running && st.Connected
				info["last_archived"] = st.LastArchived.Format(time.RFC3339)
				info["status_updated"] = st.UpdatedAt.Format(time.RFC3339)
				info["pending_media"] = st.PendingMedia
				if st.LoggedOut {
					problems = append(problems, "WhatsApp unlinked this computer at "+st.LoggedOutAt.Format(time.RFC3339)+"; run `wacli auth`")
				}
				if running && time.Since(st.UpdatedAt) > 2*time.Minute {
					problems = append(problems, "sync is running but hasn't reported for "+time.Since(st.UpdatedAt).Round(time.Second).String())
				}
				if running && !st.Connected {
					problems = append(problems, "sync is running but not connected to WhatsApp")
				}
				for _, r := range st.RepairedChats {
					problems = append(problems, "index rebuilt (some old messages can no longer be edited or deleted): "+r)
				}
			}
			if check {
				if running {
					problems = append(problems, "--check skipped: stop sync first (it owns the files)")
				} else {
					for _, c := range ar.Chats() {
						if _, err := ar.Chat(c.JID); err != nil {
							problems = append(problems, c.Folder+": "+err.Error())
						}
					}
					for _, r := range ar.Repairs() {
						problems = append(problems, "index rebuilt: "+r)
					}
				}
			}
			info["problems"] = problems
			if f.asJSON {
				return out.WriteJSON(os.Stdout, info)
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			for _, k := range []string{"archive", "store", "linked", "sync_running", "connected", "last_archived", "status_updated", "chats", "chats_history_complete", "pending_media", "date_format"} {
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
	cmd.Flags().BoolVar(&check, "check", false, "also open every chat and verify its files (sync must be stopped)")
	return cmd
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
			if err := s.wa.Logout(ctx); err != nil {
				return err
			}
			fmt.Fprintln(os.Stderr, "Unlinked. Chat folders in", s.ar.Root(), "are untouched.")
			return nil
		},
	}
}
