//go:build windows

package prefs_test

import (
	"bytes"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"downex/ui/host"
	"downex/ui/state"
	"downex/ui/state/prefs"
)

// holdUnshared opens path the way another program can (an antivirus scan, a backup tool):
// for reading, sharing nothing, so that nobody else can open it or rename over it.
func holdUnshared(t *testing.T, path string) (release func()) {
	t.Helper()
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	done := false
	release = func() {
		if !done {
			done = true
			_ = windows.CloseHandle(h)
		}
	}
	t.Cleanup(release)
	return release
}

func threeKeys(t *testing.T, f *prefs.File) {
	t.Helper()
	for k, v := range map[string]string{state.ThemeKey: "dark", state.DensityKey: "compact", state.ColumnWidthsKey: `{"size":82}`} {
		if err := f.Set(k, v); err != nil {
			t.Fatal(err)
		}
	}
	flush(t, f)
}

// A change while another program holds the file for a moment is written once it lets go,
// keeping every other key.
func TestSetWaitsForAReaderThatSharesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), prefs.FileName)
	f := open(t, path, prefs.Options{Poll: -1})
	threeKeys(t, f)
	release := holdUnshared(t, path)
	time.AfterFunc(150*time.Millisecond, release)
	if err := f.Set(state.ExistingFileKey, "replace"); err != nil {
		t.Fatal(err)
	}
	flush(t, f)
	m := fileJSON(t, path)
	if len(m) != 4 || m[state.ThemeKey] != "dark" || m[state.DensityKey] != "compact" || m[state.ColumnWidthsKey] != `{"size":82}` || m[state.ExistingFileKey] != "replace" {
		t.Fatal(m)
	}
	if len(f.Values()) != 4 {
		t.Fatal(f.Values())
	}
}

// uiBound is how long a store's Set may take on the UI thread: well under a frame.
const uiBound = 50 * time.Millisecond

// While another program holds the file, the theme and density stores change at once and
// never wait for it (the click handler's thread); the write fails, leaves the file as it
// was, is reported through the File and the stores, and is retried. When the file is free
// again the newest choices are written (several changes of one key coalesce), the report
// clears, and another window sees the change within 1 s.
func TestAChangeWhileTheFileIsHeldIsRetriedAndReported(t *testing.T) {
	path := filepath.Join(t.TempDir(), prefs.FileName)
	f := open(t, path, prefs.Options{Poll: -1})
	threeKeys(t, f)
	other := open(t, path, prefs.Options{})
	theme := state.NewTheme(state.ThemeOptions{Storage: f})
	t.Cleanup(theme.Close)
	density := state.NewDensity(f)
	t.Cleanup(density.Close)
	reports := make(chan error, 16)
	f.WatchWrites(func(err error) { reports <- err })
	themeChanges := make(chan struct{}, 64)
	theme.Changes.Subscribe(func() { themeChanges <- struct{}{} })
	seen := make(chan string, 16)
	other.Watch(func(k string) { seen <- k })

	release := holdUnshared(t, path)
	var slowest time.Duration
	for _, c := range []func(){
		func() { theme.Set(host.ThemeLight) },
		func() { density.Set(state.Comfortable) },
		func() { theme.Cycle() }, // light, then dark
		func() { theme.Set(host.ThemeLight) },
		func() { state.SaveExistingFileChoice(f, state.Replace) },
		func() { theme.Set(host.ThemeSystem) }, // removes the key
		func() { theme.Set(host.ThemeLight) },
	} {
		start := time.Now()
		c()
		slowest = max(slowest, time.Since(start))
	}
	if slowest > uiBound {
		t.Fatalf("a store's Set took %v while the file was held", slowest)
	}
	t.Logf("the slowest store Set took %v while the file was held", slowest)
	if theme.Choice() != host.ThemeLight || density.Choice() != state.Comfortable {
		t.Fatal(theme.Choice(), density.Choice())
	}
	if v, _, _ := f.Get(state.ThemeKey); v != "light" {
		t.Fatal(v)
	}

	// The attempt fails with the sharing violation; the file is as it was.
	start := time.Now()
	if err := f.Flush(); !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		t.Fatalf("the write gave %v", err)
	}
	t.Logf("the first attempt failed after %v", time.Since(start))
	select {
	case err := <-reports:
		if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("the failure was not reported")
	}
	if !errors.Is(f.WriteErr(), windows.ERROR_SHARING_VIOLATION) || theme.StorageErr() == nil || density.StorageErr() == nil {
		t.Fatal(f.WriteErr(), theme.StorageErr(), density.StorageErr())
	}
	release()
	m := fileJSON(t, path)
	if len(m) != 3 || m[state.ThemeKey] != "dark" || m[state.DensityKey] != "compact" || m[state.ExistingFileKey] != nil {
		t.Fatal(m)
	}

	// The retry stores the newest value of each key.
	released := time.Now()
	select {
	case err := <-reports:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the retry did not store the changes")
	}
	t.Logf("the retry stored the changes %v after the file was released", time.Since(released))
	flush(t, f)
	m = fileJSON(t, path)
	if len(m) != 4 || m[state.ThemeKey] != "light" || m[state.DensityKey] != "comfortable" || m[state.ExistingFileKey] != "replace" || m[state.ColumnWidthsKey] != `{"size":82}` {
		t.Fatal(m)
	}
	if f.WriteErr() != nil || theme.StorageErr() != nil || density.StorageErr() != nil {
		t.Fatal(f.WriteErr(), theme.StorageErr(), density.StorageErr())
	}
	written := time.Now()
	got := map[string]bool{}
	for len(got) < 3 {
		select {
		case k := <-seen:
			got[k] = true
		case <-time.After(time.Second):
			t.Fatalf("the other window saw only %v within 1 s of the write", got)
		}
	}
	t.Logf("the other window saw the three keys %v after the write", time.Since(written))
	if v, _, _ := other.Get(state.ThemeKey); v != "light" {
		t.Fatal(v)
	}
	// The theme store told its views of the report and of its clearing as well.
	if n := len(themeChanges); n < 4+2 {
		t.Fatal(n)
	}
}

