// Package sitejob runs yt-dlp to resolve web pages (docs/SITE-MEDIA-SPEC.md §2.6, §4.7):
// a Resolver turns a page URL into what yt-dlp says about it (internal/sitemedia.Info),
// running at most two extractions at once (plus one look-ahead, §5.6) through the tools
// manager's copies, with a private work folder, cookie file and environment per run, and
// keeps the results in memory for a short time. Everything yt-dlp prints is untrusted:
// stdout goes to sitemedia.Parse, and stderr only yields a cleaned reason without URLs.
// Nothing of a run (format URLs, headers, cookies) is stored or logged.
package sitejob

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"sync"
	"time"

	"downex/internal/media"
	"downex/internal/sitemedia"
	"downex/internal/tools"
	"downex/internal/validate"
)

// Defaults of ResolverConfig (docs/SITE-MEDIA-SPEC.md §2.6, §4.7).
const (
	DefaultMaxConcurrent = 2
	DefaultTTL           = 15 * time.Minute
	DefaultFailureTTL    = 30 * time.Second
	// DefaultMaxEntries keeps at least the pages the extension keeps answers for (its
	// VIDEO_CACHE_SIZE, 50), so that look-aheads never push out a page whose qualities a
	// browser still shows (its Add would resolve it again).
	DefaultMaxEntries = 64
	DefaultTimeout    = 90 * time.Second
	DefaultMaxOutput  = sitemedia.MaxOutputBytes
	// maxStderr is how much of the end of yt-dlp's stderr is kept for classification.
	maxStderr = 64 << 10
)

// Tools is what the resolver needs of the tools manager (*tools.Manager): the copies
// to run (for a look-ahead that no user's request joined, only those ready now, never
// an install or a trial's copy: AcquireReady), the report of how they did (fallback and
// rollback, §4.5) and the child environment from its allow-list.
type Tools interface {
	Acquire(ctx context.Context) (*tools.Lease, error)
	AcquireReady(ctx context.Context) (*tools.Lease, error)
	Report(l *tools.Lease, outcome tools.Outcome)
	Env(s tools.EnvSpec) []string
}

var _ Tools = (*tools.Manager)(nil)

// ResolverConfig configures a Resolver; zero fields take the defaults.
type ResolverConfig struct {
	// Tools provides yt-dlp and Deno (required).
	Tools Tools
	// MaxConcurrent bounds the extractions that run at once (DefaultMaxConcurrent);
	// others wait within their callers' contexts.
	MaxConcurrent int
	// TTL is how long a resolved page is reused (DefaultTTL); FailureTTL how long a
	// refusal or failure answers again (DefaultFailureTTL); MaxEntries how many pages
	// are kept (DefaultMaxEntries).
	TTL, FailureTTL time.Duration
	MaxEntries      int
	// Timeout bounds one yt-dlp run, which is killed with its whole process tree when
	// it elapses (DefaultTimeout).
	Timeout time.Duration
	// MaxOutput bounds yt-dlp's stdout (DefaultMaxOutput); more kills it.
	MaxOutput int
	// Now is the clock of the cache and of sitemedia.Parse (nil: time.Now).
	Now func() time.Time
	// Log receives one line per extraction, with the page's scheme, host and path only
	// (nil: discarded).
	Log *slog.Logger
}

// Proxy is the route of a page for yt-dlp (daemon.SiteProxy): URL "" is direct, else
// "http://", "https://", "socks5://", "socks4://" or "socks4a://" with host and port
// and without user information; the credentials, when known, are apart. It is never
// logged.
type Proxy struct{ URL, Username, Password string }

func (p Proxy) hasLogin() bool { return p.Username != "" || p.Password != "" }

// Request is a page to resolve.
type Request struct {
	// PageURL is the page (an absolute http(s) URL).
	PageURL string
	// Cookie is the Cookie header the user allowed for the page (§6.3), and Referer
	// the page that embeds it; "" = none.
	Cookie, Referer string
	// Proxy is the page's route.
	Proxy Proxy
	// Fresh bypasses the cache: the page is resolved again (a running extraction is
	// joined), and the result replaces the cached one.
	Fresh bool
	// Prefetch marks a look-ahead while the user watches the page
	// (docs/SITE-MEDIA-SPEC.md §5.6). Like any request it is answered from the cache or
	// joins the running extraction of its page, and its result is cached for the probe,
	// add and run that follow. Otherwise it starts an extraction only while the resolver
	// is idle, in a turn of its own outside MaxConcurrent, so that at most one look-ahead
	// runs and none waits behind or delays a user's request; else it fails at once with
	// ErrBusy. Unless a user's request joined it, its extraction takes only copies that
	// are ready (Tools.AcquireReady), reports nothing to the tools manager (whose
	// fallback and rollback may install a copy, §4.5), never tries another copy after a
	// failure, and a failure that may be the copy's (site_error, bad options) is not
	// cached: the user's request that follows resolves the page itself. Prefetch is not
	// part of the cache key.
	Prefetch bool
}

