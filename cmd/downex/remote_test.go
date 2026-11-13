package main

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"downex/internal/engine"
	"downex/internal/ipc"
	downexv1 "downex/internal/rpc/downex/v1"
)

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func TestAddSendsRequestAndPrintsID(t *testing.T) {
	f, h := newFakeDaemon(t)
	ctx := context.Background()
	dir := t.TempDir()

	r := h.run(ctx, "add", "https://example.test/f.iso", "-o", dir, "-c", "4", "--limit", "1M",
		"-H", "Referer: https://r.test/", "-H", "Cookie: s=1", "--name", "x.iso", "--paused")
	if r.code != exitOK || r.stdout != "7\n" || !strings.Contains(r.stderr, "Download 7 paused: https://example.test/f.iso") {
		t.Fatalf("add: %v", r)
	}
	got := f.rec().adds[0]
	if got.GetUrl() != "https://example.test/f.iso" || got.GetDir() != dir || got.GetFilename() != "x.iso" ||
		got.GetMaxConnections() != 4 || got.GetSpeedLimit() != 1_000_000 || !got.GetStartPaused() || got.GetMethod() != "" ||
		got.GetSource() != downexv1.AddSource_ADD_SOURCE_CLI {
		t.Errorf("AddRequest = %v", got)
	}
	var hdrs []string
	for _, hd := range got.GetHeaders() {
		hdrs = append(hdrs, hd.GetName()+"="+hd.GetValue())
	}
	if want := []string{"Cookie=s=1", "Referer=https://r.test/"}; !slices.Equal(hdrs, want) {
		t.Errorf("headers %q, want %q (sorted)", hdrs, want)
	}

	// -o is relative to the CLI's directory, so it is sent absolute; no -o leaves the
	// choice to the daemon.
	if r = h.run(ctx, "add", "https://example.test/g", "-o", "rel"); r.code != exitOK {
		t.Fatalf("add -o rel: %v", r)
	}
	if got := f.rec().adds[1].GetDir(); got != mustAbs(t, "rel") {
		t.Errorf("relative -o sent as %q", got)
	}
	if r = h.run(ctx, "add", "https://example.test/h"); r.code != exitOK || !strings.Contains(r.stderr, "queued, position 1 in the queue") {
		t.Fatalf("add: %v", r)
	}
	if a := f.rec().adds[2]; a.GetDir() != "" || a.GetStartPaused() || a.GetSpeedLimit() != 0 {
		t.Errorf("defaults sent as %v", a)
	}
}

func TestAddArgumentErrors(t *testing.T) {
	f, h := newFakeDaemon(t)
	ctx := context.Background()
	for _, tc := range []struct {
		args []string
		out  string
	}{
		{[]string{"add"}, "expects exactly one URL"},
		{[]string{"add", "u1", "u2"}, "expects exactly one URL"},
		{[]string{"add", "https://x.test/", "-c", "33"}, "-c must be between 1 and 32"},
		{[]string{"add", "https://x.test/", "-c", "-1"}, "-c must be between"},
		{[]string{"add", "https://x.test/", "--limit", "fast"}, "invalid rate"},
		{[]string{"add", "https://x.test/", "-H", "bad"}, `want "Name: value"`},
		{[]string{"add", "https://x.test/", "-H", "N: a\r\nX: 1"}, "control characters"},
		{[]string{"add", "https://x.test/", "--paused", "--wait"}, "cannot be combined"},
		{[]string{"add", "https://x.test/", "--nope"}, "flag provided but not defined"},
	} {
		r := h.run(ctx, tc.args...)
		if r.code != exitError || !strings.Contains(r.stdout+r.stderr, tc.out) {
			t.Errorf("downex %q: %v\nwant exit 1 and %q", tc.args, r, tc.out)
		}
	}
	if dials, _ := h.recorded(); len(dials) != 0 || len(f.rec().adds) != 0 {
		t.Errorf("bad arguments reached the daemon: %d dials, %d adds", len(dials), len(f.rec().adds))
	}

	// Validation by the daemon is reported with its message.
	f.do(func() {
		f.addErr = connect.NewError(connect.CodeInvalidArgument, errors.New(`unsupported URL scheme "ftp"`))
	})
	r := h.run(ctx, "add", "ftp://x.test/f")
	if r.code != exitError || !strings.Contains(r.stderr, `downex: error: add: unsupported URL scheme "ftp"`) || r.stdout != "" {
		t.Errorf("rejected add: %v", r)
	}
}

