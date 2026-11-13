package state

import (
	"context"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/host"
)

// ProgressStatusInterval is how often a progress window checks that downexd answers (as
// the main window does).
const ProgressStatusInterval = 3 * time.Second

// ProgressOptions are what a ProgressStore works with.
type ProgressOptions struct {
	Daemon host.Daemon
	// Events are the batches of the window's bridge, which watches this download only.
	Events Events
	ID     int64
	Clock  Clock
	Call   CallContext
	// Go runs the completion mark's call in the background (nil: a goroutine).
	Go func(func())
}

type markState uint8

const (
	markUnsent markState = iota
	markSending
	markDone
)

// ProgressStore is the state of a progress window (stores/progress.svelte.ts, DESKTOP-SPEC
// §7): one download, kept current from the bridge's events, read once at the start and
// again whenever downexd becomes reachable. The settings can change elsewhere: they are
// read at the start, again when downexd becomes reachable or a read failed, and whenever
// the window asks (on focus, before it decides what completion shows).
type ProgressStore struct {
	Changes Signal

	daemon host.Daemon
	events Events
	id     int64
	clock  Clock
	call   CallContext
	goFn   func(func())

	mu         sync.Mutex
	download   *Download
	reports    reportOrder
	missing    bool
	removed    bool
	loadError  string
	connected  bool
	settings   *downexv1.Settings
	firstState downexv1.DownloadState
	hasFirst   bool
	ready      bool

	unsub       func()
	started     bool
	timer       Timer
	disposed    bool
	settingsGen int
	mark        markState
}

// NewProgress makes the store of download o.ID; Start subscribes and reads.
func NewProgress(o ProgressOptions) *ProgressStore {
	p := &ProgressStore{daemon: o.Daemon, events: o.Events, id: o.ID, clock: clockOr(o.Clock), call: callOr(o.Call), goFn: o.Go, connected: true}
	if p.goFn == nil {
		p.goFn = func(f func()) { go f() }
	}
	return p
}

// ID is the download the window shows.
func (p *ProgressStore) ID() int64 { return p.id }

// Download is the download as last reported (nil until it is known).
func (p *ProgressStore) Download() *Download { p.mu.Lock(); defer p.mu.Unlock(); return p.download }

// Missing reports that the download did not exist when the window opened.
func (p *ProgressStore) Missing() bool { p.mu.Lock(); defer p.mu.Unlock(); return p.missing }

// Removed reports that the download was removed while the window showed it.
func (p *ProgressStore) Removed() bool { p.mu.Lock(); defer p.mu.Unlock(); return p.removed }

// LoadError is why the download could not be read (other than not existing; "" if none).
func (p *ProgressStore) LoadError() string { p.mu.Lock(); defer p.mu.Unlock(); return p.loadError }

// Connected reports that downexd answered the last check.
func (p *ProgressStore) Connected() bool { p.mu.Lock(); defer p.mu.Unlock(); return p.connected }

// Settings are the daemon's settings (nil until read, or when they could not be).
func (p *ProgressStore) Settings() *downexv1.Settings {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.settings
}

// FirstState is the state in which the window first saw the download.
func (p *ProgressStore) FirstState() (downexv1.DownloadState, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.firstState, p.hasFirst
}

// Ready reports that the first reads (download and settings) are over, whatever their
// outcome.
func (p *ProgressStore) Ready() bool { p.mu.Lock(); defer p.mu.Unlock(); return p.ready }

// Start subscribes to events, then reads the download and the settings, and starts the
// checks every interval (ProgressStatusInterval when 0).
func (p *ProgressStore) Start(interval time.Duration) {
	if interval <= 0 {
		interval = ProgressStatusInterval
	}
	p.mu.Lock()
	if p.started || p.disposed {
		p.mu.Unlock()
		return
	}
	p.started = true
	p.mu.Unlock()
	if p.events != nil {
		unsub := p.events.Subscribe(p.Apply)
		p.mu.Lock()
		p.unsub = unsub
		p.mu.Unlock()
	}
	var wg sync.WaitGroup
	wg.Go(p.Refresh)
	wg.Go(func() { p.LoadSettings() })
	wg.Wait()
	p.mu.Lock()
	p.ready = true
	p.mu.Unlock()
	p.Changes.Notify()
	p.schedule(interval)
}

