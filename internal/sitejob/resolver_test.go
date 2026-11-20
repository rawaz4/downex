package sitejob

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"downex/internal/media"
	"downex/internal/sitemedia"
	"downex/internal/tools"
	"downex/internal/validate"
)

// The test binary doubles as yt-dlp: started with SITEJOB_STANDIN in its environment
// (which the fake tools manager's Env adds), it records how it was started in
// $SITEJOB_DIR/runs/ and then acts out $SITEJOB_MODE.
func TestMain(m *testing.M) {
	if os.Getenv("SITEJOB_STANDIN") != "" {
		os.Exit(standIn())
	}
	os.Exit(m.Run())
}

// runRecord is how the stand-in was started.
type runRecord struct {
	Exe        string   `json:"exe"`
	Args       []string `json:"args"`
	Env        []string `json:"env"`
	Dir        string   `json:"dir"`
	Cookies    string   `json:"cookies"`
	CookieMode uint32   `json:"cookieMode"`
}

func standIn() int {
	dir := os.Getenv("SITEJOB_DIR")
	wd, _ := os.Getwd()
	rec := runRecord{Exe: os.Args[0], Args: os.Args[1:], Env: os.Environ(), Dir: wd}
	if i := slices.Index(rec.Args, "--cookies"); i >= 0 && i+1 < len(rec.Args) {
		if data, err := os.ReadFile(rec.Args[i+1]); err == nil {
			rec.Cookies = string(data)
		}
		if fi, err := os.Stat(rec.Args[i+1]); err == nil {
			rec.CookieMode = uint32(fi.Mode().Perm())
		}
	}
	data, _ := json.Marshal(rec)
	name := fmt.Sprintf("%020d-%d.json", time.Now().UnixNano(), os.Getpid())
	_ = os.WriteFile(filepath.Join(dir, "runs", name), data, 0o600)
	output := func() int {
		out, _ := os.ReadFile(filepath.Join(dir, "out.json"))
		_, _ = os.Stdout.Write(out)
		return 0
	}
	fail := func() int {
		e, _ := os.ReadFile(filepath.Join(dir, "stderr.txt"))
		_, _ = os.Stderr.Write(e)
		n, _ := strconv.Atoi(os.Getenv("SITEJOB_EXIT"))
		return n
	}
	waitRelease := func() {
		for start := time.Now(); time.Since(start) < 30*time.Second; time.Sleep(10 * time.Millisecond) {
			if _, err := os.Stat(filepath.Join(dir, "release")); err == nil {
				break
			}
		}
	}
	switch os.Getenv("SITEJOB_MODE") {
	case "json":
		return output()
	case "json-stderr": // writes stderr.txt, then answers as "json" does
		e, _ := os.ReadFile(filepath.Join(dir, "stderr.txt"))
		_, _ = os.Stderr.Write(e)
		return output()
	case "manifests": // as yt-dlp: out.json while YouTube's HLS and DASH are skipped, else manifests.json
		if skipsManifests(rec.Args) {
			return output()
		}
		out, _ := os.ReadFile(filepath.Join(dir, "manifests.json"))
		_, _ = os.Stdout.Write(out)
		return 0
	case "stderr": // writes stderr.txt and exits with $SITEJOB_EXIT
		return fail()
	case "block": // waits for the file "release", then answers
		waitRelease()
		return output()
	case "block-stderr": // waits for the file "release", then fails as "stderr" does
		waitRelease()
		return fail()
	case "sleep": // sleeps 3 s, then leaves the file "finished-<pid>"
		time.Sleep(3 * time.Second)
		_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("finished-%d", os.Getpid())), nil, 0o600)
		return output()
	case "flood":
		_, _ = os.Stdout.Write(bytes.Repeat([]byte("{"), 1<<20))
		return 0
	}
	return 3
}

// skipsManifests reports whether yt-dlp's arguments skip both of YouTube's manifests
// (an --extractor-args value "youtube:skip=..." listing hls and dash).
func skipsManifests(args []string) bool {
	i := slices.Index(args, "--extractor-args")
	if i < 0 || i+1 >= len(args) {
		return false
	}
	skip, ok := strings.CutPrefix(args[i+1], "youtube:skip=")
	parts := strings.Split(skip, ",")
	return ok && slices.Contains(parts, "hls") && slices.Contains(parts, "dash")
}

const testPage = "https://www.youtube.com/watch?v=aaaaaaaaaaa"

