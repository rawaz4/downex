//go:build windows

package glue

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"downex/ui/host"
	"downex/ui/kit"
	"downex/ui/views"
	"downex/ui/win"
)

// The hand-off tests run a host in a helper process of their own (part "host", on the
// test's desktop, under a test lock) and start the later starts as other helper processes
// (part "second": this package's Start; "wails-sender": today's exe) or as Start in the test
// itself. The host's helper reports what its windows do as JSON lines (hostEvent).

// argsJSON is a command line for $GLUE_ARGS.
func argsJSON(t *testing.T, args ...string) string {
	b, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// hostEvent is a line of the "host" helper.
type hostEvent struct {
	Ev       string   `json:"ev"` // opened, closed, confirm, saved, handed, exit
	Progress int64    `json:"progress,omitempty"`
	HWND     uint64   `json:"hwnd,omitempty"`
	IDs      []int64  `json:"ids,omitempty"`
	Args     []string `json:"args,omitempty"`
	Legacy   bool     `json:"legacy,omitempty"`
	Err      string   `json:"err,omitempty"`
}

// hostChild is a running "host" helper.
type hostChild struct {
	*child
	t *testing.T
}

func startHost(t *testing.T, desk, lock, dir string, args ...string) hostChild {
	t.Helper()
	ch := startChild(t, "host", t.TempDir(), map[string]string{
		envDesktop: desk, envLock: lock, envArgs: argsJSON(t, args...), envDir: dir,
	})
	return hostChild{child: ch, t: t}
}

// next is the helper's next event.
func (h hostChild) next() hostEvent {
	h.t.Helper()
	var e hostEvent
	l := h.line(h.t, 20*time.Second)
	if err := json.Unmarshal([]byte(l), &e); err != nil {
		h.t.Fatalf("host helper said %q", l)
	}
	return e
}

// expect is the helper's next event, which must be ev (of download progress).
func (h hostChild) expect(ev string, progress int64) hostEvent {
	h.t.Helper()
	e := h.next()
	if e.Ev != ev || e.Progress != progress {
		h.t.Fatalf("host helper: %+v, want %s of %d", e, ev, progress)
	}
	return e
}

// quiet checks that the helper says nothing for d.
func (h hostChild) quiet(d time.Duration) {
	h.t.Helper()
	select {
	case l, ok := <-h.lines:
		if ok {
			h.t.Fatalf("host helper said %q, want nothing", l)
		}
	case <-time.After(d):
	}
}

// later runs a later start of kind part ("second" or "wails-sender") with args and waits
// for it to exit 0.
func later(t *testing.T, desk, lock, dir, part string, args ...string) {
	t.Helper()
	ch := startChild(t, part, t.TempDir(), map[string]string{
		envDesktop: desk, envLock: lock, envArgs: argsJSON(t, args...), envDir: dir,
	})
	if code := ch.wait(t, 20*time.Second); code != 0 {
		t.Fatalf("the later start %s %q exited %d, want 0 (handed over)", part, args, code)
	}
	if part == "second" {
		var r helperResult
		if err := json.Unmarshal([]byte(ch.line(t, time.Second)), &r); err != nil {
			t.Fatal(err)
		}
		if !r.HandedOver || !r.Allowed || r.Err != "" {
			t.Fatalf("the native later start: %+v (want handed over, AllowSetForegroundWindow first)", r)
		}
	}
}

const (
	wmClose      = 0x0010
	wmKeyDown    = 0x0100
	wmSysCommand = 0x0112
	wmEndSession = 0x0016
	scClose      = 0xF060
	swMinimize   = 7 // SW_SHOWMINNOACTIVE
)

// One process hosts every window. The first start, whatever it asks for, is the host; a
// later start of any kind (this exe's, or today's Wails exe) hands its arguments to it and
// exits 0: no arguments shows the main window (brought back from the taskbar, or made when
// only progress windows are open), "--progress <id>" that download's window (one per
// download: a second request brings it back), "--confirm <id>" the main window's
// confirmation. Closing the main window leaves the progress windows open, and the process
// ends cleanly with its last window.
func TestOneHostHasEveryWindow(t *testing.T) {
	for _, part := range []string{"second", "wails-sender"} {
		t.Run(part, func(t *testing.T) {
			desk := onTestDesktop(t)
			dir, lock := t.TempDir(), testLock()
			h := startHost(t, desk, lock, dir, "--progress", "42") // a progress window first: only it opens
			p42 := h.expect("opened", 42)

			later(t, desk, lock, dir, part, "--confirm", "6", "--confirm=7") // the main window, with the requests
			m := h.expect("opened", 0)
			if e := h.next(); e.Ev != "confirm" || !slices.Equal(e.IDs, []int64{6, 7}) {
				t.Fatalf("confirmations %+v, want [6 7]", e)
			}
			// A download whose window is not open: an older build's start would open a
			// process of its own under its lock (no host has it), so only this exe's asks the host.
			later(t, desk, lock, dir, "second", "--progress", "43")
			p43 := h.expect("opened", 43)

			call(procShowWindow, uintptr(p42.HWND), swMinimize)
			later(t, desk, lock, dir, part, "--progress=42") // open already: back from the taskbar
			waitFor(t, "progress 42 restored", func() bool { return !iconic(uintptr(p42.HWND)) })
			call(procShowWindow, uintptr(m.HWND), swMinimize)
			later(t, desk, lock, dir, part, "--confirm", "9")
			if e := h.next(); e.Ev != "confirm" || !slices.Equal(e.IDs, []int64{9}) {
				t.Fatalf("confirmations %+v, want [9]", e)
			}
			waitFor(t, "the main window restored", func() bool { return !iconic(uintptr(m.HWND)) })
			later(t, desk, lock, dir, part) // the tray's Open: nothing new
			h.quiet(300 * time.Millisecond)

			// The user closes the main window: the progress windows stay, and a later start
			// makes the main window again.
			call(procSendMessage, uintptr(m.HWND), wmSysCommand, scClose, 0)
			h.expect("saved", 0)
			h.expect("closed", 0)
			later(t, desk, lock, dir, part)
			m = h.expect("opened", 0)
			call(procSendMessage, uintptr(m.HWND), wmSysCommand, scClose, 0)
			h.expect("saved", 0)
			h.expect("closed", 0)
			call(procSendMessage, uintptr(p42.HWND), wmSysCommand, scClose, 0)
			h.expect("closed", 42)
			call(procSendMessage, uintptr(p43.HWND), wmSysCommand, scClose, 0)
			h.expect("closed", 43)
			h.expect("exit", 0)
			if code := h.wait(t, 10*time.Second); code != 0 {
				t.Fatalf("the host exited %d", code)
			}
			// Its locks are gone with it: the next start is the host again.
			again := startHost(t, desk, lock, dir)
			m = again.expect("opened", 0)
			call(procSendMessage, uintptr(m.HWND), wmSysCommand, scClose, 0)
			again.expect("saved", 0)
			again.expect("closed", 0)
			again.expect("exit", 0)
		})
	}
}

// A start of the native exe in the test's process (Start) hands over to the helper's host:
// its arguments and working directory, and it lets the host come to the front first.
func TestStartHandsItsArgumentsToTheHost(t *testing.T) {
	desk := onTestDesktop(t)
	dir, lock := t.TempDir(), testLock()
	h := startHost(t, desk, lock, dir)
	m := h.expect("opened", 0)
	var allowed bool
	c := testConfig(lock, dir, "--progress", "8", `C:\Users\x\“quoted” dir`)
	c.AllowForeground = func() { allowed = true }
	c.Getwd = func() (string, error) { return `D:\work dir`, nil }
	got, handed, err := Start(c)
	if got != nil || !handed || err != nil || !allowed {
		t.Fatalf("Start = %v, %v, %v (allowed %v); want a hand-off after AllowSetForegroundWindow", got, handed, err, allowed)
	}
	e := h.expect("opened", 8)
	if !slices.Equal(e.Args, []string{"--progress", "8", `C:\Users\x\“quoted” dir`}) {
		t.Fatalf("the window was asked with %q", e.Args)
	}
	call(procPostMessage, uintptr(m.HWND), wmClose, 0, 0) // another program's close: every window
	closeAll(t, h, 0, 8)
}

// closeAll reads the events of the host closing every window (ids, the main window as 0)
// and ending.
func closeAll(t *testing.T, h hostChild, ids ...int64) {
	t.Helper()
	left := slices.Clone(ids)
	for len(left) > 0 {
		e := h.next()
		if e.Ev == "saved" {
			continue
		}
		i := slices.Index(left, e.Progress)
		if e.Ev != "closed" || i < 0 {
			t.Fatalf("host helper: %+v while closing %v", e, left)
		}
		left = slices.Delete(left, i, i+1)
	}
	h.expect("exit", 0)
	if code := h.wait(t, 10*time.Second); code != 0 {
		t.Fatalf("the host exited %d", code)
	}
}

// An installer stops the window with CloseMainWindow, a WM_CLOSE to one window of the
// process (then ends it after 5 s); the hand-off window answers WM_CLOSE too; the end of
// the user's session sends WM_ENDSESSION to each window. Each closes every window (the main
// window stores its size) and the process ends at once. The user's own close (Alt+F4, the
// system menu, the taskbar: SC_CLOSE) closes only that window.
func TestTheHostEndsOnAForeignClose(t *testing.T) {
	for _, c := range []struct {
		name string
		to   string // main, progress, handoff
		msg  uintptr
		wp   uintptr
	}{
		{"CloseMainWindow on the main window", "main", wmClose, 0},
		{"CloseMainWindow on a progress window", "progress", wmClose, 0},
		{"WM_CLOSE to the hand-off window", "handoff", wmClose, 0},
		{"the end of the session", "progress", wmEndSession, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			desk := onTestDesktop(t)
			dir, lock := t.TempDir(), testLock()
			h := startHost(t, desk, lock, dir)
			m := h.expect("opened", 0)
			later(t, desk, lock, dir, "second", "--progress", "5")
			p := h.expect("opened", 5)
			to := uintptr(m.HWND)
			switch c.to {
			case "progress":
				to = uintptr(p.HWND)
			case "handoff":
				n := HandoffNames(lock, host.View{})
				to = call(procFindWindowX, hwndMessage, 0, utf16(n.Class), utf16(n.Window))
				if to == 0 {
					t.Fatal("no hand-off window")
				}
			}
			start := time.Now()
			if c.msg == wmEndSession {
				call(procSendMessage, to, 0x0011 /* WM_QUERYENDSESSION */, 0, 0)
				call(procSendMessage, to, c.msg, c.wp, 0x80000000 /* ENDSESSION_LOGOFF */)
			} else {
				call(procPostMessage, to, c.msg, c.wp, 0)
			}
			var saved bool
			left := []int64{0, 5}
			for len(left) > 0 {
				e := h.next()
				if e.Ev == "saved" {
					saved = true
					continue
				}
				if i := slices.Index(left, e.Progress); e.Ev == "closed" && i >= 0 {
					left = slices.Delete(left, i, i+1)
					continue
				}
				t.Fatalf("host helper: %+v", e)
			}
			if !saved {
				t.Fatal("the main window did not store its size")
			}
			if c.msg != wmEndSession { // at the session's end the process may be ended any time after
				h.expect("exit", 0)
			}
			if code := h.wait(t, 10*time.Second); code != 0 {
				t.Fatalf("the host exited %d", code)
			}
			if took := time.Since(start); took > 5*time.Second {
				t.Fatalf("the host took %v to end, past the installer's 5 s", took)
			}
		})
	}
}

