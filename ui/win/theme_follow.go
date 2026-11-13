//go:build windows

package win

import (
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/windows"
)

// Following the theme and the per-viewer conveniences across processes: every window
// process of Downex keeps the theme in ui-theme.json and its conveniences in ui-prefs.json,
// in one folder (%LOCALAPPDATA%\Downex), and a change one window makes reaches the others.
// WatchFiles tells of such a change at once, through the folder's change notification, and
// at the latest after its poll interval when no notification comes (a folder that does not
// exist yet, a drive that sends none). The system's own settings (light or dark apps, a
// contrast theme, animations, the text size) reach each window as a kit.EnvironmentEvent
// (WM_SETTINGCHANGE and the registry watch, env.go and settings.go).

// FileWatch watches some files of one folder; Stop ends it.
type FileWatch struct {
	stop chan struct{}
	done chan struct{}
	quit windows.Handle // set when stop is closed, so that the wait ends at once
	once sync.Once
}

type fileStamp struct {
	exists bool
	size   int64
	mod    time.Time
}

func stampFile(path string) fileStamp {
	fi, err := os.Stat(path)
	if err != nil {
		return fileStamp{}
	}
	return fileStamp{exists: true, size: fi.Size(), mod: fi.ModTime()}
}

// WatchFiles calls changed (on a goroutine of its own, never twice at once) with the name of
// each file of names in dir that was made, changed, replaced or removed, as soon as the
// folder's change notification says so, or within poll (500 ms when 0) otherwise.
func WatchFiles(dir string, names []string, poll time.Duration, changed func(name string)) *FileWatch {
	if poll <= 0 {
		poll = 500 * time.Millisecond
	}
	fw := &FileWatch{stop: make(chan struct{}), done: make(chan struct{})}
	stamps := make([]fileStamp, len(names))
	for i, n := range names {
		stamps[i] = stampFile(filepath.Join(dir, n))
	}
	quit, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		quit = 0
	}
	fw.quit = quit
	go func() {
		defer close(fw.done)
		var note windows.Handle // the folder's change notification, once it can be made
		defer func() {
			if note != 0 {
				windows.FindCloseChangeNotification(note)
			}
		}()
		look := func() {
			for i, n := range names {
				s := stampFile(filepath.Join(dir, n))
				if s != stamps[i] {
					stamps[i] = s
					changed(n)
				}
			}
		}
		stopped := func() bool {
			select {
			case <-fw.stop:
				return true
			default:
				return false
			}
		}
		for !stopped() {
			if note == 0 && quit != 0 {
				const filter = windows.FILE_NOTIFY_CHANGE_FILE_NAME | windows.FILE_NOTIFY_CHANGE_LAST_WRITE | windows.FILE_NOTIFY_CHANGE_SIZE
				if h, err := windows.FindFirstChangeNotification(dir, false, filter); err == nil {
					note = h
				}
			}
			if quit == 0 {
				select {
				case <-fw.stop:
					return
				case <-time.After(poll):
				}
				look()
				continue
			}
			handles := []windows.Handle{quit}
			if note != 0 {
				handles = append(handles, note)
			}
			i, err := windows.WaitForMultipleObjects(handles, false, uint32(poll/time.Millisecond))
			switch {
			case err != nil && i != uint32(windows.WAIT_TIMEOUT):
				time.Sleep(poll) // should not happen: poll instead
			case i == windows.WAIT_OBJECT_0:
				return
			case i == windows.WAIT_OBJECT_0+1:
				if err := windows.FindNextChangeNotification(note); err != nil {
					windows.FindCloseChangeNotification(note)
					note = 0
				}
			}
			look()
		}
	}()
	return fw
}

// Stop ends the watch and waits until changed is no longer called.
func (fw *FileWatch) Stop() {
	if fw == nil {
		return
	}
	fw.once.Do(func() {
		close(fw.stop)
		if fw.quit != 0 {
			windows.SetEvent(fw.quit)
		}
		<-fw.done
		if fw.quit != 0 {
			windows.CloseHandle(fw.quit)
		}
	})
	<-fw.done
}