// pageJSON is a small page as yt-dlp -J prints it.
const pageJSON = `{"id": "aaaaaaaaaaa", "extractor_key": "Youtube", "title": "A video", "duration": 12.5,
 "formats": [{"format_id": "18", "protocol": "https", "url": "https://rr1.googlevideo.com/videoplayback?itag=18",
  "ext": "mp4", "vcodec": "avc1.42001E", "acodec": "mp4a.40.2", "height": 360, "filesize": 1000}]}`

// fakeTools is the tools manager of the tests: its leases run the test binary (or the
// next of exes) as yt-dlp, and Env makes it the stand-in that acts out the next of
// modes (the last ones repeat).
type fakeTools struct {
	dir, root string // the stand-in's control folder; the tools folder (cache/, run/)
	deno      string // Lease.Deno

	mu         sync.Mutex
	exes       []string
	modes      []string
	exit       int
	acquireErr error
	calls      int // of Acquire
	readyCalls int // of AcquireReady (look-aheads)
	acquires   int // that gave a lease (either way)
	envs       int
	reports    []tools.Outcome
	reported   []string // the YtDlp of each report
}

func newFakeTools(t *testing.T, modes ...string) *fakeTools {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeTools{dir: t.TempDir(), root: t.TempDir(), exes: []string{exe}, modes: modes}
	for _, d := range []string{filepath.Join(f.dir, "runs"), f.workRoot()} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	f.setOutput(t, pageJSON)
	return f
}

func (f *fakeTools) workRoot() string { return filepath.Join(f.root, "run") }

func (f *fakeTools) Acquire(ctx context.Context) (*tools.Lease, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.leaseLocked()
}

// AcquireReady is the look-ahead's Acquire: it gives the next lease as Acquire does
// (the real one never installs, and never hands out a trial's copy).
func (f *fakeTools) AcquireReady(ctx context.Context) (*tools.Lease, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.readyCalls++
	return f.leaseLocked()
}

func (f *fakeTools) leaseLocked() (*tools.Lease, error) {
	if f.acquireErr != nil {
		return nil, f.acquireErr
	}
	exe := f.exes[min(f.acquires, len(f.exes)-1)]
	f.acquires++
	return &tools.Lease{YtDlp: exe, Deno: f.deno, YtDlpCache: filepath.Join(f.root, "cache", "yt-dlp"),
		DenoCache: filepath.Join(f.root, "cache", "deno"), WorkRoot: f.workRoot(), YtDlpVersion: "2026.10.01"}, nil
}

// readyAcquireCalls returns how often AcquireReady was called.
func (f *fakeTools) readyAcquireCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.readyCalls
}

func (f *fakeTools) Report(l *tools.Lease, outcome tools.Outcome) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reports = append(f.reports, outcome)
	f.reported = append(f.reported, l.YtDlp)
}

func (f *fakeTools) Env(s tools.EnvSpec) []string {
	f.mu.Lock()
	mode := f.modes[min(f.envs, len(f.modes)-1)]
	f.envs++
	exit := f.exit
	f.mu.Unlock()
	env := tools.Environment(runtime.GOOS, os.Getenv, "", nil, s)
	return append(env, "SITEJOB_STANDIN=1", "SITEJOB_DIR="+f.dir, "SITEJOB_MODE="+mode, "SITEJOB_EXIT="+strconv.Itoa(exit))
}

func (f *fakeTools) setOutput(t *testing.T, out string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, "out.json"), []byte(out), 0o600); err != nil {
		t.Fatal(err)
	}
}

// setManifestsOutput is what the stand-in of mode "manifests" prints when its arguments
// keep YouTube's manifests.
func (f *fakeTools) setManifestsOutput(t *testing.T, out string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, "manifests.json"), []byte(out), 0o600); err != nil {
		t.Fatal(err)
	}
}

// acquireCalls returns how often Acquire was called.
func (f *fakeTools) acquireCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeTools) setStderr(t *testing.T, text string, exit int) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, "stderr.txt"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.exit = exit
	f.mu.Unlock()
}

// release lets every blocked stand-in answer.
func (f *fakeTools) release(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, "release"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

// records returns the runs of the stand-in so far, oldest first.
func (f *fakeTools) records(t *testing.T) []runRecord {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(f.dir, "runs"))
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	var out []runRecord
	for _, n := range names {
		data, err := os.ReadFile(filepath.Join(f.dir, "runs", n))
		if err != nil {
			t.Fatal(err)
		}
		var rec runRecord
		if err := json.Unmarshal(data, &rec); err != nil {
			continue // still being written
		}
		out = append(out, rec)
	}
	return out
}

func (f *fakeTools) outcomes() []tools.Outcome {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.reports)
}