// ErrClosed reports a Resolver that was closed (the daemon is shutting down).
var ErrClosed = fmt.Errorf("%w: Downex is shutting down", sitemedia.ErrSiteError)

// ErrBusy refuses a look-ahead (Request.Prefetch) that would have to wait for other
// extractions; nothing was started. It is site_resolving, so its caller retries later
// or leaves the page to the user's own request.
var ErrBusy = fmt.Errorf("%w: busy with other videos", sitemedia.ErrSiteResolving)

// Resolver resolves pages with yt-dlp. Extractions belong to the resolver, never to a
// caller: a caller whose context ends gets ErrSiteResolving while the extraction goes
// on, and its result is cached for the caller's retry. Callers of the same page (the
// same cacheKey) share one extraction, look-aheads (Request.Prefetch) included. It is
// safe for concurrent use.
type Resolver struct {
	cfg    ResolverConfig
	ctx    context.Context // the extractions'; cancelled by Close
	cancel context.CancelFunc
	slots  chan struct{} // one per running extraction a user's request started
	wg     sync.WaitGroup

	mu     sync.Mutex
	closed bool
	cache  *cache
	calls  map[string]*call // running extractions by cache key
}

// call is one extraction and the callers waiting for it.
type call struct {
	key      string
	prefetch bool          // a look-ahead started it: it runs outside the slots
	done     chan struct{} // closed once info and err are set
	info     *sitemedia.Info
	err      error

	// Guarded by Resolver.mu: waiters counts the callers still waiting; started is set
	// once yt-dlp runs (the extraction then finishes whatever its callers do); aborted
	// once every caller left before that, which ends the extraction unstarted; forUser
	// once a request that is not a look-ahead waits for it, which lets a failure try
	// another copy (extract).
	waiters                   int
	started, aborted, forUser bool
	abort                     context.CancelFunc
}

// NewResolver returns a resolver that runs the copies of cfg.Tools.
func NewResolver(cfg ResolverConfig) *Resolver {
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = DefaultMaxConcurrent
	}
	if cfg.TTL <= 0 {
		cfg.TTL = DefaultTTL
	}
	if cfg.FailureTTL <= 0 {
		cfg.FailureTTL = DefaultFailureTTL
	}
	if cfg.MaxEntries <= 0 {
		cfg.MaxEntries = DefaultMaxEntries
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.MaxOutput <= 0 {
		cfg.MaxOutput = DefaultMaxOutput
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Resolver{
		cfg: cfg, ctx: ctx, cancel: cancel, slots: make(chan struct{}, cfg.MaxConcurrent),
		cache: newCache(cfg.MaxEntries, cfg.TTL, cfg.FailureTTL), calls: make(map[string]*call),
	}
}

// Resolve returns what yt-dlp says about req's page: a cached result unless req.Fresh,
// else the result of the running extraction of the page or of a new one. The Info is
// shared: callers must not modify it.
//
// At ctx's end it fails with an error wrapping sitemedia.ErrSiteResolving (and ctx's
// error) while a started extraction goes on; an extraction that is still waiting for
// its turn when its last caller leaves is dropped. A look-ahead that would have to
// start an extraction while another runs or waits fails at once with ErrBusy. Refusals
// wrap the internal/media sentinels (encrypted, live, unsupported, malformed or too
// large output) or the sitemedia ones (site_unsupported, site_unavailable,
// auth_required, site_error); the tools manager's errors (tools_installing,
// tools_unavailable, site_media_off) are passed on. Error messages never contain URLs,
// cookies or proxy credentials.
func (r *Resolver) Resolve(ctx context.Context, req Request) (*sitemedia.Info, error) {
	if err := checkRequest(req); err != nil {
		return nil, err
	}
	key := cacheKey(req)
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, ErrClosed
	}
	if !req.Fresh {
		if res, ok := r.cache.get(key, r.cfg.Now()); ok {
			r.mu.Unlock()
			return res.info, res.err
		}
	}
	c := r.calls[key]
	if c == nil {
		if req.Prefetch && len(r.calls) > 0 {
			// Not idle: a look-ahead never waits for a turn, and never runs beside
			// another one or a user's extraction.
			r.mu.Unlock()
			return nil, ErrBusy
		}
		c = r.startLocked(key, req)
	}
	c.waiters++
	c.forUser = c.forUser || !req.Prefetch
	r.mu.Unlock()

	select {
	case <-c.done:
		return c.info, c.err
	case <-ctx.Done():
	}
	r.mu.Lock()
	c.waiters--
	if c.waiters == 0 && !c.started && !c.aborted {
		c.aborted = true
		if r.calls[key] == c {
			delete(r.calls, key)
		}
		c.abort()
	}
	r.mu.Unlock()
	return nil, &resolvingError{ctx.Err()}
}