func TestAutostartWhenNotRunning(t *testing.T) {
	_, h := newFakeDaemon(t)
	h.up, h.readyAfter = false, 3 // the daemon needs a few polls to come up
	db := filepath.Join("data", "test.db")

	r := h.run(context.Background(), "--endpoint", "ep-1", "--db", db, "add", "https://example.test/a")
	if r.code != exitOK || r.stdout != "7\n" || !strings.Contains(r.stderr, "Started downexd (pid 4242).") {
		t.Fatalf("add with autostart: %v", r)
	}
	dials, spawns := h.recorded()
	want := []string{fakeDaemonPath, "--db", mustAbs(t, db), "--endpoint", "ep-1"}
	if len(spawns) != 1 || !slices.Equal(spawns[0], want) {
		t.Errorf("spawned %q, want once %q", spawns, want)
	}
	if len(dials) < 4 {
		t.Errorf("%d dials, want the probe plus polls until the daemon came up", len(dials))
	}
	for _, ep := range dials {
		if ep != "ep-1" {
			t.Errorf("dialled %q, want the --endpoint value", ep)
		}
	}

	// Without flags the daemon gets none (it uses the same defaults), and a running
	// daemon is not started again.
	_, h = newFakeDaemon(t)
	h.up, h.readyAfter = false, 0
	if r = h.run(context.Background(), "pause", "1"); !strings.Contains(r.stderr, "Started downexd") {
		t.Fatalf("pause with autostart: %v", r)
	}
	if r = h.run(context.Background(), "ls"); r.code != exitOK {
		t.Fatalf("ls: %v", r)
	}
	if _, spawns = h.recorded(); len(spawns) != 1 || !slices.Equal(spawns[0], []string{fakeDaemonPath}) {
		t.Errorf("spawns %q, want one without flags", spawns)
	}
}

// TestNoAutostartWhenElevated (SEC-2): a command run from an elevated (administrator)
// prompt never starts downexd, which would inherit those rights for every program of
// the user; a daemon that already runs is used as usual.
func TestNoAutostartWhenElevated(t *testing.T) {
	ctx := context.Background()
	_, h := newFakeDaemon(t)
	h.up, h.elevated = false, true
	for _, args := range [][]string{{"add", "https://example.test/a"}, {"daemon", "start"}} {
		r := h.run(ctx, args...)
		if r.code != exitError || !strings.Contains(r.stderr, "elevated") {
			t.Errorf("%q from an elevated prompt: %v", args, r)
		}
	}
	if _, spawns := h.recorded(); len(spawns) != 0 {
		t.Fatalf("spawned %q from an elevated process", spawns)
	}
	h.setUp(true)
	if r := h.run(ctx, "add", "https://example.test/b"); r.code != exitOK {
		t.Errorf("add to a running daemon from an elevated prompt: %v", r)
	}
}

func TestAutostartFailures(t *testing.T) {
	ctx := context.Background()

	_, h := newFakeDaemon(t)
	h.up, h.readyAfter, h.startTimeout = false, -1, 150*time.Millisecond
	start := time.Now()
	r := h.run(ctx, "add", "https://example.test/a")
	if r.code != exitError || !strings.Contains(r.stderr, "downexd did not answer within 150ms") ||
		!strings.Contains(r.stderr, "see its log in /fake/logs") || !strings.Contains(r.stderr, fakeDaemonPath) {
		t.Errorf("daemon that never comes up: %v", r)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Errorf("gave up after %v, want about the start timeout", el)
	}

	_, h = newFakeDaemon(t)
	h.up, h.daemonErr = false, errors.New("downexd is not installed next to downex")
	if r = h.run(ctx, "watch"); r.code != exitError || !strings.Contains(r.stderr, "cannot start downexd: downexd is not installed") {
		t.Errorf("missing daemon executable: %v", r)
	}
	if _, spawns := h.recorded(); len(spawns) != 0 {
		t.Errorf("spawned %q without an executable", spawns)
	}

	_, h = newFakeDaemon(t)
	h.up, h.spawnErr = false, errors.New("exec format error")
	if r = h.run(ctx, "settings"); r.code != exitError || !strings.Contains(r.stderr, "cannot start downexd ("+fakeDaemonPath+"): exec format error") {
		t.Errorf("spawn failure: %v", r)
	}

	// Only "not running" starts a daemon: other failures (here a pipe owned by another
	// user) are reported.
	_, h = newFakeDaemon(t)
	h.dialErr = fs.ErrPermission
	if r = h.run(ctx, "pause", "1"); r.code != exitError || !strings.Contains(r.stderr, "connect to downexd at fake-endpoint") {
		t.Errorf("dial failure: %v", r)
	}
	if _, spawns := h.recorded(); len(spawns) != 0 {
		t.Errorf("spawned %q after a permission error", spawns)
	}

	// Ctrl+C while waiting for the daemon is an interruption.
	_, h = newFakeDaemon(t)
	h.up, h.readyAfter, h.startTimeout = false, -1, 5*time.Second
	cctx, cancel := context.WithCancel(ctx)
	run := h.start(cctx, "add", "https://example.test/a")
	waitFor(t, "the spawn", func() bool { _, s := h.recorded(); return len(s) == 1 })
	cancel()
	if code := run.wait(t); code != exitInterrupted {
		t.Errorf("interrupted autostart exit %d, stderr %s", code, run.stderr)
	}
}

