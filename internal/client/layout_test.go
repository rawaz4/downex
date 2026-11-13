package client

import (
	"path"
	"path/filepath"
	"runtime"
	"testing"
)

// TestLayoutOfMacBundle checks the macOS rules (docs/MACOS-SPEC.md §1) on any system:
// every executable of Downex.app finds the same installation; elsewhere the
// executables are siblings.
func TestLayoutOfMacBundle(t *testing.T) {
	bundle := func(app string) Layout {
		c := app + "/Contents"
		return Layout{
			Bundle:   app,
			Dir:      c + "/Helpers",
			Window:   c + "/MacOS/Downex",
			Progress: c + "/Helpers/Downex Progress.app/Contents/MacOS/Downex",
			Agent:    c + "/Library/LoginItems/Downex Agent.app/Contents/MacOS/downexd-agent",
		}
	}
	siblings := func(dir string) Layout {
		ui := path.Join(dir, "downex-ui")
		return Layout{Dir: dir, Window: ui, Progress: ui}
	}
	app := bundle("/Applications/Downex.app")
	for _, tc := range []struct {
		name, exe string
		want      Layout
	}{
		{"CLI", "/Applications/Downex.app/Contents/Helpers/downex", app},
		{"relay", "/Applications/Downex.app/Contents/Helpers/downex-nmhost", app},
		{"daemon", "/Applications/Downex.app/Contents/Helpers/downexd", app},
		{"window", "/Applications/Downex.app/Contents/MacOS/Downex", app},
		{"progress app", "/Applications/Downex.app/Contents/Helpers/Downex Progress.app/Contents/MacOS/Downex", app},
		{"login item", "/Applications/Downex.app/Contents/Library/LoginItems/Downex Agent.app/Contents/MacOS/downexd-agent", app},
		{"unclean path", "/Applications/Downex.app/Contents/Helpers/../MacOS/./Downex", app},
		{"renamed bundle", "/Users/me/Apps/Downex 2.app/Contents/Helpers/downexd", bundle("/Users/me/Apps/Downex 2.app")},
		{"other case", "/applications/downex.APP/contents/helpers/downex", Layout{
			Bundle:   "/applications/downex.APP",
			Dir:      "/applications/downex.APP/contents/Helpers",
			Window:   "/applications/downex.APP/contents/MacOS/Downex",
			Progress: "/applications/downex.APP/contents/Helpers/Downex Progress.app/Contents/MacOS/Downex",
			Agent:    "/applications/downex.APP/contents/Library/LoginItems/Downex Agent.app/Contents/MacOS/downexd-agent",
		}},
		{"window of a dev build", "/src/ui/build/bin/Downex.app/Contents/MacOS/Downex", bundle("/src/ui/build/bin/Downex.app")},
		{"progress app alone", "/opt/Helpers/P.app/Contents/MacOS/Downex", bundle("/opt/Helpers/P.app")},
		{"login item outside Library", "/A.app/Contents/LoginItems/G.app/Contents/MacOS/x", bundle("/A.app/Contents/LoginItems/G.app")},
		{"in PATH", "/usr/local/bin/downex", siblings("/usr/local/bin")},
		{"Helpers outside a bundle", "/opt/Helpers/downexd", siblings("/opt/Helpers")},
		{"Contents not in an app", "/opt/Downex/Contents/Helpers/downex", siblings("/opt/Downex/Contents/Helpers")},
		{"nameless app", "/x/.app/Contents/Helpers/downex", siblings("/x/.app/Contents/Helpers")},
		{"MacOS outside a bundle", "/opt/Downex/Contents/MacOS/Downex", siblings("/opt/Downex/Contents/MacOS")},
		{"relative path", "Downex.app/Contents/Helpers/downex", siblings("Downex.app/Contents/Helpers")},
		{"root", "/downexd", siblings("/")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := LayoutOf("darwin", tc.exe)
			want := tc.want
			want.goos = "darwin"
			want.Exe = path.Clean(tc.exe)
			if got != want {
				t.Errorf("LayoutOf(%q)\n got %+v\nwant %+v", tc.exe, got, want)
			}
			if d, w := got.Daemon(), path.Join(want.Dir, "downexd"); d != w {
				t.Errorf("Daemon() = %q, want %q", d, w)
			}
		})
	}
}

