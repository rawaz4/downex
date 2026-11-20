//go:build windows

package glue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync/atomic"
	"time"

	"downex/internal/client"
	"downex/internal/platform"
	"downex/ui/host"
	"downex/ui/win"

	"golang.org/x/sys/windows"
)

// DefaultWait is how long a start looks for the running window's hand-off window when the
// lock's mutex is held: the moment between a first start's mutex and its listener (Start
// listens at once, so microseconds), or a window that is closing.
const DefaultWait = 2 * time.Second

// ForwardTimeout bounds the hand-off to a running window (Wails v2.17 waits for ever: a hung
// window would keep the browser's or the tray's start waiting).
const ForwardTimeout = 10 * time.Second

// HostRetry is how long a start keeps trying to reach a host that answers HostClosing (its
// last window has just closed): once the host has gone, the start becomes the host itself.
const HostRetry = 5 * time.Second

// retryEvery is how often a start tries a closing host again.
const retryEvery = 25 * time.Millisecond

// Config is how a window process starts. The zero value of each field is the product's.
type Config struct {
	// Args is the command line without the program name (os.Args[1:]).
	Args []string
	// Version is this build's version (main.version).
	Version string
	// LockID is the single-instance id ("" is LockID(), the build's). Tests give their own.
	LockID string
	// Wait is how long a later start looks for the running window (DefaultWait when 0).
	Wait time.Duration
	// Dir is the folder of ui-theme.json, ui-prefs.json and ui-window.json (the folder of
	// host.DefaultThemePath when ""); LogDir is ui.log's (platform.LogDir when "").
	Dir, LogDir string
	// Daemon is downexd (nil: host's client daemon, which makes the first contact and starts
	// downexd when it is not running, unless this process is elevated). Every window of the
	// process shares it.
	Daemon host.Daemon
	// Getwd is the working directory a hand-off carries (os.Getwd when nil).
	Getwd func() (string, error)
	// AllowForeground lets the running window come to the front (host.AllowForegroundHandoff
	// when nil); it runs first, as today's window does.
	AllowForeground func()
	// WebView2Dir is today's window's WebView2 profile, read (never changed) when the prefs
	// file is made (webstorage.DefaultDir() when ""); tests give a folder of their own.
	WebView2Dir string
	// NoMemoryTrim leaves out today's memory settings (platform.TuneMemory and the idle
	// release each minute) and the start-up trim; tests set it.
	NoMemoryTrim bool
	// StartupTrim is when the start-up trim runs (StartupTrimAfter when 0).
	StartupTrim time.Duration
	// NoActivate: the host's windows never take the foreground, not even when a later start
	// asks for them (the -noactivate measuring flag; tests).
	NoActivate bool
	// OpenProgress starts the progress window of a download from a window of a process that
	// is not the host (a progress window of its own beside today's Wails window):
	// host.StartProgressWindow when nil. The host opens its own progress windows itself.
	OpenProgress func(id int64) error
}

// StartupTrimAfter is when a window process gives back, once, the pages its start-up
// touched and no longer uses (the working set emptied: SetProcessWorkingSetSizeEx with
// both sizes -1; the pages it still uses come back as it touches them). FirstFrameTrimAfter
// is an earlier one, after each window's first frame (Window.FirstFrame).
//
// What they take back (measured 2026-11-05, the main window with downexd, private working set
// over its first 18 s, three runs each): a warm start holds 20.5 MB until the 20 s trim, 9.6-
// 10.0 MB with the first-frame trim; the first start of a never-run exe path holds 52.6 MB,
// 42.0-42.2 MB with it. That first start's extra 32 MB is the window's own exe image (its
// pages private on the first run of each exe path, the same with the software renderer, so
// not the graphics driver), touched again from outside the window's threads for about 10 s
// after the window shows: the first-frame trim gives back the rest, and the 20 s trim (2.9 MB
// idle, about 10 MB with a list and the speeds) the image's pages as well. Neither delays the
// first frame (162-256 ms with both, 175-304 ms without). Those 32 MB are the zero-filled
// end of the image's .data read in from outside (imagetail_windows.go): the start-up watch
// gives them back as they come in, so a first start no longer holds them until the 20 s trim.
const (
	StartupTrimAfter    = 20 * time.Second
	FirstFrameTrimAfter = 250 * time.Millisecond
)