func TestLsPrefersRunningDaemon(t *testing.T) {
	f, h := newFakeDaemon(t)
	dir := t.TempDir()
	f.do(func() {
		f.downloads[3] = &downexv1.Download{Id: 3, Url: "https://example.test/big.iso", Dir: dir, Filename: "big.iso",
			Size: 200 << 20, Received: 100 << 20, State: downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING,
			Mode: downexv1.Mode_MODE_RANGED, SpeedBps: 5 << 20, Connections: 8, MaxConnections: 8,
			CreatedAt: timestamppb.New(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))}
		f.downloads[4] = &downexv1.Download{Id: 4, Url: "https://example.test/next", Dir: dir, Size: -1,
			State: downexv1.DownloadState_DOWNLOAD_STATE_QUEUED, QueuePosition: 1}
		f.downloads[5] = &downexv1.Download{Id: 5, Url: "https://example.test/gone", Dir: dir, Filename: "gone.bin", Size: 10,
			State: downexv1.DownloadState_DOWNLOAD_STATE_FAILED, Error: "HTTP 404 Not Found", ErrorCode: "http_status"}
	})
	db := filepath.Join(t.TempDir(), "never-created.db")
	ctx := context.Background()

	r := h.run(ctx, "ls", "--db", db)
	if r.code != exitOK {
		t.Fatalf("ls: %v", r)
	}
	lines := strings.Split(strings.TrimSpace(r.stdout), "\n")
	if len(lines) != 4 || !strings.HasPrefix(lines[0], "ID") ||
		!strings.Contains(lines[1], "downloading") || !strings.Contains(lines[1], "50.0%") || !strings.Contains(lines[1], "big.iso") ||
		!strings.Contains(lines[2], "queued") || !strings.Contains(lines[2], "https://example.test/next") ||
		!strings.Contains(lines[3], "failed") {
		t.Errorf("ls table:\n%s", r.stdout)
	}
	if _, err := os.Stat(db); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ls opened the database although the daemon is running: %v", err)
	}

	r = h.run(ctx, "ls", "--json")
	var views []downloadView
	if err := json.Unmarshal([]byte(r.stdout), &views); err != nil || len(views) != 3 {
		t.Fatalf("ls --json: %v\n%v", err, r)
	}
	v := views[0]
	if v.ID != 3 || v.State != "downloading" || v.Mode != "ranged" || v.Downloaded != 100<<20 || v.Size != 200<<20 ||
		v.SpeedBps != 5<<20 || v.Connections != 8 || v.Path != engine.PartPath(dir, "big.iso") ||
		!v.CreatedAt.Equal(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)) || v.CompletedAt != nil {
		t.Errorf("downloading entry: %+v", v)
	}
	if v := views[1]; v.QueuePosition != 1 || v.Path != "" || v.Size != -1 {
		t.Errorf("queued entry: %+v", v)
	}
	if v := views[2]; v.ErrorCode != "http_status" || v.Error != "HTTP 404 Not Found" {
		t.Errorf("failed entry: %+v", v)
	}
	if strings.Contains(r.stdout, `"validators"`) {
		t.Errorf("daemon entries have no validators, but they are printed:\n%s", r.stdout)
	}
	if _, spawns := h.recorded(); len(spawns) != 0 {
		t.Errorf("ls started a daemon: %q", spawns)
	}
}

