//go:build windows

package win

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"

	"downex/ui/kit"
	"downex/ui/win/internal/w32"
)

// Dialogs shows one window's system dialogs: the yes/no question asked before a risky file
// is opened (Confirm) and the folder and file pickers (picker.go). Each runs on the UI
// thread, which is in COM's single-threaded apartment as the shell's dialogs require, but
// outside every call into the window's root: the window behind the modal dialog goes on
// painting and taking what is posted to it (live numbers, toasts), as today's window does.
// The calls block until the user answers, so they are made off the UI thread (App.Go); one
// dialog shows at a time and the others wait their turn.
type Dialogs struct {
	owner *Window
	app   *App
	hwnd  w32.HWND // the message-only window that runs the dialogs

	mu      sync.Mutex
	jobs    []*dialogJob
	closed  bool
	running bool // the UI thread's: a dialog is open
}

type dialogJob struct {
	run  func()
	done chan struct{}
	app  *App // where a panic is reported
}

// msgRunDialog makes the Dialogs' window run the next dialog.
const msgRunDialog = w32.WM_APP + 0x32

var (
	dialogsOnce  sync.Once
	dialogsProc  uintptr
	dialogsClass *uint16
	dialogsByWnd struct {
		sync.Mutex
		by map[w32.HWND]*Dialogs
	}
)

// NewDialogs makes the dialogs of owner, whose modal dialogs they are. It is for the UI
// thread; Close ends them.
func NewDialogs(owner *Window) (*Dialogs, error) {
	if owner == nil || owner.app == nil {
		return nil, errInvalid("NewDialogs without a window")
	}
	if err := owner.app.onThread(); err != nil {
		return nil, err
	}
	var regErr error
	dialogsOnce.Do(func() {
		dialogsProc = w32.NewCallback(dialogsWndProc)
		dialogsByWnd.by = map[w32.HWND]*Dialogs{}
		dialogsClass, _ = windows.UTF16PtrFromString("DownexNativeDialogs")
		wc := &w32.WNDCLASSEXW{WndProc: dialogsProc, Instance: w32.GetModuleHandle(), ClassName: dialogsClass}
		wc.Size = uint32(unsafe.Sizeof(*wc))
		if _, err := w32.RegisterClassEx(wc); err != nil && !errors.Is(err, windows.ERROR_CLASS_ALREADY_EXISTS) {
			regErr = err
		}
	})
	if regErr != nil {
		return nil, fmt.Errorf("win: dialogs window class: %w", regErr)
	}
	h, err := w32.CreateWindowEx(0, dialogsClass, nil, 0, 0, 0, 0, 0, w32.HWND_MESSAGE, w32.GetModuleHandle(), 0)
	if err != nil {
		return nil, fmt.Errorf("win: dialogs window: %w", err)
	}
	d := &Dialogs{owner: owner, app: owner.app, hwnd: h}
	dialogsByWnd.Lock()
	dialogsByWnd.by[h] = d
	dialogsByWnd.Unlock()
	return d, nil
}

// Close ends the dialogs: a call waiting for its turn fails with kit.ErrClosed. It is for
// the UI thread; a dialog that is open stays until it is answered (or its owner closes).
func (d *Dialogs) Close() {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.closed = true
	jobs := d.jobs
	d.jobs = nil
	d.mu.Unlock()
	for _, j := range jobs {
		close(j.done) // never run
	}
	dialogsByWnd.Lock()
	delete(dialogsByWnd.by, d.hwnd)
	dialogsByWnd.Unlock()
	w32.DestroyWindow(d.hwnd)
}

func dialogsWndProc(h w32.HWND, msg uint32, wp, lp uintptr) uintptr {
	if msg == w32.WM_CLOSE {
		return 0 // answered: the window lives as long as its Dialogs (Close destroys it)
	}
	if msg == msgRunDialog {
		dialogsByWnd.Lock()
		d := dialogsByWnd.by[h]
		dialogsByWnd.Unlock()
		if d != nil {
			d.runQueued()
		}
		return 0
	}
	return w32.DefWindowProc(h, msg, wp, lp)
}

