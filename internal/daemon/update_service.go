package daemon

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/update"
)

// Update checks and installs (docs/M6-SPEC.md §4): DaemonService.CheckForUpdate and
// InstallUpdate, and the automatic check.

// Update timings.
const (
	// updateFirstDelay is how long after StartUpdates (and after the settings turn
	// automatic checks on) the first automatic check runs.
	updateFirstDelay = 60 * time.Second
	// updateInterval is the time between automatic checks.
	updateInterval = 24 * time.Hour
	// updateMaxWait bounds one timer of the automatic check: the wall clock is looked at
	// again at least this often, so that sleep and clock changes delay a check by at
	// most this much.
	updateMaxWait = time.Hour
	// updateCheckTimeout bounds one check (manifest and signature).
	updateCheckTimeout = 2 * time.Minute
	// updatesDirName is the folder of DataDir that downloaded packages go to.
	updatesDirName = "updates"
)

// UpdatesConfig configures the update component of a Manager (StartUpdates).
type UpdatesConfig struct {
	// DataDir holds downloaded packages in its "updates" folder (downexd: the database's
	// folder). "" selects the folder of Config.SettingsPath; without either,
	// InstallUpdate fails with FailedPrecondition.
	DataDir string
	// OldDataDirs are folders whose "updates" folder an earlier daemon downloaded packages
	// to (downexd on Windows: the database's folder in the roaming profile, before packages
	// moved to the local one). With AutoCheck their leftovers are removed like DataDir's.
	OldDataDirs []string
	// HTTPClient makes the update requests (nil: update.NewHTTPClient with the proxy of
	// Config.Network, or from the environment without it). Redirects are followed to
	// https URLs only, whatever the client.
	HTTPClient *http.Client
	// Installer applies a verified package (nil: update.NewInstaller for this system:
	// Windows Setup, macOS app bundle, unsupported elsewhere).
	Installer update.Installer
	// PublicKey is the base64 ed25519 key that manifests are signed with; "" selects
	// update.PublicKey, the key compiled into the build. Without a key updates are
	// disabled (FailedPrecondition).
	PublicKey string
	// AutoCheck runs the automatic check: updateFirstDelay after StartUpdates, then every
	// 24 hours, while the settings have an update feed and auto_check_updates on. It also
	// removes packages that an earlier update left in DataDir.
	AutoCheck bool
	// Notify is called when an automatic check finds a newer version with a package for
	// this system: after every such check (the first a minute after the start, then at
	// most one a day), whatever a client was told, so that a version the user put off is
	// offered again. It runs on the checking goroutine and must not block. The Manager's
	// notice subscribers get a NoticeUpdateAvailable then as well (the tray shows it), with
	// or without Notify, and every automatic check's result goes to the Watch streams of
	// every download (the window's pop-up), whether or not it found one.
	Notify func(*downexv1.UpdateInfo)

	// Tests: the system (runtime.GOOS/GOARCH), the running version (Config.Version),
	// the clock and the timers of the automatic check.
	goos, goarch string
	version      string
	now          func() time.Time
	afterFunc    func(d time.Duration, f func()) (stop func() bool)
	firstDelay   time.Duration
	interval     time.Duration
}

// updateService is the update component of one Manager.
type updateService struct {
	m         *Manager
	cfg       UpdatesConfig
	log       *slog.Logger
	client    *http.Client
	installer update.Installer
	started   bool // made by StartUpdates (not a default one made by an RPC)

	mu sync.Mutex // serialises checks

	// found is the result of the latest automatic check that worked (nil before one did):
	// the first batch of a Watch of every download carries it (Manager.updateEvent).
	found atomic.Pointer[downexv1.UpdateInfo]

	installing atomic.Bool // an InstallUpdate is in progress (stays set once committed)

	tick     chan struct{} // the automatic check's timer fired
	stop     chan struct{}
	stopOnce sync.Once
	done     chan struct{} // the goroutine has ended
}

// updateServices maps each Manager to its update component: the one of StartUpdates, or
// a default one (no automatic check) made by the first update RPC. An entry is removed
// when its component stops: at the Manager's Shutdown, or by the stop function of
// StartUpdates.
var updateServices = struct {
	mu sync.Mutex
	m  map[*Manager]*updateService
}{m: make(map[*Manager]*updateService)}

// StartUpdates attaches the update component configured by cfg to m: CheckForUpdate and
// InstallUpdate of m's DaemonService use it from now on, and with cfg.AutoCheck it
// checks automatically and calls cfg.Notify. It runs until m shuts down or stop is
// called (stop waits for it; it may be called more than once). downexd calls it once,
// right after New.
func StartUpdates(m *Manager, cfg UpdatesConfig) (stop func()) {
	u := newUpdateService(m, cfg)
	u.started = true
	if _, err := update.DefaultFeed(); err != nil {
		// DefaultUpdateSettings left the feed empty: updates stay off until the user sets
		// one.
		u.log.Warn("no default update feed", "err", err)
	}
	updateServices.mu.Lock()
	old := updateServices.m[m]
	updateServices.m[m] = u
	u.start()
	updateServices.mu.Unlock()
	if old != nil {
		old.close()
	}
	return u.close
}

