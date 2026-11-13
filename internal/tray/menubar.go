package tray

import (
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// The macOS menu-bar icon (docs/MACOS-SPEC.md §1: it runs in Downex Agent.app). This
// file is its logic in pure Go, so that it is tested on every system; tray_darwin.go
// and tray_darwin.m (darwin && cgo) bind it to AppKit's NSStatusItem and to
// UserNotifications.
//
// It works like the Windows tray: methods called from any goroutine queue the change
// under a lock and wake the main thread (where AppKit runs, see RunMain); drain then
// applies the queued changes to the machine on the main thread, whose shell
// operations are the backend's AppKit calls. Differences from Windows:
//   - AppKit opens the menu itself when the status item is clicked, so the machine's
//     icon events (double-click, context menu) never occur; the menu's choices arrive
//     through menuBar.dispatch.
//   - The tooltip is the status button's hover text, and its first menu line (a
//     disabled item) shows the same state as "N active · X/s" (menuStatus).
//   - Notifications are UNUserNotificationCenter notifications; clicking one sends
//     CommandOpen (appLoop.route). As on Windows they are shown only while the icon
//     is (machine.notify), because the daemon is told that the tray can ask the user
//     only then (daemon.Manager.ShowNotices).

// Menu-bar timing and bounds.
const (
	// menuBarRetryInterval is how often adding the status item is retried after a
	// failure (docs/DESKTOP-SPEC.md §3: no polling faster than 5 s).
	menuBarRetryInterval = 5 * time.Second
	// menuBarCloseTimeout bounds how long Close waits for the main thread.
	menuBarCloseTimeout = 5 * time.Second
	// menuBarMaxQueued bounds the notifications waiting for the main thread.
	menuBarMaxQueued = 8
)

// menuBarBackend is the AppKit side of a menu-bar icon (tray_darwin.go). Except for
// wake, its methods run on the main thread, inside menuBar.drain.
type menuBarBackend interface {
	// wake asks for menuBar.drain to run on the main thread soon. It is called with
	// menuBar.mu held: it must neither block nor call back into the menuBar.
	wake()
	// show creates the status item with its menu on first use, else shows it again;
	// tip is the current tooltip.
	show(tip string) error
	// hide hides the status item.
	hide() error
	// setTip changes the hover tooltip and the menu's status line.
	setTip(tip string) error
	// post shows b as a notification.
	post(b Balloon) error
	// destroy removes the status item; its menu sends no command afterwards.
	destroy()
	// release frees what callbacks from AppKit refer to (the cgo.Handle), once every
	// wake queued before it has run. Nothing of the backend is used afterwards.
	release()
}

// menuBar is a menu-bar icon: the machine on the main thread, fed by requests from
// any goroutine. The zero menuBar is not usable.
type menuBar struct {
	opts Options
	log  *slog.Logger
	be   menuBarBackend

	// Main-thread state.
	m          *machine
	retryTimer *time.Timer
	retryGen   uint64 // counts retry calls: identifies the armed timer
	destroyed  bool

	retryInterval time.Duration
	closeTimeout  time.Duration

	mu         sync.Mutex // guards the requests below
	visible    *bool
	tip        *string
	balloons   []Balloon
	calls      []func(*machine)
	closing    bool
	wakeQueued bool // a wake is pending: drain has not started since

	done      chan struct{} // closed by the final drain
	cmds      chan Command
	stop      chan struct{} // closed by close: the command worker ends
	closeOnce sync.Once
	closeErr  error
}

func newMenuBar(opts Options, be menuBarBackend) *menuBar {
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	mb := &menuBar{
		opts: opts, log: log, be: be,
		retryInterval: menuBarRetryInterval, closeTimeout: menuBarCloseTimeout,
		done: make(chan struct{}), cmds: make(chan Command, 8), stop: make(chan struct{}),
	}
	mb.m = newMachine(menuShell{mb}, opts.Tooltip, log)
	go mb.worker()
	return mb
}

func (mb *menuBar) setVisible(v bool) { mb.request(func() { mb.visible = &v }) }

func (mb *menuBar) setTooltip(tip string) { mb.request(func() { mb.tip = &tip }) }

func (mb *menuBar) notify(b Balloon) {
	mb.request(func() {
		if len(mb.balloons) == menuBarMaxQueued {
			mb.balloons = mb.balloons[1:]
		}
		mb.balloons = append(mb.balloons, b)
	})
}

// call runs f on the main thread.
func (mb *menuBar) call(f func(*machine)) {
	mb.request(func() { mb.calls = append(mb.calls, f) })
}

// request records a change under the lock and wakes the main thread. The wake is
// queued under the lock too, so that none is queued after the final drain has seen
// closing (release relies on that).
func (mb *menuBar) request(record func()) {
	mb.mu.Lock()
	defer mb.mu.Unlock()
	if mb.closing {
		return
	}
	record()
	mb.wakeLocked()
}

func (mb *menuBar) wakeLocked() {
	if !mb.wakeQueued {
		mb.wakeQueued = true
		mb.be.wake()
	}
}

// drain applies the queued requests on the main thread; after close it removes the
// status item and releases the backend.
func (mb *menuBar) drain() {
	if mb.destroyed {
		return
	}
	mb.mu.Lock()
	mb.wakeQueued = false
	visible, tip, balloons, calls, closing := mb.visible, mb.tip, mb.balloons, mb.calls, mb.closing
	mb.visible, mb.tip, mb.balloons, mb.calls = nil, nil, nil, nil
	mb.mu.Unlock()
	if tip != nil {
		mb.m.setTooltip(*tip)
	}
	if visible != nil {
		mb.m.setVisible(*visible)
	}
	for _, b := range balloons {
		mb.m.notify(b)
	}
	for _, f := range calls {
		f(mb.m)
	}
	if closing {
		mb.destroyed = true
		mb.m.close()
		mb.retry(false)
		mb.be.destroy()
		mb.be.release()
		close(mb.done)
	}
}

// close removes the icon and waits for the main thread to have done so. A command
// already handed to Options.OnCommand may still run when it returns.
func (mb *menuBar) close() error {
	mb.closeOnce.Do(func() {
		mb.mu.Lock()
		mb.closing = true
		mb.wakeLocked()
		mb.mu.Unlock()
		select {
		case <-mb.done:
		case <-time.After(mb.closeTimeout):
			mb.closeErr = errors.New("tray: the main thread did not remove the menu-bar icon in time")
		}
		close(mb.stop)
	})
	return mb.closeErr
}

// worker hands commands to Options.OnCommand one at a time.
func (mb *menuBar) worker() {
	for {
		select {
		case <-mb.stop:
			return
		case c := <-mb.cmds:
			select {
			case <-mb.stop:
				return
			default:
			}
			if mb.opts.OnCommand != nil {
				mb.opts.OnCommand(c)
			}
		}
	}
}

// dispatch queues commands for the worker without blocking (it runs on the main
// thread); when the user is faster than the commands run, the excess is dropped.
func (mb *menuBar) dispatch(cmds ...Command) {
	for _, c := range cmds {
		if c < CommandOpen || c > CommandQuit {
			continue
		}
		select {
		case mb.cmds <- c:
		default:
			mb.log.Warn("tray: command dropped, the previous ones are still running", "command", c.String())
		}
	}
}

// retry arms (or, with false, stops) the timer that retries adding the status item;
// it runs on the main thread. Unlike WM_TIMER a Go timer fires once, so a retry that
// failed again re-arms it. A timer that fired after it was stopped or replaced (its
// call was already queued) does nothing.
func (mb *menuBar) retry(on bool) {
	mb.retryGen++
	if mb.retryTimer != nil {
		mb.retryTimer.Stop()
		mb.retryTimer = nil
	}
	if !on || mb.destroyed {
		return
	}
	gen := mb.retryGen
	mb.retryTimer = time.AfterFunc(mb.retryInterval, func() {
		mb.call(func(m *machine) {
			if gen != mb.retryGen {
				return
			}
			mb.retryTimer = nil
			m.retryTimer()
			if m.retrying && gen == mb.retryGen { // failed again
				mb.retry(true)
			}
		})
	})
}

// menuShell is the machine's shell over the backend.
type menuShell struct{ mb *menuBar }

var _ shell = menuShell{}

func (s menuShell) add(tip string) error              { return s.mb.be.show(tip) }
func (s menuShell) setTip(tip string) error           { return s.mb.be.setTip(tip) }
func (s menuShell) balloon(_ string, b Balloon) error { return s.mb.be.post(b) }
func (s menuShell) remove() error                     { return s.mb.be.hide() }
func (s menuShell) retry(on bool)                     { s.mb.retry(on) }

// popupMenu never runs: AppKit opens the status item's menu by itself.
func (s menuShell) popupMenu(int32, int32) (Command, bool) { return 0, false }

// tooltipPrefix starts the tooltip (notify.Tooltip: "Downex — N active, X/s").
const tooltipPrefix = "Downex — "

// menuStatus is the menu's first line for a tooltip: "Downex — 2 active, 1.5 MB/s"
// becomes "2 active · 1.5 MB/s" (the menu is Downex's own, so the name is dropped).
// Any other text is shown as it is.
func menuStatus(tip string) string {
	rest, ok := strings.CutPrefix(tip, tooltipPrefix)
	if !ok {
		return tip
	}
	return strings.Replace(rest, " active, ", " active · ", 1)
}

// cText is s fit for a C string and for NSString's UTF-8 initialiser, which fails on
// invalid UTF-8: NUL bytes (which would end the C string) and invalid sequences are
// dropped.
func cText(s string) string {
	return strings.ToValidUTF8(strings.ReplaceAll(s, "\x00", ""), "")
}