// Close stops the running extractions (their process trees are killed and their work
// folders removed) and waits for them. Later calls of Resolve fail with ErrClosed.
func (r *Resolver) Close() error {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	r.cancel()
	r.wg.Wait()
	return nil
}

// startLocked starts the extraction of req's page under key.
func (r *Resolver) startLocked(key string, req Request) *call {
	sctx, abort := context.WithCancel(r.ctx)
	c := &call{key: key, prefetch: req.Prefetch, done: make(chan struct{}), abort: abort}
	r.calls[key] = c
	r.wg.Go(func() {
		defer abort()
		info, keep, err := r.extract(sctx, c, req)
		if r.ctx.Err() != nil && err != nil {
			err = ErrClosed
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.calls[key] == c {
			delete(r.calls, key)
		}
		if !c.aborted && keep && cacheable(err) {
			r.cache.put(&result{key: key, info: info, err: err, at: r.cfg.Now()})
		}
		c.info, c.err = info, err
		close(c.done)
	})
	return c
}

// cacheable reports a result that describes the page: not the tools' state, a
// cancellation or the end of the resolver.
func cacheable(err error) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded), errors.Is(err, ErrClosed),
		errors.Is(err, sitemedia.ErrToolsInstalling), errors.Is(err, sitemedia.ErrToolsUnavailable),
		errors.Is(err, sitemedia.ErrSiteMediaOff):
		return false
	}
	return true
}

// extract waits for a turn (a look-ahead takes none: it started on an idle resolver)
// and the tools, then resolves req's page (resolveWith). When the run fails in a way
// that may be the copy's fault (site_error, bad options), the tools manager may offer
// another copy: the managed one instead of a PATH copy, or the previous version instead
// of a new one (§4.5). The page is then tried once more with it, and that outcome is
// reported too.
//
// A look-ahead that no user's request joined is passive: it takes only copies that are
// ready (AcquireReady), its outcomes are not reported (a report may start an install
// or a trial), it never asks for another copy, and keep is false for a failure that may
// be the copy's, so that it is not cached and the user's request that follows resolves
// the page with the fallback of a user's extraction. A user's request that joins it
// makes it a user's extraction from then on.
func (r *Resolver) extract(ctx context.Context, c *call, req Request) (info *sitemedia.Info, keep bool, err error) {
	if !c.prefetch {
		select {
		case r.slots <- struct{}{}:
		case <-ctx.Done():
			return nil, true, ctx.Err()
		}
		defer func() { <-r.slots }()
	}
	acquire := r.cfg.Tools.Acquire
	if !r.forUser(c) {
		acquire = r.cfg.Tools.AcquireReady
	}
	lease, err := acquire(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil, true, ctx.Err()
		}
		return nil, true, err
	}
	r.mu.Lock()
	if c.aborted {
		r.mu.Unlock()
		lease.Release()
		return nil, true, context.Canceled
	}
	c.started = true
	r.mu.Unlock()

	info, manifests, last, err := r.resolveWith(ctx, c, lease, req, false)
	failed := lease.YtDlp
	forUser := r.forUser(c)
	if last.pending && forUser {
		// A user's request joined after the run: it counts as a user's run.
		r.cfg.Tools.Report(lease, last.outcome)
	}
	lease.Release()
	switch {
	case !last.againstCopy:
		return info, true, err
	case !forUser:
		return info, false, err
	}
	alt, aerr := r.cfg.Tools.Acquire(ctx)
	if aerr != nil {
		return info, true, err
	}
	defer alt.Release()
	if alt.YtDlp == failed {
		return info, true, err
	}
	r.cfg.Log.Info("trying the page with another yt-dlp", "page", logURL(req.PageURL), "managed", alt.YtDlpManaged,
		"version", alt.YtDlpVersion)
	info, _, _, err = r.resolveWith(ctx, c, alt, req, manifests)
	return info, true, err
}

