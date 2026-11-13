//go:build darwin && cgo

package glue

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"downex/ui/state/prefs"
)

// TestPrefsPollOffWhereWatched [e2e]: the prefs file does not poll by itself where the
// window's file watch reloads it (ui-prefs.json beside ui-theme.json: always, FilesOf), so
// idle windows do not wake 5 times a second each; elsewhere it keeps prefs' poll.
func TestPrefsPollOffWhereWatched(t *testing.T) {
	cases := []struct {
		f    Files
		want time.Duration
	}{
		{FilesOf(false, "/u/Downex", "/u/logs"), -1},
		{FilesOf(true, "/u/Downex", "/u/logs"), -1},
		{Files{Prefs: "/u/Downex/ui-prefs.json"}, -1},                    // the watch takes the prefs folder
		{Files{Theme: "/a/ui-theme.json", Prefs: "/b/ui-prefs.json"}, 0}, // not watched: polls
		{Files{Theme: "/a/ui-theme.json"}, 0},                            // no prefs file
	}
	for _, c := range cases {
		if got := prefsPoll(c.f); got != c.want {
			t.Errorf("prefsPoll(%+v) = %v, want %v", c.f, got, c.want)
		}
	}
}

// TestWatchReloadsPrefsWithoutPoll [e2e]: with the prefs poll off, another process's write of
// ui-prefs.json still reaches the window through the folder's watch (what followFiles does).
func TestWatchReloadsPrefsWithoutPoll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, prefs.FileName)
	f, err := prefs.Open(path, prefs.Options{Poll: prefsPoll(Files{Theme: filepath.Join(dir, "ui-theme.json"), Prefs: path})})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	changed := make(chan struct{}, 8)
	cancel := f.Watch(func(key string) {
		select {
		case changed <- struct{}{}:
		default:
		}
	})
	defer cancel()
	w := watchFiles(dir, []string{prefs.FileName}, followPoll, func(name string) { f.Reload() })
	defer w.Stop()
	other := filepath.Join(dir, "other.tmp")
	if err := os.WriteFile(other, []byte(`{"downex.density":"compact"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(other, path); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Fatal("another process's write did not reach the file")
	}
}