// A failure in one window (here a panic in its procedure, which closes it) leaves the
// host's other windows open and the process running.
func TestAPanicClosesOnlyItsWindow(t *testing.T) {
	desk := onTestDesktop(t)
	dir, lock := t.TempDir(), testLock()
	h := startHost(t, desk, lock, dir)
	m := h.expect("opened", 0)
	later(t, desk, lock, dir, "second", "--progress", "3")
	p := h.expect("opened", 3)
	call(procPostMessage, uintptr(p.HWND), wmKeyDown, 'P', 0) // the helper's window panics on a key
	h.expect("closed", 3)
	h.quiet(300 * time.Millisecond)
	if !visible(uintptr(m.HWND)) {
		t.Fatal("the main window went with the failed one")
	}
	later(t, desk, lock, dir, "second", "--progress", "3") // the download's window opens again
	h.expect("opened", 3)
	call(procPostMessage, uintptr(m.HWND), wmClose, 0, 0)
	closeAll(t, h, 0, 3)
}

// During an upgrade today's window (the Wails stand-in, which answers every WM_COPYDATA 0
// and reads only dwData 1542) may hold the main lock. A new start finds no host there and
// does what today's would, without changing anything in today's window: "--progress <id>"
// becomes a progress window of its own under that download's lock (a legacy host: that
// window only), and any other start hands its arguments to today's main window.
func TestAnOlderMainWindowGetsTodaysHandoff(t *testing.T) {
	onOwnThread()
	lock := testLock()
	rec := startChild(t, "wails-receiver", t.TempDir(), map[string]string{envLock: lock, envWait: "1500"})
	if l := rec.line(t, 10*time.Second); l != "ready" {
		t.Fatalf("stand-in said %q", l)
	}
	dir := t.TempDir()
	h, handed, err := Start(testConfig(lock, dir, "--progress", "11"))
	if err != nil || handed || h == nil || !h.Legacy {
		t.Fatalf("Start(--progress 11) = %+v, %v, %v; want a legacy host", h, handed, err)
	}
	pn := HandoffNames(lock, host.View{Progress: 11})
	if call(procFindWindowX, hwndMessage, 0, utf16(pn.Class), utf16(pn.Window)) == 0 {
		t.Fatal("the legacy host does not listen under its download's lock")
	}
	if again, handed, err := Start(testConfig(lock, dir, "--progress", "11")); again != nil || !handed || err != nil {
		t.Fatalf("a second --progress 11: %v %v %v; want a hand-off to the first", again, handed, err)
	}
	h.Close()
	if l := rec.line(t, 10*time.Second); l != "none" {
		t.Fatalf("today's window received %q from a progress start, want nothing", l)
	}
	rec.wait(t, 10*time.Second)

	rec = startChild(t, "wails-receiver", t.TempDir(), map[string]string{envLock: lock})
	if l := rec.line(t, 10*time.Second); l != "ready" {
		t.Fatalf("stand-in said %q", l)
	}
	c := testConfig(lock, dir, "--confirm", "12")
	c.Getwd = func() (string, error) { return `D:\work dir`, nil }
	if h, handed, err := Start(c); h != nil || !handed || err != nil {
		t.Fatalf("Start(--confirm 12) = %v, %v, %v; want a hand-off to today's window", h, handed, err)
	}
	var d secondInstanceData
	if err := json.Unmarshal([]byte(rec.line(t, 10*time.Second)), &d); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(d.Args, []string{"--confirm", "12"}) || d.WorkingDirectory != `D:\work dir` {
		t.Fatalf("today's window received %+v", d)
	}
	if code := rec.wait(t, 10*time.Second); code != 0 {
		t.Fatalf("stand-in exited %d", code)
	}
}

