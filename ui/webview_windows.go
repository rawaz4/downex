//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"syscall"
	"time"
	"unsafe"

	"github.com/wailsapp/go-webview2/pkg/edge"
	"golang.org/x/sys/windows"
)

// A window in the background still holds what WebView2 keeps for drawing and running its
// page (116 MB fresh, ~156 MB after a download, measured). WebView2's memory target "low"
// is Microsoft's way for an app that is not in use: the page keeps running and drawing,
// and what it does not touch may be paged out until it is used again. Every Downex window
// (each its own process) pulses it while it is minimised or behind another app's window,
// and keeps "normal" while it is in front (trimPolicy says when).

var procIsIconic = user32.NewProc("IsIconic") // user32: shell_windows.go

// startMemoryTrim watches this window from its start-up context ctx and sets WebView2's
// memory target with it. Nothing happens when the WebView cannot be found (see webviewOf).
func startMemoryTrim(ctx context.Context, log *slog.Logger) {
	wv := webviewOf(ctx)
	if wv == nil {
		log.Warn("WebView2 memory target unavailable: this Wails version keeps its WebView elsewhere")
		return
	}
	go wv.trimWhileInactive(ctx, log)
}

// webview is the WebView2 of one Wails v2 window and the means to reach it on its thread.
type webview struct {
	chromium *edge.Chromium
	invoke   func(func()) bool // runs f on the window's thread (WebView2 is single-threaded COM)
	hwnd     windows.HWND
}

// webviewOf finds the window's WebView2 in Wails v2's Windows frontend, which keeps it
// unexported (v2.17.0: the context's "frontend" value is a *Frontend with the fields
// chromium *edge.Chromium and mainWindow, whose Invoke and Handle come from winc's
// ControlBase). Fields are taken by name and exact type, so another shape gives nil.
func webviewOf(ctx context.Context) *webview {
	fe := reflect.ValueOf(ctx.Value("frontend"))
	if fe.Kind() != reflect.Pointer || fe.IsNil() || fe.Elem().Kind() != reflect.Struct {
		return nil
	}
	fe = fe.Elem()
	ch := field(fe, "chromium")
	if !ch.IsValid() || ch.Type() != reflect.TypeFor[*edge.Chromium]() || ch.IsNil() {
		return nil
	}
	win := field(fe, "mainWindow")
	if !win.IsValid() || win.Kind() != reflect.Pointer || win.IsNil() {
		return nil
	}
	invoke := win.MethodByName("Invoke")
	handle := win.MethodByName("Handle")
	if !invoke.IsValid() || invoke.Type() != reflect.TypeFor[func(func()) bool]() ||
		!handle.IsValid() || handle.Type().NumIn() != 0 || handle.Type().NumOut() != 1 ||
		handle.Type().Out(0).Kind() != reflect.Uintptr {
		return nil
	}
	hwnd := windows.HWND(handle.Call(nil)[0].Uint())
	if hwnd == 0 {
		return nil
	}
	return &webview{
		chromium: ch.Interface().(*edge.Chromium),
		invoke:   invoke.Interface().(func(func()) bool),
		hwnd:     hwnd,
	}
}

// field is the struct field name of s, readable although unexported.
func field(s reflect.Value, name string) reflect.Value {
	f := s.FieldByName(name)
	if !f.IsValid() {
		return f
	}
	return reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem()
}

func (w *webview) trimWhileInactive(ctx context.Context, log *slog.Logger) {
	t := time.NewTicker(trimPoll)
	defer t.Stop()
	var p trimPolicy
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			low, change := p.step(now, w.inUse())
			if !change {
				continue
			}
			err := w.setMemoryLow(ctx, low)
			if err == nil || errors.Is(err, errWindowGone) {
				if err != nil {
					return
				}
				continue
			}
			log.Warn("cannot set the WebView2 memory target", "low", low, "error", err)
			if errors.Is(err, errUnsupported) {
				return // the next change would fail the same way
			}
		}
	}
}