// forUser reports whether a user's request waits for c: always for an extraction that
// a user's request started, for a look-ahead once one joined it.
func (r *Resolver) forUser(c *call) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return c.forUser
}

// lastRun is how the last run of resolveWith counts for the tools manager.
type lastRun struct {
	outcome tools.Outcome
	// againstCopy: it failed in a way that may be the copy's fault (site_error, bad
	// options).
	againstCopy bool
	// pending: its outcome says something about the copy but was not reported, since
	// no user's request had joined the look-ahead.
	pending bool
}

// resolveWith resolves req's page with l's yt-dlp and reports the outcome of each run
// to the tools manager while a user's request waits for c (forUser). Unless manifests,
// YouTube's HLS and DASH manifests are skipped (§4.7); a YouTube page that offers no
// direct MP4 video that way (needsManifests) is resolved once more with them, so that
// the manifest fallback (§3.11) still works. It returns the last run's result, whether
// that run kept the manifests, and how it counts (lastRun).
func (r *Resolver) resolveWith(ctx context.Context, c *call, l *tools.Lease, req Request, manifests bool) (info *sitemedia.Info,
	withManifests bool, last lastRun, err error) {
	for {
		var (
			outcome tools.Outcome
			report  bool
		)
		info, outcome, report, err = r.attempt(ctx, l, req, manifests)
		last = lastRun{outcome: outcome, againstCopy: report && outcome != tools.OK}
		if report {
			if r.forUser(c) {
				r.cfg.Tools.Report(l, outcome)
			} else {
				last.pending = true
			}
		}
		if manifests || err != nil || !needsManifests(info, req.PageURL) {
			return info, manifests, last, err
		}
		manifests = true
		r.cfg.Log.Info("no direct MP4 video: resolving the page again with its manifests", "page", logURL(req.PageURL))
	}
}

// youtubeExtractor is yt-dlp's extractor key of YouTube videos, the extractor whose
// arguments ("youtube:skip=...") skip the manifests.
const youtubeExtractor = "Youtube"

// needsManifests reports a YouTube page that, resolved with its manifests skipped,
// offers no direct MP4 video: sitemedia.Offers gives the manifest fallback or a
// refusal. Only a run with the manifests can tell whether the fallback has an MP4.
// Pages of other extractors are not affected by the skip.
func needsManifests(info *sitemedia.Info, pageURL string) bool {
	if info == nil || info.ExtractorKey != youtubeExtractor {
		return false
	}
	var host string
	if u, err := url.Parse(pageURL); err == nil {
		host = u.Host
	}
	off, err := sitemedia.Offers(info, host)
	return err != nil || off.Format != sitemedia.FormatHTTP
}

// attempt runs l's yt-dlp once for req's page in a new work folder, which it removes
// afterwards; manifests keeps YouTube's manifests. report is false when the run says
// nothing about the copy (Downex could not prepare it, or the resolver was closed).
func (r *Resolver) attempt(ctx context.Context, l *tools.Lease, req Request, manifests bool) (info *sitemedia.Info,
	outcome tools.Outcome, report bool, err error) {
	run, err := tools.NewRunDir(l.WorkRoot)
	if err != nil {
		return nil, tools.OK, false, fmt.Errorf("%w: cannot prepare the run: %v", sitemedia.ErrSiteError, err)
	}
	defer os.RemoveAll(run)
	cookies := ""
	if req.Cookie != "" {
		if cookies, err = writeCookieFile(run, req.PageURL, req.Cookie); err != nil {
			return nil, tools.OK, false, fmt.Errorf("%w: cannot prepare the run: %v", sitemedia.ErrSiteError, err)
		}
	}
	spec := tools.ExecSpec{
		Path: l.YtDlp,
		Args: buildArgs(argsSpec{Deno: l.Deno, CacheDir: l.YtDlpCache, Proxy: req.Proxy, Cookies: cookies,
			Referer: req.Referer, PageURL: req.PageURL, Manifests: manifests}),
		Env:         r.cfg.Tools.Env(envSpec(l, run, req.Proxy)),
		Dir:         run,
		Timeout:     r.cfg.Timeout,
		MaxStdout:   r.cfg.MaxOutput,
		MaxStderr:   maxStderr,
		MemoryLimit: tools.DefaultMemoryLimit,
	}
	start := time.Now()
	res, err := tools.Exec(ctx, spec)
	if err != nil && ctx.Err() != nil {
		return nil, tools.OK, false, ctx.Err()
	}
	info, outcome, err = r.interpret(res, err)
	attrs := []any{"page", logURL(req.PageURL), "elapsed", time.Since(start).Round(time.Millisecond), "exit", res.ExitCode,
		"managed", l.YtDlpManaged, "version", l.YtDlpVersion, "manifests", manifests, "lookahead", req.Prefetch}
	if err != nil {
		attrs = append(attrs, "error", err)
	}
	r.cfg.Log.Info("yt-dlp ran", attrs...)
	return info, outcome, true, err
}

