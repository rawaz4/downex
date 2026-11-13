//go:build !windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"

	"downex/internal/client"
)

// checkSystemHost refuses a host for a system-wide registration that a user other than
// an administrator could replace, or whose daemon or windows such a user could replace:
// the relay exe, the programs it and its daemon start, every directory above them and,
// inside Downex.app, everything in the bundle must be owned by root and writable by no
// one else (systemOwnerRules, ownerRules.checkHost).
func checkSystemHost(exe string) error {
	err := systemOwnerRules(runtime.GOOS, 0).checkHost(runtime.GOOS, exe, osOwnerFS())
	if l := client.LayoutOf(runtime.GOOS, exe); err != nil && l.Bundle != "" {
		return fmt.Errorf("%w (everything in Downex.app must belong to root: sudo chown -R root:wheel %q)", err, l.Bundle)
	}
	return err
}

// osOwnerFS is the real file system for ownerRules.checkHost.
func osOwnerFS() ownerFS {
	return ownerFS{lstat: lstatOwner, resolve: filepath.EvalSymlinks, names: dirNames}
}

func lstatOwner(p string) (fileOwner, error) {
	fi, err := os.Lstat(p)
	if err != nil {
		return fileOwner{}, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fileOwner{}, errors.New("no owner information")
	}
	return fileOwner{uid: st.Uid, gid: st.Gid, mode: fi.Mode()}, nil
}

func dirNames(dir string) ([]string, error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.Readdirnames(-1)
}
