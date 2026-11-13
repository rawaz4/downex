// Package pac evaluates proxy auto-configuration (PAC) scripts in an isolated
// JavaScript runtime with the standard FindProxyForURL helpers, a per-call timeout and
// a per-host result cache. It performs no I/O of its own: the caller fetches the PAC
// script and passes its source to New or NewIsolated. DNS- and host-dependent helpers
// (dnsResolve, isResolvable, myIpAddress) are injected through Options so they can be
// bounded and faked in tests. A script from the network is evaluated with NewIsolated
// (isolate.go): goja's native built-ins can exhaust memory or run without end in a way
// that neither the timeout nor recover stops, so New's in-process runtime is for the
// helper process and for tests.
package pac

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
)

// Limits.
const (
	// DefaultEvalTimeout bounds one FindProxyForURL call (docs/M6-SPEC.md §2).
	DefaultEvalTimeout = time.Second
	// DefaultCacheTTL is how long a host's result is reused.
	DefaultCacheTTL = 5 * time.Minute
	// MaxScriptBytes bounds an accepted PAC script.
	MaxScriptBytes = 1 << 20
	// maxStackSize bounds JavaScript recursion depth.
	maxStackSize = 2048
	// dnsTimeout bounds each DNS lookup a helper performs.
	dnsTimeout = 5 * time.Second
)

// ErrTooLarge reports a PAC script larger than MaxScriptBytes.
var ErrTooLarge = errors.New("pac: script too large")

// errInterrupted is the value passed to Runtime.Interrupt; it is distinct from any
// value a script can throw.
var errInterrupted = errors.New("pac: evaluation timed out")

// Options configures an Evaluator. A zero Options uses the real DNS resolver, the real
// clock and the default limits.
type Options struct {
	// Now is the clock for weekdayRange, dateRange and timeRange (default time.Now).
	Now func() time.Time
	// LookupIP resolves a host for isResolvable, dnsResolve and isInNet (default: the
	// system resolver, bounded by a 5 s timeout). It must return quickly and be safe
	// for concurrent use.
	LookupIP func(host string) ([]net.IP, error)
	// LocalIP returns the address myIpAddress reports (default: the first non-loopback
	// IPv4 address of the host, else 127.0.0.1).
	LocalIP func() net.IP
	// EvalTimeout bounds one FindProxyForURL call (default DefaultEvalTimeout).
	EvalTimeout time.Duration
	// CacheTTL is how long a host's result is cached (default DefaultCacheTTL; negative
	// disables caching).
	CacheTTL time.Duration
}

func (o Options) withDefaults() Options {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.LookupIP == nil {
		o.LookupIP = defaultLookupIP
	}
	if o.LocalIP == nil {
		o.LocalIP = defaultLocalIP
	}
	if o.EvalTimeout <= 0 {
		o.EvalTimeout = DefaultEvalTimeout
	}
	if o.CacheTTL == 0 {
		o.CacheTTL = DefaultCacheTTL
	}
	return o
}

// Evaluator runs one compiled PAC script. Its methods are safe for concurrent use
// (calls are serialised onto the single runtime).
type Evaluator struct {
	opts Options

	mu      sync.Mutex
	vm      *goja.Runtime
	find    goja.Callable
	timer   *time.Timer
	cacheMu sync.Mutex
	cache   map[string]cacheEntry
}

type cacheEntry struct {
	results []Result
	expires time.Time
}

// New compiles script, installs the PAC helpers in a fresh runtime of this process and
// verifies that it defines FindProxyForURL. Running the top-level script is bounded by
// EvalTimeout (JavaScript loops; native built-ins are not interruptible, see
// NewIsolated for untrusted scripts).
func New(script string, opts Options) (*Evaluator, error) {
	if len(script) > MaxScriptBytes {
		return nil, fmt.Errorf("%w: %d bytes", ErrTooLarge, len(script))
	}
	opts = opts.withDefaults()
	e := &Evaluator{opts: opts, cache: map[string]cacheEntry{}}
	vm := goja.New()
	vm.SetMaxCallStackSize(maxStackSize)
	// A PAC runtime has no host objects beyond the helpers: no timers, console,
	// require or network except the injected, bounded helpers.
	h := &helpers{opts: opts}
	if err := h.install(vm); err != nil {
		return nil, err
	}
	prog, err := goja.Compile("proxy.pac", script, true)
	if err != nil {
		return nil, fmt.Errorf("pac: compile: %w", err)
	}
	e.vm = vm
	if err := e.runBounded(func() error {
		_, rerr := vm.RunProgram(prog)
		return rerr
	}); err != nil {
		return nil, fmt.Errorf("pac: run script: %w", err)
	}
	fn := vm.Get("FindProxyForURL")
	if fn == nil {
		return nil, errors.New("pac: script does not define FindProxyForURL")
	}
	find, ok := goja.AssertFunction(fn)
	if !ok {
		return nil, errors.New("pac: FindProxyForURL is not a function")
	}
	e.find = find
	return e, nil
}