// runQueued runs the waiting dialogs one after another. A dialog's modal loop dispatches
// the wake-up of the next one, which then waits here for the open one to be answered.
func (d *Dialogs) runQueued() {
	if d.running {
		return
	}
	d.running = true
	defer func() { d.running = false }()
	for {
		d.mu.Lock()
		if len(d.jobs) == 0 {
			d.mu.Unlock()
			return
		}
		j := d.jobs[0]
		d.jobs = d.jobs[1:]
		d.mu.Unlock()
		j.runSafely()
	}
}

func (j *dialogJob) runSafely() {
	defer close(j.done)
	defer func() {
		// The job records its own result; a panic leaves its zero answer, and is reported as
		// a window procedure's is (App.fail).
		if v := recover(); v != nil && j.app != nil {
			j.app.fail(v, debug.Stack())
		}
	}()
	j.run()
}

// do runs f on the UI thread, outside every call into the root, and waits for it. Called on
// the UI thread itself it runs f at once. ctx ends the wait for a dialog that has not opened
// yet; once it is open, the call waits for the user's answer.
func (d *Dialogs) do(ctx context.Context, f func()) error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return fmt.Errorf("win: dialogs closed: %w", kit.ErrClosed)
	}
	if windows.GetCurrentThreadId() == d.app.thread {
		d.mu.Unlock()
		f()
		return nil
	}
	started := make(chan struct{})
	j := &dialogJob{run: func() { close(started); f() }, done: make(chan struct{}), app: d.app}
	d.jobs = append(d.jobs, j)
	d.mu.Unlock()
	if err := w32.PostMessage(d.hwnd, msgRunDialog, 0, 0); err != nil {
		d.forget(j)
		return fmt.Errorf("win: %w", err)
	}
	select {
	case <-started:
		<-j.done
		return nil
	case <-j.done: // closed before it ran
		return fmt.Errorf("win: dialogs closed: %w", kit.ErrClosed)
	case <-ctx.Done():
		if d.forget(j) {
			return ctx.Err()
		}
		<-j.done // it opened meanwhile: the user answers it
		return nil
	}
}

// forget takes a job that has not run off the queue and reports whether it was there.
func (d *Dialogs) forget(j *dialogJob) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i, x := range d.jobs {
		if x == j {
			d.jobs = append(d.jobs[:i], d.jobs[i+1:]...)
			return true
		}
	}
	return false
}

// ownerHWND is the dialogs' owner while it is shown (today's window owns its dialogs only
// while it is visible), else none.
func (d *Dialogs) ownerHWND() w32.HWND {
	w := d.owner
	if w == nil || w.closed || w.hwnd == 0 || !windows.IsWindowVisible(windows.HWND(w.hwnd)) {
		return 0
	}
	return w.hwnd
}

// Confirm asks a yes/no question in the system's message box, as today's window asks
// before it opens a risky file (Wails' QuestionDialog with the buttons Yes and No, No the
// default: MB_YESNO | MB_DEFBUTTON2 | MB_SYSTEMMODAL, no icon), owned by the window while it
// is shown. It reports a yes. title and message are shown as they are: the host escapes
// the names in them (host.ConfirmText). It has host.ConfirmFunc's signature, for
// Files.SetConfirm.
func (d *Dialogs) Confirm(title, message string) (bool, error) {
	t, err := windows.UTF16PtrFromString(title)
	if err != nil {
		return false, fmt.Errorf("win: message box title: %w", err)
	}
	m, err := windows.UTF16PtrFromString(message)
	if err != nil {
		return false, fmt.Errorf("win: message box text: %w", err)
	}
	var (
		answer int32
		boxErr error
	)
	if err := d.do(context.Background(), func() {
		answer, boxErr = windows.MessageBox(windows.HWND(d.ownerHWND()), m, t, mbYesNo|mbDefButton2|mbSystemModal)
	}); err != nil {
		return false, err
	}
	if answer == 0 {
		return false, fmt.Errorf("win: message box: %w", boxErr)
	}
	return answer == idYes, nil
}
