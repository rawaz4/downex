package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"downex/internal/dlock"
	"downex/internal/dlview"
	"downex/internal/engine"
	"downex/internal/model"
	"downex/internal/store"
	"downex/internal/testserver"
)

// These tests drive the CLI end to end: the real HTTP transport, the SQLite store in
// a temporary directory and an httptest range server with injected faults.

type cliResult struct {
	code           int
	stdout, stderr string
}

func (r cliResult) String() string {
	return fmt.Sprintf("exit %d\nstdout:\n%s\nstderr:\n%s", r.code, r.stdout, r.stderr)
}

// runCLI runs one command line without a daemon: nothing listens on the endpoint and
// none can be started, so these tests never reach a real downexd.
func runCLI(ctx context.Context, args ...string) cliResult {
	return runWith(ctx, offlineSystem(), args...)
}

// env is one test's download directory and database.
type env struct {
	dir, db string
}

func newEnv(t *testing.T) env {
	t.Helper()
	return env{dir: t.TempDir(), db: filepath.Join(t.TempDir(), "downex.db")}
}

// get runs "downex get" for url into e.dir with extra flags.
func (e env) get(ctx context.Context, url string, extra ...string) cliResult {
	return runCLI(ctx, append([]string{"get", url, "-o", e.dir, "--db", e.db, "--proxy", "direct"}, extra...)...)
}

func (e env) cli(ctx context.Context, args ...string) cliResult {
	return runCLI(ctx, append(args, "--db", e.db)...)
}

// load reads download id and its segments from the test database.
func (e env) load(t *testing.T, id model.DownloadID) (*model.Download, []model.Segment) {
	t.Helper()
	st, err := store.OpenSQLite(e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	d, err := st.GetDownload(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	segs, err := st.LoadSegments(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return d, segs
}

func newServer(t *testing.T, o testserver.Options) *testserver.Server {
	t.Helper()
	s := testserver.New(o)
	t.Cleanup(s.Close)
	return s
}

func fileSum(t *testing.T, path string) [32]byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatal(err)
	}
	return [32]byte(h.Sum(nil))
}

// cancelAfterSent cancels the returned context once srv has sent at least n body bytes.
func cancelAfterSent(t *testing.T, srv *testserver.Server, n int64) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		for srv.BytesSent() < n {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
		}
		cancel()
	}()
	return ctx
}

func parseRange(t *testing.T, v string) (start, end int64) {
	t.Helper()
	spec, ok := strings.CutPrefix(v, "bytes=")
	a, b, ok2 := strings.Cut(spec, "-")
	start, err1 := strconv.ParseInt(a, 10, 64)
	end, err2 := strconv.ParseInt(b, 10, 64)
	if !ok || !ok2 || err1 != nil || err2 != nil {
		t.Fatalf("resumed request has range %q, want a bounded bytes=s-e", v)
	}
	return start, end
}

func noPartFiles(t *testing.T, dir string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "*.dxpart"))
	if err != nil || len(matches) > 0 {
		t.Errorf("leftover part files: %v %v", matches, err)
	}
}

func TestGetEightConnections(t *testing.T) {
	t.Parallel()
	const size = 32 << 20
	srv := newServer(t, testserver.Options{Size: size, Seed: 11, ETag: `"v1"`, Throttle: 4 << 20})
	e := newEnv(t)

	r := e.get(context.Background(), srv.URL+"/files/data.bin", "-c", "8")
	if r.code != exitOK {
		t.Fatalf("get failed: %v", r)
	}
	path := filepath.Join(e.dir, "data.bin")
	if fileSum(t, path) != srv.Sum256() {
		t.Fatal("downloaded file differs from the served content")
	}
	noPartFiles(t, e.dir)
	if c := srv.MaxConcurrent(); c < 2 || c > 8 {
		t.Errorf("server saw at most %d concurrent requests, want 2..8", c)
	}
	if !strings.Contains(r.stdout, "Saved "+path) || !strings.Contains(r.stderr, "Download 1:") {
		t.Errorf("unexpected output: %v", r)
	}
	d, _ := e.load(t, 1)
	if d.State != model.StateCompleted || d.Mode != model.ModeRanged || d.Size != size || d.MaxConnections != 8 {
		t.Errorf("stored download = %+v", d)
	}
	t.Logf("requests=%d max concurrent=%d sent=%d", len(srv.Requests()), srv.MaxConcurrent(), srv.BytesSent())
}

