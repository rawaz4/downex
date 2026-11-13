package engine

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"downex/internal/model"
	"downex/internal/store"
)

// partExt is the suffix of a download's in-progress file.
const partExt = ".dxpart"

// maxNameVariants bounds the "name (n).ext" candidates tried for a free filename.
const maxNameVariants = 10_000

func openOSFile(name string, flag int, perm fs.FileMode) (partFile, error) {
	return os.OpenFile(name, flag, perm)
}

func partPath(dir, name string) string { return filepath.Join(dir, name+partExt) }

// PartPath returns the in-progress file ("<name>.dxpart") of a download whose reserved
// filename is name in dir. The engine renames it to dir/name when the download completes.
func PartPath(dir, name string) string { return partPath(dir, name) }

// numbered returns name for n = 0, else "base (n).ext".
func numbered(name string, n int) string {
	if n == 0 {
		return name
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	if base == "" { // dot-files such as ".profile" have no extension to keep
		base, ext = name, ""
	}
	return fmt.Sprintf("%s (%d)%s", base, n, ext)
}

func exists(path string) (bool, error) {
	_, err := os.Lstat(path)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	}
	return false, err
}

// reserve picks the first variant of name ("name", "name (1).ext", ...) that neither
// exists on disk (final or part file) nor is reserved by another unfinished download
// in the store, and creates its part file with O_EXCL so that two downloads can never
// share one. A record keeps its name reserved while its part file is missing (deleted,
// or not recreated yet), which the files alone cannot show. self is the download that
// reserves (0 before it is created); its own record is ignored. It creates dir if needed.
// With replace (a completed download downloaded again to replace its file,
// model.Download.ReplaceFile), a regular file under name itself that no other download
// names does not take it: the new copy replaces it at completion (promote). A folder, a
// link or another download's file there does.
func (e *Engine) reserve(ctx context.Context, dir, name string, self model.DownloadID, replace bool) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create download directory: %w", err)
	}
	claimed, err := e.claimedNames(ctx, dir, self)
	if err != nil {
		return "", err
	}
	for i := range maxNameVariants {
		cand := numbered(name, i)
		if claimed[nameKey(cand)] {
			continue
		}
		taken, err := exists(filepath.Join(dir, cand))
		if err != nil {
			return "", fmt.Errorf("check %q: %w", cand, err)
		}
		if taken && i == 0 && replace {
			ok, err := e.replaceableBy(ctx, dir, cand, self)
			if err != nil {
				return "", fmt.Errorf("check %q: %w", cand, err)
			}
			taken = !ok
		}
		if taken {
			continue
		}
		f, err := os.OpenFile(partPath(dir, cand), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", diskError("create part file", err)
		}
		if err := f.Close(); err != nil {
			return "", diskError("create part file", err)
		}
		return cand, nil
	}
	return "", fmt.Errorf("no free filename for %q in %s", name, dir)
}

// claimedNames returns the names (as nameKey) reserved by the unfinished downloads in
// dir other than self.
func (e *Engine) claimedNames(ctx context.Context, dir string, self model.DownloadID) (map[string]bool, error) {
	all, err := e.st.ListDownloads(ctx)
	if err != nil {
		return nil, fmt.Errorf("list downloads: %w", err)
	}
	claimed := make(map[string]bool)
	for _, d := range all {
		if d.ID == self || d.Filename == "" || d.State == model.StateCompleted || nameKey(d.Dir) != nameKey(dir) {
			continue
		}
		claimed[nameKey(d.Filename)] = true
	}
	return claimed, nil
}

// nameKey normalises a path for comparing reservations: cleaned, and case-folded where
// file names are case-insensitive by default.
func nameKey(p string) string {
	p = filepath.Clean(p)
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		p = strings.ToLower(p)
	}
	return p
}

// promote renames the part file of name to the first free final name and returns it.
// Existing files are never replaced, not even one created at the moment of the rename:
// the rename itself refuses an existing destination and the next "name (n).ext" is
// tried. before, if non-nil, is called with each variant (never with name itself) just
// before renaming to it, so that the caller can commit the name the file will really
// have; its error stops promote. Transient sharing violations (Windows) are retried.
// With replace (model.Download.ReplaceFile of download self) the part file replaces a
// regular file under name itself that no other download names, in one atomic rename
// (renameReplace); a folder, a link, another download's file or any other kind of file
// there is left alone, and the variants are tried as without replace.
func (e *Engine) promote(ctx context.Context, dir, name string, self model.DownloadID, replace bool, before func(variant string) error) (string, error) {
	src := partPath(dir, name)
	for i := range maxNameVariants {
		cand := numbered(name, i)
		dst := filepath.Join(dir, cand)
		taken, err := exists(dst)
		if err != nil {
			return "", fmt.Errorf("check %q: %w", cand, err)
		}
		if taken && sameFile(src, dst) { // a link-and-remove rename was interrupted
			if err := os.Remove(src); err != nil {
				return "", diskError("remove part file", err)
			}
			syncDir(dir)
			return cand, nil
		}
		if taken && i == 0 && replace {
			ok, err := e.replaceableBy(ctx, dir, cand, self)
			if err != nil {
				return "", fmt.Errorf("check %q: %w", cand, err)
			}
			if ok {
				if err := e.retrySharing(ctx, src, func() error { return renameReplace(src, dst) }); err != nil {
					return "", diskError("replace the file with the part file", err)
				}
				syncDir(dir)
				return cand, nil
			}
		}
		if taken {
			continue
		}
		if i > 0 && before != nil {
			if err := before(cand); err != nil {
				return "", err
			}
		}
		err = e.renameWithRetry(ctx, src, dst)
		switch {
		case err == nil:
			syncDir(dir)
			return cand, nil
		case errors.Is(err, fs.ErrExist):
			continue
		}
		return "", diskError("rename part file", err)
	}
	return "", fmt.Errorf("no free filename for %q in %s", name, dir)
}