// Dispose unsubscribes and stops the checks.
func (p *ProgressStore) Dispose() {
	p.mu.Lock()
	p.disposed = true
	unsub := p.unsub
	p.unsub = nil
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
	p.mu.Unlock()
	if unsub != nil {
		unsub()
	}
}

// Apply takes one batch of events. The bridge also reports a download that it cannot
// watch (it does not exist) as removed.
func (p *ProgressStore) Apply(batch Batch) {
	p.mu.Lock()
	changed := false
	var marks []func()
	events, snapshot := p.reports.arrived()
	for _, ev := range batch {
		switch k := ev.GetKind().(type) {
		case *downexv1.Event_Removed:
			if k.Removed == p.id {
				p.reports.remove(p.id, events)
				p.goneLocked()
				changed = true
			}
		case *downexv1.Event_Snapshot:
			if k.Snapshot.GetId() == p.id {
				marks = append(marks, p.putLocked(k.Snapshot, true, snapshot))
				changed = true
			}
		case *downexv1.Event_Updated:
			if k.Updated.GetId() == p.id {
				marks = append(marks, p.putLocked(k.Updated, true, events))
				changed = true
			}
		}
	}
	p.mu.Unlock()
	p.runMarks(marks...)
	if changed {
		p.Changes.Notify()
	}
}

func (p *ProgressStore) goneLocked() {
	if p.download == nil {
		p.missing = true
	} else {
		p.removed = true
	}
}

// Refresh reads the download; not_found means it is gone. The answer replaces the download
// shown only when it is newer (reportOrder).
func (p *ProgressStore) Refresh() {
	p.mu.Lock()
	at := p.reports.sent()
	p.mu.Unlock()
	d, err := callWith(p.call, func(ctx context.Context) (*Download, error) { return p.daemon.Get(ctx, p.id) })
	p.mu.Lock()
	var mark func()
	if err != nil {
		e := ErrorOf(err)
		if e.Code == connect.CodeNotFound {
			p.goneLocked()
		} else if p.download == nil {
			p.loadError = e.Message
		}
	} else {
		p.loadError = ""
		mark = p.putLocked(d, false, at)
	}
	p.reports.landed()
	p.mu.Unlock()
	p.runMarks(mark)
	p.Changes.Notify()
}

// LoadSettings reads the settings; when that fails, the last ones read (nil if none were).
func (p *ProgressStore) LoadSettings() *downexv1.Settings {
	p.mu.Lock()
	p.settingsGen++
	gen := p.settingsGen
	p.mu.Unlock()
	resp, err := callWith(p.call, p.daemon.GetSettings)
	p.mu.Lock()
	defer p.Changes.Notify()
	defer p.mu.Unlock()
	if err != nil {
		return p.settings
	}
	if gen == p.settingsGen {
		p.settings = resp.GetSettings()
	}
	return resp.GetSettings()
}

// putLocked stores a newer report, learned at at (reportOrder). Only Watch updates carry
// the connections of a running download; a read in between keeps the ones last reported.
// It returns the completion mark to send (nil when none is due).
func (p *ProgressStore) putLocked(d *Download, fromEvent bool, at uint64) func() {
	prev := p.download
	if !p.reports.newer(d, prev, at) {
		return nil
	}
	p.reports.hold(d.GetId(), at)
	next := d
	if !fromEvent && prev != nil && len(d.GetConnectionInfo()) == 0 && d.GetState() == StateDownloading && prev.GetState() == StateDownloading {
		next = cloneDownload(d)
		next.ConnectionInfo = prev.GetConnectionInfo()
	}
	if !p.hasFirst {
		p.firstState, p.hasFirst = next.GetState(), true
	}
	p.download = next
	p.missing = false
	return p.markLocked(next)
}

