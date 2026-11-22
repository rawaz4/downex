package main

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"downex/internal/client"
	"downex/internal/daemon"
	"downex/internal/ipc"
)

// TestProgressWindowHook checks daemon.Config.OpenProgress: absent without downex-ui
// next to downexd or with another --endpoint (the window could not reach the daemon),
// else "downex-ui --progress <id>" through platform.StartApp.
func TestProgressWindowHook(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	if d := desktopHooks(options{db: "x.db"}, log); d.openProgress != nil {
		t.Fatal("a progress window without downex-ui next to downexd")
	}

	l, err := installLayout()
	if err != nil {
		t.Skip("cannot locate the test executable:", err)
	}
	ui := l.Window
	if _, err := os.Stat(ui); err == nil {
		t.Skipf("%s already exists", ui)
	}
	if err := os.WriteFile(ui, []byte("stand-in"), 0o600); err != nil {
		t.Skip("cannot create a stand-in window next to the test executable:", err)
	}
	t.Cleanup(func() { _ = os.Remove(ui) })

	var started [][]string
	prev := startApp
	t.Cleanup(func() { startApp = prev })
	startApp = func(exe string, args ...string) error {
		started = append(started, append([]string{filepath.Base(exe)}, args...))
		return nil
	}

	if d := desktopHooks(options{endpoint: `\.\pipe\other`}, log); d.openProgress != nil {
		t.Error("a progress window for a daemon on another --endpoint")
	}
	d := desktopHooks(options{db: "x.db"}, log)
	if d.openProgress == nil || d.openConfirmation == nil {
		t.Fatalf("with downex-ui installed: progress %v, confirmation %v", d.openProgress != nil, d.openConfirmation != nil)
	}
	if err := d.openProgress(42); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{client.UIName(), "--progress", "42"}}
	if !slices.EqualFunc(started, want, slices.Equal) {
		t.Errorf("started %q, want %q", started, want)
	}

	// A browser add's window comes above the browser ("--raise"): its progress window (as the
	// download starts: "--started"), or its standalone Confirm download window; never the
	// main window.
	started = nil
	for _, w := range []daemon.AddWindow{daemon.AddProgress, daemon.AddProgressStarted, daemon.AddConfirm} {
		if err := d.showBrowserAdd(9, w); err != nil {
			t.Fatal(err)
		}
	}
	want = [][]string{{client.UIName(), "--progress", "9", "--raise"}, {client.UIName(), "--progress", "9", "--raise", "--started"},
		{client.UIName(), "--confirm", "9", "--raise"}}
	if !slices.EqualFunc(started, want, slices.Equal) {
		t.Errorf("started %q, want %q", started, want)
	}
}

