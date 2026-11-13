package main

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"

	"downex/internal/client"
)

// ownerRules say who may change the files of a host registered for all users on Linux
// and macOS (docs/BROWSER-SPEC.md §1: nobody but administrators). The rules are pure so
// that tests check them on every system.
type ownerRules struct {
	// root must own every file and directory.
	root uint32
	// adminGroups may hold write permission besides root: on macOS wheel (0) and admin
	// (80), whose members are its administrators (/Applications is root:admin, mode
	// 0775). None on Linux.
	adminGroups []uint32
}

// macAdminGID is the group of macOS administrators ("admin").
const macAdminGID = 80

// systemOwnerRules returns the rules of goos, with root as the owner that is required.
func systemOwnerRules(goos string, root uint32) ownerRules {
	r := ownerRules{root: root}
	if goos == "darwin" {
		r.adminGroups = []uint32{0, macAdminGID}
	}
	return r
}

// check says why p, with the owner, group and mode that Lstat reports, could be changed
// by someone other than an administrator; nil when it cannot. A symbolic link's own
// mode does not matter (only its owner can replace it).
func (r ownerRules) check(p string, uid, gid uint32, mode fs.FileMode) error {
	perm := mode.Perm()
	switch {
	case uid != r.root:
		return fmt.Errorf("%s is owned by uid %d, not by root", p, uid)
	case mode&fs.ModeSymlink != 0:
		return nil
	case perm&0o002 != 0:
		return fmt.Errorf("%s is writable by others (mode %v)", p, perm)
	case perm&0o020 != 0 && !slices.Contains(r.adminGroups, gid):
		return fmt.Errorf("%s is writable by its group (gid %d, mode %v)", p, gid, perm)
	}
	return nil
}

// fileOwner is what Lstat reports about one path.
type fileOwner struct {
	uid, gid uint32
	mode     fs.FileMode
}

// ownerFS is the file system as checkHost reads it: the real one (register_unix.go), or
// a stand-in in tests. Paths are absolute and slash-separated, as on Linux and macOS.
type ownerFS struct {
	// lstat describes p itself, not where a final symbolic link leads.
	lstat func(p string) (fileOwner, error)
	// resolve returns p with every symbolic link resolved (filepath.EvalSymlinks).
	resolve func(p string) (string, error)
	// names lists the entries of the directory dir.
	names func(dir string) ([]string, error)
}

// checkHost says why the relay exe, installed under the rules of goos, must not be
// registered for all users (r): someone other than an administrator could replace what
// it runs, as every user whose browser starts it. That is the relay itself, the daemon
// it starts (client.FindDaemon: inside Downex.app the login item when it is installed,
// else Contents/Helpers/downexd) and the windows that daemon opens (client.Layout's
// Window and Progress), each checked with every directory above it, also where symbolic
// links lead, and where it would be created when it is missing (checkProgram). Inside
// Downex.app also everything in the bundle (checkTree): Launch Services reads the
// Info.plist files of the bundles it opens, and the windows are opened that way.
func (r ownerRules) checkHost(goos, exe string, fsys ownerFS) error {
	l := client.LayoutOf(goos, exe)
	if l.Bundle != "" {
		if err := r.checkPath(l.Bundle, fsys); err != nil {
			return err
		}
		if err := r.checkTree(l.Bundle, fsys); err != nil {
			return err
		}
	}
	for _, p := range hostPrograms(l) {
		if err := r.checkProgram(p, fsys); err != nil {
			return err
		}
	}
	return nil
}

// hostPrograms are the executables that run as the user whose browser starts the relay
// of l: the relay, the daemon that it starts and the windows that the daemon opens.
func hostPrograms(l client.Layout) []string {
	var ps []string
	for _, p := range []string{l.Exe, l.Agent, l.Daemon(), l.Window, l.Progress} {
		if p != "" && !slices.Contains(ps, p) {
			ps = append(ps, p)
		}
	}
	return ps
}

// checkProgram checks the program p (checkPath), and again where symbolic links lead.
// A program that does not exist is checked where it would be created: the deepest of
// its directories that exists, so that nobody else can create it there.
func (r ownerRules) checkProgram(p string, fsys ownerFS) error {
	for {
		_, err := fsys.lstat(p)
		if err == nil {
			break
		}
		parent := path.Dir(p)
		if !errors.Is(err, fs.ErrNotExist) || parent == p {
			return fmt.Errorf("check %s: %w", p, err)
		}
		p = parent
	}
	if err := r.checkPath(p, fsys); err != nil {
		return err
	}
	resolved, err := fsys.resolve(p)
	if err != nil {
		return fmt.Errorf("check %s: %w", p, err)
	}
	if resolved == p {
		return nil
	}
	return r.checkPath(resolved, fsys)
}

// checkPath checks p (not following a final symbolic link) and every directory above it.
func (r ownerRules) checkPath(p string, fsys ownerFS) error {
	for {
		o, err := fsys.lstat(p)
		if err != nil {
			return fmt.Errorf("check %s: %w", p, err)
		}
		if err := r.check(p, o.uid, o.gid, o.mode); err != nil {
			return err
		}
		parent := path.Dir(p)
		if parent == p {
			return nil
		}
		p = parent
	}
}

// checkTree checks everything below the directory dir, without following symbolic
// links, and where each symbolic link leads (checkProgram).
func (r ownerRules) checkTree(dir string, fsys ownerFS) error {
	names, err := fsys.names(dir)
	if err != nil {
		return fmt.Errorf("check %s: %w", dir, err)
	}
	slices.Sort(names)
	for _, n := range names {
		p := path.Join(dir, n)
		o, err := fsys.lstat(p)
		if err != nil {
			return fmt.Errorf("check %s: %w", p, err)
		}
		if err := r.check(p, o.uid, o.gid, o.mode); err != nil {
			return err
		}
		switch {
		case o.mode&fs.ModeSymlink != 0:
			err = r.checkProgram(p, fsys)
		case o.mode.IsDir():
			err = r.checkTree(p, fsys)
		}
		if err != nil {
			return err
		}
	}
	return nil
}
