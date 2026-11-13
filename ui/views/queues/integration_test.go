package queues

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	"downex/internal/client"
	"downex/internal/ipc"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/host"
	"downex/ui/kit"
	"downex/ui/kit/hostcap/hostcaptest"
	"downex/ui/kit/kittest"
	"downex/ui/kit/widgets"
	"downex/ui/state"
	"downex/ui/state/statetest"
	"downex/ui/views"
)

// The plan's acceptance "reorders reach downexd through MoveDownloads": the dialog over the
// real stores and host, against a downexd built from this tree and run with its own
// --endpoint, --db and --log-dir and --no-tray (a private instance, never the user's own; the
// window's client may not start one). The arrows and a drag change the order that downexd
// itself reports. Set DOWNEX_QUEUES_TEST_DOWNEXD to a built downexd to skip the build;
// -short skips the test.

const envQueuesDaemon = "DOWNEX_QUEUES_TEST_DOWNEXD"

func daemonExe(t *testing.T) string {
	t.Helper()
	if exe := os.Getenv(envQueuesDaemon); exe != "" {
		return exe
	}
	gotool, err := exec.LookPath("go")
	if err != nil {
		gotool = filepath.Join(runtime.GOROOT(), "bin", "go")
	}
	exe := filepath.Join(t.TempDir(), "downexd")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	cmd := exec.Command(gotool, "build", "-o", exe, "downex/cmd/downexd")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build downexd: %v\n%s", err, out)
	}
	return exe
}

func privatePipe(t *testing.T) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	if runtime.GOOS == "windows" {
		return `\\.\pipe\downex-queues-test-` + hex.EncodeToString(b)
	}
	dir, err := os.MkdirTemp("", "dxq-") // 0700, as ipc requires; short for sun_path
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "rpc.sock")
}

