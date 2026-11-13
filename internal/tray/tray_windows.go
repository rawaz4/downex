//go:build windows

package tray

import (
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32  = windows.NewLazySystemDLL("user32.dll")
	shell32 = windows.NewLazySystemDLL("shell32.dll")

	procRegisterClassExW             = user32.NewProc("RegisterClassExW")
	procCreateWindowExW              = user32.NewProc("CreateWindowExW")
	procDestroyWindow                = user32.NewProc("DestroyWindow")
	procDefWindowProcW               = user32.NewProc("DefWindowProcW")
	procGetMessageW                  = user32.NewProc("GetMessageW")
	procTranslateMessage             = user32.NewProc("TranslateMessage")
	procDispatchMessageW             = user32.NewProc("DispatchMessageW")
	procPostMessageW                 = user32.NewProc("PostMessageW")
	procPostQuitMessage              = user32.NewProc("PostQuitMessage")
	procRegisterWindowMessageW       = user32.NewProc("RegisterWindowMessageW")
	procCreatePopupMenu              = user32.NewProc("CreatePopupMenu")
	procAppendMenuW                  = user32.NewProc("AppendMenuW")
	procSetMenuDefaultItem           = user32.NewProc("SetMenuDefaultItem")
	procTrackPopupMenuEx             = user32.NewProc("TrackPopupMenuEx")
	procDestroyMenu                  = user32.NewProc("DestroyMenu")
	procEndMenu                      = user32.NewProc("EndMenu")
	procSetForegroundWindow          = user32.NewProc("SetForegroundWindow")
	procSetTimer                     = user32.NewProc("SetTimer")
	procKillTimer                    = user32.NewProc("KillTimer")
	procCreateIconFromResourceEx     = user32.NewProc("CreateIconFromResourceEx")
	procDestroyIcon                  = user32.NewProc("DestroyIcon")
	procGetSystemMetrics             = user32.NewProc("GetSystemMetrics")
	procGetSystemMetricsForDpi       = user32.NewProc("GetSystemMetricsForDpi")
	procGetDpiForWindow              = user32.NewProc("GetDpiForWindow")
	procSetThreadDpiAwarenessContext = user32.NewProc("SetThreadDpiAwarenessContext")
	procChangeWindowMessageFilterEx  = user32.NewProc("ChangeWindowMessageFilterEx")
	procShellNotifyIconW             = shell32.NewProc("Shell_NotifyIconW")
)

// Win32 constants (winuser.h, shellapi.h).
const (
	wmNull    = 0x0000
	wmDestroy = 0x0002
	wmTimer   = 0x0113
	wmApp     = 0x8000

	wmCallback = wmApp + 1 // the icon's callback message
	wmWake     = wmApp + 2 // requests are queued for the window thread

	nimAdd        = 0
	nimModify     = 1
	nimDelete     = 2
	nimSetVersion = 4

	nifMessage  = 0x01
	nifIcon     = 0x02
	nifTip      = 0x04
	nifInfo     = 0x10
	nifShowTip  = 0x80
	niifInfo    = 0x01
	niifError   = 0x03
	niifRespect = 0x80 // NIIF_RESPECT_QUIET_TIME

	notifyIconVersion4 = 4

	mfString    = 0x0000
	mfSeparator = 0x0800

	tpmRightButton = 0x0002
	tpmRightAlign  = 0x0008
	tpmBottomAlign = 0x0020
	tpmNoNotify    = 0x0080
	tpmReturnCmd   = 0x0100

	smMenuDropAlignment = 40
	smCxSmIcon          = 49

	wsPopup        = 0x80000000
	wsExToolWindow = 0x00000080

	msgfltAllow = 1 // MSGFLT_ALLOW

	// iconID is the icon's uID; the window handle makes it unique.
	iconID = 1
	// retryTimerID is the timer that retries adding the icon.
	retryTimerID = 1
	// retryInterval is how often adding the icon is retried while Explorer is not
	// ready (docs/DESKTOP-SPEC.md §3: no polling faster than 5 s).
	retryInterval = 5000 // ms
	// closeTimeout bounds how long Close waits for the window thread.
	closeTimeout = 5 * time.Second
	// maxQueuedBalloons bounds the notifications waiting for the window thread; each
	// one replaces the previous on screen anyway.
	maxQueuedBalloons = 8

	windowClass = "DownexTrayWindow"
)

// DPI_AWARENESS_CONTEXT values.
var (
	dpiPerMonitorAwareV2 = ^uintptr(3) // -4
	dpiPerMonitorAware   = ^uintptr(2) // -3
)