// renameWithRetry moves src to dst without replacing an existing dst (the error then
// matches fs.ErrExist), retrying while another process briefly holds the file.
func (e *Engine) renameWithRetry(ctx context.Context, src, dst string) error {
	return e.retrySharing(ctx, src, func() error { return renameNoReplace(src, dst) })
}

// retrySharing runs the rename op of path, again while another process briefly holds a
// file it touches (Windows sharing violations).
func (e *Engine) retrySharing(ctx context.Context, path string, op func() error) error {
	delay := 50 * time.Millisecond
	for attempt := 0; ; attempt++ {
		err := op()
		if err == nil || attempt >= 8 || !isSharingViolation(err) {
			return err
		}
		e.log.Debug("rename blocked by another process, retrying", "path", path, "err", err)
		if serr := sleep(ctx, e.clock, delay); serr != nil {
			return errors.Join(err, serr)
		}
		delay = min(2*delay, 2*time.Second)
	}
}

// Replaceable reports whether the file at path may be replaced by a download that
// replaces its file (model.Download.ReplaceFile; docs/ENGINE-SPEC.md §7.1): it exists and
// is a regular file, not a folder, a symbolic link, a Windows reparse point (a junction,
// or a file a cloud or deduplication filter stands in for) or a device. A missing file is
// not (false, nil).
func Replaceable(path string) (bool, error) {
	fi, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	case err != nil:
		return false, err
	}
	return fi.Mode().IsRegular() && !isReparsePoint(fi), nil
}

// OwnFile reports whether the file under completed download d's name is plausibly d's
// own, which downloading d again may replace (RestartOptions.Replace;
// docs/ENGINE-SPEC.md §7.1): it is Replaceable, has the size d completed with (when
// known), and no other download in st, in any state (completed ones included), names it.
// Anything else may be another download's file or one the user put there: the new copy
// then keeps both.
func OwnFile(ctx context.Context, st store.Store, d *model.Download) (bool, error) {
	if d.Filename == "" {
		return false, nil
	}
	path := filepath.Join(d.Dir, d.Filename)
	if ok, err := Replaceable(path); err != nil || !ok {
		return false, err
	}
	if d.Size >= 0 {
		fi, err := os.Lstat(path)
		if err != nil {
			return false, err
		}
		if fi.Size() != d.Size {
			return false, nil
		}
	}
	other, err := namedByOther(ctx, st, d.Dir, d.Filename, d.ID)
	return !other && err == nil, err
}

// replaceableBy is MayReplace with e's store.
func (e *Engine) replaceableBy(ctx context.Context, dir, name string, self model.DownloadID) (bool, error) {
	return MayReplace(ctx, e.st, dir, name, self)
}

// MayReplace reports whether download self, downloaded again to replace its file
// (model.Download.ReplaceFile), may replace the file name in dir when its new copy is
// complete: the file is Replaceable and no other download in st names it.
func MayReplace(ctx context.Context, st store.Store, dir, name string, self model.DownloadID) (bool, error) {
	if ok, err := Replaceable(filepath.Join(dir, name)); err != nil || !ok {
		return false, err
	}
	other, err := namedByOther(ctx, st, dir, name, self)
	return !other && err == nil, err
}

// namedByOther reports whether a download other than self, in any state, names the file
// name in dir.
func namedByOther(ctx context.Context, st store.Store, dir, name string, self model.DownloadID) (bool, error) {
	all, err := st.ListDownloads(ctx)
	if err != nil {
		return false, fmt.Errorf("list downloads: %w", err)
	}
	for _, d := range all {
		if d.ID != self && d.Filename != "" && nameKey(d.Dir) == nameKey(dir) && nameKey(d.Filename) == nameKey(name) {
			return true, nil
		}
	}
	return false, nil
}

// linkRename moves src to dst without replacing dst where no atomic no-replace rename
// exists: it hard-links src as dst, which fails when dst exists, then removes src (a
// crash in between leaves two names of one file, which promote resolves). Where hard
// links are unsupported it falls back to a checked rename, which cannot exclude a file
// created at that very moment.
func linkRename(src, dst string) error {
	err := os.Link(src, dst)
	switch {
	case err == nil:
		return os.Remove(src)
	case errors.Is(err, fs.ErrExist):
		return err
	}
	taken, cerr := exists(dst)
	if cerr != nil {
		return cerr
	}
	if taken {
		return &os.LinkError{Op: "rename", Old: src, New: dst, Err: fs.ErrExist}
	}
	return os.Rename(src, dst)
}

// sameFile reports whether a and b name the same existing file.
func sameFile(a, b string) bool {
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	return err == nil && os.SameFile(fa, fb)
}

// syncDir makes a rename durable where directories can be synced (not on Windows).
func syncDir(dir string) {
	if runtime.GOOS == "windows" {
		return
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}
