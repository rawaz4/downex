package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestMacTargets checks the manifest locations of docs/MACOS-SPEC.md §4.
func TestMacTargets(t *testing.T) {
	support := "/Users/me/Library/Application Support/"
	user := macUserTargets("/Users/me")
	for b, want := range map[string]struct{ file, profile string }{
		"chrome":   {"Google/Chrome/NativeMessagingHosts/com.downex.host.json", "Google/Chrome"},
		"chromium": {"Chromium/NativeMessagingHosts/com.downex.host.json", "Chromium"},
		"edge":     {"Microsoft Edge/NativeMessagingHosts/com.downex.host.json", "Microsoft Edge"},
		"brave":    {"BraveSoftware/Brave-Browser/NativeMessagingHosts/com.downex.host.json", "BraveSoftware/Brave-Browser"},
		"firefox":  {"Mozilla/NativeMessagingHosts/com.downex.host.json", "Firefox"},
	} {
		got := user[b]
		if got != (target{browser: b, file: support + want.file, profile: support + want.profile}) {
			t.Errorf("%s per user: %+v", b, got)
		}
	}
	if len(user) != len(allBrowsers) {
		t.Errorf("per-user targets: %v", user)
	}

	system := macSystemTargets()
	for b, want := range map[string]string{
		"chrome":   "/Library/Google/Chrome/NativeMessagingHosts/com.downex.host.json",
		"edge":     "/Library/Microsoft/Edge/NativeMessagingHosts/com.downex.host.json",
		"chromium": "/Library/Application Support/Chromium/NativeMessagingHosts/com.downex.host.json",
		"firefox":  "/Library/Application Support/Mozilla/NativeMessagingHosts/com.downex.host.json",
	} {
		if got := system[b]; got != (target{browser: b, file: want}) {
			t.Errorf("%s for all users: %+v", b, got)
		}
	}
	if _, ok := system["brave"]; ok || len(system) != 4 {
		t.Errorf("system targets: %v", system)
	}

	// Brave is left out for all users, and refused when named.
	inst := installation{scope: "all users", targets: system}
	if got, err := selectBrowsers("", inst, false); err != nil || !slices.Equal(got, []string{"chrome", "edge", "chromium", "firefox"}) {
		t.Errorf("default for all users: %q %v", got, err)
	}
	if _, err := selectBrowsers("chrome,brave", inst, false); err == nil || !strings.Contains(err.Error(), "brave") {
		t.Errorf("brave for all users: %v", err)
	}
}

// TestRegisterMacUser registers the host of a Downex.app for the browsers this user ran,
// below a stand-in home directory, and unregisters it again.
func TestRegisterMacUser(t *testing.T) {
	home := t.TempDir()
	inst := installation{scope: "the test", targets: macUserTargets(home), samePath: func(a, b string) bool {
		return filepath.Clean(a) == filepath.Clean(b)
	}}
	for _, b := range []string{"chrome", "firefox"} {
		if err := os.MkdirAll(inst.targets[b].profile, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	browsers, err := selectBrowsers("", inst, true)
	if err != nil || !slices.Equal(browsers, []string{"chrome", "firefox"}) {
		t.Fatalf("detected %q %v", browsers, err)
	}
	exe := "/Applications/Downex.app/Contents/Helpers/downex-nmhost"
	o := regOptions{exe: exe, browsers: browsers, chromeIDs: []string{testChromeID}, firefoxIDs: defaultFirefoxIDs}
	var out bytes.Buffer
	if err := register(inst, o, &out); err != nil {
		t.Fatal(err)
	}
	for _, b := range browsers {
		f := inst.targets[b].file
		if m := readManifest(t, f); m.Path != exe || m.Name != hostName {
			t.Errorf("%s manifest %s: %+v", b, f, m)
		}
		if !strings.Contains(out.String(), f) {
			t.Errorf("the plan does not name %s:\n%s", f, out.String())
		}
	}
	if exists(inst.targets["edge"].file) {
		t.Error("a manifest for a browser this user never ran")
	}
	out.Reset()
	if err := unregister(inst, browsers, false, &out); err != nil {
		t.Fatal(err)
	}
	for _, b := range browsers {
		if exists(inst.targets[b].file) {
			t.Errorf("%s manifest left behind:\n%s", b, out.String())
		}
	}
}

// TestOwnerRules checks who may change a host registered for all users: root only on
// Linux; on macOS also the administrators' groups wheel and admin.
func TestOwnerRules(t *testing.T) {
	const (
		dir     = fs.ModeDir
		symlink = fs.ModeSymlink
		staff   = 20
	)
	for _, tc := range []struct {
		name       string
		uid, gid   uint32
		mode       fs.FileMode
		linux, mac bool // accepted
	}{
		{"root:wheel 0755 (/)", 0, 0, dir | 0o755, true, true},
		{"root:admin 0775 (/Applications)", 0, macAdminGID, dir | 0o775, false, true},
		{"root:wheel 0775", 0, 0, dir | 0o775, false, true},
		{"root:staff 0775", 0, staff, dir | 0o775, false, false},
		{"root:admin 0777", 0, macAdminGID, dir | 0o777, false, false},
		{"root 1777 (/tmp)", 0, 0, dir | fs.ModeSticky | 0o777, false, false},
		{"root executable 0755", 0, 0, 0o755, true, true},
		{"root executable 0757", 0, 0, 0o757, false, false},
		{"user-owned bundle 0755", 501, staff, dir | 0o755, false, false},
		{"root symlink", 0, 0, symlink | 0o777, true, true},
		{"user symlink", 501, staff, symlink | 0o777, false, false},
	} {
		for goos, want := range map[string]bool{"linux": tc.linux, "darwin": tc.mac} {
			err := systemOwnerRules(goos, 0).check("/p", tc.uid, tc.gid, tc.mode)
			if (err == nil) != want {
				t.Errorf("%s on %s: %v, want accepted %v", tc.name, goos, err, want)
			}
			if err != nil && !strings.Contains(err.Error(), "/p") {
				t.Errorf("%s on %s: %v does not name the path", tc.name, goos, err)
			}
		}
	}
	// Tests may require another owner.
	if err := systemOwnerRules("darwin", 501).check("/p", 501, staff, dir|0o755); err != nil {
		t.Error(err)
	}
}
