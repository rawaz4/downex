// Package prefs is the native window's per-viewer conveniences file, ui-prefs.json
// (%LOCALAPPDATA%\Downex\ui-prefs.json on Windows, beside ui-theme.json elsewhere): what
// today's window keeps in WebView2's localStorage (state.StorageKeys), as a JSON object of
// string values. Every window of Downex shares it: a change one makes reaches the others
// within PollInterval, as localStorage's storage event does.
//
// A Set or Remove changes the File's values at once and never waits for the disk: one
// writer goroutine per File takes the changes in order, coalesces those made while it was
// busy (the newest value of each key) and writes them. A write that fails is retried, with
// a growing pause, until it succeeds or the File is closed; meanwhile WriteErr says why
// (state.WriteStatus), and Close makes one last attempt and logs whatever it could not
// store. Flush waits for the changes made so far.
//
// Writes are atomic (a temporary file in the same folder, synced, then renamed over the
// file) and merge with what other processes wrote meanwhile: the writer reads the file again
// under a lock file, applies its changes and writes the result. Keys this build does not
// know are kept. When the file does not exist yet, Open imports the keys from today's
// localStorage, read-only (package webstorage); any failure there means defaults.
//
// The values are conveniences: a file whose content is not a JSON object gives the defaults
// (and the next write replaces it). A file that exists but cannot be read for a moment
// (another program holding it without sharing, a permission) is never taken as empty: a
// write then leaves it as it is and is retried, and a poll keeps what was read last and
// tries again.
package prefs

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"downex/ui/host"
	"downex/ui/state"
	"downex/ui/state/prefs/webstorage"
)

// The file names: the window's, and the native preview's own until the switch (as the
// preview keeps its own window state).
const (
	FileName        = "ui-prefs.json"
	PreviewFileName = "ui-prefs-native-preview.json"
)

// PollInterval is how often a File looks for changes other processes made.
const PollInterval = 200 * time.Millisecond

// maxSize bounds the file read (a handful of short values; a column-width object).
const maxSize = 1 << 20

// lockStale is how old a lock file may be before it is taken as left by a process that
// died; lockWait is how long a writer waits for one.
const (
	lockStale = 5 * time.Second
	lockWait  = 2 * time.Second
)

// readWait is how long a poll retries a file it cannot read before it keeps what it has
// until the next poll; a write and Open retry for lockWait.
const readWait = 50 * time.Millisecond

// retryFirst is the pause before a failed write is tried again; it doubles up to retryMax.
const (
	retryFirst = 100 * time.Millisecond
	retryMax   = 2 * time.Second
)

// DefaultPath is this user's file: beside ui-theme.json (host.DefaultThemePath); "" when
// there is no such folder.
func DefaultPath() string { return pathNamed(FileName) }

// PreviewPath is the native preview's file.
func PreviewPath() string { return pathNamed(PreviewFileName) }

func pathNamed(name string) string {
	theme := host.DefaultThemePath()
	if theme == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(theme), name)
}

// Importer reads the values to start a new file with.
type Importer func() (map[string]string, error)

// WebView2Import reads state.StorageKeys from today's window's localStorage (origin
// http://wails.localhost) in dir (webstorage.DefaultDir() when ""), never changing it.
func WebView2Import(dir string) Importer {
	return func() (map[string]string, error) {
		if dir == "" {
			dir = webstorage.DefaultDir()
		}
		if dir == "" {
			return nil, webstorage.ErrNoStorage
		}
		return webstorage.Read(dir, webstorage.WailsOrigin, state.StorageKeys())
	}
}

// Options are how a File is opened.
type Options struct {
	// Import gives the values of a file that does not exist yet (nil: it starts empty).
	Import Importer
	// Poll is how often other processes' changes are looked for (PollInterval when 0;
	// negative: never, Reload alone).
	Poll time.Duration
	// Log may be nil.
	Log *slog.Logger
}

