package main

import (
	"errors"
	"io/fs"
	"maps"
	"path"
	"slices"
	"strings"
	"testing"

	"downex/internal/client"
)

// fakeTree is a file system for ownerRules.checkHost: absolute slash-separated paths and
// what Lstat reports about them; a symbolic link also has its absolute target.
type fakeTree map[string]fakeNode

type fakeNode struct {
	fileOwner
	link string
}

// walk returns p with the symbolic links before its last element resolved, and with
// last the final one too.
func (f fakeTree) walk(p string, last bool, depth int) (string, error) {
	if depth > 8 {
		return "", errors.New("too many levels of symbolic links")
	}
	cur := "/"
	elems := strings.Split(strings.Trim(path.Clean(p), "/"), "/")
	for i, e := range elems {
		if e == "" {
			continue // p is "/"
		}
		next := path.Join(cur, e)
		n, ok := f[next]
		if !ok {
			return "", &fs.PathError{Op: "lstat", Path: p, Err: fs.ErrNotExist}
		}
		if n.link != "" && (last || i < len(elems)-1) {
			r, err := f.walk(n.link, true, depth+1)
			if err != nil {
				return "", err
			}
			next = r
		}
		cur = next
	}
	return cur, nil
}

func (f fakeTree) fs() ownerFS {
	return ownerFS{
		lstat: func(p string) (fileOwner, error) {
			q, err := f.walk(p, false, 0)
			if err != nil {
				return fileOwner{}, err
			}
			return f[q].fileOwner, nil
		},
		resolve: func(p string) (string, error) { return f.walk(p, true, 0) },
		names: func(dir string) ([]string, error) {
			q, err := f.walk(dir, true, 0)
			if err != nil {
				return nil, err
			}
			var names []string
			for p := range f {
				if p != q && path.Dir(p) == q {
					names = append(names, path.Base(p))
				}
			}
			return names, nil
		},
	}
}

// add adds p with owner o, and root:wheel 0755 directories above it where missing.
func (f fakeTree) add(p string, o fileOwner) {
	for d := path.Dir(p); ; d = path.Dir(d) {
		if _, ok := f[d]; !ok {
			f[d] = fakeNode{fileOwner: fileOwner{mode: fs.ModeDir | 0o755}}
		}
		if d == "/" {
			break
		}
	}
	f[p] = fakeNode{fileOwner: o}
}

// link makes p a root-owned symbolic link to target.
func (f fakeTree) link(p, target string) {
	f.removeAll(p)
	f.add(p, fileOwner{mode: fs.ModeSymlink | 0o755})
	f[p] = fakeNode{fileOwner: f[p].fileOwner, link: target}
}

// removeAll removes p and everything below it.
func (f fakeTree) removeAll(p string) {
	maps.DeleteFunc(f, func(k string, _ fakeNode) bool { return k == p || strings.HasPrefix(k, p+"/") })
}

// set changes what Lstat reports about p.
func (f fakeTree) set(p string, change func(*fileOwner)) {
	n := f[p]
	change(&n.fileOwner)
	f[p] = n
}

const (
	testApp   = "/Applications/Downex.app"
	testRelay = testApp + "/Contents/Helpers/downex-nmhost"
	testAgent = testApp + "/Contents/Library/LoginItems/Downex Agent.app/Contents/MacOS/downexd-agent"
	testUser  = 501
	testStaff = 20
)

// installedBundle is Downex.app in /Applications (root:admin 0775), everything in it
// root:wheel, and the home directory /Users/me of user 501.
func installedBundle() fakeTree {
	f := fakeTree{"/": {fileOwner: fileOwner{mode: fs.ModeDir | 0o755}}}
	f.add("/Applications", fileOwner{gid: macAdminGID, mode: fs.ModeDir | 0o775})
	for _, p := range []string{
		"Contents/MacOS/Downex",
		"Contents/Helpers/downexd",
		"Contents/Helpers/downex",
		"Contents/Helpers/downex-nmhost",
		"Contents/Helpers/Downex Progress.app/Contents/MacOS/Downex",
		"Contents/Library/LoginItems/Downex Agent.app/Contents/MacOS/downexd-agent",
	} {
		f.add(path.Join(testApp, p), fileOwner{mode: 0o755})
	}
	for _, p := range []string{
		"Contents/Info.plist",
		"Contents/Helpers/Downex Progress.app/Contents/Info.plist",
		"Contents/Library/LoginItems/Downex Agent.app/Contents/Info.plist",
		"Contents/Resources/AppIcon.icns",
	} {
		f.add(path.Join(testApp, p), fileOwner{mode: 0o644})
	}
	f.add("/Users/me", fileOwner{uid: testUser, gid: testStaff, mode: fs.ModeDir | 0o755})
	return f
}