func newTestResolver(t *testing.T, f *fakeTools, cfg ResolverConfig) *Resolver {
	t.Helper()
	cfg.Tools = f
	r := NewResolver(cfg)
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// waitFor polls cond for up to 20 s.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); !cond(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// envValue returns the value of name in env ("" when absent), ignoring the case of
// names on Windows.
func envValue(env []string, name string) (string, bool) {
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if k == name || (runtime.GOOS == "windows" && strings.EqualFold(k, name)) {
			return v, true
		}
	}
	return "", false
}

// A run gets exactly the arguments of §4.7, the cookie file (private, in its own work
// folder) and the allow-listed environment; its work folder is gone afterwards and its
// outcome is reported.
func TestResolveRunsYtDlp(t *testing.T) {
	f := newFakeTools(t, "json")
	f.deno = filepath.Join(f.root, "deno", "2.5.0", "deno")
	t.Setenv("SITEJOB_LEAK", "secret-value") // not on the allow-list
	r := newTestResolver(t, f, ResolverConfig{})
	const referer = "https://blog.example/post"
	info, err := r.Resolve(testContext(t), Request{PageURL: testPage, Cookie: "SID=abc; PREF=f6=4", Referer: referer})
	if err != nil || info.ID != "aaaaaaaaaaa" || info.Site != "YouTube" || len(info.Formats) != 1 {
		t.Fatalf("Resolve = %+v, %v", info, err)
	}
	recs := f.records(t)
	if len(recs) != 1 {
		t.Fatalf("%d runs", len(recs))
	}
	rec := recs[0]
	i := slices.Index(rec.Args, "--cookies")
	if i < 0 {
		t.Fatalf("no cookie file in %q", rec.Args)
	}
	cookies := rec.Args[i+1]
	run := filepath.Dir(cookies)
	if filepath.Dir(run) != f.workRoot() || filepath.Base(cookies) != cookieFileName {
		t.Errorf("cookie file %s is not in a work folder of %s", cookies, f.workRoot())
	}
	want := buildArgs(argsSpec{Deno: f.deno, CacheDir: filepath.Join(f.root, "cache", "yt-dlp"), Cookies: cookies,
		Referer: referer, PageURL: testPage})
	if !slices.Equal(rec.Args, want) {
		t.Errorf("arguments\n got %q\nwant %q", rec.Args, want)
	}
	// A page with a direct MP4 video is resolved once, without YouTube's manifests.
	if !slices.Contains(rec.Args, "youtube:skip=translated_subs,hls,dash") {
		t.Errorf("arguments %q do not skip YouTube's manifests", rec.Args)
	}
	if want := strings.Join(cookieLines(testPage, "SID=abc; PREF=f6=4", ""), "\n") + "\n"; rec.Cookies != want {
		t.Errorf("cookie file %q, want %q", rec.Cookies, want)
	}
	if runtime.GOOS != "windows" && rec.CookieMode != 0o600 {
		t.Errorf("cookie file mode %o", rec.CookieMode)
	}
	if v, ok := envValue(rec.Env, "SITEJOB_LEAK"); ok {
		t.Errorf("an inherited variable reached yt-dlp: %q", v)
	}
	if v, _ := envValue(rec.Env, "TEMP"); v != filepath.Join(run, "tmp") {
		t.Errorf("TEMP = %q, want %q", v, filepath.Join(run, "tmp"))
	}
	if v, _ := envValue(rec.Env, "PATH"); !strings.HasPrefix(v, filepath.Dir(f.deno)+string(os.PathListSeparator)) {
		t.Errorf("PATH = %q, want Deno's folder first", v)
	}
	if v, _ := envValue(rec.Env, "DENO_DIR"); v != filepath.Join(f.root, "cache", "deno") {
		t.Errorf("DENO_DIR = %q", v)
	}
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY"} {
		if _, ok := envValue(rec.Env, name); ok {
			t.Errorf("%s is set for a direct run", name)
		}
	}
	if entries, _ := os.ReadDir(f.workRoot()); len(entries) != 0 {
		t.Errorf("the work folder was left behind: %d entries", len(entries))
	}
	if got := f.outcomes(); !slices.Equal(got, []tools.Outcome{tools.OK}) {
		t.Errorf("reports %v, want [OK]", got)
	}
}