// inUse reports whether the window is the one in front (or a dialog of this process is)
// and not minimised. Each Downex window is its own process, so the foreground window
// belonging to this process means this window or its own dialog.
func (w *webview) inUse() bool {
	if r, _, _ := procIsIconic.Call(uintptr(w.hwnd)); r != 0 {
		return false
	}
	fg := windows.GetForegroundWindow()
	if fg == 0 {
		return false
	}
	var pid uint32
	if _, err := windows.GetWindowThreadProcessId(fg, &pid); err != nil {
		return false
	}
	return int(pid) == os.Getpid()
}

var (
	errWindowGone  = errors.New("the window is closing")
	errUnsupported = errors.New("this WebView2 runtime has no memory target (ICoreWebView2_19)")
	errBusy        = errors.New("the window's thread did not answer in time")
)

// invokeTimeout bounds the wait for the window's thread. A set still queued after it runs
// later, before any set queued after it (the thread runs them in order), so the next
// change still wins.
var invokeTimeout = 5 * time.Second

// setMemoryLow sets the WebView's memory target on the window's thread and waits for it.
func (w *webview) setMemoryLow(ctx context.Context, low bool) error {
	level := memoryTargetNormal
	if low {
		level = memoryTargetLow
	}
	done := make(chan error, 1)
	if !w.invoke(func() { done <- putMemoryTarget(w.chromium, level) }) {
		return errWindowGone
	}
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return errWindowGone
	case <-time.After(invokeTimeout):
		return errBusy
	}
}

// ICoreWebView2_19 (WebView2 runtime 114+) and its memory target, which go-webview2's edge
// does not cover (its pkg/webview2 does, but panics at init with this Go). The slots are
// WebView2.h's: IUnknown's 3, ICoreWebView2's 58 and the 58 methods that ICoreWebView2_2
// to _18 add, then get_ and put_MemoryUsageTargetLevel.
var iidCoreWebView2_19 = windows.GUID{Data1: 0x6921f954, Data2: 0x79b0, Data3: 0x437f, Data4: [8]byte{0xa9, 0x97, 0xc8, 0x58, 0x11, 0x89, 0x7c, 0x68}}

const (
	slotQueryInterface  = 0
	slotRelease         = 2
	slotGetMemoryTarget = 119
	slotPutMemoryTarget = 120

	memoryTargetNormal uint32 = 0
	memoryTargetLow    uint32 = 1
)

// comCall calls the method in slot of the COM object p. Out-parameters are passed in args
// as uintptr, so uintptrescapes moves what they point to off the stack: a stack copy
// before the call would otherwise leave the method writing into freed stack
// (unsafe.Pointer rule 4; go-webview2's ComProc.Call does the same, golang/go#34474).
//
//go:uintptrescapes
func comCall(p unsafe.Pointer, slot int, args ...uintptr) uintptr {
	vtbl := *(*unsafe.Pointer)(p)
	fn := *(*uintptr)(unsafe.Add(vtbl, slot*int(unsafe.Sizeof(uintptr(0)))))
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{uintptr(p)}, args...)...)
	return r
}

// putMemoryTarget runs on the window's thread. It reads the target first: a level other
// than normal or low there would mean these are not the slots, and nothing is set.
func putMemoryTarget(c *edge.Chromium, level uint32) error {
	ctrl := c.GetController()
	if ctrl == nil {
		return errWindowGone
	}
	core, err := ctrl.GetCoreWebView2()
	if err != nil {
		return err
	}
	defer core.Release()
	var wv19 unsafe.Pointer
	if hr := comCall(unsafe.Pointer(core), slotQueryInterface, uintptr(unsafe.Pointer(&iidCoreWebView2_19)), uintptr(unsafe.Pointer(&wv19))); hr != 0 || wv19 == nil {
		return errUnsupported
	}
	defer comCall(wv19, slotRelease)
	var current uint32 = 0xffffffff
	if hr := comCall(wv19, slotGetMemoryTarget, uintptr(unsafe.Pointer(&current))); hr != 0 {
		return syscall.Errno(hr)
	}
	if current != memoryTargetNormal && current != memoryTargetLow {
		return fmt.Errorf("unexpected memory target %d: not setting it", current)
	}
	if hr := comCall(wv19, slotPutMemoryTarget, uintptr(level)); hr != 0 {
		return syscall.Errno(hr)
	}
	return nil
}
