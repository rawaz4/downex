package host

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"downex/internal/client"
	"downex/internal/ipc"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/testserver"
)

// The host against a real downexd: one built from this tree and run with its own
// --endpoint, --db and --log-dir (a private instance: no tray icon, no sign-in entry, no
// windows), never the user's own. Set DOWNEX_HOST_TEST_DOWNEXD to a downexd executable to
// skip the build. -short skips the test.

const envTestDaemon = "DOWNEX_HOST_TEST_DOWNEXD"

// testDaemonExe returns the downexd to run: $DOWNEX_HOST_TEST_DOWNEXD, else one built into
// the test's temporary folder (pure Go, like the product).
func testDaemonExe(t *testing.T) string {
	t.Helper()
	if exe := os.Getenv(envTestDaemon); exe != "" {
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

// privateEndpoint is a pipe with a random name on Windows, a socket in a new short folder
// elsewhere (sun_path is limited).
func privateEndpoint(t *testing.T) string {
	t.Helper()
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	if runtime.GOOS == "windows" {
		return `\\.\pipe\downex-host-test-` + hex.EncodeToString(b)
	}
	dir, err := os.MkdirTemp("", "dxh-") // mode 0700, as ipc requires
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "rpc.sock")
}

// childEnv is this process's environment with overrides (names compared without case on
// Windows).
func childEnv(overrides map[string]string) []string {
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		keep := true
		for k := range overrides {
			if name == k || runtime.GOOS == "windows" && strings.EqualFold(name, k) {
				keep = false
			}
		}
		if keep {
			env = append(env, kv)
		}
	}
	for k, v := range overrides {
		env = append(env, k+"="+v)
	}
	return env
}

type testDaemon struct {
	cmd            *exec.Cmd
	done           chan struct{}
	stdout, stderr bytes.Buffer
	db, endpoint   string
}