// A proxy with credentials reaches yt-dlp through its environment only; no argument
// carries the password.
func TestResolveProxyWithCredentials(t *testing.T) {
	f := newFakeTools(t, "json")
	r := newTestResolver(t, f, ResolverConfig{})
	p := Proxy{URL: "http://proxy.lan:3128", Username: "me", Password: "s3cret"}
	if _, err := r.Resolve(testContext(t), Request{PageURL: testPage, Proxy: p}); err != nil {
		t.Fatal(err)
	}
	rec := f.records(t)[0]
	if slices.Contains(rec.Args, "--proxy") || strings.Contains(strings.Join(rec.Args, "\n"), "s3cret") {
		t.Errorf("the proxy is in the arguments: %q", rec.Args)
	}
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY"} {
		if v, _ := envValue(rec.Env, name); v != "http://me:s3cret@proxy.lan:3128" {
			t.Errorf("%s = %q", name, v)
		}
	}
}

// A SOCKS5 proxy that resolves host names (socks5h, what Downex's SOCKS5 routes are)
// reaches yt-dlp as it is, with credentials only in the environment.
func TestResolveSOCKS5H(t *testing.T) {
	f := newFakeTools(t, "json")
	r := newTestResolver(t, f, ResolverConfig{})
	ctx := testContext(t)
	if _, err := r.Resolve(ctx, Request{PageURL: testPage, Proxy: Proxy{URL: "socks5h://127.0.0.1:9050"}}); err != nil {
		t.Fatal(err)
	}
	if args := f.records(t)[0].Args; !slices.Contains(args, "socks5h://127.0.0.1:9050") {
		t.Errorf("arguments %q, want the socks5h proxy", args)
	}
	p := Proxy{URL: "socks5h://proxy.lan:1080", Username: "me", Password: "s3cret"}
	if _, err := r.Resolve(ctx, Request{PageURL: testPage + "&x=1", Proxy: p}); err != nil {
		t.Fatal(err)
	}
	if v, _ := envValue(f.records(t)[1].Env, "ALL_PROXY"); v != "socks5h://me:s3cret@proxy.lan:1080" {
		t.Errorf("ALL_PROXY = %q", v)
	}
}

// Callers of one page share one extraction, and its result answers later callers.
func TestResolveSharesOneExtraction(t *testing.T) {
	f := newFakeTools(t, "block")
	r := newTestResolver(t, f, ResolverConfig{})
	ctx := testContext(t)
	var wg sync.WaitGroup
	infos := make([]*sitemedia.Info, 2)
	errs := make([]error, 2)
	for i, page := range []string{testPage, testPage + "#t=30"} {
		wg.Go(func() { infos[i], errs[i] = r.Resolve(ctx, Request{PageURL: page}) })
	}
	waitFor(t, "the extraction", func() bool { return len(f.records(t)) == 1 })
	time.Sleep(100 * time.Millisecond)
	f.release(t)
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || infos[0] == nil || infos[0] != infos[1] {
		t.Fatalf("results %p %p, %v %v", infos[0], infos[1], errs[0], errs[1])
	}
	again, err := r.Resolve(ctx, Request{PageURL: testPage})
	if err != nil || again != infos[0] {
		t.Errorf("a later caller got %p, %v; want the cached %p", again, err, infos[0])
	}
	if n := len(f.records(t)); n != 1 {
		t.Errorf("%d extractions, want 1", n)
	}
}

// At most two extractions run at once; the others wait for a turn.
func TestResolveConcurrencyCap(t *testing.T) {
	f := newFakeTools(t, "block")
	r := newTestResolver(t, f, ResolverConfig{})
	ctx := testContext(t)
	var wg sync.WaitGroup
	errs := make([]error, 3)
	for i := range 3 {
		wg.Go(func() {
			_, errs[i] = r.Resolve(ctx, Request{PageURL: fmt.Sprintf("%s&n=%d", testPage, i)})
		})
	}
	waitFor(t, "two extractions", func() bool { return len(f.records(t)) == 2 })
	time.Sleep(300 * time.Millisecond)
	if n := len(f.records(t)); n != 2 {
		t.Fatalf("%d extractions run at once, want 2", n)
	}
	f.release(t)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("page %d: %v", i, err)
		}
	}
	if n := len(f.records(t)); n != 3 {
		t.Errorf("%d extractions, want 3", n)
	}
}

// A caller whose context ends gets site_resolving while the extraction goes on; its
// retry collects the result without another run.
func TestResolveSiteResolving(t *testing.T) {
	f := newFakeTools(t, "block")
	r := newTestResolver(t, f, ResolverConfig{})
	short, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err := r.Resolve(short, Request{PageURL: testPage})
	if !errors.Is(err, sitemedia.ErrSiteResolving) || !errors.Is(err, context.DeadlineExceeded) ||
		err.Error() != sitemedia.ErrSiteResolving.Error() {
		t.Fatalf("Resolve = %v, want site_resolving", err)
	}
	waitFor(t, "the extraction", func() bool { return len(f.records(t)) == 1 })
	f.release(t)
	info, err := r.Resolve(testContext(t), Request{PageURL: testPage})
	if err != nil || info == nil {
		t.Fatalf("retry = %v, %v", info, err)
	}
	if n := len(f.records(t)); n != 1 {
		t.Errorf("%d extractions, want 1", n)
	}
}

