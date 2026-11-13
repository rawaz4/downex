package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"downex/internal/ipc"
	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/rpc/downex/v1/downexv1connect"
	"downex/internal/testserver"
)

// Timeouts. Generous, because under -race every process is several times slower.
const (
	cliTimeout      = 2 * time.Minute  // one downex invocation
	readyTimeout    = 30 * time.Second // a started downexd answers Status
	exitTimeout     = 30 * time.Second // a stopped downexd exits
	progressTimeout = 30 * time.Second // a running download reaches some progress
	completeTimeout = 90 * time.Second // a download completes
	rpcCallTimeout  = 10 * time.Second // one RPC made by the test itself
	pollInterval    = 20 * time.Millisecond
)

// bin holds the executables built by TestMain.
var bin struct {
	downex, downexd, nmhost string
	// downexdHooks is downexd built with -tags e2etest (its test hooks: clock, power, root
	// certificates, update key; cmd/downexd/testhooks_e2e.go) and version hooksVersion.
	downexdHooks string
	err          error // why they could not be built
}

// hooksVersion is the version of bin.downexdHooks (a release version: update checks
// need one).
const hooksVersion = "1.0.0"

func TestMain(m *testing.M) {
	// Installed as yt-dlp or deno, this executable stands in for that tool
	// (site_test.go). It is told by its name, before the variables below: downexd passes
	// every DOWNEX_E2E_* variable on to the tools it runs.
	if code, ok := fakeTool(); ok {
		os.Exit(code)
	}
	// Copied next to downexd as downex-ui, this executable stands in for the desktop
	// window (desktop_test.go); the test flags are not parsed then.
	if record := os.Getenv(envFakeUI); record != "" {
		os.Exit(fakeUI(record, os.Args[1:]))
	}
	// Started as the console probe of the Windows tests (console_windows_test.go).
	if code, ok := consoleProbe(); ok {
		os.Exit(code)
	}
	flag.Parse()
	limitRaceParallelism()
	os.Exit(runTests(m))
}

// raceParallel is how many tests run at once under the race detector unless
// -test.parallel is given. Every test starts race-instrumented executables (downexd, the
// CLI, the relay, the stand-in window), and each such process commits well over 100 MiB
// on Windows. With one test per CPU, next to the other packages' test binaries, a
// "go test -race ./..." used up the system's commit limit: a child process then died
// with error 1455 ("ThreadSanitizer failed to allocate"), failing whichever test it
// belonged to (seen as "read from downex-nmhost: EOF" in TestNativeMessagingAdd, or
// "downexd exited with 66 before it answered").
const raceParallel = 8

// limitRaceParallelism lowers the default -test.parallel to raceParallel under the race
// detector.
func limitRaceParallelism() {
	if !raceEnabled {
		return
	}
	explicit := false
	flag.Visit(func(f *flag.Flag) { explicit = explicit || f.Name == "test.parallel" })
	if !explicit && runtime.GOMAXPROCS(0) > raceParallel {
		_ = flag.Set("test.parallel", strconv.Itoa(raceParallel))
	}
}

func runTests(m *testing.M) int {
	if testing.Short() {
		return m.Run() // every test skips itself
	}
	dir, err := os.MkdirTemp("", "downex-e2e-bin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e:", err)
		return 1
	}
	defer os.RemoveAll(dir)
	bin.err = build(dir)
	return m.Run()
}

