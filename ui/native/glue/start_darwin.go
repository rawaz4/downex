//go:build darwin && cgo

package glue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"downex/internal/client"
	"downex/internal/platform"
	"downex/ui/host"
	"downex/ui/mac"
)

// The launch contract on macOS (docs/MACOS-SPEC.md §5, the native window). Today's Mac
// window is one Wails process per window in two bundles: Downex.app (a regular app: Dock
// icon, menu bar) shows the main window, Downex Progress.app (LSUIElement: no Dock icon)
// each progress window. The native window keeps both identities with one host per bundle:
//
//   - the Downex.app process hosts the main window under today's main lock (Wails' flock on
//     NSTemporaryDirectory()/<id>.lock), and ends with it;
//   - the Downex Progress.app process hosts every progress window under a lock of its own
//     ("<id>-progress-host"), each window also holding its download's Wails lock, and ends
//     with its last window;
//   - outside a bundle (a development build) one process hosts every window, as on Windows.
//
// A later start of a bundle's executable hands its arguments to that bundle's host over a
// user-only unix socket (instance_unix.go) and exits 0; a start asking for the other
// bundle's window starts that bundle (Launch Services) and exits. When one of today's Wails
// windows holds a lock, the start does what Wails' own would: it posts the distributed
// notification named after the lock with the JSON hand-off (UTF-8, WorkingDirectory = the
// executable's folder), and exits. The host observes the same notifications, so a Wails
// start of an older build reaches the native window.

// DefaultWait is how long a start waits for a lock's holder to listen (the moment between a
// host's lock and its socket), before it takes the holder for an older build's window.
const DefaultWait = 2 * time.Second

// ForwardTimeout bounds a hand-off to a running host.
const ForwardTimeout = 10 * time.Second

// HostRetry is how long a start keeps trying a host that answers HostClosing.
const HostRetry = 5 * time.Second

// retryEvery is how often a start tries a closing or not yet listening host again.
const retryEvery = 25 * time.Millisecond

// maxPending bounds the hand-offs waiting for the UI thread (beyond: HostBusy).
const maxPending = 64

// Role is which windows a process hosts.
type Role int

// The roles.
const (
	// RoleAll hosts every window (outside an app bundle).
	RoleAll Role = iota
	// RoleMain hosts the main window (Downex.app).
	RoleMain
	// RoleProgress hosts the progress windows (Downex Progress.app).
	RoleProgress
)

// hostsMain and hostsProgress report the windows of r.
func (r Role) hostsMain() bool     { return r != RoleProgress }
func (r Role) hostsProgress() bool { return r != RoleMain }

// hostLockID is the lock a host of role r holds in a build whose id is id: today's main
// lock, or the progress app's own.
func hostLockID(id string, r Role) string {
	if r == RoleProgress {
		return id + "-progress-host"
	}
	return id
}

// RoleOf is the role of a process running exe as l lays it out: the progress app's
// executable hosts the progress windows, Downex.app's the main window, any other every
// window.
func RoleOf(l client.Layout) Role {
	switch {
	case l.Bundle == "":
		return RoleAll
	case l.Exe == l.Progress:
		return RoleProgress
	case l.Exe == l.Window:
		return RoleMain
	}
	return RoleAll
}

// Config is how a window process starts. The zero value of each field is the product's.
type Config struct {
	Args    []string
	Version string
	// LockID is the single-instance id ("" is LockID()).
	LockID string
	// Wait is how long a start waits for a lock's holder to listen (DefaultWait when 0).
	Wait time.Duration
	// Dir is the folder of ui-theme.json, ui-prefs.json, ui-window.json; LogDir ui.log's.
	Dir, LogDir string
	// LockDir is the folder of the locks (NSTemporaryDirectory() when ""); tests give one.
	LockDir string
	Daemon  host.Daemon
	// Role is the process's (RoleOf the executable's layout when RoleUnset).
	Role *Role
	// Exe is the running executable (os.Executable when "").
	Exe string
	// StartApp starts another bundle's executable with args (platform.StartApp when nil).
	StartApp func(exe string, args ...string) error
	// Post posts a distributed notification (mac.PostDistributed when nil); tests record.
	Post func(name, object string)
	// WebView2Dir is, on macOS, the folder of today's window's WebKit data (prefs.WebKitImport),
	// read (never changed) when the prefs file is first made; when empty ~/Library/WebKit,
	// and then only for this user's own prefs file.
	WebView2Dir string
	// NoMemoryTrim leaves out platform.TuneMemory and the idle release.
	NoMemoryTrim bool
	// NoActivate: windows never take the focus (-noactivate).
	NoActivate bool
	// OpenProgress starts a progress window of another process (host.StartProgressWindow
	// when nil): the main window's, inside a bundle.
	OpenProgress func(id int64) error
}

