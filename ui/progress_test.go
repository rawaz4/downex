package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"downex/internal/client"
	downexv1 "downex/internal/rpc/downex/v1"
)

func TestViewOf(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want int64
	}{
		{nil, 0},
		{[]string{"--progress", "42"}, 42},
		{[]string{"--progress=7"}, 7},
		{[]string{"-progress", "8", "--progress", "9"}, 8},
		{[]string{"--confirm", "5", "--progress", "6"}, 6},
		{[]string{"--progress", "0", "--progress", "-1", "--progress", "x", "--progress=9223372036854775808"}, 0},
		{[]string{"progress", "5", "--progressive", "6", "--progress"}, 0},
		{[]string{"--progress", "abc", "--progress", "12"}, 12},
	} {
		if got := viewOf(tc.args); got.progress != tc.want {
			t.Errorf("viewOf(%q) = %d, want %d", tc.args, got.progress, tc.want)
		}
	}
	// The progress flag does not count as a confirmation request, nor the reverse.
	if got := confirmArgs([]string{"--progress", "5"}); got != nil {
		t.Errorf("confirmArgs(--progress 5) = %v", got)
	}
}

func TestWindowSpecs(t *testing.T) {
	main := view{}.window()
	if main.width != 1200 || main.height != 760 || main.minWidth != 900 || main.minHeight != 600 || main.instanceID != singleInstanceID {
		t.Errorf("main window = %+v", main)
	}
	if main.frameless != (runtime.GOOS == "windows") {
		t.Errorf("main window frameless = %v on %s", main.frameless, runtime.GOOS)
	}
	p := view{progress: 5}.window()
	want := windowSpec{
		title: "Downex", width: 520, height: 300, minWidth: 480, minHeight: 260, instanceID: singleInstanceID + "-progress-5",
		light: rgb{0xff, 0xff, 0xff}, dark: rgb{0x17, 0x1e, 0x1b},
	}
	if p != want {
		t.Errorf("progress window = %+v, want %+v", p, want)
	}
	// One lock per download, none shared with the main window.
	ids := []string{main.instanceID, p.instanceID, view{progress: 6}.window().instanceID, view{progress: 55}.window().instanceID}
	for i := range ids {
		for j := range i {
			if ids[i] == ids[j] {
				t.Errorf("windows %d and %d share the single-instance id %q", i, j, ids[i])
			}
		}
	}
	if (view{progress: 5}).window().instanceID != p.instanceID {
		t.Error("the single-instance id of a download's window is not stable")
	}
}

func TestWindowFrameAndBackground(t *testing.T) {
	// Only the Windows main window is frameless (the page draws its title bar); progress
	// windows and the other systems keep the system frame.
	for _, goos := range []string{"windows", "darwin", "linux"} {
		if got := (view{}).windowOn(goos).frameless; got != (goos == "windows") {
			t.Errorf("main window on %s: frameless = %v", goos, got)
		}
		if (view{progress: 3}).windowOn(goos).frameless {
			t.Errorf("progress window on %s is frameless", goos)
		}
	}
	// The window opens in its page's ground: --chrome for the main window, --surface for a
	// progress window (docs/DESIGN-SYSTEM.md §2.1).
	for _, tc := range []struct {
		v           view
		dark        bool
		want        rgb
		description string
	}{
		{view{}, false, rgb{0xee, 0xf2, 0xf0}, "main, light"},
		{view{}, true, rgb{0x0a, 0x0e, 0x0c}, "main, dark"},
		{view{progress: 9}, false, rgb{0xff, 0xff, 0xff}, "progress, light"},
		{view{progress: 9}, true, rgb{0x17, 0x1e, 0x1b}, "progress, dark"},
	} {
		if got := tc.v.window().background(tc.dark); got != tc.want {
			t.Errorf("%s: background = %+v, want %+v", tc.description, got, tc.want)
		}
	}
}