func TestInterruptThenResume(t *testing.T) {
	t.Parallel()
	const size = 16 << 20
	srv := newServer(t, testserver.Options{Size: size, Seed: 21, ETag: `"r1"`, Throttle: 1 << 20})
	e := newEnv(t)

	// Interrupt (as Ctrl+C does) once about a fifth of the file was served.
	r := e.get(cancelAfterSent(t, srv, 3<<20), srv.URL+"/big.iso", "-c", "8")
	if r.code != exitInterrupted {
		t.Fatalf("interrupted get: %v", r)
	}
	if !strings.Contains(r.stderr, "Paused download 1") || !strings.Contains(r.stderr, "downex resume 1 --local") {
		t.Errorf("no resume hint: %v", r)
	}
	d, segs := e.load(t, 1)
	saved := dlview.Received(d, segs)
	if d.State != model.StatePaused || saved <= 0 || saved >= size {
		t.Fatalf("after pause: state %v, %d of %d bytes saved", d.State, saved, size)
	}
	part := engine.PartPath(e.dir, "big.iso")
	if fi, err := os.Stat(part); err != nil || fi.Size() != size {
		t.Fatalf("part file: %v", err)
	}
	// Durable bytes are already correct on disk.
	data, err := os.ReadFile(part)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range segs {
		if !bytes.Equal(data[s.Start:s.Start+s.Durable], testserver.Bytes(21, s.Start, s.Durable)) {
			t.Fatalf("segment %+v: durable bytes are wrong on disk", s)
		}
	}

	sentBefore, reqsBefore := srv.BytesSent(), len(srv.Requests())
	srv.Update(func(o *testserver.Options) { o.Throttle = 4 << 20 })
	r = e.cli(context.Background(), "resume", "1", "-c", "4", "--local")
	if r.code != exitOK {
		t.Fatalf("resume failed: %v", r)
	}
	if d, _ := e.load(t, 1); d.MaxConnections != 4 {
		t.Errorf("resume -c 4 stored %d connections", d.MaxConnections)
	}
	if fileSum(t, filepath.Join(e.dir, "big.iso")) != srv.Sum256() {
		t.Fatal("resumed file differs from the served content")
	}
	noPartFiles(t, e.dir)

	// No durable range is requested again, and every request is conditional.
	resumed := srv.Requests()[reqsBefore:]
	if len(resumed) == 0 {
		t.Fatal("resume made no requests")
	}
	for _, q := range resumed {
		if q.IfRange != `"r1"` {
			t.Errorf("resumed request without If-Range: %+v", q)
		}
		a, b := parseRange(t, q.Range)
		for _, s := range segs {
			if s.Durable > 0 && a <= s.Start+s.Durable-1 && b >= s.Start {
				t.Errorf("range %d-%d re-downloads durable bytes %d-%d", a, b, s.Start, s.Start+s.Durable-1)
			}
		}
	}
	// Bytes served overall: the file once, plus what was in flight at the interrupt
	// (at most one read slice per connection) and the drained tails of split responses.
	const slack = 8 * 320 << 10
	total := srv.BytesSent()
	t.Logf("saved at pause %d, served before resume %d, total %d (size %d, %d resumed requests)",
		saved, sentBefore, total, size, len(resumed))
	if total > size+slack {
		t.Errorf("server sent %d bytes for a %d-byte file (slack %d)", total, size, slack)
	}
	if got := srv.BytesSent() - sentBefore; got > size-saved+slack {
		t.Errorf("resume fetched %d bytes, but only %d were missing", got, size-saved)
	}
}

func TestServerWithoutRanges(t *testing.T) {
	t.Parallel()
	const size = 3<<20 + 123
	srv := newServer(t, testserver.Options{Size: size, Seed: 5, NoRanges: true})
	e := newEnv(t)

	r := e.get(context.Background(), srv.URL+"/plain.bin", "-c", "8")
	if r.code != exitOK {
		t.Fatalf("get failed: %v", r)
	}
	if fileSum(t, filepath.Join(e.dir, "plain.bin")) != srv.Sum256() {
		t.Fatal("downloaded file differs from the served content")
	}
	if n := len(srv.Requests()); n != 1 {
		t.Errorf("%d requests, want 1 (single connection)", n)
	}
	if !strings.Contains(r.stderr, "warning:") || !strings.Contains(r.stderr, "byte ranges") {
		t.Errorf("no single-connection warning: %v", r)
	}
	if d, _ := e.load(t, 1); d.Mode != model.ModeSingle || d.State != model.StateCompleted {
		t.Errorf("stored mode %v state %v", d.Mode, d.State)
	}
}

