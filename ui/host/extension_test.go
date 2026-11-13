package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"downex/internal/client"
)

func TestExtensionFolderOfEachInstallation(t *testing.T) {
	root := t.TempDir()
	here := client.LayoutOf(runtime.GOOS, filepath.Join(root, client.UIName()))
	mac := client.LayoutOf("darwin", "/Applications/Downex.app/Contents/MacOS/Downex")
	for _, c := range []struct {
		l    client.Layout
		kind FolderKind
		want string
	}{
		{here, FolderExtensionChrome, filepath.Join(root, "extension", "chrome-mv3")},
		{here, FolderExtensionFirefox, filepath.Join(root, "extension", "firefox-mv3")},
		{mac, FolderExtensionChrome, "/Applications/Downex.app/Contents/Resources/extension/chrome-mv3"},
		{mac, FolderExtensionFirefox, "/Applications/Downex.app/Contents/Resources/extension/firefox-mv3"},
	} {
		if got, ok := ExtensionFolderOf(c.l, c.kind); !ok || got != c.want {
			t.Errorf("ExtensionFolderOf(%s, %s) = %q %v, want %q", c.l.Exe, c.kind, got, ok, c.want)
		}
	}
	for _, kind := range []FolderKind{FolderData, FolderDownloads, "extension", ""} {
		if got, ok := ExtensionFolderOf(here, kind); ok {
			t.Errorf("ExtensionFolderOf(%q) = %q", kind, got)
		}
	}
	if got, want := RelayOf(mac, "darwin"), "/Applications/Downex.app/Contents/Helpers/downex-nmhost"; got != want {
		t.Errorf("RelayOf(Downex.app) = %q, want %q", got, want)
	}
	if got, want := RelayOf(client.LayoutOf("windows", `C:\Downex\downex-ui.exe`), "windows"), filepath.Join(`C:\Downex`, "downex-nmhost.exe"); runtime.GOOS == "windows" && got != want {
		t.Errorf("RelayOf(Windows) = %q, want %q", got, want)
	}
}

// TestExtensionFolderRefusesAppTranslocation: Downex.app opened where the Finder did not
// move it runs from a temporary copy (App Translocation); its extension folder goes away
// when Downex quits, so ExtensionFolder refuses it and says what to do (the Integration
// page then turns Copy path and Open folder off).
func TestExtensionFolderRefusesAppTranslocation(t *testing.T) {
	const want = "failed_precondition: Downex runs from a temporary copy that macOS made: move Downex.app to Applications with the Finder and open it again"
	moved := client.LayoutOf("darwin", "/Applications/Downex.app/Contents/MacOS/Downex")
	translocated := client.LayoutOf("darwin", "/private/var/folders/x1/abc123/T/AppTranslocation/0F3C9A2E-1B2C-4D5E-8F90-1234567890AB/d/Downex.app/Contents/MacOS/Downex")
	if !translocated.Translocated() || moved.Translocated() {
		t.Fatalf("Translocated: %v %v", translocated.Translocated(), moved.Translocated())
	}
	for _, kind := range []FolderKind{FolderExtensionChrome, FolderExtensionFirefox} {
		if dir, err := extensionFolderIn(translocated, kind); errText(err) != want || dir != "" {
			t.Errorf("extensionFolderIn(translocated, %s) = %q, %v; want %q", kind, dir, err, want)
		}
		wantDir, _ := ExtensionFolderOf(moved, kind)
		if dir, err := extensionFolderIn(moved, kind); err != nil || dir != wantDir {
			t.Errorf("extensionFolderIn(Applications, %s) = %q, %v; want %q", kind, dir, err, wantDir)
		}
	}
	if runtime.GOOS != "darwin" {
		return // FindLayout reads the running system's layout rules
	}
	d := newFakeDaemon()
	f, sh := newTestFiles(t, d, nil)
	f.Executable = func() (string, error) { return translocated.Exe, nil }
	if dir, err := f.ExtensionFolder(FolderExtensionChrome); errText(err) != want {
		t.Errorf("ExtensionFolder translocated = %q, %v; want %q", dir, err, want)
	}
	if err := f.OpenAppFolder(callCtx(t), FolderExtensionChrome); errText(err) != want || len(sh.shown) != 0 {
		t.Errorf("OpenAppFolder translocated = %v, shown %v", err, sh.shown)
	}
}

// installation is a temporary installation: the window's program (never run) in root.
func installation(t *testing.T, f *Files) string {
	t.Helper()
	root := t.TempDir()
	f.Executable = func() (string, error) { return filepath.Join(root, client.UIName()), nil }
	return root
}

