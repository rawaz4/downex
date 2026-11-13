// Package tray shows the daemon's notification-area icon (docs/DESKTOP-SPEC.md §3):
// an icon with a tooltip, a context menu (Open Downex, Pause all, Resume all,
// Settings…, Quit Downex), double-click to open the window, and balloon
// notifications (which Windows 10 and 11 present as toasts) that open the window
// when clicked. The icon is re-created when Explorer restarts (TaskbarCreated).
//
// Windows: tray_windows.go, Win32 through golang.org/x/sys/windows. macOS (cgo builds,
// the executable of Downex Agent.app, docs/MACOS-SPEC.md §1): a menu-bar icon
// (NSStatusItem) whose menu opens on a click and whose first, disabled line shows
// the state, and UserNotifications notifications (menubar.go, mainthread.go and
// tray_darwin.go/.m); AppKit needs the main thread, which the program hands over
// with RunMain. Elsewhere, and in darwin builds without cgo, New returns
// ErrNotSupported (Linux gets a StatusNotifierItem later).
//
// The behaviour is a small state machine (machine) over the shell operations (shell),
// so that it is tested without a desktop; the Windows backend only translates window
// messages into machine events and machine calls into Shell_NotifyIconW, the macOS
// one machine calls into AppKit.
package tray

import (
	"errors"
	"log/slog"
	"unicode/utf16"
)

// ErrNotSupported is returned by New where no tray is implemented.
var ErrNotSupported = errors.New("tray: the notification area is not supported on this system")

// Command is a user's choice from the icon: a menu item, a double-click (CommandOpen)
// or a click on a notification (CommandOpen).
type Command uint32

// Menu commands; the values are the Win32 menu item IDs.
const (
	CommandOpen Command = iota + 1
	CommandPauseAll
	CommandResumeAll
	CommandSettings
	CommandQuit
)

func (c Command) String() string {
	switch c {
	case CommandOpen:
		return "open"
	case CommandPauseAll:
		return "pause-all"
	case CommandResumeAll:
		return "resume-all"
	case CommandSettings:
		return "settings"
	case CommandQuit:
		return "quit"
	}
	return "unknown"
}

// menuItem is one entry of the context menu; a zero Command is a separator.
type menuItem struct {
	cmd   Command
	label string
}

// menu is the context menu, top to bottom (docs/DESKTOP-SPEC.md §3). The first item
// is the default (bold) one: what a double-click does.
var menu = []menuItem{
	{CommandOpen, "Open Downex"},
	{CommandPauseAll, "Pause all"},
	{CommandResumeAll, "Resume all"},
	{},
	{CommandSettings, "Settings…"},
	{CommandQuit, "Quit Downex"},
}

// Balloon is a notification shown at the icon.
type Balloon struct {
	Title string
	Text  string
	Error bool // shown with the error icon
}

// Options configure New.
type Options struct {
	// Tooltip is the initial tooltip.
	Tooltip string
	// OnCommand receives the user's commands, one at a time and in order, on a
	// goroutine of the tray (never on its window thread), so it may block. Close does
	// not wait for it: a command chosen just before Close may still run afterwards.
	OnCommand func(Command)
	// Logger receives failures of the shell calls; nil discards them.
	Logger *slog.Logger
}

// Win32 limits of NOTIFYICONDATAW's strings, in UTF-16 code units with the
// terminating NUL.
const (
	tipLen       = 128
	infoLen      = 256
	infoTitleLen = 64
)

// Icon events the machine understands: the low word of a callback message's lParam
// under NOTIFYICON_VERSION_4 (shellapi.h, winuser.h).
// Every other event (mouse moves, single clicks, balloon shown or timed out) is ignored.
const (
	evContextMenu      = 0x007B // WM_CONTEXTMENU: right click, Shift+F10, menu key
	evLButtonDblClk    = 0x0203 // WM_LBUTTONDBLCLK
	evSelect           = 0x0400 // NIN_SELECT: a single left click
	evKeySelect        = 0x0401 // NIN_KEYSELECT: Enter or Space on the focused icon
	evBalloonTimeout   = 0x0404 // NIN_BALLOONTIMEOUT
	evBalloonUserClick = 0x0405 // NIN_BALLOONUSERCLICK
)

// shell is what the machine needs from the notification area. Every method runs on
// the tray's window thread (macOS: the main thread).
type shell interface {
	// add creates the icon with tip (NIM_ADD, then NIM_SETVERSION 4).
	add(tip string) error
	// setTip changes the tooltip (NIM_MODIFY NIF_TIP).
	setTip(tip string) error
	// balloon shows b at the icon (NIM_MODIFY NIF_INFO).
	balloon(tip string, b Balloon) error
	// remove deletes the icon (NIM_DELETE).
	remove() error
	// popupMenu shows the context menu at (x, y) until the user picks an item or
	// dismisses it; ok is false when dismissed.
	popupMenu(x, y int32) (cmd Command, ok bool)
	// retry arms (or, with false, stops) a timer that calls machine.retryTimer while
	// the icon could not be added.
	retry(on bool)
}

