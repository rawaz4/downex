package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"downex/internal/browserfetch"
	"downex/internal/dlock"
	"downex/internal/dlview"
	"downex/internal/engine"
	"downex/internal/filecheck"
	"downex/internal/limiter"
	"downex/internal/model"
	"downex/internal/notify"
	"downex/internal/platform"
	"downex/internal/polite"
	"downex/internal/store"
	"downex/internal/transport"
)

// Timings.
const (
	// DefaultShutdownTimeout bounds how long Shutdown waits for running downloads to
	// pause and commit their progress (docs/DAEMON-SPEC.md §3).
	DefaultShutdownTimeout = 10 * time.Second
	// finalizeTimeout bounds the store work after a run stops.
	finalizeTimeout = 30 * time.Second
	// lockRetryInterval is how long a download that another process holds waits
	// before the scheduler tries it again.
	lockRetryInterval = 5 * time.Second
	// refreshTimeout bounds re-reading a record once its run has probed the resource.
	refreshTimeout = 5 * time.Second
)

// Config configures a Manager.
type Config struct {
	// Store persists downloads (required). The caller closes it after Shutdown.
	Store store.Store
	// Transport creates the fetchers of the shared engine (required).
	Transport transport.Factory
	// Engine configures the shared engine. DefaultMaxConnections is replaced by the
	// default_max_connections setting; a nil Logger by Logger.
	Engine engine.Options
	// SettingsPath is the settings file, normally SettingsFileName next to the
	// database. "" keeps settings in memory only.
	SettingsPath string
	// LockDir holds per-download lock files shared with the standalone CLI
	// (dlock.Dir of the database path). "" disables cross-process locking.
	LockDir string
	// DownloadsDir returns the default download directory; nil selects
	// platform.DownloadsDir.
	DownloadsDir func() (string, error)
	// Logger receives daemon events (default: discarded). Header values and request
	// bodies are never logged.
	Logger *slog.Logger
	// Version and Endpoint are reported by Status.
	Version  string
	Endpoint string
	// LogDir and ToolsDir are the folders of downexd.log and of the yt-dlp and Deno copies
	// (absolute; "" = not known, or no tools folder), reported by Status with the data
	// folder (the folder of SettingsPath) for the About page.
	LogDir, ToolsDir string
	// ShutdownTimeout bounds Shutdown (default DefaultShutdownTimeout).
	ShutdownTimeout time.Duration
	// LaunchAtLogin applies the launch_at_login setting (docs/DESKTOP-SPEC.md §5): New
	// calls it with the current value (verifying the login entry at daemon start) and
	// UpdateSettings whenever the value changes. downexd passes platform.SetLaunchAtLogin
	// for itself; nil leaves the system alone (tests, daemons of other profiles), and the
	// setting is then only stored.
	LaunchAtLogin func(enabled bool) error
	// OpenConfirmation asks the desktop UI to show the confirmation of the awaiting
	// download id (downexd starts "downex-ui --confirm <id>" when it is installed next
	// to it). Launches are coalesced to one per two seconds. nil: there is no UI, and
	// awaiting downloads are only reported (NoticeAwaiting with UILaunched false).
	OpenConfirmation func(id int64) error
	// OpenProgress opens the progress window of download id (docs/DESKTOP-SPEC.md §7;
	// downexd starts "downex-ui --progress <id>" when it is installed next to it). It is
	// called, outside any lock, while show_progress_window is on, for a download that
	// starts as the user asked (a hand-off that starts at once, a confirmation's Start with
	// progress_window, a rescue's or a renewal's first start), and once when a windowed run
	// completes or fails while show_complete_dialog is on (the window then shows the
	// download complete dialog, §8). nil: there is no window, and no run is windowed.
	OpenProgress func(id int64) error
	// ShowBrowserAdd shows the window of a download the user started in the browser
	// (source browser) above the browser, without stealing the keyboard focus when the
	// system does not grant it (docs/DESKTOP-SPEC.md §7 "Above the browser"): w is the
	// progress window as the download starts (AddProgressStarted), the confirmation, or the
	// progress window with the outcome of a windowed run (AddProgress, §8). downexd starts
	// the UI with "--raise". It replaces OpenProgress and OpenConfirmation for such a
	// download; nil: they are used as for other sources. Nothing ever shows the main window,
	// and a clipboard add keeps the plain hooks.
	ShowBrowserAdd func(id int64, w AddWindow) error
	// Now reads the clock that times the runs of downloads (started_at and active_ms);
	// nil selects time.Now. Tests pass a fake clock.
	Now func() time.Time
	// Media runs streaming-media downloads (AddMedia, ProbeMedia and the runs of
	// downloads of kind media; docs/MEDIA-SPEC.md §6). nil: AddMedia and ProbeMedia fail
	// with CodeUnimplemented and stored media downloads fail with media_unsupported.
	Media MediaJobs
	// Clock times the queue schedules and the countdown of post-queue actions
	// (docs/M6-SPEC.md §1); nil selects engine.RealClock. Tests pass an engine.FakeClock.
	Clock engine.Clock
	// Location is the zone schedules are read in; nil reads time.Local at every
	// evaluation (so that a changed system zone applies).
	Location *time.Location
	// Power performs the sleep, hibernate and shutdown post-queue actions; nil makes them
	// unavailable (validation refuses them). downexd passes the platform's for the
	// user's own daemon only; tests pass fakes.
	Power Power
	// Quit stops the daemon gracefully for the quit post-queue action (downexd: the same
	// request as the Shutdown RPC). It must not block. nil makes the action unavailable.
	Quit func()
	// Wake wakes the computer for scheduled starts of queues with wake_computer (Windows
	// waitable timer); nil: never.
	Wake WakeTimer
	// KeepAwake keeps the computer from sleeping while idle as long as a queue with a
	// post-queue action or a schedule has work, or a post-queue action counts down
	// (queue_awake.go); downexd passes the platform's power request for the user's own
	// daemon only. nil: the system's idle sleep is left alone.
	KeepAwake KeepAwake
	// Network is the live network configuration (docs/M6-SPEC.md §2): the proxy resolver,
	// the credentials and the host rules that downexd also gives the transport factory
	// (HTTPOptions.Resolver, Credentials) and the engine (Options.HostRules). The Manager
	// updates it from the settings at start, on every change of the network settings, on
	// Reevaluate and while a system or PAC proxy is in use every networkRefresh, and
	// keeps the passwords of SetSecret in its keyring. nil: the network settings are only
	// stored, and SetSecret and ClearSecret are Unimplemented.
	Network *NetworkRuntime
	// PostProcess marks and scans completed downloads before they are announced
	// (docs/M6-SPEC.md §3); nil selects NewPostProcessor (the platform's marking, the
	// configured scanner).
	PostProcess *PostProcessor
	// Site resolves web pages with yt-dlp and downloads their videos (AddMedia and
	// ProbeMedia with origin SITE, and the runs of site downloads;
	// docs/SITE-MEDIA-SPEC.md). nil: site media is unavailable (media_unsupported).
	Site SiteMedia
	// Played saves the videos the user's browser played (docs/SITE-MEDIA-SPEC.md §2.9);
	// nil refuses them (media_unsupported).
	Played PlayedMedia
	// Tools manages the yt-dlp and Deno copies Site runs (docs/SITE-MEDIA-SPEC.md §4): the
	// Manager configures it from the settings at start and on every change, waits for it
	// (Ensure) before resolving a page, serves GetTools and InstallTools with it and
	// streams its changes as Watch's tools events. Its automatic updates run only with
	// StartTools (the user's own daemon). nil: no tools manager; Site finds its tools
	// itself, and GetTools and InstallTools are unavailable.
	Tools Tools
	// PoliteProber sends Polite mode's probes and Peers lists the addresses downloads are
	// connected to (docs/POLITE-SPEC.md; downexd passes polite.NewSystemProber() and its
	// transport factory's PeerTable). Polite mode ticks on Clock. Without either, the
	// polite settings are only stored and Polite mode reports UNAVAILABLE while on.
	PoliteProber polite.Prober
	Peers        PeerSource
	// PoliteLoopback makes loopback addresses eligible anchors (end-to-end tests only).
	PoliteLoopback bool
}

