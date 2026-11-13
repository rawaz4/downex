//go:build windows

package win

import (
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"downex/ui/win/internal/w32"
)

// The registry keys whose changes alter a window's environment without a message that the
// windows get: Settings › Accessibility › Text size (TextScaleFactor), which the system
// tells WinRT apps through UISettings only, and the app colour mode (AppsUseLightTheme),
// which WM_SETTINGCHANGE also announces, watched as well so that no change is missed.
var watchedKeys = []string{
	`Software\Microsoft\Accessibility`,
	`Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`,
}

// settingsWatch posts a message to the App's message window whenever a watched key under
// HKEY_CURRENT_USER changes; the App then reads every window's environment again, and only
// a real change becomes an EnvironmentEvent.
type settingsWatch struct {
	quit windows.Handle
	done chan struct{}
}

// watchSettings watches the keys that change the look.
func watchSettings(to w32.HWND) *settingsWatch { return watchKeys(to, msgSettings, watchedKeys) }

// watchKeys watches HKEY_CURRENT_USER's keys at paths (those that exist) on a goroutine of
// its own and posts msg to the window to after each change. It returns nil when nothing can be
// watched; stop ends it.
func watchKeys(to w32.HWND, msg uint32, paths []string) *settingsWatch {
	quit, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return nil
	}
	type watched struct {
		key   registry.Key
		event windows.Handle
	}
	var ws []watched
	closeAll := func() { // the goroutine's; stop closes quit once it has ended
		for _, k := range ws {
			k.key.Close()
			windows.CloseHandle(k.event)
		}
	}
	const filter = windows.REG_NOTIFY_CHANGE_LAST_SET | windows.REG_NOTIFY_CHANGE_NAME | windows.REG_NOTIFY_THREAD_AGNOSTIC
	arm := func(k watched) bool {
		return windows.RegNotifyChangeKeyValue(windows.Handle(k.key), false, filter, k.event, true) == nil
	}
	for _, p := range paths {
		k, err := registry.OpenKey(registry.CURRENT_USER, p, registry.NOTIFY|registry.QUERY_VALUE)
		if err != nil {
			continue // a key that does not exist yet: its setting was never changed
		}
		ev, err := windows.CreateEvent(nil, 0, 0, nil)
		if err != nil {
			k.Close()
			continue
		}
		wk := watched{k, ev}
		if !arm(wk) {
			k.Close()
			windows.CloseHandle(ev)
			continue
		}
		ws = append(ws, wk)
	}
	if len(ws) == 0 {
		windows.CloseHandle(quit)
		return nil
	}
	s := &settingsWatch{quit: quit, done: make(chan struct{})}
	handles := []windows.Handle{quit}
	for _, k := range ws {
		handles = append(handles, k.event)
	}
	go func() {
		defer close(s.done)
		defer closeAll()
		for {
			i, err := windows.WaitForMultipleObjects(handles, false, windows.INFINITE)
			if err != nil || i == windows.WAIT_OBJECT_0 || i >= windows.WAIT_OBJECT_0+uint32(len(handles)) {
				return
			}
			k := ws[i-windows.WAIT_OBJECT_0-1]
			arm(k) // before reading, so that a change made meanwhile is not missed
			w32.PostMessage(to, msg, 0, 0)
		}
	}()
	return s
}

// stop ends the watch and waits for its goroutine.
func (s *settingsWatch) stop() {
	if s == nil || s.quit == 0 {
		return
	}
	windows.SetEvent(s.quit)
	<-s.done
	windows.CloseHandle(s.quit)
	s.quit = 0
}
