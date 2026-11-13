package mediajob

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"runtime"
	"syscall"
	"time"
)

// Windows error numbers that have no portable syscall constant.
const (
	winErrorAccessDenied     = syscall.Errno(5)
	winErrorSharingViolation = syscall.Errno(32)
	winErrorLockViolation    = syscall.Errno(33)
)

// isSharingViolation reports transient Windows errors raised while another process
// (an indexer or virus scanner) briefly holds a file open.
func isSharingViolation(err error) bool {
	var errno syscall.Errno
	if runtime.GOOS != "windows" || !errors.As(err, &errno) {
		return false
	}
	return errno == winErrorSharingViolation || errno == winErrorLockViolation ||
		errno == winErrorAccessDenied
}

// withSharingRetry runs op, repeating it while another process briefly holds the file.
func withSharingRetry(ctx context.Context, c Clock, op func() error) error {
	delay := 50 * time.Millisecond
	for attempt := 0; ; attempt++ {
		err := op()
		if err == nil || attempt >= 8 || !isSharingViolation(err) {
			return err
		}
		if serr := sleep(ctx, c, delay); serr != nil {
			return errors.Join(err, serr)
		}
		delay = min(2*delay, 2*time.Second)
	}
}

// renameRetry moves a completed fragment into place, replacing a stale file of an
// earlier, unfinished attempt.
func renameRetry(ctx context.Context, c Clock, src, dst string) error {
	return withSharingRetry(ctx, c, func() error { return os.Rename(src, dst) })
}

// renameNoReplaceRetry moves the assembled output into place without replacing an
// existing file (the error then matches fs.ErrExist).
func renameNoReplaceRetry(ctx context.Context, c Clock, src, dst string) error {
	return withSharingRetry(ctx, c, func() error { return renameNoReplace(src, dst) })
}

// linkRename moves src to dst without replacing dst where no atomic no-replace rename
// exists: it hard-links src as dst, which fails when dst exists, then removes src.
// Where hard links are unsupported it falls back to a checked rename.
func linkRename(src, dst string) error {
	err := os.Link(src, dst)
	switch {
	case err == nil:
		return os.Remove(src)
	case errors.Is(err, fs.ErrExist):
		return err
	}
	if _, serr := os.Lstat(dst); serr == nil {
		return &os.LinkError{Op: "rename", Old: src, New: dst, Err: fs.ErrExist}
	} else if !errors.Is(serr, fs.ErrNotExist) {
		return serr
	}
	return os.Rename(src, dst)
}

// syncDir makes renames in dir durable where directories can be synced (not on
// Windows).
func syncDir(dir string) {
	if runtime.GOOS == "windows" {
		return
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}
