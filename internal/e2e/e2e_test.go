package e2e

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"downex/internal/engine"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/testserver"
)

// add runs "downex add" with args and returns the new download's id.
func (h *harness) add(args ...string) int64 {
	h.t.Helper()
	return h.cli(append([]string{"add"}, args...)...).ok(h.t).id(h.t)
}

func idArg(id int64) string { return strconv.FormatInt(id, 10) }

// "downex add --wait" hands the download to downexd and follows it to the end; ls
// and "daemon status" show the daemon's view.
func TestAddWait(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	const size, seed = 3<<20 + 17, 101
	srv := newServer(t, testserver.Options{Size: size, Seed: seed, ETag: `"w1"`, Filename: "first report.bin"})
	d := h.startDaemon()
	h.waitReady(d)

	r := h.cli("add", srv.URL+"/dl?id=1", "-o", h.out, "-c", "4", "--wait").ok(t)
	id := r.id(t)
	path := filepath.Join(h.out, "first report.bin")
	checkFile(t, path, seed, size)
	noPartFiles(t, h.out)
	if !strings.Contains(r.stderr, "Saved "+path) {
		t.Errorf("add --wait did not report the saved file: %v", r)
	}

	e := entry(t, h.ls(), id)
	if e.State != "completed" || e.Downloaded != size || e.Size != size || e.Path != path {
		t.Errorf("ls entry = %+v", e)
	}
	r = h.cli("daemon", "status", "--json").ok(t)
	var st struct {
		Running  bool   `json:"running"`
		PID      int    `json:"pid"`
		Endpoint string `json:"endpoint"`
		Active   int    `json:"active"`
		Queued   int    `json:"queued"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &st); err != nil {
		t.Fatalf("daemon status --json: %v: %v", err, r)
	}
	if !st.Running || st.PID != d.pid() || st.Endpoint != h.endpoint || st.Active != 0 || st.Queued != 0 {
		t.Errorf("daemon status = %+v (daemon pid %d, endpoint %s)", st, d.pid(), h.endpoint)
	}

	r = h.cli("rm", idArg(id), "--delete-file").ok(t)
	if !strings.Contains(r.stdout, fmt.Sprintf("Removed download %d and deleted %s", id, path)) {
		t.Errorf("rm --delete-file: %v", r)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("rm --delete-file kept the file: %v", err)
	}
	if list := h.ls(); len(list) != 0 {
		t.Errorf("ls after rm: %+v", list)
	}

	h.stop(d)
	log := h.daemonLog()
	for _, want := range []string{`"msg":"downexd started"`, `"msg":"download completed"`, `"msg":"stopping on request"`, `"msg":"downexd stopped"`} {
		if !strings.Contains(log, want) {
			t.Errorf("daemon log lacks %s", want)
		}
	}
}

// A second downexd for the same endpoint finds the running one and exits with 0.
func TestSecondInstanceExitsZero(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	d := h.startDaemon()
	h.waitReady(d)

	// The endpoint identifies the daemon: another database does not matter.
	for _, db := range []string{h.db, filepath.Join(h.root, "other", "downex.db")} {
		r := h.run(bin.downexd, "--db", db, "--log-dir", h.logDir).ok(t)
		if !strings.Contains(r.stdout, "already running") || !strings.Contains(r.stdout, fmt.Sprintf("pid %d", d.pid())) {
			t.Errorf("second instance: %v", r)
		}
	}
	r := h.cli("daemon", "start").ok(t)
	if !strings.Contains(r.stdout, fmt.Sprintf("already running (pid %d)", d.pid())) {
		t.Errorf("daemon start while running: %v", r)
	}
	if st, err := h.status(); err != nil || int(st.GetPid()) != d.pid() {
		t.Fatalf("the first daemon no longer answers: %v %v", st, err)
	}
	h.stop(d)
}

// With max-concurrent 2, five downloads run at most two at a time, start in id order
// and all complete.
func TestQueueRunsAtMostMaxConcurrent(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	const n, size, seed = 5, 1536 << 10, 202
	srv := newServer(t, testserver.Options{Size: size, Seed: seed, ETag: `"q"`, Throttle: 1 << 20})
	d := h.startDaemon()
	h.waitReady(d)

	// A low global speed limit holds the queue while the downloads are added (each CLI
	// call takes a while, especially under -race); it is lifted below, live.
	const globalLimit = 64_000 // "64K" is decimal
	r := h.cli("settings", "--max-concurrent", "2", "--global-limit", "64K", "--json").ok(t)
	var s struct {
		MaxConcurrentDownloads int   `json:"maxConcurrentDownloads"`
		GlobalSpeedLimit       int64 `json:"globalSpeedLimit"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &s); err != nil || s.MaxConcurrentDownloads != 2 || s.GlobalSpeedLimit != globalLimit {
		t.Fatalf("settings: %v %v", err, r)
	}

	mon := h.monitor()
	limited := time.Now()
	ids := make([]int64, n)
	for i := range ids {
		// One connection each, so the server sees one request per running download.
		ids[i] = h.add(fmt.Sprintf("%s/q%d.bin", srv.URL, i+1), "-o", h.out, "-c", "1")
	}
	// The CLI shows the daemon's queue: the two oldest run, the rest wait in order.
	list := h.ls()
	elapsed := time.Since(limited)
	var runningIDs, queuedIDs []int64
	var total int64
	for _, e := range list {
		total += e.Downloaded
		switch e.State {
		case "probing", "downloading":
			runningIDs = append(runningIDs, e.ID)
		case "queued":
			queuedIDs = append(queuedIDs, e.ID)
			if want := len(queuedIDs); e.QueuePosition != want {
				t.Errorf("queued download %d has position %d, want %d", e.ID, e.QueuePosition, want)
			}
		}
	}
	if !slices.Equal(runningIDs, ids[:2]) || !slices.Equal(queuedIDs, ids[2:]) {
		t.Errorf("ls after adding: running %v, queued %v, want %v and %v: %+v", runningIDs, queuedIDs, ids[:2], ids[2:], list)
	}
	// The global limit caps all downloads together (plus one burst per download).
	if bound := int64(elapsed.Seconds()*globalLimit) + 2*(64<<10); total > bound {
		t.Errorf("%d bytes received in %v under a global limit of %d B/s (bound %d)", total, elapsed, globalLimit, bound)
	}
	h.cli("settings", "--global-limit", "0").ok(t)

	for _, id := range ids {
		h.waitCompleted(id)
	}
	res := mon.stop()
	t.Logf("max running %d, max active %d, start order %v, server max concurrent %d",
		res.maxRunning, res.maxActive, res.started, srv.MaxConcurrent())
	if res.maxRunning != 2 {
		t.Errorf("at most %d downloads were running at once, want exactly 2", res.maxRunning)
	}
	if res.maxActive > 2 {
		t.Errorf("Status reported %d active downloads, limit 2", res.maxActive)
	}
	if c := srv.MaxConcurrent(); c > 2 {
		t.Errorf("the server saw %d concurrent requests, limit 2 single-connection downloads", c)
	}
	if !slices.Equal(res.started, ids) {
		t.Errorf("downloads started in order %v, want %v (FIFO by id)", res.started, ids)
	}
	if res.err != nil {
		t.Errorf("polling the daemon: %v", res.err)
	}
	for i := range ids {
		checkFile(t, filepath.Join(h.out, fmt.Sprintf("q%d.bin", i+1)), seed, size)
	}
	h.stop(d)
}