// File is the prefs file; it implements state.SharedStorage and state.WriteStatus. It is
// safe for concurrent use.
type File struct {
	path string
	log  *slog.Logger
	// io orders this process's reads and writes of the file (a poll never sees a write
	// half done).
	io sync.Mutex

	mu    sync.Mutex
	raw   map[string]json.RawMessage // what is on disk with pending laid over it
	stamp stamp
	watch map[int]func(string)
	nextW int

	// The writer's state. gen counts the changes made; tried is the newest change an
	// attempt covered, done the newest on disk. writeErr is the last attempt's failure (nil
	// after a success); flushed is signalled after every attempt.
	pending  map[string]change
	gen      uint64
	tried    uint64
	done     uint64
	writeErr error
	errWatch map[int]func(error)
	flushed  *sync.Cond
	kick     chan struct{}

	closed  bool
	stop    chan struct{}
	stopped chan struct{}
}

// change is one key's newest change not on disk yet.
type change struct {
	value json.RawMessage // nil: removed
	gen   uint64
}

func (c change) apply(raw map[string]json.RawMessage, key string) {
	if c.value == nil {
		delete(raw, key)
	} else {
		raw[key] = c.value
	}
}

// stamp is what changes when the file is rewritten.
type stamp struct {
	mod  time.Time
	size int64
	ok   bool
}

func stampOf(path string) stamp {
	info, err := os.Stat(path)
	if err != nil {
		return stamp{}
	}
	return stamp{info.ModTime(), info.Size(), true}
}

var (
	_ state.SharedStorage = (*File)(nil)
	_ state.WriteStatus   = (*File)(nil)
)

// ErrNoPath is a File opened without a path.
var ErrNoPath = errors.New("prefs: no file for this user")

// errClosed is a Flush whose changes the closed File could not write.
var errClosed = errors.New("prefs: closed before the changes were stored")

// Open reads the file at path, creating it from o.Import when it does not exist, and
// follows other processes' changes until Close. A file that cannot be parsed reads as
// empty (and is replaced by the next write); only a path that cannot be used at all is an
// error.
func Open(path string, o Options) (*File, error) {
	if path == "" {
		return nil, ErrNoPath
	}
	f := &File{
		path: path, log: o.Log, raw: map[string]json.RawMessage{}, watch: map[int]func(string){},
		pending: map[string]change{}, errWatch: map[int]func(error){}, kick: make(chan struct{}, 1),
	}
	f.flushed = sync.NewCond(&f.mu)
	if f.log == nil {
		f.log = slog.New(slog.DiscardHandler)
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		f.create(o.Import)
	}
	raw, st, err := f.readDisk(time.Now().Add(lockWait))
	if err != nil {
		// Nothing read yet: the zero stamp makes the first poll try again.
		f.log.Warn("prefs: the file cannot be read yet", "err", err)
		raw, st = map[string]json.RawMessage{}, stamp{}
	}
	f.mu.Lock()
	f.raw, f.stamp = raw, st
	f.mu.Unlock()
	poll := o.Poll
	if poll == 0 {
		poll = PollInterval
	}
	f.stop, f.stopped = make(chan struct{}), make(chan struct{})
	go f.run(poll)
	return f, nil
}

// create writes a new file with what imp gives (nothing when it fails).
func (f *File) create(imp Importer) {
	values := map[string]string{}
	if imp != nil {
		got, err := imp()
		if err != nil {
			f.log.Info("prefs: nothing imported from the WebView2 window", "err", err)
		}
		for k, v := range got {
			values[k] = v
		}
	}
	if err := os.MkdirAll(filepath.Dir(f.path), 0o700); err != nil {
		f.log.Warn("prefs: no folder for the file", "err", err)
		return
	}
	unlock := f.lock()
	defer unlock()
	if _, err := os.Stat(f.path); err == nil {
		return // another window made it meanwhile
	}
	raw := map[string]json.RawMessage{}
	for k, v := range values {
		raw[k], _ = json.Marshal(v)
	}
	if _, err := f.writeDisk(raw); err != nil {
		f.log.Warn("prefs: cannot create the file", "err", err)
	}
}

// Close makes one last attempt to store the changes not written yet (it can wait as long as
// one write, a few seconds when another program holds the file), logs what it could not
// store, and stops following other processes' changes. A Set after Close writes at once.
func (f *File) Close() {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	f.closed = true
	stop, stopped := f.stop, f.stopped
	f.mu.Unlock()
	close(stop)
	<-stopped
}

// Path is the file's path.
func (f *File) Path() string { return f.path }

// Get implements state.Storage: this process's newest value, written or not.
func (f *File) Get(key string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return valueOf(f.raw, key)
}

