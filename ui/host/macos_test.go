package host

import (
	"errors"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"

	"downex/internal/client"
)

// fakeInfo is the fs.FileInfo of a file that does not have to exist.
type fakeInfo struct {
	name string
	mode fs.FileMode
}

func (f fakeInfo) Name() string       { return f.name }
func (f fakeInfo) Size() int64        { return 0 }
func (f fakeInfo) Mode() fs.FileMode  { return f.mode }
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fakeInfo) Sys() any           { return nil }

// The macOS rules, as riskOf applies them there (classify with macRisk).
func TestMacRisk(t *testing.T) {
	dir := func(name string) fakeInfo { return fakeInfo{name, fs.ModeDir | 0o755} }
	file := func(name string, perm fs.FileMode) fakeInfo { return fakeInfo{name, perm} }
	for _, tc := range []struct {
		name string
		info fs.FileInfo
		want Risk
	}{
		// Bundles are folders: the name decides.
		{"Setup.app", dir("Setup.app"), RiskProgram},
		{"Downex.APP", dir("Downex.APP"), RiskProgram},
		{"Install.pkg", dir("Install.pkg"), RiskProgram},
		{"Suite.mpkg", dir("Suite.mpkg"), RiskProgram},
		{"Clean Up.workflow", dir("Clean Up.workflow"), RiskProgram},
		{"Pane.prefPane", dir("Pane.prefPane"), RiskProgram},
		{"Flurry.saver", dir("Flurry.saver"), RiskProgram},
		{"Script.scptd", dir("Script.scptd"), RiskProgram},
		{"Disk.sparsebundle", dir("Disk.sparsebundle"), RiskContent},
		// Files: scripts that Terminal runs, AppleScript, profiles, locations …
		{"run.command", nil, RiskProgram},
		{"run.tool", nil, RiskProgram},
		{"Shell.terminal", nil, RiskProgram},
		{"build.sh", nil, RiskProgram},
		{"Hello.scpt", nil, RiskProgram},
		{"hello.applescript", nil, RiskProgram},
		{"Tidy.action", nil, RiskProgram},
		{"Wi-Fi.mobileconfig", nil, RiskProgram},
		{"Game.ipa", nil, RiskProgram},
		{"Notes.fileloc", nil, RiskProgram},
		{"Tool.jar", nil, RiskProgram},
		{"Installer.dmg", nil, RiskProgram},
		{"Site.webloc", nil, RiskContent},
		{"Server.afploc", nil, RiskContent},
		{"Disk.sparseimage", nil, RiskContent},
		{"Disk.iso", nil, RiskContent},
		{"Daily.shortcut", nil, RiskContent},
		{"Budget.xlsm", nil, RiskContent},
		// An execute permission bit: macOS opens the file in Terminal.
		{"tool", file("tool", 0o755), RiskProgram},
		{"notes", file("notes", 0o644), RiskNone},
		{"Folder", dir("Folder"), RiskNone},
		{"report.pdf", file("report.pdf", 0o644), RiskNone},
		{"a.zip", nil, RiskNone},
		{"photo.jpg", nil, RiskNone},
		{"a.txt", nil, RiskNone},
	} {
		if got := Classify(tc.name, tc.info, macRisk); got != tc.want {
			t.Errorf("Classify(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The macOS lists add to the shared ones: no type twice, and each a lower-case extension
// without its dot.
func TestMacExtensionLists(t *testing.T) {
	lists := map[string]map[string]bool{
		"programs": programExtensions, "content": contentExtensions,
		"mac programs": macProgramExtensions, "mac content": macContentExtensions,
	}
	seen := make(map[string]string)
	for list, set := range lists {
		for ext := range set {
			if ext != strings.ToLower(ext) || strings.Contains(ext, ".") {
				t.Errorf("%s: %q is not a lower-case extension without its dot", list, ext)
			}
			if other, dup := seen[ext]; dup && (strings.HasPrefix(list, "mac") || strings.HasPrefix(other, "mac")) {
				t.Errorf("%s is in %s and %s", ext, list, other)
			}
			seen[ext] = list
		}
	}
}

func TestMacOpensAsItem(t *testing.T) {
	for dir, want := range map[string]bool{
		"/Users/me/Downloads":              false,
		"/Users/me/Downloads/Video":        false,
		"/":                                false,
		"/Users/me/Downloads/Setup.app":    true,
		"/Volumes/Disk/Install Thing.pkg":  true,
		"/Users/me/Notes.rtfd":             true,
		"/Users/me/Downloads/release-v1.2": true, // any extension: revealed rather than opened
	} {
		if got := macOpensAsItem(dir); got != want {
			t.Errorf("macOpensAsItem(%q) = %v, want %v", dir, got, want)
		}
	}
}

func TestInterfaceStyleIsDark(t *testing.T) {
	for out, want := range map[string]bool{"Dark\n": true, "dark": true, " Dark \n": true, "": false, "Light\n": false, "Darker": false} {
		if got := interfaceStyleIsDark([]byte(out)); got != want {
			t.Errorf("interfaceStyleIsDark(%q) = %v, want %v", out, got, want)
		}
	}
}

func TestOpenToolFailure(t *testing.T) {
	exit := errors.New("exit status 1")
	for _, stderr := range []string{
		`No application knows how to open URL file:///Users/me/a.ZZZ (Error Domain=NSOSStatusErrorDomain Code=-10814 "kLSApplicationNotFoundErr: E.g. no application claims the file" UserInfo={_LSLine=1489, _LSFunction=runEvaluator}).` + "\n",
		"No application knows how to open /Users/me/a.zzz.\n",
	} {
		err := openToolFailure("/Users/me/a.ZZZ", []byte(stderr), exit)
		if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "no app is set to open .zzz files on this Mac") {
			t.Errorf("no app for the type: %v", err)
		}
	}
	if err := openToolFailure("/Users/me/README", []byte("No application knows how to open /Users/me/README."), exit); !strings.Contains(err.Error(), "open this type of file on") {
		t.Errorf("no app for a file without an extension: %v", err)
	}

	err := openToolFailure("/Users/me/gone.txt", []byte("The file /Users/me/gone.txt does not exist.\nmore\n"), exit)
	if err.Error() != "The file /Users/me/gone.txt does not exist. (/usr/bin/open: exit status 1)" || !errors.Is(err, exit) {
		t.Errorf("another failure: %v", err)
	}
	if err := openToolFailure("/a", nil, exit); err.Error() != "/usr/bin/open: exit status 1" || !errors.Is(err, exit) {
		t.Errorf("a failure without output: %v", err)
	}
	long := strings.Repeat("é", openMessageMax) + "\xff"
	err = openToolFailure("/a", []byte(long), exit)
	msg := strings.TrimSuffix(err.Error(), " (/usr/bin/open: exit status 1)")
	if !utf8.ValidString(err.Error()) || len(msg) > openMessageMax+len("…") || !strings.HasSuffix(msg, "…") {
		t.Errorf("a long message is not cut at a character: %q", msg)
	}
}

func TestHeadBuffer(t *testing.T) {
	b := &headBuffer{max: 5}
	for _, s := range []string{"abc", "defg", "hij"} {
		if n, err := b.Write([]byte(s)); n != len(s) || err != nil {
			t.Fatalf("Write(%q) = %d, %v", s, n, err)
		}
	}
	if got := string(b.Bytes()); got != "abcde" {
		t.Errorf("kept %q, want %q", got, "abcde")
	}
}

// TestHandoffBundles: a window inside Downex.app lets the main window and the progress
// app take the focus from it, whichever of them it is; a development build none.
func TestHandoffBundles(t *testing.T) {
	const app = "/Applications/Downex.app"
	want := []string{app, app + "/Contents/Helpers/Downex Progress.app"}
	for _, exe := range []string{
		app + "/Contents/MacOS/Downex",
		app + "/Contents/Helpers/Downex Progress.app/Contents/MacOS/Downex",
	} {
		if got := handoffBundles(client.LayoutOf("darwin", exe)); !slices.Equal(got, want) {
			t.Errorf("handoffBundles(%s) = %q, want %q", exe, got, want)
		}
	}
	if got := handoffBundles(client.LayoutOf("darwin", "/Users/me/downex/downex-ui")); got != nil {
		t.Errorf("outside a bundle: %q", got)
	}
}
