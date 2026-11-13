package glue

import (
	"sync"

	"downex/ui/state"
)

// uiStorage is the window's storage (the prefs file) as its stores see it: the prefs file
// tells its changes on the goroutine that read them (the file watch's Reload, the file's own
// poll, the writer's status), and the stores that hear them (state.Theme's and
// state.Density's Follow, views' details and column widths) belong to the UI thread, which
// their host pushes (ui-theme.json) also need. So every notification is posted to the UI
// thread once the window is attached; before that, no window loop runs yet and it runs at
// once; after the window is gone it is dropped. The storage's IO stays where it is.
type uiStorage struct {
	inner state.Storage

	mu   sync.Mutex
	post func(func()) error // the window's (Attach); nil before it
	gone bool               // the window closed: notifications are dropped
}

var (
	_ state.SharedStorage = (*uiStorage)(nil)
	_ state.WriteStatus   = (*uiStorage)(nil)
)

// attach makes post (the window's kit.Window.Post) the way to the UI thread.
func (u *uiStorage) attach(post func(func()) error) {
	if u == nil {
		return
	}
	u.mu.Lock()
	u.post, u.gone = post, false
	u.mu.Unlock()
}

// detach drops every later notification (the window is gone).
func (u *uiStorage) detach() {
	if u == nil {
		return
	}
	u.mu.Lock()
	u.post, u.gone = nil, true
	u.mu.Unlock()
}

// run runs f on the UI thread (see uiStorage).
func (u *uiStorage) run(f func()) {
	u.mu.Lock()
	post, gone := u.post, u.gone
	u.mu.Unlock()
	switch {
	case gone:
	case post == nil:
		f()
	default:
		_ = post(f) // a closed window takes nothing
	}
}

// Get is state.Storage's.
func (u *uiStorage) Get(key string) (string, bool, error) { return u.inner.Get(key) }

// Set is state.Storage's.
func (u *uiStorage) Set(key, value string) error { return u.inner.Set(key, value) }

// Remove is state.Storage's.
func (u *uiStorage) Remove(key string) error { return u.inner.Remove(key) }

// Watch is state.SharedStorage's: f runs on the UI thread.
func (u *uiStorage) Watch(f func(key string)) (cancel func()) {
	s, ok := u.inner.(state.SharedStorage)
	if !ok {
		return func() {}
	}
	return s.Watch(func(key string) { u.run(func() { f(key) }) })
}

// WriteErr is state.WriteStatus's.
func (u *uiStorage) WriteErr() error {
	if s, ok := u.inner.(state.WriteStatus); ok {
		return s.WriteErr()
	}
	return nil
}

// WatchWrites is state.WriteStatus's: f runs on the UI thread.
func (u *uiStorage) WatchWrites(f func(err error)) (cancel func()) {
	s, ok := u.inner.(state.WriteStatus)
	if !ok {
		return func() {}
	}
	return s.WatchWrites(func(err error) { u.run(func() { f(err) }) })
}
