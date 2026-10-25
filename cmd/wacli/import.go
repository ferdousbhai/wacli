package main

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ferdousbhai/wacli/internal/archive"
	"github.com/ferdousbhai/wacli/internal/out"
	"github.com/spf13/cobra"
)

func newImportCmd(f *rootFlags) *cobra.Command {
	var chat string
	cmd := &cobra.Command{
		Use:   "import <export.zip|folder>",
		Short: "Merge a chat exported from your phone into the archive",
		Long: `Merges an iPhone "Export chat" (zip or unzipped folder) into the matching
chat folder: messages already archived are matched by sender and time, missing
ones are added, and attachments fill in entries that say "… omitted". Use it
for history the phone won't send to linked devices. Importing twice is safe.

The chat is found from the export's name ("WhatsApp Chat - <name>.zip"); pass
--chat when that is ambiguous.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := filepath.Abs(args[0])
			if err != nil {
				return err
			}
			res, err := runLocal(f, request{Op: "import", File: path, Chat: chat})
			if err != nil {
				return err
			}
			if f.asJSON {
				return out.WriteJSON(os.Stdout, res)
			}
			fmt.Printf("%v\n", res)
			return nil
		},
	}
	cmd.Flags().StringVar(&chat, "chat", "", "chat JID, phone number or name to merge into")
	return cmd
}

// doImport needs no connection, only the archive.
func doImport(_ context.Context, ar *archive.Archive, r request) (any, error) {
	dir := r.File
	if strings.EqualFold(filepath.Ext(r.File), ".zip") {
		tmp, err := os.MkdirTemp("", "wacli-import-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(tmp)
		if err := unzipFlat(r.File, tmp); err != nil {
			return nil, err
		}
		dir = tmp
	}
	target := r.Chat
	if target == "" {
		name := strings.TrimSuffix(filepath.Base(r.File), filepath.Ext(r.File))
		target = strings.TrimPrefix(name, "WhatsApp Chat - ")
	}
	jid, err := resolveChat(ar, target)
	if err != nil {
		return nil, err
	}
	c, err := ar.Chat(jid.String())
	if err != nil {
		return nil, fmt.Errorf("%s is not archived yet; run `wacli sync` first: %w", jid, err)
	}
	entries, err := ar.ReadExport(dir)
	if err != nil {
		return nil, err
	}
	res, err := c.Merge(entries, dir)
	if err != nil {
		return nil, err
	}
	return fmt.Sprintf("%s: %d already archived, %d added, %d attachments restored",
		filepath.Base(c.Dir()), res.Matched, res.Added, res.MediaRestored), nil
}

// unzipFlat extracts the top-level files of an export; exports have no
// subfolders, and anything else is refused rather than written outside dst.
func unzipFlat(src, dst string) error {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, zf := range zr.File {
		if zf.FileInfo().IsDir() || strings.HasPrefix(filepath.Base(zf.Name), "._") || strings.Contains(zf.Name, "__MACOSX") {
			continue
		}
		name := filepath.Base(zf.Name)
		if name != zf.Name || name == "." || name == ".." {
			return fmt.Errorf("unexpected path in export zip: %q", zf.Name)
		}
		rc, err := zf.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(filepath.Join(dst, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, io.LimitReader(rc, 2<<30))
		rc.Close()
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	return nil
}
