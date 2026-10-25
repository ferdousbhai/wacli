package archive

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A folder export someone sent can hold a symlink named like an attachment;
// importing it must not copy the link's target into the chat.
func TestImportFolderIgnoresSymlinkedAttachment(t *testing.T) {
	a := openTest(t)
	c, _, _ := a.EnsureChat("1@s.whatsapp.net", "Dana", "dm", "")
	secret := filepath.Join(t.TempDir(), "id_ed25519")
	os.WriteFile(secret, []byte("PRIVATE KEY"), 0o600)
	src := t.TempDir()
	name := "00000001-PHOTO-1970-01-01-00-01-40.jpg"
	if err := os.Symlink(secret, filepath.Join(src, name)); err != nil {
		t.Fatal(err)
	}
	in := []Entry{{Time: time.Unix(100, 0).UTC(), Sender: "Dana", Attachment: name}}
	if _, err := c.Merge(context.Background(), in, src, false); err != nil {
		t.Logf("merge: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(c.Dir(), name)); err == nil {
		t.Errorf("symlink target copied into chat folder: %q", data)
	}
}

// Importing a large export (oldest first, as _chat.txt is) must not be
// quadratic.