// interpret maps a finished yt-dlp run to the page or its error (docs/SITE-MEDIA-SPEC.md
// §4.7) and to the outcome the tools manager is told: 0 with JSON is parsed; 1, or 0
// without JSON, is classified (classify); 2 (bad options), a crash, a timeout or a
// program that could not start are site_error; stdout beyond MaxOutput is
// media_invalid. Only site_error and bad options count against the copy.
func (r *Resolver) interpret(res tools.ExecResult, execErr error) (*sitemedia.Info, tools.Outcome, error) {
	switch {
	case execErr != nil:
		return nil, tools.SiteError, fmt.Errorf("%w: yt-dlp could not be started", sitemedia.ErrSiteError)
	case res.StdoutTruncated:
		return nil, tools.OK, fmt.Errorf("%w: yt-dlp printed more than %d bytes", media.ErrTooLarge, r.cfg.MaxOutput)
	case res.TimedOut:
		return nil, tools.SiteError, fmt.Errorf("%w: it took longer than %s", sitemedia.ErrSiteError, r.cfg.Timeout)
	case res.ExitCode == 0 && len(bytes.TrimSpace(res.Stdout)) > 0:
		info, err := sitemedia.Parse(res.Stdout, r.cfg.Now())
		return info, tools.OK, err
	case res.ExitCode == 2:
		reason := cleanReason(lastLine(res.StderrTail), false)
		if reason == "" {
			reason = "exit code 2"
		}
		return nil, tools.BadOptions, fmt.Errorf("%w: it rejected Downex's options (%s)", sitemedia.ErrSiteError, reason)
	case res.ExitCode == 0, res.ExitCode == 1:
		err := classify(res.StderrTail, res.ExitCode)
		if errors.Is(err, sitemedia.ErrSiteError) {
			return nil, tools.SiteError, err
		}
		return nil, tools.OK, err
	}
	return nil, tools.SiteError, fmt.Errorf("%w: %s", sitemedia.ErrSiteError, fallbackReason(res.StderrTail, res.ExitCode))
}

// resolvingError answers a caller whose context ended while its extraction goes on: it
// is sitemedia.ErrSiteResolving and also the context's error.
type resolvingError struct{ ctxErr error }

func (e *resolvingError) Error() string   { return sitemedia.ErrSiteResolving.Error() }
func (e *resolvingError) Unwrap() []error { return []error{sitemedia.ErrSiteResolving, e.ctxErr} }

// checkRequest validates a request: an http(s) page and Referer (validate.URL) without
// user information (it would reach yt-dlp's arguments, which other users of the machine
// can read) and a proxy URL of a scheme yt-dlp can use, with a host and without user
// information, path, query or fragment. Its errors never repeat the values.
func checkRequest(req Request) error {
	if validate.URL(req.PageURL) != nil || hasUserinfo(req.PageURL) {
		return fmt.Errorf("%w: the page address is not valid", sitemedia.ErrSiteError)
	}
	if req.Referer != "" && (validate.URL(req.Referer) != nil || hasUserinfo(req.Referer)) {
		return fmt.Errorf("%w: the Referer is not valid", sitemedia.ErrSiteError)
	}
	if req.Proxy.URL == "" {
		if req.Proxy.hasLogin() {
			return fmt.Errorf("%w: a proxy login without a proxy", sitemedia.ErrSiteError)
		}
		return nil
	}
	u, err := url.Parse(req.Proxy.URL)
	switch {
	case err != nil, u.User != nil, u.Hostname() == "", u.Port() == "", u.RawQuery != "", u.Fragment != "",
		u.Path != "" && u.Path != "/":
		return fmt.Errorf("%w: the proxy address is not valid", sitemedia.ErrSiteError)
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h", "socks4", "socks4a":
		return nil
	}
	return fmt.Errorf("%w: yt-dlp cannot use a %s proxy", sitemedia.ErrSiteError, u.Scheme)
}

// hasUserinfo reports a URL with user information (a user name or password).
func hasUserinfo(rawURL string) bool {
	u, err := url.Parse(rawURL)
	return err != nil || u.User != nil
}

// logURL is rawURL's scheme, host and path, which logs may keep (§6.1).
func logURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "(unparsable URL)"
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path}).String()
}