// notifyIconData mirrors NOTIFYICONDATAW (Vista and later).
type notifyIconData struct {
	size            uint32
	hwnd            windows.HWND
	id              uint32
	flags           uint32
	callbackMessage uint32
	icon            windows.Handle
	tip             [tipLen]uint16
	state           uint32
	stateMask       uint32
	info            [infoLen]uint16
	version         uint32 // union with uTimeout
	infoTitle       [infoTitleLen]uint16
	infoFlags       uint32
	guidItem        windows.GUID
	balloonIcon     windows.Handle
}

// wndClassEx mirrors WNDCLASSEXW.
type wndClassEx struct {
	size       uint32
	style      uint32
	wndProc    uintptr
	clsExtra   int32
	wndExtra   int32
	instance   windows.Handle
	icon       windows.Handle
	cursor     windows.Handle
	background windows.Handle
	menuName   *uint16
	className  *uint16
	iconSm     windows.Handle
}

// msg mirrors MSG.
type msg struct {
	hwnd    windows.HWND
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      struct{ x, y int32 }
	private uint32
}

// Supported reports whether New can show an icon on this system.
func Supported() bool { return true }

// Tray is a notification-area icon owned by a hidden window on a thread of its own.
// Its methods may be called from any goroutine; they queue the change for the window
// thread and return at once. The zero Tray is not usable; a nil *Tray ignores every
// call.
type Tray struct {
	opts Options
	log  *slog.Logger
	hwnd windows.HWND // set before New returns

	// Window-thread state.
	w              *window
	m              *machine
	taskbarCreated uint32
	taskbarDue     bool // TaskbarCreated arrived while the menu was open
	retryDue       bool // the retry timer fired while the menu was open
	destroyed      bool

	mu       sync.Mutex // guards the requests below
	visible  *bool
	tip      *string
	balloons []Balloon
	calls    []func(*machine)
	closing  bool

	loopDone  chan struct{} // closed when the window thread has ended
	cmds      chan Command
	stop      chan struct{} // closed by Close: the command worker ends
	closeOnce sync.Once
	closeErr  error
}

// New creates the hidden window of a tray. The icon is not shown until SetVisible(true).
func New(opts Options) (*Tray, error) {
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	t := &Tray{opts: opts, log: log, loopDone: make(chan struct{}), cmds: make(chan Command, 8), stop: make(chan struct{})}
	ready := make(chan error, 1)
	go t.loop(ready)
	if err := <-ready; err != nil {
		return nil, err
	}
	go t.worker()
	return t, nil
}

// SetVisible shows or removes the icon (Settings.show_tray_icon).
func (t *Tray) SetVisible(v bool) {
	t.request(func() { t.visible = &v })
}

// SetTooltip sets the tooltip; it is truncated to 127 UTF-16 code units.
func (t *Tray) SetTooltip(tip string) {
	t.request(func() { t.tip = &tip })
}

// Notify shows b as a balloon at the icon (Windows 10 and 11 show it as a toast);
// clicking it sends CommandOpen. While the icon is not visible, it is dropped.
func (t *Tray) Notify(b Balloon) {
	t.request(func() {
		if len(t.balloons) == maxQueuedBalloons {
			t.balloons = t.balloons[1:]
		}
		t.balloons = append(t.balloons, b)
	})
}

// call runs f on the window thread (for tests).
func (t *Tray) call(f func(*machine)) {
	t.request(func() { t.calls = append(t.calls, f) })
}

// Close removes the icon, destroys the window and ends its thread. A command already
// handed to Options.OnCommand may still run when it returns.
func (t *Tray) Close() error {
	if t == nil {
		return nil
	}
	t.closeOnce.Do(func() {
		t.mu.Lock()
		t.closing = true
		t.mu.Unlock()
		t.wake()
		select {
		case <-t.loopDone:
		case <-time.After(closeTimeout):
			t.closeErr = errors.New("tray: the window thread did not end in time")
		}
		close(t.stop)
	})
	return t.closeErr
}

// request records a change under the lock and wakes the window thread.
func (t *Tray) request(record func()) {
	if t == nil {
		return
	}
	t.mu.Lock()
	if t.closing {
		t.mu.Unlock()
		return
	}
	record()
	t.mu.Unlock()
	t.wake()
}

func (t *Tray) wake() {
	_, _, _ = procPostMessageW.Call(uintptr(t.hwnd), wmWake, 0, 0)
}