func TestRemoteChangeRequiresRestart(t *testing.T) {
	t.Parallel()
	const size = 8 << 20
	srv := newServer(t, testserver.Options{Size: size, Seed: 31, ETag: `"a"`, Throttle: 1 << 20})
	e := newEnv(t)

	r := e.get(cancelAfterSent(t, srv, 1536<<10), srv.URL+"/changing.zip")
	if r.code != exitInterrupted {
		t.Fatalf("interrupted get: %v", r)
	}
	srv.Update(func(o *testserver.Options) { o.ETag, o.Seed, o.Throttle = `"b"`, 32, 0 })

	r = e.cli(context.Background(), "resume", "1", "--local")
	if r.code != exitError {
		t.Fatalf("resume of a changed file: %v", r)
	}
	if !strings.Contains(r.stderr, "changed") || !strings.Contains(r.stderr, "downex resume 1 --restart --local") {
		t.Errorf("no explanation or --restart hint: %v", r)
	}
	d, segs := e.load(t, 1)
	if d.State != model.StateFailed || dlview.Received(d, segs) == 0 {
		t.Errorf("after the change: state %v, %d bytes kept", d.State, dlview.Received(d, segs))
	}

	r = e.cli(context.Background(), "resume", "1", "--restart", "--local")
	if r.code != exitOK {
		t.Fatalf("resume --restart: %v", r)
	}
	if fileSum(t, filepath.Join(e.dir, "changing.zip")) != testserver.Sum256(32, size) {
		t.Fatal("restarted download does not match the new content")
	}
	noPartFiles(t, e.dir)
}

func TestManageDownloads(t *testing.T) {
	t.Parallel()
	srv := newServer(t, testserver.Options{Size: 200_000, Seed: 51, ETag: `"m"`, Filename: "report final.pdf"})
	e := newEnv(t)
	ctx := context.Background()
	for range 2 {
		if r := e.get(ctx, srv.URL+"/dl?id=7", "-H", "Cookie: session=secret", "-H", "Referer: https://example.test/"); r.code != exitOK {
			t.Fatalf("get: %v", r)
		}
	}
	first, second := filepath.Join(e.dir, "report final.pdf"), filepath.Join(e.dir, "report final (1).pdf")
	for _, p := range []string{first, second} {
		if fileSum(t, p) != srv.Sum256() {
			t.Fatalf("%s differs from the served content", p)
		}
	}

	r := e.cli(ctx, "ls")
	if r.code != exitOK || !strings.Contains(r.stdout, "report final (1).pdf") || !strings.Contains(r.stdout, "100.0%") {
		t.Fatalf("ls: %v", r)
	}
	r = e.cli(ctx, "ls", "--json")
	var list []downloadView
	if err := json.Unmarshal([]byte(r.stdout), &list); err != nil || len(list) != 2 {
		t.Fatalf("ls --json: %v %v", err, r)
	}
	if v := list[0]; v.ID != 1 || v.State != "completed" || v.Downloaded != 200_000 || v.Path != first ||
		v.Headers.Get("Cookie") != "[redacted]" || v.Headers.Get("Referer") != "https://example.test/" {
		t.Errorf("ls --json entry: %+v", v)
	}
	if strings.Contains(r.stdout, "secret") {
		t.Error("ls --json prints the cookie value")
	}

	r = e.cli(ctx, "info", "2", "--json")
	var info infoView
	if err := json.Unmarshal([]byte(r.stdout), &info); err != nil || r.code != exitOK {
		t.Fatalf("info --json: %v %v", err, r)
	}
	var durable int64
	for _, s := range info.Segments {
		durable += s.Durable
	}
	if info.Filename != "report final (1).pdf" || durable != 200_000 || info.Validators.ETag != `"m"` {
		t.Errorf("info --json: %+v", info)
	}
	r = e.cli(ctx, "info", "2")
	if r.code != exitOK || !strings.Contains(r.stdout, "Segments (") || strings.Contains(r.stdout, "secret") {
		t.Errorf("info: %v", r)
	}

	// rm keeps the file unless --delete-file is given.
	if r = e.cli(ctx, "rm", "1", "--local"); r.code != exitOK || !strings.Contains(r.stdout, "kept") {
		t.Fatalf("rm: %v", r)
	}
	if _, err := os.Stat(first); err != nil {
		t.Errorf("rm without --delete-file removed the file: %v", err)
	}
	if r = e.cli(ctx, "rm", "2", "--delete-file", "--local"); r.code != exitOK {
		t.Fatalf("rm --delete-file: %v", r)
	}
	if _, err := os.Stat(second); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("rm --delete-file kept the file: %v", err)
	}
	if r = e.cli(ctx, "info", "2"); r.code != exitError || !strings.Contains(r.stderr, "no download with id 2") {
		t.Errorf("info after rm: %v", r)
	}
	if r = e.cli(ctx, "ls"); !strings.Contains(r.stdout, "No downloads.") {
		t.Errorf("ls after rm: %v", r)
	}
}