// An extraction still waiting for its turn when its last caller leaves never runs.
func TestResolveDropsUnstartedWork(t *testing.T) {
	f := newFakeTools(t, "block")
	r := newTestResolver(t, f, ResolverConfig{MaxConcurrent: 1})
	ctx := testContext(t)
	first := make(chan error, 1)
	go func() {
		_, err := r.Resolve(ctx, Request{PageURL: testPage})
		first <- err
	}()
	waitFor(t, "the first extraction", func() bool { return len(f.records(t)) == 1 })
	short, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	other := Request{PageURL: testPage + "&other=1"}
	if _, err := r.Resolve(short, other); !errors.Is(err, sitemedia.ErrSiteResolving) {
		t.Fatalf("waiting caller: %v", err)
	}
	f.release(t)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if n := len(f.records(t)); n != 1 {
		t.Fatalf("the abandoned extraction ran: %d runs", n)
	}
	if _, err := r.Resolve(ctx, other); err != nil {
		t.Fatal(err)
	}
	if n := len(f.records(t)); n != 2 {
		t.Errorf("%d runs, want 2", n)
	}
}

// Pages are reused for 15 minutes unless the request is fresh; refusals for 30 s.
func TestResolveCache(t *testing.T) {
	var mu sync.Mutex
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	advance := func(d time.Duration) {
		mu.Lock()
		now = now.Add(d)
		mu.Unlock()
	}
	f := newFakeTools(t, "json")
	r := newTestResolver(t, f, ResolverConfig{Now: clock})
	ctx := testContext(t)
	runs := func() int { return len(f.records(t)) }
	first, err := r.Resolve(ctx, Request{PageURL: testPage})
	if err != nil {
		t.Fatal(err)
	}
	advance(14 * time.Minute)
	if again, _ := r.Resolve(ctx, Request{PageURL: testPage}); again != first || runs() != 1 {
		t.Errorf("a page was resolved again within 15 minutes (%d runs)", runs())
	}
	fresh, err := r.Resolve(ctx, Request{PageURL: testPage, Fresh: true})
	if err != nil || fresh == first || runs() != 2 {
		t.Fatalf("a fresh request used the cache (%d runs, %v)", runs(), err)
	}
	if again, _ := r.Resolve(ctx, Request{PageURL: testPage}); again != fresh {
		t.Error("the fresh result did not replace the cached one")
	}
	advance(15 * time.Minute)
	if again, _ := r.Resolve(ctx, Request{PageURL: testPage}); again == fresh || runs() != 3 {
		t.Errorf("a page was reused after 15 minutes (%d runs)", runs())
	}
	if _, err := r.Resolve(ctx, Request{PageURL: testPage, Cookie: "SID=1"}); err != nil || runs() != 4 {
		t.Errorf("another cookie shared the cached page (%d runs)", runs())
	}

	g := newFakeTools(t, "stderr")
	g.setStderr(t, "ERROR: [youtube] aaaaaaaaaaa: Sign in to confirm your age.\n", 1)
	r = newTestResolver(t, g, ResolverConfig{Now: clock})
	for range 2 {
		if _, err := r.Resolve(ctx, Request{PageURL: testPage}); !errors.Is(err, sitemedia.ErrAuthRequired) {
			t.Fatalf("Resolve = %v, want auth_required", err)
		}
	}
	if n := len(g.records(t)); n != 1 {
		t.Errorf("a refusal was not reused: %d runs", n)
	}
	advance(30 * time.Second)
	_, _ = r.Resolve(ctx, Request{PageURL: testPage})
	if n := len(g.records(t)); n != 2 {
		t.Errorf("a refusal was reused after 30 s: %d runs", n)
	}
}