// worker hands commands to Options.OnCommand one at a time.
func (t *Tray) worker() {
	for {
		select {
		case <-t.stop:
			return
		case c := <-t.cmds:
			select {
			case <-t.stop:
				return
			default:
			}
			if t.opts.OnCommand != nil {
				t.opts.OnCommand(c)
			}
		}
	}
}

// dispatch queues commands for the worker; when the user is faster than the commands
// run, the excess is dropped.
func (t *Tray) dispatch(cmds []Command) {
	for _, c := range cmds {
		select {
		case t.cmds <- c:
		default:
			t.log.Warn("tray: command dropped, the previous ones are still running", "command", c.String())
		}
	}
}

// Window class and procedure, shared by every tray of the process.
var (
	classOnce sync.Once
	classErr  error
	className *uint16
	instance  windows.Handle

	traysMu sync.Mutex
	trays   = map[windows.HWND]*Tray{}
)

func registerClass() error {
	classOnce.Do(func() {
		if err := windows.GetModuleHandleEx(0, nil, &instance); err != nil {
			classErr = fmt.Errorf("tray: module handle: %w", err)
			return
		}
		name, err := windows.UTF16PtrFromString(windowClass)
		if err != nil {
			classErr = err
			return
		}
		wc := wndClassEx{wndProc: windows.NewCallback(wndProc), instance: instance, className: name}
		wc.size = uint32(unsafe.Sizeof(wc))
		if r, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
			classErr = fmt.Errorf("tray: register window class: %w", err)
			return
		}
		className = name
	})
	return classErr
}

// wndProc dispatches the messages of every tray window to its Tray.
func wndProc(hwnd uintptr, message uint32, wParam, lParam uintptr) uintptr {
	traysMu.Lock()
	t := trays[windows.HWND(hwnd)]
	traysMu.Unlock()
	if t != nil {
		if r, handled := t.handle(message, wParam, lParam); handled {
			return r
		}
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, uintptr(message), wParam, lParam)
	return r
}

// loop creates the window and pumps its messages until it is destroyed.
func (t *Tray) loop(ready chan<- error) {
	defer close(t.loopDone)
	// The window belongs to this thread, which must pump its messages. The thread is
	// never unlocked, so it ends with the goroutine (and takes its DPI awareness along).
	runtime.LockOSThread()
	setThreadDPIAware()
	if err := registerClass(); err != nil {
		ready <- err
		return
	}
	// A hidden top-level window rather than a message-only one: only top-level windows
	// receive Explorer's TaskbarCreated broadcast.
	hwnd, _, err := procCreateWindowExW.Call(wsExToolWindow, uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(className)), wsPopup, 0, 0, 0, 0, 0, 0, uintptr(instance), 0)
	if hwnd == 0 {
		ready <- fmt.Errorf("tray: create window: %w", err)
		return
	}
	t.hwnd = windows.HWND(hwnd)
	if name, err := windows.UTF16PtrFromString("TaskbarCreated"); err == nil {
		r, _, _ := procRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(name)))
		t.taskbarCreated = uint32(r)
	}
	// Explorer's messages reach a process of higher integrity (--allow-elevated) only
	// when the window lets them through.
	for _, message := range []uint32{wmCallback, t.taskbarCreated} {
		if message != 0 {
			_, _, _ = procChangeWindowMessageFilterEx.Call(uintptr(t.hwnd), uintptr(message), msgfltAllow, 0)
		}
	}
	t.w = &window{hwnd: t.hwnd, log: t.log}
	t.m = newMachine(t.w, t.opts.Tooltip, t.log)
	traysMu.Lock()
	trays[t.hwnd] = t
	traysMu.Unlock()
	defer func() {
		traysMu.Lock()
		delete(trays, t.hwnd)
		traysMu.Unlock()
		t.w.destroyIcon()
	}()
	ready <- nil

	var m msg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 { // WM_QUIT or an error
			return
		}
		_, _, _ = procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		_, _, _ = procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