func TestRunningDownloadIsLocked(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	st, path, err := openStore(e.db)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	lock, err := dlock.Acquire(dlock.Dir(path), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	for _, args := range [][]string{{"resume", "1", "--local"}, {"rm", "1", "--local"}} {
		if r := e.cli(context.Background(), args...); r.code != exitError || !strings.Contains(r.stderr, dlock.ErrBusy.Error()) {
			t.Errorf("%v while locked: %v", args, r)
		}
	}
	lock.Release()
	// Once released, the CLI gets past the lock (and reports the missing download).
	if r := e.cli(context.Background(), "rm", "1", "--local"); r.code != exitError || strings.Contains(r.stderr, dlock.ErrBusy.Error()) {
		t.Errorf("rm after release: %v", r)
	}
}

// TestSpeedLimit runs alone (not in parallel) so that timing is not skewed.
func TestSpeedLimit(t *testing.T) {
	const (
		size  = 1536 << 10
		limit = 1 << 20
	)
	srv := newServer(t, testserver.Options{Size: size, Seed: 61, ETag: `"s"`})
	e := newEnv(t)
	start := time.Now()
	r := e.get(context.Background(), srv.URL+"/limited.bin", "--limit", "1MiB", "-c", "8") // binary, as before 2026-10-31
	elapsed := time.Since(start)
	if r.code != exitOK {
		t.Fatalf("get failed: %v", r)
	}
	if fileSum(t, filepath.Join(e.dir, "limited.bin")) != srv.Sum256() {
		t.Fatal("downloaded file differs from the served content")
	}
	rate := float64(size) / elapsed.Seconds()
	t.Logf("%d bytes in %v: %.0f B/s (limit %d B/s)", size, elapsed, rate, limit)
	if rate > 1.25*limit || rate < 0.75*limit {
		t.Errorf("average rate %.0f B/s is not within 25%% of the %d B/s limit", rate, limit)
	}
	if d, _ := e.load(t, 1); d.SpeedLimit != limit {
		t.Errorf("stored speed limit %d", d.SpeedLimit)
	}
}

func TestGetRestartsWhenFileChangesMidway(t *testing.T) {
	t.Parallel()
	const size = 8 << 20
	srv := newServer(t, testserver.Options{Size: size, Seed: 71, ETag: `"old"`, Throttle: 1 << 20})
	e := newEnv(t)
	// Replace the file while the first connection streams it: the next connection's
	// If-Range no longer matches. Ramp-up opens the other connections on the first
	// tick after the probe's first bytes (ENGINE-SPEC §3), so the file changes as soon
	// as the probe was answered.
	go func() {
		for len(srv.Requests()) == 0 {
			select {
			case <-t.Context().Done():
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
		srv.Update(func(o *testserver.Options) { o.ETag, o.Seed, o.Throttle = `"new"`, 72, 0 })
	}()

	r := e.get(context.Background(), srv.URL+"/moving.tar", "--restart")
	if r.code != exitOK {
		t.Fatalf("get --restart: %v", r)
	}
	if !strings.Contains(r.stderr, "changed; downloading 1 again from the beginning") {
		t.Errorf("no restart notice: %v", r)
	}
	if fileSum(t, filepath.Join(e.dir, "moving.tar")) != testserver.Sum256(72, size) {
		t.Fatal("file does not match the new content")
	}
	noPartFiles(t, e.dir)
}