// Each way a run can end gives its error and the outcome the tools manager is told;
// only site_error and bad options count against the copy.
func TestResolveOutcomes(t *testing.T) {
	for _, c := range []struct {
		name, mode, out, stderr string
		exit                    int
		maxOutput               int
		want                    error
		outcome                 tools.Outcome
		text                    string
	}{
		{name: "sign-in", mode: "stderr", stderr: "ERROR: [youtube] x: Sign in to confirm your age\n", exit: 1,
			want: sitemedia.ErrAuthRequired, outcome: tools.OK},
		{name: "unknown failure", mode: "stderr", stderr: "ERROR: [youtube] x: Something new broke\n", exit: 1,
			want: sitemedia.ErrSiteError, outcome: tools.SiteError, text: "Something new broke"},
		{name: "bad options", mode: "stderr", stderr: "Usage: yt-dlp [OPTIONS] URL\n\nyt-dlp: error: no such option: --no-plugin-dirs\n",
			exit: 2, want: sitemedia.ErrSiteError, outcome: tools.BadOptions,
			text: "it rejected Downex's options (yt-dlp: error: no such option: --no-plugin-dirs)"},
		{name: "no JSON", mode: "stderr", stderr: "ERROR: Unsupported URL: https://example.com/\n", exit: 0,
			want: sitemedia.ErrSiteUnsupported, outcome: tools.OK},
		{name: "crash", mode: "stderr", stderr: "Fatal Python error: bad thing\n", exit: 3,
			want: sitemedia.ErrSiteError, outcome: tools.SiteError, text: "Fatal Python error: bad thing"},
		{name: "playlist", mode: "json", out: `{"_type": "playlist", "id": "PL1", "extractor_key": "YoutubeTab"}`,
			want: media.ErrUnsupported, outcome: tools.OK},
		{name: "live", mode: "json", out: `{"id": "x", "extractor_key": "Youtube", "live_status": "is_live"}`,
			want: media.ErrLive, outcome: tools.OK},
		{name: "malformed", mode: "json", out: `{"id": "x", "extractor_key"`, want: media.ErrMalformed, outcome: tools.OK},
		// A post without a video: yt-dlp prints null (amendment 2026-11-18).
		{name: "nothing found", mode: "json", out: "null\n", want: sitemedia.ErrSiteUnsupported, outcome: tools.OK,
			text: "yt-dlp found nothing to play in it"},
		// A claim without a video that the site's reader calls live, and warns is not.
		{name: "not live", mode: "json-stderr",
			out:    `{"id": "x", "extractor_key": "LBRY", "is_live": true, "live_status": "is_live", "formats": []}`,
			stderr: "WARNING: [lbry] x: This stream is not live\nWARNING: No video formats found!\n",
			want:   sitemedia.ErrSiteUnsupported, outcome: tools.OK, text: "yt-dlp found nothing to play in it"},
		// A live stream whose manifests were skipped has no format either: still live.
		{name: "live without formats", mode: "json-stderr",
			out:    `{"id": "x", "extractor_key": "Youtube", "is_live": true, "live_status": "is_live", "formats": []}`,
			stderr: "WARNING: [youtube] x: Some formats are skipped\nWARNING: No video formats found!\n",
			want:   media.ErrLive, outcome: tools.OK},
		{name: "too much output", mode: "flood", maxOutput: 1024, want: media.ErrTooLarge, outcome: tools.OK},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeTools(t, c.mode)
			if c.out != "" {
				f.setOutput(t, c.out)
			}
			f.setStderr(t, c.stderr, c.exit)
			r := newTestResolver(t, f, ResolverConfig{MaxOutput: c.maxOutput})
			info, err := r.Resolve(testContext(t), Request{PageURL: testPage})
			if !errors.Is(err, c.want) || info != nil {
				t.Fatalf("Resolve = %v, %v; want %v", info, err, c.want)
			}
			if c.text != "" && err.Error() != c.want.Error()+": "+c.text {
				t.Errorf("message %q, want %q", err, c.want.Error()+": "+c.text)
			}
			if strings.Contains(err.Error(), "://") {
				t.Errorf("the message keeps a URL: %q", err)
			}
			if got := f.outcomes(); !slices.Equal(got, []tools.Outcome{c.outcome}) {
				t.Errorf("reports %v, want [%v]", got, c.outcome)
			}
		})
	}
}