// build compiles downex, downexd and downex-nmhost into dir: pure Go like the product,
// or with the race detector (which needs cgo) when the tests run with it.
func build(dir string) error {
	gotool, err := exec.LookPath("go")
	if err != nil {
		return fmt.Errorf("find the go command: %w", err)
	}
	args := []string{"build", "-o", dir + string(os.PathSeparator)}
	cgo := "CGO_ENABLED=0"
	if raceEnabled {
		args, cgo = append(args, "-race"), "CGO_ENABLED=1"
	}
	args = append(args, "downex/cmd/downex", "downex/cmd/downexd", "downex/cmd/downex-nmhost")
	cmd := exec.Command(gotool, args...)
	cmd.Env = append(os.Environ(), cgo)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("go %s: %w\n%s", strings.Join(args, " "), err, out)
	}
	bin.downex = filepath.Join(dir, exeName("downex"))
	bin.downexd = filepath.Join(dir, exeName("downexd"))
	bin.nmhost = filepath.Join(dir, exeName("downex-nmhost"))

	hooks := filepath.Join(dir, "hooks")
	args = []string{"build", "-tags", "e2etest", "-ldflags", "-X main.version=" + hooksVersion, "-o", hooks + string(os.PathSeparator)}
	if raceEnabled {
		args = append(args, "-race")
	}
	args = append(args, "downex/cmd/downexd")
	cmd = exec.Command(gotool, args...)
	cmd.Env = append(os.Environ(), cgo)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("go %s: %w\n%s", strings.Join(args, " "), err, out)
	}
	bin.downexdHooks = filepath.Join(hooks, exeName("downexd"))
	return nil
}

func exeName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// harness is one test's isolated installation: a private endpoint, database,
// download directory and per-user state directory for every process it starts.
type harness struct {
	t        *testing.T
	root     string // working directory of the child processes
	db       string
	out      string // download directory
	endpoint string
	logDir   string // --log-dir of the daemons the test starts itself
	userDir  string // LOCALAPPDATA / XDG_STATE_HOME / HOME of the child processes
	raceDir  string // race detector reports of the child processes
	env      []string
	dl       downexv1connect.DownloadServiceClient
	dm       downexv1connect.DaemonServiceClient
	dq       downexv1connect.QueueServiceClient
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	if testing.Short() {
		t.Skip("end-to-end test: builds and runs the downex and downexd executables")
	}
	if bin.err != nil {
		t.Fatalf("cannot build the executables: %v", bin.err)
	}
	root := t.TempDir()
	h := &harness{
		t: t, root: root,
		db:       filepath.Join(root, "state", "downex.db"),
		out:      filepath.Join(root, "out"),
		logDir:   filepath.Join(root, "logs"),
		userDir:  filepath.Join(root, "user"),
		raceDir:  filepath.Join(root, "race"),
		endpoint: newEndpoint(t),
	}
	for _, d := range []string{h.out, h.userDir, h.raceDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	overrides := map[string]string{
		ipc.EnvEndpoint: h.endpoint,
		"DOWNEX_DAEMON": "", // autostart must find downexd next to downex
		// Keep autostarted daemons' logs (and anything else per user) in the test.
		"LOCALAPPDATA":   h.userDir,
		"XDG_STATE_HOME": h.userDir,
		"HOME":           h.userDir, // macOS keeps them below ~/Library
		"GORACE":         "",
	}
	if raceEnabled && !strings.Contains(h.raceDir, "'") {
		// Reports go to files, so that races in detached daemons are seen too. The
		// value is quoted: the option parser splits at ':' (as in C:\) and spaces.
		overrides["GORACE"] = "log_path='" + filepath.Join(h.raceDir, "report") + "'"
	}
	h.env = childEnv(overrides)
	hc := ipc.NewHTTPClient(h.endpoint)
	h.dl = downexv1connect.NewDownloadServiceClient(hc, ipc.BaseURL)
	h.dm = downexv1connect.NewDaemonServiceClient(hc, ipc.BaseURL)
	h.dq = downexv1connect.NewQueueServiceClient(hc, ipc.BaseURL)
	t.Cleanup(func() {
		h.stopLeftoverDaemon()
		hc.CloseIdleConnections()
		h.checkRaceReports()
	})
	return h
}

// newEndpoint returns a private endpoint: a pipe with a random name on Windows, a
// socket in a new short directory (sun_path is limited to 108 bytes) elsewhere.
func newEndpoint(t *testing.T) string {
	t.Helper()
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	if runtime.GOOS == "windows" {
		return `\\.\pipe\downex-e2e-` + hex.EncodeToString(b)
	}
	dir, err := os.MkdirTemp("", "dxe2e-") // mode 0700, as ipc requires
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "rpc.sock")
}

