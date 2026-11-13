package e2e

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/testserver"
	"downex/internal/update"
)

// End-to-end tests of M6 (docs/M6-SPEC.md) with real downexd processes: queues and their
// admission, the schedule, post-queue actions, host rules, marking of downloaded files and
// update checks. The schedule, the post-queue actions, the trusted test certificates and
// the update key need the e2etest build of downexd (bin.downexdHooks, test hooks of
// cmd/downexd/testhooks_e2e.go); everything else runs the product build.

// Environment of the e2etest build (cmd/downexd/testhooks_e2e.go).
const (
	envHookClock     = "DOWNEX_E2E_CLOCK"
	envHookPowerLog  = "DOWNEX_E2E_POWER_LOG"
	envHookRootCA    = "DOWNEX_E2E_ROOT_CA"
	envHookUpdateKey = "DOWNEX_E2E_UPDATE_KEY"
)

// startHooksDaemon starts the e2etest build of downexd with the harness's environment
// plus extra.
func (h *harness) startHooksDaemon(extra map[string]string) *daemonProc {
	h.t.Helper()
	env := slices.Clone(h.env)
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	p := h.startDaemonFrom(bin.downexdHooks, env)
	h.waitReady(p)
	return p
}

// call runs one RPC of the test with rpcCallTimeout and fails the test on an error.
func call[Res any](t *testing.T, what string, fn func(ctx context.Context) (*connect.Response[Res], error)) *Res {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), rpcCallTimeout)
	defer cancel()
	res, err := fn(ctx)
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	return res.Msg
}

// settings replaces the daemon's settings with edit applied to the current ones.
func (h *harness) settings(edit func(*downexv1.Settings)) *downexv1.Settings {
	h.t.Helper()
	cur := call(h.t, "get settings", func(ctx context.Context) (*connect.Response[downexv1.GetSettingsResponse], error) {
		return h.dm.GetSettings(ctx, connect.NewRequest(&downexv1.GetSettingsRequest{}))
	}).GetSettings()
	edit(cur)
	return call(h.t, "update settings", func(ctx context.Context) (*connect.Response[downexv1.UpdateSettingsResponse], error) {
		return h.dm.UpdateSettings(ctx, connect.NewRequest(&downexv1.UpdateSettingsRequest{Settings: cur}))
	}).GetSettings()
}

func (h *harness) createQueue(q *downexv1.Queue) *downexv1.Queue {
	h.t.Helper()
	return call(h.t, "create queue "+q.GetName(), func(ctx context.Context) (*connect.Response[downexv1.CreateQueueResponse], error) {
		return h.dq.CreateQueue(ctx, connect.NewRequest(&downexv1.CreateQueueRequest{Queue: q}))
	}).GetQueue()
}

func (h *harness) startQueue(id int64) {
	h.t.Helper()
	call(h.t, "start queue", func(ctx context.Context) (*connect.Response[downexv1.StartQueueResponse], error) {
		return h.dq.StartQueue(ctx, connect.NewRequest(&downexv1.StartQueueRequest{Id: id}))
	})
}

func (h *harness) queue(id int64) *downexv1.Queue {
	h.t.Helper()
	qs := call(h.t, "list queues", func(ctx context.Context) (*connect.Response[downexv1.ListQueuesResponse], error) {
		return h.dq.ListQueues(ctx, connect.NewRequest(&downexv1.ListQueuesRequest{}))
	}).GetQueues()
	for _, q := range qs {
		if q.GetId() == id {
			return q
		}
	}
	h.t.Fatalf("queue %d is not listed: %v", id, qs)
	return nil
}

// addTo adds a download of url into queue (0 = the main queue) over the API.
func (h *harness) addTo(url string, queue int64, maxConns int32) *downexv1.Download {
	h.t.Helper()
	return call(h.t, "add "+url, func(ctx context.Context) (*connect.Response[downexv1.AddResponse], error) {
		return h.dl.Add(ctx, connect.NewRequest(&downexv1.AddRequest{Url: url, Dir: h.out, QueueId: queue, MaxConnections: maxConns}))
	}).GetDownload()
}

// waitFailed waits until download id has failed and returns it.
func (h *harness) waitFailed(id int64) *downexv1.Download {
	h.t.Helper()
	deadline := time.Now().Add(completeTimeout)
	for {
		d := h.get(id)
		switch d.GetState() {
		case downexv1.DownloadState_DOWNLOAD_STATE_FAILED:
			return d
		case downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED:
			h.t.Fatalf("download %d completed, want it to fail: %v", id, d)
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("download %d did not fail within %v: %v", id, completeTimeout, d)
		}
		time.Sleep(pollInterval)
	}
}

