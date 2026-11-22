package main

import (
	"context"
	"errors"
	"log/slog"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"downex/internal/client"
)

// fakeRegistration is a registration of the layout of exe under goos, with files the only
// regular files there are, and a run that records its calls.
type fakeRegistration struct {
	registration
	mu    sync.Mutex
	calls [][]string
	ran   chan struct{}
}

func newFakeRegistration(goos, exe string, files ...string) *fakeRegistration {
	f := &fakeRegistration{ran: make(chan struct{}, 8)}
	f.registration = registration{
		goos:   goos,
		layout: func() (client.Layout, error) { return client.LayoutOf(goos, exe), nil },
		isFile: func(p string) bool { return slices.Contains(files, p) },
		run: func(_ context.Context, relay string, args ...string) ([]byte, error) {
			f.mu.Lock()
			f.calls = append(f.calls, append([]string{relay}, args...))
			f.mu.Unlock()
			f.ran <- struct{}{}
			return []byte("Plan:\n  1. write the chrome manifest\nDone: 1 change(s).\n"), nil
		},
	}
	return f
}

func TestBrowserRegistrationOnlyForAnInstalledLayout(t *testing.T) {
	const (
		winDir   = `C:\Users\u\AppData\Local\Programs\Downex`
		winRelay = winDir + `\downex-nmhost.exe`
		helpers  = "/Applications/Downex.app/Contents/Helpers"
		macRelay = helpers + "/downex-nmhost"
	)
	for _, tc := range []struct {
		name  string
		goos  string
		exe   string
		own   bool
		files []string
		relay string // "" = no registration
		why   string // part of the reason when there is none
	}{
		{"windows installation", "windows", winDir + `\downexd.exe`, true, []string{winRelay}, winRelay, ""},
		{"private daemon", "windows", winDir + `\downexd.exe`, false, []string{winRelay}, "", "private"},
		{"windows without the relay", "windows", winDir + `\downexd.exe`, true, nil, "", "not installed next to downexd"},
		{"windows development build", "windows", `D:\src\Downex\bin\downexd.exe`, true,
			[]string{`D:\src\Downex\bin\downex-nmhost.exe`}, "", "bin folder"},
		{"windows build in a BIN folder", "windows", `C:\Tools\BIN\downexd.exe`, true,
			[]string{`C:\Tools\BIN\downex-nmhost.exe`}, "", "bin folder"},
		{"windows build in a source checkout", "windows", `D:\src\Downex\dist\downexd.exe`, true,
			[]string{`D:\src\Downex\dist\downex-nmhost.exe`, `D:\src\Downex\go.mod`}, "", "source tree"},
		{"windows build in a worktree", "windows", `D:\src\Downex\.claude\worktrees\w1\out\downexd.exe`, true,
			[]string{`D:\src\Downex\.claude\worktrees\w1\out\downex-nmhost.exe`, `D:\src\Downex\.claude\worktrees\w1\.git`}, "", "source tree"},
		{"macOS installation", "darwin", helpers + "/downexd", true, []string{macRelay}, macRelay, ""},
		{"macOS login item", "darwin", "/Applications/Downex.app/Contents/Library/LoginItems/Downex Agent.app/Contents/MacOS/downexd-agent",
			true, []string{macRelay}, macRelay, ""},
		{"macOS translocated", "darwin", "/private/var/folders/xy/T/AppTranslocation/0A1B/d/Downex.app/Contents/Helpers/downexd", true,
			[]string{"/private/var/folders/xy/T/AppTranslocation/0A1B/d/Downex.app/Contents/Helpers/downex-nmhost"}, "", "App Translocation"},
		{"macOS outside a bundle", "darwin", "/Users/u/src/downex/bin/downexd", true,
			[]string{"/Users/u/src/downex/bin/downex-nmhost"}, "", "not inside Downex.app"},
		{"macOS bundle built in a checkout", "darwin", "/Users/u/src/downex/dist/Downex.app/Contents/Helpers/downexd", true,
			[]string{"/Users/u/src/downex/dist/Downex.app/Contents/Helpers/downex-nmhost", "/Users/u/src/downex/go.mod"}, "", "source tree"},
		{"linux", "linux", "/usr/bin/downexd", true, []string{"/usr/bin/downex-nmhost"}, "", "installation registers"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.goos == "windows" && runtime.GOOS != "windows" {
				t.Skip("Windows paths are split by package filepath only on Windows")
			}
			f := newFakeRegistration(tc.goos, tc.exe, tc.files...)
			relay, why := f.relay(tc.own)
			if relay != tc.relay || (tc.relay == "" && !strings.Contains(why, tc.why)) {
				t.Errorf("relay(%v) = %q, %q; want %q (reason with %q)", tc.own, relay, why, tc.relay, tc.why)
			}
		})
	}

	broken := newFakeRegistration("darwin", helpers+"/downexd", macRelay)
	broken.layout = func() (client.Layout, error) { return client.Layout{}, errors.New("no executable") }
	if relay, why := broken.relay(true); relay != "" || !strings.Contains(why, "cannot locate") {
		t.Errorf("no layout: %q, %q", relay, why)
	}
}

// logRecords receives each record a text handler writes (one Write per record).
type logRecords chan string

func (r logRecords) Write(p []byte) (int, error) {
	r <- string(p)
	return len(p), nil
}

// next is the next record, failing the test after a while without one.
func (r logRecords) next(t *testing.T) string {
	t.Helper()
	select {
	case rec := <-r:
		return rec
	case <-time.After(10 * time.Second):
		t.Fatal("nothing was logged")
		return ""
	}
}

func TestBrowserRegistrationRunsKeepInTheBackground(t *testing.T) {
	// macOS paths: client.LayoutOf splits them alike on every system.
	const relay = "/Applications/Downex.app/Contents/Helpers/downex-nmhost"
	const exe = "/Applications/Downex.app/Contents/Helpers/downexd"
	records := make(logRecords, 8)
	log := slog.New(slog.NewTextHandler(records, &slog.HandlerOptions{Level: slog.LevelDebug}))

	f := newFakeRegistration("darwin", exe, relay)
	f.delay = 0
	stop := f.start(context.Background(), log, true)
	if rec := records.next(t); !strings.Contains(rec, "registered with the browsers again") || !strings.Contains(rec, "Done: 1 change(s).") {
		t.Errorf("log: %s", rec)
	}
	stop()
	if want := [][]string{{relay, "register", "--keep"}}; !slices.EqualFunc(f.calls, want, slices.Equal) {
		t.Errorf("runs %q, want %q", f.calls, want)
	}

	// A failure is only logged, with the relay's last line.
	f.run = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("downex-nmhost register: read the manifest: access denied\n"), errors.New("exit status 1")
	}
	stop = f.start(context.Background(), log, true)
	if rec := records.next(t); !strings.Contains(rec, "cannot keep Downex registered") || !strings.Contains(rec, "access denied") {
		t.Errorf("failure log: %s", rec)
	}
	stop()

	// Not the user's own daemon: nothing runs.
	f = newFakeRegistration("darwin", exe, relay)
	f.delay = 0
	f.start(context.Background(), log, false)()
	if rec := records.next(t); !strings.Contains(rec, "private daemon") {
		t.Errorf("private daemon log: %s", rec)
	}
	// Stopped before its delay ended: nothing runs either, and nothing is logged.
	f.delay = time.Hour
	f.start(context.Background(), log, true)()
	if len(f.calls) != 0 || len(records) != 0 {
		t.Errorf("runs %q and %d log records, want none", f.calls, len(records))
	}
}
