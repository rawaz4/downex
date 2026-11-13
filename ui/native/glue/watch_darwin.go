//go:build darwin

package glue

import (
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// fileWatch follows files of one folder: a kqueue on the folder tells of an entry written,
// renamed or added (the atomic writes of ui-theme.json and ui-prefs.json are a rename), and
// the files that changed are told; without a kqueue, or once it fails, a poll reads them
// every poll.
type fileWatch struct {
	stop chan struct{}
	once sync.Once
	wg   sync.WaitGroup
	kq   int // the kqueue (Stop closes it), -1 when the folder is polled
	fd   int // the folder's descriptor in it
}

// wakeIdent is the kqueue's user event that Stop triggers to end the wait at once.
const wakeIdent = 1

// keventHook is unix.Kevent (tests make it fail).
var keventHook = unix.Kevent

// fileStamp is what tells a file's change.
type fileStamp struct {
	ok    bool
	size  int64
	mod   time.Time
	inode uint64
}

func stampOf(p string) fileStamp {
	fi, err := os.Stat(p)
	if err != nil {
		return fileStamp{}
	}
	s := fileStamp{ok: true, size: fi.Size(), mod: fi.ModTime()}
	if st, ok := fi.Sys().(*unixStat); ok {
		s.inode = uint64(st.Ino)
	}
	return s
}

type unixStat = unix.Stat_t

// watchFiles calls cb (on the watch's goroutine) with each of names in dir that changed.
func watchFiles(dir string, names []string, poll time.Duration, cb func(name string)) *fileWatch {
	fw := &fileWatch{stop: make(chan struct{}), kq: -1, fd: -1}
	stamps := map[string]fileStamp{}
	for _, n := range names {
		stamps[n] = stampOf(filepath.Join(dir, n))
	}
	check := func() {
		for _, n := range names {
			s := stampOf(filepath.Join(dir, n))
			if s != stamps[n] {
				stamps[n] = s
				cb(n)
			}
		}
	}
	fw.kq, fw.fd = openKqueue(dir)
	fw.wg.Add(1)
	go func() {
		defer fw.wg.Done()
		if fw.kq >= 0 && fw.waitKqueue(check) {
			return
		}
		// No kqueue, or it failed: poll until Stop.
		t := time.NewTicker(poll)
		defer t.Stop()
		for {
			select {
			case <-fw.stop:
				return
			case <-t.C:
				check()
			}
		}
	}()
	return fw
}

// openKqueue is a kqueue telling of dir's entries, with the user event Stop triggers; -1, -1
// when there is none.
func openKqueue(dir string) (kq, fd int) {
	k, err := unix.Kqueue()
	if err != nil {
		return -1, -1
	}
	d, err := unix.Open(dir, unix.O_EVTONLY, 0)
	if err != nil {
		unix.Close(k)
		return -1, -1
	}
	var vnode, wake unix.Kevent_t
	unix.SetKevent(&vnode, d, unix.EVFILT_VNODE, unix.EV_ADD|unix.EV_CLEAR)
	vnode.Fflags = unix.NOTE_WRITE | unix.NOTE_ATTRIB | unix.NOTE_EXTEND | unix.NOTE_RENAME | unix.NOTE_DELETE
	unix.SetKevent(&wake, wakeIdent, unix.EVFILT_USER, unix.EV_ADD|unix.EV_CLEAR)
	if _, err := keventHook(k, []unix.Kevent_t{vnode, wake}, nil, nil); err != nil {
		unix.Close(d)
		unix.Close(k)
		return -1, -1
	}
	return k, d
}

// waitKqueue checks the files at each of the folder's events until Stop (true), or until
// the kqueue fails (false: the caller polls from then on).
func (fw *fileWatch) waitKqueue(check func()) bool {
	events := make([]unix.Kevent_t, 4)
	// Stop wakes the wait; the timeout only bounds a wake that did not arrive.
	timeout := unix.NsecToTimespec(int64(time.Second))
	for {
		select {
		case <-fw.stop:
			return true
		default:
		}
		n, err := keventHook(fw.kq, nil, events, &timeout)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			check() // what changed before it failed
			return false
		}
		for _, ev := range events[:n] {
			if ev.Filter == unix.EVFILT_USER {
				return true
			}
		}
		if n > 0 {
			check()
		}
	}
}

// Stop ends the watch at once and waits for it (nil does nothing).
func (fw *fileWatch) Stop() {
	if fw == nil {
		return
	}
	fw.once.Do(func() {
		close(fw.stop)
		if fw.kq >= 0 {
			var wake unix.Kevent_t
			unix.SetKevent(&wake, wakeIdent, unix.EVFILT_USER, 0)
			wake.Fflags = unix.NOTE_TRIGGER
			_, _ = unix.Kevent(fw.kq, []unix.Kevent_t{wake}, nil, nil)
		}
		fw.wg.Wait()
		if fw.kq >= 0 {
			unix.Close(fw.fd)
			unix.Close(fw.kq)
		}
	})
	fw.wg.Wait()
}