// TestLayoutOfSiblings checks Windows and Linux (on the native path syntax): the
// executables are siblings, whatever the directories are called.
func TestLayoutOfSiblings(t *testing.T) {
	root := filepath.Join(string(filepath.Separator)+"opt", "Downex")
	for _, tc := range []struct {
		goos, exe, daemon, ui string
	}{
		{"windows", filepath.Join(root, "downex.exe"), "downexd.exe", "downex-ui.exe"},
		{"windows", filepath.Join(root, "Downex.app", "Contents", "Helpers", "downex-nmhost.exe"), "downexd.exe", "downex-ui.exe"},
		{"linux", filepath.Join(root, "downex-nmhost"), "downexd", "downex-ui"},
		{"linux", filepath.Join(root, "Downex.app", "Contents", "MacOS", "Downex"), "downexd", "downex-ui"},
	} {
		l := LayoutOf(tc.goos, tc.exe)
		dir := filepath.Dir(tc.exe)
		want := Layout{Exe: tc.exe, Dir: dir, Window: filepath.Join(dir, tc.ui), Progress: filepath.Join(dir, tc.ui), goos: tc.goos}
		if l != want {
			t.Errorf("LayoutOf(%s, %q)\n got %+v\nwant %+v", tc.goos, tc.exe, l, want)
		}
		if d := l.Daemon(); d != filepath.Join(dir, tc.daemon) {
			t.Errorf("%s Daemon() = %q", tc.goos, d)
		}
	}
}

func TestUIName(t *testing.T) {
	want := "downex-ui"
	if runtime.GOOS == "windows" {
		want = "downex-ui.exe"
	}
	if UIName() != want {
		t.Errorf("UIName() = %q, want %q", UIName(), want)
	}
}

// TestFindLayoutOfThisExecutable: the test binary is not in a bundle, so the window
// and the daemon are its siblings.
func TestFindLayoutOfThisExecutable(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "downex-cli")
	l, err := FindLayout(func() (string, error) { return exe, nil })
	if err != nil {
		t.Fatal(err)
	}
	if l.Exe != exe || l.Bundle != "" || l.Daemon() != filepath.Join(filepath.Dir(exe), DaemonName()) ||
		l.Window != filepath.Join(filepath.Dir(exe), UIName()) || l.Progress != l.Window || l.Agent != "" {
		t.Errorf("layout %+v", l)
	}
}

// TestAutostart: inside Downex.app clients start the login item, the daemon with the
// menu-bar icon, when it is installed (docs/MACOS-SPEC.md §1), else Contents/Helpers/downexd;
// outside a bundle, and on other systems, the downexd next to them.
func TestAutostart(t *testing.T) {
	const helpers = "/Applications/Downex.app/Contents/Helpers"
	const agent = "/Applications/Downex.app/Contents/Library/LoginItems/Downex Agent.app/Contents/MacOS/downexd-agent"
	everything := func(string) bool { return true }
	nothing := func(string) bool { return false }
	for _, exe := range []string{helpers + "/downex", helpers + "/downex-nmhost", "/Applications/Downex.app/Contents/MacOS/Downex",
		helpers + "/Downex Progress.app/Contents/MacOS/Downex", agent} {
		l := LayoutOf("darwin", exe)
		if got := l.autostart(everything); got != agent {
			t.Errorf("from %s with the login item: %q, want %q", exe, got, agent)
		}
		if got := l.autostart(nothing); got != helpers+"/downexd" {
			t.Errorf("from %s without the login item: %q", exe, got)
		}
	}
	if got := LayoutOf("darwin", "/usr/local/bin/downex").autostart(everything); got != "/usr/local/bin/downexd" {
		t.Errorf("outside a bundle: %q", got)
	}
	exe := filepath.Join(string(filepath.Separator)+"opt", "Downex", "downex.exe")
	if got := LayoutOf("windows", exe).autostart(everything); got != filepath.Join(filepath.Dir(exe), "downexd.exe") {
		t.Errorf("windows: %q", got)
	}
}

// TestTranslocated: the read-only copy that Gatekeeper runs of a quarantined bundle
// opened in place (App Translocation) is recognised by its path element.
func TestTranslocated(t *testing.T) {
	const translocated = "/private/var/folders/x1/abc/T/AppTranslocation/0F1E2D3C-4B5A-6978-8796-A5B4C3D2E1F0/d/Downex.app"
	for p, want := range map[string]bool{
		translocated + "/Contents/Helpers/downex-nmhost":                            true,
		translocated + "/Contents/MacOS/Downex":                                     true,
		"/Applications/Downex.app/Contents/Helpers/downex-nmhost":                   false,
		"/Users/me/AppTranslocationNotes/Downex.app/Contents/Helpers/downex-nmhost": false,
	} {
		if got := MacTranslocated(p); got != want {
			t.Errorf("MacTranslocated(%q) = %v", p, got)
		}
	}
	if !LayoutOf("darwin", translocated+"/Contents/Library/LoginItems/Downex Agent.app/Contents/MacOS/downexd-agent").Translocated() {
		t.Error("the login item of a translocated bundle")
	}
	if LayoutOf("darwin", "/Applications/Downex.app/Contents/Helpers/downexd").Translocated() ||
		LayoutOf("darwin", "/x/AppTranslocation/y/downexd").Translocated() {
		t.Error("an installed bundle, or no bundle at all, is not translocated")
	}
}