// trimWorkingSet empties this process's working set (a variable so that tests count trims).
var trimWorkingSet = platform.TrimWorkingSet

// Host is the window process: it holds the main window's single-instance lock, and opens and
// closes every window of the process on its UI thread (Run): the main window and each
// download's progress window, as its own start and later starts ask (see Start).
//
// A legacy host is the one exception: a progress window started while one of today's
// windows (Wails, or the 0.9.2 native window) holds the main lock. It holds only that
// download's lock, as today's progress processes do, and shows only that window.
type Host struct {
	// LockID is the build's single-instance id.
	LockID string
	// Files are the per-user files (the preview's own in a preview build).
	Files Files
	// Log is ui.log (the preview's own log in a preview build).
	Log *slog.Logger
	// Legacy: see Host.
	Legacy bool

	version    string
	daemon     host.Daemon
	launch     []string
	launchLock *win.Instance // a legacy host's progress lock, for its window
	instance   *win.Instance // the main lock (nil in a legacy host)
	webview2   string
	getwd      func() (string, error)
	noActivate bool
	openOther  func(id int64) error // a legacy host's OpenProgress

	closeLog func()
	stopTrim context.CancelFunc
	trimCtx  context.Context // nil: no memory trims (Config.NoMemoryTrim)
	trimLog  *slog.Logger

	run       RunOptions
	app       atomic.Pointer[win.App] // Run's, read from any goroutine (OpenProgress)
	opening   *Window                 // the window open is opening (windowMade)
	accepting bool                    // a host hand-off is taken (HostAnswer), not refused (HostClosing)
	windows   []*Window
	main      *Window
	progress  map[int64]*Window
	confirm   *Window // the standalone Confirm download window (one at a time)
	closed    bool
}

