//go:build !windows && !linux && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly

package dlock

import "os"

// tryLock is a no-op where no advisory file lock is implemented: concurrent downex
// processes are then not prevented from running the same download.
func tryLock(*os.File) error { return nil }

func unlock(*os.File) error { return nil }
