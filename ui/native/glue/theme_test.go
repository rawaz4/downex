package glue

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"downex/ui/host"
	"downex/ui/state"
	"downex/ui/state/statetest"
)

// sharedPrefs is one prefs file as every window sees it: a change any window makes is told
// to all (prefs.File's poll, at once here).
type sharedPrefs struct {
	mu      sync.Mutex
	values  map[string]string
	watches []func(string)
	writes  int
}

func (s *sharedPrefs) Get(key string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.values[key]
	return v, ok, nil
}

func (s *sharedPrefs) set(key string, v *string) {
	s.mu.Lock()
	if s.values == nil {
		s.values = map[string]string{}
	}
	if v == nil {
		delete(s.values, key)
	} else {
		s.values[key] = *v
	}
	s.writes++
	w := append([]func(string){}, s.watches...)
	s.mu.Unlock()
	for _, f := range w {
		f(key)
	}
}

func (s *sharedPrefs) Set(key, v string) error { s.set(key, &v); return nil }
func (s *sharedPrefs) Remove(key string) error { s.set(key, nil); return nil }
func (s *sharedPrefs) Watch(f func(string)) func() {
	s.mu.Lock()
	s.watches = append(s.watches, f)
	s.mu.Unlock()
	return func() {}
}

// themeFile counts the writes of ui-theme.json (the windows' and "today's window's").
type themeFile struct {
	path   string
	writes int
}

func (f *themeFile) save(c host.ThemeChoice) error {
	f.writes++
	return host.SaveThemeChoice(f.path, c)
}

// window is one native window's theme store over the shared prefs.
type window struct {
	theme *state.Theme
	link  *themeLink
}

func newWindow(prefs state.Storage, f *themeFile) window {
	l := newThemeLink(prefs, f.path, f.save)
	t := state.NewTheme(state.ThemeOptions{Storage: l})
	l.attach(t)
	return window{t, l}
}

// The theme goes from window to window without echoes: the user's choice in one window is
// written to ui-prefs.json and ui-theme.json once; another native window follows the prefs
// and writes nothing; a choice one of today's windows writes to ui-theme.json is followed
// (and kept in the prefs for the next native window) but never written back.
func TestThemeLinkFollowsWithoutEchoes(t *testing.T) {
	dir := t.TempDir()
	f := &themeFile{path: filepath.Join(dir, host.ThemeFileName)}
	prefs := &sharedPrefs{}

	// The first window, with no file yet: Attach writes what it shows, as today's attach does.
	a := newWindow(prefs, f)
	if f.writes != 1 || host.LoadThemeChoice(f.path) != host.ThemeSystem {
		t.Fatalf("first attach wrote %d times", f.writes)
	}
	// A second window: the file already says so, nothing is written.
	b := newWindow(prefs, f)
	if f.writes != 1 {
		t.Fatalf("second attach wrote again (%d)", f.writes)
	}

	t.Run("the user's choice is written once and followed by the other window", func(t *testing.T) {
		a.theme.Set(host.ThemeDark)
		if b.theme.Choice() != host.ThemeDark {
			t.Fatalf("the other window shows %q", b.theme.Choice())
		}
		if f.writes != 2 || host.LoadThemeChoice(f.path) != host.ThemeDark {
			t.Fatalf("ui-theme.json written %d times, holds %q", f.writes, host.LoadThemeChoice(f.path))
		}
		if v, _, _ := prefs.Get(state.ThemeKey); v != "dark" {
			t.Fatalf("prefs hold %q", v)
		}
		// The file watch of each window fires for the write: nothing changes.
		if a.link.follow(a.theme) || b.link.follow(b.theme) || f.writes != 2 {
			t.Fatalf("the watch of the windows' own write changed something (writes %d)", f.writes)
		}
	})

	t.Run("today's window writes ui-theme.json: both follow, nothing is written back", func(t *testing.T) {
		if err := host.SaveThemeChoice(f.path, host.ThemeLight); err != nil { // as today's SetTheme does
			t.Fatal(err)
		}
		writes := prefs.writes
		if !a.link.follow(a.theme) {
			t.Fatal("the first window did not follow")
		}
		b.link.follow(b.theme) // already light through the prefs: nothing to do, or the same
		if a.theme.Choice() != host.ThemeLight || b.theme.Choice() != host.ThemeLight {
			t.Fatalf("windows show %q and %q", a.theme.Choice(), b.theme.Choice())
		}
		if f.writes != 2 {
			t.Fatalf("a followed choice was written back to ui-theme.json (%d writes)", f.writes)
		}
		if v, _, _ := prefs.Get(state.ThemeKey); v != "light" || prefs.writes == writes {
			t.Fatalf("the prefs keep %q for the next native window", v)
		}
	})

	t.Run("System removes the key and is written as system", func(t *testing.T) {
		b.theme.Set(host.ThemeSystem)
		if _, ok, _ := prefs.Get(state.ThemeKey); ok {
			t.Fatal("System kept a stored key")
		}
		if a.theme.Choice() != host.ThemeSystem || host.LoadThemeChoice(f.path) != host.ThemeSystem || f.writes != 3 {
			t.Fatalf("first window %q, file %q, writes %d", a.theme.Choice(), host.LoadThemeChoice(f.path), f.writes)
		}
	})

	t.Run("a missing ui-theme.json is no news", func(t *testing.T) {
		a.theme.Set(host.ThemeDark)
		if err := os.Remove(f.path); err != nil {
			t.Fatal(err)
		}
		if a.link.follow(a.theme) || a.theme.Choice() != host.ThemeDark {
			t.Fatalf("a removed file changed the theme to %q", a.theme.Choice())
		}
	})
}

