// Command downexd is the per-user Downex daemon (docs/DAEMON-SPEC.md). It owns every
// download of the user, runs them with one shared engine and serves the Downex API
// (proto/downex/v1) over Connect on a user-only local endpoint: a named pipe on
// Windows, a unix socket on Linux. Streaming-media downloads (HLS/DASH) run through
// internal/mediajob and internal/remux (media.go); the videos of web pages are resolved
// with yt-dlp (internal/sitejob, run with the copies of internal/tools; tools.go) and
// downloaded the same way (site.go, docs/SITE-MEDIA-SPEC.md).
//
// Usage:
//
//	downexd [--db path] [--endpoint path] [--log-level level] [--log-dir dir] [--foreground]
//	        [--background] [--no-tray] [--allow-elevated] [--tools-dir dir]
//
// A second start for the same endpoint prints "already running" and exits 0 when the
// running daemon answers. SIGINT, SIGTERM, SIGHUP (Linux), closing its console, the end
// of the user session (Windows), the Shutdown RPC and "Quit Downex" in the
// notification-area menu pause every download (progress committed) and exit 0. downexd
// refuses to run elevated (as administrator) unless --allow-elevated is given.
//
// The user's own daemon (default database and endpoint; desktop.go) shows a
// notification-area icon on Windows, and a menu-bar icon in a macOS cgo build (the
// executable of Downex Agent.app, whose main thread runs AppKit: mainthread_darwin.go),
// while Settings.show_tray_icon is set (docs/DESKTOP-SPEC.md §3–§4; tray.go), unless
// --no-tray is given, and applies
// Settings.launch_at_login (§5). --background is what the sign-in entry and the
// installer pass. On Windows no console window stays open for them: releases build
// downexd as a GUI-subsystem program (scripts/build-windows.ps1), and a console build
// started with --background in a console of its own continues as a detached copy;
// a GUI build run from a terminal with --foreground (or --help, --version) writes to
// that terminal (console_windows.go).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"time"

	"connectrpc.com/connect"

	"downex/internal/daemon"
	"downex/internal/dlock"
	"downex/internal/engine"
	"downex/internal/ipc"
	"downex/internal/pac"
	"downex/internal/platform"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/rpc/downex/v1/downexv1connect"
	"downex/internal/store"
	"downex/internal/transport"
	"downex/internal/tray"
)

// version is reported by Status and --version; release builds set it with
// -ldflags "-X main.version=<version>".
var version = "dev"

// Exit statuses.
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

// Shutdown bounds: downloads get daemon.DefaultShutdownTimeout to pause, then open
// connections this long to finish their last responses. When the system terminates the
// process within seconds (its console was closed, the user session ends) the urgent
// budgets apply instead, so that progress is committed before that happens.
const (
	serverShutdownTimeout = 5 * time.Second
	urgentPauseTimeout    = 3 * time.Second
	urgentServerTimeout   = 500 * time.Millisecond
	statusProbeTimeout    = 3 * time.Second
	instanceRetryInterval = 100 * time.Millisecond
)

// stopCause is why the daemon stops: the cause of run's context.
type stopCause struct {
	signal os.Signal // nil when the user session ends
	urgent bool      // the system terminates the process within seconds
}

func (c *stopCause) Error() string {
	if c.signal != nil {
		return "signal " + c.signal.String()
	}
	return "the user session is ending"
}

func main() {
	// PAC scripts are evaluated in a helper process: downexd started again, which
	// serves its parent here and exits (internal/pac, isolate.go).
	pac.ServeIfHelper()
	platform.TuneMemory()
	// Before anything is written: no console window must stay open for a daemon
	// started at sign-in (console_windows.go).
	stdout, stderr, handedOver := setupConsole(os.Args[1:])
	if handedOver {
		os.Exit(exitOK)
	}
	// The first stop signal stops the daemon gracefully; afterwards default signal
	// handling is restored so that a second one terminates at once. Even then the store
	// never claims bytes that were not flushed.
	ctx, cancel := context.WithCancelCause(context.Background())
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, stopSignals()...)
	go func() {
		select {
		case s := <-sigs:
			signal.Stop(sigs)
			cancel(&stopCause{signal: s, urgent: urgentSignal(s)})
		case <-ctx.Done():
		}
	}()
	// The macOS menu-bar icon (a cgo build) needs the main thread for AppKit: RunMain
	// runs the daemon on another goroutine there (mainthread_darwin.go); everywhere
	// else it just calls it.
	code := tray.RunMain(func() int { return run(ctx, os.Args[1:], stdout, stderr) })
	cancel(nil)
	os.Exit(code)
}

