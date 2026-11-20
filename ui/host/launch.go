package host

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode/utf16"

	"connectrpc.com/connect"

	"downex/internal/client"
	"downex/internal/platform"
)

// The launch contract of a window process (docs/DESKTOP-SPEC.md §1, §7), as today's
// window keeps it (ui/progress.go, ui/app.go): "downex-ui" is the main window,
// "downex-ui --progress <id>" the progress window of download <id> in a process of its
// own, and "--confirm <id>" (downexd's request to confirm a browser hand-off) is shown by
// the main window. downexd, the tray, the browser hand-offs and the installer start the
// same file with the same arguments (internal/e2e checks them).

// MainInstanceID names the per-user single-instance lock of the main window: starting
// Downex while its window is open brings that window to the front instead of opening
// another one.
const MainInstanceID = "downex-ui-6f0d2c1e-4b7a-4e58-9a3d-2c8b7e51f904"

const (
	// ProgressFlag is the command-line option of a progress window: "--progress <id>".
	ProgressFlag = "progress"
	// ConfirmFlag is the command-line option that asks the main window to show the
	// confirmation dialog of a download awaiting confirmation: "--confirm <id>".
	ConfirmFlag = "confirm"
	// RenewFlag is the command-line option that asks the main window to show the New
	// link dialog of a download whose link expired: "--renew <id>" (docs/DESKTOP-SPEC.md
	// §10; a progress window's "New link…" starts the main window with it).
	RenewFlag = "renew"
	// RaiseFlag asks the window of a download the user just started in the browser (its
	// progress window, or its standalone Confirm download window: "--confirm <id> --raise")
	// to come above the browser, even when the system does not grant it the foreground:
	// "--raise" (downexd's Config.ShowBrowserAdd; docs/DESKTOP-SPEC.md §7 "Above the
	// browser").
	RaiseFlag = "raise"
)

// RaiseArg reports a "--raise" (or "-raise") option in args (without the program name).
func RaiseArg(args []string) bool {
	return slices.ContainsFunc(args, func(a string) bool { return a == "--"+RaiseFlag || a == "-"+RaiseFlag })
}

// The progress window's sizes (DESKTOP-SPEC §7), in DIPs. It grows to its expanded
// height when the user shows the details, and shrinks back.
const (
	ProgressWidth     = 520
	ProgressHeight    = 300 // collapsed; 560 with the details
	ProgressMinWidth  = 480
	ProgressMinHeight = 260
)

// The main window's default and minimum sizes, in DIPs.
const (
	MainWidth     = 1200
	MainHeight    = 760
	MainMinWidth  = 900
	MainMinHeight = 600
)

// View is what one window shows: the main window (Progress and Confirm 0), the progress
// window of download Progress, or the standalone Confirm download window that asks first
// about download Confirm (docs/DESKTOP-SPEC.md §1).
type View struct {
	Progress int64
	Confirm  int64
}

// IsProgress reports a progress window.
func (v View) IsProgress() bool { return v.Progress > 0 }

// IsConfirm reports the standalone Confirm download window.
func (v View) IsConfirm() bool { return v.Confirm > 0 && v.Progress <= 0 }

// IsMain reports the main window.
func (v View) IsMain() bool { return !v.IsProgress() && !v.IsConfirm() }

// ViewOf returns the view that the command-line arguments args (without the program
// name) ask for: the progress window of the first valid "--progress <id>"; the standalone
// Confirm download window for "--confirm <id> --raise" (a download the user just started in
// the browser: downexd's Config.ShowBrowserAdd; the main window is not shown); else the main
// window (which shows the "--confirm <id>" of a start without "--raise" in its own dialog).
func ViewOf(args []string) View {
	if ids := IDArgs(args, ProgressFlag); len(ids) > 0 {
		return View{Progress: ids[0]}
	}
	if ids := ConfirmArgs(args); len(ids) > 0 && RaiseArg(args) {
		return View{Confirm: ids[0]}
	}
	return View{}
}

// ConfirmArgs returns the download ids of the "--confirm <id>" and "--confirm=<id>"
// options (one dash works too) in args, in order and without repeats. Other arguments,
// and ids that are not positive decimal integers, are ignored.
func ConfirmArgs(args []string) []int64 { return IDArgs(args, ConfirmFlag) }