func valueOf(raw map[string]json.RawMessage, key string) (string, bool, error) {
	r, ok := raw[key]
	if !ok {
		return "", false, nil
	}
	var s string
	if json.Unmarshal(r, &s) != nil {
		return "", false, nil // not a string: not this build's
	}
	return s, true, nil
}

// Values are the string values (a copy).
func (f *File) Values() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]string{}
	for k := range f.raw {
		if v, ok, _ := valueOf(f.raw, k); ok {
			out[k] = v
		}
	}
	return out
}

// Set implements state.Storage: value is this File's under key at once, and the writer
// stores it, keeping what other processes wrote meanwhile. Only a File already closed
// writes before Set returns (and then returns the write's error).
func (f *File) Set(key, value string) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return f.enqueue(key, data)
}

// Remove implements state.Storage, as Set does.
func (f *File) Remove(key string) error { return f.enqueue(key, nil) }

func (f *File) enqueue(key string, value json.RawMessage) error {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return f.write(map[string]change{key: {value: value}})
	}
	f.gen++
	c := change{value: value, gen: f.gen}
	f.pending[key] = c
	c.apply(f.raw, key)
	f.mu.Unlock()
	select {
	case f.kick <- struct{}{}:
	default: // the writer is told already
	}
	return nil
}

// WriteErr implements state.WriteStatus: why the newest changes are not stored yet (they
// are being retried), nil when everything is.
func (f *File) WriteErr() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.pending) == 0 {
		return nil
	}
	return f.writeErr
}

// WatchWrites implements state.WriteStatus: fn is called (on the writer's goroutine) with
// the new WriteErr each time a write fails after a success, or succeeds after a failure.
func (f *File) WatchWrites(fn func(err error)) (cancel func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.nextW
	f.nextW++
	f.errWatch[id] = fn
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		delete(f.errWatch, id)
	}
}

// Flush waits until the writer has attempted every change made before the call and returns
// that attempt's error (nil when they are all on disk). It never makes the writer try
// sooner than it would.
func (f *File) Flush() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	want := f.gen
	for f.tried < want && !f.finishedLocked() {
		f.flushed.Wait()
	}
	switch {
	case f.done >= want:
		return nil
	case f.writeErr != nil:
		return f.writeErr
	}
	return errClosed
}

// finishedLocked reports that the writer has stopped.
func (f *File) finishedLocked() bool {
	select {
	case <-f.stopped:
		return true
	default:
		return false
	}
}

// run is the File's goroutine: it writes the pending changes when told, retries a failed
// write, polls the file, and at stop makes the last attempt.
func (f *File) run(poll time.Duration) {
	defer func() {
		f.mu.Lock()
		close(f.stopped)
		f.flushed.Broadcast()
		f.mu.Unlock()
	}()
	var tick <-chan time.Time
	if poll > 0 {
		t := time.NewTicker(poll)
		defer t.Stop()
		tick = t.C
	}
	// A stopped timer that never fired: nothing can be read from it until a Reset.
	retry := time.NewTimer(time.Hour)
	retry.Stop()
	defer retry.Stop()
	waiting := false // a retry is scheduled
	delay := retryFirst
	attempt := func() {
		if f.writePending() == nil {
			delay = retryFirst
			return
		}
		retry.Reset(delay)
		waiting = true
		delay = min(2*delay, retryMax)
	}
	for {
		select {
		case <-f.stop:
			if err := f.writePending(); err != nil {
				f.mu.Lock()
				keys := slices.Sorted(maps.Keys(f.pending))
				f.mu.Unlock()
				f.log.Error("prefs: closed with changes that could not be stored", "keys", keys, "err", err)
			}
			return
		case <-tick:
			f.Reload()
		case <-f.kick:
			if !waiting { // else the scheduled retry takes this change too
				attempt()
			}
		case <-retry.C:
			waiting = false
			attempt()
		}
	}
}