// childEnv returns this process's environment with overrides applied; an empty value
// removes the variable.
func childEnv(overrides map[string]string) []string {
	key := func(k string) string {
		if runtime.GOOS == "windows" { // environment names are case-insensitive there
			return strings.ToUpper(k)
		}
		return k
	}
	replaced := make(map[string]bool, len(overrides))
	for k := range overrides {
		replaced[key(k)] = true
	}
	var env []string
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); !replaced[key(k)] {
			env = append(env, kv)
		}
	}
	for k, v := range overrides {
		if v != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// ---- processes ---------------------------------------------------------------------

// syncBuffer is a bytes.Buffer that is safe for concurrent use.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// daemonProc is a downexd process started by the test.
type daemonProc struct {
	cmd            *exec.Cmd
	stdout, stderr syncBuffer
	done           chan struct{} // closed when the process has exited
	code           int           // exit code, valid after done
}

func (p *daemonProc) pid() int { return p.cmd.Process.Pid }

func (p *daemonProc) exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

func (p *daemonProc) String() string {
	return fmt.Sprintf("downexd pid %d\nstdout:\n%s\nstderr:\n%s", p.pid(), p.stdout.String(), p.stderr.String())
}

// startDaemon starts downexd on the harness's endpoint (from DOWNEX_ENDPOINT) and
// database. The process is killed at the end of the test if it still runs.
func (h *harness) startDaemon() *daemonProc {
	h.t.Helper()
	return h.startDaemonFrom(bin.downexd, h.env)
}

// startDaemonFrom starts the downexd executable exe like startDaemon, with environment
// env.
func (h *harness) startDaemonFrom(exe string, env []string) *daemonProc {
	h.t.Helper()
	p := &daemonProc{done: make(chan struct{})}
	p.cmd = exec.Command(exe, "--db", h.db, "--log-dir", h.logDir, "--log-level", "debug")
	p.cmd.Env, p.cmd.Dir = env, h.root
	p.cmd.Stdout, p.cmd.Stderr = &p.stdout, &p.stderr
	if err := p.cmd.Start(); err != nil {
		h.t.Fatalf("start downexd: %v", err)
	}
	go func() {
		_ = p.cmd.Wait()
		p.code = p.cmd.ProcessState.ExitCode()
		close(p.done)
	}()
	h.t.Cleanup(func() {
		if !p.exited() {
			p.kill(h.t)
		}
		if strings.Contains(p.stderr.String(), "WARNING: DATA RACE") {
			h.t.Errorf("data race in downexd:\n%s", p.stderr.String())
		}
	})
	return p
}

// kill terminates p at once (no graceful shutdown) and waits for it to exit.
func (p *daemonProc) kill(t *testing.T) {
	t.Helper()
	if err := p.cmd.Process.Kill(); err != nil && !p.exited() {
		t.Errorf("kill downexd: %v", err)
	}
	select {
	case <-p.done:
	case <-time.After(exitTimeout):
		t.Errorf("downexd pid %d did not exit after being killed", p.pid())
	}
}

// waitExit waits for p to exit and returns its exit code.
func (h *harness) waitExit(p *daemonProc) int {
	h.t.Helper()
	select {
	case <-p.done:
		return p.code
	case <-time.After(exitTimeout):
		h.t.Fatalf("downexd did not exit within %v: %v", exitTimeout, p)
		return -1
	}
}

// waitReady waits until p answers Status on the harness's endpoint.
func (h *harness) waitReady(p *daemonProc) *downexv1.StatusResponse {
	h.t.Helper()
	deadline := time.Now().Add(readyTimeout)
	for {
		st, err := h.status()
		if err == nil {
			if int(st.GetPid()) != p.pid() {
				h.t.Fatalf("the endpoint is served by pid %d, not by the started downexd: %v", st.GetPid(), p)
			}
			return st
		}
		if p.exited() {
			h.t.Fatalf("downexd exited with %d before it answered: %v", p.code, p)
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("downexd did not answer within %v (%v): %v", readyTimeout, err, p)
		}
		time.Sleep(pollInterval)
	}
}

// stopLeftoverDaemon stops a daemon that still answers on the endpoint at the end
// of the test (an autostarted one, or after a failure): gracefully, else by killing
// the process that answered there.
func (h *harness) stopLeftoverDaemon() {
	st, err := h.status()
	if err != nil {
		return
	}
	pid := int(st.GetPid())
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, _ = h.dm.Shutdown(ctx, connect.NewRequest(&downexv1.ShutdownRequest{}))
	for ctx.Err() == nil {
		if _, err := h.status(); err != nil {
			h.t.Logf("stopped downexd pid %d left running by the test", pid)
			return
		}
		time.Sleep(pollInterval)
	}
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
	h.t.Errorf("downexd pid %d still answered after Shutdown and was killed", pid)
}

// checkRaceReports fails the test when a child process reported a data race.
func (h *harness) checkRaceReports() {
	files, _ := filepath.Glob(filepath.Join(h.raceDir, "*"))
	for _, f := range files {
		if b, err := os.ReadFile(f); err == nil && len(bytes.TrimSpace(b)) > 0 {
			h.t.Errorf("race detector report %s:\n%s", filepath.Base(f), b)
		}
	}
}

// result is the outcome of one command.
type result struct {
	args           []string
	code           int
	stdout, stderr string
}

func (r result) String() string {
	return fmt.Sprintf("%q: exit %d\nstdout:\n%s\nstderr:\n%s", r.args, r.code, r.stdout, r.stderr)
}

// ok fails the test unless the command exited with 0.
func (r result) ok(t *testing.T) result {
	t.Helper()
	if r.code != 0 {
		t.Fatalf("command failed: %v", r)
	}
	return r
}

// id parses the download id that "downex add" prints as its only stdout line.
func (r result) id(t *testing.T) int64 {
	t.Helper()
	id, err := strconv.ParseInt(strings.TrimSpace(r.stdout), 10, 64)
	if err != nil || id <= 0 {
		t.Fatalf("no download id on stdout: %v", r)
	}
	return id
}

// cli runs the downex executable with --db of the harness and args.
func (h *harness) cli(args ...string) result {
	h.t.Helper()
	return h.run(bin.downex, append([]string{"--db", h.db}, args...)...)
}

// run runs exe with args in the harness's environment and waits for it.
func (h *harness) run(exe string, args ...string) result {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), cliTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Env, cmd.Dir = h.env, h.root
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	// A process it started (an autostarted daemon) must not keep its output open.
	cmd.WaitDelay = 10 * time.Second
	err := cmd.Run()
	r := result{args: append([]string{filepath.Base(exe)}, args...), stdout: stdout.String(), stderr: stderr.String()}
	var ee *exec.ExitError
	switch {
	case ctx.Err() != nil:
		h.t.Fatalf("command did not finish within %v: %v", cliTimeout, r)
	case errors.Is(err, exec.ErrWaitDelay):
		h.t.Fatalf("a child process kept the command's output open: %v", r)
	case errors.As(err, &ee):
		r.code = ee.ExitCode()
	case err != nil:
		h.t.Fatalf("run %v: %v", r.args, err)
	}
	if strings.Contains(r.stderr, "WARNING: DATA RACE") {
		h.t.Errorf("data race: %v", r)
	}
	return r
}