// FindProxy evaluates FindProxyForURL(u, u.Host) and returns the ordered proxy routes
// it names. Results are cached per host for CacheTTL. A script error or timeout returns
// an error; the caller should then fall back to a direct connection or another mode.
func (e *Evaluator) FindProxy(u *url.URL) ([]Result, error) {
	host := strings.ToLower(u.Hostname())
	if r, ok := e.cached(host); ok {
		return r, nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	var raw goja.Value
	err := e.runBounded(func() error {
		v, cerr := e.find(goja.Undefined(), e.vm.ToValue(ScriptURL(u)), e.vm.ToValue(host))
		if cerr != nil {
			return cerr
		}
		raw = v
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("pac: FindProxyForURL(%q): %w", host, err)
	}
	results, err := parseResults(stringOf(raw))
	if err != nil {
		return nil, err
	}
	e.store(host, results)
	return results, nil
}

// ScriptURL is the URL FindProxyForURL gets for u: never user information or a
// fragment, and for https and wss targets only "scheme://host[:port]/". Their path and
// query (where signed download links carry tokens) travel encrypted, so no proxy
// decision needs them, and an untrusted script (WPAD, a pac_url over plain http) could
// send them out through dnsResolve or isResolvable. http targets keep path and query, as
// scripts may route by them.
func ScriptURL(u *url.URL) string {
	switch scheme := strings.ToLower(u.Scheme); scheme {
	case "https", "wss":
		return (&url.URL{Scheme: scheme, Host: u.Host, Path: "/"}).String()
	}
	safe := *u
	safe.User = nil
	safe.Fragment, safe.RawFragment = "", ""
	return safe.String()
}

// runBounded runs fn with the runtime, interrupting it after EvalTimeout. fn must be
// the only code touching e.vm for its duration (the caller holds e.mu, except New
// which runs before the evaluator is shared).
func (e *Evaluator) runBounded(fn func() error) error {
	done := make(chan struct{})
	timer := time.AfterFunc(e.opts.EvalTimeout, func() {
		e.vm.Interrupt(errInterrupted)
	})
	var err error
	func() {
		defer close(done)
		err = fn()
	}()
	timer.Stop()
	e.vm.ClearInterrupt()
	var ie *goja.InterruptedError
	if errors.As(err, &ie) {
		return errInterrupted
	}
	return err
}

func (e *Evaluator) cached(host string) ([]Result, bool) {
	if e.opts.CacheTTL < 0 {
		return nil, false
	}
	e.cacheMu.Lock()
	defer e.cacheMu.Unlock()
	ent, ok := e.cache[host]
	if !ok || e.opts.Now().After(ent.expires) {
		return nil, false
	}
	return ent.results, true
}

func (e *Evaluator) store(host string, results []Result) {
	if e.opts.CacheTTL < 0 {
		return
	}
	e.cacheMu.Lock()
	defer e.cacheMu.Unlock()
	e.cache[host] = cacheEntry{results: results, expires: e.opts.Now().Add(e.opts.CacheTTL)}
}

func stringOf(v goja.Value) string {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return ""
	}
	return v.String()
}

func defaultLookupIP(host string) ([]net.IP, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dnsTimeout)
	defer cancel()
	return net.DefaultResolver.LookupIP(ctx, "ip", host)
}

func defaultLocalIP() net.IP {
	addrs, err := net.InterfaceAddrs()
	if err == nil {
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok {
				if v4 := ipnet.IP.To4(); v4 != nil && !v4.IsLoopback() {
					return v4
				}
			}
		}
	}
	return net.IPv4(127, 0, 0, 1)
}
