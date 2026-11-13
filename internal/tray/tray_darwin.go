//go:build darwin && cgo

package tray

// The macOS menu-bar icon and its notifications (docs/MACOS-SPEC.md §1), the
// executable of Downex Agent.app: menubar.go and mainthread.go bound to AppKit and
// UserNotifications (tray_darwin.m, macOS 10.14+ for UserNotifications; the
// deployment target is macOS 13). A darwin build without cgo has no menu-bar icon
// (tray_other.go: New returns ErrNotSupported).
//
// Go and Objective-C call each other only through the C functions of tray_darwin.h
// and the functions exported below. A menuBar is known to Objective-C as a
// cgo.Handle, which it passes back on every call: queued drains (downexWake) and
// the menu's choices (the status object). The handle is deleted by a block queued
// to the main queue after the final drain (downexRelease): the main queue is serial
// and no wake is queued after the final drain has seen closing (menuBar.request), so
// no block uses the handle afterwards, and the status object stops calling with it
// when destroyed.

/*
#cgo CFLAGS: -x objective-c -fobjc-arc -mmacosx-version-min=13.0
#cgo LDFLAGS: -framework Foundation -framework AppKit -framework UserNotifications -framework CoreGraphics -mmacosx-version-min=13.0
#include <stdint.h>
#include <stdlib.h>
#include "tray_darwin.h"
*/
import "C"

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime/cgo"
	"sync"
	"sync/atomic"
	"unsafe"
)

// app serves the process's main thread for AppKit.
var app = newAppLoop(cocoaApp{}, os.Exit)

// RunMain runs f and returns its result. AppKit, which shows the menu-bar icon, runs
// on the process's main thread only, so a program with a Tray calls RunMain from
// main, with the main goroutine locked to the main thread by runtime.LockOSThread in
// an init function: f then runs on another goroutine while the main thread waits for
// it and, once the first Tray is created, runs the AppKit event loop. When f returns
// the event loop ends and RunMain returns; should the main thread not come back
// within a few seconds, the process exits with f's result. A system request to quit
// the app (logout, Activity Monitor) reaches the tray's OnCommand as CommandQuit and
// waits for f to return. Called on another thread, RunMain just calls f (and New
// fails).
func RunMain(f func() int) int { return app.run(f) }

// cocoaApp implements appKit with tray_darwin.m.
type cocoaApp struct{}

func (cocoaApp) mainThread() bool { return C.downexIsMainThread() != 0 }

func (cocoaApp) start() error {
	switch r := C.downexAppStart(); r {
	case C.DOWNEX_APP_OK:
		return nil
	case C.DOWNEX_APP_NOT_MAIN:
		return errors.New("tray: AppKit must be started on the main thread")
	case C.DOWNEX_APP_NO_SESSION:
		return fmt.Errorf("%w: there is no window-server (GUI) session", ErrNotSupported)
	default:
		return fmt.Errorf("tray: AppKit did not start (%d)", int(r))
	}
}

func (cocoaApp) run()            { C.downexAppRun() }
func (cocoaApp) stop()           { C.downexAppStop() }
func (cocoaApp) replyTerminate() { C.downexAppReplyTerminate() }

// Supported reports whether New can show an icon: in a window-server (GUI) session.
func Supported() bool { return C.downexHasWindowServer() != 0 }

// Tray is a menu-bar icon (NSStatusItem) with its menu, and the notifications of
// Downex Agent.app. Its methods may be called from any goroutine; they queue the
// change for the main thread and return at once. The zero Tray is not usable; a nil
// *Tray ignores every call.
type Tray struct {
	mb            *menuBar
	removeHandler func()
}

// New creates a menu-bar icon, starting AppKit on the main thread first (see
// RunMain). The icon is not shown until SetVisible(true).
func New(opts Options) (*Tray, error) {
	if err := app.ensure(); err != nil {
		return nil, err
	}
	be := &cocoaStatus{notify: C.downexNotifyAvailable() != 0}
	mb := newMenuBar(opts, be)
	be.h = cgo.NewHandle(mb)
	if opts.Logger != nil {
		notifyLog.Store(opts.Logger)
	}
	if !be.notify {
		mb.log.Warn("tray: notifications are not shown: they need the app bundle (Downex Agent.app)")
	}
	// Clicks on notifications and the system's requests to quit go to this tray.
	remove := app.setHandler(func(c Command) { mb.dispatch(c) })
	return &Tray{mb: mb, removeHandler: remove}, nil
}

// SetVisible shows or removes the icon (Settings.show_tray_icon).
func (t *Tray) SetVisible(v bool) {
	if t != nil {
		t.mb.setVisible(v)
	}
}

// SetTooltip sets the icon's tooltip; the menu's first line shows it without the
// "Downex — " prefix, as "N active · X/s".
func (t *Tray) SetTooltip(tip string) {
	if t != nil {
		t.mb.setTooltip(tip)
	}
}

// Notify posts b as a notification (alerts only, silent); clicking it sends
// CommandOpen. The first one asks the user for authorization. While the icon is not
// visible, it is dropped.
func (t *Tray) Notify(b Balloon) {
	if t != nil {
		t.mb.notify(b)
	}
}

// Close removes the icon. A command already handed to Options.OnCommand may still
// run when it returns. AppKit keeps running until RunMain's function returns.
func (t *Tray) Close() error {
	if t == nil {
		return nil
	}
	t.removeHandler()
	return t.mb.close()
}

