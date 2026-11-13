//go:build darwin && cgo

package mac

// [popups] A window's system dialogs: ui/win's Dialogs, with today's Mac dialogs.

/*
#include <stdlib.h>
#include <pthread.h>
#include "dialogs_darwin.h"
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"unsafe"

	"downex/ui/kit"
)

// Dialogs shows one window's system dialogs: the yes/no question asked before a risky file
// is opened (Confirm) and the folder and file pickers, as today's Mac window shows them
// (Wails v2.17's darwin MessageDialog and OpenFileDialog). Each runs on the main thread from
// the run loop, outside every call into the window's root and outside the main dispatch
// queue, so the window behind a modal dialog goes on painting and taking what is posted to
// it. The calls block until the user answers, so they are made off the main thread; one
// dialog shows at a time and the others wait their turn (a picker shown as a sheet counts
// until its sheet ends).
type Dialogs struct {
	owner *Window
	app   *App
	id    uintptr // its key in dialogsByID, which the main thread's wake-ups look it up by

	mu      sync.Mutex
	jobs    []*dialogJob
	closed  bool
	running bool    // the main thread's: a dialog is open
	sheet   uintptr // the main thread's: the token of the picker shown as a sheet, 0 for none
}

var (
	dialogsMu   sync.Mutex
	dialogsByID = map[uintptr]*Dialogs{}
	dialogsNext uintptr
)

type dialogJob struct {
	run     func(modal bool, done func())
	started chan struct{}
	done    chan struct{}
}

// NewDialogs makes the dialogs of owner, whose dialogs they are. It is for the main
// thread; Close ends them.
func NewDialogs(owner *Window) (*Dialogs, error) {
	if owner == nil || owner.app == nil {
		return nil, errInvalid("NewDialogs without a window")
	}
	onMain()
	d := &Dialogs{owner: owner, app: owner.app}
	dialogsMu.Lock()
	dialogsNext++
	d.id = dialogsNext
	dialogsByID[d.id] = d
	dialogsMu.Unlock()
	return d, nil
}

// Close ends the dialogs: a call waiting for its turn fails with kit.ErrClosed, and a
// picker shown as a sheet ends as cancelled. An application-modal dialog that is open stays
// until it is answered.
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
	dialogsMu.Lock()
	delete(dialogsByID, d.id) // a wake-up still on its way finds nothing
	dialogsMu.Unlock()
	if onMainThread() {
		d.endSheet()
	} else {
		d.app.Post(d.endSheet)
	}
}

// endSheet ends the picker shown as a sheet, if one is: its caller hears "" (cancelled), as
// when the user cancels it. For the main thread.
func (d *Dialogs) endSheet() {
	tok := d.sheet
	if tok == 0 {
		return
	}
	d.sheet = 0
	if w := d.owner; w != nil && w.ns != nil {
		C.dxpPickerEnd(w.ns)
	}
	pickDone(tok, "") // when AppKit has not answered by now; else nothing
}

// dialogsOwnerClosing ends the sheets on w, which is closing: AppKit would never answer
// them. For the main thread.
func dialogsOwnerClosing(w *Window) {
	dialogsMu.Lock()
	var ds []*Dialogs
	for _, d := range dialogsByID {
		if d.owner == w {
			ds = append(ds, d)
		}
	}
	dialogsMu.Unlock()
	for _, d := range ds {
		d.endSheet()
	}
}

func onMainThread() bool { return C.pthread_main_np() != 0 }

// do runs f on the main thread, outside every call into the root, and waits until it calls
// done. Called on the main thread itself it runs f at once, modally. ctx ends the wait for
// a dialog that has not opened yet; once it is open, the call waits for the user's answer.
func (d *Dialogs) do(ctx context.Context, f func(modal bool, done func())) error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return fmt.Errorf("mac: dialogs closed: %w", kit.ErrClosed)
	}
	if onMainThread() {
		d.mu.Unlock()
		ch := make(chan struct{})
		var once sync.Once
		f(true, func() { once.Do(func() { close(ch) }) })
		<-ch
		return nil
	}
	j := &dialogJob{run: f, started: make(chan struct{}), done: make(chan struct{})}
	d.jobs = append(d.jobs, j)
	d.mu.Unlock()
	C.dxpDialogsWake(C.uintptr_t(d.id))
	select {
	case <-j.started:
		<-j.done
		return nil
	case <-j.done: // closed before it ran
		return fmt.Errorf("mac: dialogs closed: %w", kit.ErrClosed)
	case <-ctx.Done():
		if d.forget(j) {
			return ctx.Err()
		}
		<-j.done // it opened meanwhile: the user answers it
		return nil
	}
}

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

// next opens the next waiting dialog unless one is open. A modal dialog's loop runs the
// wake-up of the next one, which then waits for the open one to be answered.
func (d *Dialogs) next() {
	if d.running {
		return
	}
	d.mu.Lock()
	if len(d.jobs) == 0 {
		d.mu.Unlock()
		return
	}
	j := d.jobs[0]
	d.jobs = d.jobs[1:]
	d.mu.Unlock()
	d.running = true
	close(j.started)
	var once sync.Once
	finish := func() {
		once.Do(func() {
			close(j.done)
			d.running = false
			C.dxpDialogsWake(C.uintptr_t(d.id))
		})
	}
	defer func() {
		// A panic leaves the job's zero answer and is reported as a root call's is.
		if v := recover(); v != nil {
			finish()
			d.app.safely(func() { panic(v) })
		}
	}()
	j.run(false, finish)
}

//export goDialogsRun
func goDialogsRun(id C.uintptr_t) {
	dialogsMu.Lock()
	d := dialogsByID[uintptr(id)]
	dialogsMu.Unlock()
	if d != nil {
		d.next()
	}
}

// ownerNS is the dialogs' owner for a sheet: its window while it shows, else none.
func (d *Dialogs) ownerNS() unsafe.Pointer {
	w := d.owner
	if w == nil || w.closed || w.ns == nil || !bool(C.dxpOwnerCanSheet(w.ns)) {
		return nil
	}
	return w.ns
}

// Confirm asks a yes/no question as today's Mac window asks before it opens a risky file:
// an NSAlert of the informational style with the buttons Yes and No, No the default
// (Return), above other windows (NSFloatingWindowLevel), application-modal. It reports a
// yes. It has host.ConfirmFunc's signature, for Files.SetConfirm.
func (d *Dialogs) Confirm(title, message string) (bool, error) {
	var yes bool
	err := d.do(context.Background(), func(_ bool, done func()) {
		defer done()
		t, m := C.CString(stripNULs(title)), C.CString(stripNULs(message))
		defer C.free(unsafe.Pointer(t))
		defer C.free(unsafe.Pointer(m))
		yes = bool(C.dxpAlertRun(t, m))
	})
	return yes, err
}

// PickFolder shows the folder picker titled title, starting in start ("" leaves the
// panel's own choice), and returns the chosen folder, or "" when the user cancels. It
// blocks until the user answers; ctx ends a wait for an earlier dialog only.
func (d *Dialogs) PickFolder(ctx context.Context, title, start string) (string, error) {
	return d.pick(ctx, title, start, true)
}

// PickFile shows the file picker for one file, titled title, starting in start, and
// returns the chosen file, or "" when the user cancels.
func (d *Dialogs) PickFile(ctx context.Context, title, start string) (string, error) {
	return d.pick(ctx, title, start, false)
}

var (
	pickMu   sync.Mutex
	pickWait = map[uintptr]func(string){}
	pickNext uintptr
)

// pick shows an NSOpenPanel set up as today's: a sheet on the window while it shows (else
// application-modal); a start folder that does not exist is left to the panel, as today's.
func (d *Dialogs) pick(ctx context.Context, title, start string, folders bool) (string, error) {
	var path string
	err := d.do(ctx, func(modal bool, done func()) {
		owner := unsafe.Pointer(nil)
		if !modal {
			owner = d.ownerNS()
		}
		pickMu.Lock()
		pickNext++
		tok := pickNext
		pickWait[tok] = func(p string) {
			if d.sheet == tok {
				d.sheet = 0
			}
			path = p
			done()
		}
		pickMu.Unlock()
		if owner != nil {
			d.sheet = tok
		}
		t, s := C.CString(stripNULs(title)), C.CString(stripNULs(start))
		defer C.free(unsafe.Pointer(t))
		defer C.free(unsafe.Pointer(s))
		C.dxpPickerRun(owner, C.uintptr_t(tok), C.bool(folders), t, s)
	})
	return path, err
}

//export goPickDone
func goPickDone(tok C.uintptr_t, path *C.char) {
	p := ""
	if path != nil {
		p = C.GoString(path)
	}
	pickDone(uintptr(tok), p)
}

// pickDone answers the picker of tok once: AppKit's answer, or "" for a sheet ended because
// its window or its Dialogs closed.
func pickDone(tok uintptr, path string) {
	pickMu.Lock()
	f := pickWait[tok]
	delete(pickWait, tok)
	pickMu.Unlock()
	if f != nil {
		f(path)
	}
}

// externalURL is what OpenExternal opens: an https address with a host, as today's
// openExternal accepts (wails.ts).
var externalURL = regexp.MustCompile(`(?i)^https://[^/]`)

// ErrNotHTTPS is OpenExternal's answer to anything but an https address.
var ErrNotHTTPS = errors.New("not an https address")

// OpenExternal opens an https address (a licence's page) in the default browser. It
// needs no window; NSWorkspace is asked on the main thread (from another thread the call
// waits for it, at most a few seconds).
func OpenExternal(ctx context.Context, url string) error {
	if !externalURL.MatchString(url) {
		return ErrNotHTTPS
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	cs := C.CString(stripNULs(url))
	defer C.free(unsafe.Pointer(cs))
	if !C.dxpOpenURL(cs) {
		return fmt.Errorf("mac: open %s: the browser did not open it", url)
	}
	return nil
}