func TestLsFallsBackToDatabase(t *testing.T) {
	_, h := newFakeDaemon(t)
	h.up = false
	db := filepath.Join(t.TempDir(), "x.db")
	r := h.run(context.Background(), "--db", db, "ls")
	if r.code != exitOK || !strings.Contains(r.stdout, "No downloads.") || r.stderr != "" {
		t.Fatalf("ls without a daemon: %v", r)
	}
	if _, err := os.Stat(db); err != nil {
		t.Errorf("ls did not read the --db database: %v", err)
	}
	if _, spawns := h.recorded(); len(spawns) != 0 {
		t.Errorf("ls started a daemon: %q", spawns)
	}

	// A daemon that cannot be reached for another reason is reported, then the
	// database is read.
	h.dialErr = fs.ErrPermission
	r = h.run(context.Background(), "ls", "--json", "--db", db)
	if r.code != exitOK || strings.TrimSpace(r.stdout) != "[]" || !strings.Contains(r.stderr, "warning:") {
		t.Errorf("ls with an unreachable daemon: %v", r)
	}
}

func TestPauseResumeRemove(t *testing.T) {
	f, h := newFakeDaemon(t)
	dir := t.TempDir()
	f.do(func() {
		f.downloads[3] = &downexv1.Download{Id: 3, Url: "https://example.test/a", Dir: dir, Filename: "a.bin",
			Size: 100 << 10, Received: 50 << 10, State: downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING}
	})
	ctx := context.Background()

	r := h.run(ctx, "pause", "3")
	if r.code != exitOK || !strings.Contains(r.stdout, "Paused download 3 with 51.2 KB of 102.4 KB (50.0%) saved.") ||
		!strings.Contains(r.stdout, "Resume with: downex resume 3") {
		t.Errorf("pause: %v", r)
	}
	if p := f.rec().pauses; !slices.Equal(p, []int64{3}) {
		t.Errorf("Pause requests %v", p)
	}

	r = h.run(ctx, "resume", "3")
	if r.code != exitOK || !strings.Contains(r.stdout, "Resumed download 3 (queued, position 2 in the queue).") {
		t.Errorf("resume: %v", r)
	}
	if q := f.rec().resumes[0]; q.GetId() != 3 || q.GetRestart() || q.MaxConnections != nil || q.SpeedLimit != nil {
		t.Errorf("plain resume sent %v", q)
	}
	if r = h.run(ctx, "resume", "--restart", "3", "-c", "4", "--limit", "0"); r.code != exitOK {
		t.Fatalf("resume with flags: %v", r)
	}
	if q := f.rec().resumes[1]; !q.GetRestart() || q.MaxConnections == nil || *q.MaxConnections != 4 || q.SpeedLimit == nil || *q.SpeedLimit != 0 {
		t.Errorf("resume --restart -c 4 --limit 0 sent %v", q)
	}

	// rm keeps the file unless --delete-file is given, as without a daemon.
	if err := os.WriteFile(engine.PartPath(dir, "a.bin"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	r = h.run(ctx, "rm", "3")
	if r.code != exitOK || !strings.Contains(r.stdout, "Removed download 3; kept "+engine.PartPath(dir, "a.bin")) {
		t.Errorf("rm: %v", r)
	}
	f.do(func() {
		f.downloads[4] = &downexv1.Download{Id: 4, Dir: dir, Filename: "b.bin", State: downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED}
	})
	r = h.run(ctx, "rm", "4", "--delete-file")
	if r.code != exitOK || !strings.Contains(r.stdout, "Removed download 4 and deleted "+filepath.Join(dir, "b.bin")) {
		t.Errorf("rm --delete-file: %v", r)
	}
	if rm := f.rec().removes; len(rm) != 2 || rm[0].GetDeleteFile() || !rm[1].GetDeleteFile() || rm[1].GetId() != 4 {
		t.Errorf("Remove requests %v", rm)
	}

	for _, args := range [][]string{{"pause", "9"}, {"resume", "9"}, {"rm", "9"}} {
		if r := h.run(ctx, args...); r.code != exitError || !strings.Contains(r.stderr, `no download with id 9 (see "downex ls")`) {
			t.Errorf("%q of an unknown id: %v", args, r)
		}
	}
	for _, tc := range []struct {
		args []string
		out  string
	}{
		{[]string{"pause"}, "expects exactly one download id"},
		{[]string{"pause", "x"}, `invalid download id "x"`},
		{[]string{"resume", "0"}, `invalid download id "0"`},
		{[]string{"resume", "3", "-c", "40"}, "-c must be between"},
		{[]string{"resume", "3", "--limit", "-5"}, "invalid rate"},
		{[]string{"resume", "3", "--proxy", "direct"}, "apply only with --local"},
		{[]string{"resume", "3", "-v"}, "apply only with --local"},
		{[]string{"resume", "3", "--local", "--wait"}, "--wait cannot be combined with --local"},
		{[]string{"rm", "a", "b"}, "expects exactly one download id"},
	} {
		if r := h.run(ctx, tc.args...); r.code != exitError || !strings.Contains(r.stderr, tc.out) {
			t.Errorf("downex %q: %v\nwant %q", tc.args, r, tc.out)
		}
	}

	// --local refuses to run beside a daemon that manages the same downloads.
	n := len(f.rec().resumes)
	for _, args := range [][]string{{"resume", "3", "--local"}, {"rm", "3", "--local"}} {
		r := h.run(ctx, args...)
		if r.code != exitError || !strings.Contains(r.stderr, "downexd is running and manages the downloads") {
			t.Errorf("%q beside a daemon: %v", args, r)
		}
	}
	if len(f.rec().resumes) != n {
		t.Error("resume --local called the daemon")
	}
}

func TestSettingsCommand(t *testing.T) {
	f, h := newFakeDaemon(t)
	ctx := context.Background()

	r := h.run(ctx, "settings")
	if r.code != exitOK || !strings.Contains(r.stdout, "Max concurrent downloads:   3") ||
		!strings.Contains(r.stdout, "Global speed limit:         unlimited") ||
		!strings.Contains(r.stdout, "Default directory:          /home/u/Downloads") ||
		!strings.Contains(r.stdout, "Default connections:        8") {
		t.Errorf("settings:\n%v", r)
	}
	if len(f.rec().updates) != 0 {
		t.Error("settings without flags changed something")
	}

	r = h.run(ctx, "settings", "--max-concurrent", "5", "--global-limit", "2M", "--dir", "dl", "--json")
	if r.code != exitOK {
		t.Fatalf("settings with flags: %v", r)
	}
	updates := f.rec().updates
	if len(updates) != 1 {
		t.Fatalf("%d updates, want 1", len(updates))
	}
	// A full replacement: the changed fields, and the others as they were.
	if u := updates[0]; u.GetMaxConcurrentDownloads() != 5 || u.GetGlobalSpeedLimit() != 2_000_000 ||
		u.GetDefaultDir() != mustAbs(t, "dl") || u.GetDefaultMaxConnections() != 8 {
		t.Errorf("UpdateSettings %v", u)
	}
	var v settingsView
	if err := json.Unmarshal([]byte(r.stdout), &v); err != nil || v.MaxConcurrentDownloads != 5 || v.GlobalSpeedLimit != 2_000_000 {
		t.Errorf("settings --json: %+v %v\n%s", v, err, r.stdout)
	}
	r = h.run(ctx, "settings", "--connections", "16")
	if u := f.rec().updates; r.code != exitOK || len(u) != 2 || u[1].GetDefaultMaxConnections() != 16 ||
		u[1].GetMaxConcurrentDownloads() != 5 {
		t.Errorf("settings --connections: %v %v", r, u)
	}

	for _, tc := range []struct {
		args []string
		out  string
	}{
		{[]string{"settings", "--max-concurrent", "0"}, "--max-concurrent must be between 1 and 16"},
		{[]string{"settings", "--max-concurrent", "17"}, "--max-concurrent must be between 1 and 16"},
		{[]string{"settings", "--connections", "33"}, "--connections must be between 1 and 32"},
		{[]string{"settings", "--global-limit", "fast"}, "invalid rate"},
		{[]string{"settings", "--dir", ""}, "--dir must not be empty"},
		{[]string{"settings", "extra"}, "takes no arguments"},
	} {
		if r := h.run(ctx, tc.args...); r.code != exitError || !strings.Contains(r.stderr, tc.out) {
			t.Errorf("downex %q: %v\nwant %q", tc.args, r, tc.out)
		}
	}
	if u := f.rec().updates; len(u) != 2 {
		t.Errorf("invalid settings were sent: %v", u)
	}
}

func TestSettingsCommandM45(t *testing.T) {
	f, h := newFakeDaemon(t)
	ctx := context.Background()

	r := h.run(ctx, "settings")
	for _, want := range []string{"Category folders:           on", "Confirm browser downloads:  on",
		"Launch at login:            off", "Tray icon:                  on", "Categories:", "Video",
		filepath.Join("/home/u/Downloads", "Video") + " (default)", "mp4 mkv", "/srv/books", "epub"} {
		if r.code != exitOK || !strings.Contains(r.stdout, want) {
			t.Errorf("settings: want %q in\n%s", want, r.stdout)
		}
	}

	// Changing one field writes every other one back as it was, categories included.
	if r = h.run(ctx, "settings", "--max-concurrent", "4"); r.code != exitOK {
		t.Fatalf("settings: %v", r)
	}
	u := f.rec().updates[0]
	if u.GetMaxConcurrentDownloads() != 4 || len(u.GetCategories()) != 2 || u.GetCategories()[1].GetDir() != "/srv/books" ||
		!u.GetUseCategoryFolders() || !u.GetConfirmBrowserDownloads() || !u.GetNotifyOnComplete() ||
		!u.GetNotifyOnFailure() || u.GetLaunchAtLogin() || !u.GetShowTrayIcon() {
		t.Errorf("UpdateSettings %v", u)
	}

	r = h.run(ctx, "settings", "--categories-folders", "off", "--confirm-browser=false", "--notify-complete", "off",
		"--notify-failure", "no", "--launch-at-login", "on", "--tray", "OFF", "--json")
	if r.code != exitOK {
		t.Fatalf("settings with on/off flags: %v", r)
	}
	u = f.rec().updates[1]
	if u.GetUseCategoryFolders() || u.GetConfirmBrowserDownloads() || u.GetNotifyOnComplete() || u.GetNotifyOnFailure() ||
		!u.GetLaunchAtLogin() || u.GetShowTrayIcon() || u.GetMaxConcurrentDownloads() != 4 || len(u.GetCategories()) != 2 {
		t.Errorf("UpdateSettings %v", u)
	}
	var v settingsView
	if err := json.Unmarshal([]byte(r.stdout), &v); err != nil || !v.LaunchAtLogin || v.ShowTrayIcon || len(v.Categories) != 2 ||
		v.Categories[0].Folder != filepath.Join("/home/u/Downloads", "Video") || v.Categories[1].Dir != "/srv/books" {
		t.Errorf("settings --json: %+v %v\n%s", v, err, r.stdout)
	}
	if r := h.run(ctx, "settings", "--tray", "maybe"); r.code != exitError || !strings.Contains(r.stderr, "want on or off") {
		t.Errorf("bad on/off value: %v", r)
	}
	if u := f.rec().updates; len(u) != 2 {
		t.Errorf("an invalid flag sent settings: %v", u)
	}
	// No categories at all.
	f.do(func() { f.settings.Categories = nil })
	if r := h.run(ctx, "settings"); r.code != exitOK || !strings.Contains(r.stdout, "Categories: none") {
		t.Errorf("settings without categories: %v", r)
	}
}

func TestConfirmCommand(t *testing.T) {
	f, h := newFakeDaemon(t)
	ctx := context.Background()
	awaiting := downexv1.DownloadState_DOWNLOAD_STATE_AWAITING_CONFIRMATION
	f.do(func() {
		f.downloads[5] = &downexv1.Download{Id: 5, Url: "https://example.test/a.zip", Dir: "/home/u/Downloads/Archives",
			Filename: "a.zip", State: awaiting, Size: -1}
		f.downloads[6] = &downexv1.Download{Id: 6, Url: "https://example.test/b.zip", Dir: "/d", Filename: "b.zip",
			State: awaiting, Size: -1}
		f.downloads[8] = &downexv1.Download{Id: 8, Url: "https://example.test/c", State: downexv1.DownloadState_DOWNLOAD_STATE_PAUSED}
	})

	r := h.run(ctx, "ls")
	if r.code != exitOK || !strings.Contains(r.stdout, "awaiting") {
		t.Errorf("ls: %v", r)
	}
	r = h.run(ctx, "confirm", "5", "-o", "rel", "--name", "x.zip")
	if r.code != exitOK || !strings.Contains(r.stdout, "Confirmed download 5 (queued, position 1 in the queue): "+filepath.Join(mustAbs(t, "rel"), "x.zip")) {
		t.Fatalf("confirm: %v", r)
	}
	c := f.rec().confirms[0]
	if c.GetId() != 5 || c.GetDir() != mustAbs(t, "rel") || c.GetFilename() != "x.zip" || !c.GetStart() {
		t.Errorf("ConfirmRequest = %v", c)
	}
	r = h.run(ctx, "confirm", "6", "--later")
	if r.code != exitOK || !strings.Contains(r.stdout, "Confirmed download 6 for later: "+filepath.Join("/d", "b.zip")) ||
		!strings.Contains(r.stdout, "downex resume 6") {
		t.Fatalf("confirm --later: %v", r)
	}
	if c := f.rec().confirms[1]; c.GetStart() || c.GetDir() != "" || c.GetFilename() != "" {
		t.Errorf("ConfirmRequest = %v", c)
	}
	for _, tc := range []struct {
		args []string
		out  string
	}{
		{[]string{"confirm", "8"}, "not awaiting confirmation"},
		{[]string{"confirm", "99"}, "no download with id 99"},
		{[]string{"confirm"}, "expects exactly one download id"},
		{[]string{"confirm", "x"}, "invalid download id \"x\""},
		{[]string{"confirm", "5", "--later", "--wait"}, "cannot be combined"},
		{[]string{"confirm", "5", "--name", ""}, "--name must not be empty"},
	} {
		if r := h.run(ctx, tc.args...); r.code != exitError || !strings.Contains(r.stderr, tc.out) {
			t.Errorf("downex %q: %v\nwant %q", tc.args, r, tc.out)
		}
	}
	if n := len(f.rec().confirms); n != 4 { // the two above, and 8 and 99
		t.Errorf("%d confirms sent, want 4", n)
	}
}

func TestDaemonSubcommands(t *testing.T) {
	f, h := newFakeDaemon(t)
	ctx := context.Background()
	f.do(func() {
		f.status.StartedAt = timestamppb.New(time.Now().Add(-5 * time.Minute))
		f.status.Active, f.status.Queued = 1, 2
	})

	r := h.run(ctx, "daemon", "status")
	if r.code != exitOK || !strings.Contains(r.stdout, "running") || !strings.Contains(r.stdout, "4242") ||
		!strings.Contains(r.stdout, "0.2.0-test") || !strings.Contains(r.stdout, "1 active, 2 queued") ||
		!strings.Contains(r.stdout, "(up 5m00s)") {
		t.Errorf("status:\n%v", r)
	}
	r = h.run(ctx, "daemon", "status", "--json")
	var st statusView
	if err := json.Unmarshal([]byte(r.stdout), &st); err != nil || !st.Running || st.PID != 4242 || st.Endpoint != "fake-endpoint" ||
		st.StartedAt == nil || st.Queued != 2 {
		t.Errorf("status --json: %+v %v", st, err)
	}
	if r = h.run(ctx, "daemon", "start"); r.code != exitOK || !strings.Contains(r.stdout, "downexd is already running (pid 4242).") {
		t.Errorf("start while running: %v", r)
	}

	// stop asks the daemon to shut down and waits until its endpoint is gone.
	f.do(func() { f.onShutdown = func() { h.setUp(false) } })
	if r = h.run(ctx, "daemon", "stop"); r.code != exitOK || !strings.Contains(r.stdout, "Stopped downexd.") || f.rec().shutdowns != 1 {
		t.Errorf("stop: %v (%d shutdowns)", r, f.rec().shutdowns)
	}
	if r = h.run(ctx, "daemon", "stop"); r.code != exitOK || !strings.Contains(r.stdout, "downexd is not running.") || f.rec().shutdowns != 1 {
		t.Errorf("stop while stopped: %v", r)
	}
	if r = h.run(ctx, "daemon", "status"); r.code != exitError || !strings.Contains(r.stdout, "downexd is not running.") || r.stderr != "" {
		t.Errorf("status while stopped: %v", r)
	}
	if r = h.run(ctx, "daemon", "status", "--json"); r.code != exitError || !strings.Contains(r.stdout, `"running": false`) {
		t.Errorf("status --json while stopped: %v", r)
	}
	if _, spawns := h.recorded(); len(spawns) != 0 {
		t.Errorf("stop/status started a daemon: %q", spawns)
	}

	h.readyAfter = 1
	if r = h.run(ctx, "daemon", "start"); r.code != exitOK || !strings.Contains(r.stdout, "Started downexd (pid 4242) on fake-endpoint.") {
		t.Errorf("start: %v", r)
	}

	// A daemon that does not go away is reported.
	f.do(func() { f.onShutdown = nil })
	h.stopTimeout = 100 * time.Millisecond
	if r = h.run(ctx, "daemon", "stop"); r.code != exitError || !strings.Contains(r.stderr, "still running") {
		t.Errorf("stop of a daemon that stays: %v", r)
	}

	for _, args := range [][]string{{"daemon"}, {"daemon", "restart"}, {"daemon", "start", "stop"}} {
		if r := h.run(ctx, args...); r.code != exitError || !strings.Contains(r.stderr, "downex daemon:") {
			t.Errorf("downex %q: %v", args, r)
		}
	}
}

func TestGlobalFlagsAndHelp(t *testing.T) {
	_, h := newFakeDaemon(t)
	ctx := context.Background()

	if r := h.run(ctx, "--endpoint", "ep-2", "ls"); r.code != exitOK {
		t.Fatalf("ls: %v", r)
	}
	if r := h.run(ctx, "ls", "--endpoint", "ep-3"); r.code != exitOK {
		t.Fatalf("ls: %v", r)
	}
	if r := h.run(ctx, "--endpoint", "ep-4", "ls", "--endpoint", "ep-5"); r.code != exitOK {
		t.Fatalf("ls: %v", r)
	}
	if dials, _ := h.recorded(); !slices.Equal(dials, []string{"ep-2", "ep-3", "ep-5"}) {
		t.Errorf("dialled %q, want the --endpoint values (the command's own winning)", dials)
	}

	for _, tc := range []struct {
		args []string
		code int
		out  string
	}{
		{[]string{"--bogus", "ls"}, exitError, "flag provided but not defined: -bogus"},
		{[]string{"--db"}, exitError, "flag needs an argument"},
		{[]string{"--db", "x.db"}, exitError, "Usage:"},
		{[]string{"--db", "x.db", "-h"}, exitOK, "Global flags"},
		{[]string{"help"}, exitOK, "downex daemon start|stop|status"},
		{[]string{"help", "add"}, exitOK, "-wait"},
		{[]string{"help", "watch"}, exitOK, "Ctrl+C"},
		{[]string{"help", "daemon"}, exitOK, "status  show whether downexd is running"},
		{[]string{"help", "settings"}, exitOK, "-max-concurrent"},
		{[]string{"help", "resume"}, exitOK, "-local"},
		{[]string{"help", "pause"}, exitOK, "Usage: downex pause <id>"},
	} {
		r := h.run(ctx, tc.args...)
		if r.code != tc.code || !strings.Contains(r.stdout+r.stderr, tc.out) {
			t.Errorf("downex %q: %v\nwant exit %d and %q", tc.args, r, tc.code, tc.out)
		}
	}
}

func TestRPCErrorMessages(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{connect.NewError(connect.CodeInvalidArgument, errors.New("bad dir")), "add: bad dir"},
		{connect.NewError(connect.CodeFailedPrecondition, errors.New("download is complete")), "add: download is complete"},
		{connect.NewError(connect.CodeUnavailable, errors.New("eof")), "add: downexd is not reachable"},
		{connect.NewError(connect.CodeDeadlineExceeded, context.DeadlineExceeded), "add: downexd did not answer in time"},
		{errors.New("plain"), "add: plain"},
	} {
		got := rpcError("add", tc.err)
		if !strings.HasPrefix(got.Error(), tc.want) || !errors.Is(got, tc.err) {
			t.Errorf("rpcError(%v) = %q, want prefix %q and the cause wrapped", tc.err, got, tc.want)
		}
	}
	canceled := connect.NewError(connect.CodeCanceled, context.Canceled)
	if got := rpcError("add", canceled); got != canceled { //nolint:errorlint // returned unchanged
		t.Errorf("canceled call rewritten: %v", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !interrupted(ctx, connect.NewError(connect.CodeCanceled, errors.New("stream closed"))) {
		t.Error("a canceled call after Ctrl+C is not an interruption")
	}
	if got := downloadRPCError("rm", 4, connect.NewError(connect.CodeNotFound, errors.New("x"))); got.Error() != `no download with id 4 (see "downex ls")` {
		t.Errorf("not found: %q", got)
	}
	if !errors.Is(&daemonError{msg: "m", err: ipc.ErrNotRunning}, ipc.ErrNotRunning) {
		t.Error("daemonError does not wrap its cause")
	}
}

func TestCommandLineQuoting(t *testing.T) {
	if got := commandLine(&daemonTarget{}, "resume", "3"); got != "downex resume 3" {
		t.Errorf("commandLine = %q", got)
	}
	got := commandLine(&daemonTarget{endpoint: `\\.\pipe\downex-test`, db: "/tmp/my db/x.db"}, "watch", "3")
	if !strings.HasPrefix(got, `downex watch 3 --endpoint \\.\pipe\downex-test --db "`) || !strings.HasSuffix(got, `x.db"`) {
		t.Errorf("commandLine with flags = %q", got)
	}
}