// A File closed while its changes cannot be written makes one last attempt and logs the
// keys it could not store: nothing is lost silently.
func TestCloseWhileTheFileIsHeldReportsWhatWasNotStored(t *testing.T) {
	path := filepath.Join(t.TempDir(), prefs.FileName)
	threeKeys(t, open(t, path, prefs.Options{Poll: -1}))
	var logged syncBuffer
	f, err := prefs.Open(path, prefs.Options{Poll: -1, Log: slog.New(slog.NewTextHandler(&logged, nil))})
	if err != nil {
		t.Fatal(err)
	}
	release := holdUnshared(t, path)
	if err := f.Set(state.DensityKey, "comfortable"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if out := logged.String(); !strings.Contains(out, "closed with changes that could not be stored") || !strings.Contains(out, state.DensityKey) {
		t.Fatal(out)
	}
	if err := f.Flush(); err == nil {
		t.Fatal("Flush after a failed Close reports success")
	}
	release()
	if m := fileJSON(t, path); m[state.DensityKey] != "compact" {
		t.Fatal(m)
	}
	// A Set after Close writes before it returns.
	if err := f.Set(state.DensityKey, "comfortable"); err != nil {
		t.Fatal(err)
	}
	if m := fileJSON(t, path); m[state.DensityKey] != "comfortable" {
		t.Fatal(m)
	}
}

// syncBuffer is a log's buffer that the writer's goroutine and the test share.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// A poll that meets such a reader keeps the values it had, reports nothing, and reads the
// other process's change at the next poll.
func TestAPollThatCannotReadTheFileKeepsTheValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), prefs.FileName)
	f := open(t, path, prefs.Options{Poll: -1})
	if err := f.Set(state.ThemeKey, "dark"); err != nil {
		t.Fatal(err)
	}
	flush(t, f)
	other := open(t, path, prefs.Options{Poll: -1})
	if err := other.Set(state.DensityKey, "compact"); err != nil { // changes the stamp
		t.Fatal(err)
	}
	flush(t, other)
	var changed []string
	f.Watch(func(k string) { changed = append(changed, k) })
	release := holdUnshared(t, path)
	f.Reload()
	if len(changed) != 0 {
		t.Fatalf("a blocked poll reported %v", changed)
	}
	if v, _, _ := f.Get(state.ThemeKey); v != "dark" {
		t.Fatalf("theme reads %q after a blocked poll", v)
	}
	release()
	f.Reload()
	if len(changed) != 1 || changed[0] != state.DensityKey {
		t.Fatalf("the next poll reported %v", changed)
	}
	if v, _, _ := f.Get(state.DensityKey); v != "compact" {
		t.Fatal(v)
	}
	if v, _, _ := f.Get(state.ThemeKey); v != "dark" {
		t.Fatal(v)
	}
}

// Open waits a moment for such a reader. When the file stays held, it starts with nothing
// read and the first poll after the release reads it.
func TestOpenWhileTheFileIsHeld(t *testing.T) {
	path := filepath.Join(t.TempDir(), prefs.FileName)
	threeKeys(t, open(t, path, prefs.Options{Poll: -1}))
	noImport := func() (map[string]string, error) { t.Error("imported over a file"); return nil, nil }

	release := holdUnshared(t, path)
	time.AfterFunc(100*time.Millisecond, release)
	f := open(t, path, prefs.Options{Poll: -1, Import: noImport})
	if v, _, _ := f.Get(state.ThemeKey); v != "dark" {
		t.Fatal(v)
	}

	release = holdUnshared(t, path)
	g := open(t, path, prefs.Options{Poll: -1, Import: noImport})
	if len(g.Values()) != 0 {
		t.Fatal(g.Values())
	}
	var changed []string
	g.Watch(func(k string) { changed = append(changed, k) })
	release()
	g.Reload()
	if v, _, _ := g.Get(state.ThemeKey); v != "dark" || len(changed) != 3 {
		t.Fatal(v, changed)
	}
}
