package daemon

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"
)

// The tools of site media (docs/SITE-MEDIA-SPEC.md §4) as the Manager drives them:
// Config.Tools follows the site-media settings (startToolsSettings, every daemon with
// Config.Tools), and StartTools runs the automatic update check of the user's own daemon,
// timed like the update check (update_service.go).

// Tools update timings (docs/SITE-MEDIA-SPEC.md §4.5).
const (
	// toolsFirstDelay is how long after StartTools (and after the settings turn automatic
	// updates on) the first check runs, give or take toolsFirstJitter.
	toolsFirstDelay  = 5 * time.Minute
	toolsFirstJitter = time.Minute
	// toolsInterval is the time between checks, give or take toolsJitter.
	toolsInterval = 24 * time.Hour
	toolsJitter   = time.Hour
	// toolsCheckTimeout bounds one check, including the install of a newer release.
	toolsCheckTimeout = 15 * time.Minute
)

// startToolsSettings hands the site-media settings to Config.Tools now and after every
// change, and streams its changes to Watch (the hub's tools events). New calls it once;
// the settings goroutine ends at Shutdown, which also ends the subscription to the tools.
func (m *Manager) startToolsSettings() {
	t := m.cfg.Tools
	// Subscribe before reading the settings, so that no change in between is missed.
	notices, unsubscribe := m.Subscribe()
	cur := m.Settings().Media
	t.Configure(cur.SiteMedia, cur.ToolsAutoUpdate, cur.channel())
	m.toolsCancel = t.Subscribe(m.hub.publishTools)
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer unsubscribe()
		for {
			select {
			case n, ok := <-notices:
				if !ok {
					return
				}
				if n.Kind != NoticeSettings || n.Settings.Media == cur {
					continue
				}
				cur = n.Settings.Media
				t.Configure(cur.SiteMedia, cur.ToolsAutoUpdate, cur.channel())
			case <-m.schedDone:
				return
			}
		}
	}()
}

// autoUpdates reports whether the settings ask for automatic tools updates.
func (s MediaSettings) autoUpdates() bool { return s.SiteMedia && s.ToolsAutoUpdate }

// ToolsServiceConfig configures StartTools. Its fields are for tests: the zero value is
// the product's timing.
type ToolsServiceConfig struct {
	now        func() time.Time
	afterFunc  func(d time.Duration, f func()) (stop func() bool)
	jitter     func(limit time.Duration) time.Duration // uniform in [-limit, limit]
	firstDelay time.Duration
	interval   time.Duration
}

// toolsService is the automatic update check of one Manager's tools.
type toolsService struct {
	m    *Manager
	cfg  ToolsServiceConfig
	log  *slog.Logger
	tick chan struct{} // the timer fired

	stop     chan struct{}
	stopOnce sync.Once
	done     chan struct{} // the goroutine has ended
}

// StartTools runs the automatic update check of m's Config.Tools
// (docs/SITE-MEDIA-SPEC.md §4.5): toolsFirstDelay (±1 min) after the call, then every 24
// hours (±1 h), while site media and tools_auto_update are on; when the settings turn
// them on, the next check follows toolsFirstDelay later. Each check is Tools.CheckUpdate.
// Only the user's own daemon calls it (downexd, right after New): other daemons never
// download tools. It runs until m shuts down or stop is called (stop waits for it; it may
// be called more than once). Without Config.Tools it does nothing.
func StartTools(m *Manager, cfg ToolsServiceConfig) (stop func()) {
	if m.cfg.Tools == nil {
		return func() {}
	}
	if cfg.now == nil {
		cfg.now = time.Now
	}
	if cfg.afterFunc == nil {
		cfg.afterFunc = func(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop }
	}
	if cfg.jitter == nil {
		cfg.jitter = func(limit time.Duration) time.Duration {
			if limit <= 0 {
				return 0
			}
			return time.Duration(rand.Int64N(int64(2*limit)+1)) - limit
		}
	}
	if cfg.firstDelay <= 0 {
		cfg.firstDelay = toolsFirstDelay
	}
	if cfg.interval <= 0 {
		cfg.interval = toolsInterval
	}
	s := &toolsService{
		m: m, cfg: cfg, log: m.log.With("component", "tools"), tick: make(chan struct{}, 1),
		stop: make(chan struct{}), done: make(chan struct{}),
	}
	notices, unsubscribe := m.Subscribe()
	go s.run(notices, unsubscribe)
	return s.close
}

// close stops the service and waits for its goroutine.
func (s *toolsService) close() {
	s.stopOnce.Do(func() { close(s.stop) })
	<-s.done
}

// run is the service's goroutine; it ends when the notices end (Shutdown) or at close.
// A check in progress is cancelled when Shutdown starts.
func (s *toolsService) run(notices <-chan Notice, unsubscribe func()) {
	defer close(s.done)
	defer unsubscribe()
	ctx, cancel := context.WithCancel(s.m.base)
	defer cancel()
	go func() {
		select {
		case <-s.stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	var stopTimer func() bool
	arm := func(at time.Time) {
		if stopTimer != nil {
			stopTimer()
		}
		// The clock is looked at again at least hourly, so that sleep and clock changes
		// delay a check by at most that much.
		d := min(max(at.Sub(s.cfg.now()), 0), updateMaxWait)
		stopTimer = s.cfg.afterFunc(d, func() {
			select {
			case s.tick <- struct{}{}:
			default:
			}
		})
	}
	defer func() {
		if stopTimer != nil {
			stopTimer()
		}
	}()
	first := func() time.Time { return s.cfg.now().Add(s.cfg.firstDelay + s.cfg.jitter(toolsFirstJitter)) }
	cur := s.m.Settings().Media
	next := first()
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
			if m := n.Settings.Media; m.autoUpdates() && !cur.autoUpdates() {
				next = first()
				arm(next)
			}
			cur = n.Settings.Media
		case <-s.tick:
			if s.cfg.now().Before(next) {
				arm(next)
				continue
			}
			s.check(ctx)
			next = s.cfg.now().Add(s.cfg.interval + s.cfg.jitter(toolsJitter))
			arm(next)
		case <-s.stop:
			return
		}
	}
}

// check runs one automatic check when the settings ask for it.
func (s *toolsService) check(ctx context.Context) {
	if !s.m.Settings().Media.autoUpdates() {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, toolsCheckTimeout)
	defer cancel()
	if err := s.m.cfg.Tools.CheckUpdate(cctx); err != nil && ctx.Err() == nil {
		s.log.Warn("automatic check for yt-dlp and Deno updates failed", "err", err)
	}
}
