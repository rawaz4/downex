//go:build windows

package win

import (
	"fmt"
	"os"
	"testing"
	"time"

	"downex/ui/kit"
	"downex/ui/win/internal/w32"

	"golang.org/x/sys/windows/registry"
)

// A change of a watched key reaches the UI thread, which reads every window's environment
// again; only a real change becomes an EnvironmentEvent. The key is a test key of the user's
// own (HKEY_CURRENT_USER\Software\DownexTest-...), never a system setting.
func TestSettingsWatch(t *testing.T) {
	root := &testRoot{}
	a, w := openTest(t, root, WindowOptions{})
	pump(t, a, w, "the first frame", 2*time.Second, func() bool { return w.painted })

	path := fmt.Sprintf(`Software\DownexTest-%d-settings`, os.Getpid())
	k, _, err := registry.CreateKey(registry.CURRENT_USER, path, registry.SET_VALUE)
	if err != nil {
		t.Skipf("cannot make a test key: %v", err)
	}
	k.Close()
	t.Cleanup(func() { registry.DeleteKey(registry.CURRENT_USER, path) })

	reads := 0
	env := w.Environment()
	a.readEnv = func(dpi uint32) kit.Environment { reads++; return env }
	watch := watchKeys(a.msgWnd, msgSettings, []string{path})
	if watch == nil {
		t.Fatal("nothing watched")
	}
	defer watch.stop()

	k, err = registry.OpenKey(registry.CURRENT_USER, path, registry.SET_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	if err := k.SetDWordValue("TextScaleFactor", 125); err != nil {
		t.Fatal(err)
	}
	k.Close()
	if !pumpFor(t, a, w, 3*time.Second, func() bool { return reads > 0 }) {
		t.Fatal("a change of the watched key did not reach the UI thread")
	}
	for _, e := range root.events {
		if _, ok := e.(kit.EnvironmentEvent); ok {
			t.Errorf("an EnvironmentEvent without a change: %v", e)
		}
	}

	// A setting that does change the look (here the app mode) is an EnvironmentEvent, from
	// the watch's message as from WM_SETTINGCHANGE.
	env.Dark = !env.Dark
	root.events = nil
	w32.PostMessage(a.msgWnd, msgSettings, 0, 0)
	pumpFor(t, a, w, time.Second, func() bool { return len(root.events) > 0 })
	ee, ok := lastEnv(root.events)
	if !ok || ee.Env.Dark != env.Dark {
		t.Errorf("after the watch's message: %v", root.events)
	}
	env.HighContrast = true
	env.ReducedMotion = true
	env.Scale.Text = 1.25
	root.events = nil
	send(w, w32.WM_SETTINGCHANGE, 0, 0)
	ee, ok = lastEnv(root.events)
	if !ok || !ee.Env.HighContrast || !ee.Env.ReducedMotion || ee.Env.Scale.Text != 1.25 {
		t.Errorf("after WM_SETTINGCHANGE: %v", root.events)
	}
}

func lastEnv(events []kit.Event) (kit.EnvironmentEvent, bool) {
	for i := len(events) - 1; i >= 0; i-- {
		if e, ok := events[i].(kit.EnvironmentEvent); ok {
			return e, true
		}
	}
	return kit.EnvironmentEvent{}, false
}

// The environment carries the system's hover time, for tooltips.
func TestEnvironmentHoverTime(t *testing.T) {
	env := readEnvironment(96)
	if env.HoverTime <= 0 || env.HoverTime > 10*time.Second {
		t.Errorf("hover time %v", env.HoverTime)
	}
	t.Logf("hover time %v, text %v, dark %v, contrast %v, reduced motion %v", env.HoverTime, env.Scale.Text, env.Dark, env.HighContrast, env.ReducedMotion)
}