// TestSystemHostPrograms: the relay inside Downex.app runs, as the browser's user, the
// login item that it starts as the daemon (or Helpers/downexd), and that daemon opens
// the window and the progress app: all of them are checked.
func TestSystemHostPrograms(t *testing.T) {
	got := hostPrograms(client.LayoutOf("darwin", testRelay))
	want := []string{
		testRelay,
		testAgent,
		testApp + "/Contents/Helpers/downexd",
		testApp + "/Contents/MacOS/Downex",
		testApp + "/Contents/Helpers/Downex Progress.app/Contents/MacOS/Downex",
	}
	if !slices.Equal(got, want) {
		t.Errorf("hostPrograms = %q, want %q", got, want)
	}
	got = hostPrograms(client.LayoutOf("darwin", "/usr/local/bin/downex-nmhost"))
	if want := []string{"/usr/local/bin/downex-nmhost", "/usr/local/bin/downexd", "/usr/local/bin/downex-ui"}; !slices.Equal(got, want) {
		t.Errorf("outside a bundle: %q, want %q", got, want)
	}
}

// TestSystemHostBundleOwners registers Downex.app for all users only when nobody but
// root (and the administrators' groups) can change anything in it, or anything that the
// relay, the daemon it starts and that daemon's windows run, also where symbolic links
// lead, and nobody else can create a missing one.
func TestSystemHostBundleOwners(t *testing.T) {
	userOwned := func(p string) func(fakeTree) {
		return func(f fakeTree) { f.set(p, func(o *fileOwner) { o.uid, o.gid = testUser, testStaff }) }
	}
	worldWritable := func(p string) func(fakeTree) {
		return func(f fakeTree) { f.set(p, func(o *fileOwner) { o.mode |= 0o002 }) }
	}
	const (
		library    = testApp + "/Contents/Library"
		loginItems = library + "/LoginItems"
		progress   = testApp + "/Contents/Helpers/Downex Progress.app"
	)
	for _, tc := range []struct {
		name   string
		change func(fakeTree)
		bad    string // the path the refusal names; "" when accepted
	}{
		{"root-owned bundle", func(fakeTree) {}, ""},
		{"user-owned Library", userOwned(library), library},
		{"user-owned LoginItems", userOwned(loginItems), loginItems},
		{"user-owned login item", userOwned(testAgent), testAgent},
		{"user-owned MacOS", userOwned(testApp + "/Contents/MacOS"), testApp + "/Contents/MacOS"},
		{"user-owned window", userOwned(testApp + "/Contents/MacOS/Downex"), testApp + "/Contents/MacOS/Downex"},
		{"user-owned progress app", userOwned(progress), progress},
		{"user-owned Info.plist", userOwned(testApp + "/Contents/Info.plist"), testApp + "/Contents/Info.plist"},
		{"user-owned icon", userOwned(testApp + "/Contents/Resources/AppIcon.icns"), testApp + "/Contents/Resources/AppIcon.icns"},
		{"user-owned Helpers", userOwned(testApp + "/Contents/Helpers"), testApp + "/Contents/Helpers"},
		{"user-owned bundle", userOwned(testApp), testApp},
		{"world-writable LoginItems", worldWritable(loginItems), loginItems},
		{"MacOS writable by admin", func(f fakeTree) {
			f.set(testApp+"/Contents/MacOS", func(o *fileOwner) { o.gid, o.mode = macAdminGID, fs.ModeDir|0o775 })
		}, ""},
		{"MacOS writable by staff", func(f fakeTree) {
			f.set(testApp+"/Contents/MacOS", func(o *fileOwner) { o.gid, o.mode = testStaff, fs.ModeDir|0o775 })
		}, testApp + "/Contents/MacOS"},
		{"no login item", func(f fakeTree) { f.removeAll(library) }, ""},
		{"a login item others could create", func(f fakeTree) {
			f.removeAll(loginItems + "/Downex Agent.app")
			worldWritable(loginItems)(f)
		}, loginItems},
		{"Library linked into a home directory", func(f fakeTree) {
			f.link(library, "/Users/me/lib")
			f.add("/Users/me/lib/LoginItems/Downex Agent.app/Contents/MacOS/downexd-agent", fileOwner{uid: testUser, mode: 0o755})
		}, "/Users/me"},
		{"Library linked to a missing directory", func(f fakeTree) { f.link(library, "/Users/me/lib") }, library},
		{"downexd linked to root's", func(f fakeTree) {
			f.add("/usr/local/bin/downexd", fileOwner{mode: 0o755})
			f.link(testApp+"/Contents/Helpers/downexd", "/usr/local/bin/downexd")
		}, ""},
		{"downexd linked to a user's", func(f fakeTree) {
			f.add("/Users/me/downexd", fileOwner{uid: testUser, mode: 0o755})
			f.link(testApp+"/Contents/Helpers/downexd", "/Users/me/downexd")
		}, "/Users/me/downexd"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := installedBundle()
			tc.change(f)
			err := systemOwnerRules("darwin", 0).checkHost("darwin", testRelay, f.fs())
			switch {
			case tc.bad == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case tc.bad != "" && err == nil:
				t.Fatal("accepted")
			case tc.bad != "" && !strings.HasPrefix(err.Error(), tc.bad+" ") && !strings.HasPrefix(err.Error(), "check "+tc.bad+":"):
				t.Fatalf("%v does not name %s", err, tc.bad)
			}
		})
	}
}
