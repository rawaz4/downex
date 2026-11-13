//go:build windows

package win

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"downex/ui/kit"
	"downex/ui/win/internal/w32"
)

// writeAtomic writes as the host and the prefs file write: a temporary file renamed over.
func writeAtomic(t *testing.T, path, s string) {
	t.Helper()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

type changeLog struct {
	mu  sync.Mutex
	got map[string]time.Time
}

func (c *changeLog) add(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.got == nil {
		c.got = map[string]time.Time{}
	}
	if _, ok := c.got[name]; !ok {
		c.got[name] = time.Now()
	}
}

func (c *changeLog) wait(name string, d time.Duration) (time.Time, bool) {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		at, ok := c.got[name]
		c.mu.Unlock()
		if ok {
			return at, true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return time.Time{}, false
}

func (c *changeLog) reset() { c.mu.Lock(); c.got = nil; c.mu.Unlock() }

// A change another process makes to ui-theme.json or ui-prefs.json (an atomic rewrite, a
// removal) is told at once by the folder's notification: well within the 1 s the windows
// follow it in. Other files of the folder are not told.
func TestWatchFilesTellsChanges(t *testing.T) {
	dir := t.TempDir()
	var log changeLog
	fw := WatchFiles(dir, []string{"ui-theme.json", "ui-prefs.json"}, 10*time.Second, log.add) // poll too slow to matter
	defer fw.Stop()
	time.Sleep(50 * time.Millisecond)
	for _, step := range []struct {
		name string
		do   func()
	}{
		{"ui-theme.json", func() { writeAtomic(t, filepath.Join(dir, "ui-theme.json"), `{"theme":"dark"}`) }},
		{"ui-prefs.json", func() { writeAtomic(t, filepath.Join(dir, "ui-prefs.json"), `{"downex-theme":"dark"}`) }},
		{"ui-theme.json", func() { writeAtomic(t, filepath.Join(dir, "ui-theme.json"), `{"theme":"light"}`) }},
		{"ui-prefs.json", func() { os.Remove(filepath.Join(dir, "ui-prefs.json")) }},
	} {
		log.reset()
		start := time.Now()
		step.do()
		at, ok := log.wait(step.name, 2*time.Second)
		if !ok {
			t.Fatalf("%s: no change told", step.name)
		}
		if took := at.Sub(start); took > time.Second {
			t.Errorf("%s: told after %v", step.name, took)
		} else {
			t.Logf("%s: told after %v", step.name, took)
		}
	}
	log.reset()
	writeAtomic(t, filepath.Join(dir, "ui-window.json"), `{}`)
	if _, ok := log.wait("ui-window.json", 300*time.Millisecond); ok {
		t.Error("a file that is not watched was told")
	}
}

// A folder that does not exist yet (no notification can be asked of it) is polled: its
// files are told once they appear.
func TestWatchFilesPollsAMissingFolder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Downex")
	var log changeLog
	fw := WatchFiles(dir, []string{"ui-theme.json"}, 100*time.Millisecond, log.add)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeAtomic(t, filepath.Join(dir, "ui-theme.json"), `{"theme":"dark"}`)
	if _, ok := log.wait("ui-theme.json", 2*time.Second); !ok {
		t.Fatal("a file in a folder made later was not told")
	}
	fw.Stop()
	fw.Stop() // twice is fine
}

// The system's settings reach the window through WM_SETTINGCHANGE (synthetic here: no
// setting of this PC changes): light or dark apps, a contrast theme, animations off and the
// text size each become an EnvironmentEvent with the new value, and the same settings again
// none.
func TestSettingChangeFollowsTheSystem(t *testing.T) {
	root := &testRoot{}
	a, w := openTest(t, root, WindowOptions{Software: true})
	env := w.env
	a.readEnv = func(dpi uint32) kit.Environment {
		e := env
		e.Scale.PixelsPerDIP = float32(dpi) / 96
		return e
	}
	area, _ := windows.UTF16PtrFromString("ImmersiveColorSet")
	send := func() {
		w32.SendMessage(w.hwnd, w32.WM_SETTINGCHANGE, 0, uintptr(unsafe.Pointer(area)))
	}
	lastEnv := func() (kit.Environment, int) {
		n := 0
		var last kit.Environment
		for _, e := range root.events {
			if ev, ok := e.(kit.EnvironmentEvent); ok {
				n++
				last = ev.Env
			}
		}
		return last, n
	}
	_, before := lastEnv()
	for i, c := range []struct {
		name string
		set  func(e *kit.Environment)
		ok   func(e kit.Environment) bool
	}{
		{"dark apps", func(e *kit.Environment) { e.Dark = !e.Dark }, func(e kit.Environment) bool { return e.Dark == env.Dark }},
		{"a contrast theme", func(e *kit.Environment) { e.HighContrast = true }, func(e kit.Environment) bool { return e.HighContrast }},
		{"animations off", func(e *kit.Environment) { e.ReducedMotion = true }, func(e kit.Environment) bool { return e.ReducedMotion }},
		{"text size 150%", func(e *kit.Environment) { e.Scale.Text = 1.5 }, func(e kit.Environment) bool { return e.Scale.Text == 1.5 }},
	} {
		c.set(&env)
		send()
		got, n := lastEnv()
		if n != before+i+1 || !c.ok(got) || w.Environment() != got {
			t.Fatalf("%s: %d environment events (want %d), last %+v", c.name, n-before, i+1, got)
		}
	}
	_, n := lastEnv()
	send()
	if _, again := lastEnv(); again != n {
		t.Fatal("an unchanged setting was told again")
	}
}