// After a run that counts against the copy, another copy the tools manager offers gets
// the page once more, and its outcome is reported too; the same copy is not retried.
func TestResolveRetriesWithAnotherCopy(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	alt := filepath.Join(t.TempDir(), "yt-dlp-alt"+filepath.Ext(exe))
	copyFile(t, exe, alt)
	for _, c := range []struct {
		name string
		exit int
		want tools.Outcome
	}{
		{"site error", 1, tools.SiteError},
		{"bad options", 2, tools.BadOptions},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeTools(t, "stderr", "json")
			f.exes = []string{exe, alt}
			f.setStderr(t, "ERROR: [youtube] x: Something broke\n", c.exit)
			r := newTestResolver(t, f, ResolverConfig{})
			info, err := r.Resolve(testContext(t), Request{PageURL: testPage})
			if err != nil || info == nil {
				t.Fatalf("Resolve = %v, %v", info, err)
			}
			recs := f.records(t)
			if len(recs) != 2 || recs[1].Exe != alt {
				t.Fatalf("runs %d, second with %v", len(recs), recs)
			}
			if got := f.outcomes(); !slices.Equal(got, []tools.Outcome{c.want, tools.OK}) {
				t.Errorf("reports %v", got)
			}
			if f.reported[1] != alt {
				t.Errorf("the second report is for %s", f.reported[1])
			}
		})
	}
	f := newFakeTools(t, "stderr", "json")
	f.setStderr(t, "ERROR: [youtube] x: Something broke\n", 1)
	r := newTestResolver(t, f, ResolverConfig{})
	if _, err := r.Resolve(testContext(t), Request{PageURL: testPage}); !errors.Is(err, sitemedia.ErrSiteError) {
		t.Fatalf("Resolve = %v", err)
	}
	if n := len(f.records(t)); n != 1 {
		t.Errorf("the same copy ran %d times", n)
	}
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	src, err := os.Open(from)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatal(err)
	}
	if err := dst.Close(); err != nil {
		t.Fatal(err)
	}
}

// A run past its timeout is site_error, and yt-dlp is killed rather than left running.
func TestResolveTimeoutKillsYtDlp(t *testing.T) {
	t.Parallel()
	f := newFakeTools(t, "sleep")
	r := newTestResolver(t, f, ResolverConfig{Timeout: 500 * time.Millisecond})
	start := time.Now()
	_, err := r.Resolve(testContext(t), Request{PageURL: testPage})
	if !errors.Is(err, sitemedia.ErrSiteError) || !strings.Contains(err.Error(), "took longer than 500ms") {
		t.Fatalf("Resolve = %v", err)
	}
	if d := time.Since(start); d > 2500*time.Millisecond {
		t.Errorf("Resolve took %v", d)
	}
	if got := f.outcomes(); !slices.Equal(got, []tools.Outcome{tools.SiteError}) {
		t.Errorf("reports %v", got)
	}
	time.Sleep(time.Until(start.Add(4 * time.Second)))
	if m, _ := filepath.Glob(filepath.Join(f.dir, "finished-*")); len(m) != 0 {
		t.Error("the timed-out yt-dlp kept running")
	}
	if entries, _ := os.ReadDir(f.workRoot()); len(entries) != 0 {
		t.Errorf("the work folder was left behind: %d entries", len(entries))
	}
}

// Close kills running extractions, removes their work folders and refuses later calls.
func TestResolveClose(t *testing.T) {
	f := newFakeTools(t, "block")
	r := NewResolver(ResolverConfig{Tools: f})
	done := make(chan error, 1)
	go func() {
		_, err := r.Resolve(testContext(t), Request{PageURL: testPage})
		done <- err
	}()
	waitFor(t, "the extraction", func() bool { return len(f.records(t)) == 1 })
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, ErrClosed) || !errors.Is(err, sitemedia.ErrSiteError) {
		t.Errorf("waiting caller: %v", err)
	}
	if _, err := r.Resolve(testContext(t), Request{PageURL: testPage}); !errors.Is(err, ErrClosed) {
		t.Errorf("Resolve after Close: %v", err)
	}
	if entries, _ := os.ReadDir(f.workRoot()); len(entries) != 0 {
		t.Errorf("the work folder was left behind: %d entries", len(entries))
	}
	if got := f.outcomes(); len(got) != 0 {
		t.Errorf("a stopped run was reported: %v", got)
	}
}

