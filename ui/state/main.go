package state

import (
	"context"
	"sync"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/query"
	"downex/ui/host"
)

// MainOptions are what the main window's stores work with.
type MainOptions struct {
	Daemon   host.Daemon
	Events   Events
	Files    FileActions
	CopyText func(ctx context.Context, text string) error
	Clock    Clock
	Frames   Frames
	Call     CallContext
	// Collator orders names (UseUserLocale's).
	Collator query.Collator
	// MotionOK reports that animations may run (the toasts fade out); nil: they may not.
	MotionOK func() bool
	// Go runs background reads (nil: a goroutine).
	Go func(func())
	// Storage keeps the update offer the user put off (UpdateStore; nil: for the session).
	Storage Storage
	// UpdateInstalled is UpdateOptions.Installed (nil: nothing).
	UpdateInstalled func(info *downexv1.UpdateInfo)
}

// Main is the main window's stores as App.svelte connects them: the list, the queues, the
// tools, the update offered and the notices, with the daemon's settings. Settings and queues are read
// whenever downexd is reachable and they were not read since it last was (again at
// every status check after a failed read), and the settings when the window gets the
// focus; a stale list invalidates both; a queue filter on a deleted queue shows every
// queue again.
type Main struct {
	Downloads *DownloadsStore
	Queues    *QueuesStore
	Tools     *ToolsStore
	Update    *UpdateStore
	Polite    *PoliteStore
	Toasts    *Toasts
	Notices   *Notices
	// Changes tells that the settings changed (the stores have their own).
	Changes Signal

	daemon host.Daemon
	call   CallContext
	goFn   func(func())

	mu             sync.Mutex
	settings       *downexv1.Settings
	settingsLoaded bool
	reading        chan struct{}
	readOK         bool
	lastDaemon     *DaemonState
	unsubs         []func()
	started        bool
}

// NewMain makes the main window's stores; Start starts them.
func NewMain(o MainOptions) *Main {
	m := &Main{daemon: o.Daemon, call: callOr(o.Call), goFn: o.Go}
	if m.goFn == nil {
		m.goFn = func(f func()) { go f() }
	}
	m.Downloads = NewDownloads(DownloadsOptions{Daemon: o.Daemon, Events: o.Events, Files: o.Files, CopyText: o.CopyText,
		Clock: o.Clock, Frames: o.Frames, Call: o.Call, Collator: o.Collator})
	m.Queues = NewQueues(QueuesOptions{Daemon: o.Daemon, Events: o.Events, Clock: o.Clock, Call: o.Call, Go: o.Go})
	m.Tools = NewTools(ToolsOptions{Daemon: o.Daemon, Events: o.Events, Call: o.Call, Go: o.Go})
	m.Update = NewUpdate(UpdateOptions{Daemon: o.Daemon, Events: o.Events, Storage: o.Storage, Installed: o.UpdateInstalled})
	m.Polite = NewPolite(PoliteOptions{Events: o.Events})
	m.Toasts = NewToasts(o.Clock, o.MotionOK)
	m.Notices = NewNotices(o.Clock, o.MotionOK)
	return m
}

// Start subscribes the stores, loads the list (in the background) and starts the status
// checks every StatusInterval.
func (m *Main) Start() {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return
	}
	m.started = true
	m.unsubs = append(m.unsubs,
		m.Downloads.Changes.Subscribe(m.onDownloads),
		m.Queues.Changes.Subscribe(m.onQueues),
	)
	m.mu.Unlock()
	m.Queues.Start()
	m.Tools.Start()
	m.Update.Start()
	m.Polite.Start()
	m.goFn(func() {
		ctx, cancel := m.call()
		defer cancel()
		m.Downloads.Start(ctx)
	})
	m.Downloads.StartStatusPolling(StatusInterval)
}

// Dispose stops the stores.
func (m *Main) Dispose() {
	m.mu.Lock()
	unsubs := m.unsubs
	m.unsubs = nil
	m.mu.Unlock()
	for _, u := range unsubs {
		u()
	}
	m.Queues.Dispose()
	m.Tools.Dispose()
	m.Update.Dispose()
	m.Polite.Dispose()
	m.Downloads.Dispose()
	m.Notices.Clear()
}

// onDownloads runs the effects of a status check (a new DaemonState each time).
func (m *Main) onDownloads() {
	d := m.Downloads.Daemon()
	m.mu.Lock()
	if d == m.lastDaemon {
		m.mu.Unlock()
		return
	}
	m.lastDaemon = d
	stale := m.Downloads.Stale()
	loadSettings := !stale && !m.settingsLoaded
	if stale {
		m.settingsLoaded = false
	}
	m.mu.Unlock()
	if stale {
		m.Queues.Invalidate()
		return
	}
	if loadSettings {
		m.goFn(func() { m.LoadSettings() })
	}
	if !m.Queues.Loaded() {
		m.goFn(func() { m.Queues.Load() })
	}
}

func (m *Main) onQueues() {
	if m.Queues.Loaded() {
		m.Downloads.SetQueues(m.Queues.IDs())
	}
}

// Settings are the daemon's settings as last read; loaded is false when they were not read
// since downexd last became reachable (they are then the last ones, or nil).
func (m *Main) Settings() (s *downexv1.Settings, loaded bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.settings, m.settingsLoaded
}

// SetSettings takes settings a dialog saved (the daemon's answer).
func (m *Main) SetSettings(s *downexv1.Settings) {
	m.mu.Lock()
	m.settings, m.settingsLoaded = s, true
	m.mu.Unlock()
	m.Downloads.SetCategories(s.GetCategories())
	m.Changes.Notify()
}

// LoadSettings reads the settings (concurrent calls share one read) and reports whether
// that worked; a failure keeps the last ones (the Settings dialog reports the error).
func (m *Main) LoadSettings() bool {
	m.mu.Lock()
	if m.reading != nil {
		ch := m.reading
		m.mu.Unlock()
		<-ch
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.readOK
	}
	ch := make(chan struct{})
	m.reading = ch
	m.mu.Unlock()
	resp, err := callWith(m.call, m.daemon.GetSettings)
	ok := err == nil && resp.GetSettings() != nil
	if ok {
		m.SetSettings(resp.GetSettings())
	} else {
		m.mu.Lock()
		m.settingsLoaded = false
		m.mu.Unlock()
	}
	m.mu.Lock()
	m.readOK = ok
	m.reading = nil
	m.mu.Unlock()
	close(ch)
	return ok
}

// Focus is the window getting the focus: the settings are read again unless the list is
// stale.
func (m *Main) Focus() {
	if !m.Downloads.Stale() {
		m.goFn(func() { m.LoadSettings() })
	}
}