// Manager owns the download queue of one daemon: the shared engine, the global speed
// limiter, the settings and one goroutine per running download. Its methods are safe
// for concurrent use. Create it with New and stop it with Shutdown.
type Manager struct {
	cfg     Config
	st      store.Store
	eng     *engine.Engine
	log     *slog.Logger
	global  limiter.Limiter
	hub     *hub
	notices noticeHub
	confirm *confirmer
	started time.Time
	now     func() time.Time // Config.Now: times the runs (started_at, active_ms)
	// setConns changes the connection limit of a running file download
	// (engine.SetMaxConnections; tests replace it). Its errors are only logged: the
	// limit is stored when the run stops and applies on the next start at the latest.
	setConns func(id model.DownloadID, n int) error

	settingsMu sync.Mutex // serialises UpdateSettings, including its file write
	mediaMu    sync.Mutex // serialises the name reservations of media downloads (freeName + create)
	// siteCookies holds the Cookie headers of site downloads, in memory only (site.go).
	siteCookies siteCookies
	// browser keeps the fetch tickets of site downloads a browser fetches for, and the
	// requests their runs hand to it (browser-fetched saves, docs/SITE-MEDIA-SPEC.md §2.8).
	browser *browserfetch.Broker
	// toolsCancel ends the subscription to Config.Tools' changes (nil without it).
	toolsCancel func()
	// polite is Polite mode (polite.go): the polite level of the limiter chain and its
	// monitor.
	polite politeState

	// Queues (docs/M6-SPEC.md §1; queue.go). queueMu serialises the queue operations
	// that write the store (it is taken before mu, never while holding it).
	qstore    store.QueueStore
	queueMu   sync.Mutex
	clock     engine.Clock  // Config.Clock: schedules and post-queue countdowns
	schedKick chan struct{} // capacity 1: re-evaluate the schedules (Reevaluate)
	schedDone chan struct{} // closed by Shutdown: ends the scheduler and the countdowns
	awakeKick chan struct{} // capacity 1: re-evaluate Config.KeepAwake (queue_awake.go)
	wakeMu    sync.Mutex
	wakeSet   time.Time // the scheduled start Config.Wake is armed for (zero: none)

	// M6 network and post-processing (network.go, postprocess.go).
	netKick    chan struct{} // capacity 1: update Config.Network soon (network.go)
	post       *PostProcessor
	base       context.Context // ends when Shutdown starts: bounds post-processing scans
	baseCancel context.CancelFunc

	mu      sync.Mutex
	entries map[model.DownloadID]*entry
	removed map[model.DownloadID]struct{} // ids whose records are gone (ids are never reused)
	// rescuing holds the ids of records a Rescue is adopting (rescue.go): no List or
	// lookup indexes them until it returns.
	rescuing  map[model.DownloadID]struct{}
	rescues   rescueLocks // one rescue per partial path at a time
	queues    map[model.QueueID]*queueState
	waiting   map[model.DownloadID]struct{} // queued downloads waiting to start (in their queue's order)
	active    map[model.DownloadID]*entry   // entries with a run, including runs that are stopping
	positions map[model.DownloadID]int      // queue positions of the waiting downloads (posDirty: stale)
	posDirty  bool
	saving    bool          // a saveQueues goroutine runs
	lastTurn  model.QueueID // the queue that started a download last (round robin)
	running   int           // len(active)
	runs      uint64        // runs started since the Manager started (Active)
	settings  Settings
	closed    bool
	retry     *time.Timer // wakes the scheduler for downloads deferred by a busy lock
	retryAt   time.Time

	wg       sync.WaitGroup // run goroutines and their helpers
	shutOnce sync.Once
	shutErr  error
}

