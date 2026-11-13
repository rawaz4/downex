//go:build !windows && !linux && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly

package main

import (
	"errors"
	"os"
)

// enableVT fails where no terminal support is implemented, so watch prints a log of
// row changes instead of redrawing its table.
func enableVT(*os.File) (restore func(), err error) { return nil, errors.ErrUnsupported }

// termSize is unknown where no terminal support is implemented.
func termSize(*os.File) (width, height int, ok bool) { return 0, 0, false }