// ---- CLI views ---------------------------------------------------------------------

// lsEntry is the part of "downex ls --json" / "info --json" the tests read.
type lsEntry struct {
	ID            int64           `json:"id"`
	State         string          `json:"state"`
	Dir           string          `json:"dir"`
	Filename      string          `json:"filename"`
	Path          string          `json:"path"`
	Size          int64           `json:"size"`
	Downloaded    int64           `json:"downloaded"`
	Error         string          `json:"error"`
	QueuePosition int             `json:"queuePosition"`
	Segments      []model.Segment `json:"segments"` // info only
}

// ls runs "downex ls --json".
func (h *harness) ls() []lsEntry {
	h.t.Helper()
	r := h.cli("ls", "--json").ok(h.t)
	var list []lsEntry
	if err := json.Unmarshal([]byte(r.stdout), &list); err != nil {
		h.t.Fatalf("ls --json: %v: %v", err, r)
	}
	return list
}

// info runs "downex info <id> --json", which reads the database directly.
func (h *harness) info(id int64) lsEntry {
	h.t.Helper()
	r := h.cli("info", strconv.FormatInt(id, 10), "--json").ok(h.t)
	var v lsEntry
	if err := json.Unmarshal([]byte(r.stdout), &v); err != nil {
		h.t.Fatalf("info --json: %v: %v", err, r)
	}
	return v
}