// Start is the first thing a window process does, on its UI thread (the main thread, locked
// to it), before any window: it lets the running window come to the front, opens ui.log,
// and hands its arguments to the running host, or becomes the host.
//
//   - A host holds the main window's lock: the start sends it its arguments
//     (host.HostHandoffKind) and reports handedOver once the host answered host.HostAnswer;
//     the process then exits 0. A host that is closing answers host.HostClosing, and the
//     start tries again (HostRetry) until it can become the host.
//   - Today's window (Wails, or 0.9.2's native window) holds the main lock and leaves the
//     host's kind alone: the start does what today's would. "--progress <id>" takes that
//     download's own lock (handing over to its window when it is open; otherwise the process
//     is a legacy Host with that window only); any other start hands its arguments to the
//     main window as today's do (host.HandoffDataKind).
//   - Nobody holds the lock: this process is the host, whatever its arguments. It listens
//     for later starts at once (their hand-offs wait for Run) and returns.
//
// Nothing contacts downexd until a window's first call or Session.Start.
func Start(c Config) (h *Host, handedOver bool, err error) {
	allow := c.AllowForeground
	if allow == nil {
		allow = host.AllowForegroundHandoff
	}
	allow()
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
		logDir, _ = platform.LogDir() // none: messages are discarded
	}
	files := FilesOf(IsPreview(id), dir, logDir)
	log, closeLog := host.OpenLog(files.LogDir, files.LogName)
	wait := c.Wait
	if wait <= 0 {
		wait = DefaultWait
	}
	getwd := c.Getwd
	if getwd == nil {
		getwd = os.Getwd
	}
	handed := func(err error, how string) (*Host, bool, error) {
		defer closeLog()
		if err != nil {
			log.Warn("the running window did not take the hand-off", "error", err)
			return nil, true, err
		}
		log.Info(how, "progress", view.Progress)
		return nil, true, nil
	}

	mainNames := HandoffNames(id, host.View{})
	deadline := time.Now().Add(HostRetry)
	var inst *win.Instance
	for {
		got, running, lockErr := win.ClaimInstance(win.InstanceNames(mainNames), wait)
		if !running.OK() {
			if lockErr != nil {
				// Wails opens its window without the lock in this case; so does this host.
				log.Warn("single-instance lock", "error", lockErr)
			}
			inst = got
			break
		}
		answer, err := send(running, host.HostHandoffKind, c.Args, getwd)
		switch hostReply(answer, err, running.Alive()) {
		case replyTaken:
			return handed(nil, "handed over to the running window")
		case replyGone:
			// The window has gone since it was found (a host whose hand-off window was being
			// destroyed): look again, whatever the time, as the lock is about to be free.
			time.Sleep(retryEvery)
			continue
		case replyRetry:
			if time.Now().Before(deadline) {
				time.Sleep(retryEvery) // the host is closing
				continue
			}
		}
		// An older build holds the lock (it answered 0 or 1), or none answers in time.
		log.Info("the running window is an older build's", "answer", answer, "error", err)
		if !view.IsProgress() {
			return handed(forward(running, c.Args, getwd), "handed over to the running window")
		}
		pi, prun, perr := win.ClaimInstance(win.InstanceNames(HandoffNames(id, view)), wait)
		if prun.OK() {
			return handed(forward(prun, c.Args, getwd), "handed over to the running progress window")
		}
		if perr != nil {
			log.Warn("single-instance lock", "error", perr)
		}
		h = newHost(c, id, files, log, closeLog)
		h.Legacy, h.launchLock = true, pi
		// Later starts of this download (whichever build) find it at once.
		if err := pi.Listen(win.ListenOptions{Kind: host.HandoffDataKind, Max: host.MaxHandoff, OnData: h.handoff, OnClose: h.CloseAll}); err != nil {
			log.Warn("later starts cannot hand over", "error", err)
		}
		break
	}
	if h == nil {
		h = newHost(c, id, files, log, closeLog)
		h.instance = inst
		if err := inst.Listen(win.ListenOptions{
			Kind: host.HandoffDataKind, AlsoKinds: []uintptr{host.HostHandoffKind}, Max: host.MaxHandoff,
			Answer: h.answer, BusyAnswer: host.HostBusy, ClosedAnswer: host.HostClosing, OnData: h.handoff, OnClose: h.CloseAll,
		}); err != nil {
			log.Warn("later starts cannot hand over", "error", err)
		}
	}
	h.launch = c.Args
	h.startTrims(c)
	log.Info("window process started", "version", h.version, "progress", view.Progress, "preview", IsPreview(id), "legacy", h.Legacy)
	return h, false, nil
}

func newHost(c Config, id string, files Files, log *slog.Logger, closeLog func()) *Host {
	d := c.Daemon
	if d == nil {
		d = host.NewClientDaemon(client.Default())
	}
	open := c.OpenProgress
	if open == nil {
		open = host.StartProgressWindow
	}
	getwd := c.Getwd
	if getwd == nil {
		getwd = os.Getwd
	}
	version := c.Version
	if version == "" {
		version = "dev"
	}
	return &Host{
		LockID: id, Files: files, Log: log, version: version, daemon: d, webview2: c.WebView2Dir,
		getwd: getwd, noActivate: c.NoActivate, openOther: open, closeLog: closeLog,
		progress: map[int64]*Window{},
	}
}

func (h *Host) startTrims(c Config) {
	if c.NoMemoryTrim {
		return
	}
	platform.TuneMemory()
	// A window process holds no downloads: what it freed goes back to the system each minute
	// it allocated something (today's main.go); its own trims below empty its working set.
	ctx, cancel := context.WithCancel(context.Background())
	h.stopTrim = cancel
	h.trimCtx, h.trimLog = ctx, h.Log
	go platform.ReleaseMemoryWhileIdle(ctx, time.Minute, func() bool { return false })
	after := c.StartupTrim
	if after <= 0 {
		after = StartupTrimAfter
	}
	go startupTrim(ctx, after, h.Log)
	// The first start of an exe file reads its image in (imagetail_windows.go).
	go watchImageTail(ctx, TailWatchEvery, after, h.Log)
}

// firstFrame is a window's first frame: the first-frame trim runs FirstFrameTrimAfter later.
func (h *Host) firstFrame() {
	if h.trimCtx != nil {
		go startupTrim(h.trimCtx, FirstFrameTrimAfter, h.trimLog)
	}
}