// options are the parsed command line.
type options struct {
	db            string
	endpoint      string
	logLevel      slog.Level
	logDir        string
	foreground    bool
	background    bool
	allowElevated bool
	noTray        bool
	toolsDir      string
}

func parseFlags(args []string, stdout, stderr io.Writer) (options, bool, int) {
	var (
		o           options
		level       string
		showVersion bool
	)
	fs := flag.NewFlagSet("downexd", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.db, "db", "", "download database `path` (default: <user config dir>/Downex/downex.db)")
	fs.StringVar(&o.endpoint, "endpoint", "", "local endpoint `path` to serve (default: the per-user endpoint, or $"+ipc.EnvEndpoint+")")
	fs.StringVar(&level, "log-level", "info", "log `level`: debug, info, warn or error")
	fs.StringVar(&o.logDir, "log-dir", "", "`directory` for "+logFileName+" (default: the per-user log directory)")
	fs.BoolVar(&o.foreground, "foreground", false, "also write logs to stderr, for running in a terminal")
	// --background is how sign-in (the Run entry) and the installer start the daemon
	// (docs/DESKTOP-SPEC.md §5, §6). It runs in the background anyway; on Windows a
	// console build then also leaves a console window of its own (console_windows.go).
	fs.BoolVar(&o.background, "background", false, "run in the background (the default; for sign-in and the installer)")
	fs.BoolVar(&o.noTray, "no-tray", false, "never show the notification-area icon or its notifications (only the "+
		"user's own daemon, on the default database and endpoint, shows one)")
	fs.BoolVar(&o.allowElevated, "allow-elevated", false,
		"run even with administrator (elevated) rights; every program of the user can then use them through downexd")
	fs.StringVar(&o.toolsDir, "tools-dir", "", "`directory` of the yt-dlp and Deno copies that resolve video pages "+
		"(default: the per-user tools folder for the user's own daemon, else a tools folder next to --db)")
	fs.BoolVar(&showVersion, "version", false, "print the version and exit")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: downexd [flags]\n\nRun the Downex download daemon for the current user.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return o, false, exitOK
		}
		return o, false, exitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "downexd: unexpected argument %q\n", fs.Arg(0))
		return o, false, exitUsage
	}
	if showVersion {
		fmt.Fprintf(stdout, "downexd %s\n", version)
		return o, false, exitOK
	}
	lv, err := parseLevel(level)
	if err != nil {
		fmt.Fprintf(stderr, "downexd: --log-level: %v\n", err)
		return o, false, exitUsage
	}
	o.logLevel = lv
	return o, true, exitOK
}

// errElevated reports a daemon started with administrator rights (docs/DAEMON-SPEC.md
// §1: no elevation).
var errElevated = errors.New("refusing to run with administrator (elevated) rights: every program of " +
	"this user could then make downexd write or delete files anywhere on the system. Start it from a " +
	"normal (not elevated) prompt, or pass --allow-elevated to accept that risk")

// checkElevation returns errElevated when elevated reports elevated rights and they
// were not allowed. A failed check refuses too: elevation cannot be ruled out.
func checkElevation(elevated func() (bool, error), allow bool) error {
	if allow {
		return nil
	}
	up, err := elevated()
	if err != nil {
		return fmt.Errorf("cannot tell whether downexd runs elevated (%w); pass --allow-elevated to skip the check", err)
	}
	if up {
		return errElevated
	}
	return nil
}

