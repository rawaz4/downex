//go:build windows

package glue

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/host"
	"downex/ui/kit"
	"downex/ui/state/statetest"
)

// startWith starts a process whose session talks to d.
func startWith(t *testing.T, d host.Daemon) *Window {
	t.Helper()
	dir := t.TempDir()
	h, handed, err := Start(Config{
		Version: "0.0.0-test", LockID: testLock(), Dir: dir, LogDir: dir, Daemon: d,
		NoMemoryTrim: true, AllowForeground: func() {}, WebView2Dir: dir,
	})
	if err != nil || handed {
		t.Fatal(err)
	}
	t.Cleanup(h.Close)
	return h.newWindow(host.View{}, nil, nil)
}

type openAnswer struct{ err error }

// Opening a risky download asks today's question in the system's Yes/No box, owned by the
// window: the host's title and text with the file's name escaped (a right-to-left override
// shown as ⟨U+202E⟩), No the default; No opens nothing and reports nothing.
func TestRiskyFileAsksInTheMessageBox(t *testing.T) {
	desk := onTestDesktop(t)
	dir := t.TempDir()
	name := "invoice‮fdp.exe" // shows as "invoiceexe.pdf" where the override is honoured
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("MZ"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := statetest.NewDaemon(&downexv1.Download{
		Id: 5, State: downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED, Dir: dir, Filename: name,
	})
	p := startWith(t, d)
	tw := openWindow(t, kit.WindowOptions{})
	if _, err := p.Attach(tw.w, tw.kw, AttachOptions{}); err != nil {
		t.Fatal(err)
	}
	wantTitle, wantText := host.ConfirmText(host.RiskProgram, path, path)
	if !strings.Contains(wantText, "invoice⟨U+202E⟩fdp.exe") {
		t.Fatalf("the host's text does not escape the name: %q", wantText)
	}
	res := make(chan openAnswer, 1)
	go func() { res <- openAnswer{p.Session.Files().OpenFile(testCtx(t), 5)} }()
	var (
		shown       bool
		text, title string
		owner, def  uintptr
	)
	drv := onDesktop(t, desk, func() {
		dlg := waitDialog(wantTitle, 10*time.Second)
		if dlg == 0 {
			return
		}
		shown, title = true, windowText(dlg)
		text = windowText(call(procGetDlgItem, dlg, 0xFFFF))
		owner = call(procGetWindow, dlg, 4 /* GW_OWNER */)
		def = call(procSendMessage, dlg, 0x0400 /* DM_GETDEFID */, 0, 0) & 0xFFFF
		call(procPostMessage, dlg, 0x0111 /* WM_COMMAND */, 7 /* IDNO */, 0)
	})
	var got openAnswer
	go func() {
		got = <-res
		tw.close()
	}()
	tw.run(t, 30*time.Second)
	<-drv
	if !shown {
		t.Fatalf("no message box titled %q", wantTitle)
	}
	if title != wantTitle || text != wantText {
		t.Fatalf("box %q / %q, want %q / %q", title, text, wantTitle, wantText)
	}
	if owner != tw.hwnd() || def != 7 {
		t.Fatalf("owner %#x (window %#x), default button %d (want No)", owner, tw.hwnd(), def)
	}
	if got.err != nil {
		t.Fatalf("a declined open answered %v", got.err)
	}
}

type pickAnswer struct {
	path string
	err  error
}

// The platform's pickers are the window's, with today's titles: "Choose a folder" starting in
// the nearest existing folder of the one asked for; a caller's file picker title, or "Choose a
// file" when it is empty or longer than 200 bytes. A cancelled picker is "" without an error.
func TestPlatformPickersHaveTodaysTitles(t *testing.T) {
	desk := onTestDesktop(t)
	p := startWith(t, statetest.NewDaemon())
	tw := openWindow(t, kit.WindowOptions{})
	g, err := p.Attach(tw.w, tw.kw, AttachOptions{})
	if err != nil {
		t.Fatal(err)
	}
	plat := g.Platform()
	start := t.TempDir()
	long := strings.Repeat("x", 201)
	for _, c := range []struct {
		name, title string
		pick        func() (string, error)
	}{
		{"folder picker from a folder that does not exist", host.FolderPickerTitle, func() (string, error) {
			return plat.PickFolder(testCtx(t), filepath.Join(start, "not", "made"))
		}},
		{"file picker titled by its caller", "Choose the scanner", func() (string, error) {
			return plat.PickFile(testCtx(t), "Choose the scanner", start)
		}},
		{"file picker without a title", host.FilePickerTitle, func() (string, error) {
			return plat.PickFile(testCtx(t), "", start)
		}},
		{"file picker with a 201-byte title", host.FilePickerTitle, func() (string, error) {
			return plat.PickFile(testCtx(t), long, "")
		}},
	} {
		t.Logf("case %q", c.name)
		res := make(chan pickAnswer, 1)
		go func() {
			path, err := c.pick()
			res <- pickAnswer{path, err}
		}()
		var shown bool
		var owner uintptr
		drv := onDesktop(t, desk, func() {
			dlg := waitDialog(c.title, 15*time.Second)
			if dlg == 0 {
				return
			}
			shown = true
			owner = call(procGetWindow, dlg, 4)
			time.Sleep(500 * time.Millisecond) // the dialog fills its view first
			call(procPostMessage, dlg, 0x0111, 2 /* IDCANCEL */, 0)
		})
		var got pickAnswer
		done := make(chan struct{})
		go func() {
			select {
			case got = <-res:
			case <-time.After(25 * time.Second):
				got.err = os.ErrDeadlineExceeded
			}
			close(done)
		}()
		pumpUntil(t, tw, done, 30*time.Second)
		<-drv
		if !shown {
			t.Fatalf("%s: no dialog titled %q", c.name, c.title)
		}
		if owner != tw.hwnd() {
			t.Errorf("%s: owner %#x, want the window %#x", c.name, owner, tw.hwnd())
		}
		if got.err != nil || got.path != "" {
			t.Fatalf("%s: a cancelled picker answered %q, %v", c.name, got.path, got.err)
		}
	}
	tw.close()
	tw.run(t, 10*time.Second)
}
