package host

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"connectrpc.com/connect"

	"downex/internal/client"
	downexv1 "downex/internal/rpc/downex/v1"
)

// getenv reads the environment (tests of the package replace nothing here: options do).
var getenv = os.Getenv

// Session is one window process's host: what today's App (ui/app.go) keeps besides its
// Wails bindings. It knows what the process shows (View), reaches downexd (Daemon), opens
// files (Files), carries the "--confirm <id>" requests (Confirmations) and forwards the
// download events (Start). Its methods are safe for concurrent use.
//
// The first call or the bridge's first Watch is the window's first contact with downexd,
// which starts it when it is not running; nothing starts it later (ClientDaemon).
type Session struct {
	view       View
	version    string
	daemon     Daemon
	files      *Files
	confirm    *Confirmations
	log        *slog.Logger
	themePath  string
	minBackoff time.Duration
	maxBackoff time.Duration
	openWindow func(id int64) error
	openRaised func(id int64) error
	env        func(string) string

	ctx    context.Context // ended by Close
	cancel context.CancelFunc

	mu      sync.Mutex
	started bool
	stop    func()
}

// SessionOptions are a Session's parts; the zero value of each field is the product's.
type SessionOptions struct {
	// Args is the command line without the program name (os.Args[1:]): its "--progress
	// <id>" chooses the view, its "--confirm <id>" requests wait until the window is ready.
	Args []string
	// Version is this build's version, logged at start and shown in About ("dev" when "").
	Version string
	// Daemon is downexd (NewClientDaemon(client.Default()) when nil).
	Daemon Daemon
	// Shell is the desktop's file handling (NewShell() when nil).
	Shell Shell
	// Log receives the session's messages (discarded when nil; OpenDefaultLog is ui.log).
	Log *slog.Logger
	// ThemePath is the stored theme choice (DefaultThemePath() when ""; tests: a temporary
	// file, never the user's own).
	ThemePath string
	// MinBackoff and MaxBackoff bound the events bridge's reconnect delay (1 s and 10 s
	// when zero).
	MinBackoff, MaxBackoff time.Duration
	// OpenWindow starts the progress window of a download (StartProgressWindow when nil).
	OpenWindow func(id int64) error
	// OpenRaisedWindow starts the progress window of a download the user just started in
	// the browser, above the browser ("--progress <id> --raise"; StartRaisedProgressWindow
	// when nil, which a running window process takes over).
	OpenRaisedWindow func(id int64) error
	// FreeSpace reads the bytes free on the volume of an absolute folder
	// (platform.FreeSpace when nil).
	FreeSpace func(dir string) (int64, error)
	// Env reads the environment for the diagnostics (os.Getenv when nil).
	Env func(string) string
}

// NewSession returns the session of a window process. Nothing starts until Start.
func NewSession(o SessionOptions) *Session {
	log := o.Log
	if log == nil {
		log = discardLogger()
	}
	d := o.Daemon
	if d == nil {
		d = NewClientDaemon(client.Default())
	}
	sh := o.Shell
	if sh == nil {
		sh = NewShell()
	}
	themePath := o.ThemePath
	if themePath == "" {
		themePath = DefaultThemePath()
	}
	open := o.OpenWindow
	if open == nil {
		open = StartProgressWindow
	}
	openRaised := o.OpenRaisedWindow
	if openRaised == nil {
		openRaised = StartRaisedProgressWindow
	}
	env := o.Env
	if env == nil {
		env = getenv
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Session{
		view:       ViewOf(o.Args),
		version:    cmp.Or(o.Version, "dev"),
		daemon:     d,
		files:      &Files{Daemon: d, Shell: sh, Log: log, FreeSpaceOf: o.FreeSpace},
		log:        log,
		themePath:  themePath,
		minBackoff: o.MinBackoff,
		maxBackoff: o.MaxBackoff,
		openWindow: open,
		openRaised: openRaised,
		env:        env,
		ctx:        ctx,
		cancel:     cancel,
	}
	s.confirm = NewConfirmations(s.view, log)
	s.confirm.Request(ConfirmArgs(o.Args))
	s.confirm.RequestRenew(RenewArgs(o.Args))
	return s
}

// View is what this process shows.
func (s *Session) View() View { return s.view }

// Version is this build's version.
func (s *Session) Version() string { return s.version }

// Daemon is downexd.
func (s *Session) Daemon() Daemon { return s.daemon }

// Files opens, shows and checks the files of downloads.
func (s *Session) Files() *Files { return s.files }

// Confirmations carries the "--confirm <id>" requests to the main window's dialogs.
func (s *Session) Confirmations() *Confirmations { return s.confirm }

// Log is the session's logger.
func (s *Session) Log() *slog.Logger { return s.log }

// ThemePath is the stored theme choice ("" when there is no folder for it).
func (s *Session) ThemePath() string { return s.themePath }

// Context ends when the session closes.
func (s *Session) Context() context.Context { return s.ctx }

// CallContext bounds one call's daemon work (CallTimeout); it ends early when the session
// closes.
func (s *Session) CallContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(s.ctx, CallTimeout)
}