func durable(segs []model.Segment) int64 {
	var n int64
	for _, s := range segs {
		n += s.Durable
	}
	return n
}

func entry(t *testing.T, list []lsEntry, id int64) lsEntry {
	t.Helper()
	for _, e := range list {
		if e.ID == id {
			return e
		}
	}
	t.Fatalf("download %d is not listed: %+v", id, list)
	return lsEntry{}
}

// ---- daemon RPCs (for polling) -------------------------------------------------------

func (h *harness) status() (*downexv1.StatusResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	res, err := h.dm.Status(ctx, connect.NewRequest(&downexv1.StatusRequest{}))
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
}

func (h *harness) get(id int64) *downexv1.Download {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), rpcCallTimeout)
	defer cancel()
	res, err := h.dl.Get(ctx, connect.NewRequest(&downexv1.GetRequest{Id: id}))
	if err != nil {
		h.t.Fatalf("get download %d: %v", id, err)
	}
	return res.Msg.GetDownload()
}

func (h *harness) list() ([]*downexv1.Download, error) {
	ctx, cancel := context.WithTimeout(context.Background(), rpcCallTimeout)
	defer cancel()
	res, err := h.dl.List(ctx, connect.NewRequest(&downexv1.ListRequest{}))
	if err != nil {
		return nil, err
	}
	return res.Msg.GetDownloads(), nil
}

func running(s downexv1.DownloadState) bool {
	return s == downexv1.DownloadState_DOWNLOAD_STATE_PROBING || s == downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING
}

// waitReceived waits until download id has received at least n bytes.
func (h *harness) waitReceived(id, n int64) *downexv1.Download {
	h.t.Helper()
	deadline := time.Now().Add(progressTimeout)
	for {
		d := h.get(id)
		switch {
		case d.GetReceived() >= n && running(d.GetState()):
			return d
		case !running(d.GetState()) && d.GetState() != downexv1.DownloadState_DOWNLOAD_STATE_QUEUED:
			h.t.Fatalf("download %d stopped before %d bytes: %v", id, n, d)
		case time.Now().After(deadline):
			h.t.Fatalf("download %d did not reach %d bytes within %v: %v", id, n, progressTimeout, d)
		}
		time.Sleep(pollInterval)
	}
}

// waitCompleted waits until download id is completed.
func (h *harness) waitCompleted(id int64) *downexv1.Download {
	h.t.Helper()
	deadline := time.Now().Add(completeTimeout)
	for {
		d := h.get(id)
		switch d.GetState() {
		case downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED:
			return d
		case downexv1.DownloadState_DOWNLOAD_STATE_FAILED, downexv1.DownloadState_DOWNLOAD_STATE_PAUSED:
			h.t.Fatalf("download %d stopped: %v", id, d)
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("download %d did not complete within %v: %v", id, completeTimeout, d)
		}
		time.Sleep(pollInterval)
	}
}

