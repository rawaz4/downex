package state

import (
	"sync"

	"downex/ui/host"
)

// ThemeChoice is the app's theme choice (host.ThemeChoice).
type ThemeChoice = host.ThemeChoice

var themeOrder = []ThemeChoice{host.ThemeSystem, host.ThemeLight, host.ThemeDark}

// NextTheme is the choice after c: system → light → dark → system.
func NextTheme(c ThemeChoice) ThemeChoice {
	for i, x := range themeOrder {
		if x == c {
			return themeOrder[(i+1)%len(themeOrder)]
		}
	}
	return themeOrder[0]
}

// ThemeOptions are what a Theme works with.
type ThemeOptions struct {
	// Storage holds the choice under ThemeKey (nil: none, the choice lasts the session).
	// A SharedStorage is followed: a change another window makes is taken at once.
	Storage Storage
	// SystemDark reports the system's scheme at start (nil: light).
	SystemDark func() bool
}

// Theme is the light/dark theme (stores/theme.svelte.ts): it follows the system by default,
// with a manual override stored per viewer under ThemeKey. Every window of Downex shares
// that storage: a change made in one is followed by the others (the storage's change
// notification, else when they get the focus). The host is told the choice when it
// attaches and on every change, so the native parts it draws (title-bar colours,
// ui-theme.json) match (DESIGN-SYSTEM.md §4.8).
type Theme struct {
	Changes Signal

	storage Storage

	mu sync.Mutex
	// gen counts Sets and storing the Sets that are storing their choice: a Follow (a
	// storage watch, on another goroutine or inside the store itself) that overlaps a Set
	// takes nothing, since what it read can be the value before the Set (it put the old value
	// back and lost the user's choice: TestThemeFollowDuringSetKeepsTheUsersChoice).
	gen        uint64
	storing    int
	choice     ThemeChoice
	systemDark bool
	push       func(ThemeChoice) error
	unwatch    func()
	unwrites   func()
	writeErr   error // the last synchronous Set or Remove's
}

// NewTheme reads the stored choice and follows other windows' changes until Close.
func NewTheme(o ThemeOptions) *Theme {
	t := &Theme{storage: o.Storage, choice: host.ThemeSystem}
	if o.SystemDark != nil {
		t.systemDark = o.SystemDark()
	}
	if c, ok := t.read(); ok {
		t.choice = c
	}
	if shared, ok := o.Storage.(SharedStorage); ok {
		t.unwatch = shared.Watch(func(key string) {
			// "": the whole storage may have changed
			if key == ThemeKey || key == "" {
				t.Follow()
			}
		})
	}
	t.unwrites = watchWrites(o.Storage, t.Changes.Notify)
	return t
}

// Close stops following other windows.
func (t *Theme) Close() {
	t.mu.Lock()
	unwatch, unwrites := t.unwatch, t.unwrites
	t.unwatch, t.unwrites = nil, nil
	t.mu.Unlock()
	if unwatch != nil {
		unwatch()
	}
	if unwrites != nil {
		unwrites()
	}
}

// StorageErr reports why the choice is not stored (it then lasts for this session unless a
// retry stores it): the storage's error, nil when it is stored. A change of it is notified
// through Changes.
func (t *Theme) StorageErr() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return storageErr(t.storage, t.writeErr)
}

// Choice is the choice.
func (t *Theme) Choice() ThemeChoice { t.mu.Lock(); defer t.mu.Unlock(); return t.choice }

// Dark reports the scheme actually shown; canvases read it to know when to redraw.
func (t *Theme) Dark() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.choice.IsDark(t.systemDark)
}

// SetSystemDark takes the system's scheme (WM_SETTINGCHANGE).
func (t *Theme) SetSystemDark(dark bool) {
	t.mu.Lock()
	changed := t.systemDark != dark
	t.systemDark = dark
	t.mu.Unlock()
	if changed {
		t.Changes.Notify()
	}
}

// Attach connects the host: the native window parts take the choice now and on every
// change. A host that cannot store it fails; the window keeps its theme either way.
func (t *Theme) Attach(push func(ThemeChoice) error) {
	t.mu.Lock()
	t.push = push
	c := t.choice
	t.mu.Unlock()
	t.pushChoice(push, c)
}

// Set chooses a theme and stores it (system: the key is removed). A storage that writes
// later (WriteStatus) never makes it wait; a failure is reported by StorageErr, and the
// choice lasts for this session either way.
func (t *Theme) Set(c ThemeChoice) {
	t.mu.Lock()
	t.choice = c
	t.gen++
	t.storing++
	push := t.push
	t.mu.Unlock()
	var err error
	if t.storage != nil {
		if c == host.ThemeSystem {
			err = t.storage.Remove(ThemeKey)
		} else {
			err = t.storage.Set(ThemeKey, string(c))
		}
	}
	t.mu.Lock()
	t.storing--
	if t.storage != nil {
		t.writeErr = err
	}
	t.mu.Unlock()
	t.Changes.Notify()
	t.pushChoice(push, c)
}

// Cycle is the title bar's theme button.
func (t *Theme) Cycle() { t.Set(NextTheme(t.Choice())) }

// read is the stored choice; ok is false when storage cannot be read.
func (t *Theme) read() (ThemeChoice, bool) {
	if t.storage == nil {
		return "", false
	}
	v, _, err := t.storage.Get(ThemeKey)
	if err != nil {
		return "", false
	}
	if v == string(host.ThemeLight) || v == string(host.ThemeDark) {
		return ThemeChoice(v), true
	}
	return host.ThemeSystem, true
}

// Follow takes a choice another window stored (also called when this window gets the
// focus, in case a notification did not arrive); unreadable storage keeps this session's.
func (t *Theme) Follow() {
	t.mu.Lock()
	gen, busy := t.gen, t.storing > 0
	t.mu.Unlock()
	if busy {
		return // a Set is storing: its choice is the newest
	}
	stored, ok := t.read()
	t.mu.Lock()
	if !ok || stored == t.choice || t.gen != gen || t.storing > 0 {
		t.mu.Unlock()
		return
	}
	t.choice = stored
	push := t.push
	t.mu.Unlock()
	t.Changes.Notify()
	t.pushChoice(push, stored)
}

func (t *Theme) pushChoice(push func(ThemeChoice) error, c ThemeChoice) {
	if push != nil {
		_ = push(c) // a host without the binding, or one that could not store it: nothing to do
	}
}