// intent is what a run's goroutine does once engine.Run has returned. System intents
// never override user intents; among user intents the latest wins, except that a
// removal is final.
type intent uint8

const (
	intentNone     intent = iota
	intentRequeue         // system: back into the queue (max_concurrent_downloads lowered)
	intentShutdown        // system: persist as queued for the next start, do not schedule
	intentRestart         // user: discard progress (engine.Restart), then queue
	intentResume          // user: queue again (a Resume that overrides a pending Pause)
	intentPause           // user: paused
	intentRemove          // user: stopped for removal (left paused, never rescheduled)
)

// run is one engine.Run of a download.
type run struct {
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{} // closed once the outcome is applied
	lim     *chainLink    // the download's own limit, below its current queue's (queue_limit.go)
	lock    *dlock.Lock   // cross-process lock, nil without Config.LockDir
	recheck bool          // re-read the stored state first: another process held the download
	started time.Time     // when the run started (Manager.now)

	progress  atomic.Pointer[engine.Progress] // latest progress event (file downloads)
	mprogress atomic.Pointer[MediaProgress]   // latest progress event (media downloads)
	refreshed atomic.Bool                     // the record was re-read after the probe

	// Guarded by Manager.mu.
	intent     intent
	finalizing bool
	// windowed: a progress window shows this run (downexd opened it as the run started, a
	// window of the user's marked it, or it was open on the download before the run
	// started: entry.next). Only such a run gets the download complete dialog: its outcome
	// shows in that window, brought above the browser for a browser download, and the tray
	// stays quiet about it (docs/DESKTOP-SPEC.md §8). shown: a window of the user's marked
	// it (a failed launch of downexd's keeps the mark then); marked is the completion_dialog
	// that markWindowedLocked set for it (unmarkWindowed takes back only that one).
	windowed bool
	shown    bool
	marked   *bool
	stopped  time.Time // when the run stopped (set with finalizing): the end of its active time
}

func (r *run) setIntent(i intent) {
	switch {
	case r.intent == intentRemove:
	case i >= intentRestart:
		r.intent = i
	case r.intent == intentNone || r.intent == intentRequeue:
		r.intent = i
	}
}

// entry is the daemon's view of one download.
type entry struct {
	id model.DownloadID
	op chan struct{} // capacity 1: held by the client operation acting on this download

	// Guarded by Manager.mu.
	rec        model.Download // the stored record (refreshed after each run and the probe)
	qid        model.QueueID  // its queue (authoritative: rec.QueueID may be older)
	received   int64          // durable bytes while stopped
	errCode    string         // stable code of the last failure
	foreign    bool           // created or held by another process; adopted on the first operation
	deferUntil time.Time      // another process held the lock: do not start before
	recheck    bool           // re-read the stored state before the next start
	stale      bool           // rec may be older than the store's copy: re-read it before acting
	parked     bool           // out of the waiting set while an operation writes its record (parkLocked)
	run        *run
	pendMax    *int   // max_connections to store when the current run stops
	pendSpeed  *int64 // speed_limit to store when the current run stops
	pendDialog *bool  // completion_dialog to store when the current run stops
	// early is the EARLY file check of the current or last run (docs/DESKTOP-SPEC.md
	// §9.1): set by checkEarly while that run is the entry's run, cleared when a run
	// starts; never stored.
	early *model.FileCheck
	// Expired links (renew.go): check is the hold of a new add whose link is being
	// checked against downloads whose link expired; offer an awaiting add's offer to
	// continue one of them instead. Both in memory only.
	check *renewCheck
	offer *renewOffer
	// rescueWait is when a rescue stored paused for the browser relay was made: its first
	// start is ResumeRequest.rescue_start (rescue.go), until RescueStartWindow has passed.
	// In memory only (zero = none).
	rescueWait time.Time
	// next: a progress window is open on the stopped (or queued) download, so its next run
	// is windowed as it starts (startLocked; browseradd.go). In memory only.
	next nextWindow
}

func newEntry(rec model.Download) *entry {
	e := &entry{id: rec.ID, rec: rec, qid: rec.QueueID, op: make(chan struct{}, 1)}
	if rec.State == model.StateFailed {
		e.errCode = recordErrorCode(&rec)
	}
	if rec.State == model.StateCompleted {
		e.received = max(rec.Size, 0)
	}
	return e
}