// Without a ui-theme.json path (no folder for it) the link stores the prefs only.
func TestThemeLinkWithoutAFile(t *testing.T) {
	prefs := &sharedPrefs{}
	l := newThemeLink(prefs, "", func(host.ThemeChoice) error { t.Fatal("saved without a path"); return nil })
	th := state.NewTheme(state.ThemeOptions{Storage: l})
	l.attach(th)
	th.Set(host.ThemeDark)
	if v, _, _ := prefs.Get(state.ThemeKey); v != "dark" || l.follow(th) {
		t.Fatalf("prefs %q", v)
	}
	// A nil storage keeps the choice for the session.
	n := newThemeLink(nil, "", nil)
	if _, ok, err := n.Get(state.ThemeKey); ok || err != nil || n.Set("k", "v") != nil || n.Remove("k") != nil ||
		n.WriteErr() != nil {
		t.Fatal("a link without storage failed")
	}
	n.Watch(func(string) {})()
	n.WatchWrites(func(error) {})()
}

// The window's stores hear the prefs file's changes on the UI thread: before Attach at once
// (no loop runs yet), once attached through the window's Post, and not at all once the
// window is gone; reads and writes go straight to the file.
func TestUIStoragePostsNotificationsToTheUIThread(t *testing.T) {
	mem := statetest.NewMemory(nil)
	u := &uiStorage{inner: mem}
	var s state.Storage = u
	shared, ok := s.(state.SharedStorage)
	if !ok {
		t.Fatal("UIStorage is not shared storage")
	}
	var heard []string
	cancel := shared.Watch(func(key string) { heard = append(heard, key) })
	defer cancel()
	mem.OtherWindowWrites("before", "1", true)
	var posted []func()
	u.attach(func(f func()) error { posted = append(posted, f); return nil })
	mem.OtherWindowWrites("attached", "1", true)
	if len(heard) != 1 || heard[0] != "before" || len(posted) != 1 {
		t.Fatalf("heard %q, posted %d; want only \"before\" heard and one posted", heard, len(posted))
	}
	posted[0]()
	if len(heard) != 2 || heard[1] != "attached" {
		t.Fatalf("after the post ran: %q", heard)
	}
	u.detach()
	mem.OtherWindowWrites("gone", "1", true)
	if len(heard) != 2 || len(posted) != 1 {
		t.Fatalf("after detach: heard %q, posted %d", heard, len(posted))
	}
	if err := s.Set("k", "v"); err != nil {
		t.Fatal(err)
	}
	if v, ok, _ := mem.Get("k"); !ok || v != "v" {
		t.Fatalf("a write did not reach the storage: %q %v", v, ok)
	}
}
