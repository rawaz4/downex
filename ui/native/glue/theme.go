package glue

import (
	"os"
	"sync"

	"downex/ui/host"
	"downex/ui/state"
)

// The theme across processes. Every window of Downex shows the same theme: the native windows
// keep the choice in ui-prefs.json (state.ThemeKey, followed by the state.Theme of each
// within prefs.PollInterval), and every window, today's Wails windows included, also writes it
// to ui-theme.json, which the windows read before they exist (their first frame and title
// bar). A native window follows ui-theme.json too, so a change made in one of today's windows
// during an upgrade (which writes localStorage and ui-theme.json, never ui-prefs.json) reaches
// it within the file watch's second.
//
// A window writes ui-theme.json when its user chose a theme in it (and, as today's window
// does when it attaches, once at start when the file says otherwise); a choice it took from
// another window is never written back, so two windows cannot keep undoing each other's
// change.

// themeLink stands between a window's state.Theme and its storage (the prefs file): it tells
// the user's choices (a Set or Remove of state.ThemeKey) from the choices the window follows,
// and writes ui-theme.json for the first only.
type themeLink struct {
	inner state.Storage
	path  string // ui-theme.json; "" stores nothing
	save  func(host.ThemeChoice) error

	mu        sync.Mutex
	user      bool // the user changed the choice; the push that follows writes it
	attaching bool // the push at Attach: written when the file says otherwise
	following bool // the window takes another window's choice: nothing is written back
}

var (
	_ state.SharedStorage = (*themeLink)(nil)
	_ state.WriteStatus   = (*themeLink)(nil)
)

func newThemeLink(inner state.Storage, path string, save func(host.ThemeChoice) error) *themeLink {
	if save == nil {
		save = func(c host.ThemeChoice) error { return host.SaveThemeChoice(path, c) }
	}
	return &themeLink{inner: inner, path: path, save: save}
}

// Get is state.Storage's.
func (l *themeLink) Get(key string) (string, bool, error) {
	if l.inner == nil {
		return "", false, nil
	}
	return l.inner.Get(key)
}

// Set is state.Storage's.
func (l *themeLink) Set(key, value string) error {
	l.mark(key)
	if l.inner == nil {
		return nil
	}
	return l.inner.Set(key, value)
}

// Remove is state.Storage's.
func (l *themeLink) Remove(key string) error {
	l.mark(key)
	if l.inner == nil {
		return nil
	}
	return l.inner.Remove(key)
}

func (l *themeLink) mark(key string) {
	if key != state.ThemeKey {
		return
	}
	l.mu.Lock()
	if !l.following {
		l.user = true
	}
	l.mu.Unlock()
}

// Watch is state.SharedStorage's: the prefs file's own.
func (l *themeLink) Watch(f func(key string)) (cancel func()) {
	if s, ok := l.inner.(state.SharedStorage); ok {
		return s.Watch(f)
	}
	return func() {}
}

// WriteErr is state.WriteStatus's.
func (l *themeLink) WriteErr() error {
	if s, ok := l.inner.(state.WriteStatus); ok {
		return s.WriteErr()
	}
	return nil
}

// WatchWrites is state.WriteStatus's.
func (l *themeLink) WatchWrites(f func(err error)) (cancel func()) {
	if s, ok := l.inner.(state.WriteStatus); ok {
		return s.WatchWrites(f)
	}
	return func() {}
}

// stored is the choice in ui-theme.json; ok is false when there is no such file.
func (l *themeLink) stored() (host.ThemeChoice, bool) {
	if l.path == "" {
		return "", false
	}
	if _, err := os.Stat(l.path); err != nil {
		return "", false
	}
	return host.LoadThemeChoice(l.path), true
}

// push is the state.Theme's host push (Theme.Attach): the window's choice c, written to
// ui-theme.json when the user made it here, or at Attach when the file says otherwise.
func (l *themeLink) push(c host.ThemeChoice) error {
	l.mu.Lock()
	user, attaching := l.user, l.attaching
	l.user = false
	l.mu.Unlock()
	if l.path == "" || (!user && !attaching) {
		return nil
	}
	if s, ok := l.stored(); ok && s == c {
		return nil
	}
	return l.save(c)
}

// attach connects t to the link: its push writes ui-theme.json as push says.
func (l *themeLink) attach(t *state.Theme) {
	l.mu.Lock()
	l.attaching = true
	l.mu.Unlock()
	t.Attach(l.push)
	l.mu.Lock()
	l.attaching = false
	l.mu.Unlock()
}

// follow takes the choice ui-theme.json now holds, when it differs from t's: another window
// (one of today's, or a native one) chose it. The prefs file gets it, so that the next native
// window starts with it; ui-theme.json is not written back. It is for the UI thread. It
// reports whether t changed.
func (l *themeLink) follow(t *state.Theme) bool {
	c, ok := l.stored()
	if !ok || c == t.Choice() {
		return false
	}
	l.mu.Lock()
	l.following = true
	l.mu.Unlock()
	t.Set(c)
	l.mu.Lock()
	l.following = false
	l.user = false
	l.mu.Unlock()
	return true
}