// An older build's progress window (today's per-download process, the stand-in under that
// download's lock) keeps its download: the host hands it the request instead of opening a
// second window, and opens the others. Its own progress windows keep their downloads'
// locks, so an older build's start of one finds it there.
func TestOlderProgressWindowsKeepTheirDownloads(t *testing.T) {
	desk := onTestDesktop(t)
	dir, lock := t.TempDir(), testLock()
	old := startChild(t, "wails-receiver", t.TempDir(), map[string]string{
		envDesktop: desk, envLock: lock, envArgs: argsJSON(t, "--progress", "21"),
	})
	if l := old.line(t, 10*time.Second); l != "ready" {
		t.Fatalf("stand-in said %q", l)
	}
	h := startHost(t, desk, lock, dir)
	m := h.expect("opened", 0)
	later(t, desk, lock, dir, "second", "--progress", "21")
	var d secondInstanceData
	if err := json.Unmarshal([]byte(old.line(t, 10*time.Second)), &d); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(d.Args, []string{"--progress", "21"}) {
		t.Fatalf("the older progress window received %+v", d)
	}
	h.quiet(300 * time.Millisecond) // no window of the host's own for 21
	later(t, desk, lock, dir, "second", "--progress", "22")
	p := h.expect("opened", 22)
	call(procShowWindow, uintptr(p.HWND), swMinimize)
	later(t, desk, lock, dir, "wails-sender", "--progress", "22") // an older build's start, under 22's lock
	waitFor(t, "progress 22 restored", func() bool { return !iconic(uintptr(p.HWND)) })
	h.quiet(200 * time.Millisecond)
	call(procPostMessage, uintptr(m.HWND), wmClose, 0, 0)
	closeAll(t, h, 0, 22)
}