// TestBundleHooks checks the hooks inside the macOS app bundle (docs/MACOS-SPEC.md §1–§2),
// with a layout like client.LayoutOf's for Downex.app: confirmations open the window,
// progress windows the progress app, and the sign-in entry starts the login item.
func TestBundleHooks(t *testing.T) {
	root := t.TempDir()
	app := filepath.Join(root, "Downex.app")
	contents := filepath.Join(app, "Contents")
	l := client.Layout{
		Exe:      filepath.Join(contents, "Helpers", "downexd"),
		Bundle:   app,
		Dir:      filepath.Join(contents, "Helpers"),
		Window:   filepath.Join(contents, "MacOS", "Downex"),
		Progress: filepath.Join(contents, "Helpers", "Downex Progress.app", "Contents", "MacOS", "Downex"),
		Agent:    filepath.Join(contents, "Library", "LoginItems", "Downex Agent.app", "Contents", "MacOS", "downexd-agent"),
	}
	prevLayout, prevStart, prevLogin := installLayout, startApp, setLaunchAtLogin
	t.Cleanup(func() { installLayout, startApp, setLaunchAtLogin = prevLayout, prevStart, prevLogin })
	installLayout = func() (client.Layout, error) { return l, nil }
	var started [][]string
	startApp = func(exe string, args ...string) error {
		started = append(started, append([]string{exe}, args...))
		return nil
	}
	var login []string
	setLaunchAtLogin = func(enabled bool, exe string, args ...string) error {
		login = append([]string{exe}, args...)
		return nil
	}
	create := func(p string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("stand-in"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	log := slog.New(slog.DiscardHandler)
	t.Setenv(ipc.EnvEndpoint, "")

	// Nothing installed but the daemon: no windows; the sign-in entry starts downexd.
	create(l.Exe)
	d := desktopHooks(options{}, log)
	if d.openConfirmation != nil || d.openProgress != nil || d.launchAtLogin == nil {
		t.Fatalf("daemon only: %+v", d)
	}
	if err := d.launchAtLogin(true); err != nil || !slices.Equal(login, []string{l.Exe, "--background"}) {
		t.Errorf("sign-in entry without the login item: %q %v", login, err)
	}
	if err := openUI(); err == nil || !strings.Contains(err.Error(), "not installed in "+app) {
		t.Errorf("openUI without the window: %v", err)
	}

	// The window without the progress app: confirmations only.
	create(l.Window)
	if d := desktopHooks(options{}, log); d.openConfirmation == nil || d.openProgress != nil {
		t.Fatalf("window only: confirmation %v, progress %v", d.openConfirmation != nil, d.openProgress != nil)
	}

	create(l.Progress)
	create(l.Agent)
	d = desktopHooks(options{}, log)
	if d.openConfirmation == nil || d.openProgress == nil {
		t.Fatalf("whole bundle: confirmation %v, progress %v", d.openConfirmation != nil, d.openProgress != nil)
	}
	if err := d.openConfirmation(7); err != nil {
		t.Fatal(err)
	}
	if err := d.openProgress(42); err != nil {
		t.Fatal(err)
	}
	if err := openUI(); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{l.Window, "--confirm", "7"}, {l.Progress, "--progress", "42"}, {l.Window}}
	if !slices.EqualFunc(started, want, slices.Equal) {
		t.Errorf("started %q, want %q", started, want)
	}
	if err := d.launchAtLogin(true); err != nil || !slices.Equal(login, []string{l.Agent, "--background"}) {
		t.Errorf("sign-in entry = %q %v, want the login item", login, err)
	}
}

// TestTranslocatedLaunchAtLogin: a daemon running from the temporary copy that macOS makes
// of a quarantined Downex.app (App Translocation) refuses to write the login item, which
// would name that copy, and may still remove it.
func TestTranslocatedLaunchAtLogin(t *testing.T) {
	const app = "/private/var/folders/x1/abc/T/AppTranslocation/0F1E2D3C-4B5A-6978-8796-A5B4C3D2E1F0/d/Downex.app"
	l := client.LayoutOf("darwin", app+"/Contents/Helpers/downexd")
	if !l.Translocated() {
		t.Fatalf("layout %+v is not translocated", l)
	}
	prevLayout, prevLogin := installLayout, setLaunchAtLogin
	t.Cleanup(func() { installLayout, setLaunchAtLogin = prevLayout, prevLogin })
	installLayout = func() (client.Layout, error) { return l, nil }
	var calls []string
	setLaunchAtLogin = func(enabled bool, exe string, args ...string) error {
		calls = append(calls, strconv.FormatBool(enabled)+" "+exe)
		return nil
	}
	t.Setenv(ipc.EnvEndpoint, "")
	d := desktopHooks(options{}, slog.New(slog.DiscardHandler))
	if d.launchAtLogin == nil {
		t.Fatal("no launch-at-login hook")
	}
	if err := d.launchAtLogin(true); !errors.Is(err, errTranslocated) || len(calls) != 0 {
		t.Errorf("enable from a translocated copy: %v, calls %q", err, calls)
	}
	if err := d.launchAtLogin(false); err != nil || !slices.Equal(calls, []string{"false " + l.Exe}) {
		t.Errorf("disable from a translocated copy: %v, calls %q", err, calls)
	}
}
