package main

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ferdousbhai/wacli/internal/wa"

	"github.com/ferdousbhai/wacli/internal/archive"
	"github.com/ferdousbhai/wacli/internal/out"
	"github.com/spf13/cobra"

	"go.mau.fi/whatsmeow/types"
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
func doImport(ctx context.Context, ar *archive.Archive, r request, lidOf func(types.JID) types.JID) (any, error) {
	dir, unpacked := r.File, false
	if strings.EqualFold(filepath.Ext(r.File), ".zip") {
		// Unpack inside the archive (same disk, cleared after a crash),
		// so attachments can be moved into place rather than copied.
		tmp, err := os.MkdirTemp(ar.TmpDir(), "import-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(tmp)
		if err := unzipFlat(ctx, r.File, tmp); err != nil {
			return nil, err
		}
		dir, unpacked = tmp, true
	}
	target := r.Chat
	if target == "" {
		target = exportChatName(r.File)
	}
	jid, err := resolveChat(ar, target, lidOf)
	if err != nil {
		return nil, err
	}
	c, err := ar.Chat(jid.String())
	if err != nil {
		return nil, fmt.Errorf("%s isn't archived, so there is no chat to merge the export into: %w", jid, err)
	}
	entries, err := ar.ReadExport(dir)
	if err != nil {
		return nil, err
	}
	res, err := c.Merge(ctx, entries, dir, unpacked)
	if err != nil {
		return nil, err
	}
	msg := fmt.Sprintf("%s: %d already archived, %d added, %d attachments restored",
		filepath.Base(c.Dir()), res.Matched, res.Added, res.MediaRestored)
	if res.Skipped > 0 {
		msg += fmt.Sprintf(", %d skipped (dated in the future)", res.Skipped)
	}
	return msg, nil
}

// exportChatName is the chat name in an export's file or folder name
// ("WhatsApp Chat - Dr. Smith.zip" → "Dr. Smith"); only ".zip" is an
// extension, so a dot in the name stays.
func exportChatName(path string) string {
	name := filepath.Base(strings.TrimRight(path, string(filepath.Separator)))
	if strings.EqualFold(filepath.Ext(name), ".zip") {
		name = name[:len(name)-len(".zip")]
	}
	return strings.TrimPrefix(name, "WhatsApp Chat - ")
}

// Limits for unpacking an export zip, which may come from someone else: a
// file as large as WhatsApp media can be, and a bound on the whole.
const maxImportFile = wa.MaxMediaDownloadSize

var (
	maxImportTotal int64 = 64 << 30
	maxImportFiles       = 500_000
)

// unzipFlat extracts the top-level files of an export; exports have no
// subfolders, and anything else is refused rather than written outside dst.
func unzipFlat(ctx context.Context, src, dst string) error {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer zr.Close()
	// Exports are mostly media, already compressed: a zip that unpacks to
	// far more than its size is not one.
	limit := maxImportTotal
	if st, err := os.Stat(src); err == nil {
		limit = min(limit, 20*st.Size()+256<<20)
	}
	if len(zr.File) > maxImportFiles {
		return fmt.Errorf("export zip has %d files; an export has at most one per message", len(zr.File))
	}
	var total int64
	for _, zf := range zr.File {
		if err := ctx.Err(); err != nil {
			return err
		}
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
		var n int64
		n, err = io.Copy(out, io.LimitReader(rc, min(maxImportFile, limit-total)+1))
		total += n
		switch {
		case err != nil:
		case n > maxImportFile:
			err = fmt.Errorf("%s in export zip is larger than %d bytes", name, int64(maxImportFile))
		case total > limit:
			err = fmt.Errorf("export zip unpacks to more than %d bytes", limit)
		}
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
