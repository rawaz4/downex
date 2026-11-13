package prefs_test

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"downex/ui/host"
	"downex/ui/internal/research"
	"downex/ui/state"
	"downex/ui/state/prefs"
)

func open(t *testing.T, path string, o prefs.Options) *prefs.File {
	t.Helper()
	f, err := prefs.Open(path, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.Close)
	return f
}

// flush waits for f's writer and fails the test when a write failed.
func flush(t *testing.T, f *prefs.File) {
	t.Helper()
	if err := f.Flush(); err != nil {
		t.Fatal(err)
	}
}

func fileJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func owner() map[string]string {
	return map[string]string{
		state.ColumnWidthsKey:    `{"size":82}`,
		state.DensityKey:         "compact",
		state.ExistingFileKey:    "replace",
		state.ProgressDetailsKey: "1",
		state.ThemeKey:           "dark",
	}
}

func TestOpenImportsOnceIntoANewFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Downex", prefs.FileName)
	calls := 0
	imp := func() (map[string]string, error) { calls++; return owner(), nil }
	f := open(t, path, prefs.Options{Import: imp})
	if calls != 1 {
		t.Fatal(calls)
	}
	for k, v := range owner() {
		if got, ok, err := f.Get(k); got != v || !ok || err != nil {
			t.Fatalf("%s = %q %v %v", k, got, ok, err)
		}
	}
	if m := fileJSON(t, path); m[state.ColumnWidthsKey] != `{"size":82}` || len(m) != 5 {
		t.Fatal(m)
	}
	if w := state.ColumnWidths(f); w["size"] != 82 {
		t.Fatal(w)
	}
	f.Close()
	// The file exists now: nothing is imported again.
	open(t, path, prefs.Options{Import: imp})
	if calls != 1 {
		t.Fatal(calls)
	}
}

func TestOpenStartsEmptyWhenTheImportFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), prefs.FileName)
	f := open(t, path, prefs.Options{Import: func() (map[string]string, error) { return nil, errors.New("no WebView2 folder") }})
	if len(f.Values()) != 0 {
		t.Fatal(f.Values())
	}
	if m := fileJSON(t, path); len(m) != 0 {
		t.Fatal(m)
	}
	// a folder that holds no localStorage is no failure either
	g := open(t, filepath.Join(t.TempDir(), prefs.FileName), prefs.Options{Import: prefs.WebView2Import(t.TempDir())})
	if len(g.Values()) != 0 {
		t.Fatal(g.Values())
	}
	if _, err := prefs.Open("", prefs.Options{}); !errors.Is(err, prefs.ErrNoPath) {
		t.Fatal(err)
	}
}

func TestSetAndRemoveKeepOtherKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), prefs.FileName)
	// a key of a later build, with a value that is not a string
	if err := os.WriteFile(path, []byte(`{"downex.later":{"a":1},"downex.density":"compact"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	f := open(t, path, prefs.Options{Poll: -1})
	if v, ok, _ := f.Get("downex.later"); ok || v != "" {
		t.Fatal("a non-string value read as a string")
	}
	if err := f.Set(state.ThemeKey, "light"); err != nil {
		t.Fatal(err)
	}
	if err := f.Remove(state.DensityKey); err != nil {
		t.Fatal(err)
	}
	flush(t, f)
	m := fileJSON(t, path)
	if m[state.ThemeKey] != "light" || m[state.DensityKey] != nil || m["downex.later"] == nil {
		t.Fatal(m)
	}
	// no temporary or lock file is left behind
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatal(entries)
	}
}

func TestAnUnreadableFileReadsAsEmptyAndIsReplacedByTheNextWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), prefs.FileName)
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := open(t, path, prefs.Options{Poll: -1, Import: func() (map[string]string, error) { t.Fatal("imported over a file"); return nil, nil }})
	if len(f.Values()) != 0 {
		t.Fatal(f.Values())
	}
	if err := f.Set(state.DensityKey, "compact"); err != nil {
		t.Fatal(err)
	}
	flush(t, f)
	if m := fileJSON(t, path); m[state.DensityKey] != "compact" {
		t.Fatal(m)
	}
}

func TestTwoFilesSeeEachOthersChangesAndMerge(t *testing.T) {
	path := filepath.Join(t.TempDir(), prefs.FileName)
	a := open(t, path, prefs.Options{})
	b := open(t, path, prefs.Options{})
	seen := make(chan string, 8)
	b.Watch(func(k string) { seen <- k })
	start := time.Now()
	if err := a.Set(state.DensityKey, "compact"); err != nil {
		t.Fatal(err)
	}
	select {
	case k := <-seen:
		if k != state.DensityKey || time.Since(start) > time.Second {
			t.Fatal(k, time.Since(start))
		}
	case <-time.After(time.Second):
		t.Fatal("not seen within 1 s")
	}
	if v, _, _ := b.Get(state.DensityKey); v != "compact" {
		t.Fatal(v)
	}
	// b writes without having seen a's newest change: both stay
	if err := a.Set(state.ThemeKey, "dark"); err != nil {
		t.Fatal(err)
	}
	if err := b.Set(state.ExistingFileKey, "replace"); err != nil {
		t.Fatal(err)
	}
	flush(t, a)
	flush(t, b)
	m := fileJSON(t, path)
	if m[state.ThemeKey] != "dark" || m[state.ExistingFileKey] != "replace" || m[state.DensityKey] != "compact" {
		t.Fatal(m)
	}
}

// A poll while this process's change is still waiting for the lock file keeps that change
// over what it reads, and reports only what the other program changed.
func TestAPollKeepsThisProcesssPendingChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), prefs.FileName)
	f := open(t, path, prefs.Options{Poll: -1})
	if err := f.Set(state.DensityKey, "compact"); err != nil {
		t.Fatal(err)
	}
	flush(t, f)
	// another process's fresh lock: the writer waits for it (up to 2 s)
	if err := os.WriteFile(path+".lock", []byte("4242 other\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var changed []string
	f.Watch(func(k string) { mu.Lock(); changed = append(changed, k); mu.Unlock() })
	start := time.Now()
	if err := f.Set(state.ThemeKey, "dark"); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 50*time.Millisecond {
		t.Fatalf("Set waited %v for the lock", took)
	}
	// the other program writes the file meanwhile
	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(path, []byte(`{"downex.density":"comfortable"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	f.Reload()
	if v, _, _ := f.Get(state.ThemeKey); v != "dark" {
		t.Fatalf("a poll took back the pending theme: %q", v)
	}
	mu.Lock()
	got := slices.Clone(changed)
	mu.Unlock()
	if v, _, _ := f.Get(state.DensityKey); v != "comfortable" || len(got) != 1 || got[0] != state.DensityKey {
		t.Fatal(v, got)
	}
	_ = os.Remove(path + ".lock")
	flush(t, f)
	if m := fileJSON(t, path); m[state.ThemeKey] != "dark" || m[state.DensityKey] != "comfortable" {
		t.Fatal(m)
	}
}

func TestTheThemeFollowsAnotherWindowThroughTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), prefs.FileName)
	a := open(t, path, prefs.Options{})
	b := open(t, path, prefs.Options{})
	ta := state.NewTheme(state.ThemeOptions{Storage: a})
	tb := state.NewTheme(state.ThemeOptions{Storage: b})
	t.Cleanup(ta.Close)
	t.Cleanup(tb.Close)
	changed := make(chan struct{}, 4)
	tb.Changes.Subscribe(func() { changed <- struct{}{} })
	ta.Set(host.ThemeDark)
	select {
	case <-changed:
	case <-time.After(time.Second):
		t.Fatal("the other window did not follow within 1 s")
	}
	if tb.Choice() != host.ThemeDark {
		t.Fatal(tb.Choice())
	}
}

// defaultCopies is where the copies of real WebView2 folders are, in the research folder beside the repository
// ($DOWNEX_RESEARCH names another; as webstorage's tests read them, beside the step-1 references);
// $DOWNEX_WEBSTORAGE_COPIES names another folder.
var defaultCopies = research.Beside(research.NativeUI, "webstorage-copies")

// TestImportsTheOwnerShapedValuesFromAWebView2Copy starts a new file from a copy of a real
// WebView2 folder.
func TestImportsTheOwnerShapedValuesFromAWebView2Copy(t *testing.T) {
	root := os.Getenv("DOWNEX_WEBSTORAGE_COPIES")
	if root == "" {
		root = defaultCopies
	}
	dir := filepath.Join(root, "a")
	if _, err := os.Stat(filepath.Join(dir, "CURRENT")); err != nil {
		t.Skipf("SKIPPED: no copy of a real WebView2 folder at %s (set $DOWNEX_WEBSTORAGE_COPIES to a folder holding a)", dir)
	}
	path := filepath.Join(t.TempDir(), prefs.FileName)
	f := open(t, path, prefs.Options{Import: prefs.WebView2Import(dir)})
	want := map[string]string{
		state.ColumnWidthsKey: `{"size":82}`, state.DensityKey: "compact", state.ExistingFileKey: "replace",
		state.ProgressDetailsKey: "1", state.DetailsCollapsedKey: "1", state.ThemeKey: "dark",
	}
	got := f.Values()
	if len(got) != len(want) { // only the window's keys: not the test's own or the fillers
		t.Fatal(got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s = %q, want %q", k, got[k], v)
		}
	}
	if state.ColumnWidths(f)["size"] != 82 || !state.NewDensity(f).Compact() || state.ExistingFileChoice(f) != state.Replace ||
		!state.ProgressDetails(f) || !state.DetailsCollapsed(f) || state.NewTheme(state.ThemeOptions{Storage: f}).Choice() != host.ThemeDark {
		t.Fatal("the stores read other values")
	}
}