// RenewArgs returns the download ids of the "--renew <id>" options in args, as ConfirmArgs.
func RenewArgs(args []string) []int64 { return IDArgs(args, RenewFlag) }

// IDArgs returns the download ids of the "--<flag> <id>" and "--<flag>=<id>" options
// (one dash works too) in args, in order and without repeats. Other arguments, and ids
// that are not positive decimal integers, are ignored.
func IDArgs(args []string, flag string) []int64 {
	var ids []int64
	for i := 0; i < len(args); i++ {
		name, value, hasValue := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(args[i], "-"), "-"), "=")
		if !strings.HasPrefix(args[i], "-") || name != flag {
			continue
		}
		if !hasValue {
			if i+1 >= len(args) {
				break
			}
			i++
			value = args[i]
		}
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id <= 0 || slices.Contains(ids, id) {
			continue
		}
		ids = append(ids, id)
	}
	return ids
}

// ProgressInstanceID is the single-instance id of the progress window of download id,
// derived from the main one: opening the progress of a download whose window is open
// brings that window to the front, and the main window and the progress windows of other
// downloads stay independent of it.
func ProgressInstanceID(id int64) string {
	return MainInstanceID + "-progress-" + strconv.FormatInt(id, 10)
}

// ConfirmInstanceID names the standalone Confirm download window (it has no lock of its own:
// the window host shows it, one at a time).
const ConfirmInstanceID = MainInstanceID + "-confirm"

// InstanceID is the single-instance id of v's window.
func (v View) InstanceID() string {
	switch {
	case v.IsProgress():
		return ProgressInstanceID(v.Progress)
	case v.IsConfirm():
		return ConfirmInstanceID
	}
	return MainInstanceID
}

// The standalone Confirm download window's sizes, in DIPs: it opens as the dialog of a file
// asks (44rem wide, its height at that width), so that the common dialog shows without a
// resize, and fits the dialog it shows (ui/views/confirmwin).
const (
	ConfirmWidth     = 704
	ConfirmHeight    = 439
	ConfirmMinWidth  = 480
	ConfirmMinHeight = 300
)

// RGB is an opaque colour.
type RGB struct{ R, G, B uint8 }

// COLORREF is c as a Windows COLORREF (0x00BBGGRR).
func (c RGB) COLORREF() uint32 { return uint32(c.R) | uint32(c.G)<<8 | uint32(c.B)<<16 }

// The pages' grounds (docs/DESIGN-SYSTEM.md §2.1), so that a window opens in the colour it
// then paints: the main window's --chrome (title bar, sidebar, status bar), the progress
// window's --surface and the Confirm download window's --raised (the dialog filling it),
// each in the light and the dark theme.
var (
	ChromeLight  = RGB{0xee, 0xf2, 0xf0}
	ChromeDark   = RGB{0x0a, 0x0e, 0x0c}
	SurfaceLight = RGB{0xff, 0xff, 0xff}
	SurfaceDark  = RGB{0x17, 0x1e, 0x1b}
	RaisedLight  = RGB{0xff, 0xff, 0xff}
	RaisedDark   = RGB{0x21, 0x2a, 0x26}
)

// WindowSpec is the window of a view, and its single-instance lock.
type WindowSpec struct {
	Title               string
	Width, Height       int // DIPs
	MinWidth, MinHeight int // DIPs
	InstanceID          string
	// Frameless: the window has no system title bar and draws its own with the window
	// controls (docs/DESIGN-SYSTEM.md §4.2, §6.2). Only the main window on Windows:
	// progress windows and macOS keep the system frame.
	Frameless bool
	// Light and Dark are the background in the light and the dark theme.
	Light, Dark RGB
}

// Window returns the window of v on this system.
func (v View) Window() WindowSpec { return v.WindowOn(runtime.GOOS) }

