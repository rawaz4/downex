package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
)

const testChromeID = "dlgpbdafcmciojddbbjikgeeefipejcn"

// otherChromeID stands for a store's ID of the same extension.
const otherChromeID = "abcdefghijklmnopabcdefghijklmnop"

// memRegistry is an in-memory regStore.
type memRegistry struct {
	mu   sync.Mutex
	keys map[string]map[string]string
}

func newMemRegistry() *memRegistry { return &memRegistry{keys: map[string]map[string]string{}} }

func (r *memRegistry) root() string { return "HKTEST" }

func (r *memRegistry) value(key, name string) (string, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.keys[key][name]
	return v, ok, nil
}

func (r *memRegistry) setValue(key, name, v string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.keys[key] == nil {
		r.keys[key] = map[string]string{}
	}
	r.keys[key][name] = v
	return nil
}

func (r *memRegistry) deleteValue(key, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.keys[key], name)
	return nil
}

func (r *memRegistry) deleteKey(key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.keys, key)
	return nil
}

// testInstallation places every browser's manifest in a temporary directory and,
// with a registry, its key in reg.
func testInstallation(t *testing.T, reg *memRegistry) installation {
	t.Helper()
	dir := t.TempDir()
	inst := installation{scope: "the test", targets: map[string]target{}, samePath: func(a, b string) bool {
		return filepath.Clean(a) == filepath.Clean(b)
	}}
	if reg != nil {
		inst.reg = reg
	}
	for _, b := range allBrowsers {
		tg := target{browser: b, file: filepath.Join(dir, b, hostName+".json"), profile: filepath.Join(dir, "profiles", b)}
		if reg != nil {
			tg.regKey = `Software\Test\` + b + `\` + hostName
		}
		inst.targets[b] = tg
	}
	return inst
}

func testOptions(t *testing.T, browsers ...string) regOptions {
	if len(browsers) == 0 {
		browsers = allBrowsers
	}
	exe := filepath.Join(t.TempDir(), "bin", "downex-nmhost")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	return regOptions{exe: exe, browsers: browsers, chromeIDs: []string{testChromeID}, firefoxIDs: defaultFirefoxIDs}
}

func readManifest(t *testing.T, path string) manifest {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m manifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return m
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestRegisterWritesManifestsAndKeys(t *testing.T) {
	for _, withReg := range []bool{false, true} {
		name := "files only"
		if withReg {
			name = "with registry"
		}
		t.Run(name, func(t *testing.T) {
			var reg *memRegistry
			if withReg {
				reg = newMemRegistry()
			}
			inst := testInstallation(t, reg)
			o := testOptions(t)
			var out bytes.Buffer
			if err := register(inst, o, &out); err != nil {
				t.Fatal(err)
			}
			for _, b := range allBrowsers {
				tg := inst.targets[b]
				m := readManifest(t, tg.file)
				if m.Name != hostName || m.Path != o.exe || m.Type != "stdio" || m.Description == "" || !filepath.IsAbs(m.Path) {
					t.Errorf("%s manifest: %+v", b, m)
				}
				if b == "firefox" {
					// the permanent AMO ID only: browser@downex.local is not an ID Downex owns
					if !slices.Equal(m.AllowedExtensions, []string{"extension@downex.app"}) || m.AllowedOrigins != nil {
						t.Errorf("firefox manifest: %+v", m)
					}
				} else if !slices.Equal(m.AllowedOrigins, []string{"chrome-extension://" + testChromeID + "/"}) || m.AllowedExtensions != nil {
					t.Errorf("%s manifest: %+v", b, m)
				}
				if withReg {
					if v, ok, _ := reg.value(tg.regKey, ""); !ok || v != tg.file {
						t.Errorf("%s key = %q %v", b, v, ok)
					}
				}
			}
			if !strings.Contains(out.String(), "Plan:") || !strings.Contains(out.String(), "Done:") {
				t.Errorf("output:\n%s", out.String())
			}

			// Idempotent: nothing changes the second time.
			before := snapshot(t, inst)
			out.Reset()
			if err := register(inst, o, &out); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "Nothing to change") || strings.Contains(out.String(), "Plan:") {
				t.Errorf("second register:\n%s", out.String())
			}
			if after := snapshot(t, inst); !maps2Equal(before, after) {
				t.Error("second register changed files")
			}
			if withReg && len(reg.keys) != len(allBrowsers) {
				t.Errorf("registry keys: %v", reg.keys)
			}

			// Unregister removes everything again.
			out.Reset()
			if err := unregister(inst, allBrowsers, false, &out); err != nil {
				t.Fatal(err)
			}
			for _, b := range allBrowsers {
				if exists(inst.targets[b].file) {
					t.Errorf("%s manifest left behind", b)
				}
			}
			if withReg && len(reg.keys) != 0 {
				t.Errorf("registry keys left behind: %v", reg.keys)
			}
			out.Reset()
			if err := unregister(inst, allBrowsers, false, &out); err != nil || !strings.Contains(out.String(), "Nothing to change") {
				t.Errorf("second unregister: %v\n%s", err, out.String())
			}
		})
	}
}

// snapshot returns the content of every file below the installation's directory.
func snapshot(t *testing.T, inst installation) map[string]string {
	t.Helper()
	root := filepath.Dir(filepath.Dir(inst.targets["chrome"].file))
	files := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		files[p] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func maps2Equal(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestRegisterBacksUpAndUnregisterRestores(t *testing.T) {
	reg := newMemRegistry()
	inst := testInstallation(t, reg)
	o := testOptions(t)
	chrome, edge, brave, firefox := inst.targets["chrome"], inst.targets["edge"], inst.targets["brave"], inst.targets["firefox"]

	// Someone else's manifest and registry value are in place before.
	foreign := []byte(`{"name":"com.downex.host","path":"/opt/other/relay","type":"stdio"}`)
	if err := writeFileAtomic(chrome.file, foreign); err != nil {
		t.Fatal(err)
	}
	_ = reg.setValue(edge.regKey, "", `C:\Other\edge-host.json`)

	var out bytes.Buffer
	if err := register(inst, o, &out); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(chrome.file + backupSuffix); err != nil || !bytes.Equal(b, foreign) {
		t.Fatalf("chrome backup: %q %v", b, err)
	}
	if v, _, _ := reg.value(edge.regKey, backupValueName); v != `C:\Other\edge-host.json` {
		t.Fatalf("edge backup value %q", v)
	}
	if v, _, _ := reg.value(edge.regKey, ""); v != edge.file {
		t.Fatalf("edge value %q", v)
	}
	if !strings.Contains(out.String(), "back up") {
		t.Errorf("plan does not mention the backups:\n%s", out.String())
	}

	// Another program takes over the brave key; firefox's manifest is replaced by one
	// that is not ours.
	_ = reg.setValue(brave.regKey, "", `C:\Someone\else.json`)
	notOurs := []byte(`{"name":"org.example.other","path":"/usr/bin/other"}`)
	if err := os.WriteFile(firefox.file, notOurs, 0o644); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	if err := unregister(inst, allBrowsers, false, &out); err != nil {
		t.Fatal(err)
	}
	// Restored: the previous chrome manifest and edge value.
	if b, _ := os.ReadFile(chrome.file); !bytes.Equal(b, foreign) || exists(chrome.file+backupSuffix) {
		t.Errorf("chrome manifest not restored: %q", b)
	}
	if v, ok, _ := reg.value(edge.regKey, ""); v != `C:\Other\edge-host.json` || !ok {
		t.Errorf("edge value not restored: %q", v)
	}
	if _, ok, _ := reg.value(edge.regKey, backupValueName); ok {
		t.Error("edge backup value left behind")
	}
	// Left alone: entries that are not ours.
	if v, _, _ := reg.value(brave.regKey, ""); v != `C:\Someone\else.json` {
		t.Errorf("brave value changed: %q", v)
	}
	if b, _ := os.ReadFile(firefox.file); !bytes.Equal(b, notOurs) {
		t.Errorf("foreign firefox manifest changed: %q", b)
	}
	// Removed: ours.
	if exists(brave.file) || exists(inst.targets["chromium"].file) {
		t.Error("our manifests left behind")
	}
	if _, ok, _ := reg.value(inst.targets["chromium"].regKey, ""); ok {
		t.Error("chromium key left behind")
	}
	if !strings.Contains(out.String(), "unchanged:") {
		t.Errorf("skipped entries are not reported:\n%s", out.String())
	}
}

// TestReregisterAfterMove backs up the older registration of this host when the
// executable moved, and unregister then removes both.
func TestReregisterAfterMove(t *testing.T) {
	inst := testInstallation(t, nil)
	o := testOptions(t, "chrome")
	var out bytes.Buffer
	if err := register(inst, o, &out); err != nil {
		t.Fatal(err)
	}
	o2 := o
	o2.exe = filepath.Join(t.TempDir(), filepath.Base(o.exe))
	if err := register(inst, o2, &out); err != nil {
		t.Fatal(err)
	}
	f := inst.targets["chrome"].file
	if m := readManifest(t, f); m.Path != o2.exe {
		t.Fatalf("path %q", m.Path)
	}
	if !exists(f + backupSuffix) {
		t.Fatal("no backup of the older registration")
	}
	if err := unregister(inst, []string{"chrome"}, false, &out); err != nil {
		t.Fatal(err)
	}
	if exists(f) || exists(f+backupSuffix) {
		t.Fatalf("left behind after unregister:\n%s", out.String())
	}
}

// TestRegisterKeep: --keep (the macOS window's registration at its start) leaves a manifest of
// this host that allows other IDs as well as it is, adds the IDs it lacks without dropping
// those it has, rewrites one that names another path, writes the missing ones, and never
// replaces a .bak that is there.
func TestRegisterKeep(t *testing.T) {
	const handID = "abcdefghijklmnopabcdefghijklmnop" // an ID registered by hand, say the Edge Add-ons copy's
	inst := testInstallation(t, nil)
	o := testOptions(t, "chrome", "edge", "brave", "firefox")
	chrome, edge, brave, firefox := inst.targets["chrome"].file, inst.targets["edge"].file, inst.targets["brave"].file, inst.targets["firefox"].file
	var out bytes.Buffer

	// By hand: chrome with the built-in ID and another one, firefox with one ID only.
	byHand := o
	byHand.chromeIDs = []string{testChromeID, handID}
	byHand.firefoxIDs = []string{"extension@downex.app"}
	if err := register(inst, byHand, &out); err != nil {
		t.Fatal(err)
	}
	handChrome, _ := os.ReadFile(chrome)
	// edge names a Downex.app that was moved, and a .bak of the user's is there.
	moved := o
	moved.exe = filepath.Join(t.TempDir(), filepath.Base(o.exe))
	moved.browsers = []string{"edge"}
	if err := writeFileAtomic(edge, manifestFor("edge", moved)); err != nil {
		t.Fatal(err)
	}
	userBak := []byte(`{"name":"com.downex.host","path":"/kept/by/the/user"}`)
	if err := writeFileAtomic(edge+backupSuffix, userBak); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(brave); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{chrome, firefox} {
		_ = os.Remove(f + backupSuffix)
	}

	k := o
	k.keep = true
	out.Reset()
	if err := register(inst, k, &out); err != nil {
		t.Fatal(err)
	}
	// chrome: allows the built-in ID already, so it is left as it is (the hand ID stays).
	if b, _ := os.ReadFile(chrome); !bytes.Equal(b, handChrome) || exists(chrome+backupSuffix) {
		t.Errorf("chrome manifest changed:\n%s", b)
	}
	// firefox: the missing built-in ID is added after the one it had.
	if m := readManifest(t, firefox); !slices.Equal(m.AllowedExtensions, union([]string{"extension@downex.app"}, defaultFirefoxIDs)) ||
		m.Path != o.exe {
		t.Errorf("firefox manifest %+v", m)
	}
	// edge: the path of this executable, the user's .bak as it was.
	if m := readManifest(t, edge); m.Path != o.exe || !slices.Equal(m.AllowedOrigins, []string{"chrome-extension://" + testChromeID + "/"}) {
		t.Errorf("edge manifest %+v", m)
	}
	if b, _ := os.ReadFile(edge + backupSuffix); !bytes.Equal(b, userBak) {
		t.Errorf("edge .bak replaced: %s", b)
	}
	// brave: written again.
	if m := readManifest(t, brave); m.Path != o.exe {
		t.Errorf("brave manifest %+v", m)
	}

	// Run again: nothing left to change.
	out.Reset()
	if err := register(inst, k, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Nothing to change") {
		t.Errorf("a second --keep changed something:\n%s", out.String())
	}

	// A manifest written by an earlier Downex that allowed browser@downex.local (an ID Downex
	// does not own): --keep drops it, keeping the other IDs, and leaves the result alone after.
	legacy := o
	legacy.firefoxIDs = []string{"extension@downex.app", "browser@downex.local", "kept@example.org"}
	if err := writeFileAtomic(firefox, manifestFor("firefox", legacy)); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := register(inst, k, &out); err != nil {
		t.Fatal(err)
	}
	if m := readManifest(t, firefox); !slices.Equal(m.AllowedExtensions, []string{"extension@downex.app", "kept@example.org"}) {
		t.Errorf("--keep on a manifest allowing browser@downex.local: firefox %+v", m)
	}
	// As many IDs after the drop as before (one dropped, one added) is a change too.
	legacy.firefoxIDs = []string{"browser@downex.local"}
	if err := writeFileAtomic(firefox, manifestFor("firefox", legacy)); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := register(inst, k, &out); err != nil {
		t.Fatal(err)
	}
	if m := readManifest(t, firefox); !slices.Equal(m.AllowedExtensions, []string{"extension@downex.app"}) {
		t.Errorf("--keep replacing browser@downex.local: firefox %+v", m)
	}
	out.Reset()
	if err := register(inst, k, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Nothing to change") {
		t.Errorf("--keep after dropping browser@downex.local changed something:\n%s", out.String())
	}
	// Named with --firefox-extension-id it is still written: the user's choice, never a default.
	named := k
	named.firefoxIDs = []string{"extension@downex.app", "browser@downex.local"}
	out.Reset()
	if err := register(inst, named, &out); err != nil {
		t.Fatal(err)
	}
	if m := readManifest(t, firefox); !slices.Equal(m.AllowedExtensions, named.firefoxIDs) {
		t.Errorf("--keep --firefox-extension-id browser@downex.local: firefox %+v", m)
	}
	// Without --keep, register writes exactly the built-in IDs again, as before.
	out.Reset()
	if err := register(inst, o, &out); err != nil {
		t.Fatal(err)
	}
	if m := readManifest(t, chrome); !slices.Equal(m.AllowedOrigins, []string{"chrome-extension://" + testChromeID + "/"}) {
		t.Errorf("register without --keep: chrome %+v", m)
	}
}

func TestDryRunChangesNothing(t *testing.T) {
	reg := newMemRegistry()
	inst := testInstallation(t, reg)
	o := testOptions(t)
	o.dryRun = true
	var out bytes.Buffer
	if err := register(inst, o, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Dry run") || len(snapshot(t, inst)) != 0 || len(reg.keys) != 0 {
		t.Fatalf("dry run changed something:\n%s", out.String())
	}
}

func TestSelectBrowsersAndIDs(t *testing.T) {
	inst := testInstallation(t, nil)
	if _, err := selectBrowsers("", inst, true); err == nil {
		t.Error("no profiles: no error")
	}
	for _, b := range []string{"edge", "firefox"} {
		if err := os.MkdirAll(inst.targets[b].profile, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := selectBrowsers("", inst, true); err != nil || !slices.Equal(got, []string{"edge", "firefox"}) {
		t.Errorf("detected %q %v", got, err)
	}
	if got, _ := selectBrowsers("", inst, false); !slices.Equal(got, allBrowsers) {
		t.Errorf("all: %q", got)
	}
	if got, err := selectBrowsers(" Chrome ,brave,chrome", inst, true); err != nil || !slices.Equal(got, []string{"chrome", "brave"}) {
		t.Errorf("explicit: %q %v", got, err)
	}
	for _, bad := range []string{"opera", ",", "chrome,safari"} {
		if _, err := selectBrowsers(bad, inst, true); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}

	ok := regOptions{browsers: []string{"chrome", "firefox"}, chromeIDs: []string{testChromeID}, firefoxIDs: defaultFirefoxIDs}
	if err := checkIDs(ok); err != nil {
		t.Error(err)
	}
	for name, o := range map[string]regOptions{
		"no chrome id":    {browsers: []string{"edge"}},
		"bad chrome id":   {browsers: []string{"chrome"}, chromeIDs: []string{"ABCDEFGHIJKLMNOPQRSTUVWXYZABCDEF"}},
		"short chrome id": {browsers: []string{"chrome"}, chromeIDs: []string{"abc"}},
		"bad firefox id":  {browsers: []string{"firefox"}, firefoxIDs: []string{`"quoted"@x`}},
		"bad second id":   {browsers: []string{"firefox"}, firefoxIDs: []string{"extension@downex.app", ""}},
		"no firefox id":   {browsers: []string{"firefox"}},
	} {
		if err := checkIDs(o); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if err := checkIDs(regOptions{browsers: []string{"firefox"}, firefoxIDs: []string{"{12345678-1234-1234-1234-123456789abc}"}}); err != nil {
		t.Errorf("firefox only, GUID id: %v", err)
	}
}

func TestRegisterCommandRefusesWithoutChromeID(t *testing.T) {
	isolateUserDirs(t)
	old := DefaultChromeExtensionIDs
	t.Cleanup(func() { DefaultChromeExtensionIDs = old })

	DefaultChromeExtensionIDs = ""
	var stdout, stderr bytes.Buffer
	code := runRegister([]string{"--browsers", "chrome,edge", "--dry-run"}, &stdout, &stderr)
	if code != exitUsage || !strings.Contains(stderr.String(), "--chrome-extension-id") {
		t.Fatalf("exit %d, stderr %s", code, stderr.String())
	}
	stderr.Reset()
	if code := runRegister([]string{"--browsers", "chrome", "--chrome-extension-id", "nope", "--dry-run"}, &stdout, &stderr); code != exitUsage {
		t.Fatalf("invalid id: exit %d", code)
	}

	// The built-in IDs (as set with -ldflags) are used, and a flag overrides them.
	DefaultChromeExtensionIDs = testChromeID + ", " + otherChromeID
	stdout.Reset()
	if code := runRegister([]string{"--browsers", "chrome", "--dry-run"}, &stdout, &stderr); code != exitOK ||
		!strings.Contains(stdout.String(), "Dry run") ||
		!strings.Contains(stdout.String(), "Chromium extension IDs: "+testChromeID+", "+otherChromeID+"\n") {
		t.Fatalf("built-in ids: exit %d\n%s%s", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	if code := runRegister([]string{"--browsers", "chrome", "--chrome-extension-id", otherChromeID, "--dry-run"}, &stdout, &stderr); code != exitOK ||
		!strings.Contains(stdout.String(), "Chromium extension IDs: "+otherChromeID+"\n") {
		t.Fatalf("flag over built-in ids: exit %d\n%s%s", code, stdout.String(), stderr.String())
	}
	// A broken built-in list is the build's fault: refused whole, never registered in part.
	for _, bad := range []string{testChromeID + ",nope", testChromeID + ",," + otherChromeID, testChromeID + ";" + otherChromeID} {
		DefaultChromeExtensionIDs = bad
		stderr.Reset()
		if code := runRegister([]string{"--browsers", "chrome", "--dry-run"}, &stdout, &stderr); code != exitError ||
			!strings.Contains(stderr.String(), "built in") {
			t.Fatalf("built-in %q: exit %d, stderr %s", bad, code, stderr.String())
		}
	}
	// With a valid built-in list, a bad --firefox-extension-id is the command line's fault.
	DefaultChromeExtensionIDs = testChromeID + "," + otherChromeID
	for _, browsers := range []string{"firefox", "chrome,firefox"} {
		stderr.Reset()
		if code := runRegister([]string{"--browsers", browsers, "--firefox-extension-id", "bad", "--dry-run"}, &stdout, &stderr); code != exitUsage ||
			strings.Contains(stderr.String(), "built in") || !strings.Contains(stderr.String(), "Firefox add-on ID") {
			t.Fatalf("bad firefox id (%s): exit %d, stderr %s", browsers, code, stderr.String())
		}
	}
	// Firefox: the AMO ID by default, the flag's IDs instead.
	stdout.Reset()
	if code := runRegister([]string{"--browsers", "firefox", "--dry-run"}, &stdout, &stderr); code != exitOK ||
		!strings.Contains(stdout.String(), "Firefox add-on IDs: extension@downex.app\n") {
		t.Fatalf("default firefox ids: exit %d\n%s%s", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	if code := runRegister([]string{"--browsers", "firefox", "--firefox-extension-id", "a@example.test,b@example.test", "--dry-run"}, &stdout, &stderr); code != exitOK ||
		!strings.Contains(stdout.String(), "Firefox add-on IDs: a@example.test, b@example.test\n") {
		t.Fatalf("flag over default firefox ids: exit %d\n%s%s", code, stdout.String(), stderr.String())
	}
	if code := runRegister([]string{"--bogus"}, &stdout, &stderr); code != exitUsage {
		t.Fatalf("unknown flag: exit %d", code)
	}
	if code := runUnregister([]string{"--browsers", "firefox", "--dry-run"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("unregister dry run: exit %d\n%s", code, stderr.String())
	}
}

// isolateUserDirs points the per-user locations of the default installation at a
// temporary directory (the registry, read-only in dry runs, is the real one).
func isolateUserDirs(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LOCALAPPDATA", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("HOME", dir)
}

func TestOurManifest(t *testing.T) {
	for in, want := range map[string]bool{
		`{"name":"com.downex.host","path":"/usr/local/bin/downex-nmhost"}`:  true,
		`{"name":"com.downex.host","path":"C:\\Tools\\Downex-NMHost.EXE"}`:  true,
		`{"name":"com.downex.host","path":"/opt/other/relay"}`:              false,
		`{"name":"org.example.host","path":"/usr/local/bin/downex-nmhost"}`: false,
		`not json`: false,
	} {
		if got := ourManifest([]byte(in)); got != want {
			t.Errorf("ourManifest(%s) = %v", in, got)
		}
	}
}

func TestRegisterAllowsEveryChromeID(t *testing.T) {
	inst := testInstallation(t, nil)
	o := testOptions(t, "chrome", "edge", "firefox")
	o.chromeIDs = []string{testChromeID, otherChromeID}
	var out bytes.Buffer
	if err := register(inst, o, &out); err != nil {
		t.Fatal(err)
	}
	want := []string{"chrome-extension://" + testChromeID + "/", "chrome-extension://" + otherChromeID + "/"}
	for _, b := range []string{"chrome", "edge"} {
		if m := readManifest(t, inst.targets[b].file); !slices.Equal(m.AllowedOrigins, want) {
			t.Errorf("%s allowed_origins = %q", b, m.AllowedOrigins)
		}
	}
	if m := readManifest(t, inst.targets["firefox"].file); m.AllowedOrigins != nil {
		t.Errorf("firefox manifest: %+v", m)
	}
	// Another list replaces the manifests: a store ID added later reaches every browser.
	o.chromeIDs = []string{testChromeID}
	out.Reset()
	if err := register(inst, o, &out); err != nil || strings.Contains(out.String(), "Nothing to change") {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if m := readManifest(t, inst.targets["edge"].file); !slices.Equal(m.AllowedOrigins, want[:1]) {
		t.Errorf("edge allowed_origins after re-register = %q", m.AllowedOrigins)
	}
}

func TestAppendIDs(t *testing.T) {
	for in, want := range map[string][]string{
		"":           nil,
		"  ":         nil,
		testChromeID: {testChromeID},
		" " + testChromeID + " , " + otherChromeID + " ": {testChromeID, otherChromeID},
		testChromeID + "," + testChromeID:                {testChromeID},
		testChromeID + ",,":                              {testChromeID, "", ""},
	} {
		if got := appendIDs(nil, in); !slices.Equal(got, want) {
			t.Errorf("appendIDs(%q) = %q, want %q", in, got, want)
		}
	}
	var f listFlag
	for _, v := range []string{testChromeID, otherChromeID + "," + testChromeID} {
		if err := f.Set(v); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Equal(f, listFlag{testChromeID, otherChromeID}) {
		t.Errorf("repeated flag = %q", f)
	}
}

// TestExtensionIDFile checks ext/EXTENSION_ID.txt as the build scripts read it into
// -X main.DefaultChromeExtensionIDs: one ID per line, "#" starts a comment line, blank
// lines are skipped. Every ID must be valid (a bad line fails the build, never dropped),
// and the ID of this repository's key (testChromeID) must be among them.
func TestExtensionIDFile(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "ext", "EXTENSION_ID.txt"))
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !chromeIDPattern.MatchString(line) {
			t.Errorf("EXTENSION_ID.txt: %q is not a Chromium extension ID", line)
		}
		ids = append(ids, line)
	}
	if !slices.Contains(ids, testChromeID) {
		t.Errorf("EXTENSION_ID.txt lists %q, without the development ID %s", ids, testChromeID)
	}
	joined := strings.Join(ids, ",")
	if got := appendIDs(nil, joined); !slices.Equal(got, ids) {
		t.Errorf("the joined list reads back as %q, want %q (a repeated ID?)", got, ids)
	}
	if err := checkIDs(regOptions{browsers: []string{"chrome"}, chromeIDs: appendIDs(nil, joined)}); err != nil {
		t.Error(err)
	}
}