// handle processes a message on the window thread. While the context menu is open
// (TrackPopupMenuEx runs a modal loop that dispatches messages to this window), work
// is deferred until it closes, and a pending Close closes the menu.
func (t *Tray) handle(message uint32, wParam, lParam uintptr) (uintptr, bool) {
	switch {
	case message == wmCallback:
		if !t.w.inMenu.Load() {
			// NOTIFYICON_VERSION_4: the event is in lParam's low word, the anchor point
			// in wParam (signed x and y words).
			x, y := int32(int16(uint16(wParam))), int32(int16(uint16(wParam>>16)))
			t.dispatch(t.m.iconEvent(uint16(lParam), x, y))
			t.afterMenu()
		}
		return 0, true
	case message == wmWake:
		if t.w.inMenu.Load() {
			if t.isClosing() {
				_, _, _ = procEndMenu.Call()
			}
			return 0, true
		}
		t.drain()
		return 0, true
	case message == wmTimer && wParam == retryTimerID:
		if t.w.inMenu.Load() {
			t.retryDue = true
			return 0, true
		}
		t.m.retryTimer()
		return 0, true
	case t.taskbarCreated != 0 && message == t.taskbarCreated:
		if t.w.inMenu.Load() {
			t.taskbarDue = true
			return 0, true
		}
		t.m.taskbarCreated()
		return 0, true
	case message == wmDestroy:
		_, _, _ = procPostQuitMessage.Call(0)
		return 0, true
	}
	return 0, false
}

// afterMenu runs the work deferred while the menu was open.
func (t *Tray) afterMenu() {
	if t.taskbarDue {
		t.taskbarDue = false
		t.m.taskbarCreated()
	}
	if t.retryDue {
		t.retryDue = false
		t.m.retryTimer()
	}
	t.drain()
}

func (t *Tray) isClosing() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.closing
}

// drain applies the queued requests; after Close it removes the icon and destroys the
// window.
func (t *Tray) drain() {
	if t.destroyed {
		return
	}
	t.mu.Lock()
	visible, tip, balloons, calls, closing := t.visible, t.tip, t.balloons, t.calls, t.closing
	t.visible, t.tip, t.balloons, t.calls = nil, nil, nil, nil
	t.mu.Unlock()
	if tip != nil {
		t.m.setTooltip(*tip)
	}
	if visible != nil {
		t.m.setVisible(*visible)
	}
	for _, b := range balloons {
		t.m.notify(b)
	}
	for _, f := range calls {
		f(t.m)
	}
	if closing {
		t.destroyed = true
		t.m.close()
		t.w.retry(false)
		_, _, _ = procDestroyWindow.Call(uintptr(t.hwnd))
	}
}

// setThreadDPIAware makes the windows of this thread per-monitor DPI aware (Windows
// 10 1607 and later), so that the icon is drawn at the notification area's real size.
func setThreadDPIAware() {
	if procSetThreadDpiAwarenessContext.Find() != nil {
		return
	}
	if r, _, _ := procSetThreadDpiAwarenessContext.Call(dpiPerMonitorAwareV2); r == 0 {
		_, _, _ = procSetThreadDpiAwarenessContext.Call(dpiPerMonitorAware)
	}
}

// window implements shell with Shell_NotifyIconW on the window thread.
type window struct {
	hwnd     windows.HWND
	log      *slog.Logger
	icon     windows.Handle
	iconSize int
	inMenu   atomic.Bool // the context menu is open (read by tests)
}

var _ shell = (*window)(nil)

func (w *window) data(flags uint32) notifyIconData {
	nid := notifyIconData{hwnd: w.hwnd, id: iconID, flags: flags, callbackMessage: wmCallback}
	nid.size = uint32(unsafe.Sizeof(nid))
	return nid
}

func shellNotify(message uint32, nid *notifyIconData) error {
	r, _, err := procShellNotifyIconW.Call(uintptr(message), uintptr(unsafe.Pointer(nid)))
	if r != 0 {
		return nil
	}
	if errno, ok := err.(syscall.Errno); !ok || errno == 0 {
		err = errors.New("the shell refused the request")
	}
	return fmt.Errorf("Shell_NotifyIconW(%d): %w", message, err)
}

func (w *window) add(tip string) error {
	if err := w.ensureIcon(); err != nil {
		return err
	}
	nid := w.data(nifMessage | nifIcon | nifTip | nifShowTip)
	nid.icon = w.icon
	fillUTF16(nid.tip[:], tip)
	if err := shellNotify(nimAdd, &nid); err != nil {
		// A busy Explorer can time out NIM_ADD although it added the icon: an icon
		// that accepts NIM_MODIFY exists.
		if shellNotify(nimModify, &nid) != nil {
			return err
		}
	}
	nid.version = notifyIconVersion4
	if err := shellNotify(nimSetVersion, &nid); err != nil {
		// The callback messages differ without version 4: remove it and retry.
		_ = shellNotify(nimDelete, &nid)
		return err
	}
	return nil
}