// WindowOn returns the window of v on system goos.
func (v View) WindowOn(goos string) WindowSpec {
	if v.IsConfirm() {
		return WindowSpec{
			Title: "Confirm download",
			Width: ConfirmWidth, Height: ConfirmHeight,
			MinWidth: ConfirmMinWidth, MinHeight: ConfirmMinHeight,
			InstanceID: ConfirmInstanceID,
			Light:      RaisedLight, Dark: RaisedDark,
		}
	}
	if v.IsProgress() {
		return WindowSpec{
			Title: "Downex",
			Width: ProgressWidth, Height: ProgressHeight,
			MinWidth: ProgressMinWidth, MinHeight: ProgressMinHeight,
			InstanceID: ProgressInstanceID(v.Progress),
			Light:      SurfaceLight, Dark: SurfaceDark,
		}
	}
	return WindowSpec{
		Title: "Downex",
		Width: MainWidth, Height: MainHeight,
		MinWidth: MainMinWidth, MinHeight: MainMinHeight,
		InstanceID: MainInstanceID,
		Frameless:  goos == "windows",
		Light:      ChromeLight, Dark: ChromeDark,
	}
}

// Background is the window's colour before it paints, dark or not.
func (s WindowSpec) Background(dark bool) RGB {
	if dark {
		return s.Dark
	}
	return s.Light
}

// Capabilities is what this system's window can do (today's Startup binding).
type Capabilities struct {
	// OpenWith: Shell.OpenWith can show the system's "Open with" dialog; without it the
	// window does not offer Open with….
	OpenWith bool
	// Hibernate: the system can hibernate after a queue (docs/M6-SPEC.md §1; macOS cannot,
	// and downexd refuses it there): the queue editor offers it only then.
	Hibernate bool
	// WakeTimers: a queue's schedule can wake the computer for its start (Windows
	// waitable timers): the schedule editor offers "Wake the computer" only then.
	WakeTimers bool
	// CustomTitleBar: the window is frameless (WindowSpec.Frameless) and draws the title
	// bar with the minimise, maximise and close buttons.
	CustomTitleBar bool
}

// Capabilities returns what v's window can do on this system.
func (v View) Capabilities() Capabilities {
	return Capabilities{
		OpenWith:       CanOpenWith,
		Hibernate:      CanHibernate(runtime.GOOS),
		WakeTimers:     HasWakeTimers(runtime.GOOS),
		CustomTitleBar: v.Window().Frameless,
	}
}

// CanHibernate reports whether downexd can hibernate the system goos after a queue.
func CanHibernate(goos string) bool { return goos != "darwin" }

// HasWakeTimers reports whether a queue's schedule can wake system goos from sleep.
func HasWakeTimers(goos string) bool { return goos == "windows" }

// OpenProgressWindow opens the progress window of download id (DESKTOP-SPEC §7): it starts
// "<this program> --progress <id>" (StartProgressWindow), which hands it to the running
// window process (or, with today's Wails window, opens a process of its own). When that
// download's window is open already, it comes to the front.
// An id that is not positive is invalid_argument, a failed start internal. log may be nil.
func OpenProgressWindow(id int64, log *slog.Logger) error {
	return openProgressWindow(id, StartProgressWindow, log)
}

func openProgressWindow(id int64, start func(int64) error, log *slog.Logger) error {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if id <= 0 {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("no such download: %d", id))
	}
	if err := start(id); err != nil {
		log.Warn("cannot open a progress window", "download", id, "error", err)
		return connect.NewError(connect.CodeInternal, fmt.Errorf("cannot open the progress window: %w", err))
	}
	log.Info("progress window requested", "download", id)
	return nil
}

// StartProgressWindow starts "<this program> --progress <id>" detached, with its window
// shown (platform.StartApp). This process is in the foreground (the user just asked for
// the window), and it lets the new one take the foreground too: when the window is open
// already, the new process hands over to it, which then comes to the front. On macOS,
// inside Downex.app, the program is the same one in Downex Progress.app, whose windows
// have no Dock icon of their own (ProgressProgram).
func StartProgressWindow(id int64) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate this program: %w", err)
	}
	if l, err := client.FindLayout(func() (string, error) { return exe, nil }); err == nil {
		exe = ProgressProgram(l, exe, regularFile)
	}
	AllowForegroundHandoff()
	return platform.StartApp(exe, "--"+ProgressFlag, strconv.FormatInt(id, 10))
}

