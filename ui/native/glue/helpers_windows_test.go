//go:build windows

package glue

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"downex/ui/host"
	"downex/ui/kit"
	"downex/ui/state/statetest"
	"downex/ui/views"
	"downex/ui/win"
)

// The tests open their windows and system dialogs on a desktop of their own (win.Desktop):
// nothing reaches the user's screen, focus or keyboard. The child processes the hand-off
// tests start are this test binary again (TestMain runs their part, by $GLUE_HELPER), on that
// desktop too. No test talks to a daemon: the sessions get statetest's.

const (
	envHelper  = "GLUE_HELPER"  // the child's part: second, wails-sender, wails-receiver, theme-writer
	envDesktop = "GLUE_DESKTOP" // the desktop the child's thread goes to
	envLock    = "GLUE_LOCK"    // the lock id
	envArgs    = "GLUE_ARGS"    // the child's command line, JSON
	envDir     = "GLUE_DIR"     // the files' folder
	envWait    = "GLUE_WAIT"    // how long a stand-in waits, in ms
)

func TestMain(m *testing.M) {
	if h := os.Getenv(envHelper); h != "" {
		os.Exit(helperMain(h))
	}
	os.Exit(m.Run())
}

var seq atomic.Int32

// testLock is a lock id of this test alone.
func testLock() string {
	return fmt.Sprintf("downex-glue-test-%d-%d", os.Getpid(), seq.Add(1))
}

// onTestDesktop puts the test's thread, locked for the rest of the test, on a new desktop and
// returns its name. It runs before the thread makes any window.
func onTestDesktop(t *testing.T) string {
	t.Helper()
	runtime.LockOSThread()
	name := fmt.Sprintf("DownexGlueTest-%d-%d", os.Getpid(), seq.Add(1))
	d, err := win.NewDesktop(name)
	if err != nil {
		runtime.UnlockOSThread()
		t.Fatal(err)
	}
	restore, err := win.UseDesktop(name)
	if err != nil {
		d.Close()
		runtime.UnlockOSThread()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = restore() // fails while COM's windows are on the thread: it ends with the test's goroutine
		d.Close()
		// Never unlocked: the thread keeps COM's and OLE's hidden windows, so it can never
		// move to another test's desktop; Go ends it with the test's goroutine.
	})
	return name
}

// onOwnThread locks the test's goroutine to a thread that ends with it: the windows a test
// makes there (hand-off windows, COM's) never reach another test's thread.
func onOwnThread() { runtime.LockOSThread() }

// onDesktop runs f on a thread of its own on the desktop called name (a dialog's driver).
func onDesktop(t *testing.T, name string, f func()) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		runtime.LockOSThread() // never unlocked: the thread ends with the goroutine
		restore, err := win.UseDesktop(name)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = restore() }()
		f()
	}()
	return done
}

var (
	user32          = windows.NewLazySystemDLL("user32.dll")
	procIsIconic    = user32.NewProc("IsIconic")
	procShowWindow  = user32.NewProc("ShowWindow")
	procFindWindowW = user32.NewProc("FindWindowW")
	procFindWindowX = user32.NewProc("FindWindowExW")
	procGetDlgItem  = user32.NewProc("GetDlgItem")
	procGetWindow   = user32.NewProc("GetWindow")
	procSendMessage = user32.NewProc("SendMessageW")
	procPostMessage = user32.NewProc("PostMessageW")
	procIsVisible   = user32.NewProc("IsWindowVisible")
)

func call(p *windows.LazyProc, a ...uintptr) uintptr {
	r, _, _ := syscall.SyscallN(p.Addr(), a...)
	return r
}

func iconic(h uintptr) bool  { return call(procIsIconic, h) != 0 }
func visible(h uintptr) bool { return call(procIsVisible, h) != 0 }

func utf16(s string) uintptr {
	p, _ := windows.UTF16PtrFromString(s)
	return uintptr(unsafe.Pointer(p))
}