// A host whose last window has just closed answers HostClosing; a start then tries again
// and becomes the host once the old one has let the lock go (the request is never lost).
func TestAStartOutlivesAClosingHost(t *testing.T) {
	onOwnThread()
	lock := testLock()
	ch := startChild(t, "closing-host", t.TempDir(), map[string]string{envLock: lock, envWait: "400"})
	if l := ch.line(t, 10*time.Second); l != "ready" {
		t.Fatalf("closing host said %q", l)
	}
	start := time.Now()
	h, handed, err := Start(testConfig(lock, t.TempDir(), "--progress", "4"))
	if err != nil || handed || h == nil || h.Legacy {
		t.Fatalf("Start = %+v, %v, %v; want the new host", h, handed, err)
	}
	defer h.Close()
	if took := time.Since(start); took < 300*time.Millisecond {
		t.Fatalf("became the host after %v, while the old one still held the lock", took)
	}
	n, err := strconv.Atoi(ch.line(t, 10*time.Second))
	if err != nil || n < 1 {
		t.Fatalf("the closing host answered %d starts (%v), want at least 1", n, err)
	}
	if h.instance == nil || !h.instance.Listening() {
		t.Fatal("the new host does not listen")
	}
}

// The main lock's answers: a host's start is taken while the host runs and refused with
// HostClosing once its last window has closed; today's starts are always taken.
func TestTheHostsAnswers(t *testing.T) {
	h := &Host{}
	if r, take := h.answer(host.HostHandoffKind); r != host.HostClosing || take {
		t.Fatalf("closing host: %#x %v", r, take)
	}
	h.accepting = true
	if r, take := h.answer(host.HostHandoffKind); r != host.HostAnswer || !take {
		t.Fatalf("running host: %#x %v", r, take)
	}
	h.accepting = false
	if r, take := h.answer(host.HandoffDataKind); r != 1 || !take {
		t.Fatalf("today's start to a closing host: %#x %v", r, take)
	}
}