// writePending makes one attempt to store the pending changes: nil when there were none or
// they are on disk now.
func (f *File) writePending() error {
	f.mu.Lock()
	if len(f.pending) == 0 {
		f.mu.Unlock()
		return nil
	}
	batch := make(map[string]change, len(f.pending))
	for k, c := range f.pending {
		batch[k] = c
	}
	upto := f.gen
	prevErr := f.writeErr
	f.mu.Unlock()

	err := f.write(batch)

	f.mu.Lock()
	f.tried = max(f.tried, upto)
	if err == nil {
		f.done = max(f.done, upto)
	}
	f.writeErr = err
	var errWatchers []func(error)
	if (err == nil) != (prevErr == nil) {
		for _, fn := range f.errWatch {
			errWatchers = append(errWatchers, fn)
		}
	}
	f.flushed.Broadcast()
	f.mu.Unlock()
	switch {
	case err != nil && prevErr == nil:
		f.log.Warn("prefs: a change cannot be stored yet; retrying", "err", err)
	case err == nil && prevErr != nil:
		f.log.Info("prefs: the changes are stored again")
	}
	for _, fn := range errWatchers {
		fn(err)
	}
	return err
}

// write applies batch to what is on disk now and writes the result, under the lock file.
// A file that cannot be read is left as it is, never replaced by the keys changed. On
// success the changes the batch covered leave pending (a newer change of a key stays), and
// the watchers hear of what other processes had written meanwhile.
func (f *File) write(batch map[string]change) error {
	// The lock file first: polls go on while another process holds it.
	unlock := f.lock()
	defer unlock()
	f.io.Lock()
	defer f.io.Unlock()
	disk, _, err := f.readDisk(time.Now().Add(lockWait))
	if err != nil {
		return fmt.Errorf("prefs: nothing was changed: %w", err)
	}
	for k, c := range batch {
		c.apply(disk, k)
	}
	st, err := f.writeDisk(disk)
	if err != nil {
		return fmt.Errorf("prefs: %w", err)
	}
	f.mu.Lock()
	for k, c := range batch {
		if cur, ok := f.pending[k]; ok && cur.gen == c.gen {
			delete(f.pending, k)
		}
	}
	view := f.overlayLocked(disk)
	keys := changedKeys(f.raw, view)
	f.raw, f.stamp = view, st
	watchers := f.watchersLocked()
	f.mu.Unlock()
	// What another process had written meanwhile is news to this one's watchers; this
	// process's own changes are not (a window's storage event never reports its own).
	f.notify(watchers, keys)
	return nil
}

// overlayLocked is disk with the changes not written yet laid over it (disk is changed).
func (f *File) overlayLocked(disk map[string]json.RawMessage) map[string]json.RawMessage {
	for k, c := range f.pending {
		c.apply(disk, k)
	}
	return disk
}

// Watch implements state.SharedStorage: f is called with each key another process
// changed (on the File's goroutine).
func (f *File) Watch(fn func(key string)) (cancel func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.nextW
	f.nextW++
	f.watch[id] = fn
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		delete(f.watch, id)
	}
}

func (f *File) watchersLocked() []func(string) {
	out := make([]func(string), 0, len(f.watch))
	for _, fn := range f.watch {
		out = append(out, fn)
	}
	return out
}

func (f *File) notify(watchers []func(string), keys []string) {
	for _, k := range keys {
		for _, fn := range watchers {
			fn(k)
		}
	}
}