// waitDialog waits up to d for a top-level dialog titled title on the calling thread's
// desktop.
func waitDialog(title string, d time.Duration) uintptr {
	deadline := time.Now().Add(d)
	for {
		if h := call(procFindWindowX, 0, 0, utf16("#32770"), utf16(title)); h != 0 || !time.Now().Before(deadline) {
			return h
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func windowText(h uintptr) string {
	buf := make([]uint16, 8192)
	n := call(procSendMessage, h, 0x000D /* WM_GETTEXT */, uintptr(len(buf)), uintptr(unsafe.Pointer(&buf[0])))
	return windows.UTF16ToString(buf[:n])
}

// testWindow is a window of the test's app: a label in a win.Window.
type testWindow struct {
	a  *win.App
	w  *win.Window
	kw *kit.Window
}

func (tw testWindow) hwnd() uintptr { return uintptr(tw.w.HWND()) }

// openWindow opens a window on the test's thread; run runs its loop until it closes.
func openWindow(t *testing.T, o kit.WindowOptions) testWindow {
	t.Helper()
	a, err := win.NewApp(win.AppOptions{ClassName: "DownexGlueTest"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	o.Title = "Downex glue test"
	kw := kit.NewWindow(kit.NewLabel("glue", views.FontBody, kit.TextOptions{}), o)
	w, err := a.NewWindow(kw, win.WindowOptions{Title: o.Title, Size: kit.Size{W: 480, H: 320}, Software: true, NoActivate: true})
	if err != nil {
		t.Fatal(err)
	}
	return testWindow{a: a, w: w, kw: kw}
}

// run runs the window's loop until it closes, at most d; a timeout closes it and fails.
func (tw testWindow) run(t *testing.T, d time.Duration) {
	t.Helper()
	var timedOut atomic.Bool
	timer := time.AfterFunc(d, func() {
		timedOut.Store(true)
		_ = tw.kw.Post(tw.kw.Close)
	})
	defer timer.Stop()
	if err := tw.a.Run(); err != nil {
		t.Fatal(err)
	}
	if timedOut.Load() {
		t.Fatalf("the test's window was still open after %v", d)
	}
}

// pumpUntil runs the window's loop, the window staying open, until done is closed (at most d).
func pumpUntil(t *testing.T, tw testWindow, done <-chan struct{}, d time.Duration) {
	t.Helper()
	go func() {
		select {
		case <-done:
		case <-time.After(d):
		}
		_ = tw.kw.Post(tw.a.Quit)
	}()
	if err := tw.a.Run(); err != nil {
		t.Fatal(err)
	}
}

// close closes the window from any goroutine.
func (tw testWindow) close() { _ = tw.kw.Post(tw.kw.Close) }

// start starts a host for a test (its own lock, files and log in dir, the fake daemon) and
// returns the part of the window its arguments ask for, not drawn (the test attaches a
// window of its own); p.h.Close ends it.
func start(t *testing.T, lock, dir string, args ...string) (*Window, bool, error) {
	t.Helper()
	h, handed, err := Start(testConfig(lock, dir, args...))
	if h == nil {
		return nil, handed, err
	}
	return h.newWindow(host.ViewOf(args), args, nil), handed, err
}

// testConfig is a test host's: its own lock, files and log in dir, the fake daemon.
func testConfig(lock, dir string, args ...string) Config {
	return Config{
		Args: args, Version: "0.0.0-test", LockID: lock, Dir: dir, LogDir: dir,
		Daemon: statetest.NewDaemon(), NoMemoryTrim: true, AllowForeground: func() {},
		WebView2Dir: dir, // no WebView2 profile there: the prefs start empty
	}
}

// child is a helper process: this test binary with $GLUE_HELPER.
type child struct {
	cmd   *exec.Cmd
	lines chan string
	done  chan error
}

// startChild starts helper part with the environment env, in the working directory wd.
func startChild(t *testing.T, part, wd string, env map[string]string) *child {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Dir = wd
	cmd.Env = append(os.Environ(), envHelper+"="+part, "LOCALAPPDATA="+wd)
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	c := &child{cmd: cmd, lines: make(chan string, 16), done: make(chan error, 1)}
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			c.lines <- sc.Text()
		}
		close(c.lines)
		c.done <- cmd.Wait()
	}()
	t.Cleanup(func() {
		select {
		case <-c.done:
		case <-time.After(20 * time.Second):
			_ = cmd.Process.Kill() // only this test's own child
			t.Error("a helper process did not end")
		}
	})
	return c
}

// line waits for the child's next line of output.
func (c *child) line(t *testing.T, d time.Duration) string {
	t.Helper()
	select {
	case l, ok := <-c.lines:
		if !ok {
			t.Fatal("the helper ended without its line")
		}
		return l
	case <-time.After(d):
		t.Fatalf("no line from the helper within %v", d)
	}
	return ""
}

// wait waits for the child to end and returns its exit code.
func (c *child) wait(t *testing.T, d time.Duration) int {
	t.Helper()
	code := c.exitCode(d)
	if code < 0 {
		t.Fatalf("the helper did not end within %v", d)
	}
	return code
}

// exitCode waits at most d for the child to end: its exit code, -1 when it did not end (any
// goroutine).
func (c *child) exitCode(d time.Duration) int {
	select {
	case err := <-c.done:
		c.done <- err // for the cleanup
		if err == nil {
			return 0
		}
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		return -2
	case <-time.After(d):
		return -1
	}
}

// helperResult is what the second-start helper prints.
type helperResult struct {
	HandedOver bool   `json:"handedOver"`
	Allowed    bool   `json:"allowed"`
	Err        string `json:"err,omitempty"`
}

// helperMain is a child's part; its exit code is the test's to read.
func helperMain(part string) int {
	runtime.LockOSThread() // the main goroutine: the child's UI thread
	if d := os.Getenv(envDesktop); d != "" {
		if _, err := win.UseDesktop(d); err != nil {
			fmt.Fprintln(os.Stderr, "helper:", err)
			return 10
		}
	}
	var args []string
	if s := os.Getenv(envArgs); s != "" {
		if err := json.Unmarshal([]byte(s), &args); err != nil {
			return 11
		}
	}
	lock := os.Getenv(envLock)
	switch part {
	case "second": // a later start of the native window
		var allowed bool
		p, handed, err := Start(Config{
			Args: args, LockID: lock, Dir: os.Getenv(envDir), LogDir: os.Getenv(envDir),
			Daemon: statetest.NewDaemon(), NoMemoryTrim: true, AllowForeground: func() { allowed = true },
		})
		r := helperResult{HandedOver: handed, Allowed: allowed}
		if err != nil {
			r.Err = err.Error()
		}
		b, _ := json.Marshal(r)
		fmt.Println(string(b))
		if p != nil {
			p.Close()
			return 3
		}
		if !handed || err != nil {
			return 4
		}
		return 0
	case "wails-sender":
		return wailsSend(lock, args)
	case "wails-receiver":
		return wailsReceive(lock, args, waitEnv(), os.Stdout)
	case "host":
		return hostMain(lock, os.Getenv(envDir), args)
	case "closing-host":
		return closingHost(lock, waitEnv())
	case "theme-writer":
		return themeWriter(os.Getenv(envDir), args)
	}
	return 12
}

// waitEnv is $GLUE_WAIT (0 when unset).
func waitEnv() time.Duration {
	ms, _ := strconv.Atoi(os.Getenv(envWait))
	return time.Duration(ms) * time.Millisecond
}

// testCtx is a context for a test's blocking call.
func testCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}