// updateComponent returns m's update component, making a default one when StartUpdates
// was not called.
func (m *Manager) updateComponent() (*updateService, error) {
	if m.isClosed() {
		return nil, errShuttingDown()
	}
	updateServices.mu.Lock()
	defer updateServices.mu.Unlock()
	if u := updateServices.m[m]; u != nil {
		return u, nil
	}
	u := newUpdateService(m, UpdatesConfig{})
	updateServices.m[m] = u
	u.start()
	return u, nil
}

func newUpdateService(m *Manager, cfg UpdatesConfig) *updateService {
	if cfg.goos == "" {
		cfg.goos, cfg.goarch = runtime.GOOS, runtime.GOARCH
	}
	if cfg.version == "" {
		cfg.version = m.cfg.Version
	}
	if cfg.now == nil {
		cfg.now = time.Now
	}
	if cfg.afterFunc == nil {
		cfg.afterFunc = func(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop }
	}
	if cfg.firstDelay <= 0 {
		cfg.firstDelay = updateFirstDelay
	}
	if cfg.interval <= 0 {
		cfg.interval = updateInterval
	}
	if cfg.DataDir == "" && m.cfg.SettingsPath != "" {
		cfg.DataDir = filepath.Dir(m.cfg.SettingsPath)
	}
	log := m.log.With("component", "update")
	u := &updateService{
		m: m, cfg: cfg, log: log, client: cfg.HTTPClient, installer: cfg.Installer,
		tick: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{}),
	}
	if u.client == nil {
		var rt http.RoundTripper // the proxy from the environment
		if m.cfg.Network != nil {
			// The proxy of the network settings (docs/M6-SPEC.md §2).
			t := http.DefaultTransport.(*http.Transport).Clone()
			t.Proxy = m.cfg.Network.ProxyURL
			t.ResponseHeaderTimeout = 30 * time.Second
			rt = t
		}
		u.client = update.NewHTTPClient(rt)
	}
	if u.installer == nil {
		u.installer = update.NewInstaller(update.System{GOOS: cfg.goos, Logger: log})
	}
	return u
}

// start runs the component's goroutine, which ends when m shuts down or close is called.
func (u *updateService) start() {
	ch, cancel := u.m.Subscribe()
	go u.run(ch, cancel)
}

// close stops the component and waits for its goroutine.
func (u *updateService) close() {
	u.stopOnce.Do(func() { close(u.stop) })
	<-u.done
}

// run is the component's goroutine: the automatic check (cfg.AutoCheck), restarted when
// the settings turn it on or change the feed; it ends when the notices end (Shutdown) or
// at close, and then removes the component from updateServices.
func (u *updateService) run(notices <-chan Notice, unsubscribe func()) {
	defer close(u.done)
	defer func() {
		updateServices.mu.Lock()
		if updateServices.m[u.m] == u {
			delete(updateServices.m, u.m)
		}
		updateServices.mu.Unlock()
	}()
	defer unsubscribe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-u.stop:
			cancel() // ends a check in progress
		case <-ctx.Done():
		}
	}()
	if !u.cfg.AutoCheck {
		for {
			select {
			case _, ok := <-notices:
				if !ok {
					return
				}
			case <-u.stop:
				return
			}
		}
	}
	for _, dir := range append([]string{u.cfg.DataDir}, u.cfg.OldDataDirs...) {
		if dir != "" {
			update.CleanDir(filepath.Join(dir, updatesDirName))
		}
	}

	var stopTimer func() bool
	arm := func(at time.Time) {
		if stopTimer != nil {
			stopTimer()
		}
		d := min(max(at.Sub(u.cfg.now()), 0), updateMaxWait)
		stopTimer = u.cfg.afterFunc(d, func() {
			select {
			case u.tick <- struct{}{}:
			default:
			}
		})
	}
	defer func() {
		if stopTimer != nil {
			stopTimer()
		}
	}()
	cur := u.m.Settings().Update
	next := u.cfg.now().Add(u.cfg.firstDelay)
	arm(next)
	for {
		select {
		case n, ok := <-notices:
			if !ok {
				return
			}
			if n.Kind != NoticeSettings {
				continue
			}
			s := n.Settings.Update
			if s.autoChecks() && (!cur.autoChecks() || s.FeedURL != cur.FeedURL) {
				next = u.cfg.now().Add(u.cfg.firstDelay)
				arm(next)
			}
			cur = s
		case <-u.tick:
			if u.cfg.now().Before(next) {
				arm(next)
				continue
			}
			u.autoCheck(ctx)
			next = u.cfg.now().Add(u.cfg.interval)
			arm(next)
		case <-u.stop:
			return
		}
	}
}