// startupTrim runs the start-up trim after d, unless ctx ends first.
func startupTrim(ctx context.Context, d time.Duration, log *slog.Logger) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
		debug.FreeOSMemory() // what Go freed goes first, so the trim takes the rest
		if err := trimWorkingSet(); err != nil {
			log.Warn("start-up memory trim", "error", err)
		}
	}
}

// What a start makes of the running window's answer to its host hand-off (hostReply).
const (
	replyOlder = iota // an older build's window: it answered 0 or 1, or nothing in time
	replyTaken        // a host took it (host.HostAnswer)
	replyRetry        // a host that is closing or busy, or a window that failed the send
	replyGone         // the window no longer exists: what it answered says nothing
)

// hostReply is what a start makes of answer and err, the running window's answer to its host
// hand-off; alive is whether that window still exists after it. Only a window that is still
// there is taken for an older build's: a host's hand-off window that is being destroyed can
// leave its start with DefWindowProc's 0, and a send to a window destroyed meanwhile fails.
func hostReply(answer uintptr, err error, alive bool) int {
	switch {
	case err == nil && answer == host.HostAnswer:
		return replyTaken
	case !alive:
		return replyGone
	case err == nil && (answer == host.HostClosing || answer == host.HostBusy):
		return replyRetry
	case err != nil && !errors.Is(err, windows.ERROR_TIMEOUT):
		return replyRetry
	}
	return replyOlder
}

// payload is the hand-off of args and the working directory, as Wails v2.17 sends it.
func payload(args []string, getwd func() (string, error)) ([]byte, error) {
	wd, _ := getwd() // Wails sends "" when it cannot read it
	if args == nil {
		args = []string{} // Wails v2.17 sends [] for no arguments, never null
	}
	return host.EncodeHandoff(host.Handoff{Args: args, WorkingDirectory: wd})
}

// send hands args to the running window under dwData kind and returns its answer.
func send(r win.Running, kind uintptr, args []string, getwd func() (string, error)) (uintptr, error) {
	b, err := payload(args, getwd)
	if err != nil {
		return 0, err
	}
	return r.Send(kind, b, ForwardTimeout)
}

// forward hands args and the working directory to today's running window (dwData
// host.HandoffDataKind): AllowForeground ran first, so it may come to the front.
func forward(r win.Running, args []string, getwd func() (string, error)) error {
	_, err := send(r, host.HandoffDataKind, args, getwd)
	return err
}

// answer is the main lock's answer to a hand-off of kind: a host's start is taken while the
// host has windows to run (HostAnswer), refused once it is closing (HostClosing: the start
// tries again); today's starts (HandoffDataKind) are always taken, as they never retry.
func (h *Host) answer(kind uintptr) (uintptr, bool) {
	if kind != host.HostHandoffKind {
		return 1, true
	}
	if !h.accepting {
		return host.HostClosing, false
	}
	return host.HostAnswer, true
}

// handoff is the main lock's: a later start's payload, on the UI thread.
func (h *Host) handoff(b []byte) {
	d, err := host.DecodeHandoff(b)
	if err != nil {
		h.Log.Warn("a later start's hand-off", "error", err)
		return
	}
	h.Log.Info("a later start handed over", "arguments", len(d.Args))
	h.Request(d.Args)
}

// ErrClosed is the answer of a closed Host or Window.
var ErrClosed = errors.New("glue: closed")

// Close ends the process's glue, on its UI thread, once Run has returned (or instead of
// it): every window's part that is left, the main lock, the trims and ui.log. A second call
// does nothing.
func (h *Host) Close() {
	if h == nil || h.closed {
		return
	}
	h.closed = true
	for len(h.windows) > 0 {
		h.windows[len(h.windows)-1].end()
	}
	h.instance.Close()
	h.launchLock.Close()
	if h.stopTrim != nil {
		h.stopTrim()
	}
	if h.closeLog != nil {
		h.closeLog()
		h.closeLog = nil
	}
}

func errorf(format string, a ...any) error { return fmt.Errorf("glue: "+format, a...) }