// templateImages are the menu-bar icon's PNG representations: 1× and 2× (Retina).
var templateImages = sync.OnceValues(func() ([]byte, []byte) {
	return templatePNG(templatePoints), templatePNG(2 * templatePoints)
})

// cocoaStatus implements menuBarBackend with tray_darwin.m.
type cocoaStatus struct {
	h      cgo.Handle // of the menuBar; set by New before any request
	notify bool       // the process is an app bundle: notifications can be posted

	status unsafe.Pointer // the retained DownexStatus object; main thread
}

var _ menuBarBackend = (*cocoaStatus)(nil)

func (c *cocoaStatus) wake() { C.downexWake(C.uintptr_t(c.h)) }

func (c *cocoaStatus) show(tip string) error {
	if c.status != nil {
		if err := c.setTip(tip); err != nil {
			return err
		}
		C.downexStatusSetVisible(c.status, 1)
		return nil
	}
	png1, png2 := templateImages()
	ctip, cline := C.CString(cText(tip)), C.CString(cText(menuStatus(tip)))
	defer C.free(unsafe.Pointer(ctip))
	defer C.free(unsafe.Pointer(cline))
	s := C.downexStatusCreate(C.uintptr_t(c.h), ctip, cline,
		bytesPointer(png1), C.int(len(png1)), bytesPointer(png2), C.int(len(png2)), C.double(templatePoints))
	if s == nil {
		return errors.New("tray: the menu bar did not create the status item")
	}
	for _, it := range menu {
		label := C.CString(cText(it.label))
		C.downexStatusAddItem(s, C.int(it.cmd), label)
		C.free(unsafe.Pointer(label))
	}
	c.status = s
	return nil
}

func (c *cocoaStatus) hide() error {
	if c.status != nil {
		C.downexStatusSetVisible(c.status, 0)
	}
	return nil
}

func (c *cocoaStatus) setTip(tip string) error {
	if c.status == nil {
		return errors.New("tray: no status item")
	}
	ctip, cline := C.CString(cText(tip)), C.CString(cText(menuStatus(tip)))
	defer C.free(unsafe.Pointer(ctip))
	defer C.free(unsafe.Pointer(cline))
	C.downexStatusSetTip(c.status, ctip, cline)
	return nil
}

func (c *cocoaStatus) post(b Balloon) error {
	if !c.notify {
		return nil // reported once by New
	}
	title, body := C.CString(cText(b.Title)), C.CString(cText(b.Text))
	defer C.free(unsafe.Pointer(title))
	defer C.free(unsafe.Pointer(body))
	C.downexNotifyPost(title, body)
	return nil
}

func (c *cocoaStatus) destroy() {
	if c.status != nil {
		C.downexStatusDestroy(c.status)
		c.status = nil
	}
}

func (c *cocoaStatus) release() { C.downexRelease(C.uintptr_t(c.h)) }

// describe is the status item's state for tests (downexStatusDescribe): visibility,
// tooltip, then the menu's lines. Main thread.
func (c *cocoaStatus) describe() string {
	if c.status == nil {
		return ""
	}
	s := C.downexStatusDescribe(c.status)
	if s == nil {
		return ""
	}
	defer C.free(unsafe.Pointer(s))
	return C.GoString(s)
}

// perform chooses the menu item at index, as a click does (tests). Main thread.
func (c *cocoaStatus) perform(index int) {
	if c.status != nil {
		C.downexStatusPerform(c.status, C.int(index))
	}
}

// bytesPointer is b's first byte for C, which copies it during the call (NSData).
func bytesPointer(b []byte) unsafe.Pointer {
	if len(b) == 0 {
		return nil
	}
	return unsafe.Pointer(&b[0])
}

// Notification outcomes reported by the notification center's queue.
var (
	notifyLog    atomic.Pointer[slog.Logger] // the latest tray's logger
	notifyDenied atomic.Bool                 // the denial was logged
	// releasedHandles counts the menuBars released (tests).
	releasedHandles atomic.Int64
)

// Callbacks from tray_darwin.m.

//export goDownexTrayDrain
func goDownexTrayDrain(h C.uintptr_t) {
	cgo.Handle(h).Value().(*menuBar).drain()
}

//export goDownexTrayReleased
func goDownexTrayReleased(h C.uintptr_t) {
	cgo.Handle(h).Delete()
	releasedHandles.Add(1)
}

//export goDownexTrayCommand
func goDownexTrayCommand(h C.uintptr_t, cmd C.int) {
	if cmd > 0 {
		cgo.Handle(h).Value().(*menuBar).dispatch(Command(cmd))
	}
}

//export goDownexAppShouldTerminate
func goDownexAppShouldTerminate() C.int {
	if app.shouldTerminate() {
		return 1
	}
	return 0
}

//export goDownexAppWillTerminate
func goDownexAppWillTerminate() {
	app.willTerminate()
}

//export goDownexNotificationClicked
func goDownexNotificationClicked() {
	app.route(CommandOpen)
}

//export goDownexNotifyResult
func goDownexNotifyResult(kind C.int, msg *C.char) {
	log := notifyLog.Load()
	if log == nil {
		return
	}
	text := ""
	if msg != nil {
		text = C.GoString(msg)
	}
	switch kind {
	case C.DOWNEX_NOTIFY_DENIED:
		if notifyDenied.CompareAndSwap(false, true) {
			log.Info("tray: notifications are not allowed for Downex (System Settings › Notifications)", "reason", text)
		}
	default:
		log.Warn("tray: show notification", "err", text)
	}
}
