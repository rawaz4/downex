package state_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"downex/ui/host"
	"downex/ui/state"
	"downex/ui/state/statetest"
)

// ---- theme (stores/theme.test.ts) --------------------------------------------------------

type themeHost struct {
	themes []state.ThemeChoice
	fail   string
	calls  int
}

func (h *themeHost) push(c state.ThemeChoice) error {
	h.calls++
	h.themes = append(h.themes, c)
	if h.fail != "" {
		return errors.New(h.fail)
	}
	return nil
}

func TestThemeReadsTheStoredChoiceAndCycles(t *testing.T) {
	th := state.NewTheme(state.ThemeOptions{Storage: statetest.NewMemory(map[string]string{state.ThemeKey: "dark"})})
	if th.Choice() != host.ThemeDark || !th.Dark() {
		t.Fatal(th.Choice())
	}
	if c := state.NewTheme(state.ThemeOptions{Storage: statetest.NewMemory(map[string]string{state.ThemeKey: "sepia"})}).Choice(); c != host.ThemeSystem {
		t.Fatal(c)
	}
	got := []state.ThemeChoice{state.NextTheme(host.ThemeSystem), state.NextTheme(host.ThemeLight), state.NextTheme(host.ThemeDark)}
	if !slices.Equal(got, []state.ThemeChoice{host.ThemeLight, host.ThemeDark, host.ThemeSystem}) {
		t.Fatal(got)
	}
}

func TestThemeGivesTheHostTheChoiceOnAttachAndEveryChange(t *testing.T) {
	store := statetest.NewMemory(map[string]string{state.ThemeKey: "light"})
	th := state.NewTheme(state.ThemeOptions{Storage: store})
	h := &themeHost{}
	th.Attach(h.push)
	if !slices.Equal(h.themes, []state.ThemeChoice{host.ThemeLight}) {
		t.Fatal(h.themes)
	}
	th.Set(host.ThemeDark)
	th.Set(host.ThemeSystem)
	if !slices.Equal(h.themes, []state.ThemeChoice{host.ThemeLight, host.ThemeDark, host.ThemeSystem}) {
		t.Fatal(h.themes)
	}
	if _, ok := store.Value(state.ThemeKey); ok {
		t.Fatal("system is stored as no key")
	}
	th.Cycle()
	if h.themes[len(h.themes)-1] != host.ThemeLight {
		t.Fatal(h.themes)
	}
	if v, _ := store.Value(state.ThemeKey); v != "light" {
		t.Fatal(v)
	}
}

func TestThemeFollowsAnotherWindow(t *testing.T) {
	store := statetest.NewMemory(map[string]string{state.ThemeKey: "light"})
	th := state.NewTheme(state.ThemeOptions{Storage: store})
	t.Cleanup(th.Close)
	h := &themeHost{}
	th.Attach(h.push)

	store.OtherWindowWrites(state.ThemeKey, "dark", true)
	if th.Choice() != host.ThemeDark || !th.Dark() || !slices.Equal(h.themes, []state.ThemeChoice{host.ThemeLight, host.ThemeDark}) {
		t.Fatal(th.Choice(), h.themes)
	}
	// A notification that did not arrive: the focus reads it again.
	store.OtherWindowWrites(state.ThemeKey, "", false)
	th.Follow()
	if th.Choice() != host.ThemeSystem || len(h.themes) != 3 || h.themes[2] != host.ThemeSystem {
		t.Fatal(th.Choice(), h.themes)
	}
	// Nothing changed: the host is not asked again; other keys are ignored.
	th.Follow()
	store.OtherWindowWrites(state.DensityKey, "compact", true)
	if len(h.themes) != 3 {
		t.Fatal(h.themes)
	}
}

func TestThemeKeepsWorkingWhenTheHostCannotStoreIt(t *testing.T) {
	th := state.NewTheme(state.ThemeOptions{Storage: statetest.NewMemory(nil)})
	h := &themeHost{fail: "unimplemented: SetTheme"}
	th.Attach(h.push)
	th.Set(host.ThemeDark)
	if th.Choice() != host.ThemeDark || h.calls != 2 {
		t.Fatal(th.Choice(), h.calls)
	}
}