// run starts the daemon and serves until ctx ends or a client asks it to shut down.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	o, ok, code := parseFlags(args, stdout, stderr)
	if !ok {
		return code
	}
	if err := checkElevation(platform.Elevated, o.allowElevated); err != nil {
		fmt.Fprintf(stderr, "downexd: %v\n", err)
		return exitError
	}
	endpoint := o.endpoint
	if endpoint == "" {
		ep, err := ipc.Endpoint()
		if err != nil {
			fmt.Fprintf(stderr, "downexd: %v\n", err)
			return exitError
		}
		endpoint = ep
	}

	// The instance lock comes first: a second daemon must not even touch the log.
	release, err := ipc.AcquireInstanceLock(endpoint)
	if errors.Is(err, ipc.ErrAlreadyRunning) {
		// Either a daemon runs, or one is just exiting: wait for one or the other.
		if release, code = awaitInstance(ctx, endpoint, true, stdout, stderr); release == nil {
			return code
		}
		err = nil
	}
	if err != nil {
		fmt.Fprintf(stderr, "downexd: %v\n", err)
		return exitError
	}
	defer release()

	// The end of the user session (Windows) stops the daemon like a signal, and keeps
	// the system from terminating it until it has stopped (the deferred finished).
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	session, sessionErr := watchSessionEnd(func() { cancel(&stopCause{urgent: true}) })
	defer session.close()
	defer session.finished()

	logDir := o.logDir
	if logDir == "" {
		if logDir, err = platform.LogDir(); err != nil {
			fmt.Fprintf(stderr, "downexd: log directory: %v\n", err)
			return exitError
		}
	}
	var console io.Writer
	if o.foreground {
		console = stderr
	}
	logs, err := newLogger(logDir, o.logLevel, console)
	if err != nil {
		fmt.Fprintf(stderr, "downexd: %v\n", err)
		return exitError
	}
	defer logs.file.Close()
	log := logs.logger
	fail := func(msg string, err error) int {
		log.Error(msg, "err", err)
		fmt.Fprintf(stderr, "downexd: %s: %v\n", msg, err)
		return exitError
	}
	// Downloads keep their speed while the processor is busy (other programs, Downex's
	// own windows): downexd runs above normal priority, for a few percent of one core.
	if err := platform.RaiseDownloadPriority(); err != nil {
		log.Warn("cannot raise downexd's priority", "err", err)
	}
	if sessionErr != nil {
		log.Warn("the end of the user session will not pause downloads", "err", sessionErr)
	}

	dbPath, err := databasePath(o.db)
	if err != nil {
		return fail("database path", err)
	}
	// One daemon per database, whatever endpoint it serves: two would each schedule
	// (and recover) the other's downloads.
	dbLock, err := dlock.AcquireDaemon(dbPath)
	if err != nil {
		return fail("lock database", err)
	}
	defer dbLock.Release()

	ln, err := ipc.Listen(endpoint)
	if errors.Is(err, ipc.ErrAlreadyRunning) {
		log.Warn("endpoint is in use", "endpoint", endpoint, "err", err)
		_, code := awaitInstance(ctx, endpoint, false, stdout, stderr)
		return code
	}
	if err != nil {
		return fail("listen", err)
	}
	defer ln.Close()

	st, err := store.OpenSQLite(dbPath)
	if err != nil {
		return fail("open database", err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			log.Error("close database", "err", err)
		}
	}()

	hooks, err := loadTestHooks() // none in product builds
	if err != nil {
		return fail("test hooks", err)
	}
	own := ownDaemon(o, os.Getenv)
	// The network settings (docs/M6-SPEC.md §2): the runtime is the proxy resolver and the
	// credentials of every connection and the host rules of the engine; the Manager keeps
	// it up to date with the settings and stores passwords in the keyring.
	keyring, err := openKeyring(own, dbPath, st, log)
	if err != nil {
		log.Warn("proxy and site-login passwords cannot be stored", "err", err)
		keyring = nil
	}
	network := daemon.NewNetworkRuntime(keyring, log)
	defer network.Close()
	topts := transport.DefaultHTTPOptions()
	topts.UserAgent = "Downex/" + version
	// Downloads speak HTTP/1.1. Measured 2026-10-29 on a ~84 Mbit/s line, the same
	// 1.33 GiB file in the order 1.1, 2, 2, 1.1: HTTP/1.1 took 194.6 s on average and
	// HTTP/2 204.2 s, whose reset streams also drag out the end game. Every slot has its
	// own connection either way, so HTTP/2's multiplexing never helped a download.
	topts.EnableHTTP2 = false
	topts.Resolver, topts.Credentials = network, network
	tf, err := transport.NewHTTPFactoryWithRoots(topts, hooks.roots)
	if err != nil {
		return fail("transport", err)
	}
	// Site media (docs/SITE-MEDIA-SPEC.md): the yt-dlp and Deno copies and the resolver
	// that runs them. Only the user's own daemon (or an e2etest build given a release
	// server) downloads and updates them; a daemon whose tools folder cannot be used
	// still runs, without video pages.
	var site *siteTools
	toolsDir, err := toolsFolder(o.toolsDir, own, dbPath) // reported in Status ("": unknown)
	if err != nil {
		log.Warn("video downloads from web pages are unavailable: no tools folder", "err", err)
		toolsDir = ""
	} else if site, err = openSiteTools(toolsDir, own, network, hooks, log); err != nil {
		log.Warn("video downloads from web pages are unavailable", "dir", toolsDir, "err", err)
		site = nil
	} else {
		defer site.close(log)
	}
	var (
		siteMedia daemon.SiteMedia
		siteTools daemon.Tools
	)
	if site != nil {
		sm := newSiteMedia(site.resolver)
		sm.freeSpace = platform.FreeSpace
		siteMedia, siteTools = sm, toolsAdapter{site.mgr}
	}
	desk := desktopHooks(o, log)
	pw := powerHooks(o, log)
	defer pw.close()
	if hooks.power != nil {
		pw.power = hooks.power
	}
	shutdownRequested := make(chan struct{})
	requestShutdown := sync.OnceFunc(func() { close(shutdownRequested) })
	// Recovery runs to completion even if a signal arrives meanwhile; the signal is
	// then handled below like any other.
	m, err := daemon.New(context.WithoutCancel(ctx), daemon.Config{
		Store:            st,
		Transport:        tf,
		Engine:           engine.Options{HostRules: network},
		Media:            mediaJobs{},
		SettingsPath:     filepath.Join(filepath.Dir(dbPath), daemon.SettingsFileName),
		LogDir:           logDir,
		ToolsDir:         toolsDir,
		LockDir:          dlock.Dir(dbPath),
		Logger:           log,
		Version:          version,
		Endpoint:         endpoint,
		LaunchAtLogin:    desk.launchAtLogin,
		OpenConfirmation: desk.openConfirmation,
		OpenProgress:     desk.openProgress,
		// Queues (docs/M6-SPEC.md §1): the quit post-queue action stops this daemon like
		// the Shutdown RPC; power actions, wake timers and keeping the computer awake only
		// for the user's own daemon.
		Quit:      requestShutdown,
		Power:     pw.power,
		Wake:      pw.wake,
		KeepAwake: pw.awake,
		Clock:     hooks.clock,
		Network:   network,
		Site:      siteMedia,
		Tools:     siteTools,
	})
	if err != nil {
		return fail("start", err)
	}
	session.setOnResume(m.Reevaluate)
	// While no download runs: Go's freed memory back after activity, and the working set
	// emptied once start-up is over and each time the last download ends (lowmem.go). A run
	// started since the last look counts as busy, so a download that started and ended
	// between two looks is trimmed after as well. Only this goroutine calls the func.
	var runsSeen uint64
	go platform.ReleaseMemoryAndTrimWhileIdle(ctx, time.Minute, platform.IdleStartupTrimAfter,
		func() bool {
			running, runs := m.Active()
			ran := runs != runsSeen
			runsSeen = runs
			return running > 0 || ran
		})
	// Updates (docs/M6-SPEC.md §4): checked a minute after start and then daily while a
	// feed is set; the tray tells about a new version.
	// Packages are a download cache: the user's own daemon keeps them in the local data
	// folder next to its tools (on Windows %LOCALAPPDATA%\Downex), not in the roaming
	// profile that holds the database, where they were downloaded before.
	updatesDir, oldUpdatesDirs := filepath.Dir(dbPath), []string(nil)
	if own {
		if t, err := platform.ToolsDir(); err == nil && filepath.Dir(t) != updatesDir {
			updatesDir, oldUpdatesDirs = filepath.Dir(t), []string{updatesDir}
		}
	}
	stopUpdates := daemon.StartUpdates(m, daemon.UpdatesConfig{
		DataDir:     updatesDir,
		OldDataDirs: oldUpdatesDirs,
		HTTPClient:  updateClient(network, hooks.roots),
		PublicKey:   hooks.updateKey,
		AutoCheck:   true,
	})
	defer stopUpdates()
	// yt-dlp and Deno updates (docs/SITE-MEDIA-SPEC.md §4.5): checked 5 minutes after start
	// and then daily, only by a daemon that downloads tools.
	if site != nil && site.downloads {
		stopTools := daemon.StartTools(m, daemon.ToolsServiceConfig{})
		defer stopTools()
	}
	// The clipboard monitor (docs/M6-SPEC.md §3) only for the user's own daemon; it
	// follows the clipboard_monitor setting and ends with the Manager.
	var helpers sync.WaitGroup
	defer helpers.Wait()
	if own {
		helpers.Add(1)
		go func() {
			defer helpers.Done()
			m.WatchClipboard(ctx, daemon.SystemClipboard(log))
		}()
	}
	// The tray starts before the first request is served: whether its icon shows
	// notifications decides whether the first browser hand-off (often the one that
	// autostarted this daemon) can be held for confirmation.
	stopTray := func() {}
	if desk.tray {
		stopTray = startTray(ctx, trayConfig{
			daemon: m,
			log:    log,
			quit:   requestShutdown,
			openUI: func() error { return openUI() },
		})
	}
	defer stopTray()

	srv := ipc.NewServer(daemon.NewHandler(m, requestShutdown))
	srv.ErrorLog = slog.NewLogLogger(log.Handler(), slog.LevelWarn)
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	log.Info("downexd started", "version", version, "pid", os.Getpid(), "endpoint", endpoint, "db", dbPath)

	code = exitOK
	pauseBudget, serverBudget := daemon.DefaultShutdownTimeout, serverShutdownTimeout
	select {
	case <-ctx.Done():
		var cause *stopCause
		switch {
		case !errors.As(context.Cause(ctx), &cause):
			log.Info("stopping on signal")
		case cause.signal != nil:
			log.Info("stopping on signal", "signal", cause.signal.String())
		default:
			log.Info("stopping: the user session is ending")
		}
		if cause != nil && cause.urgent {
			pauseBudget, serverBudget = urgentPauseTimeout, urgentServerTimeout
		}
	case <-shutdownRequested:
		log.Info("stopping on request")
	case err := <-served:
		log.Error("server stopped unexpectedly", "err", err)
		code = exitError
	}
	// The icon goes first, so that it does not linger while downloads pause.
	stopTray()
	pctx, pcancel := context.WithTimeout(context.Background(), pauseBudget)
	defer pcancel()
	if err := m.Shutdown(pctx); err != nil {
		log.Warn("not every download paused in time; they are requeued on the next start", "err", err)
	}
	// With the downloads paused, running extractions and tool installs are stopped (their
	// process trees killed, their work folders removed).
	if site != nil {
		site.close(log)
	}
	sctx, scancel := context.WithTimeout(context.Background(), serverBudget)
	defer scancel()
	if err := srv.Shutdown(sctx); err != nil {
		log.Warn("closing remaining connections", "err", err)
		_ = srv.Close()
	}
	log.Info("downexd stopped")
	return code
}