func TestStartupBinding(t *testing.T) {
	// openWith is always sent: true where OpenWith shows the system's dialog (Windows).
	with := strconv.FormatBool(canOpenWith)
	if canOpenWith != (runtime.GOOS == "windows") {
		t.Errorf("canOpenWith = %v on %s", canOpenWith, runtime.GOOS)
	}
	// hibernate and wakeTimers are always sent too (docs/M6-SPEC.md §1).
	caps := `"hibernate":` + strconv.FormatBool(runtime.GOOS != "darwin") + `,"wakeTimers":` + strconv.FormatBool(runtime.GOOS == "windows")
	// customTitleBar is always sent: true for the frameless main window (Windows).
	// build (the About page) ends the object; about_test.go checks it.
	build, err := json.Marshal(currentBuild())
	if err != nil {
		t.Fatal(err)
	}
	a, _ := newTestApp(t, newFakeDaemon(), nil)
	raw, err := a.Startup()
	custom := `,"customTitleBar":` + strconv.FormatBool(runtime.GOOS == "windows")
	if want := `{"view":"main","openWith":` + with + `,` + caps + custom + `,"build":` + string(build) + `}`; err != nil || string(raw) != want {
		t.Errorf("main window: Startup = %s, %v; want %s", raw, err, want)
	}
	a.view = viewOf([]string{"--progress", "42"})
	raw, err = a.Startup()
	if want := `{"view":"progress","id":"42","openWith":` + with + `,` + caps + `,"customTitleBar":false,"build":` + string(build) + `}`; err != nil || string(raw) != want {
		t.Errorf("progress window: Startup = %s, %v; want %s", raw, err, want)
	}
}

func TestPlatformCapabilities(t *testing.T) {
	for _, tc := range []struct {
		goos             string
		hibernate, timer bool
	}{
		{"windows", true, true},
		{"darwin", false, false},
		{"linux", true, false},
	} {
		if got := canHibernate(tc.goos); got != tc.hibernate {
			t.Errorf("canHibernate(%s) = %v", tc.goos, got)
		}
		if got := hasWakeTimers(tc.goos); got != tc.timer {
			t.Errorf("hasWakeTimers(%s) = %v", tc.goos, got)
		}
	}
}