// stop runs "downex daemon stop" and waits for p to exit with 0.
func (h *harness) stop(p *daemonProc) {
	h.t.Helper()
	r := h.cli("daemon", "stop").ok(h.t)
	if !strings.Contains(r.stdout, "Stopped downexd.") {
		h.t.Errorf("daemon stop: %v", r)
	}
	if code := h.waitExit(p); code != 0 {
		h.t.Fatalf("downexd exited with %d after daemon stop: %v", code, p)
	}
}

// daemonLog returns the log of the daemons the test started itself.
func (h *harness) daemonLog() string {
	h.t.Helper()
	b, err := os.ReadFile(filepath.Join(h.logDir, "downexd.log"))
	if err != nil {
		h.t.Fatalf("read the daemon log: %v", err)
	}
	return string(b)
}

// ---- content checks ----------------------------------------------------------------

func newServer(t *testing.T, o testserver.Options) *testserver.Server {
	t.Helper()
	s := testserver.New(o)
	t.Cleanup(s.Close)
	return s
}

// checkFile verifies that path holds the test server's content for seed and size.
func checkFile(t *testing.T, path string, seed uint64, size int64) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("downloaded file: %v", err)
	}
	defer f.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, f)
	if err != nil {
		t.Fatal(err)
	}
	if n != size || [32]byte(hash.Sum(nil)) != testserver.Sum256(seed, size) {
		t.Fatalf("%s: %d bytes, SHA-256 differs from the served content (%d bytes)", path, n, size)
	}
}

func noPartFiles(t *testing.T, dir string) {
	t.Helper()
	if m, _ := filepath.Glob(filepath.Join(dir, "*.dxpart")); len(m) > 0 {
		t.Errorf("leftover part files: %v", m)
	}
}

// checkResumed verifies the requests a resumed download made: each one is
// conditional on etag (If-Range) and none asks again for bytes that were durable in
// segs when it stopped.
func checkResumed(t *testing.T, reqs []testserver.Request, etag string, segs []model.Segment) {
	t.Helper()
	if len(reqs) == 0 {
		t.Fatal("the resumed download made no requests")
	}
	for _, q := range reqs {
		if q.IfRange != etag {
			t.Errorf("resumed request without If-Range %s: %+v", etag, q)
		}
		a, b, ok := parseRange(q.Range)
		if !ok {
			t.Errorf("resumed request without a byte range: %+v", q)
			continue
		}
		for _, s := range segs {
			if s.Durable > 0 && a <= s.Start+s.Durable-1 && b >= s.Start {
				t.Errorf("range %d-%d downloads durable bytes %d-%d again", a, b, s.Start, s.Start+s.Durable-1)
			}
		}
	}
}

// parseRange parses "bytes=a-b" or "bytes=a-".
func parseRange(v string) (start, end int64, ok bool) {
	spec, found := strings.CutPrefix(v, "bytes=")
	first, last, found2 := strings.Cut(spec, "-")
	if !found || !found2 {
		return 0, 0, false
	}
	start, err := strconv.ParseInt(first, 10, 64)
	if err != nil {
		return 0, 0, false
	}
	if last == "" {
		return start, math.MaxInt64, true
	}
	end, err = strconv.ParseInt(last, 10, 64)
	return start, end, err == nil
}

// userLog is where a process started without --log-dir writes its log name under
// the harness's per-user directory: %LOCALAPPDATA%\Downex\logs on Windows,
// ~/Library/Logs/Downex on macOS, $XDG_STATE_HOME/downex elsewhere.
func (h *harness) userLog(name string) string {
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(h.userDir, "Downex", "logs", name)
	case "darwin":
		return filepath.Join(h.userDir, "Library", "Logs", "Downex", name)
	}
	return filepath.Join(h.userDir, "downex", name)
}