func TestOpenAppFolderShowsTheExtension(t *testing.T) {
	d := newFakeDaemon()
	f, sh := newTestFiles(t, d, nil)
	root := installation(t, f)
	chrome := filepath.Join(root, "extension", "chrome-mv3")
	if err := os.MkdirAll(chrome, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := f.ExtensionFolder(FolderExtensionChrome); err != nil || got != chrome {
		t.Fatalf("ExtensionFolder = %q %v, want %q", got, err, chrome)
	}
	if err := f.OpenAppFolder(callCtx(t), FolderExtensionChrome); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sh.shown, []string{"folder:" + chrome}) {
		t.Errorf("shown %v", sh.shown)
	}
	// The Firefox folder is missing from this installation: nothing opens.
	if err := f.OpenAppFolder(callCtx(t), FolderExtensionFirefox); errText(err) != "failed_precondition: the extension-firefox folder does not exist yet" {
		t.Errorf("OpenAppFolder(firefox) = %v", err)
	}
	if len(sh.shown) != 1 {
		t.Errorf("shown %v", sh.shown)
	}
	// ExtensionFolder names only the extension's folders.
	if _, err := f.ExtensionFolder(FolderData); errText(err) != `invalid_argument: kind must be "extension-chrome" or "extension-firefox"` {
		t.Errorf("ExtensionFolder(data) = %v", err)
	}
	// A program that cannot be located has no installation to show.
	f.Executable = func() (string, error) { return "", errors.New("no program") }
	if _, err := f.ExtensionFolder(FolderExtensionChrome); !hasPrefix(err, "failed_precondition: cannot locate this program") {
		t.Errorf("ExtensionFolder without a program = %v", err)
	}
}

// fakeRelay records the relay's runs and answers out and err.
type fakeRelay struct {
	runs [][]string
	out  string
	err  error
}

func (r *fakeRelay) run(ctx context.Context, exe string, args ...string) ([]byte, error) {
	r.runs = append(r.runs, append([]string{exe}, args...))
	return []byte(r.out), r.err
}

func TestRegisterBrowsersRunsTheInstalledRelay(t *testing.T) {
	f, _ := newTestFiles(t, newFakeDaemon(), nil)
	root := installation(t, f)
	relay := &fakeRelay{}
	f.RunRelay = relay.run

	// No relay next to the program: nothing runs.
	if err := f.RegisterBrowsers(callCtx(t)); errText(err) != "failed_precondition: downex-nmhost is not installed next to Downex" || len(relay.runs) != 0 {
		t.Fatalf("RegisterBrowsers without a relay = %v (runs %v)", err, relay.runs)
	}

	exe := RelayOf(client.LayoutOf(runtime.GOOS, filepath.Join(root, client.UIName())), runtime.GOOS)
	writeFile(t, exe)
	if err := f.RegisterBrowsers(callCtx(t)); err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{exe, "register"}}; fmt.Sprint(relay.runs) != fmt.Sprint(want) {
		t.Errorf("runs %v, want %v", relay.runs, want)
	}
	// The macOS window's start keeps what is registered.
	relay.runs = nil
	if err := f.KeepBrowsersRegistered(callCtx(t)); err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{exe, "register", "--keep"}}; fmt.Sprint(relay.runs) != fmt.Sprint(want) {
		t.Errorf("runs %v, want %v", relay.runs, want)
	}

	// A failed register reports the relay's own last message.
	relay.out = "registered Chrome\ndownex-nmhost register: write the Edge manifest: access is denied\n"
	relay.err = errors.New("exit status 1")
	if err := f.RegisterBrowsers(callCtx(t)); errText(err) != "internal: write the Edge manifest: access is denied" {
		t.Errorf("failed register = %v", err)
	}
	relay.out = ""
	if err := f.RegisterBrowsers(callCtx(t)); errText(err) != "internal: downex-nmhost could not register Downex" {
		t.Errorf("silent failed register = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := f.RegisterBrowsers(ctx); errText(err) != "deadline_exceeded: downex-nmhost did not finish in time" {
		t.Errorf("register past its time = %v", err)
	}
}

func TestRelayMessage(t *testing.T) {
	for in, want := range map[string]string{
		"":                                       "",
		"\r\n  \r\n":                             "",
		"downex-nmhost: locate downex-nmhost: x": "locate downex-nmhost: x",
		"a\r\ndownex-nmhost register: b\r\n\r\n": "b",
		"plain words":                            "plain words",
	} {
		if got := RelayMessage([]byte(in)); got != want {
			t.Errorf("RelayMessage(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestRelayHelperProcess is the relay of TestRunRelay (a run of this test binary).
func TestRelayHelperProcess(t *testing.T) {
	if os.Getenv("DOWNEX_TEST_RELAY_HELPER") != "1" {
		t.Skip("run by TestRunRelay")
	}
	fmt.Fprintln(os.Stdout, "registered Chrome")
	fmt.Fprintln(os.Stderr, "downex-nmhost register: "+strings.Repeat("x", 3))
	os.Exit(1)
}

// TestRunRelay: the product's runner starts the program itself (no shell) and keeps what it
// wrote on stdout and stderr; a non-zero exit is an error.
func TestRunRelay(t *testing.T) {
	t.Setenv("DOWNEX_TEST_RELAY_HELPER", "1")
	out, err := runRelay(callCtx(t), os.Args[0], "-test.run=^TestRelayHelperProcess$")
	if err == nil {
		t.Fatalf("no error (output %q)", out)
	}
	if got := RelayMessage(out); got != "xxx" || !strings.Contains(string(out), "registered Chrome") {
		t.Errorf("output %q", out)
	}
	var c capped
	_, _ = c.Write(make([]byte, relayOutputMax+10))
	if n, _ := c.Write([]byte("more")); n != 4 || c.Len() != relayOutputMax {
		t.Errorf("capped keeps %d bytes", c.Len())
	}
}
