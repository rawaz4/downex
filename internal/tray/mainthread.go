package tray

import (
	"errors"
	"sync"
	"time"
)

// AppKit (the macOS menu-bar icon) must run on the process's main thread, and its
// event loop, [NSApp run], keeps that thread for as long as it runs. appLoop is the
// logic that hands the main thread over (RunMain, tray_darwin.go), in pure Go so that
// it is tested on every system:
//
//   - RunMain runs the program (the daemon) on a new goroutine; the main goroutine,
//     locked to the main thread by an init function, waits for it to finish or for a
//     tray to ask for AppKit (ensure).
//   - ensure starts AppKit on the main thread once; the main thread then runs the
//     event loop, and every later AppKit call is queued to it (dispatch_async).
//     A program that never creates a tray never touches AppKit (no window-server
//     connection for --no-tray or private daemons).
//   - When the program returns, the event loop is stopped and RunMain returns its
//     status; should the main thread not come back, the process exits after grace.
//   - The system asking the app to quit (logout, Activity Monitor's Quit: the quit
//     Apple Event, applicationShouldTerminate:) is postponed (NSTerminateLater) and
//     turned into CommandQuit, the graceful shutdown; once the program has stopped,
//     the termination proceeds (replyTerminate) and the process exits with the
//     program's status (willTerminate).

// appKit is what appLoop needs from AppKit (tray_darwin.go).
type appKit interface {
	// mainThread reports whether the caller runs on the process's main thread.
	mainThread() bool
	// start sets up the application (NSApplication, its delegate, the notification
	// center's delegate); it runs on the main thread.
	start() error
	// run runs the event loop on the main thread until stop or a termination.
	run()
	// stop ends run; it may be called from any thread, also before run started.
	stop()
	// replyTerminate lets a termination that the delegate postponed proceed; it may
	// be called from any thread.
	replyTerminate()
}

// Main-thread hand-over bounds.
const (
	// appExitGrace is how long the process waits for the main thread to leave the
	// event loop after the program returned before it exits anyway.
	appExitGrace = 3 * time.Second
	// appStartTimeout bounds how long ensure waits for the main thread.
	appStartTimeout = 10 * time.Second
	// appTerminateWait bounds how long a termination that AppKit carries out without
	// asking (it never should) waits for the program to stop.
	appTerminateWait = 20 * time.Second
	// exitAbandoned is the exit status when the program did not finish.
	exitAbandoned = 1
)

var (
	// errMainNotServed: RunMain does not serve the main thread.
	errMainNotServed = errors.New("tray: the menu bar needs the main thread: run the program through " +
		"tray.RunMain from main, locked to the main thread by runtime.LockOSThread in an init function")
	// errAppEnded: the program has already returned.
	errAppEnded = errors.New("tray: the program is ending")
)

// appLoop serves the main thread. The zero appLoop is not usable.
type appLoop struct {
	kit          appKit
	exit         func(code int) // os.Exit
	grace        time.Duration
	startTimeout time.Duration
	termWait     time.Duration

	startMu sync.Mutex // serialises ensure

	mu          sync.Mutex
	served      bool  // RunMain serves the main thread
	started     bool  // AppKit is set up and its event loop runs (or is about to)
	startErr    error // AppKit could not be set up
	finished    bool  // the program returned
	code        int
	terminating bool // a termination is postponed until the program has stopped
	handler     func(Command)
	handlerID   uint64
	pendingOpen bool // a notification was clicked before any handler was set

	starts chan chan error // ensure's requests to the main goroutine
	done   chan struct{}   // closed when the program returned
}

func newAppLoop(kit appKit, exit func(int)) *appLoop {
	return &appLoop{
		kit: kit, exit: exit,
		grace: appExitGrace, startTimeout: appStartTimeout, termWait: appTerminateWait,
		starts: make(chan chan error), done: make(chan struct{}),
	}
}