func TestThemeKeepsTheSessionsChoiceWhenStorageFails(t *testing.T) {
	th := state.NewTheme(state.ThemeOptions{Storage: statetest.Broken{}})
	if th.Choice() != host.ThemeSystem {
		t.Fatal(th.Choice())
	}
	th.Set(host.ThemeDark)
	th.Follow()
	if th.Choice() != host.ThemeDark || !th.Dark() {
		t.Fatal(th.Choice())
	}
}

func TestThemeResolvesTheSystemScheme(t *testing.T) {
	th := state.NewTheme(state.ThemeOptions{SystemDark: func() bool { return true }})
	if !th.Dark() {
		t.Fatal("system dark")
	}
	th.SetSystemDark(false)
	if th.Dark() {
		t.Fatal("system light")
	}
}

// ---- density (stores/density.test.ts) ----------------------------------------------------

func TestDensityStartsComfortable(t *testing.T) {
	d := state.NewDensity(statetest.NewMemory(nil))
	if d.Choice() != state.Comfortable || d.Compact() {
		t.Fatal(d.Choice())
	}
}

func TestDensityPersistsTheChoiceAndReadsItBack(t *testing.T) {
	store := statetest.NewMemory(nil)
	d := state.NewDensity(store)
	d.Set(state.Compact)
	if v, _ := store.Value(state.DensityKey); v != "compact" {
		t.Fatal(v)
	}
	if state.NewDensity(store).Choice() != state.Compact {
		t.Fatal("not read back")
	}
	d.Toggle()
	if v, _ := store.Value(state.DensityKey); d.Choice() != state.Comfortable || v != "comfortable" {
		t.Fatal(d.Choice(), v)
	}
}

func TestDensityReadsAnythingUnknownAsComfortable(t *testing.T) {
	if c := state.NewDensity(statetest.NewMemory(map[string]string{state.DensityKey: "cosy"})).Choice(); c != state.Comfortable {
		t.Fatal(c)
	}
	if state.ParseDensity("compact") != state.Compact || state.ParseDensity("") != state.Comfortable {
		t.Fatal("parse")
	}
}

func TestDensityKeepsWorkingWhenStorageFailsOrIsMissing(t *testing.T) {
	d := state.NewDensity(statetest.Broken{})
	if d.Choice() != state.Comfortable {
		t.Fatal(d.Choice())
	}
	d.Set(state.Compact)
	if d.Choice() != state.Compact {
		t.Fatal(d.Choice())
	}
	none := state.NewDensity(nil)
	none.Toggle()
	if none.Choice() != state.Compact {
		t.Fatal(none.Choice())
	}
}

// ---- the other per-viewer keys --------------------------------------------------------------

func TestViewPrefsReadAsTodaysWindowDoes(t *testing.T) {
	store := statetest.NewMemory(map[string]string{
		state.ColumnWidthsKey:     `{"size":82,"name":300,"speed":-1,"eta":"9","status":1e999}`,
		state.ExistingFileKey:     "replace",
		state.ProgressDetailsKey:  "1",
		state.DetailsCollapsedKey: "0",
	})
	// name is the flexible column (no width): never restored; bad numbers are dropped
	if w := state.ColumnWidths(store); len(w) != 1 || w["size"] != 82 {
		t.Fatal(w)
	}
	if state.ExistingFileChoice(store) != state.Replace || !state.ProgressDetails(store) || state.DetailsCollapsed(store) {
		t.Fatal("flags")
	}
	state.SaveColumnWidths(store, map[string]float64{"size": 90, "speed": 70})
	if v, _ := store.Value(state.ColumnWidthsKey); v != `{"size":90,"speed":70}` {
		t.Fatal(v)
	}
	state.SaveExistingFileChoice(store, state.KeepBoth)
	state.SaveDetailsCollapsed(store, true)
	state.SaveProgressDetails(store, false)
	for k, want := range map[string]string{state.ExistingFileKey: "keepBoth", state.DetailsCollapsedKey: "1", state.ProgressDetailsKey: "0"} {
		if v, _ := store.Value(k); v != want {
			t.Fatal(k, v)
		}
	}
	// a broken or missing storage gives the defaults
	if len(state.ColumnWidths(statetest.Broken{})) != 0 || state.ExistingFileChoice(nil) != state.KeepBoth || state.ProgressDetails(statetest.Broken{}) {
		t.Fatal("defaults")
	}
	if len(state.ColumnWidths(statetest.NewMemory(map[string]string{state.ColumnWidthsKey: "not json"}))) != 0 ||
		len(state.ColumnWidths(statetest.NewMemory(map[string]string{state.ColumnWidthsKey: `{"size":82} x`}))) != 0 {
		t.Fatal("bad json")
	}
}

