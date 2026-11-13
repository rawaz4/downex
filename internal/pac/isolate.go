package pac

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"runtime/debug"
	"runtime/metrics"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Isolation. goja's built-ins run as Go code that the evaluation timeout cannot
// interrupt, and they allocate what a script asks for: "x".repeat(2**40) makes the Go
// runtime fail with a fatal "out of memory" that no recover catches, and a native loop
// over an array-like of length 2**53 never returns. A PAC script may come from anyone
// on the network (WPAD, a pac_url over plain http), so the daemon evaluates it with an
// Isolated evaluator: in a helper process, this executable started again (see
// ServeIfHelper), which ends itself when its heap or a call exceeds a bound and is
// killed when a call overruns. A hostile script then costs that process only; the next
// evaluation starts a new one (after a pause that grows while helpers keep failing).

const (
	// helperEnv marks a process started as a PAC helper; its value names the protocol
	// (helperVersion), and a helper of another version refuses to serve. Other values
	// are not this mark and are ignored.
	helperEnv     = "DOWNEX_PAC_HELPER"
	helperPrefix  = "pac-helper-"
	helperVersion = helperPrefix + "1"

	// helperHeapLimit is the heap a helper may use before it ends itself. PAC scripts
	// need a few MiB.
	helperHeapLimit = 256 << 20
	// helperCommitLimit is the memory a helper may commit where the system enforces it
	// (Windows job objects): the heap limit plus room for the runtime (and the race
	// detector's shadow memory in test builds).
	helperCommitLimit = 1 << 30
	// helperWatchInterval is how often a helper checks its heap and the running call.
	helperWatchInterval = 10 * time.Millisecond
	// helperCallGrace is how long a call may overrun EvalTimeout before the helper ends
	// itself or is killed: a DNS lookup of a helper function (dnsTimeout) holds off the
	// interrupt as it did in this process; only native code beyond that is cut short.
	helperCallGrace = dnsTimeout + time.Second
	// helperStartGrace is the extra time a new helper has to start and run the script.
	helperStartGrace = 10 * time.Second
	// maxHelperRequest bounds a request line (the script is at most MaxScriptBytes,
	// base64-encoded) and maxHelperResponse a response line.
	maxHelperRequest  = 2*MaxScriptBytes + 64<<10
	maxHelperResponse = 64 << 10
	// maxHelperError bounds an error text passed back (a script can throw anything).
	maxHelperError = 512
	// maxRestartDelay bounds the pause before a new helper after failures.
	maxRestartDelay = time.Minute

	// Exit codes of a helper.
	helperExitProtocol = 2
	helperExitMemory   = 3
	helperExitTimeout  = 4
)

// ErrNoHelper reports that this executable cannot run isolated PAC evaluators: it did
// not call ServeIfHelper first thing in main.
var ErrNoHelper = errors.New("pac: this program cannot start an isolated PAC evaluator")

var (
	errEvaluatorClosed = errors.New("pac: evaluator closed")
	errHelperFailed    = errors.New("pac: the evaluator process failed; trying again later")
	errHelperTimeout   = errors.New("pac: the evaluator process did not answer in time")
)

// helperExecutable is this executable once ServeIfHelper has found that it can serve
// as its own PAC helper.
var helperExecutable atomic.Pointer[string]

// ServeIfHelper must be called first thing in main by a program that uses
// NewIsolated. In a process started as a PAC helper it serves the evaluator that
// started it (on stdin and stdout) and exits; otherwise it records that this
// executable can be started as one and returns.
func ServeIfHelper() {
	if v := os.Getenv(helperEnv); strings.HasPrefix(v, helperPrefix) {
		if v != helperVersion {
			os.Exit(helperExitProtocol)
		}
		os.Exit(serveHelper(os.Stdin, os.Stdout))
	}
	if exe, err := os.Executable(); err == nil {
		helperExecutable.Store(&exe)
	}
}

// helperRequest is one line from the evaluator to its helper: first the script, then
// one URL per FindProxy.
type helperRequest struct {
	Script    []byte `json:"script,omitempty"`
	TimeoutMS int64  `json:"timeout_ms,omitempty"`
	URL       string `json:"url,omitempty"`
}