// run runs f and returns its result, serving the main thread meanwhile when called
// on it (see RunMain).
func (a *appLoop) run(f func() int) int {
	if !a.kit.mainThread() {
		return f()
	}
	a.mu.Lock()
	if a.served { // a nested or second RunMain: nothing more to serve
		a.mu.Unlock()
		return f()
	}
	a.served = true
	a.mu.Unlock()
	go a.finish(f)
	for {
		select {
		case <-a.done:
			return a.result()
		case reply := <-a.starts:
			err := a.kit.start()
			a.mu.Lock()
			switch {
			case err != nil:
				a.startErr = err
			case a.finished:
				err = errAppEnded
			default:
				a.started = true
			}
			running := a.started
			a.mu.Unlock()
			reply <- err
			if !running {
				continue
			}
			// finish stops the event loop only after done is closed; should it return
			// for another reason, it runs again: the trays still need the main thread.
			for {
				a.kit.run()
				select {
				case <-a.done:
					return a.result()
				default:
				}
			}
		}
	}
}

// finish runs the program on its goroutine and then ends the event loop.
func (a *appLoop) finish(f func() int) {
	code := f()
	a.mu.Lock()
	a.finished, a.code = true, code
	started, terminating := a.started, a.terminating
	close(a.done)
	a.mu.Unlock()
	if !started {
		return // the main goroutine is waiting for done
	}
	if terminating {
		a.kit.replyTerminate() // AppKit ends the process: willTerminate
	} else {
		a.kit.stop()
	}
	time.AfterFunc(a.grace, func() { a.exit(code) })
}

func (a *appLoop) isFinished() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.finished
}

// result is the program's status.
func (a *appLoop) result() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.finished {
		return exitAbandoned
	}
	return a.code
}

// ensure starts AppKit on the main thread unless it runs already.
func (a *appLoop) ensure() error {
	a.startMu.Lock()
	defer a.startMu.Unlock()
	a.mu.Lock()
	served, started, startErr, finished := a.served, a.started, a.startErr, a.finished
	a.mu.Unlock()
	switch {
	case started:
		return nil
	case startErr != nil:
		return startErr
	case finished:
		return errAppEnded
	case !served:
		return errMainNotServed
	}
	reply := make(chan error, 1)
	timeout := time.NewTimer(a.startTimeout)
	defer timeout.Stop()
	select {
	case a.starts <- reply:
	case <-a.done:
		return errAppEnded
	case <-timeout.C:
		return errors.New("tray: the main thread does not answer")
	}
	select {
	case err := <-reply:
		return err
	case <-timeout.C:
		return errors.New("tray: AppKit did not start in time")
	}
}

// setHandler makes h receive the commands that belong to no particular icon: a click
// on a notification (CommandOpen) and the system's request to quit (CommandQuit). h
// runs on the main thread (and, for a click that came before any handler, on the
// caller's goroutine) and must not block. The latest handler wins; remove
// unregisters it unless another one replaced it since.
func (a *appLoop) setHandler(h func(Command)) (remove func()) {
	a.mu.Lock()
	a.handlerID++
	id := a.handlerID
	a.handler = h
	// The click that launched the app arrives while it finishes launching, which may
	// be before the first tray has set its handler.
	open := a.pendingOpen && h != nil
	if open {
		a.pendingOpen = false
	}
	a.mu.Unlock()
	if open {
		h(CommandOpen)
	}
	return func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.handlerID == id {
			a.handler = nil
		}
	}
}

// route hands c to the handler; a click on a notification without one waits for the
// next handler.
func (a *appLoop) route(c Command) {
	a.mu.Lock()
	h := a.handler
	if h == nil && c == CommandOpen {
		a.pendingOpen = true
	}
	a.mu.Unlock()
	if h != nil {
		h(c)
	}
}

// shouldTerminate answers applicationShouldTerminate: on the main thread. Once the
// program has returned the app may terminate now (true); otherwise the termination
// is postponed (false: NSTerminateLater), the program is asked to stop gracefully
// (CommandQuit), and finish lets the termination proceed once it has.
func (a *appLoop) shouldTerminate() (now bool) {
	a.mu.Lock()
	if a.finished {
		a.mu.Unlock()
		return true
	}
	a.terminating = true
	h := a.handler
	a.mu.Unlock()
	if h != nil {
		h(CommandQuit)
	}
	return false
}

// willTerminate runs when AppKit is about to end the process (applicationWillTerminate:,
// main thread): the process ends with the program's status instead, once the program
// has returned (AppKit would exit with 0).
func (a *appLoop) willTerminate() {
	if !a.isFinished() {
		a.route(CommandQuit) // AppKit did not ask first (it should have)
	}
	wait := time.NewTimer(a.termWait)
	defer wait.Stop()
	select {
	case <-a.done:
	case <-wait.C:
	}
	a.exit(a.result())
}