// TestQueueAdmission: two running queues with their own limits share the global limit;
// a stopped queue starts nothing until it is started.
func TestQueueAdmission(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	p := h.startDaemon()
	h.waitReady(p)
	const size = 384 << 10
	ts := newServer(t, testserver.Options{Size: size, Seed: 61, ETag: `"q"`, Throttle: 192 << 10})
	h.settings(func(s *downexv1.Settings) { s.MaxConcurrentDownloads = 3 })

	a := h.createQueue(&downexv1.Queue{Name: "One at a time", MaxConcurrent: 1})
	b := h.createQueue(&downexv1.Queue{Name: "Three at a time", MaxConcurrent: 3})
	c := h.createQueue(&downexv1.Queue{Name: "Stopped"})
	queueOf := map[int64]int64{}
	var ids []int64
	for i, q := range []int64{a.GetId(), a.GetId(), a.GetId(), b.GetId(), b.GetId(), b.GetId(), b.GetId()} {
		d := h.addTo(fmt.Sprintf("%s/admit-%d.bin", ts.URL, i), q, 1)
		if d.GetQueueId() != q || d.GetState() != downexv1.DownloadState_DOWNLOAD_STATE_QUEUED {
			t.Fatalf("added %v, want queued in queue %d", d, q)
		}
		queueOf[d.GetId()], ids = q, append(ids, d.GetId())
	}
	held := h.addTo(ts.URL+"/held.bin", c.GetId(), 1)
	// Queues other than the main one are stopped when created: nothing ran yet.
	time.Sleep(300 * time.Millisecond)
	if d := h.get(ids[0]); d.GetState() != downexv1.DownloadState_DOWNLOAD_STATE_QUEUED {
		t.Fatalf("a download of a stopped queue runs: %v", d)
	}
	h.startQueue(a.GetId())
	h.startQueue(b.GetId())

	sawFull := false
	deadline := time.Now().Add(2 * completeTimeout)
	for {
		list, err := h.list()
		if err != nil {
			t.Fatal(err)
		}
		perQueue, done := map[int64]int{}, 0
		for _, d := range list {
			switch {
			case running(d.GetState()):
				perQueue[d.GetQueueId()]++
			case d.GetState() == downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED:
				done++
			case d.GetState() == downexv1.DownloadState_DOWNLOAD_STATE_FAILED:
				t.Fatalf("download failed: %v", d)
			}
		}
		total := perQueue[a.GetId()] + perQueue[b.GetId()] + perQueue[c.GetId()]
		switch {
		case perQueue[a.GetId()] > 1:
			t.Fatalf("queue %q runs %d downloads, its limit is 1", a.GetName(), perQueue[a.GetId()])
		case total > 3:
			t.Fatalf("%d downloads run, the global limit is 3: %v", total, perQueue)
		case perQueue[c.GetId()] > 0:
			t.Fatalf("the stopped queue runs a download")
		}
		if perQueue[a.GetId()] == 1 && perQueue[b.GetId()] == 2 {
			sawFull = true
		}
		if done == len(ids) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d downloads completed: %v", done, len(ids), list)
		}
		time.Sleep(pollInterval)
	}
	if !sawFull {
		t.Error("never saw both queues at their share of the global limit (1 + 2)")
	}
	for _, id := range ids {
		d := h.get(id)
		checkFile(t, filepath.Join(d.GetDir(), d.GetFilename()), 61, size)
	}
	if d := h.get(held.GetId()); d.GetState() != downexv1.DownloadState_DOWNLOAD_STATE_QUEUED {
		t.Fatalf("the download of the stopped queue = %v, want still queued", d)
	}
	h.startQueue(c.GetId())
	h.waitCompleted(held.GetId())
	h.stop(p)
}

// localAt is the next Monday after now at hh:mm local time (a stable, ordinary date for
// the scaled schedule clock).
func localAt(hh, mm int) time.Time {
	d := time.Now().AddDate(0, 0, 1)
	for d.Weekday() != time.Monday {
		d = d.AddDate(0, 0, 1)
	}
	return time.Date(d.Year(), d.Month(), d.Day(), hh, mm, 0, 0, time.Local)
}