// Host is the window process (see the launch contract above).
type Host struct {
	LockID string
	Files  Files
	Log    *slog.Logger
	// Legacy: a progress window started while today's Wails main window holds the main lock
	// (outside a bundle only): this process shows that window only.
	Legacy bool
	// Role is which windows this process hosts.
	Role Role

	version    string
	daemon     host.Daemon
	launch     []string
	lock       *fileLock        // the host's lock
	listener   *handoffListener // its socket
	launchLock *fileLock        // a legacy host's download lock
	lockDir    string
	noActivate bool
	openOther  func(id int64) error
	exe        string
	post       func(name, object string)
	startApp   func(exe string, args ...string) error
	webview2   string

	closeLog func()
	stopTrim context.CancelFunc

	mu        sync.Mutex
	accepting bool       // hand-offs are taken (else HostClosing)
	pending   [][]string // taken hand-offs the UI thread has not opened yet
	postWake  func()     // posts drainPending to the UI thread (Run's)

	run       RunOptions
	app       *mac.App
	opening   *Window  // the window open is opening (windowMade)
	observers []func() // the host lock's Wails notifications
	windows   []*Window
	main      *Window
	progress  map[int64]*Window
	confirm   *Window // the standalone Confirm download window (one at a time)
	closed    bool
	ending    bool
}

// Start is the first thing a window process does on its main thread (see the contract).
func Start(c Config) (h *Host, handedOver bool, err error) {
	id := c.LockID
	if id == "" {
		id = LockID()
	}
	view := host.ViewOf(c.Args)
	dir := c.Dir
	if dir == "" {
		if t := host.DefaultThemePath(); t != "" {
			dir = filepath.Dir(t)
		}
	}
	logDir := c.LogDir
	if logDir == "" {
		logDir, _ = platform.LogDir()
	}
	files := FilesOf(IsPreview(id), dir, logDir)
	log, closeLog := host.OpenLog(files.LogDir, files.LogName)
	exe := c.Exe
	if exe == "" {
		exe, _ = os.Executable()
	}
	layout := client.LayoutOf("darwin", exe)
	if l, err := client.FindLayout(func() (string, error) { return exe, nil }); err == nil {
		layout = l
	}
	role := RoleOf(layout)
	if c.Role != nil {
		role = *c.Role
	}
	startApp := c.StartApp
	if startApp == nil {
		startApp = platform.StartApp
	}
	post := c.Post
	if post == nil {
		post = mac.PostDistributed
	}
	lockDir := c.LockDir
	if lockDir == "" {
		lockDir = mac.TempDir()
	}
	wait := c.Wait
	if wait <= 0 {
		wait = DefaultWait
	}
	wd := filepath.Dir(exe) // what Wails hands over as the working directory
	handed := func(err error, how string) (*Host, bool, error) {
		defer closeLog()
		if err != nil {
			log.Warn("the running window did not take the hand-off", "error", err)
			return nil, true, err
		}
		log.Info(how, "progress", view.Progress)
		return nil, true, nil
	}

	// A start for the other bundle's window starts that bundle.
	switch {
	case role == RoleMain && view.IsProgress():
		return handed(startApp(layout.Progress, c.Args...), "started the progress app")
	case role == RoleProgress && !view.IsProgress():
		return handed(startApp(layout.Window, c.Args...), "started Downex")
	}

	payload, err := handoffJSON(c.Args, wd)
	if err != nil {
		closeLog()
		return nil, false, err
	}
	sockDir, sockErr := socketDir(lockDir)
	if sockErr != nil {
		log.Warn("the hand-off folder", "error", sockErr)
	}
	hostID := hostLockID(id, role)
	sock := ""
	if sockErr == nil {
		sock = socketPath(sockDir, hostID)
	}
	retryUntil := time.Now().Add(HostRetry)
	listenUntil := time.Now().Add(wait)
	var lock *fileLock
	older := false
	for {
		l, held, lerr := claimLock(lockPath(lockDir, hostID))
		if !held {
			if lerr != nil {
				// Wails opens its window without the lock in this case; so does this host.
				log.Warn("single-instance lock", "error", lerr)
			}
			lock = l
			break
		}
		var answer uint32
		serr := errNoListener
		if sock != "" {
			answer, serr = sendHandoff(sock, host.HostHandoffKind, payload, ForwardTimeout)
		}
		switch hostReply(answer, serr) {
		case replyTaken:
			return handed(nil, "handed over to the running window")
		case replyRetry:
			limit := retryUntil
			if errors.Is(serr, errNoListener) {
				limit = listenUntil // a host between its lock and its socket, or Wails
			}
			if time.Now().Before(limit) {
				time.Sleep(retryEvery)
				continue
			}
		}
		older = true
		log.Info("the running window is an older build's", "answer", answer, "error", serr)
		break
	}
	if older {
		if !view.IsProgress() || role == RoleProgress {
			if role == RoleProgress {
				// Nothing answers on the progress app's own lock: a hung host. The window opens
				// in a process of its own under its download's lock, as today's do.
				goto legacy
			}
			post(hostID, string(payload)) // today's Wails main window shows it
			return handed(nil, "handed over to the running window")
		}
		goto legacy
	}
	h = newHost(c, id, files, log, closeLog, role)
	h.lock, h.lockDir, h.exe, h.post, h.startApp = lock, lockDir, exe, post, startApp
	if sock != "" {
		ln, err := listenHandoffs(sock, h.answer)
		if err != nil {
			log.Warn("later starts cannot hand over", "error", err)
		}
		h.listener = ln
	}
	if role.hostsMain() {
		// Starts of today's Wails builds post their hand-off to the main lock's name.
		h.observers = append(h.observers, mac.ObserveDistributed(id, h.distributed))
	}
	h.launch = c.Args
	h.startTrims(c)
	log.Info("window process started", "version", h.version, "progress", view.Progress, "preview", IsPreview(id), "role", int(role))
	return h, false, nil

legacy:
	{
		names := InstanceID(id, view)
		pl, held, perr := claimLock(lockPath(lockDir, names))
		if held {
			post(names, string(payload))
			return handed(nil, "handed over to the running progress window")
		}
		if perr != nil {
			log.Warn("single-instance lock", "error", perr)
		}
		h = newHost(c, id, files, log, closeLog, role)
		h.Legacy, h.launchLock = true, pl
		h.lockDir, h.exe, h.post, h.startApp = lockDir, exe, post, startApp
		h.launch = c.Args
		h.startTrims(c)
		log.Info("window process started", "version", h.version, "progress", view.Progress, "legacy", true)
		return h, false, nil
	}
}

