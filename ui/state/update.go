package state

import (
	"context"
	"strconv"
	"sync"

	downexv1 "downex/internal/rpc/downex/v1"
)

// UpdateLaterKey is where the window remembers the update offer the user put off (Later):
// UpdateOfferKey of the offer, which belongs to one automatic check of downexd.
const UpdateLaterKey = "downex.updateLater"

// UpdateOfferKey names the offer of info: its version and the time of the check that found
// it ("1.0.1@1789000000000", milliseconds), so that Later puts off this check's offer only and
// the next automatic check (at most one a day, and one after downexd starts) offers the
// version again.
func UpdateOfferKey(info *downexv1.UpdateInfo) string {
	key := info.GetLatestVersion()
	if t := info.GetCheckedAt(); t != nil {
		key += "@" + strconv.FormatInt(t.AsTime().UnixMilli(), 10)
	}
	return key
}

// UpdateDaemon is what the UpdateStore calls (a host.Daemon has it).
type UpdateDaemon interface {
	InstallUpdate(ctx context.Context) (*downexv1.InstallUpdateResponse, error)
}

// UpdateOptions are what an UpdateStore works with.
type UpdateOptions struct {
	Daemon UpdateDaemon
	Events Events
	// Storage keeps the offer put off (UpdateLaterKey); nil keeps it for the session.
	Storage Storage
	// Installed runs, off the UI thread, once downexd has started installing an update and
	// exits for it (the macOS window then starts again from the new Downex.app; on Windows
	// Setup does that). nil: nothing.
	Installed func(info *downexv1.UpdateInfo)
}

// UpdateStore is the update that downexd's automatic checks offer (docs/M6-SPEC.md §4). The
// update events of Watch carry each automatic check's result; the window offers an available
// version in a pop-up (Offer) until the user installs it (Install) or puts it off (Later),
// which lasts until the next automatic check finds it again. A check that failed (no update
// information published yet, offline) sends nothing, and a development build has no checks,
// so neither shows anything. The offer put off is kept in the prefs file, so a window opened
// later (the tray's notification) does not offer it again before the next check.
type UpdateStore struct {
	Changes Signal

	daemon    UpdateDaemon
	events    Events
	storage   Storage
	installed func(*downexv1.UpdateInfo)

	mu       sync.Mutex
	unsub    func()
	started  bool
	info     *downexv1.UpdateInfo
	later    string
	busy     bool
	failure  string
	finished *downexv1.UpdateInfo
}

// NewUpdate makes the store; Start subscribes it.
func NewUpdate(o UpdateOptions) *UpdateStore {
	u := &UpdateStore{daemon: o.Daemon, events: o.Events, storage: o.Storage, installed: o.Installed}
	if o.Storage != nil {
		if v, ok, err := o.Storage.Get(UpdateLaterKey); err == nil && ok {
			u.later = v
		}
	}
	return u
}

// Start subscribes to the update events.
func (u *UpdateStore) Start() {
	u.mu.Lock()
	if u.started {
		u.mu.Unlock()
		return
	}
	u.started = true
	u.mu.Unlock()
	if u.events != nil {
		unsub := u.events.Subscribe(u.Apply)
		u.mu.Lock()
		u.unsub = unsub
		u.mu.Unlock()
	}
}

// Dispose unsubscribes.
func (u *UpdateStore) Dispose() {
	u.mu.Lock()
	unsub := u.unsub
	u.unsub = nil
	u.mu.Unlock()
	if unsub != nil {
		unsub()
	}
}

// Apply takes one batch of events: the latest automatic check's result.
func (u *UpdateStore) Apply(batch Batch) {
	changed := false
	u.mu.Lock()
	for _, ev := range batch {
		if k, ok := ev.GetKind().(*downexv1.Event_Update); ok && k.Update != nil {
			u.info = k.Update
			changed = true
		}
	}
	u.mu.Unlock()
	if changed {
		u.Changes.Notify()
	}
}

// Info is the latest automatic check's result (nil before downexd reported one).
func (u *UpdateStore) Info() *downexv1.UpdateInfo { u.mu.Lock(); defer u.mu.Unlock(); return u.info }

// Offer is the update to offer in the pop-up: an available version that the user has not
// put off (Later) since the check that found it, while no update was installed.
func (u *UpdateStore) Offer() (*downexv1.UpdateInfo, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.info == nil || !u.info.GetAvailable() || u.finished != nil || UpdateOfferKey(u.info) == u.later {
		return nil, false
	}
	return u.info, true
}

// Later puts off the offer of info until the next automatic check finds the version again.
func (u *UpdateStore) Later(info *downexv1.UpdateInfo) {
	if info == nil {
		return
	}
	key := UpdateOfferKey(info)
	u.mu.Lock()
	u.later = key
	u.mu.Unlock()
	if u.storage != nil {
		_ = u.storage.Set(UpdateLaterKey, key) // a convenience: the session keeps it anyway
	}
	u.Changes.Notify()
}

// Busy reports that an Install runs.
func (u *UpdateStore) Busy() bool { u.mu.Lock(); defer u.mu.Unlock(); return u.busy }

// Failure is why the last Install failed ("" when it did not).
func (u *UpdateStore) Failure() string { u.mu.Lock(); defer u.mu.Unlock(); return u.failure }

// Installed is the update downexd started installing (nil until it did).
func (u *UpdateStore) Installed() *downexv1.UpdateInfo {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.finished
}

// Install has downexd download, verify and install the available update (InstallUpdate:
// Windows runs the new Setup silently, which restarts Downex; macOS replaces Downex.app and
// starts it again) and reports whether it started; ctx bounds the call (host.InstallTimeout:
// downexd downloads the package first). It does nothing while an Install runs. A failure
// leaves downexd running and its reason in Failure.
func (u *UpdateStore) Install(ctx context.Context) bool {
	u.mu.Lock()
	if u.busy || u.finished != nil {
		u.mu.Unlock()
		return false
	}
	u.busy, u.failure = true, ""
	u.mu.Unlock()
	u.Changes.Notify()

	resp, err := u.daemon.InstallUpdate(ctx)
	u.mu.Lock()
	u.busy = false
	if err != nil {
		u.failure = Message(err)
		if u.failure == "" {
			u.failure = err.Error()
		}
		u.mu.Unlock()
		u.Changes.Notify()
		return false
	}
	u.mu.Unlock()
	u.MarkInstalled(resp.GetInfo())
	return true
}

// MarkInstalled records that downexd started installing info, which another view asked for
// (Settings → Updates), as Install does: nothing is offered any more, and Installed runs.
func (u *UpdateStore) MarkInstalled(info *downexv1.UpdateInfo) {
	if info == nil {
		info = &downexv1.UpdateInfo{}
	}
	u.mu.Lock()
	first := u.finished == nil
	u.finished = info
	u.mu.Unlock()
	u.Changes.Notify()
	if first && u.installed != nil {
		u.installed(info)
	}
}