// ProgressProgram returns the program that shows the progress windows of this one (exe,
// installed as l says): inside the macOS app bundle the progress app's executable
// (client.Layout.Progress, docs/MACOS-SPEC.md §1) when isFile finds it, which is the
// progress app itself there; otherwise exe (Windows, Linux, a development build, and a
// bundle without the progress app, whose windows then show in the Dock).
func ProgressProgram(l client.Layout, exe string, isFile func(string) bool) string {
	if l.Bundle != "" && isFile(l.Progress) {
		return l.Progress
	}
	return exe
}

func regularFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

// ---- second start ---------------------------------------------------------------------

// A second start of a window whose lock is held hands its arguments to the running one
// and exits 0. Today's windows (Wails v2.17 on Windows) do this with a named mutex, a
// message-only window and WM_COPYDATA; the native window speaks the same protocol, so an
// old and a new downex-ui.exe hand over to each other during an upgrade. The Win32 side
// lives in the window backend; these are its names and its payload.

// HandoffDataKind is the WM_COPYDATA dwData of a hand-off.
const HandoffDataKind = 1542

// One process hosts every native window (the main window and each progress window): a
// start finds the host by the main window's lock and hands it its arguments with the same
// payload, under dwData HostHandoffKind. Only a host takes that kind, and it answers
// HostAnswer; today's windows (Wails v2.17, which answers every WM_COPYDATA 0, and the
// 0.9.2 native window, which answers 0 to a kind it does not know) leave it alone, so the
// start knows it reached an older build without changing anything there, and then does
// what that build expects (HandoffDataKind to its main window, or a progress window of its
// own under that download's lock). A host whose last window has closed answers
// HostClosing (and HostBusy while too many hand-offs wait): the start tries again, and
// becomes the host once the old one has gone.
const (
	HostHandoffKind = 1543
	HostAnswer      = 0x44584831 // "DXH1"
	HostClosing     = 0x44584830 // "DXH0"
	HostBusy        = 0x44584832 // "DXH2"
)

// HandoffNames are the kernel and window names of the lock of instance id.
type HandoffNames struct {
	Mutex  string // the named mutex that a running window holds
	Class  string // the class of its message-only window
	Window string // that window's title
}

// HandoffNamesOf returns the names of the lock of instance id (MainInstanceID, or a
// progress window's ProgressInstanceID).
func HandoffNamesOf(id string) HandoffNames {
	base := "wails-app-" + id
	return HandoffNames{Mutex: base + "sim", Class: base + "-sic", Window: base + "-siw"}
}

// Handoff is what a second start hands over: its arguments (without the program name)
// and its working directory, as the JSON object {"Args":[…],"WorkingDirectory":"…"}.
type Handoff struct {
	Args             []string
	WorkingDirectory string
}

// MaxHandoff bounds the WM_COPYDATA payload of a hand-off, in bytes; command lines are far
// shorter (Windows limits one to 32,767 UTF-16 units).
const MaxHandoff = 1 << 20

// EncodeHandoff returns the WM_COPYDATA payload (lpData, cbData = its length) of h: its
// JSON as little-endian UTF-16 code units ending with a NUL unit. Wails v2.17's receiver
// reads the payload up to that NUL. (Wails' own sender claims one byte more than it
// wrote; DecodeHandoff reads both.)
func EncodeHandoff(h Handoff) ([]byte, error) {
	j, err := json.Marshal(h) // valid UTF-8 with every control character escaped: no NUL inside
	if err != nil {
		return nil, err
	}
	units := utf16.Encode([]rune(string(j)))
	if 2*(len(units)+1) > MaxHandoff {
		return nil, errors.New("the hand-off is too long")
	}
	b := make([]byte, 0, 2*(len(units)+1))
	for _, u := range units {
		b = binary.LittleEndian.AppendUint16(b, u)
	}
	return append(b, 0, 0), nil
}

// DecodeHandoff parses the WM_COPYDATA payload of a hand-off (the cbData bytes at lpData):
// little-endian UTF-16 JSON up to the first NUL unit or the end of b, whichever comes first.
// An odd last byte (Wails' sender claims one more than it wrote) is ignored, unpaired
// surrogates read as U+FFFD, and unknown JSON fields are ignored.
func DecodeHandoff(b []byte) (Handoff, error) {
	if len(b) > MaxHandoff {
		return Handoff{}, errors.New("the hand-off is too long")
	}
	units := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u := binary.LittleEndian.Uint16(b[i:])
		if u == 0 {
			break
		}
		units = append(units, u)
	}
	var h Handoff
	if err := json.Unmarshal([]byte(string(utf16.Decode(units))), &h); err != nil {
		return Handoff{}, fmt.Errorf("the hand-off is not readable: %w", err)
	}
	return h, nil
}