// TestQueueScheduleStarts: a queue with a schedule starts at its start minute (the
// e2etest build's clock runs 30 times faster, from 09:55 local time to the 10:00 start).
func TestQueueScheduleStarts(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	p := h.startHooksDaemon(map[string]string{envHookClock: localAt(9, 55).Format(time.RFC3339) + ",30"})
	ts := newServer(t, testserver.Options{Size: 256 << 10, Seed: 62, ETag: `"s"`})

	q := h.createQueue(&downexv1.Queue{Name: "At ten", Schedule: &downexv1.Schedule{
		Enabled: true, StartMinute: 10 * 60, Weekdays: 0x7f,
	}})
	d := h.addTo(ts.URL+"/scheduled.bin", q.GetId(), 0)
	if h.queue(q.GetId()).GetRunning() {
		t.Fatal("the scheduled queue runs before its start")
	}
	if got := h.get(d.GetId()); got.GetState() != downexv1.DownloadState_DOWNLOAD_STATE_QUEUED {
		t.Fatalf("download before the start = %v", got)
	}
	// 5 scheduled minutes are 10 s here.
	h.waitCompleted(d.GetId())
	checkFile(t, filepath.Join(h.out, h.get(d.GetId()).GetFilename()), 62, 256<<10)
	// The queue started by its schedule; having finished, it stopped again (a queue other
	// than the main one runs until its work is done).
	if log := h.daemonLog(); !strings.Contains(log, `"msg":"queue started","queue":`+strconv.FormatInt(q.GetId(), 10)) ||
		!strings.Contains(log, `"by":"schedule"`) {
		t.Errorf("the log does not show the scheduled start:\n%s", log)
	}
	h.stop(p)
}

// powerActions returns the actions the e2etest build recorded in path.
func powerActions(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(b))
}

// TestPostQueueActions: a queue that finished performs its post-queue action once after
// the countdown (here a recording stand-in for the power actions, with the clock 10 times
// faster: 6 s); CancelPostAction prevents it; quitting stops the daemon.
func TestPostQueueActions(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	powerLog := filepath.Join(h.root, "power.log")
	p := h.startHooksDaemon(map[string]string{
		envHookClock:    time.Now().Format(time.RFC3339) + ",10",
		envHookPowerLog: powerLog,
	})
	ts := newServer(t, testserver.Options{Size: 128 << 10, Seed: 63, ETag: `"p"`})

	// Sleep once.
	sleepQ := h.createQueue(&downexv1.Queue{Name: "Then sleep", PostAction: downexv1.PostAction_POST_ACTION_SLEEP})
	d := h.addTo(ts.URL+"/sleep.bin", sleepQ.GetId(), 0)
	h.startQueue(sleepQ.GetId())
	h.waitCompleted(d.GetId())
	deadline := time.Now().Add(30 * time.Second)
	for len(powerActions(t, powerLog)) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the sleep action did not happen after the countdown")
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(2 * time.Second) // nothing more follows
	if got := powerActions(t, powerLog); !slices.Equal(got, []string{"sleep"}) {
		t.Fatalf("power actions = %q, want one sleep", got)
	}
	if h.queue(sleepQ.GetId()).GetRunning() {
		t.Error("the finished queue still runs")
	}

	// Cancelled shutdown.
	shutQ := h.createQueue(&downexv1.Queue{Name: "Then shut down", PostAction: downexv1.PostAction_POST_ACTION_SHUTDOWN})
	d = h.addTo(ts.URL+"/shutdown.bin", shutQ.GetId(), 0)
	h.startQueue(shutQ.GetId())
	deadline = time.Now().Add(completeTimeout)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), rpcCallTimeout)
		_, err := h.dm.CancelPostAction(ctx, connect.NewRequest(&downexv1.CancelPostActionRequest{QueueId: shutQ.GetId()}))
		cancel()
		if err == nil {
			break
		}
		if connect.CodeOf(err) != connect.CodeFailedPrecondition || time.Now().After(deadline) {
			t.Fatalf("cancel post-queue action: %v", err)
		}
		time.Sleep(pollInterval)
	}
	time.Sleep(8 * time.Second) // past the countdown's deadline
	if got := powerActions(t, powerLog); !slices.Equal(got, []string{"sleep"}) {
		t.Fatalf("power actions after the cancelled shutdown = %q", got)
	}

	// Quit: the daemon stops by itself.
	quitQ := h.createQueue(&downexv1.Queue{Name: "Then quit", PostAction: downexv1.PostAction_POST_ACTION_QUIT_DOWNEX})
	d = h.addTo(ts.URL+"/quit.bin", quitQ.GetId(), 0)
	h.startQueue(quitQ.GetId())
	h.waitCompleted(d.GetId())
	if code := h.waitExit(p); code != 0 {
		t.Fatalf("downexd exited with %d after the quit action: %v", code, p)
	}
	if log := h.daemonLog(); !strings.Contains(log, "post-queue action cancelled") {
		t.Errorf("the log does not show the cancelled action:\n%s", log)
	}
}