// awaitInstance runs when another process holds endpoint's instance lock or endpoint.
// It waits up to statusProbeTimeout for that daemon to answer Status, and then prints
// "already running" and returns exitOK (docs/DAEMON-SPEC.md §1). With retryLock it also
// retries the instance lock while nothing answers, because the holder may be a daemon
// that is just exiting (after "downex daemon stop"); when the lock is acquired it
// returns its release function, and this process carries on as the daemon. An endpoint
// or lock held by a foreign process (another user, a sandboxed process) is reported at
// once.
func awaitInstance(ctx context.Context, endpoint string, retryLock bool, stdout, stderr io.Writer) (release func(), code int) {
	hc := ipc.NewHTTPClient(endpoint)
	defer hc.CloseIdleConnections()
	client := downexv1connect.NewDaemonServiceClient(hc, ipc.BaseURL)
	ctx, cancel := context.WithTimeout(ctx, statusProbeTimeout)
	defer cancel()
	var last error
	for {
		res, err := client.Status(ctx, connect.NewRequest(&downexv1.StatusRequest{}))
		if err == nil {
			s := res.Msg
			fmt.Fprintf(stdout, "downexd is already running (pid %d, version %s, endpoint %s)\n",
				s.GetPid(), s.GetVersion(), s.GetEndpoint())
			return nil, exitOK
		}
		last = err
		if errors.Is(err, ipc.ErrNotRunning) && retryLock {
			release, lerr := ipc.AcquireInstanceLock(endpoint)
			if lerr == nil {
				return release, exitOK
			}
			if !errors.Is(lerr, ipc.ErrAlreadyRunning) {
				last = lerr
			}
		}
		if errors.Is(last, ipc.ErrForeignEndpoint) {
			fmt.Fprintf(stderr, "downexd: %s is held by a program that is not a downexd of this user; "+
				"refusing to use it: %v\n", endpoint, last)
			return nil, exitError
		}
		select {
		case <-ctx.Done():
			fmt.Fprintf(stderr, "downexd: another instance holds %s but does not answer: %v\n", endpoint, last)
			return nil, exitError
		case <-time.After(instanceRetryInterval):
		}
	}
}

// databasePath resolves --db (default store.DefaultPath()), creating its directory.
func databasePath(flagValue string) (string, error) {
	if flagValue == "" {
		return store.DefaultPath()
	}
	abs, err := filepath.Abs(flagValue)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return "", fmt.Errorf("create database directory: %w", err)
	}
	return abs, nil
}