// ---- toasts (stores/toasts.svelte.ts) ----------------------------------------------------------

func messages(items []state.Toast) []string {
	var out []string
	for _, x := range items {
		m := x.Message
		if x.Leaving {
			m += " (leaving)"
		}
		out = append(out, m)
	}
	return out
}

func TestToastsKeepAtMostFourAndExpire(t *testing.T) {
	c := statetest.NewClock(0)
	ts := state.NewToasts(c, nil)
	for _, m := range []string{"a", "b", "c", "d", "e"} {
		ts.Show(m, state.ToastInfo)
	}
	if !slices.Equal(messages(ts.Items()), []string{"b", "c", "d", "e"}) {
		t.Fatal(messages(ts.Items()))
	}
	ts.Error("failed")
	if !slices.Equal(messages(ts.Items()), []string{"c", "d", "e", "failed"}) {
		t.Fatal(messages(ts.Items()))
	}
	c.Advance(state.ToastLifetime)
	// without motion a toast goes at once; an error stays twice as long
	if !slices.Equal(messages(ts.Items()), []string{"failed"}) || ts.Items()[0].Tone != state.ToastError {
		t.Fatal(messages(ts.Items()))
	}
	c.Advance(state.ToastLifetime)
	if len(ts.Items()) != 0 {
		t.Fatal(messages(ts.Items()))
	}
}

func TestToastsFadeOutWithMotion(t *testing.T) {
	c := statetest.NewClock(0)
	ts := state.NewToasts(c, func() bool { return true })
	id := ts.Success("Settings saved")
	ts.Dismiss(id)
	if !slices.Equal(messages(ts.Items()), []string{"Settings saved (leaving)"}) || ts.Items()[0].Tone != state.ToastSuccess {
		t.Fatal(messages(ts.Items()))
	}
	// a leaving toast does not count towards the four
	for _, m := range []string{"a", "b", "c", "d"} {
		ts.Show(m, state.ToastInfo)
	}
	if len(ts.Items()) != 5 {
		t.Fatal(messages(ts.Items()))
	}
	c.Advance(state.ToastExit)
	if !slices.Equal(messages(ts.Items()), []string{"a", "b", "c", "d"}) {
		t.Fatal(messages(ts.Items()))
	}
	ts.Dismiss(999) // unknown: nothing
	c.Advance(time.Hour)
	if len(ts.Items()) != 0 {
		t.Fatal(messages(ts.Items()))
	}
}

// slowStore is a storage whose Set waits, once entered, until it is let go: the window
// between Set's change of the choice and its store.
type slowStore struct {
	*statetest.Memory
	entered, release chan struct{}
}

func (s *slowStore) Set(key, value string) error {
	close(s.entered)
	<-s.release
	return s.Memory.Set(key, value)
}

// A Follow on another goroutine (the prefs file's watch) that comes while Set is storing the
// user's choice never puts the old stored value back: the choice and the store stay the
// user's (TestThemeFollowsOtherProcesses saw prefs "dark" with ui-theme.json "system").
func TestThemeFollowDuringSetKeepsTheUsersChoice(t *testing.T) {
	st := &slowStore{Memory: statetest.NewMemory(nil), entered: make(chan struct{}), release: make(chan struct{})}
	th := state.NewTheme(state.ThemeOptions{Storage: st})
	h := &themeHost{}
	th.Attach(h.push)
	set := make(chan struct{})
	go func() { th.Set(host.ThemeDark); close(set) }()
	<-st.entered
	followed := make(chan struct{})
	go func() { th.Follow(); close(followed) }()
	select {
	case <-followed: // without the ordering it ran at once, reading the old stored value
	case <-time.After(100 * time.Millisecond):
	}
	close(st.release)
	<-set
	<-followed
	if c := th.Choice(); c != host.ThemeDark {
		t.Errorf("after a Follow during Set the choice is %q, want dark", c)
	}
	if v, _, _ := st.Get(state.ThemeKey); v != "dark" {
		t.Errorf("stored %q, want dark", v)
	}
	if last := h.themes[len(h.themes)-1]; last != host.ThemeDark {
		t.Errorf("the host was last told %q (all: %v), want dark", last, h.themes)
	}
}