// machine is the tray's state: whether the icon should be shown, whether it is, and
// what it shows. It is driven on the window thread only.
type machine struct {
	sh  shell
	log *slog.Logger

	want     bool // the icon should be shown
	added    bool // the icon exists
	tip      string
	pending  *Balloon // the latest notification while the icon is being added
	retrying bool
	closed   bool
}

func newMachine(sh shell, tip string, log *slog.Logger) *machine {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &machine{sh: sh, tip: tip, log: log}
}

// setVisible shows or removes the icon.
func (m *machine) setVisible(v bool) {
	if m.closed {
		return
	}
	m.want = v
	if !v {
		m.pending = nil
	}
	m.sync()
}

// setTooltip changes the tooltip; the shell is told only when it changed.
func (m *machine) setTooltip(tip string) {
	if m.closed || tip == m.tip {
		return
	}
	m.tip = tip
	if m.added {
		if err := m.sh.setTip(tip); err != nil {
			m.log.Debug("tray: set tooltip", "err", err)
		}
	}
}

// notify shows b. While the icon is wanted but not added yet (Explorer is not
// running), the latest notification waits for it; while the icon is hidden by the
// user's settings, notifications are dropped (they appear at the icon).
func (m *machine) notify(b Balloon) {
	if m.closed || !m.want {
		return
	}
	if !m.added {
		m.pending = &b
		return
	}
	if err := m.sh.balloon(m.tip, b); err != nil {
		m.log.Warn("tray: show notification", "err", err)
	}
}

// taskbarCreated handles Explorer's TaskbarCreated broadcast: the taskbar was
// (re)created, so the icon is added again (after deleting it, in case the old one
// survived, as it does when the broadcast follows a display change).
func (m *machine) taskbarCreated() {
	if m.closed {
		return
	}
	if m.added {
		_ = m.sh.remove()
		m.added = false
	}
	m.sync()
}

// retryTimer retries adding the icon.
func (m *machine) retryTimer() {
	if m.closed {
		return
	}
	m.sync()
}

// iconEvent handles a callback message of the icon and returns the commands chosen.
func (m *machine) iconEvent(event uint16, x, y int32) []Command {
	if m.closed || !m.added {
		return nil
	}
	switch event {
	case evContextMenu:
		if cmd, ok := m.sh.popupMenu(x, y); ok {
			return []Command{cmd}
		}
	case evLButtonDblClk, evKeySelect, evBalloonUserClick:
		return []Command{CommandOpen}
	}
	return nil
}

// close removes the icon; the machine ignores everything afterwards.
func (m *machine) close() {
	if m.closed {
		return
	}
	m.want = false
	m.sync()
	m.closed = true
}

// sync makes the shell match want.
func (m *machine) sync() {
	switch {
	case m.want && !m.added:
		if err := m.sh.add(m.tip); err != nil {
			if !m.retrying {
				m.log.Warn("tray: add the notification-area icon; retrying", "err", err)
				m.retrying = true
				m.sh.retry(true)
			}
			return
		}
		m.added = true
		if m.pending != nil {
			b := *m.pending
			m.pending = nil
			if err := m.sh.balloon(m.tip, b); err != nil {
				m.log.Warn("tray: show notification", "err", err)
			}
		}
	case !m.want && m.added:
		if err := m.sh.remove(); err != nil {
			m.log.Debug("tray: remove the notification-area icon", "err", err)
		}
		m.added = false
	}
	if m.retrying && (m.added || !m.want) {
		m.retrying = false
		m.sh.retry(false)
	}
}

// fillUTF16 copies s into dst as UTF-16 with a terminating NUL, truncated to fit
// without splitting a surrogate pair, and zeroes the rest of dst.
func fillUTF16(dst []uint16, s string) {
	clear(dst)
	if len(dst) == 0 {
		return
	}
	n := 0
	for _, r := range s {
		if r == 0 {
			break
		}
		w := 1
		if r >= 0x10000 {
			w = 2
		}
		if n+w > len(dst)-1 {
			break
		}
		if w == 2 {
			hi, lo := utf16.EncodeRune(r)
			dst[n], dst[n+1] = uint16(hi), uint16(lo)
		} else {
			dst[n] = uint16(r)
		}
		n += w
	}
}