// Invalid requests are refused before anything runs, without repeating their values;
// the tools manager's errors are passed on and not cached.
func TestResolveRefusals(t *testing.T) {
	f := newFakeTools(t, "json")
	r := newTestResolver(t, f, ResolverConfig{})
	ctx := testContext(t)
	for _, req := range []Request{
		{PageURL: "ftp://example.com/v"},
		{PageURL: "not a url"},
		{PageURL: testPage, Referer: "javascript:alert(1)"},
		{PageURL: testPage, Proxy: Proxy{URL: "http://user:pw@proxy.lan:3128"}},
		{PageURL: testPage, Proxy: Proxy{URL: "socks://proxy.lan:1080"}},
		{PageURL: "https://alice:pw@proxy.lan/video/1"},
		{PageURL: testPage, Referer: "https://bob:pw@proxy.lan/"},
		{PageURL: testPage, Proxy: Proxy{URL: "http://proxy.lan"}},
		{PageURL: testPage, Proxy: Proxy{URL: "http://proxy.lan:3128/path?q=1"}},
		{PageURL: testPage, Proxy: Proxy{Username: "me", Password: "pw"}},
		{PageURL: testPage, Cookie: "SID=pw", CookieDomain: "proxy.lan"},
		{PageURL: testPage, Cookie: "SID=pw", CookieDomain: "com"},
		{PageURL: testPage, Cookie: "SID=" + strings.Repeat("p", validate.MaxSiteCookieBytes)},
	} {
		_, err := r.Resolve(ctx, req)
		if !errors.Is(err, sitemedia.ErrSiteError) || strings.Contains(err.Error(), "pw") ||
			strings.Contains(err.Error(), "proxy.lan") {
			t.Errorf("Resolve(%+v) = %v", req, err)
		}
	}
	if n := len(f.records(t)); n != 0 {
		t.Errorf("%d runs for invalid requests", n)
	}
	f.acquireErr = fmt.Errorf("%w: no build for this platform", sitemedia.ErrToolsUnavailable)
	for range 2 {
		if _, err := r.Resolve(ctx, Request{PageURL: testPage}); !errors.Is(err, sitemedia.ErrToolsUnavailable) {
			t.Errorf("Resolve = %v, want tools_unavailable", err)
		}
	}
	if f.calls != 2 || f.envs != 0 {
		t.Errorf("Acquire called %d times, %d runs; want 2 and none", f.calls, f.envs)
	}
}

// The sign-in's domain (§6.3) scopes the cookie file to that domain and its subdomains;
// an answer got with it is cached apart from the anonymous one and from one for the
// page's exact host, and a cancelled run leaves no cookie file behind.
func TestResolveCookieDomain(t *testing.T) {
	f := newFakeTools(t, "json")
	r := newTestResolver(t, f, ResolverConfig{})
	ctx := testContext(t)
	for _, req := range []Request{
		{PageURL: testPage},
		{PageURL: testPage, Cookie: "SID=abc"},
		{PageURL: testPage, Cookie: "SID=abc", CookieDomain: "youtube.com"},
		{PageURL: testPage, Cookie: "SID=abc", CookieDomain: "youtube.com"}, // cached
	} {
		if _, err := r.Resolve(ctx, req); err != nil {
			t.Fatalf("Resolve(%+v) = %v", req, err)
		}
	}
	recs := f.records(t)
	if len(recs) != 3 {
		t.Fatalf("%d runs, want 3", len(recs))
	}
	if slices.Contains(recs[0].Args, "--cookies") || recs[0].Cookies != "" {
		t.Errorf("the anonymous run had a cookie file: %q", recs[0].Args)
	}
	if want := "# Netscape HTTP Cookie File\nwww.youtube.com\tFALSE\t/\tTRUE\t0\tSID\tabc\n"; recs[1].Cookies != want {
		t.Errorf("exact-host cookie file %q, want %q", recs[1].Cookies, want)
	}
	if want := "# Netscape HTTP Cookie File\n.youtube.com\tTRUE\t/\tTRUE\t0\tSID\tabc\n"; recs[2].Cookies != want {
		t.Errorf("domain cookie file %q, want %q", recs[2].Cookies, want)
	}
	for _, rec := range recs {
		if slices.ContainsFunc(rec.Args, func(a string) bool { return strings.Contains(a, "abc") }) {
			t.Errorf("the cookie is in the arguments: %q", rec.Args)
		}
	}
	if entries, _ := os.ReadDir(f.workRoot()); len(entries) != 0 {
		t.Errorf("work folders left behind: %d", len(entries))
	}
}

// A caller that gives up does not stop the run, but the run's folder and its cookie
// file go when yt-dlp exits; Close removes them at once.
func TestResolveCookieFileGoesWithTheRun(t *testing.T) {
	f := newFakeTools(t, "block")
	r := NewResolver(ResolverConfig{Tools: f})
	ctx, cancel := context.WithCancel(testContext(t))
	done := make(chan error, 1)
	go func() {
		_, err := r.Resolve(ctx, Request{PageURL: testPage, Cookie: "SID=abc", CookieDomain: "youtube.com"})
		done <- err
	}()
	waitFor(t, "the extraction", func() bool { return len(f.records(t)) == 1 })
	if m, _ := filepath.Glob(filepath.Join(f.workRoot(), "*", cookieFileName)); len(m) != 1 {
		t.Fatalf("cookie files while the run lasts: %q", m)
	}
	cancel()
	if err := <-done; !errors.Is(err, sitemedia.ErrSiteResolving) {
		t.Errorf("cancelled caller: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(f.workRoot()); len(entries) != 0 {
		t.Errorf("the work folder was left behind: %d entries", len(entries))
	}
}
