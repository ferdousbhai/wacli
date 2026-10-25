package fsutil

import (
	"bufio"
	"os"
	"path/filepath"
)

// WriteAtomic replaces path with what fill writes. It writes a temp file in
// tmpDir (path's own directory when empty; it must be on the same
// filesystem), syncs it and renames it over path, so neither a reader nor a
// crash ever sees the file half written, and a crash leaves no temp file
// next to path.
func WriteAtomic(path, tmpDir string, mode os.FileMode, fill func(w *bufio.Writer) error) error {
	if tmpDir == "" {
		tmpDir = filepath.Dir(path)
	}
	tmp, err := os.CreateTemp(tmpDir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(tmp, 256<<10)
	err = fill(w)
	if err == nil {
		err = w.Flush()
	}
	if err == nil {
		err = tmp.Chmod(mode)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}
