//go:build darwin && cgo

package mac

/*
#include <stdlib.h>
#include <pthread.h>
#include "window_darwin.h"
*/
import "C"

import (
	"errors"
	"fmt"
	"runtime"
	"runtime/cgo"
	"sync"
)

// AppOptions configure the application.
type AppOptions struct {
	// Regular: the application has a Dock icon and the menu bar (the main window's
	// Downex.app); else it is an accessory (the progress windows' Downex Progress.app).
	Regular bool
	// OnPanic hears a panic recovered from a call into a root (nil: it is re-raised).
	OnPanic func(v any)
	// [program]
	// OnWindowClosed hears that w has closed (its root detached), before Run may end.
	OnWindowClosed func(w *Window)
	// OnWindowMade is told each window NewWindow makes, before its root is attached: a
	// failure from there on (a panic in Attach) can close it.
	OnWindowMade func(w *Window)
	// [/program]
}

// App is the process's AppKit application: every window of the backend, one Resources
// they share, and the event loop. Its methods belong to the main thread (the program locks
// the main goroutine to it in init), except Post.
type App struct {
	opts    AppOptions
	res     *Resources
	windows map[*Window]struct{}

	mu    sync.Mutex
	posts []func()
	woken bool

	// [program]
	progCalls []*Window // the windows whose root calls are in progress, innermost last (PanicWindow)
	// [/program]
}

// ErrNotMainThread: AppKit runs only on the process's main thread.
var ErrNotMainThread = errors.New("mac: the application must run on the main thread")

var current *App

// NewApp makes the application. Only one exists per process.
func NewApp(o AppOptions) (*App, error) {
	if current != nil {
		return nil, errors.New("mac: an App exists already")
	}
	res, err := NewResources()
	if err != nil {
		return nil, fmt.Errorf("mac: resources: %w", err)
	}
	C.dxAppInit(C.bool(o.Regular))
	a := &App{opts: o, res: res, windows: map[*Window]struct{}{}}
	current = a
	return a, nil
}

// Resources is the drawing and text resources every window shares.
func (a *App) Resources() *Resources { return a.res }

// Run runs the event loop until the last window closes or Quit.
func (a *App) Run() error {
	if len(a.windows) == 0 {
		return nil
	}
	C.dxAppRun()
	return nil
}

// Quit ends Run.
func (a *App) Quit() { C.dxAppStop() }

// Close releases the application's resources once Run has returned.
func (a *App) Close() {
	if a.res != nil {
		a.res.Close()
		a.res = nil
	}
	current = nil
}

// Post runs f on the main thread soon. Any goroutine may call it.
func (a *App) Post(f func()) {
	a.mu.Lock()
	a.posts = append(a.posts, f)
	wake := !a.woken
	a.woken = true
	a.mu.Unlock()
	if wake {
		C.dxAppWake()
	}
}

func (a *App) runPosts() {
	a.mu.Lock()
	fs := a.posts
	a.posts, a.woken = nil, false
	a.mu.Unlock()
	for _, f := range fs {
		a.safely(f)
	}
}

// safely runs f, handing a panic to OnPanic.
func (a *App) safely(f func()) {
	if a.opts.OnPanic == nil {
		f()
		return
	}
	defer func() {
		if v := recover(); v != nil {
			a.opts.OnPanic(v)
		}
	}()
	f()
}

func (a *App) removed(w *Window) {
	delete(a.windows, w)
	// [program]
	if a.opts.OnWindowClosed != nil {
		a.safely(func() { a.opts.OnWindowClosed(w) })
	}
	// [/program]
	if len(a.windows) == 0 {
		C.dxAppStop()
	}
}

//export goAppPosts
func goAppPosts() {
	if current != nil {
		current.runPosts()
	}
}

//export goAppEnvironment
func goAppEnvironment() {
	if current == nil {
		return
	}
	for w := range current.windows {
		w.environmentChanged()
	}
}

// windowOf finds the window of a handle; nil once it is gone.
func windowOf(h C.uintptr_t) *Window {
	if h == 0 {
		return nil
	}
	// [popups] a closed window's views and delegate forget the handle before it is deleted
	// (dxWindowRelease): an event AppKit still has for the window brings 0
	w, _ := cgo.Handle(h).Value().(*Window)
	// [/popups]
	return w
}

// onMain panics off the main thread: a programming error AppKit would crash on later.
func onMain() {
	if C.pthread_main_np() == 0 {
		panic(ErrNotMainThread)
	}
}

func init() {
	// The program's main goroutine must stay on the main thread for AppKit; a program that
	// imports this package locks it in its own init, which runs after this one. Locking here
	// too makes a test binary safe.
	runtime.LockOSThread()
}
