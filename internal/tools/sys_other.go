//go:build !windows && !linux && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly

package tools

import (
	"errors"
	"os"
	"syscall"
)

// tryLock is a no-op where no advisory file lock is implemented: two daemons sharing a
// tools folder are then not kept apart.
func tryLock(*os.File) (bool, error) { return false, nil }

func unlockFile(*os.File) error { return nil }

// freeSpace is unknown here: the space check is skipped.
func freeSpace(string) (uint64, error) { return 0, errors.ErrUnsupported }

func isDiskFull(err error) bool { return errors.Is(err, syscall.ENOSPC) }

func chmodExe(f *os.File) error { return f.Chmod(0o755) }