// markLocked: a download whose progress window was opened gets the download complete
// dialog, also when the window is closed before it completes: the window tells downexd
// once. A completed download needs no mark (this window already shows its completion),
// nor one marked already.
func (p *ProgressStore) markLocked(d *Download) func() {
	if p.mark != markUnsent || p.disposed {
		return nil
	}
	if d.GetCompletionDialog() || d.GetState() == StateCompleted {
		p.mark = markDone
		return nil
	}
	p.mark = markSending
	return func() {
		p.mu.Lock()
		at := p.reports.sent()
		p.mu.Unlock()
		resp, err := callWith(p.call, func(ctx context.Context) (*downexv1.SetDownloadOptionsResponse, error) {
			return p.daemon.SetDownloadOptions(ctx, &downexv1.SetDownloadOptionsRequest{Id: p.id, CompletionDialog: proto.Bool(true)})
		})
		p.mu.Lock()
		var again func()
		if err != nil {
			// Unreachable: the next report (after downexd is back) tries again. Any other
			// refusal (completed meanwhile, being removed) is final.
			switch codeOf(err) {
			case connect.CodeUnavailable, connect.CodeDeadlineExceeded:
				p.mark = markUnsent
			default:
				p.mark = markDone
			}
		} else {
			p.mark = markDone
			if !p.disposed {
				again = p.putLocked(resp.GetDownload(), false, at)
			}
		}
		p.reports.landed()
		p.mu.Unlock()
		p.runMarks(again)
		p.Changes.Notify()
	}
}

func (p *ProgressStore) runMarks(marks ...func()) {
	for _, m := range marks {
		if m != nil {
			p.goFn(m)
		}
	}
}

func (p *ProgressStore) schedule(interval time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.disposed {
		return
	}
	p.timer = p.clock.AfterFunc(interval, func() { p.check(interval) })
}

func (p *ProgressStore) check(interval time.Duration) {
	_, err := callWith(p.call, p.daemon.Status)
	p.mu.Lock()
	if err != nil {
		p.connected = false
		p.mu.Unlock()
		p.Changes.Notify()
		p.schedule(interval)
		return
	}
	wasDown := !p.connected
	p.connected = true
	refresh := wasDown || p.loadError != ""
	settings := wasDown || p.settings == nil
	p.mu.Unlock()
	var wg sync.WaitGroup
	if refresh {
		wg.Go(p.Refresh)
	}
	if settings {
		wg.Go(func() { p.LoadSettings() })
	}
	wg.Wait()
	p.Changes.Notify()
	p.schedule(interval)
}

// ---- actions (the error is the daemon's) -----------------------------------------------

// ask sends call, whose answer is the download, and shows that answer unless the window
// learned a newer report meanwhile (reportOrder).
func (p *ProgressStore) ask(call func() (*Download, error)) (*Download, error) {
	p.mu.Lock()
	at := p.reports.sent()
	p.mu.Unlock()
	d, err := call()
	p.mu.Lock()
	var mark func()
	if err == nil {
		mark = p.putLocked(d, false, at)
	}
	p.reports.landed()
	p.mu.Unlock()
	if err != nil {
		return nil, err
	}
	p.runMarks(mark)
	p.Changes.Notify()
	return d, nil
}

// Pause pauses the download.
func (p *ProgressStore) Pause(ctx context.Context) error {
	_, err := p.ask(func() (*Download, error) {
		resp, err := p.daemon.Pause(ctx, &downexv1.PauseRequest{Id: p.id})
		return resp.GetDownload(), err
	})
	return err
}

// Resume resumes it (restart: from the start).
func (p *ProgressStore) Resume(ctx context.Context, restart bool) error {
	_, err := p.ask(func() (*Download, error) {
		resp, err := p.daemon.Resume(ctx, &downexv1.ResumeRequest{Id: p.id, Restart: restart})
		return resp.GetDownload(), err
	})
	return err
}

// SetOptions changes the download's limits; it returns the download as the daemon
// answered it.
func (p *ProgressStore) SetOptions(ctx context.Context, speedLimit *int64, maxConnections *int32) (*Download, error) {
	return p.ask(func() (*Download, error) {
		resp, err := p.daemon.SetDownloadOptions(ctx, &downexv1.SetDownloadOptionsRequest{Id: p.id, SpeedLimit: speedLimit, MaxConnections: maxConnections})
		return resp.GetDownload(), err
	})
}

// SetShowCompleteDialog turns the completion view on or off (Settings.show_complete_dialog).
// The settings are edited on the message GetSettings returns, so every other field,
// those this build does not know included, is written back as it was.
func (p *ProgressStore) SetShowCompleteDialog(ctx context.Context, show bool) error {
	saved, err := host.EditSettings(ctx, p.daemon, func(s *downexv1.Settings) error {
		s.ShowCompleteDialog = show
		return nil
	})
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.settingsGen++
	if saved != nil {
		p.settings = saved
	}
	p.mu.Unlock()
	p.Changes.Notify()
	return nil
}