func (w *window) setTip(tip string) error {
	nid := w.data(nifTip | nifShowTip)
	fillUTF16(nid.tip[:], tip)
	return shellNotify(nimModify, &nid)
}

func (w *window) balloon(tip string, b Balloon) error {
	nid := w.data(nifInfo | nifTip | nifShowTip)
	fillUTF16(nid.tip[:], tip)
	fillUTF16(nid.infoTitle[:], b.Title)
	text := b.Text
	if text == "" {
		text = " " // an empty text would remove the balloon instead
	}
	fillUTF16(nid.info[:], text)
	nid.infoFlags = niifInfo | niifRespect
	if b.Error {
		nid.infoFlags = niifError | niifRespect
	}
	return shellNotify(nimModify, &nid)
}

func (w *window) remove() error {
	nid := w.data(0)
	return shellNotify(nimDelete, &nid)
}

func (w *window) popupMenu(x, y int32) (Command, bool) {
	h, _, err := procCreatePopupMenu.Call()
	if h == 0 {
		w.log.Warn("tray: create menu", "err", err)
		return 0, false
	}
	defer procDestroyMenu.Call(h) //nolint:errcheck // best effort
	for _, it := range menu {
		if it.cmd == 0 {
			_, _, _ = procAppendMenuW.Call(h, mfSeparator, 0, 0)
			continue
		}
		label, err := windows.UTF16PtrFromString(it.label)
		if err != nil {
			continue
		}
		_, _, _ = procAppendMenuW.Call(h, mfString, uintptr(it.cmd), uintptr(unsafe.Pointer(label)))
	}
	_, _, _ = procSetMenuDefaultItem.Call(h, uintptr(menu[0].cmd), 0)
	flags := uintptr(tpmReturnCmd | tpmNoNotify | tpmRightButton | tpmBottomAlign)
	if r, _, _ := procGetSystemMetrics.Call(smMenuDropAlignment); r != 0 {
		flags |= tpmRightAlign
	}
	// The menu closes when the user clicks elsewhere only if its window is in the
	// foreground; WM_NULL afterwards lets a second right click work at once.
	_, _, _ = procSetForegroundWindow.Call(uintptr(w.hwnd))
	w.inMenu.Store(true)
	r, _, _ := procTrackPopupMenuEx.Call(h, flags, uintptr(x), uintptr(y), uintptr(w.hwnd), 0)
	w.inMenu.Store(false)
	_, _, _ = procPostMessageW.Call(uintptr(w.hwnd), wmNull, 0, 0)
	if r == 0 {
		return 0, false
	}
	return Command(r), true
}

func (w *window) retry(on bool) {
	if on {
		_, _, _ = procSetTimer.Call(uintptr(w.hwnd), retryTimerID, retryInterval, 0)
		return
	}
	_, _, _ = procKillTimer.Call(uintptr(w.hwnd), retryTimerID)
}

// smallIconSize is the notification area's icon size at the window's DPI.
func (w *window) smallIconSize() int {
	size := 0
	if procGetDpiForWindow.Find() == nil && procGetSystemMetricsForDpi.Find() == nil {
		if dpi, _, _ := procGetDpiForWindow.Call(uintptr(w.hwnd)); dpi != 0 {
			r, _, _ := procGetSystemMetricsForDpi.Call(smCxSmIcon, dpi)
			size = int(int32(r))
		}
	}
	if size <= 0 {
		r, _, _ := procGetSystemMetrics.Call(smCxSmIcon)
		size = int(int32(r))
	}
	return min(max(size, 16), 256)
}

// ensureIcon (re)creates the icon at the current size.
func (w *window) ensureIcon() error {
	size := w.smallIconSize()
	if w.icon != 0 && w.iconSize == size {
		return nil
	}
	bits := iconResource(size)
	h, _, err := procCreateIconFromResourceEx.Call(uintptr(unsafe.Pointer(&bits[0])), uintptr(len(bits)),
		1 /* icon */, 0x00030000 /* version */, uintptr(size), uintptr(size), 0 /* LR_DEFAULTCOLOR */)
	if h == 0 {
		return fmt.Errorf("tray: create icon: %w", err)
	}
	w.destroyIcon()
	w.icon, w.iconSize = windows.Handle(h), size
	return nil
}

func (w *window) destroyIcon() {
	if w.icon != 0 {
		_, _, _ = procDestroyIcon.Call(uintptr(w.icon))
		w.icon = 0
	}
}