// startTestDaemon starts downexd on a private endpoint and database below root and waits
// until it answers; it is shut down (else killed) when the test ends.
func startTestDaemon(t *testing.T, root string) *testDaemon {
	t.Helper()
	exe := testDaemonExe(t)
	p := &testDaemon{done: make(chan struct{}), db: filepath.Join(root, "state", "downex.db"), endpoint: privateEndpoint(t)}
	user := filepath.Join(root, "user")
	if err := os.MkdirAll(user, 0o700); err != nil {
		t.Fatal(err)
	}
	p.cmd = exec.Command(exe, "--db", p.db, "--endpoint", p.endpoint, "--log-dir", filepath.Join(root, "logs"),
		"--log-level", "debug", "--no-tray")
	p.cmd.Dir = root
	// Anything per user that the daemon might touch stays in the test.
	p.cmd.Env = childEnv(map[string]string{
		ipc.EnvEndpoint: p.endpoint, "DOWNEX_DAEMON": "",
		"LOCALAPPDATA": user, "APPDATA": user, "XDG_STATE_HOME": user, "XDG_CONFIG_HOME": user, "XDG_DATA_HOME": user,
		"HOME": user, // macOS keeps them below ~/Library
	})
	p.cmd.Stdout, p.cmd.Stderr = &p.stdout, &p.stderr
	if err := p.cmd.Start(); err != nil {
		t.Fatalf("start downexd: %v", err)
	}
	go func() {
		_ = p.cmd.Wait()
		close(p.done)
	}()
	raw := client.Default().Client(p.endpoint)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = raw.Daemon.Shutdown(ctx, connect.NewRequest(&downexv1.ShutdownRequest{}))
		select {
		case <-p.done:
		case <-time.After(10 * time.Second):
			_ = p.cmd.Process.Kill()
			<-p.done
			t.Errorf("downexd did not stop after Shutdown; killed\n%s", p.stderr.String())
		}
	})
	deadline := time.Now().Add(30 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		st, err := raw.Daemon.Status(ctx, connect.NewRequest(&downexv1.StatusRequest{}))
		cancel()
		if err == nil {
			if int(st.Msg.GetPid()) != p.cmd.Process.Pid {
				t.Fatalf("the endpoint is served by pid %d, not by the started downexd %d", st.Msg.GetPid(), p.cmd.Process.Pid)
			}
			return p
		}
		select {
		case <-p.done:
			t.Fatalf("downexd exited before it answered: %v\nstderr:\n%s", err, p.stderr.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("downexd did not answer within 30 s: %v\nstderr:\n%s", err, p.stderr.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// waitEvent reads batches until one has an event that match accepts, for at most 30 s.
func waitEvent(t *testing.T, ch <-chan *downexv1.WatchResponse, what string, match func(*downexv1.Event) bool) *downexv1.Event {
	t.Helper()
	timeout := time.After(30 * time.Second)
	for {
		select {
		case m := <-ch:
			for _, e := range m.GetEvents() {
				if match(e) {
					return e
				}
			}
		case <-timeout:
			t.Fatalf("no event: %s", what)
			return nil
		}
	}
}

// downloadIs matches a snapshot or update of download id in state.
func downloadIs(id int64, state downexv1.DownloadState, also func(*downexv1.Download) bool) func(*downexv1.Event) bool {
	return func(e *downexv1.Event) bool {
		d := e.GetUpdated()
		if d == nil {
			d = e.GetSnapshot()
		}
		return d != nil && d.GetId() == id && d.GetState() == state && (also == nil || also(d))
	}
}

func removedEvent(id int64) func(*downexv1.Event) bool {
	return func(e *downexv1.Event) bool {
		_, ok := e.GetKind().(*downexv1.Event_Removed)
		return ok && e.GetRemoved() == id
	}
}

// hideField moves field name of m into m's unknown fields, as a build that does not know
// it holds it: the value travels only as the bytes the daemon sent.
func hideField(t *testing.T, m proto.Message, name protoreflect.Name) {
	t.Helper()
	r := m.ProtoReflect()
	fd := r.Descriptor().Fields().ByName(name)
	if fd == nil {
		t.Fatalf("no field %s", name)
	}
	one := r.New()
	one.Set(fd, r.Get(fd))
	b, err := proto.Marshal(one.Interface())
	if err != nil || len(b) == 0 {
		t.Fatalf("encode %s: %v (%d bytes: a default value has no bytes to hide)", name, err, len(b))
	}
	r.Clear(fd)
	r.SetUnknown(append(r.GetUnknown(), b...))
}

func TestHostWithRealDaemon(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: builds and runs downexd")
	}
	root := t.TempDir()
	daemon := startTestDaemon(t, root)
	out := filepath.Join(root, "out")
	if err := os.MkdirAll(out, 0o700); err != nil {
		t.Fatal(err)
	}

	// The window finds the daemon through $DOWNEX_ENDPOINT (ipc.Endpoint), and this test
	// never lets it start one.
	t.Setenv(ipc.EnvEndpoint, daemon.endpoint)
	env := client.Default()
	var starts atomic.Int32
	env.StartDetached = func(string, ...string) error {
		starts.Add(1)
		return errors.New("the test starts no daemon")
	}
	env.DaemonPath = func() (string, error) { return "", errors.New("no downexd for this test") }
	d := NewClientDaemon(env)
	t.Cleanup(func() {
		if n := starts.Load(); n != 0 {
			t.Errorf("the host tried to start downexd %d times", n)
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()

	sh := &fakeShell{}
	main := NewSession(SessionOptions{Daemon: d, Shell: sh, ThemePath: filepath.Join(root, "theme", ThemeFileName), Version: "it"})
	defer main.Close()
	mr := &recorder{emitted: make(chan *downexv1.WatchResponse, 1024)}
	main.Start(mr.emit)

	// The first batch is the snapshot: no downloads yet, and the main queue.
	first := nextBatch(t, mr.emitted)
	kinds := eventKinds(t, first)
	if !slices.Contains(kinds, "queue_updated:1") || slices.ContainsFunc(kinds, func(k string) bool { return strings.HasPrefix(k, "snapshot:") }) {
		t.Fatalf("first batch = %v, want the main queue and no downloads", kinds)
	}

	// Add, paused: a slow server, so that it is still running when paused.
	srv := testserver.New(testserver.Options{Size: 8 << 20, Throttle: 256 << 10})
	defer srv.Close()
	add, err := d.Add(ctx, &downexv1.AddRequest{Url: srv.URL + "/sample.bin", Dir: out, Filename: "sample.bin",
		StartPaused: true, MaxConnections: 1})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	id := add.GetDownload().GetId()
	if add.GetDownload().GetState() != downexv1.DownloadState_DOWNLOAD_STATE_PAUSED {
		t.Fatalf("added %v, want paused", add.GetDownload())
	}
	waitEvent(t, mr.emitted, "the added download, paused", downloadIs(id, downexv1.DownloadState_DOWNLOAD_STATE_PAUSED, nil))

	// A progress window watches that download only.
	ps := NewSession(SessionOptions{Args: []string{"--progress", strconv.FormatInt(id, 10)}, Daemon: d, Shell: &fakeShell{},
		ThemePath: filepath.Join(root, "theme", ThemeFileName)})
	defer ps.Close()
	pr := &recorder{emitted: make(chan *downexv1.WatchResponse, 1024)}
	ps.Start(pr.emit)
	if got := eventKinds(t, nextBatch(t, pr.emitted)); !slices.Equal(got, []string{"snapshot:" + strconv.FormatInt(id, 10)}) {
		t.Fatalf("progress window's first batch = %v", got)
	}
	// One for a download that does not exist reports it removed (not_found).
	us := NewSession(SessionOptions{Args: []string{"--progress", "987654"}, Daemon: d, Shell: &fakeShell{},
		ThemePath: filepath.Join(root, "theme", ThemeFileName)})
	ur := &recorder{emitted: make(chan *downexv1.WatchResponse, 1024)}
	us.Start(ur.emit)
	if got := eventKinds(t, nextBatch(t, ur.emitted)); !slices.Equal(got, []string{"removed:987654"}) {
		t.Errorf("unknown download's first batch = %v", got)
	}
	us.Close()

	// Resume, then pause while it downloads.
	if _, err := d.Resume(ctx, &downexv1.ResumeRequest{Id: id}); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	waitEvent(t, mr.emitted, "the download receiving bytes", downloadIs(id, downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING,
		func(dl *downexv1.Download) bool { return dl.GetReceived() > 0 }))
	waitEvent(t, pr.emitted, "the progress window seeing it download", downloadIs(id, downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING, nil))
	if _, err := d.Pause(ctx, &downexv1.PauseRequest{Id: id}); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	waitEvent(t, mr.emitted, "the download paused", downloadIs(id, downexv1.DownloadState_DOWNLOAD_STATE_PAUSED, nil))
	got, err := d.Get(ctx, id)
	if err != nil || got.GetState() != downexv1.DownloadState_DOWNLOAD_STATE_PAUSED || got.GetReceived() <= 0 || got.GetReceived() >= 8<<20 {
		t.Fatalf("Get after pause = %v, %v", got, err)
	}

	// Its unfinished file is shown in the file manager; there is no finished file yet.
	if err := main.Files().OpenFolder(ctx, id); err != nil {
		t.Fatalf("OpenFolder: %v", err)
	}
	if want := []string{"reveal:" + filepath.Join(out, "sample.bin"+PartSuffix)}; !slices.Equal(sh.shown, want) {
		t.Errorf("shown %q, want %q", sh.shown, want)
	}
	if exists, err := main.Files().FileExists(ctx, id); err != nil || exists {
		t.Errorf("FileExists of a paused download = %v, %v", exists, err)
	}
	if err := main.Files().OpenFile(ctx, id); !hasPrefix(err, fmt.Sprintf("failed_precondition: download %d is not completed (paused)", id)) {
		t.Errorf("OpenFile of a paused download = %v", err)
	}

	// Remove it with its file: both windows see it go.
	if _, err := d.Remove(ctx, &downexv1.RemoveRequest{Id: id, DeleteFile: true}); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	waitEvent(t, mr.emitted, "the main window seeing the removal", removedEvent(id))
	waitEvent(t, pr.emitted, "the progress window seeing the removal", removedEvent(id))
	if _, err := d.Get(ctx, id); !hasPrefix(err, "not_found: ") {
		t.Errorf("Get after Remove = %v", err)
	}

	// Settings: a field this build would not know survives, both ways of saving.
	before, err := d.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !before.GetSettings().GetShowCompleteDialog() {
		t.Fatal("a fresh daemon's show_complete_dialog is off; the test needs a field whose value has bytes")
	}
	n := before.GetSettings().GetMaxConcurrentDownloads()%4 + 1 // 1..4, never the current value
	saved, err := EditSettings(ctx, d, func(s *downexv1.Settings) error {
		hideField(t, s, "show_complete_dialog")
		s.MaxConcurrentDownloads = n
		return nil
	})
	if err != nil {
		t.Fatalf("EditSettings: %v", err)
	}
	if saved.GetMaxConcurrentDownloads() != n || !saved.GetShowCompleteDialog() {
		t.Errorf("EditSettings stored %d at once, complete dialog %v; want %d and on", saved.GetMaxConcurrentDownloads(), saved.GetShowCompleteDialog(), n)
	}
	// The control: dropping the field instead turns it off, so the check above can tell.
	if s, err := EditSettings(ctx, d, func(s *downexv1.Settings) error {
		s.ProtoReflect().Clear(s.ProtoReflect().Descriptor().Fields().ByName("show_complete_dialog"))
		return nil
	}); err != nil || s.GetShowCompleteDialog() {
		t.Fatalf("control: %v, %v (complete dialog should be off)", s, err)
	}
	if _, err := EditSettings(ctx, d, func(s *downexv1.Settings) error { s.ShowCompleteDialog = true; return nil }); err != nil {
		t.Fatal(err)
	}
	// A draft held for a while (the Settings dialog), saved with SaveSettings.
	cur, err := d.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	draft := proto.CloneOf(cur.GetSettings())
	hideField(t, draft, "show_complete_dialog")
	hideField(t, draft, "show_progress_window")
	draft.MaxConcurrentDownloads = n%4 + 1
	saved, err = SaveSettings(ctx, d, draft)
	if err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	after, err := d.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(saved, after.GetSettings()) || after.GetSettings().GetMaxConcurrentDownloads() != n%4+1 ||
		!after.GetSettings().GetShowCompleteDialog() || !after.GetSettings().GetShowProgressWindow() {
		t.Errorf("after SaveSettings: %v", after.GetSettings())
	}
	// Everything else came back as it was.
	want := proto.CloneOf(cur.GetSettings())
	want.MaxConcurrentDownloads = n%4 + 1
	if !proto.Equal(after.GetSettings(), want) {
		t.Errorf("SaveSettings changed more than one field:\n%v\nwant\n%v", after.GetSettings(), want)
	}

	// Downex's own folders come from the daemon's Status: this daemon's, not the user's.
	if dir, err := main.Files().AppFolder(ctx, FolderData); err != nil || dir != filepath.Dir(daemon.db) {
		t.Errorf("AppFolder(data) = %q, %v; want %q", dir, err, filepath.Dir(daemon.db))
	}
	if dir, err := main.Files().AppFolder(ctx, FolderLogs); err != nil || dir != filepath.Join(root, "logs") {
		t.Errorf("AppFolder(logs) = %q, %v", dir, err)
	}

	// Diagnostics name the daemon's version and never its endpoint.
	text := main.Diagnostics(ctx, "")
	st, err := d.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "downexd "+st.GetVersion()+", running since") || strings.Contains(text, daemon.endpoint) ||
		!strings.Contains(text, "Downloads: 0 (0 videos or streams)") {
		t.Errorf("diagnostics:\n%s", text)
	}

	// Queues and tools reach the real services.
	if q, err := d.ListQueues(ctx); err != nil || len(q.GetQueues()) == 0 || !q.GetQueues()[0].GetIsMain() {
		t.Errorf("ListQueues = %v, %v", q, err)
	}
	if _, err := d.GetTools(ctx); err != nil {
		t.Errorf("GetTools = %v", err)
	}
}