func changedKeys(a, b map[string]json.RawMessage) []string {
	var out []string
	for k, v := range a {
		if w, ok := b[k]; !ok || !bytes.Equal(v, w) {
			out = append(out, k)
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

// Reload looks for changes other processes made (the File does every Poll) and tells the
// watchers which keys changed. A file that cannot be read for the moment changes nothing:
// the values read last stay, and the next Reload tries again. This process's changes not
// written yet stay over what is read.
func (f *File) Reload() {
	f.io.Lock()
	defer f.io.Unlock()
	st := stampOf(f.path)
	f.mu.Lock()
	same := st == f.stamp
	f.mu.Unlock()
	if same {
		return
	}
	disk, st, err := f.readDisk(time.Now().Add(readWait))
	if err != nil {
		f.log.Debug("prefs: the file cannot be read for now", "err", err)
		return
	}
	f.mu.Lock()
	view := f.overlayLocked(disk)
	keys := changedKeys(f.raw, view)
	f.raw, f.stamp = view, st
	watchers := f.watchersLocked()
	f.mu.Unlock()
	f.notify(watchers, keys)
}

// errTooLarge is a file far larger than this package ever writes: not ours to read.
var errTooLarge = errors.New("prefs: the file is too large")

// readDisk reads the file: its entries and the stamp of what was read. A missing file has
// no entries, and so has one that was read but is not a JSON object (or is too large to be
// ours): the next write replaces it. Any other failure (another program holding the file
// without sharing, a writer's rename in progress, a permission) is retried every 10 ms until
// deadline and then returned, so that the caller keeps what it has.
func (f *File) readDisk(deadline time.Time) (map[string]json.RawMessage, stamp, error) {
	for {
		data, st, err := readShared(f.path)
		switch {
		case err == nil:
		case errors.Is(err, os.ErrNotExist):
			return map[string]json.RawMessage{}, stamp{}, nil
		case errors.Is(err, errTooLarge):
			f.log.Warn("prefs: the file is too large; it reads as empty")
			return map[string]json.RawMessage{}, st, nil
		default:
			if !time.Now().Before(deadline) {
				return nil, stamp{}, err
			}
			time.Sleep(10 * time.Millisecond)
			continue
		}
		raw := map[string]json.RawMessage{}
		dec := json.NewDecoder(bytes.NewReader(data))
		if dec.Decode(&raw) != nil || raw == nil {
			f.log.Warn("prefs: the file is not a JSON object; it reads as empty")
			return map[string]json.RawMessage{}, st, nil
		}
		return raw, st, nil
	}
}

// readShared reads the file and the stamp of the handle it read through (so the stamp is
// the content's, whatever is renamed over the path meanwhile).
func readShared(path string) ([]byte, stamp, error) {
	file, err := openShared(path)
	if err != nil {
		return nil, stamp{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, stamp{}, err
	}
	st := stamp{info.ModTime(), info.Size(), true}
	if info.Size() > maxSize {
		return nil, st, errTooLarge
	}
	buf := make([]byte, info.Size())
	n, err := file.ReadAt(buf, 0)
	if err != nil && n != len(buf) {
		return nil, stamp{}, err
	}
	return buf, st, nil
}

// writeDisk replaces the file atomically with raw (keys sorted, indented) and returns the
// stamp of what it wrote: the temporary file's, taken before the rename (which keeps it), so
// that a write another process makes right after this one still changes the stamp.
func (f *File) writeDisk(raw map[string]json.RawMessage) (stamp, error) {
	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return stamp{}, err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(f.path), filepath.Base(f.path)+".*.tmp")
	if err != nil {
		return stamp{}, err
	}
	name := tmp.Name()
	_, werr := tmp.Write(data)
	serr := tmp.Sync()
	cerr := tmp.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		_ = os.Remove(name)
		return stamp{}, err
	}
	st := stampOf(name)
	// A reader without delete sharing (another program) can hold the file a moment.
	for attempt := 0; ; attempt++ {
		err = os.Rename(name, f.path)
		if err == nil || attempt >= 40 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err != nil {
		_ = os.Remove(name)
		return stamp{}, err
	}
	return st, nil
}

// lock takes the lock file beside the file (best effort: after lockWait, or when the
// folder cannot hold one, the write goes on without it). The lock file holds a token of its
// own; unlock removes it only while it still holds that token, so a writer whose lock
// another process took as stale never removes that process's lock.
func (f *File) lock() (unlock func()) {
	path := f.path + ".lock"
	token := lockToken()
	deadline := time.Now().Add(lockWait)
	for {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, werr := file.Write(token)
			cerr := file.Close()
			if werr != nil || cerr != nil {
				_ = os.Remove(path) // nobody else can hold a token there yet
				return func() {}
			}
			return func() { removeLock(path, token) }
		}
		if !errors.Is(err, os.ErrExist) {
			return func() {}
		}
		if held, info, rerr := readLock(path); rerr == nil && time.Since(info.ModTime()) > lockStale {
			removeLock(path, held) // left by a process that died
			continue
		}
		if time.Now().After(deadline) {
			f.log.Warn("prefs: the lock file stays taken; writing without it")
			return func() {}
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// lockToken is what one lock holds: this process and 16 random bytes.
func lockToken() []byte {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return fmt.Appendf(nil, "%d %s\n", os.Getpid(), hex.EncodeToString(b[:]))
}

func readLock(path string) ([]byte, os.FileInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, nil, err
	}
	held, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	return held, info, nil
}

// removeLock removes the lock file while it holds token.
func removeLock(path string, token []byte) {
	held, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(held, token) {
		return
	}
	_ = os.Remove(path)
}