// queueMonitor polls the daemon's list and status.
type queueMonitor struct {
	quit chan struct{}
	wg   sync.WaitGroup
	res  monitorResult
}

type monitorResult struct {
	maxRunning, maxActive int
	started               []int64 // ids in the order they were first seen running
	err                   error   // the first polling error
}

func (h *harness) monitor() *queueMonitor {
	m := &queueMonitor{quit: make(chan struct{})}
	seen := map[int64]bool{}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		for {
			list, err := h.list()
			st, serr := h.status()
			switch {
			case err != nil && m.res.err == nil:
				m.res.err = err
			case serr != nil && m.res.err == nil:
				m.res.err = serr
			}
			if err == nil {
				var now []int64
				count := 0
				for _, d := range list {
					if running(d.GetState()) {
						count++
						if !seen[d.GetId()] {
							seen[d.GetId()] = true
							now = append(now, d.GetId())
						}
					}
				}
				slices.Sort(now)
				m.res.started = append(m.res.started, now...)
				m.res.maxRunning = max(m.res.maxRunning, count)
			}
			if serr == nil {
				m.res.maxActive = max(m.res.maxActive, int(st.GetActive()))
			}
			select {
			case <-m.quit:
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}()
	return m
}

func (m *queueMonitor) stop() monitorResult {
	close(m.quit)
	m.wg.Wait()
	return m.res
}

// "downex pause" stops a running download with its progress saved; "resume --wait"
// continues it from there with conditional range requests.
func TestPauseResume(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	const size, seed, etag = 12 << 20, 303, `"p1"`
	srv := newServer(t, testserver.Options{Size: size, Seed: seed, ETag: etag, Throttle: 512 << 10})
	d := h.startDaemon()
	h.waitReady(d)

	id := h.add(srv.URL+"/paused.bin", "-o", h.out, "-c", "2")
	h.waitReceived(id, 1<<20)
	r := h.cli("pause", idArg(id)).ok(t)
	if !strings.Contains(r.stdout, fmt.Sprintf("Paused download %d", id)) {
		t.Errorf("pause: %v", r)
	}
	p := h.get(id)
	if p.GetState() != downexv1.DownloadState_DOWNLOAD_STATE_PAUSED || p.GetReceived() <= 0 || p.GetReceived() >= size {
		t.Fatalf("after pause: %v", p)
	}
	if fi, err := os.Stat(engine.PartPath(h.out, "paused.bin")); err != nil || fi.Size() != size {
		t.Fatalf("part file after pause: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	if again := h.get(id); again.GetReceived() != p.GetReceived() || again.GetState() != p.GetState() {
		t.Fatalf("a paused download changed: %v -> %v", p, again)
	}
	stored := h.info(id) // the database, read by the CLI
	if stored.State != "paused" || durable(stored.Segments) != p.GetReceived() {
		t.Fatalf("stored after pause: state %s, %d durable bytes; the daemon reports %d", stored.State, durable(stored.Segments), p.GetReceived())
	}
	if e := entry(t, h.ls(), id); e.State != "paused" {
		t.Errorf("ls after pause: %+v", e)
	}

	before := len(srv.Requests())
	srv.Update(func(o *testserver.Options) { o.Throttle = 0 })
	r = h.cli("resume", idArg(id), "--wait").ok(t)
	if !strings.Contains(r.stderr, "Saved ") {
		t.Errorf("resume --wait: %v", r)
	}
	checkFile(t, filepath.Join(h.out, "paused.bin"), seed, size)
	noPartFiles(t, h.out)
	checkResumed(t, srv.Requests()[before:], etag, stored.Segments)
	h.stop(d)
}

// A downexd killed hard mid-download leaves it "downloading" in the database; the
// next start requeues it and completes it from its committed progress.
func TestKilledDaemonRecovers(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	const size, seed, etag = 16 << 20, 404, `"k1"`
	srv := newServer(t, testserver.Options{Size: size, Seed: seed, ETag: etag, Throttle: 512 << 10})
	d1 := h.startDaemon()
	h.waitReady(d1)

	id := h.add(srv.URL+"/killed.bin", "-o", h.out, "-c", "2")
	// A pause commits progress (the engine otherwise flushes only every few seconds),
	// so the kill below happens with durable bytes on disk and bytes in flight.
	h.waitReceived(id, 1<<20)
	h.cli("pause", idArg(id)).ok(t)
	committed := h.get(id).GetReceived()
	h.cli("resume", idArg(id)).ok(t)
	h.waitReceived(id, committed+1<<20)
	d1.kill(t)
	if d1.code == 0 {
		t.Fatalf("killed downexd exited with 0: %v", d1)
	}

	// Without a daemon the CLI reads the database: the download is left as running.
	if r := h.cli("daemon", "status"); r.code != 1 || !strings.Contains(r.stdout, "not running") {
		t.Errorf("daemon status after the kill: %v", r)
	}
	if e := entry(t, h.ls(), id); e.State != "downloading" {
		t.Errorf("ls after the kill: %+v", e)
	}
	stored := h.info(id)
	if got := durable(stored.Segments); got < committed || got >= size {
		t.Fatalf("after the kill %d bytes are durable, want at least the %d committed by the pause", got, committed)
	}

	before := len(srv.Requests())
	srv.Update(func(o *testserver.Options) { o.Throttle = 0 })
	d2 := h.startDaemon()
	h.waitReady(d2)
	h.waitCompleted(id)
	checkFile(t, filepath.Join(h.out, "killed.bin"), seed, size)
	noPartFiles(t, h.out)
	checkResumed(t, srv.Requests()[before:], etag, stored.Segments)
	if log := h.daemonLog(); !strings.Contains(log, `"requeued":1`) {
		t.Errorf("the restarted daemon did not report requeueing the download:\n%s", log)
	}
	h.stop(d2)
}

// "downex daemon stop" pauses the running downloads with their progress saved and
// stores them as queued; the next start resumes them.
func TestStopPausesAndRestartResumes(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	const size, etag = 12 << 20, `"s1"`
	seeds := []uint64{505, 506}
	var srvs []*testserver.Server
	for _, seed := range seeds {
		srvs = append(srvs, newServer(t, testserver.Options{Size: size, Seed: seed, ETag: etag, Throttle: 512 << 10}))
	}
	d1 := h.startDaemon()
	h.waitReady(d1)
	var ids []int64
	for i, srv := range srvs {
		ids = append(ids, h.add(fmt.Sprintf("%s/stopped%d.bin", srv.URL, i), "-o", h.out, "-c", "2"))
	}
	for _, id := range ids {
		h.waitReceived(id, 512<<10)
	}
	h.stop(d1)
	log := h.daemonLog()
	if !strings.Contains(log, `"msg":"stopping on request"`) || !strings.Contains(log, `"pausing":2`) {
		t.Errorf("the daemon log does not show a shutdown pausing 2 downloads:\n%s", log)
	}

	list := h.ls() // read from the database: no daemon runs
	var stored []lsEntry
	for _, id := range ids {
		e := entry(t, list, id)
		info := h.info(id)
		if e.State != "queued" || e.Downloaded <= 0 || e.Downloaded >= size || durable(info.Segments) != e.Downloaded {
			t.Errorf("download %d after daemon stop: %+v (%d durable)", id, e, durable(info.Segments))
		}
		stored = append(stored, info)
	}

	var before []int
	for _, srv := range srvs {
		before = append(before, len(srv.Requests()))
		srv.Update(func(o *testserver.Options) { o.Throttle = 0 })
	}
	d2 := h.startDaemon()
	h.waitReady(d2)
	for i, id := range ids {
		h.waitCompleted(id)
		checkFile(t, filepath.Join(h.out, fmt.Sprintf("stopped%d.bin", i)), seeds[i], size)
		checkResumed(t, srvs[i].Requests()[before[i]:], etag, stored[i].Segments)
	}
	noPartFiles(t, h.out)
	h.stop(d2)
}

var startedPID = regexp.MustCompile(`Started downexd \(pid (\d+)\)`)

// A daemon command finds no daemon, starts downexd detached (next to downex, with
// the same database) and uses it.
func TestAutostart(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	const size, seed = 1<<20 + 3, 606
	srv := newServer(t, testserver.Options{Size: size, Seed: seed, ETag: `"a1"`})
	// Run the new executable once, so that a first-run delay (a virus scan, say) does
	// not count against the CLI's 3 s start timeout.
	h.run(bin.downexd, "--version").ok(t)

	if r := h.cli("daemon", "status"); r.code != 1 || !strings.Contains(r.stdout, "not running") {
		t.Fatalf("daemon status before autostart: %v", r)
	}
	r := h.cli("add", srv.URL+"/auto.bin", "-o", h.out, "--wait").ok(t)
	m := startedPID.FindStringSubmatch(r.stderr)
	if m == nil {
		t.Fatalf("add did not report starting downexd: %v", r)
	}
	pid, _ := strconv.Atoi(m[1])
	id := r.id(t)
	checkFile(t, filepath.Join(h.out, "auto.bin"), seed, size)

	st, err := h.status()
	if err != nil || int(st.GetPid()) != pid || st.GetEndpoint() != h.endpoint {
		t.Fatalf("status of the autostarted daemon = %v, %v (want pid %d on %s)", st, err, pid, h.endpoint)
	}
	// It logs into the per-user log directory of its environment.
	logPath := h.userLog("downexd.log")
	if b, err := os.ReadFile(logPath); err != nil || !strings.Contains(string(b), fmt.Sprintf(`"pid":%d`, pid)) {
		t.Errorf("autostarted daemon log %s: %v\n%s", logPath, err, b)
	}

	h.cli("daemon", "stop").ok(t)
	if r := h.cli("daemon", "status"); r.code != 1 {
		t.Errorf("daemon status after stop: %v", r)
	}
	// It used the CLI's database: without a daemon, ls reads the download from there.
	if e := entry(t, h.ls(), id); e.State != "completed" {
		t.Errorf("download in the CLI's database: %+v", e)
	}
	waitProcessGone(t, pid)
}

// waitProcessGone waits briefly for process pid to exit after its endpoint closed.
func waitProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(exitTimeout)
	for processAlive(pid) {
		if time.Now().After(deadline) {
			t.Errorf("downexd pid %d still runs after daemon stop", pid)
			return
		}
		time.Sleep(pollInterval)
	}
}