func newHost(c Config, id string, files Files, log *slog.Logger, closeLog func(), role Role) *Host {
	d := c.Daemon
	if d == nil {
		d = host.NewClientDaemon(client.Default())
	}
	open := c.OpenProgress
	if open == nil {
		open = host.StartProgressWindow
	}
	version := c.Version
	if version == "" {
		version = "dev"
	}
	return &Host{
		LockID: id, Files: files, Log: log, Role: role, version: version, daemon: d,
		noActivate: c.NoActivate, openOther: open, closeLog: closeLog, webview2: c.WebView2Dir,
		progress: map[int64]*Window{}, accepting: true,
	}
}

func (h *Host) startTrims(c Config) {
	if c.NoMemoryTrim {
		return
	}
	platform.TuneMemory()
	// A window process holds no downloads: what it freed goes back to the system each minute
	// (today's main.go). Windows' working-set trims have no macOS counterpart.
	ctx, cancel := context.WithCancel(context.Background())
	h.stopTrim = cancel
	go platform.ReleaseMemoryWhileIdle(ctx, time.Minute, func() bool { return false })
}

// answer is the socket's answer to a start's hand-off (on the listener's goroutine): taken
// while the host has windows to run, HostClosing once its last window has closed, HostBusy
// while too many wait.
func (h *Host) answer(kind uint32, payload []byte) uint32 {
	if kind != host.HostHandoffKind {
		return 1
	}
	d, err := parseHandoffJSON(payload)
	if err != nil {
		h.Log.Warn("a later start's hand-off", "error", err)
		return 1
	}
	h.mu.Lock()
	switch {
	case !h.accepting:
		h.mu.Unlock()
		return host.HostClosing
	case len(h.pending) >= maxPending:
		h.mu.Unlock()
		return host.HostBusy
	}
	h.pending = append(h.pending, d.Args)
	wake := h.postWake
	h.mu.Unlock()
	h.Log.Info("a later start handed over", "arguments", len(d.Args))
	if wake != nil {
		wake()
	}
	return host.HostAnswer
}

// distributed is a Wails start's hand-off (an older build's), on the main thread.
func (h *Host) distributed(object string) {
	d, err := parseHandoffJSON([]byte(object))
	if err != nil {
		h.Log.Warn("an older start's hand-off", "error", err)
		return
	}
	h.Log.Info("an older build's start handed over", "arguments", len(d.Args))
	if h.app == nil {
		h.mu.Lock()
		h.pending = append(h.pending, d.Args)
		h.mu.Unlock()
		return
	}
	h.Request(d.Args)
}

// drainPending opens what the taken hand-offs ask for (UI thread).
func (h *Host) drainPending() {
	h.mu.Lock()
	p := h.pending
	h.pending = nil
	h.mu.Unlock()
	for _, args := range p {
		h.Request(args)
	}
}

// ErrClosed is the answer of a closed Host or Window.
var ErrClosed = errors.New("glue: closed")

// Close ends the process's glue once Run has returned (or instead of it).
func (h *Host) Close() {
	if h == nil || h.closed {
		return
	}
	h.closed = true
	for len(h.windows) > 0 {
		h.windows[len(h.windows)-1].end()
	}
	h.stopListening()
	h.launchLock.Close()
	if h.stopTrim != nil {
		h.stopTrim()
	}
	if h.closeLog != nil {
		h.closeLog()
		h.closeLog = nil
	}
}

// stopListening lets the host's lock, socket and notifications go: a later start becomes the
// next host at once.
func (h *Host) stopListening() {
	h.mu.Lock()
	h.accepting = false
	h.mu.Unlock()
	h.listener.Close()
	h.listener = nil
	for _, f := range h.observers {
		f()
	}
	h.observers = nil
	h.lock.Close()
	h.lock = nil
}

func errorf(format string, a ...any) error { return fmt.Errorf("glue: "+format, a...) }