// startDaemon runs downexd on a private endpoint and database below root until the test ends
// (Shutdown, else killed), and returns its endpoint once it answers as that process.
func startDaemon(t *testing.T, root string) (endpoint string, raw *client.Client) {
	t.Helper()
	exe := daemonExe(t)
	endpoint = privatePipe(t)
	user := filepath.Join(root, "user")
	if err := os.MkdirAll(user, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "--db", filepath.Join(root, "state", "downex.db"), "--endpoint", endpoint,
		"--log-dir", filepath.Join(root, "logs"), "--no-tray")
	cmd.Dir = root
	// Anything per user that the daemon might touch stays in the test's folder.
	env := []string{}
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		switch strings.ToUpper(name) {
		case ipc.EnvEndpoint, "DOWNEX_DAEMON", "LOCALAPPDATA", "APPDATA", "XDG_STATE_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "HOME":
			continue
		}
		env = append(env, kv)
	}
	cmd.Env = append(env, ipc.EnvEndpoint+"="+endpoint, "LOCALAPPDATA="+user, "APPDATA="+user,
		"XDG_STATE_HOME="+user, "XDG_CONFIG_HOME="+user, "XDG_DATA_HOME="+user, "HOME="+user)
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stderr, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start downexd: %v", err)
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	raw = client.Default().Client(endpoint)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = raw.Daemon.Shutdown(ctx, connect.NewRequest(&downexv1.ShutdownRequest{}))
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-done
			t.Errorf("downexd did not stop after Shutdown; killed\n%s", stderr.String())
		}
	})
	deadline := time.Now().Add(30 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		st, err := raw.Daemon.Status(ctx, connect.NewRequest(&downexv1.StatusRequest{}))
		cancel()
		if err == nil {
			if int(st.Msg.GetPid()) != cmd.Process.Pid {
				t.Fatalf("the endpoint is served by pid %d, not by the started downexd %d", st.Msg.GetPid(), cmd.Process.Pid)
			}
			return endpoint, raw
		}
		select {
		case <-done:
			t.Fatalf("downexd exited before it answered: %v\n%s", err, stderr.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("downexd did not answer within 30 s: %v\n%s", err, stderr.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// liveRig is the dialog in a headless window over the real stores: host.ClientDaemon and its
// events bridge, state.Main, views.App; the calls run on goroutines as in the product.
type liveRig struct {
	t      *testing.T
	loop   *kittest.Loop
	window *kit.Window
	app    *views.App
	main   *state.Main
	// streamed: the events bridge delivered its stream's first batch (downexd's snapshot of
	// every download) to the stores.
	streamed *atomic.Bool
}

func newLiveRig(t *testing.T, endpoint string) *liveRig {
	t.Helper()
	t.Setenv(ipc.EnvEndpoint, endpoint)
	env := client.Default()
	env.Endpoint = func() (string, error) { return endpoint, nil }
	var starts atomic.Int32
	env.StartDetached = func(string, ...string) error {
		starts.Add(1)
		return errors.New("the test starts no daemon")
	}
	env.DaemonPath = func() (string, error) { return "", errors.New("no downexd for this test") }
	t.Cleanup(func() {
		if n := starts.Load(); n != 0 {
			t.Errorf("the window tried to start downexd %d times", n)
		}
	})
	daemon := host.NewClientDaemon(env)
	hub := &state.Hub{}
	ctx, cancel := context.WithCancel(context.Background())
	streamed := new(atomic.Bool)
	emit := func(msg *downexv1.WatchResponse) {
		hub.Emit(msg) // the subscribers apply it before Emit returns
		streamed.Store(true)
	}
	stopBridge := (&host.Bridge{Daemon: daemon, Emit: emit}).Start(ctx)
	call := func() (context.Context, context.CancelFunc) { return context.WithTimeout(ctx, 30*time.Second) }
	goFn := func(f func()) { go f() }
	m := state.NewMain(state.MainOptions{Daemon: daemon, Events: hub, Clock: state.SystemClock, Call: call, Go: goFn})

	clock := kittest.NewClock()
	storage := statetest.NewMemory(nil)
	theme := state.NewTheme(state.ThemeOptions{Storage: storage})
	theme.Set(host.ThemeLight)
	root := widgets.NewScope(nil)
	root.Style = kit.Style{Display: kit.DisplayBlock, Width: kit.Pct(100), Height: kit.Pct(100)}
	var app *views.App
	w := kit.NewWindow(root, kit.WindowOptions{Title: "Downex", Now: clock.Now, Scheduler: clock,
		Palette: func(e kit.Environment) kit.Palette { return app.WidgetTheme.Palette(e) }})
	light := false
	app, err := views.NewApp(views.Options{Window: w, Daemon: daemon, Main: m, Theme: theme, Density: state.NewDensity(storage),
		Storage: storage, Clock: state.SystemClock, Caps: host.Capabilities{CustomTitleBar: true, Hibernate: true}, Call: call,
		Go: goFn, SystemDark: &light, StatusBarHeight: 32})
	if err != nil {
		t.Fatal(err)
	}
	root.Theme = app.WidgetTheme
	hc := hostcaptest.NewHost(kit.Size{W: 1920, H: 1080}, kit.Environment{Scale: kit.Scale{PixelsPerDIP: 1, Text: 1}}, clock)
	loop := &kittest.Loop{Host: hc.Host, Root: w, Canvas: &kittest.Canvas{}, Clock: clock, Refresh: time.Second / 60}
	w.Attach(hc)
	r := &liveRig{t: t, loop: loop, window: w, app: app, main: m, streamed: streamed}
	t.Cleanup(func() {
		app.Dispose()
		w.Close()
		m.Dispose()
		stopBridge()
		cancel()
	})
	m.Start()
	m.Downloads.RefreshStatus()
	m.Queues.Load()
	return r
}

// liveDeadline is how long the rig waits for downexd and the window to agree: a round trip
// takes milliseconds, but under a full parallel run with the race detector (every package's
// tests and their downexd and builds at once) one took over 20 s.
const liveDeadline = 90 * time.Second

// until pumps the window until ok holds (posted answers and events arrive on other
// goroutines), for at most liveDeadline.
func (r *liveRig) until(what string, ok func() bool) {
	r.t.Helper()
	r.untilShows(what, ok, nil)
}

// untilShows is until, and on a timeout says what detail reports of the state it waited on.
func (r *liveRig) untilShows(what string, ok func() bool, detail func() string) {
	r.t.Helper()
	deadline := time.Now().Add(liveDeadline)
	for {
		r.loop.Settle(30)
		if ok() {
			return
		}
		if time.Now().After(deadline) {
			if detail != nil {
				r.t.Fatalf("timed out after %v waiting: %s (%s)", liveDeadline, what, detail())
			}
			r.t.Fatalf("timed out after %v waiting: %s", liveDeadline, what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (r *liveRig) click(e *kit.Element) {
	r.window.ScrollIntoView(e)
	r.loop.Settle(30)
	at := e.WindowBounds().Center()
	r.loop.Send(kit.PointerEvent{Action: kit.PointerMove, Pos: at})
	r.loop.Send(kit.PointerEvent{Action: kit.PointerDown, Pos: at, Button: kit.ButtonLeft, Buttons: kit.ButtonLeft, Clicks: 1})
	r.loop.Send(kit.PointerEvent{Action: kit.PointerUp, Pos: at, Button: kit.ButtonLeft})
	r.loop.Settle(30)
}

func TestReordersReachDownexd(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: builds and runs downexd")
	}
	root := t.TempDir()
	endpoint, raw := startDaemon(t, root)
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Minute)
	defer cancel()

	// A stopped queue (a new queue starts stopped) with three downloads that wait in it.
	created, err := raw.Queues.CreateQueue(ctx, connect.NewRequest(&downexv1.CreateQueueRequest{Queue: &downexv1.Queue{Name: "Night"}}))
	if err != nil {
		t.Fatalf("CreateQueue: %v", err)
	}
	qid := created.Msg.GetQueue().GetId()
	if created.Msg.GetQueue().GetRunning() {
		t.Fatal("the new queue runs")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "1024")
		_, _ = w.Write(make([]byte, 1024))
	}))
	defer srv.Close()
	out := filepath.Join(root, "out")
	if err := os.MkdirAll(out, 0o700); err != nil {
		t.Fatal(err)
	}
	names := []string{"first.bin", "second.bin", "third.bin"}
	ids := map[string]int64{}
	for _, n := range names {
		res, err := raw.Downloads.Add(ctx, connect.NewRequest(&downexv1.AddRequest{Url: srv.URL + "/" + n, Dir: out, Filename: n, QueueId: qid}))
		if err != nil {
			t.Fatalf("Add %s: %v", n, err)
		}
		ids[n] = res.Msg.GetDownload().GetId()
	}
	order := func() []int64 {
		res, err := raw.Queues.ListQueues(ctx, connect.NewRequest(&downexv1.ListQueuesRequest{}))
		if err != nil {
			t.Fatalf("ListQueues: %v", err)
		}
		for _, q := range res.Msg.GetQueues() {
			if q.GetId() == qid {
				return q.GetDownloadIds()
			}
		}
		t.Fatal("the queue is gone")
		return nil
	}
	want := func(ns ...string) []int64 {
		var out []int64
		for _, n := range ns {
			out = append(out, ids[n])
		}
		return out
	}
	if got := order(); !slices.Equal(got, want(names...)) {
		t.Fatalf("downexd's order %v, want %v", got, want(names...))
	}

	r := newLiveRig(t, endpoint)
	r.until("the queues and downloads are read", func() bool {
		q, ok := r.main.Queues.Get(qid)
		if !ok || len(q.GetDownloadIds()) != 3 {
			return false
		}
		for _, id := range ids {
			if _, ok := r.main.Downloads.Get(id); !ok {
				return false
			}
		}
		return true
	})
	// The stream's snapshot is the downloads as downexd had them when the window subscribed:
	// applied after a move (a subscription slow under a full parallel run), it would put back
	// the queue positions the move changed, which carry the same updated_at. The moves wait
	// for it, as a person's first move comes after the window has its events.
	r.until("the events bridge delivered downexd's snapshot", r.streamed.Load)
	d := New(r.app, views.QueuesRequest{Queue: qid})
	r.app.Dialogs.Show(d)
	r.until("the order lists the three downloads", func() bool { return len(d.order.ids) == 3 })
	if r.app.Stale() {
		t.Fatal("the window thinks downexd is unreachable")
	}
	rowNames := func() []string {
		var out []string
		for _, id := range d.order.ids {
			out = append(out, d.order.rows[id].name.Text())
		}
		return out
	}
	if got := rowNames(); !slices.Equal(got, names) {
		t.Fatalf("rows %q", got)
	}
	// The window's "Queued #n" of every download is downexd's queue_position: a move changes
	// the positions of the downloads it passes too, though downexd publishes an update only
	// for the one that moved.
	positions := func(ns ...string) []int32 {
		var out []int32
		for _, n := range ns {
			res, err := raw.Downloads.Get(ctx, connect.NewRequest(&downexv1.GetRequest{Id: ids[n]}))
			if err != nil {
				t.Fatalf("Get %s: %v", n, err)
			}
			out = append(out, res.Msg.GetDownload().GetQueuePosition())
		}
		return out
	}
	shown := func(ns ...string) []int32 {
		var out []int32
		for _, n := range ns {
			d, _ := r.main.Downloads.Get(ids[n])
			out = append(out, d.GetQueuePosition())
		}
		return out
	}
	samePositions := func(what string) {
		t.Helper()
		var want, got []int32
		r.untilShows(what, func() bool {
			want, got = positions(names...), shown(names...)
			return slices.Equal(got, want)
		}, func() string { return fmt.Sprintf("the window shows %v, downexd %v", got, want) })
		t.Logf("%s: %v", what, got)
	}
	samePositions("the window starts with downexd's positions")

	// The Up arrow of the second row: MoveDownloads([second], Night, 0).
	r.click(&d.order.rows[ids["second.bin"]].up.Element)
	r.until("downexd puts second.bin first", func() bool { return slices.Equal(order(), want("second.bin", "first.bin", "third.bin")) })
	r.until("the dialog shows downexd's order", func() bool {
		return slices.Equal(rowNames(), []string{"second.bin", "first.bin", "third.bin"}) && !d.locked()
	})
	samePositions("the window shows downexd's positions after the move")

	// Drag third.bin onto the upper half of the first row.
	from := d.order.rows[ids["third.bin"]]
	to := d.order.rows[ids["second.bin"]]
	r.window.ScrollIntoView(&d.order.list.Element)
	r.loop.Settle(30)
	at := from.name.WindowBounds().Center()
	b := to.WindowBounds()
	over := kit.Pt(b.Min.X+40, b.Min.Y+b.Height()*0.2)
	r.loop.Send(kit.PointerEvent{Action: kit.PointerMove, Pos: at})
	r.loop.Send(kit.PointerEvent{Action: kit.PointerDown, Pos: at, Button: kit.ButtonLeft, Buttons: kit.ButtonLeft, Clicks: 1})
	r.loop.Send(kit.PointerEvent{Action: kit.PointerMove, Pos: kit.Pt(at.X, (at.Y+over.Y)/2), Buttons: kit.ButtonLeft})
	r.loop.Send(kit.PointerEvent{Action: kit.PointerMove, Pos: over, Buttons: kit.ButtonLeft})
	r.loop.Settle(30)
	if d.order.list.dropAt != ids["second.bin"] {
		t.Fatalf("drop mark at %d, want before second.bin", d.order.list.dropAt)
	}
	r.loop.Send(kit.PointerEvent{Action: kit.PointerUp, Pos: over, Button: kit.ButtonLeft})
	r.until("downexd puts third.bin first", func() bool { return slices.Equal(order(), want("third.bin", "second.bin", "first.bin")) })
	r.until("the dialog shows downexd's order", func() bool {
		return slices.Equal(rowNames(), []string{"third.bin", "second.bin", "first.bin"}) && !d.locked()
	})
	samePositions("the window shows downexd's positions after the move")

	// The Down arrow of the last-but-one row puts it at the end.
	r.click(&d.order.rows[ids["second.bin"]].down.Element)
	r.until("downexd puts second.bin last", func() bool { return slices.Equal(order(), want("third.bin", "first.bin", "second.bin")) })
	r.until("the dialog shows downexd's order", func() bool {
		return slices.Equal(rowNames(), []string{"third.bin", "first.bin", "second.bin"})
	})
	samePositions("the window shows downexd's positions after the move")
	if d.failure != "" {
		t.Fatalf("refusal %q", d.failure)
	}
	t.Logf("downexd's order of queue %d after the arrows and a drag: %v", qid, order())
}