// Without a daemon of its own the host's windows share the host's client daemon, which makes
// the first contact with downexd (and starts it when it is not running, unless the process
// is elevated) on its first call: Start itself contacts nothing.
func TestTheSessionMakesTheFirstContact(t *testing.T) {
	onOwnThread()
	dir := t.TempDir()
	h, handed, err := Start(Config{LockID: testLock(), Dir: dir, LogDir: dir, NoMemoryTrim: true, AllowForeground: func() {}})
	if err != nil || handed {
		t.Fatal(err)
	}
	defer h.Close()
	w1, w2 := h.newWindow(host.View{}, nil, nil), h.newWindow(host.View{Progress: 2}, []string{"--progress", "2"}, nil)
	if _, ok := w1.Session.Daemon().(*host.ClientDaemon); !ok || w1.Session.Daemon() != w2.Session.Daemon() {
		t.Fatalf("the sessions' daemons are %T and %T, want the host's one client daemon", w1.Session.Daemon(), w2.Session.Daemon())
	}
	if w1.Session.ThemePath() != h.Files.Theme || w1.Session.View() != w1.View || w2.Session.View() != w2.View {
		t.Fatalf("session theme %q views %+v %+v", w1.Session.ThemePath(), w1.Session.View(), w2.Session.View())
	}
	h.Close()
	h.Close() // twice: nothing
	if len(h.windows) != 0 || w1.Session.Context().Err() == nil || w2.Session.Context().Err() == nil {
		t.Fatal("Close left a window's session open")
	}
	// ui.log (the preview's own name here) has the start, as JSON lines.
	b, err := os.ReadFile(filepath.Join(dir, PreviewLogName))
	if err != nil || !strings.Contains(string(b), `"msg":"window process started"`) || !strings.Contains(string(b), `"preview":true`) {
		t.Fatalf("log %q: %v", b, err)
	}
}