// New loads the settings and every stored download, requeues downloads left
// downloading or probing by a crash or kill (unless another process is running
// them), keeps downloads awaiting confirmation as they are, applies launch_at_login
// (Config.LaunchAtLogin) and starts the scheduler.
func New(ctx context.Context, cfg Config) (*Manager, error) {
	if cfg.Store == nil || cfg.Transport == nil {
		return nil, errors.New("daemon: Config.Store and Config.Transport are required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	if cfg.DownloadsDir == nil {
		cfg.DownloadsDir = platform.DownloadsDir
	}
	if cfg.ShutdownTimeout <= 0 {
		cfg.ShutdownTimeout = DefaultShutdownTimeout
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Clock == nil {
		cfg.Clock = engine.RealClock()
	}
	qs, ok := cfg.Store.(store.QueueStore)
	if !ok {
		return nil, fmt.Errorf("daemon: the store (%T) does not persist queues", cfg.Store)
	}
	m := &Manager{
		cfg: cfg, st: cfg.Store, log: cfg.Logger, hub: newHub(), started: time.Now(), now: cfg.Now,
		qstore: qs, clock: cfg.Clock, schedKick: make(chan struct{}, 1), schedDone: make(chan struct{}),
		entries: make(map[model.DownloadID]*entry), removed: make(map[model.DownloadID]struct{}),
		rescuing: make(map[model.DownloadID]struct{}),
		queues:   make(map[model.QueueID]*queueState), waiting: make(map[model.DownloadID]struct{}),
		active: make(map[model.DownloadID]*entry), netKick: make(chan struct{}, 1), post: cfg.PostProcess,
		awakeKick: make(chan struct{}, 1), browser: browserfetch.New(),
	}
	m.base, m.baseCancel = context.WithCancel(context.Background())
	if m.post == nil {
		m.post = NewPostProcessor(m.log)
	}
	raw, err := loadSettings(cfg.SettingsPath)
	if err != nil {
		m.log.Warn("ignoring unreadable settings file; using defaults", "path", cfg.SettingsPath, "err", err)
	}
	s, problems := raw.repair(cfg.DownloadsDir)
	for _, p := range problems {
		m.log.Warn("invalid setting replaced by its default", "path", cfg.SettingsPath, "problem", p)
	}
	m.settings = s
	m.global = limiter.New(s.GlobalSpeedLimit)
	m.newPolite()
	m.confirm = &confirmer{m: m, launch: cfg.OpenConfirmation, interval: confirmLaunchInterval}
	if cfg.LaunchAtLogin != nil {
		if err := cfg.LaunchAtLogin(s.LaunchAtLogin); err != nil {
			m.log.Warn("cannot apply the launch_at_login setting", "launch_at_login", s.LaunchAtLogin, "err", err)
		}
	}

	opts := cfg.Engine
	opts.DefaultMaxConnections = s.DefaultMaxConnections
	if opts.Logger == nil {
		opts.Logger = m.log.With("component", "engine")
	}
	m.eng = engine.New(cfg.Store, cfg.Transport, opts)
	m.setConns = m.eng.SetMaxConnections

	if err := m.loadQueues(ctx); err != nil {
		m.baseCancel()
		return nil, err
	}
	if err := m.recover(ctx); err != nil {
		m.baseCancel()
		return nil, err
	}
	// The proxy and the host rules apply before the first download starts; a proxy that
	// cannot be set up in time (an unreachable PAC script) is retried in the background
	// and connections go direct meanwhile.
	netOK := m.refreshNetwork(networkStartTimeout) == nil
	m.mu.Lock()
	m.scheduleLocked()
	m.mu.Unlock()
	m.wg.Add(1)
	go m.runScheduler()
	if cfg.KeepAwake != nil {
		m.wg.Add(1)
		go m.runAwake()
	}
	if cfg.Network != nil {
		m.wg.Add(1)
		go m.runNetwork(!netOK)
	}
	if cfg.Tools != nil {
		m.startToolsSettings()
	}
	m.startPolite()
	return m, nil
}

// recover builds the index from the store (docs/DAEMON-SPEC.md §3).
func (m *Manager) recover(ctx context.Context) error {
	recs, err := m.st.ListDownloads(ctx)
	if err != nil {
		return fmt.Errorf("daemon: list downloads: %w", err)
	}
	requeued := 0
	// The stored queue orders list every download; one in no known queue joins the
	// main queue.
	listed := make(map[model.DownloadID]model.QueueID)
	for _, q := range m.queues {
		for _, id := range q.order {
			listed[id] = q.id()
		}
	}
	for i := range recs {
		e := newEntry(recs[i])
		if qid, ok := listed[e.id]; ok {
			e.qid = qid
		} else {
			m.joinQueueLocked(e, e.rec.QueueID)
		}
		if e.received, err = m.storedBytes(ctx, &e.rec); err != nil {
			return fmt.Errorf("daemon: load download %d: %w", e.id, err)
		}
		if med := e.rec.Media; med != nil && med.FetchTicket != "" && e.rec.State != model.StateCompleted {
			// The browser that added it may still hold its ticket: it serves the download
			// again (docs/SITE-MEDIA-SPEC.md §2.8, amendment 2026-11-19).
			if err := m.browser.Restore(e.id, med.FetchTicket); err != nil {
				m.log.Warn("cannot restore the fetch ticket of a download", "download", int64(e.id), "err", err)
			}
		}
		switch e.rec.State {
		case model.StateProbing, model.StateDownloading:
			if m.lockedElsewhere(e.id) {
				e.foreign = true
				break
			}
			e.rec.State = model.StateQueued
			e.rec.UpdatedAt = time.Now()
			if err := m.st.UpdateDownload(ctx, &e.rec); err != nil {
				return fmt.Errorf("daemon: requeue download %d: %w", e.id, err)
			}
			m.enqueueLocked(e.id)
			requeued++
		case model.StateQueued:
			m.enqueueLocked(e.id)
		}
		m.entries[e.id] = e
	}
	// Orders may name downloads deleted between the two reads.
	for _, q := range m.queues {
		q.order = slices.DeleteFunc(q.order, func(id model.DownloadID) bool { return m.entries[id] == nil })
	}
	m.posDirty = true
	m.log.Info("downloads loaded", "count", len(recs), "queued", len(m.waiting), "requeued", requeued,
		"queues", len(m.queues))
	return nil
}

// storedBytes returns the durable bytes of a stopped download (dlview.Received), reading
// its segments only when that needs them.
func (m *Manager) storedBytes(ctx context.Context, d *model.Download) (int64, error) {
	var segs []model.Segment
	if dlview.NeedsSegments(d) {
		var err error
		if segs, err = m.st.LoadSegments(ctx, d.ID); err != nil {
			return 0, err
		}
	}
	return dlview.Received(d, segs), nil
}

// lockedElsewhere reports whether another process holds download id's lock.
func (m *Manager) lockedElsewhere(id model.DownloadID) bool {
	if m.cfg.LockDir == "" {
		return false
	}
	lk, err := dlock.Acquire(m.cfg.LockDir, id)
	if err != nil {
		if !errors.Is(err, dlock.ErrBusy) {
			m.log.Warn("cannot check download lock", "download", int64(id), "err", err)
		}
		return errors.Is(err, dlock.ErrBusy)
	}
	lk.Release()
	return false
}

// ---- runs --------------------------------------------------------------------------

// armRetryLocked makes sure the scheduler runs again at t (zero: not needed).
func (m *Manager) armRetryLocked(t time.Time) {
	if t.IsZero() || (m.retry != nil && !m.retryAt.After(t)) {
		return
	}
	if m.retry != nil {
		m.retry.Stop()
	}
	m.retryAt = t
	m.retry = time.AfterFunc(time.Until(t), func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.retry, m.retryAt = nil, time.Time{}
		m.scheduleLocked()
	})
}

// startLocked starts e's run goroutine. It returns false, deferring e, when another
// process holds the download.
func (m *Manager) startLocked(e *entry, now time.Time) bool {
	var lk *dlock.Lock
	if m.cfg.LockDir != "" {
		l, err := dlock.Acquire(m.cfg.LockDir, e.id)
		if err != nil {
			if errors.Is(err, dlock.ErrBusy) {
				m.log.Debug("download is in use by another process; retrying later", "download", int64(e.id))
			} else {
				m.log.Warn("cannot lock download; retrying later", "download", int64(e.id), "err", err)
			}
			e.deferUntil = now.Add(lockRetryInterval)
			e.recheck = true
			return false
		}
		lk = l
	}
	m.dequeueLocked(e.id)
	q := m.queueOfLocked(e)
	if q.post != nil { // the queue has work again
		m.cancelPostLocked(q, "a download of the queue started")
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &run{
		ctx: ctx, cancel: cancel, done: make(chan struct{}),
		lim: newChainLink(e.rec.SpeedLimit, q.lim), lock: lk, recheck: e.recheck, started: m.now(),
	}
	r.lim.setMeter(&m.polite.rx)
	e.run, e.recheck, e.deferUntil, e.early = r, false, time.Time{}, nil
	if e.next != nextNone { // a progress window waits for this run (browseradd.go)
		m.markWindowedLocked(e, e.next == nextShown)
		e.next = nextNone
	}
	m.active[e.id] = e
	m.running++
	m.runs++
	// Shown until the engine reports progress: a ranged download resumes at once,
	// anything else probes first.
	if e.rec.Mode == model.ModeRanged {
		e.rec.State = model.StateDownloading
	} else {
		e.rec.State = model.StateProbing
	}
	e.rec.Error, e.errCode = "", ""
	m.wg.Add(1)
	go m.runDownload(e, r)
	m.hub.publish(e.id, kindUpdated)
	m.politeWake()
	return true
}

// runDownload is the goroutine of one run.
func (m *Manager) runDownload(e *entry, r *run) {
	defer m.wg.Done()
	if r.recheck {
		// Another process held this download; if it stopped it meanwhile (paused,
		// completed, failed), keep that outcome instead of running it again.
		ctx, cancel := context.WithTimeout(context.Background(), refreshTimeout)
		rec, err := m.st.GetDownload(ctx, e.id)
		cancel()
		if err == nil && !activeState(rec.State) {
			m.finish(e, r, nil, true)
			return
		}
	}
	m.mu.Lock()
	kind := e.rec.Kind
	m.mu.Unlock()
	if kind == model.KindMedia {
		m.finish(e, r, m.runMedia(e, r), false)
		return
	}
	onProgress := func(p engine.Progress) {
		head := p.Head
		p.Head = nil // the stored progress never keeps the file's bytes
		r.progress.Store(&p)
		if head != nil {
			m.wg.Add(1)
			go m.checkEarly(e, r, head)
		}
		if !r.refreshed.Swap(true) {
			m.wg.Add(1)
			go m.refreshLive(e, r)
		}
		m.hub.publish(e.id, kindUpdated)
	}
	m.log.Debug("download started", "download", int64(e.id))
	err := m.eng.Run(r.ctx, e.id, r.lim, onProgress)
	m.finish(e, r, err, false)
}

// activeState reports the states of a download that waits for or is in a run.
func activeState(s model.State) bool {
	return s == model.StateQueued || s == model.StateProbing || s == model.StateDownloading
}

// refreshLive re-reads a running download's record after its first progress event,
// when the engine has stored the probe's outcome (name, size, mode, final URL).
func (m *Manager) refreshLive(e *entry, r *run) {
	defer m.wg.Done()
	// A connection limit set before the engine had started this run is applied now.
	m.applyPendingConns(e, r)
	ctx, cancel := context.WithTimeout(context.Background(), refreshTimeout)
	defer cancel()
	rec, err := m.st.GetDownload(ctx, e.id)
	if err != nil {
		// Try again on the next progress event: without the probe's outcome the
		// cached record lacks the name, size, mode and validators.
		m.log.Debug("cannot re-read running download", "download", int64(e.id), "err", err)
		r.refreshed.Store(false)
		return
	}
	m.mu.Lock()
	// A record the run has already ended (the engine stores the completion just before
	// Run returns, and a small file's first progress event is its last) is left to
	// finish, which tells by the cached state whether this run ended the download.
	if e.run == r && !r.finalizing {
		if activeState(rec.State) {
			e.rec = *rec
		} else {
			// Read again at the next progress event, if any: a single-connection download
			// that failed is retried by the same Run, which probes again.
			r.refreshed.Store(false)
		}
	}
	m.mu.Unlock()
	m.hub.publish(e.id, kindUpdated)
}

// checkEarly runs the EARLY file check of run r on head, the file's first bytes
// (engine.Progress.Head), and keeps the result with the entry while r is its run
// (docs/DESKTOP-SPEC.md §9.1). The record is read again for the name and served type
// the probe stored. Nothing happens while check_downloaded_files is off.
func (m *Manager) checkEarly(e *entry, r *run, head []byte) {
	defer m.wg.Done()
	m.mu.Lock()
	on := m.settings.Desktop.CheckFiles
	m.mu.Unlock()
	if !on {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), refreshTimeout)
	rec, err := m.st.GetDownload(ctx, e.id)
	cancel()
	if err != nil {
		m.log.Debug("cannot read the download for its early file check", "download", int64(e.id), "err", err)
		return
	}
	served := rec.ServedType
	if served == "" {
		served = rec.MIME
	}
	fc := filecheck.CheckHead(rec.Filename, rec.NameControls, served, head)
	m.mu.Lock()
	current := e.run == r && !r.finalizing
	if current {
		e.early = fc
	}
	m.mu.Unlock()
	if !current {
		return
	}
	if fc != nil {
		m.log.Info("early file check", "download", int64(e.id), "level", int(fc.Level), "reason", fc.Reason, "content", fc.Content)
	}
	m.hub.publish(e.id, kindUpdated)
}

// applyPendingConns hands a connection limit that SetDownloadOptions or Resume set
// during run r of file download e to the engine (it is stored when the run stops).
// It is never called from an engine callback.
func (m *Manager) applyPendingConns(e *entry, r *run) {
	m.mu.Lock()
	var n int
	ok := e.run == r && !r.finalizing && e.pendMax != nil && e.rec.Kind == model.KindFile
	if ok {
		n = *e.pendMax
	}
	m.mu.Unlock()
	if ok {
		m.applyConns(e.id, n)
	}
}

// applyConns asks the engine to run download id with at most n connections (0 = the
// default_max_connections setting) from now on. Failures (the run has not started or
// has already stopped) are only logged.
func (m *Manager) applyConns(id model.DownloadID, n int) {
	if n <= 0 {
		n = m.Settings().DefaultMaxConnections
	}
	switch err := m.setConns(id, n); {
	case err == nil:
	case errors.Is(err, engine.ErrNotRunning): // starting or stopping: stored with the run's end
		m.log.Debug("connection limit applies on the next start", "download", int64(id), "max_connections", n)
	default:
		m.log.Warn("cannot change the connections of a running download; the limit applies on the next start",
			"download", int64(id), "max_connections", n, "err", err)
	}
}

// finish applies the outcome of a run once engine.Run has returned. skipped means Run
// was not called because another process had already stopped the download.
func (m *Manager) finish(e *entry, r *run, runErr error, skipped bool) {
	m.mu.Lock()
	r.finalizing, r.stopped = true, m.now()
	in := r.intent
	// The pending options stay visible to clients until the stored record replaces the
	// cached one below; no operation sets new ones while the run is finalizing.
	pend := pendingOptions{max: e.pendMax, speed: e.pendSpeed, dialog: e.pendDialog}
	cached, received := e.rec, e.received
	m.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), finalizeTimeout)
	defer cancel()
	before := cached.State
	rec, err := m.st.GetDownload(ctx, e.id)
	gone := errors.Is(err, store.ErrNotFound)
	stale := false
	var (
		code string
		post PostResult // of a download this run completed
	)
	switch {
	case gone:
	case err != nil:
		// Without the engine's final record nothing is written: the cached copy may be
		// older than what the engine stored (mode, size, validators, reserved name).
		// The next start recovers the download from whatever the store holds.
		// The entry is marked stale, so the next operation reads the record again
		// before it writes anything (see Manager.freshen).
		m.log.Warn("cannot read download after its run", "download", int64(e.id), "err", err)
		rec, stale = &cached, true
		if runErr != nil && !isContextError(runErr) {
			rec.State, rec.Error, code = model.StateFailed, runErr.Error(), runErrorCode(rec.Kind, runErr)
		} else {
			rec.State = model.StatePaused
		}
		// Shown until the next operation re-reads the stored record.
		if !skipped {
			accountRun(rec, r)
		}
		if pend.dialog != nil {
			rec.CompletionDialog = *pend.dialog
		}
	default:
		code = m.settle(ctx, rec, r, in, runErr, skipped, pend)
		if received, err = m.storedBytes(ctx, rec); err != nil {
			m.log.Warn("cannot read download progress", "download", int64(e.id), "err", err)
		}
		if !skipped && in != intentRemove && before != model.StateCompleted && rec.State == model.StateCompleted {
			post = m.postProcess(rec)
		}
	}
	// Never from the cached copy (stale): its checkpoint can lag the stored one, so resumable
	// fragments would look dead.
	if !gone && !stale && !skipped && in != intentRemove && before != model.StateFailed && rec.State == model.StateFailed {
		m.dropDeadParts(rec) // before the lock goes, so no new run is using them
	}
	r.lock.Release()

	// The run ended the download: completed or failed (not removed, paused or requeued).
	ended := !gone && !skipped && in != intentRemove && rec.State != before &&
		(rec.State == model.StateCompleted || rec.State == model.StateFailed)
	m.mu.Lock()
	e.run = nil
	e.pendMax, e.pendSpeed, e.pendDialog = nil, nil, nil
	delete(m.active, e.id)
	m.running--
	q := m.queueOfLocked(e)
	if ended && rec.State == model.StateCompleted {
		q.completed++
		m.dropOffersLocked(e.id)
	}
	if gone { // deleted by another process
		m.forgetLocked(e.id)
	} else {
		e.rec, e.received, e.errCode, e.stale = *rec, received, code, stale
		if rec.State == model.StateQueued && in != intentRemove && in != intentShutdown && !m.closed {
			m.enqueueLocked(e.id)
		}
	}
	// The download complete dialog (docs/DESKTOP-SPEC.md §8): once per completion or
	// failure of a run that a progress window showed or was asked for (r.windowed; the
	// stored completion_dialog of an earlier run does not count: a queue, scheduler or CLI
	// start that showed no window gets the tray notification only). Its outcome shows in
	// that window, above the browser for a browser download, and the tray stays quiet
	// about it.
	dialog := ended && r.windowed && m.settings.ShowCompleteDialog && m.cfg.OpenProgress != nil && !m.closed
	inWindow := dialog
	var outcome Notice // the notification left to the window (sent after all if it cannot open)
	if dialog {
		m.wg.Add(1)
	}
	m.scheduleLocked()
	if ended {
		// Before a post-queue action's notice that this completion may cause.
		switch rec.State {
		case model.StateCompleted:
			pd := m.protoLocked(e)
			outcome = Notice{Kind: NoticeCompleted, Download: pd}
			m.notify(Notice{Kind: NoticeCompleted, Download: pd, InWindow: inWindow})
			if text, ok := post.FileCheckNotice(notify.DisplayName(pd), m.settings.NotifyOnComplete); ok {
				m.notify(Notice{Kind: NoticeFileCheck, Download: pd, Text: text, Level: post.Check.Level})
			}
			if post.Alarming() {
				m.notify(Notice{Kind: NoticeScanProblem, Download: pd, Text: post.NotificationText(notify.DisplayName(pd))})
			}
		case model.StateFailed:
			pd := m.protoLocked(e)
			outcome = Notice{Kind: NoticeFailed, Download: pd}
			m.notify(Notice{Kind: NoticeFailed, Download: pd, InWindow: inWindow})
		}
	}
	if ended { // a pause, removal or requeue never finishes a queue
		m.checkFinishedLocked(q)
	}
	m.mu.Unlock()
	r.cancel()
	close(r.done)

	if gone {
		m.hub.publish(e.id, kindRemoved)
		return
	}
	m.hub.publish(e.id, kindUpdated)
	if dialog {
		src := rec.Source
		go func() {
			defer m.wg.Done()
			if err := m.openProgressFor(int64(e.id), src, false); err != nil {
				m.log.Warn("cannot open the download complete dialog", "download", int64(e.id), "err", err)
				if outcome.Download != nil {
					m.notify(outcome) // the outcome is not lost: the tray tells it
				}
			}
		}()
	}
	switch rec.State {
	case model.StateCompleted:
		// active_ms: the running time of all runs, pauses excluded; avg_bps: the bytes over it.
		m.log.Info("download completed", "download", int64(e.id), "bytes", rec.Size,
			"active_ms", rec.ActiveMS, "avg_bps", dlview.AverageBps(rec.Size, rec.ActiveMS))
	case model.StateFailed:
		m.log.Warn("download failed", "download", int64(e.id), "error_code", code, "err", rec.Error)
	default:
		m.log.Debug("download stopped", "download", int64(e.id), "state", rec.State.String())
	}
}

