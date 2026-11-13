//go:build !windows

package prefs

import "os"

func openShared(path string) (*os.File, error) { return os.Open(path) }