// Start forwards the daemon's download events to emit, on a goroutine of the bridge's
// (a progress window's: those of its download only), until Close. It runs once: later
// calls, and calls after Close, do nothing.
func (s *Session) Start(emit func(*downexv1.WatchResponse)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started || s.ctx.Err() != nil {
		return
	}
	s.started = true
	var ids []int64
	if s.view.IsProgress() {
		ids = []int64{s.view.Progress}
	}
	b := &Bridge{Daemon: s.daemon, IDs: ids, Emit: emit, Log: s.log, Min: s.minBackoff, Max: s.maxBackoff}
	s.stop = b.Start(s.ctx)
	switch {
	case s.view.IsProgress():
		s.log.Info("progress window started", "version", s.version, "download", s.view.Progress)
	case s.view.IsConfirm():
		s.log.Info("confirm window started", "version", s.version, "download", s.view.Confirm)
	default:
		s.log.Info("window started", "version", s.version)
	}
}

// Close stops the events bridge and cancels the calls in progress. It waits for the bridge
// for at most ShutdownWait; no event is emitted once it has returned in time.
func (s *Session) Close() {
	s.cancel()
	s.mu.Lock()
	stop := s.stop
	s.mu.Unlock()
	if stop != nil {
		stop()
	}
}

// SecondInstance handles a later start of this window's program (args without the
// program name): it brings the window to the front (focus) and asks for the confirmation
// dialogs of that start's "--confirm <id>" arguments (a progress window ignores them).
func (s *Session) SecondInstance(args []string, focus func()) {
	s.confirm.SecondInstance(args, focus)
}

// Startup is what today's Startup binding tells the page: what this window shows, what
// this system's window can do, and the build (Settings → About).
type Startup struct {
	View         View
	Capabilities Capabilities
	Build        BuildInfo
}

// Startup returns this window's start-up information.
func (s *Session) Startup() Startup {
	return Startup{View: s.view, Capabilities: s.view.Capabilities(), Build: CurrentBuild(s.version)}
}

// LoadTheme reads the stored theme choice (system when there is none).
func (s *Session) LoadTheme() ThemeChoice { return LoadThemeChoice(s.themePath) }

// SaveTheme stores the app's theme choice for the windows opened later, which read it
// before their window exists. The caller applies it to its own window first. An unknown
// choice is invalid_argument; a choice that cannot be stored is internal.
func (s *Session) SaveTheme(c ThemeChoice) error {
	if _, ok := ParseThemeChoice(string(c)); !ok {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(`theme must be "system", "light" or "dark"`))
	}
	if err := SaveThemeChoice(s.themePath, c); err != nil {
		s.log.Warn("cannot store the theme", "error", err)
		return connect.NewError(connect.CodeInternal, fmt.Errorf("cannot store the theme: %w", err))
	}
	return nil
}

// OpenProgressWindow opens the progress window of download id (DESKTOP-SPEC §7) with
// SessionOptions.OpenWindow: the native host opens it in its own process, other windows
// start "<this program> --progress <id>"; when that download's window is open already, it
// comes to the front. An id that is not positive is invalid_argument, a failed start
// internal.
func (s *Session) OpenProgressWindow(id int64) error {
	return openProgressWindow(id, s.openWindow, s.log)
}

// OpenProgressWindowRaised opens the progress window of download id as OpenProgressWindow
// does, above the browser even without the foreground (RaiseFlag): the progress window of a
// download the user just started in the browser, when downexd does not open it itself
// (docs/DESKTOP-SPEC.md §7; SessionOptions.OpenRaisedWindow).
func (s *Session) OpenProgressWindowRaised(id int64) error {
	return openProgressWindow(id, s.openRaised, s.log)
}

// Diagnostics is the "Copy diagnostics" text of this window (Diagnostics); webview is the
// browser engine's version of a window that has one, else "". ctx bounds the daemon calls.
func (s *Session) Diagnostics(ctx context.Context, webview string) string {
	return Diagnostics(ctx, s.daemon, DiagnosticsOptions{Version: s.version, View: s.view, Webview: webview, Env: s.env})
}
