//go:build !windows

package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSystemHostMustBeRootOnly refuses to register for all users a host that a user
// could replace, and accepts one in root's directories.
func TestSystemHostMustBeRootOnly(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "downex-nmhost")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Owned by the test user, or (as root) below the world-writable temporary directory.
	if err := checkSystemHost(exe); err == nil {
		t.Fatalf("a host in %s was accepted", filepath.Dir(exe))
	}
	sh, err := filepath.EvalSymlinks("/bin/sh")
	if err != nil {
		t.Skipf("no /bin/sh: %v", err)
	}
	if err := checkSystemHost(sh); err != nil {
		t.Fatalf("%s: %v", sh, err)
	}
}

// diskOwners is the real file system below root as checkHost reads it, as if root were
// in a directory that only the test user (standing in for root) can change: the
// directories above root are reported as the user's, mode 0755, and the paths in
// others as another user's.
type diskOwners struct {
	root   string
	uid    uint32
	others map[string]bool
}

func (d diskOwners) fs() ownerFS {
	return ownerFS{
		lstat: func(p string) (fileOwner, error) {
			if p != d.root && !strings.HasPrefix(p, d.root+"/") {
				return fileOwner{uid: d.uid, mode: fs.ModeDir | 0o755}, nil
			}
			o, err := lstatOwner(p)
			if err == nil && d.others[p] {
				o.uid = d.uid + 1
			}
			return o, err
		},
		resolve: filepath.EvalSymlinks,
		names:   dirNames,
	}
}

// TestSystemHostChecksBundle registers a Downex.app on disk for all users: refused when
// anything the relay or its daemon starts, or anything in the bundle, could be changed
// by another user — also a login item, window or progress app the relay itself never
// names, and a missing login item that such a user could create.
func TestSystemHostChecksBundle(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(t *testing.T, app string) (others []string)
		bad    string // the path the refusal names; "" accepted
	}{
		{name: "root-owned bundle"},
		{name: "user-owned LoginItems", bad: "Contents/Library/LoginItems",
			change: func(t *testing.T, app string) []string {
				return []string{filepath.Join(app, "Contents/Library/LoginItems")}
			}},
		{name: "user-owned MacOS", bad: "Contents/MacOS",
			change: func(t *testing.T, app string) []string { return []string{filepath.Join(app, "Contents/MacOS")} }},
		{name: "user-owned agent", bad: "downexd-agent",
			change: func(t *testing.T, app string) []string {
				return []string{filepath.Join(app, "Contents/Library/LoginItems/Downex Agent.app/Contents/MacOS/downexd-agent")}
			}},
		{name: "user-owned progress app", bad: "Downex Progress.app/Contents/MacOS/Downex",
			change: func(t *testing.T, app string) []string {
				return []string{filepath.Join(app, "Contents/Helpers/Downex Progress.app/Contents/MacOS/Downex")}
			}},
		{name: "user-owned Info.plist", bad: "Contents/Info.plist",
			change: func(t *testing.T, app string) []string { return []string{filepath.Join(app, "Contents/Info.plist")} }},
		{name: "world-writable MacOS", bad: "Contents/MacOS",
			change: func(t *testing.T, app string) []string {
				chmod(t, filepath.Join(app, "Contents/MacOS"), 0o777)
				return nil
			}},
		{name: "no login item", change: func(t *testing.T, app string) []string {
			if err := os.RemoveAll(filepath.Join(app, "Contents/Library")); err != nil {
				t.Fatal(err)
			}
			return nil
		}},
		{name: "login item that others could create", bad: "Contents/Library/LoginItems",
			change: func(t *testing.T, app string) []string {
				items := filepath.Join(app, "Contents/Library/LoginItems")
				if err := os.RemoveAll(filepath.Join(items, "Downex Agent.app")); err != nil {
					t.Fatal(err)
				}
				chmod(t, items, 0o777)
				return nil
			}},
		{name: "Library linked to another user's directory", bad: "elsewhere",
			change: func(t *testing.T, app string) []string {
				lib := filepath.Join(app, "Contents/Library")
				elsewhere := filepath.Join(filepath.Dir(app), "elsewhere")
				if err := os.Rename(lib, elsewhere); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(elsewhere, lib); err != nil {
					t.Fatal(err)
				}
				return []string{elsewhere}
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, err := os.MkdirTemp("", "dx")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(root) })
			if root, err = filepath.EvalSymlinks(root); err != nil { // macOS: /var → /private/var
				t.Fatal(err)
			}
			app := filepath.Join(root, "Downex.app")
			for _, f := range []string{
				"Contents/Info.plist",
				"Contents/MacOS/Downex",
				"Contents/Helpers/downexd",
				"Contents/Helpers/downex",
				"Contents/Helpers/downex-nmhost",
				"Contents/Helpers/Downex Progress.app/Contents/Info.plist",
				"Contents/Helpers/Downex Progress.app/Contents/MacOS/Downex",
				"Contents/Library/LoginItems/Downex Agent.app/Contents/Info.plist",
				"Contents/Library/LoginItems/Downex Agent.app/Contents/MacOS/downexd-agent",
				"Contents/Resources/AppIcon.icns",
			} {
				p := filepath.Join(app, f)
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte("stand-in"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			d := diskOwners{root: root, uid: uint32(os.Getuid()), others: map[string]bool{}}
			if tc.change != nil {
				for _, p := range tc.change(t, app) {
					d.others[p] = true
				}
			}
			exe := filepath.Join(app, "Contents/Helpers/downex-nmhost")
			err = systemOwnerRules("darwin", d.uid).checkHost("darwin", exe, d.fs())
			switch {
			case tc.bad == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case tc.bad != "" && err == nil:
				t.Fatal("accepted")
			case tc.bad != "" && !strings.Contains(err.Error(), tc.bad):
				t.Fatalf("%v does not name %s", err, tc.bad)
			}
		})
	}
}

// TestSystemHostChecksSiblings: outside a bundle (Linux) the relay's daemon and window
// are its siblings downexd and downex-ui, checked when they exist.
func TestSystemHostChecksSiblings(t *testing.T) {
	root, err := os.MkdirTemp("", "dx")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if root, err = filepath.EvalSymlinks(root); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(root, "downex-nmhost")
	ui := filepath.Join(root, "downex-ui")
	for _, p := range []string{exe, ui} {
		if err := os.WriteFile(p, []byte("stand-in"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	d := diskOwners{root: root, uid: uint32(os.Getuid()), others: map[string]bool{}}
	rules := systemOwnerRules("linux", d.uid)
	if err := rules.checkHost("linux", exe, d.fs()); err != nil { // no downexd: nothing to replace
		t.Fatalf("refused: %v", err)
	}
	d.others[ui] = true
	if err := rules.checkHost("linux", exe, d.fs()); err == nil || !strings.Contains(err.Error(), "downex-ui") {
		t.Fatalf("a window another user owns: %v", err)
	}
}

func chmod(t *testing.T, p string, mode fs.FileMode) {
	t.Helper()
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
}