// TestHostRuleCapsConnections: a host rule's max_connections caps the connections of a
// download to that host; another host name of the same server is not capped.
func TestHostRuleCapsConnections(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	p := h.startDaemon()
	h.waitReady(p)
	const size = 6 << 20
	capped := newServer(t, testserver.Options{Size: size, Seed: 64, ETag: `"h"`, Throttle: 2 << 20})
	h.settings(func(s *downexv1.Settings) {
		s.HostRules = []*downexv1.HostRule{{Pattern: "127.0.0.1", MaxConnections: 1}}
	})
	d := h.addTo(capped.URL+"/capped.bin", 0, 8)
	h.waitCompleted(d.GetId())
	checkFile(t, filepath.Join(h.out, h.get(d.GetId()).GetFilename()), 64, size)
	if n := capped.MaxConcurrent(); n != 1 {
		t.Fatalf("the host rule allows 1 connection, the server saw %d at once", n)
	}

	free := newServer(t, testserver.Options{Size: size, Seed: 65, ETag: `"f"`, Throttle: 2 << 20})
	d = h.addTo(strings.Replace(free.URL, "127.0.0.1", "localhost", 1)+"/free.bin", 0, 8)
	h.waitCompleted(d.GetId())
	if n := free.MaxConcurrent(); n < 2 {
		t.Errorf("without a matching rule the download used %d connection(s), want several", n)
	}
	h.stop(p)
}

// TestMarkDownloadedFiles: a completed download carries its origin (Windows
// Zone.Identifier, macOS quarantine, Linux user.xdg.origin.url) with mark_downloaded_files
// on, the default.
func TestMarkDownloadedFiles(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	p := h.startDaemon()
	h.waitReady(p)
	ts := newServer(t, testserver.Options{Size: 64 << 10, Seed: 66, ETag: `"m"`})
	d := h.addTo(ts.URL+"/marked.bin?token=1", 0, 0)
	h.waitCompleted(d.GetId())
	path := filepath.Join(h.out, h.get(d.GetId()).GetFilename())
	mark, err := readOriginMark(path)
	if err == errNoMarks {
		t.Skipf("%s: %v", path, err)
	}
	if err != nil {
		t.Fatalf("read the mark of %s: %v", path, err)
	}
	if !strings.Contains(mark, wantMark(ts.URL+"/marked.bin?token=1")) {
		t.Fatalf("mark of %s = %q", path, mark)
	}
	h.stop(p)
}

// errNoMarks reports a file system without origin marks (no alternate data streams or
// extended attributes there): the marking test is skipped.
var errNoMarks = errors.New("the file system keeps no origin marks")

// wantMark is what the origin mark of a file downloaded from url contains.
func wantMark(url string) string { return url }

// TestUpdateCheck: a signed feed (a test key; the e2etest build is version 1.0.0) reports
// a newer version; a manifest changed after signing is refused.
func TestUpdateCheck(t *testing.T) {
	t.Parallel()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var manifest, sig []byte
	feed := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/downex.json":
			_, _ = w.Write(manifest)
		case "/downex.json.sig":
			_, _ = w.Write(sig)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(feed.Close)
	arch := runtime.GOARCH
	if runtime.GOOS == "darwin" {
		arch = "universal"
	}
	m, _ := json.Marshal(map[string]any{
		"version": "1.1.0", "published_at": "2026-10-01T00:00:00Z", "notes": "End-to-end test release",
		"assets": []map[string]any{{"os": runtime.GOOS, "arch": arch, "url": feed.URL + "/pkg",
			"sha256": strings.Repeat("ab", 32), "size": 1234}},
	})
	mu.Lock()
	manifest, sig = m, update.EncodeSignature(ed25519.Sign(priv, m))
	mu.Unlock()

	h := newHarness(t)
	ca := filepath.Join(h.root, "feed-ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: feed.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	p := h.startHooksDaemon(map[string]string{
		envHookRootCA: ca, envHookUpdateKey: base64.StdEncoding.EncodeToString(pub),
	})
	h.settings(func(s *downexv1.Settings) {
		s.UpdateFeedUrl, s.AutoCheckUpdates = feed.URL+"/downex.json", false
	})
	check := func() (*downexv1.UpdateInfo, error) {
		ctx, cancel := context.WithTimeout(context.Background(), rpcCallTimeout)
		defer cancel()
		res, err := h.dm.CheckForUpdate(ctx, connect.NewRequest(&downexv1.CheckForUpdateRequest{}))
		if err != nil {
			return nil, err
		}
		return res.Msg.GetInfo(), nil
	}
	info, err := check()
	if err != nil {
		t.Fatalf("check for update: %v", err)
	}
	if !info.GetAvailable() || info.GetLatestVersion() != "1.1.0" || info.GetCurrentVersion() != hooksVersion ||
		info.GetNotes() != "End-to-end test release" {
		t.Fatalf("update info = %v", info)
	}

	mu.Lock()
	manifest = []byte(strings.Replace(string(m), "1.1.0", "9.9.9", 1)) // the signature no longer matches
	mu.Unlock()
	if info, err := check(); connect.CodeOf(err) != connect.CodeDataLoss {
		t.Fatalf("check of a tampered manifest = %v, %v; want DataLoss", info, err)
	}
	h.stop(p)
}