func TestPathsAreBesideTheThemeFile(t *testing.T) {
	theme := host.DefaultThemePath()
	if theme == "" {
		t.Skip("no folder for this user")
	}
	if prefs.DefaultPath() != filepath.Join(filepath.Dir(theme), "ui-prefs.json") || prefs.PreviewPath() != filepath.Join(filepath.Dir(theme), "ui-prefs-native-preview.json") {
		t.Fatal(prefs.DefaultPath(), prefs.PreviewPath())
	}
}

// ---- two processes ----------------------------------------------------------------------------

// TestHelperProcess is the other window: it opens the file, reports each change it sees
// ("seen <key> <value>"), and sets what its standard input says ("set <key> <value>").
func TestHelperProcess(t *testing.T) {
	path := os.Getenv("DOWNEX_PREFS_HELPER")
	if path == "" {
		t.Skip("run by TestTwoProcessesSeeEachOthersChanges")
	}
	f, err := prefs.Open(path, prefs.Options{})
	if err != nil {
		fmt.Println("error", err)
		os.Exit(1)
	}
	f.Watch(func(k string) {
		v, _, _ := f.Get(k)
		fmt.Printf("seen %s %s\n", k, v)
	})
	fmt.Println("ready")
	in := bufio.NewScanner(os.Stdin)
	for in.Scan() {
		parts := strings.Fields(in.Text())
		if len(parts) == 3 && parts[0] == "set" {
			if err := f.Set(parts[1], parts[2]); err != nil {
				fmt.Println("error", err)
			}
			if err := f.Flush(); err != nil {
				fmt.Println("error", err)
			}
			fmt.Println("done", parts[1])
		}
	}
	f.Close()
	os.Exit(0)
}

func TestTwoProcessesSeeEachOthersChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), prefs.FileName)
	f := open(t, path, prefs.Options{})
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.count=1")
	cmd.Env = append(os.Environ(), "DOWNEX_PREFS_HELPER="+path)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close(); _ = cmd.Wait() })
	lines := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	wait := func(prefix string) (string, time.Duration) {
		start := time.Now()
		for {
			select {
			case l, ok := <-lines:
				if !ok {
					t.Fatalf("the other process ended before %q", prefix)
				}
				if strings.HasPrefix(l, prefix) {
					return l, time.Since(start)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("no %q", prefix)
			}
		}
	}
	wait("ready")

	// This process changes a key: the other one sees it within 1 s.
	start := time.Now()
	if err := f.Set(state.DensityKey, "compact"); err != nil {
		t.Fatal(err)
	}
	l, _ := wait("seen " + state.DensityKey)
	took := time.Since(start)
	if l != "seen downex.density compact" || took > time.Second {
		t.Fatal(l, took)
	}
	t.Logf("the other process saw the change after %v", took)

	// The other process changes one: this one sees it within 1 s.
	seen := make(chan time.Time, 1)
	f.Watch(func(k string) {
		if k == state.ThemeKey {
			seen <- time.Now()
		}
	})
	fmt.Fprintln(stdin, "set downex.theme dark")
	_, _ = wait("done downex.theme")
	written := time.Now()
	select {
	case at := <-seen:
		if v, _, _ := f.Get(state.ThemeKey); v != "dark" {
			t.Fatal(v)
		}
		// measured from the other process's answer: at most a poll interval later
		t.Logf("this process saw the change %v after the other one wrote it", max(0, at.Sub(written)))
	case <-time.After(time.Second):
		t.Fatal("not seen within 1 s")
	}
	flush(t, f)
	if m := fileJSON(t, path); m[state.DensityKey] != "compact" || m[state.ThemeKey] != "dark" {
		t.Fatal(m)
	}
}
