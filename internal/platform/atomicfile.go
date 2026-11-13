package platform

import (
	"io/fs"
	"os"
	"path/filepath"
)

// writeFileAtomic replaces the file at path with data, so that a reader (the desktop
// session or launchd reading a login entry) sees the old contents or the new ones,
// never a mix: it writes a temporary file ".downex-*.tmp" in path's directory, which
// must exist, gives it mode perm (exactly, whatever the umask), flushes it to stable
// storage and renames it over path. A symbolic link at path is replaced, not followed.
// The temporary file is removed when any step fails. The file belongs to the calling
// process's effective user.
func writeFileAtomic(path string, data []byte, perm fs.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".downex-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if err == nil {
		err = f.Chmod(perm)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