// autoCheck runs one automatic check (when the settings ask for it), hands its result to
// the Watch streams and notifies about an available version. A check that fails (no feed
// published yet, offline) is only logged: the user hears nothing of it.
func (u *updateService) autoCheck(ctx context.Context) {
	if !u.m.Settings().Update.autoChecks() {
		return
	}
	u.mu.Lock()
	info, res, err := u.checkLocked(ctx)
	u.mu.Unlock()
	if err != nil {
		switch {
		case ctx.Err() != nil:
		case errors.Is(err, update.ErrNotPublished):
			u.log.Info("automatic update check: no update information is published yet")
		default:
			u.log.Warn("automatic update check failed", "err", err)
		}
		return
	}
	u.log.Info("checked for updates", "current", info.GetCurrentVersion(), "latest", info.GetLatestVersion(),
		"available", info.GetAvailable())
	u.found.Store(info)
	u.m.hub.publishUpdate(info)
	if !res.Available {
		return
	}
	if u.cfg.Notify != nil {
		u.cfg.Notify(info)
	}
	// The tray and the menu-bar icon show it (cmd/downexd tray.go).
	u.m.notify(Notice{Kind: NoticeUpdateAvailable, Update: info})
}

// check is CheckForUpdate (also InstallUpdate's fresh check). Its result is the caller's
// only: it does not reach the Watch streams, so a version the user put off is offered
// again by the next automatic check only.
func (u *updateService) check(ctx context.Context) (*downexv1.UpdateInfo, update.Result, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.checkLocked(ctx)
}

// updateEvent is the Watch event of the latest automatic update check's result, nil before
// one worked (or without an update component).
func (m *Manager) updateEvent() *downexv1.Event {
	updateServices.mu.Lock()
	u := updateServices.m[m]
	updateServices.mu.Unlock()
	if u == nil {
		return nil
	}
	info := u.found.Load()
	if info == nil {
		return nil
	}
	return &downexv1.Event{Kind: &downexv1.Event_Update{Update: info}}
}