// ---- confirmation requests ------------------------------------------------------------

// Confirmations carries "--confirm <id>" requests to the main window's confirmation
// dialogs (or to the standalone Confirm download window's queue): those of its own command
// line, and those that later starts hand over. Until the
// window is ready they wait; the window also shows every download awaiting confirmation
// that it lists, so a request lost to a restart costs nothing. A progress window shows no
// confirmation dialogs: it ignores them.
type Confirmations struct {
	view View
	log  *slog.Logger

	mu      sync.Mutex
	show    func(ids []int64)
	pending []int64
	// The "--renew <id>" requests (docs/DESKTOP-SPEC.md §10), kept the same way.
	renewShow    func(ids []int64)
	renewPending []int64
}

// NewConfirmations returns the requests of the window of v. log may be nil.
func NewConfirmations(v View, log *slog.Logger) *Confirmations {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Confirmations{view: v, log: log}
}

// Request asks for the confirmation dialogs of ids, in order: at once when the window is
// ready, else once it is.
func (c *Confirmations) Request(ids []int64) {
	if c.view.IsProgress() {
		return
	}
	c.mu.Lock()
	c.pending = append(c.pending, ids...)
	show := c.show
	if show == nil || len(c.pending) == 0 {
		c.mu.Unlock()
		return
	}
	send := c.pending
	c.pending = nil
	c.mu.Unlock()
	show(send)
	c.log.Info("confirmation requested", "downloads", len(send))
}

// Ready says that the window shows confirmation dialogs now, with show (called on the
// caller's goroutine of Request or Ready, never with an empty list): the requests that
// waited go to it at once. A later Ready replaces show; nothing is sent twice.
func (c *Confirmations) Ready(show func(ids []int64)) {
	c.mu.Lock()
	c.show = show
	c.mu.Unlock()
	c.Request(nil)
}

// SecondInstance handles a later start of this window: it brings the window to the front
// (focus) and asks for the confirmation dialogs and New link dialogs of that start's
// arguments (args without the program name).
func (c *Confirmations) SecondInstance(args []string, focus func()) {
	if focus != nil {
		focus()
	}
	c.Request(ConfirmArgs(args))
	c.RequestRenew(RenewArgs(args))
}

// RequestRenew asks for the New link dialogs of ids: at once when the window is ready
// (ReadyRenew), else once it is. A progress window ignores them.
func (c *Confirmations) RequestRenew(ids []int64) {
	if c.view.IsProgress() {
		return
	}
	c.mu.Lock()
	c.renewPending = append(c.renewPending, ids...)
	show := c.renewShow
	if show == nil || len(c.renewPending) == 0 {
		c.mu.Unlock()
		return
	}
	send := c.renewPending
	c.renewPending = nil
	c.mu.Unlock()
	show(send)
	c.log.Info("new link requested", "downloads", len(send))
}

// ReadyRenew says that the window shows New link dialogs now, with show.
func (c *Confirmations) ReadyRenew(show func(ids []int64)) {
	c.mu.Lock()
	c.renewShow = show
	c.mu.Unlock()
	c.RequestRenew(nil)
}

// StartMainRenew starts the main window with "--renew <id>" (a progress window's "New
// link…", docs/DESKTOP-SPEC.md §10): an open main window takes the request over (the
// window host on Windows, Downex.app's instance on macOS) and shows the New link dialog.
func StartMainRenew(id int64) error {
	if id <= 0 {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("no such download: %d", id))
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate this program: %w", err)
	}
	if l, err := client.FindLayout(func() (string, error) { return exe, nil }); err == nil && l.Bundle != "" && regularFile(l.Window) {
		exe = l.Window
	}
	AllowForegroundHandoff()
	return platform.StartApp(exe, "--"+RenewFlag, strconv.FormatInt(id, 10))
}