// helperResponse is one line from the helper: the results of a URL, or an error (also
// the answer to the script: empty when it compiled and ran).
type helperResponse struct {
	Results []Result `json:"results,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// Isolated is an Evaluator in a helper process (see the isolation note above). Results
// are cached here, per host for CacheTTL. Its methods are safe for concurrent use.
type Isolated struct {
	exe    string
	script []byte
	opts   Options
	cache  resultCache

	closed atomic.Bool

	mu       sync.Mutex // serialises calls, as the helper evaluates one at a time
	h        *helperProc
	failures int       // helpers that failed in a row
	retryAt  time.Time // no new helper is started before
}

// Bounds of the routes a helper may return and of the hosts cached.
const (
	maxResults     = 32
	maxCachedHosts = 4096
)

// NewIsolated is New in a helper process: it starts the helper, which compiles and runs
// script, and returns an evaluator that sends every FindProxy there. Of opts, Now,
// EvalTimeout and CacheTTL apply; the helper uses the system's DNS resolver and address.
// It fails with ErrNoHelper unless this executable called ServeIfHelper. Close stops the
// helper.
func NewIsolated(script string, opts Options) (*Isolated, error) {
	if len(script) > MaxScriptBytes {
		return nil, fmt.Errorf("%w: %d bytes", ErrTooLarge, len(script))
	}
	exe := helperExecutable.Load()
	if exe == nil {
		return nil, ErrNoHelper
	}
	opts = opts.withDefaults()
	i := &Isolated{exe: *exe, script: []byte(script), opts: opts, cache: newResultCache(opts)}
	i.mu.Lock()
	defer i.mu.Unlock()
	if err := i.startLocked(); err != nil {
		return nil, err
	}
	return i, nil
}

// FindProxy is Evaluator.FindProxy, evaluated by the helper. When the helper fails it
// returns an error (the caller falls back to a direct connection) and a new helper is
// started for a later call.
func (i *Isolated) FindProxy(u *url.URL) ([]Result, error) {
	host := strings.ToLower(u.Hostname())
	if r, ok := i.cache.get(host); ok {
		return r, nil
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed.Load() {
		return nil, errEvaluatorClosed
	}
	if i.h == nil {
		if err := i.startLocked(); err != nil {
			return nil, fmt.Errorf("pac: FindProxyForURL(%q): %w", host, err)
		}
	}
	resp, err := i.h.call(helperRequest{URL: ScriptURL(u)}, i.opts.EvalTimeout+helperCallGrace)
	if err != nil {
		i.failedLocked()
		return nil, fmt.Errorf("pac: FindProxyForURL(%q): %w", host, err)
	}
	i.failures = 0
	if resp.Error != "" {
		return nil, fmt.Errorf("pac: FindProxyForURL(%q): %s", host, resp.Error)
	}
	results := validResults(resp.Results)
	if len(results) == 0 {
		return nil, fmt.Errorf("pac: FindProxyForURL(%q): no usable proxy", host)
	}
	i.cache.put(host, results)
	return results, nil
}

// Close stops the helper without waiting: later calls fail at once, and the helper ends
// once a call in progress has been answered.
func (i *Isolated) Close() {
	if i.closed.Swap(true) {
		return
	}
	go func() {
		i.mu.Lock()
		defer i.mu.Unlock()
		if i.h != nil {
			i.h.stop()
			i.h = nil
		}
	}()
}

// startLocked starts a helper and hands it the script. A script that does not compile
// or run is reported as such; a helper that fails delays the next start.
func (i *Isolated) startLocked() error {
	if i.closed.Load() {
		return errEvaluatorClosed
	}
	if time.Now().Before(i.retryAt) {
		return errHelperFailed
	}
	h, err := startHelper(i.exe)
	if err != nil {
		i.failedLocked()
		return err
	}
	resp, err := h.call(helperRequest{Script: i.script, TimeoutMS: i.opts.EvalTimeout.Milliseconds()},
		i.opts.EvalTimeout+helperStartGrace)
	switch {
	case err != nil:
		h.kill()
		i.failedLocked()
		return fmt.Errorf("pac: run script: %w", err)
	case resp.Error != "":
		h.stop()
		return errors.New(resp.Error) // the helper's New error, "pac: ..."
	}
	i.h = h
	return nil
}

// failedLocked ends the current helper after a failure and delays the next one: one
// second after the first failure, doubling up to maxRestartDelay.
func (i *Isolated) failedLocked() {
	if i.h != nil {
		i.h.kill()
		i.h = nil
	}
	delay := maxRestartDelay
	if i.failures < 6 {
		delay = min(time.Second<<i.failures, maxRestartDelay)
	}
	i.failures++
	i.retryAt = time.Now().Add(delay)
}

// validResults keeps the well-formed results a helper returned (it runs untrusted code).
func validResults(rs []Result) []Result {
	out := make([]Result, 0, min(len(rs), maxResults))
	for _, r := range rs {
		if len(out) == maxResults {
			break
		}
		switch {
		case r.Kind == Direct:
			out = append(out, Result{Kind: Direct})
		case r.Kind <= SOCKS5 && validResultHost(r.Host) && validPacPort(r.Port):
			out = append(out, r)
		}
	}
	return out
}

// helperProc is a running helper process.
type helperProc struct {
	cmd    *exec.Cmd
	in     *os.File      // the helper's stdin
	outF   *os.File      // the helper's stdout
	out    *bufio.Reader // over outF
	exited chan struct{} // closed once the process has been waited for
	once   sync.Once
}

// startHelper starts exe as a PAC helper.
func startHelper(exe string) (*helperProc, error) {
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("pac: start the evaluator process: %w", err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		return nil, fmt.Errorf("pac: start the evaluator process: %w", err)
	}
	cmd := exec.Command(exe)
	cmd.Env = append(withoutEnv(os.Environ(), helperEnv), helperEnv+"="+helperVersion)
	cmd.Stdin, cmd.Stdout = inR, outW // stderr: the null device
	hideHelperWindow(cmd)
	err = cmd.Start()
	inR.Close()
	outW.Close()
	if err != nil {
		inW.Close()
		outR.Close()
		return nil, fmt.Errorf("pac: start the evaluator process: %w", err)
	}
	h := &helperProc{cmd: cmd, in: inW, outF: outR, out: bufio.NewReaderSize(outR, maxHelperResponse), exited: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(h.exited)
	}()
	return h, nil
}

// call sends req and waits up to timeout for the answer; the helper is killed when it
// does not answer in time or answers out of protocol.
func (h *helperProc) call(req helperRequest, timeout time.Duration) (helperResponse, error) {
	line, err := json.Marshal(req)
	if err != nil {
		return helperResponse{}, err
	}
	line = append(line, '\n')
	type reply struct {
		resp helperResponse
		err  error
	}
	done := make(chan reply, 1)
	go func() {
		if _, err := h.in.Write(line); err != nil {
			done <- reply{err: errHelperFailed}
			return
		}
		b, err := h.out.ReadSlice('\n')
		if err != nil { // ended, or a line longer than any answer
			done <- reply{err: errHelperFailed}
			return
		}
		var resp helperResponse
		if err := json.Unmarshal(b, &resp); err != nil {
			done <- reply{err: errHelperFailed}
			return
		}
		done <- reply{resp: resp}
	}()
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case r := <-done:
		if r.err != nil {
			h.kill()
		}
		return r.resp, r.err
	case <-t.C:
		h.kill()
		return helperResponse{}, errHelperTimeout
	}
}

// stop ends the helper gently: it exits when its stdin closes, or is killed after a
// moment.
func (h *helperProc) stop() {
	_ = h.in.Close()
	go func() {
		select {
		case <-h.exited:
		case <-time.After(2 * time.Second):
		}
		h.kill()
	}()
}

// kill ends the helper at once. Its pipes are closed once it has exited (a blocked read
// returns then).
func (h *helperProc) kill() {
	h.once.Do(func() {
		_ = h.cmd.Process.Kill()
		_ = h.in.Close()
		go func() {
			<-h.exited
			_ = h.outF.Close()
		}()
	})
}

// withoutEnv returns env without the variable name.
func withoutEnv(env []string, name string) []string {
	out := env[:0:0]
	for _, kv := range env {
		if k, _, _ := strings.Cut(kv, "="); !strings.EqualFold(k, name) {
			out = append(out, kv)
		}
	}
	return out
}

// serveHelper is the helper side: it reads the script and then URLs from in and
// answers each on out, until in ends. A watchdog ends the process when its heap
// exceeds helperHeapLimit or a call runs past its time; the evaluator then reports
// the failure.
func serveHelper(in io.Reader, out io.Writer) int {
	limitHelperProcess(helperCommitLimit)
	debug.SetMemoryLimit(helperHeapLimit / 2) // collect early; the watchdog is the bound
	var busySince atomic.Int64                // UnixNano when the running call started, 0 when idle
	var callLimit atomic.Int64                // how long a call may run, in ns
	go watchHelper(&busySince, &callLimit)

	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64<<10), maxHelperRequest)
	w := bufio.NewWriter(out)
	answer := func(resp helperResponse) bool {
		b, err := json.Marshal(resp)
		if err != nil {
			return false
		}
		_, _ = w.Write(append(b, '\n'))
		return w.Flush() == nil
	}
	if !sc.Scan() {
		return helperExitProtocol
	}
	var init helperRequest
	if err := json.Unmarshal(sc.Bytes(), &init); err != nil {
		return helperExitProtocol
	}
	timeout := time.Duration(init.TimeoutMS) * time.Millisecond
	callLimit.Store(int64(max(timeout, DefaultEvalTimeout) + helperStartGrace))
	busySince.Store(time.Now().UnixNano())
	ev, err := New(string(init.Script), Options{EvalTimeout: timeout, CacheTTL: -1})
	busySince.Store(0)
	if err != nil {
		answer(helperResponse{Error: clipError(err.Error())})
		return 0
	}
	callLimit.Store(int64(ev.opts.EvalTimeout + helperCallGrace))
	if !answer(helperResponse{}) {
		return 0
	}
	for sc.Scan() {
		var req helperRequest
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			return helperExitProtocol
		}
		u, err := url.Parse(req.URL)
		var resp helperResponse
		switch {
		case err != nil:
			resp.Error = "pac: invalid URL"
		default:
			busySince.Store(time.Now().UnixNano())
			results, ferr := ev.FindProxy(u)
			busySince.Store(0)
			if ferr != nil {
				resp.Error = clipError(ferr.Error())
			} else {
				resp.Results = results
			}
		}
		if !answer(resp) {
			return 0
		}
	}
	if sc.Err() != nil {
		return helperExitProtocol
	}
	return 0
}

// watchHelper ends the helper process when its heap exceeds helperHeapLimit or a call
// has been running for longer than callLimit (a native built-in that never returns).
// It also ends a helper whose evaluator is gone, so nothing outlives it.
func watchHelper(busySince, callLimit *atomic.Int64) {
	sample := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
	t := time.NewTicker(helperWatchInterval)
	defer t.Stop()
	for range t.C {
		metrics.Read(sample)
		if sample[0].Value.Kind() == metrics.KindUint64 && sample[0].Value.Uint64() > helperHeapLimit {
			os.Exit(helperExitMemory)
		}
		if since := busySince.Load(); since != 0 && time.Since(time.Unix(0, since)) > time.Duration(callLimit.Load()) {
			os.Exit(helperExitTimeout)
		}
	}
}

// clipError bounds an error text passed back to the evaluator.
func clipError(s string) string {
	if len(s) <= maxHelperError {
		return s
	}
	return strings.ToValidUTF8(s[:maxHelperError], "") + "..."
}

// validResultHost reports whether h can be a proxy host: a name or address of at most
// 253 bytes without spaces or control characters.
func validResultHost(h string) bool {
	if h == "" || len(h) > 253 {
		return false
	}
	if net.ParseIP(h) != nil {
		return true
	}
	for i := 0; i < len(h); i++ {
		if c := h[i]; c <= ' ' || c == 0x7f || strings.IndexByte("/?#@[]\\", c) >= 0 {
			return false
		}
	}
	return true
}

// resultCache keeps FindProxy results per host for CacheTTL (negative: no caching).
type resultCache struct {
	ttl time.Duration
	now func() time.Time

	mu sync.Mutex
	m  map[string]cacheEntry
}

func newResultCache(opts Options) resultCache {
	return resultCache{ttl: opts.CacheTTL, now: opts.Now, m: map[string]cacheEntry{}}
}

func (c *resultCache) get(host string) ([]Result, bool) {
	if c.ttl < 0 {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	ent, ok := c.m[host]
	if !ok || c.now().After(ent.expires) {
		return nil, false
	}
	return ent.results, true
}

func (c *resultCache) put(host string, results []Result) {
	if c.ttl < 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) >= maxCachedHosts {
		now := c.now()
		for k, ent := range c.m {
			if now.After(ent.expires) {
				delete(c.m, k)
			}
		}
		if len(c.m) >= maxCachedHosts {
			clear(c.m)
		}
	}
	c.m[host] = cacheEntry{results: results, expires: c.now().Add(c.ttl)}
}