// checkLocked fetches and verifies the manifest of the configured feed and compares it
// with the running version. u.mu is held.
func (u *updateService) checkLocked(ctx context.Context) (*downexv1.UpdateInfo, update.Result, error) {
	if u.m.isClosed() {
		return nil, update.Result{}, errShuttingDown()
	}
	s := u.m.Settings().Update
	if s.FeedURL == "" {
		return nil, update.Result{}, errPrecondition("updates are off: no update feed is set (update_feed_url)")
	}
	keyText := u.cfg.PublicKey
	if keyText == "" {
		keyText = update.PublicKey
	}
	key, err := update.ParsePublicKey(keyText)
	if err != nil {
		return nil, update.Result{}, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	cur, err := update.ParseVersion(u.cfg.version)
	if err != nil {
		return nil, update.Result{}, errPrecondition("updates are disabled: this build (version %q) is not a release", u.cfg.version)
	}
	ctx, cancel := context.WithTimeout(ctx, updateCheckTimeout)
	defer cancel()
	feed := update.Feed{URL: s.FeedURL, Key: key, Client: u.client, UserAgent: u.userAgent()}
	res, err := feed.Check(ctx, cur, u.cfg.goos, u.cfg.goarch)
	if err != nil {
		return nil, update.Result{}, updateError(err)
	}
	info := &downexv1.UpdateInfo{
		CurrentVersion: u.cfg.version,
		LatestVersion:  res.Latest.String(),
		Available:      res.Available,
		Notes:          res.Manifest.Notes,
		CheckedAt:      timestamppb.New(u.cfg.now()),
	}
	if !res.Manifest.PublishedAt.IsZero() {
		info.PublishedAt = timestamppb.New(res.Manifest.PublishedAt)
	}
	return info, res, nil
}

func (u *updateService) userAgent() string { return "Downex/" + u.cfg.version }

// install is InstallUpdate. Everything that can fail while the daemon keeps running
// comes first: the installer's Prepare, a fresh check, the download (size and SHA-256)
// and the installer's Stage. Then every download is paused (Manager.Shutdown), the
// staged package is committed and shutdown (the daemon's graceful exit) is called,
// whatever Commit returned.
func (u *updateService) install(ctx context.Context, shutdown func()) (*downexv1.UpdateInfo, error) {
	if !u.installing.CompareAndSwap(false, true) {
		return nil, connect.NewError(connect.CodeAborted, errors.New("an update is already being installed"))
	}
	committed := false
	defer func() {
		if !committed {
			u.installing.Store(false)
		}
	}()
	if u.cfg.DataDir == "" {
		return nil, errPrecondition("this daemon has no data folder to download the update to")
	}
	if err := u.installer.Prepare(ctx); err != nil {
		return nil, updateError(err)
	}
	info, res, err := u.check(ctx)
	if err != nil {
		return nil, err
	}
	if !res.Available {
		if res.Newer {
			return nil, errPrecondition("Downex %s has no package for this system (%s/%s)", res.Latest, u.cfg.goos, u.cfg.goarch)
		}
		return nil, errPrecondition("Downex %s is up to date (the latest version is %s)", u.cfg.version, res.Latest)
	}
	dir := filepath.Join(u.cfg.DataDir, updatesDirName)
	pkg, err := update.Download(ctx, u.client, u.userAgent(), res.Asset, dir, u.installer.FileName(res.Latest))
	if err != nil {
		return nil, updateError(err)
	}
	staged, err := u.installer.Stage(ctx, pkg, res.Latest)
	if err != nil {
		_ = os.Remove(pkg)
		return nil, updateError(err)
	}
	if err := ctx.Err(); err != nil || u.m.isClosed() {
		staged.Discard()
		if err != nil {
			return nil, err
		}
		return nil, errShuttingDown()
	}
	committed = true
	u.log.Info("installing an update; downexd stops", "from", u.cfg.version, "to", res.Latest.String())
	if err := u.m.Shutdown(context.WithoutCancel(ctx)); err != nil {
		u.log.Warn("not every download paused in time; they are requeued on the next start", "err", err)
	}
	cerr := staged.Commit()
	shutdown()
	if cerr != nil {
		u.log.Error("the update could not be installed", "err", cerr)
		return nil, errInternal("install the update (downexd stops; start Downex again)", cerr)
	}
	return info, nil
}

// notPublishedMessage is what CheckForUpdate and InstallUpdate say when the feed has no
// manifest yet (update.ErrNotPublished: HTTP 404 or 410), in place of the HTTP status.
const notPublishedMessage = "No update information is published yet"

// notPublishedError says notPublishedMessage and keeps update.ErrNotPublished as its cause,
// so that the daemon's own callers (autoCheck) still recognise it.
type notPublishedError struct{}

func (notPublishedError) Error() string { return notPublishedMessage }
func (notPublishedError) Unwrap() error { return update.ErrNotPublished }

// updateError maps an error of internal/update to an RPC error.
func updateError(err error) error {
	var ce *connect.Error
	is := func(targets ...error) bool {
		for _, t := range targets {
			if errors.Is(err, t) {
				return true
			}
		}
		return false
	}
	switch {
	case errors.As(err, &ce):
		return err
	case errors.Is(err, context.DeadlineExceeded):
		return connect.NewError(connect.CodeDeadlineExceeded, err)
	case errors.Is(err, context.Canceled):
		return connect.NewError(connect.CodeCanceled, err)
	case is(update.ErrDisabled, update.ErrBadKey, update.ErrNotUpdatable, update.ErrInsecureURL):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case is(update.ErrNotPublished):
		return connect.NewError(connect.CodeNotFound, notPublishedError{})
	case is(update.ErrUnsupported):
		return connect.NewError(connect.CodeUnimplemented, err)
	case is(update.ErrCodeSignature):
		return connect.NewError(connect.CodePermissionDenied, err)
	case is(update.ErrSignature, update.ErrManifest, update.ErrVersion, update.ErrChecksum, update.ErrSize, update.ErrTooLarge):
		return connect.NewError(connect.CodeDataLoss, err)
	case is(update.ErrFetch):
		return connect.NewError(connect.CodeUnavailable, err)
	}
	return errInternal("update", err)
}

// CheckForUpdate fetches the update feed's manifest, verifies its signature and
// reports whether a newer version with a package for this system is available.
func (s daemonService) CheckForUpdate(ctx context.Context, _ *connect.Request[downexv1.CheckForUpdateRequest]) (*connect.Response[downexv1.CheckForUpdateResponse], error) {
	u, err := s.m.updateComponent()
	if err != nil {
		return nil, err
	}
	info, _, err := u.check(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&downexv1.CheckForUpdateResponse{Info: info}), nil
}

// InstallUpdate downloads and verifies the available update, pauses every download,
// starts the installation and stops the daemon (the new version starts by itself).
// Failures before the installation starts leave the daemon running.
func (s daemonService) InstallUpdate(ctx context.Context, _ *connect.Request[downexv1.InstallUpdateRequest]) (*connect.Response[downexv1.InstallUpdateResponse], error) {
	u, err := s.m.updateComponent()
	if err != nil {
		return nil, err
	}
	info, err := u.install(ctx, s.onShutdown)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&downexv1.InstallUpdateResponse{Info: info}), nil
}