// pendingOptions are the options that SetDownloadOptions or Resume set during a run,
// stored when it stops (nil: unchanged).
type pendingOptions struct {
	max    *int
	speed  *int64
	dialog *bool
}

// accountRun adds the active time of run r, which has stopped, to rec and records the
// run's start as the download's first one when none is known.
func accountRun(rec *model.Download, r *run) {
	if rec.StartedAt.IsZero() {
		rec.StartedAt = r.started
	}
	rec.ActiveMS += runMillis(r.started, r.stopped)
}

// runMillis is the active time of a run from start to end in whole milliseconds (0 when
// the clock went backwards).
func runMillis(start, end time.Time) int64 {
	return max(end.Sub(start).Milliseconds(), 0)
}

// settle turns the record the engine left behind into the state the run's intent asks
// for, adds run r's active time, stores it and returns the failure's error code (""
// unless failed).
func (m *Manager) settle(ctx context.Context, rec *model.Download, r *run, in intent, runErr error, skipped bool,
	pend pendingOptions) string {
	want := rec.State
	restarted := false // this run's bytes were discarded, and so is its time
	switch {
	case skipped || rec.State == model.StateCompleted:
	case in == intentRestart:
		if err := m.restartDownload(ctx, rec.ID, engine.RestartOptions{}); err != nil {
			m.log.Warn("cannot restart download", "download", int64(rec.ID), "err", err)
			want = model.StatePaused
			break
		}
		fresh, err := m.st.GetDownload(ctx, rec.ID)
		if err != nil {
			// Restart stored the reset record as queued; writing the old copy over it
			// would bring back the discarded validators.
			m.log.Warn("cannot read restarted download", "download", int64(rec.ID), "err", err)
			rec.State, rec.Error = model.StateQueued, ""
			return ""
		}
		*rec = *fresh
		want, restarted = model.StateQueued, true
	case rec.State == model.StateFailed:
		// A real failure wins over a concurrent pause, requeue or shutdown.
	case in == intentPause || in == intentRemove:
		want = model.StatePaused
	case in == intentShutdown || in == intentRequeue || in == intentResume:
		want = model.StateQueued
	case runErr != nil && isContextError(runErr):
		want = model.StatePaused
	case runErr != nil:
		// Run gave up without recording it (for example its first store access failed).
		want, rec.Error = model.StateFailed, runErr.Error()
	default:
		want, rec.Error = model.StateFailed, "download stopped unexpectedly"
	}
	dirty := want != rec.State
	rec.State = want
	if !skipped && !restarted {
		// Neither the engine nor a media run changes these fields: rec holds the totals
		// of the earlier runs.
		accountRun(rec, r)
		dirty = true
	}
	if pend.max != nil && rec.MaxConnections != *pend.max {
		rec.MaxConnections, dirty = *pend.max, true
	}
	if pend.speed != nil && rec.SpeedLimit != *pend.speed {
		rec.SpeedLimit, dirty = *pend.speed, true
	}
	if pend.dialog != nil && rec.CompletionDialog != *pend.dialog {
		rec.CompletionDialog, dirty = *pend.dialog, true
	}
	if dirty {
		rec.UpdatedAt = time.Now()
		if err := m.st.UpdateDownload(ctx, rec); err != nil {
			m.log.Warn("cannot save download state", "download", int64(rec.ID), "err", err)
		}
	}
	if rec.State != model.StateFailed {
		return ""
	}
	if runErr != nil && !isContextError(runErr) {
		return runErrorCode(rec.Kind, runErr)
	}
	return recordErrorCode(rec)
}