func TestOpenProgressWindow(t *testing.T) {
	a, _ := newTestApp(t, newFakeDaemon(), nil)
	var mu sync.Mutex
	var opened []int64
	var fail error
	a.openWindow = func(id int64) error {
		mu.Lock()
		defer mu.Unlock()
		opened = append(opened, id)
		return fail
	}
	if err := a.OpenProgressWindow(json.RawMessage(`{"id":"5"}`)); err != nil {
		t.Fatal(err)
	}
	if err := a.OpenProgressWindow(json.RawMessage(`{"id":6}`)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`{}`, `{"id":0}`, `{"id":-3}`} {
		if err := a.OpenProgressWindow(json.RawMessage(bad)); err == nil || !strings.HasPrefix(err.Error(), "invalid_argument: ") {
			t.Errorf("OpenProgressWindow(%s) = %v", bad, err)
		}
	}
	if err := a.OpenProgressWindow(json.RawMessage(`{"id":"x"}`)); err == nil || !strings.HasPrefix(err.Error(), "invalid_argument: malformed request") {
		t.Errorf("OpenProgressWindow with a malformed id = %v", err)
	}
	mu.Lock()
	fail = errors.New("no such file")
	mu.Unlock()
	if err := a.OpenProgressWindow(json.RawMessage(`{"id":7}`)); err == nil ||
		err.Error() != "internal: cannot open the progress window: no such file" {
		t.Errorf("a failed start = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(opened, []int64{5, 6, 7}) {
		t.Errorf("opened %v", opened)
	}
}

func TestGetAndSetDownloadOptions(t *testing.T) {
	f := newFakeDaemon(sampleDownload())
	a, _ := newTestApp(t, f, nil)

	raw, err := a.Get(json.RawMessage(`{"id":"7"}`))
	if err != nil {
		t.Fatal(err)
	}
	if d := decodeJSON(t, raw)["download"].(map[string]any); d["id"] != "7" || d["state"] != "DOWNLOAD_STATE_DOWNLOADING" {
		t.Errorf("Get = %s", raw)
	}
	if _, err := a.Get(json.RawMessage(`{"id":3}`)); err == nil || !strings.HasPrefix(err.Error(), "not_found: ") {
		t.Errorf("Get of an unknown download = %v", err)
	}

	// Absent fields stay absent; 0 is sent as 0 (unlimited, the daemon's default).
	raw, err = a.SetDownloadOptions(json.RawMessage(`{"id":"7","speedLimit":"1048576"}`))
	if err != nil {
		t.Fatal(err)
	}
	got := f.lastRequest(t).(*downexv1.SetDownloadOptionsRequest)
	if got.GetId() != 7 || got.SpeedLimit == nil || got.GetSpeedLimit() != 1<<20 || got.MaxConnections != nil {
		t.Errorf("SetDownloadOptions sent %v", got)
	}
	if d := decodeJSON(t, raw)["download"].(map[string]any); d["speedLimit"] != "1048576" || d["maxConnections"] != float64(8) {
		t.Errorf("SetDownloadOptionsResponse = %s", raw)
	}
	if _, err := a.SetDownloadOptions(json.RawMessage(`{"id":7,"speedLimit":0,"maxConnections":4}`)); err != nil {
		t.Fatal(err)
	}
	want := &downexv1.SetDownloadOptionsRequest{Id: 7, SpeedLimit: proto.Int64(0), MaxConnections: proto.Int32(4)}
	if got := f.lastRequest(t); !proto.Equal(got, want) {
		t.Errorf("SetDownloadOptions sent %v, want %v", got, want)
	}
	if _, err := a.SetDownloadOptions(json.RawMessage(`{"id":7,"speed":1}`)); err == nil ||
		!strings.HasPrefix(err.Error(), "invalid_argument: malformed request: ") {
		t.Errorf("SetDownloadOptions with an unknown field = %v", err)
	}
	if _, err := a.SetDownloadOptions(json.RawMessage(`{"id":3,"speedLimit":1}`)); err == nil || !strings.HasPrefix(err.Error(), "not_found: ") {
		t.Errorf("SetDownloadOptions of an unknown download = %v", err)
	}
}

// A progress window watches its own download only, and shows no confirmation dialogs.
func TestProgressWindowWatchesItsDownload(t *testing.T) {
	sw := &scriptedWatch{sessions: []func(context.Context) (WatchStream, error){
		stream(nil, snapshotOf(42), updated(42, 10)),
	}}
	f := newFakeDaemon()
	f.watch = sw.watch
	h := &fakeHost{emitted: make(chan json.RawMessage, 8)}
	a, _ := newTestApp(t, f, nil)
	a.view = viewOf([]string{"--progress", "42"})
	a.requestConfirmations(confirmArgs([]string{"--confirm", "5"}))
	a.startup(h)
	a.domReady()

	if got := eventKinds(t, nextEvent(t, h.emitted)); !slices.Equal(got, []string{"snapshot:42"}) {
		t.Errorf("first batch = %v", got)
	}
	if got := eventKinds(t, nextEvent(t, h.emitted)); !slices.Equal(got, []string{"updated:42"}) {
		t.Errorf("second batch = %v", got)
	}
	f.mu.Lock()
	ids := f.watchIDs
	f.mu.Unlock()
	if len(ids) == 0 || !slices.Equal(ids[0], []int64{42}) {
		t.Errorf("Watch ids = %v, want [[42]]", ids)
	}

	// A second start for the same download brings the window forward and asks nothing.
	a.secondInstance([]string{"--progress", "42", "--confirm", "6"})
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.focused != 1 {
		t.Errorf("Focus called %d times, want 1", h.focused)
	}
	if len(h.confirmIDs) != 0 {
		t.Errorf("a progress window asked for confirmations: %s", h.confirmIDs)
	}
}

// The main window still watches every download.
func TestMainWindowWatchesEverything(t *testing.T) {
	sw := &scriptedWatch{sessions: []func(context.Context) (WatchStream, error){stream(nil, snapshotOf(1))}}
	f := newFakeDaemon()
	f.watch = sw.watch
	h := &fakeHost{emitted: make(chan json.RawMessage, 4)}
	newTestApp(t, f, h)
	nextEvent(t, h.emitted)
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.watchIDs) == 0 || f.watchIDs[0] != nil {
		t.Errorf("Watch ids = %v, want all downloads", f.watchIDs)
	}
}