// waitFor waits up to 10 s for ok.
func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("%s: not within 10 s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// hostMain is the "host" helper: a host under $GLUE_LOCK whose windows are labels, reporting
// on out what they do. The progress windows panic on a key (TestAPanicClosesOnlyItsWindow).
func hostMain(lock, dir string, args []string) int {
	out := func(e hostEvent) {
		b, _ := json.Marshal(e)
		fmt.Println(string(b))
	}
	c := testConfig(lock, dir, args...)
	h, handed, err := Start(c)
	if handed {
		out(hostEvent{Ev: "handed"})
		return 0
	}
	if err != nil {
		out(hostEvent{Ev: "exit", Err: err.Error()})
		return 2
	}
	defer h.Close()
	err = h.Run(RunOptions{
		App: win.AppOptions{ClassName: "DownexGlueTestHost"},
		Open: func(a *win.App, w *Window) (*win.Window, error) {
			id := w.View.Progress
			main := !w.View.IsProgress()
			kw := kit.NewWindow(kit.NewLabel("host "+strconv.FormatInt(id, 10), views.FontBody, kit.TextOptions{}), kit.WindowOptions{
				Title: "Downex glue host test",
				OnClose: func() bool {
					if main {
						out(hostEvent{Ev: "saved"})
					}
					return false
				},
				OnKey: func(e kit.KeyEvent) bool {
					if !main && e.Action == kit.KeyDown {
						panic("a progress window's bug")
					}
					return false
				},
			})
			ww, err := a.NewWindow(kw, win.WindowOptions{
				Title: "Downex glue host test", Size: kit.Size{W: 360, H: 200}, Software: true, NoActivate: true,
				OnForeignClose: w.ForeignClose,
			})
			if err != nil {
				return nil, err
			}
			if _, err := w.Attach(ww, kw, AttachOptions{}); err != nil {
				return nil, err
			}
			w.Defer(func() { out(hostEvent{Ev: "closed", Progress: id}) })
			out(hostEvent{Ev: "opened", Progress: id, HWND: uint64(ww.HWND()), Args: w.Args})
			if main {
				w.Session.Confirmations().Ready(func(ids []int64) { out(hostEvent{Ev: "confirm", IDs: ids}) })
			}
			return ww, nil
		},
	})
	if err != nil {
		out(hostEvent{Ev: "exit", Err: err.Error()})
		return 3
	}
	out(hostEvent{Ev: "exit"})
	return 0
}

// closingHost is the "closing-host" helper: a host whose last window has closed, holding the
// main lock for $GLUE_WAIT ms and answering HostClosing; it prints how many it answered.
func closingHost(lock string, wait time.Duration) int {
	inst, running, err := win.ClaimInstance(win.InstanceNames(HandoffNames(lock, host.View{})), 0)
	if running.OK() || err != nil {
		return 4
	}
	n := 0
	if err := inst.Listen(win.ListenOptions{
		Kind: host.HandoffDataKind, AlsoKinds: []uintptr{host.HostHandoffKind}, Max: host.MaxHandoff,
		Answer: func(uintptr) (uintptr, bool) { n++; return host.HostClosing, false },
	}); err != nil {
		return 5
	}
	fmt.Println("ready")
	pump(wait)
	inst.Close()
	fmt.Println(n)
	return 0
}