// Shutdown pauses every running download, waits (bounded by ctx and
// Config.ShutdownTimeout) until their progress is committed, persists them as queued
// so the next start resumes them, and ends all Watch streams and Subscribe channels. Later calls return the
// first call's result; afterwards mutating calls fail with CodeUnavailable. The store
// stays open.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.shutOnce.Do(func() { m.shutErr = m.shutdown(ctx) })
	return m.shutErr
}

func (m *Manager) shutdown(ctx context.Context) error {
	m.stopPolite() // no probe and no cap while the downloads pause
	m.mu.Lock()
	m.closed = true
	close(m.schedDone) // the scheduler, the network updates and the post-queue countdowns end
	m.baseCancel()     // a scan in progress stops; its download stays completed with a note
	for _, q := range m.queues {
		if p := q.post; p != nil {
			q.post = nil
			close(p.cancel)
		}
	}
	if m.retry != nil {
		m.retry.Stop()
		m.retry = nil
	}
	m.confirm.close()
	stopping := 0
	for _, e := range m.entries {
		if r := e.run; r != nil {
			r.setIntent(intentShutdown)
			r.cancel()
			stopping++
		}
	}
	m.mu.Unlock()
	m.log.Info("shutting down", "pausing", stopping)

	ctx, cancel := context.WithTimeout(ctx, m.cfg.ShutdownTimeout)
	defer cancel()
	idle := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(idle)
	}()
	var err error
	select {
	case <-idle:
	case <-ctx.Done():
		m.mu.Lock()
		n := m.running
		m.mu.Unlock()
		err = fmt.Errorf("daemon: %d downloads did not pause in time: %w", n, ctx.Err())
	}
	if m.toolsCancel != nil {
		m.toolsCancel()
	}
	m.hub.close()
	m.closeNotices()
	return err
}

// isClosed reports whether Shutdown has started.
func (m *Manager) isClosed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
}