// A watched download that does not exist (removed meanwhile, or never there) is
// reported as removed, whether Watch refuses at once or its stream says so.
func TestBridgeReportsAMissingDownloadAsRemoved(t *testing.T) {
	notFound := connect.NewError(connect.CodeNotFound, errors.New("download 42 not found"))
	f := newFakeDaemon()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sw := &scriptedWatch{sessions: []func(context.Context) (WatchStream, error){
		refuse(notFound),
		stream(notFound),
		refuse(connect.NewError(connect.CodeUnavailable, errors.New("down"))),
		stream(nil, snapshotOf(42)),
	}}
	f.watch = sw.watch
	var payloads []string
	b := &eventBridge{
		daemon: f, ids: []int64{42}, log: slog.New(slog.DiscardHandler),
		emit: func(p json.RawMessage) {
			payloads = append(payloads, string(p))
			if len(payloads) == 3 {
				cancel()
			}
		},
		min: time.Millisecond, max: time.Millisecond,
		sleep: func(context.Context, time.Duration) bool { return ctx.Err() == nil },
	}
	b.run(ctx)
	want := []string{`{"events":[{"removed":"42"}]}`, `{"events":[{"removed":"42"}]}`}
	if len(payloads) != 3 || !reflect.DeepEqual(payloads[:2], want) || !strings.Contains(payloads[2], `"snapshot"`) {
		t.Errorf("payloads = %q, want two removals, then the snapshot", payloads)
	}

	// Without ids (the main window), not_found is just another failed subscription.
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	f.watch = (&scriptedWatch{sessions: []func(context.Context) (WatchStream, error){refuse(notFound)}}).watch
	var emitted int
	b = &eventBridge{
		daemon: f, log: slog.New(slog.DiscardHandler), emit: func(json.RawMessage) { emitted++ },
		min: time.Millisecond, max: time.Millisecond,
		sleep: func(context.Context, time.Duration) bool { cancel2(); return false },
	}
	b.run(ctx2)
	if emitted != 0 {
		t.Errorf("the main window's bridge emitted %d batches", emitted)
	}
}

func TestClientDaemonWatchesSomeDownloads(t *testing.T) {
	sys := &fakeSystem{}
	d := newClientDaemon(sys.env(t))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s, err := d.Watch(ctx, []int64{2})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !s.Receive() {
		t.Fatalf("no snapshot: %v", s.Err())
	}
	if ev := s.Msg().GetEvents(); len(ev) != 1 || ev[0].GetSnapshot().GetId() != 2 {
		t.Errorf("snapshot = %v, want download 2 only", ev)
	}

	// An unknown id ends the stream with not_found (from Receive, as Connect reports it).
	s2, err := d.Watch(ctx, []int64{9})
	if err == nil {
		defer s2.Close()
		if s2.Receive() {
			t.Fatal("a stream for an unknown download delivered a batch")
		}
		err = s2.Err()
	}
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("Watch of an unknown download: %v, want not_found", err)
	}

	resp, err := d.SetDownloadOptions(ctx, &downexv1.SetDownloadOptionsRequest{Id: 2, SpeedLimit: proto.Int64(4096)})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetDownload().GetSpeedLimit() != 4096 {
		t.Errorf("SetDownloadOptions = %v", resp)
	}
}

// TestProgressProgram: inside Downex.app the progress windows are the progress app's
// (docs/MACOS-SPEC.md §1, client.Layout), when it is installed; otherwise, and on every
// other system, this program shows them.
func TestProgressProgram(t *testing.T) {
	const app = "/Applications/Downex.app"
	const helper = app + "/Contents/Helpers/Downex Progress.app/Contents/MacOS/Downex"
	installed := func(p string) bool { return p == helper }
	missing := func(string) bool { return false }
	for _, tc := range []struct {
		exe    string
		isFile func(string) bool
		want   string
	}{
		{app + "/Contents/MacOS/Downex", installed, helper},
		{app + "/Contents/MacOS/Downex", missing, app + "/Contents/MacOS/Downex"},
		{helper, installed, helper}, // the progress app is its own progress program
		{"/src/ui/build/bin/Downex.app/Contents/MacOS/Downex", installed, "/src/ui/build/bin/Downex.app/Contents/MacOS/Downex"},
		{"/src/ui/build/bin/Downex", installed, "/src/ui/build/bin/Downex"},
	} {
		if got := progressProgram(client.LayoutOf("darwin", tc.exe), tc.exe, tc.isFile); got != tc.want {
			t.Errorf("progressProgram(%q) = %q, want %q", tc.exe, got, tc.want)
		}
	}
	exe := filepath.Join(t.TempDir(), "downex-ui.exe")
	if got := progressProgram(client.LayoutOf("windows", exe), exe, func(string) bool { return true }); got != exe {
		t.Errorf("windows: progressProgram = %q, want %q", got, exe)
	}
}
