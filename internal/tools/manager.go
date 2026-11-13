package tools

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"downex/internal/sitemedia"
	"downex/internal/update"
)

// Config configures a Manager.
type Config struct {
	// Dir is the tools folder, an absolute path (required; platform.ToolsDir for the
	// user's own daemon). It is created when missing.
	Dir string
	// Client downloads releases: https only, through Downex's proxy
	// (update.NewHTTPClient). nil: the manager never downloads (a daemon other than the
	// user's own) and uses PATH copies or copies already in Dir.
	Client *http.Client
	// ReleaseBase replaces "https://github.com" (e2e tests: a local TLS server).
	ReleaseBase string
	// YtDlpKeys are armored OpenPGP public keys that yt-dlp's checksum signatures must
	// verify with; nil pins the embedded yt-dlp key.
	YtDlpKeys [][]byte
	// LookPath finds programs on the daemon's PATH (nil: exec.LookPath).
	LookPath func(string) (string, error)
	// Now is the clock (nil: time.Now).
	Now func() time.Time
	// GOOS and GOARCH select the release assets ("" = the running system).
	GOOS, GOARCH string
	// PassEnvPrefix: variables of the daemon's environment whose names start with it are
	// passed on to children (e2etest builds: "DOWNEX_E2E_"; "" = none).
	PassEnvPrefix string
}

// State is a tool's state; the values are ToolState's (proto).
type State int

const (
	StateUnspecified State = iota
	StateMissing
	StateInstalling // also while updating: a ready copy keeps working meanwhile
	StateReady
	StateFailed
	StateDisabled // site media is off, or no build for this platform (Deno)
)

// Info is the status of one tool; its fields mirror ToolInfo (proto).
type Info struct {
	Name          string // ToolYtDlp or ToolDeno
	State         State
	Version       string // of the copy in use ("" = none)
	Path          string // absolute path of the copy in use
	Managed       bool   // a copy in the tools folder (false: found on PATH)
	LatestVersion string // last release seen ("" = not checked)
	CheckedAt     time.Time
	BytesDone     int64 // while installing
	BytesTotal    int64 // while installing; -1 = unknown
	Error         string
	ErrorCode     string
	Verified      string // "openpgp+sha256" | "sha256+authenticode" | "sha256" | "" (PATH copy)
	// Signer is the Authenticode signer of a managed deno.exe (not in the proto yet).
	Signer string
}

// Snapshot is the status of both tools; it mirrors ToolsStatus (proto).
type Snapshot struct {
	Tools   []Info // yt-dlp, then Deno
	Enabled bool   // site media is on
	// Ready: yt-dlp can run, and Deno can run or has no build for this platform. While
	// a ready copy is being updated it stays true.
	Ready bool
	// Discovering: the copies have not been looked for yet since the manager started
	// (the first discovery has not ended), so Ready may still turn true without an
	// install. Not in the API.
	Discovering bool
}

// Outcome is what an extraction with a Lease's copies did (Report).
type Outcome int

const (
	// OK: yt-dlp answered (whatever the page).
	OK Outcome = iota
	// SiteError: yt-dlp failed in a way classified site_error.
	SiteError
	// BadOptions: yt-dlp exited with code 2 (it rejected Downex's arguments).
	BadOptions
)

// Manager finds, downloads, verifies, installs and updates yt-dlp and Deno
// (docs/SITE-MEDIA-SPEC.md §4). It has the method set of daemon.Tools (Configure,
// Status, Ensure, Install, CheckUpdate, Subscribe; cmd/downexd adapts its types) plus
// Acquire, Report and Close for the resolver. Installs, discoveries and cleanups are
// owned by the manager, never by the request that started them.
type Manager struct {
	cfg    Config
	h      hooks
	lay    layout
	fetch  *fetcher // nil: never download
	assets platformAssets
	keys   []pgpKey
	keyErr error // the embedded yt-dlp key is missing: yt-dlp downloads fail their check

	ctx    context.Context // ends at Close
	cancel context.CancelFunc
	wg     sync.WaitGroup

	opMu     sync.Mutex // with .lock: install runs, cleanups and switches
	saveMu   sync.Mutex // serializes writes of tools.json
	notifyMu sync.Mutex // serializes subscriber calls

	mu            sync.Mutex
	closed        bool
	enabled       bool
	autoUpdate    bool
	channel       string
	configured    bool
	st            stateFile
	rt            [2]toolRT
	disc          chan struct{} // closed when the running discovery ends (nil = none runs)
	discAgain     bool          // the running discovery repeats once more
	discovered    bool          // a discovery has completed
	run           *installRun
	switchPending bool // the channel may need a switch once discovery and the run end
	failures      int  // consecutive failed runs that left the tools unusable
	retryAt       time.Time
	lastErr       error
	leases        map[string]int // version folder → leases holding it
	trial         *trial
	newVer        string // a managed yt-dlp just switched to, watched for a rollback
	newFails      int
	lastExtra     time.Time // last extra update check after a site_error
	subs          map[uint64]func()
	nextSub       uint64
	lastNote      time.Time // last progress notification (real time)
}

// Indexes of Manager.rt.
const (
	ytIdx   = 0
	denoIdx = 1
)

var toolNames = [2]string{ToolYtDlp, ToolDeno}

// toolRT is what the manager knows of one tool at run time.
type toolRT struct {
	path       *pathCopy // the PATH copy that serves (nil = none)
	managedOK  bool      // the state's managed copy passed its size and SHA-256 check
	noBuild    bool      // upstream publishes no build for this platform
	installing bool
	bytesDone  int64
	bytesTotal int64
	err        *Error // the last failure; nil after a success
}

// pathCopy is a copy found on PATH.
type pathCopy struct{ path, version string }

// hooks are the parts tests replace.
type hooks struct {
	authenticode   func(path string) (signer string, err error)
	musl           func() bool
	freeSpace      func(dir string) (uint64, error)
	headerTimeout  time.Duration
	stallWindow    time.Duration
	stallMinRate   int64
	installTimeout time.Duration
	pathTimeout    time.Duration // --version of a PATH copy
	smokeYtDlp     time.Duration
	smokeDeno      time.Duration
	lockPoll       time.Duration
	backoffBase    time.Duration
	backoffMax     time.Duration
}

func defaultHooks() hooks {
	return hooks{
		authenticode: func(p string) (string, error) {
			s, err := update.VerifyAuthenticode(p)
			return s.Subject, err
		},
		musl:           detectMusl,
		freeSpace:      freeSpace,
		headerTimeout:  headerTimeout,
		stallWindow:    stallWindow,
		stallMinRate:   stallMinRate,
		installTimeout: installTimeout,
		pathTimeout:    10 * time.Second,
		smokeYtDlp:     60 * time.Second,
		smokeDeno:      30 * time.Second,
		lockPoll:       100 * time.Millisecond,
		backoffBase:    30 * time.Second,
		backoffMax:     30 * time.Minute,
	}
}

// Rules of §4.1 and §4.5.
const (
	// maxPathAge: a PATH yt-dlp older than this is not used (yt-dlp's own staleness rule).
	maxPathAge = 90 * 24 * time.Hour
	// denoCheckInterval: the daily check looks for a new Deno this often.
	denoCheckInterval = 30 * 24 * time.Hour
	// extraCheckInterval bounds the extra update checks after site errors.
	extraCheckInterval = time.Hour
	// rollbackAfter: a new yt-dlp whose first extractions all fail this often is rolled back.
	rollbackAfter = 3
	// progressInterval bounds progress notifications (4 per second).
	progressInterval = 250 * time.Millisecond
)

// denoSigner is the Authenticode subject deno.exe must be signed by. It is empty until
// a real deno.exe confirms it (docs/SITE-MEDIA-SPEC.md §9): meanwhile any trusted
// signer is accepted and recorded.
const denoSigner = ""

// New returns a manager of the tools folder cfg.Dir: it creates the folder, reads
// tools.json and removes what an earlier run left (partial downloads, work folders,
// unfinished and unused version folders). Nothing runs or is downloaded until the
// manager is configured or used; site media starts enabled with automatic updates on
// the default channel.
func New(cfg Config) (*Manager, error) { return newManager(cfg, defaultHooks()) }

func newManager(cfg Config, h hooks) (*Manager, error) {
	if !filepath.IsAbs(cfg.Dir) {
		return nil, fmt.Errorf("tools: folder %q is not an absolute path", cfg.Dir)
	}
	cfg.Dir = filepath.Clean(cfg.Dir)
	if cfg.ReleaseBase == "" {
		cfg.ReleaseBase = "https://github.com"
	}
	cfg.ReleaseBase = strings.TrimRight(cfg.ReleaseBase, "/")
	if u, err := url.Parse(cfg.ReleaseBase); err != nil || u.Scheme != "https" || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("tools: release base %q is not an https address", cfg.ReleaseBase)
	}
	if cfg.LookPath == nil {
		cfg.LookPath = exec.LookPath
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.GOOS == "" {
		cfg.GOOS = runtime.GOOS
	}
	if cfg.GOARCH == "" {
		cfg.GOARCH = runtime.GOARCH
	}
	keys, keyErr := pinnedKeys(cfg.YtDlpKeys)
	if keyErr != nil && cfg.YtDlpKeys != nil {
		return nil, keyErr
	}
	lay := layout{dir: cfg.Dir, goos: cfg.GOOS}
	if err := lay.prepare(); err != nil {
		return nil, err
	}
	m := &Manager{
		cfg: cfg, h: h, lay: lay, keys: keys, keyErr: keyErr,
		assets:     assetsFor(cfg.GOOS, cfg.GOARCH, h.musl()),
		enabled:    true,
		autoUpdate: true,
		channel:    DefaultChannel,
		st:         loadState(lay),
		leases:     map[string]int{},
		subs:       map[uint64]func(){},
	}
	if cfg.Client != nil {
		m.fetch = &fetcher{client: secureClient(cfg.Client), headerTimeout: h.headerTimeout,
			stallWindow: h.stallWindow, stallMinBytes: h.stallMinRate * int64(h.stallWindow/time.Second)}
	}
	m.rt[ytIdx].noBuild = !m.assets.hasYtDlp()
	m.rt[denoIdx].noBuild = m.assets.Deno == ""
	m.ctx, m.cancel = context.WithCancel(context.Background())
	m.startupCleanup()
	return m, nil
}

// startupCleanup removes what an earlier run left, unless another process holds the
// folder's lock.
func (m *Manager) startupCleanup() {
	f, err := os.OpenFile(m.lay.lock(), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	if busy, err := tryLock(f); err != nil || busy {
		return
	}
	defer unlockFile(f)
	clearDir(m.lay.tmp())
	clearDir(m.lay.run())
	if tmps, err := filepath.Glob(filepath.Join(m.lay.dir, ".tools-*.tmp")); err == nil {
		for _, p := range tmps {
			_ = os.Remove(p)
		}
	}
	m.opMu.Lock()
	m.restoreSetAside()
	m.cleanupHeld()
	m.opMu.Unlock()
}

// restoreSetAside puts back, with the folder's lock held, a version folder that a stop
// between placeVersion's two renames left only as <version>.old-<random>: the current
// or previous copy of a tool whose folder is missing, when exactly one such folder
// exists (otherwise the next use installs it again). Discovery checks it as usual.
func (m *Manager) restoreSetAside() {
	type copyOf struct{ tool, version string }
	var copies []copyOf
	m.mu.Lock()
	for _, tool := range toolNames {
		rec := m.st.record(tool)
		if rec.Version != "" {
			copies = append(copies, copyOf{tool, rec.Version})
		}
		if rec.Previous != nil {
			copies = append(copies, copyOf{tool, rec.Previous.Version})
		}
	}
	m.mu.Unlock()
	for _, c := range copies {
		final := m.lay.versionDir(c.tool, c.version)
		if _, err := os.Lstat(final); !errors.Is(err, os.ErrNotExist) {
			continue
		}
		entries, err := os.ReadDir(m.lay.toolDir(c.tool))
		if err != nil {
			continue
		}
		var aside []string
		for _, e := range entries {
			if e.IsDir() && strings.HasPrefix(e.Name(), c.version+oldInfix) {
				aside = append(aside, filepath.Join(m.lay.toolDir(c.tool), e.Name()))
			}
		}
		if len(aside) == 1 {
			_ = renameRetry(aside[0], final)
		}
	}
}

// Close stops the manager: running installs, discoveries and their children are
// cancelled and waited for. Later calls fail with ErrClosed.
func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	m.mu.Unlock()
	m.cancel()
	m.wg.Wait()
	return nil
}

// Configure applies the settings site_media, tools_auto_update and tools_channel (""
// and unknown channels read as DefaultChannel). Turning site media off cancels a
// running install. A change runs discovery again; a channel change installs the latest
// release of the new channel at once when the managed yt-dlp serves (§4.5).
func (m *Manager) Configure(enabled, autoUpdate bool, channel string) {
	channel = NormalizeChannel(channel)
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	changed := !m.configured || m.enabled != enabled || m.autoUpdate != autoUpdate || m.channel != channel
	m.enabled, m.autoUpdate, m.channel, m.configured = enabled, autoUpdate, channel, true
	if !enabled {
		if m.run != nil {
			m.run.cancel()
		}
		m.trial = nil
	}
	if changed && enabled {
		m.startDiscoveryLocked(true)
	}
	m.switchPending = enabled
	m.trySwitchLocked()
	m.mu.Unlock()
	if changed {
		m.notify()
	}
}

// trySwitchLocked installs the configured channel's latest yt-dlp when the managed copy
// serves and comes from another channel, once discovery and any running install have
// ended (switchPending waits for them).
func (m *Manager) trySwitchLocked() {
	if !m.switchPending || !m.enabled || !m.discovered || m.disc != nil || m.run != nil || m.closed {
		return
	}
	m.switchPending = false
	yt := &m.rt[ytIdx]
	if m.fetch == nil || yt.path != nil || !yt.managedOK || NormalizeChannel(m.st.YtDlp.Channel) == m.channel {
		return
	}
	m.startRunLocked(runMode{switchChannel: true})
}

// Status returns the tools' current status.
func (m *Manager) Status() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshotLocked()
}

func (m *Manager) snapshotLocked() Snapshot {
	s := Snapshot{Enabled: m.enabled, Ready: m.readyLocked(), Discovering: !m.discovered}
	for i := range toolNames {
		s.Tools = append(s.Tools, m.infoLocked(i))
	}
	return s
}

func (m *Manager) infoLocked(i int) Info {
	name, rt := toolNames[i], &m.rt[i]
	rec := m.st.record(name)
	in := Info{Name: name, LatestVersion: rec.Latest, CheckedAt: rec.CheckedAt}
	switch {
	case rt.path != nil:
		in.Version, in.Path = rt.path.version, rt.path.path
	case rt.managedOK:
		in.Version, in.Path, in.Managed = rec.Version, m.lay.exe(name, rec.Version), true
		in.Verified, in.Signer = rec.Verified, rec.Signer
	}
	if rt.err != nil {
		in.Error, in.ErrorCode = rt.err.Error(), rt.err.Code
	}
	switch {
	case !m.enabled:
		in.State = StateDisabled
	case rt.installing:
		in.State, in.BytesDone, in.BytesTotal = StateInstalling, rt.bytesDone, rt.bytesTotal
	case in.Version != "":
		in.State = StateReady
	case rt.noBuild:
		// Deno is optional where it has no build; yt-dlp is not.
		in.State = StateFailed
		if i == denoIdx {
			in.State = StateDisabled
		}
		e := m.noBuildError(i)
		in.Error, in.ErrorCode = e.Error(), e.Code
	case rt.err != nil:
		in.State = StateFailed
	default:
		in.State = StateMissing
	}
	return in
}

// hasCopyLocked reports whether tool i has a copy to run.
func (m *Manager) hasCopyLocked(i int) bool { return m.rt[i].path != nil || m.rt[i].managedOK }

// readyLocked: site media is on, yt-dlp can run, and Deno can run or has no build.
func (m *Manager) readyLocked() bool {
	return m.enabled && m.hasCopyLocked(ytIdx) && (m.hasCopyLocked(denoIdx) || m.rt[denoIdx].noBuild)
}

// noBuildError is the error of tool i having no build for this platform.
func (m *Manager) noBuildError(i int) *Error {
	return &Error{Tool: toolNames[i], Code: CodeUnsupportedPlatform, GOOS: m.cfg.GOOS}
}

// notReadyLocked is why the tools cannot be used.
func (m *Manager) notReadyLocked() error {
	for i := range toolNames {
		switch {
		case m.hasCopyLocked(i) || i == denoIdx && m.rt[i].noBuild:
		case m.rt[i].noBuild:
			return m.noBuildError(i)
		case m.rt[i].err != nil:
			return m.rt[i].err
		}
	}
	if m.lastErr != nil {
		return m.lastErr
	}
	if m.fetch == nil {
		return ErrNoDownloads
	}
	return errors.New("yt-dlp or Deno is missing")
}

// percentLocked is the progress of the running downloads (0–99).
func (m *Manager) percentLocked() int {
	var done, total int64
	for i := range m.rt {
		if rt := &m.rt[i]; rt.installing && rt.bytesTotal > 0 {
			done += min(rt.bytesDone, rt.bytesTotal)
			total += rt.bytesTotal
		}
	}
	if total == 0 {
		return 0
	}
	return int(min(done*100/total, 99))
}

// Subscribe calls fn after each change of the status (progress at most 4 times per
// second) until cancel is called. Calls are serialized; fn must not block, and of the
// manager's methods it may call only Status.
func (m *Manager) Subscribe(fn func()) (cancel func()) {
	m.mu.Lock()
	id := m.nextSub
	m.nextSub++
	m.subs[id] = fn
	m.mu.Unlock()
	return sync.OnceFunc(func() {
		m.mu.Lock()
		delete(m.subs, id)
		m.mu.Unlock()
	})
}

// notify calls the subscribers.
func (m *Manager) notify() {
	m.notifyMu.Lock()
	defer m.notifyMu.Unlock()
	m.mu.Lock()
	fns := make([]func(), 0, len(m.subs))
	for _, fn := range m.subs {
		fns = append(fns, fn)
	}
	m.lastNote = time.Now()
	m.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

// progressed notifies about download progress, at most every progressInterval.
func (m *Manager) progressed() {
	m.mu.Lock()
	due := time.Since(m.lastNote) >= progressInterval
	m.mu.Unlock()
	if due {
		m.notify()
	}
}

// Ensure returns nil when yt-dlp and Deno (where it has a build) can run. Otherwise it
// starts the install (or joins the running one) and waits for it: at ctx's end it fails
// with sitemedia.ErrToolsInstalling and the progress ("downloading yt-dlp and Deno
// (34%)") while the install goes on. It fails with sitemedia.ErrSiteMediaOff when site
// media is off, and with sitemedia.ErrToolsUnavailable (wrapping the cause) when the
// install failed, after a recent failure (backoff: 30 s doubling to 30 min) or when
// this manager never downloads.
func (m *Manager) Ensure(ctx context.Context) error {
	waited := false // a run that installs missing tools has ended
	for {
		m.mu.Lock()
		switch {
		case m.closed:
			m.mu.Unlock()
			return unavailable(ErrClosed)
		case !m.enabled:
			m.mu.Unlock()
			return sitemedia.ErrSiteMediaOff
		case !m.discovered:
			ch := m.startDiscoveryLocked(false)
			m.mu.Unlock()
			select {
			case <-ch:
				continue
			case <-ctx.Done():
				return installing(0)
			}
		case m.readyLocked():
			m.mu.Unlock()
			return nil
		}
		r := m.run
		if r == nil {
			if waited {
				err := m.notReadyLocked()
				m.mu.Unlock()
				return unavailable(err)
			}
			if err := m.cannotInstallLocked(); err != nil {
				m.mu.Unlock()
				return unavailable(err)
			}
			if m.cfg.Now().Before(m.retryAt) {
				err := m.notReadyLocked()
				m.mu.Unlock()
				return unavailable(err)
			}
			r = m.startRunLocked(runMode{installMissing: true})
		}
		m.mu.Unlock()
		select {
		case <-r.done:
			waited = waited || r.mode.installMissing
		case <-ctx.Done():
			m.mu.Lock()
			p := m.percentLocked()
			m.mu.Unlock()
			return installing(p)
		}
	}
}

// cannotInstallLocked is why a missing tool cannot be installed at all, or nil.
func (m *Manager) cannotInstallLocked() error {
	if !m.hasCopyLocked(ytIdx) && m.rt[ytIdx].noBuild {
		return m.noBuildError(ytIdx)
	}
	if m.fetch == nil {
		return ErrNoDownloads
	}
	return nil
}

// Install starts the install of missing tools (or joins the running one) and, with
// update, also looks for newer releases of the managed copies. It is a user action: it
// ignores the backoff of automatic installs. It returns when the install ends, or with
// the current status and no error at ctx's end (the install goes on). Errors:
// sitemedia.ErrSiteMediaOff; sitemedia.ErrToolsUnavailable wrapping the failure, or
// ErrNoDownloads for a manager that never downloads.
func (m *Manager) Install(ctx context.Context, update bool) (Snapshot, error) {
	m.mu.Lock()
	switch {
	case m.closed:
		s := m.snapshotLocked()
		m.mu.Unlock()
		return s, unavailable(ErrClosed)
	case !m.enabled:
		s := m.snapshotLocked()
		m.mu.Unlock()
		return s, sitemedia.ErrSiteMediaOff
	case !update && m.discovered && m.readyLocked():
		s := m.snapshotLocked()
		m.mu.Unlock()
		return s, nil
	case m.fetch == nil:
		s := m.snapshotLocked()
		m.mu.Unlock()
		return s, unavailable(ErrNoDownloads)
	}
	r := m.run
	if r == nil {
		r = m.startRunLocked(runMode{installMissing: true, update: update})
	}
	m.mu.Unlock()
	select {
	case <-r.done:
	case <-ctx.Done():
		return m.Status(), nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.snapshotLocked()
	switch {
	case r.err != nil:
		return s, unavailable(r.err)
	case !m.readyLocked():
		return s, unavailable(m.notReadyLocked())
	}
	return s, nil
}

// Lease is the copies one extraction runs, held until Release: a managed version folder
// is never removed or replaced while a lease holds it.
type Lease struct {
	YtDlp        string // absolute path of the yt-dlp to run
	Deno         string // absolute path of the Deno to run ("" = none: no build for this platform)
	YtDlpCache   string // yt-dlp's --cache-dir
	DenoCache    string // DENO_DIR
	WorkRoot     string // where per-run folders are made (NewRunDir)
	YtDlpVersion string
	YtDlpManaged bool // false: the yt-dlp found on PATH

	m        *Manager
	dirs     []string  // version folders held
	alt      trialKind // this lease is the alternative copy of a fallback or rollback trial
	once     sync.Once
	reported atomic.Bool
}

// Release lets the lease's version folders go. It is safe on nil and more than once.
func (l *Lease) Release() {
	if l == nil || l.m == nil {
		return
	}
	l.once.Do(func() { l.m.release(l) })
}

// Acquire returns the copies to run, after Ensure. After an extraction was reported
// with SiteError or BadOptions, the next Acquire may return another yt-dlp (its YtDlp
// differs from the failed lease's): the managed copy instead of a PATH copy (installed
// first if needed), or the previous version instead of a new one that failed its first
// extractions. The resolver retries the failed page once with it and reports that
// outcome too (§4.5).
func (m *Manager) Acquire(ctx context.Context) (*Lease, error) {
	if err := m.Ensure(ctx); err != nil {
		return nil, err
	}
	m.mu.Lock()
	if t := m.trial; t != nil && !t.given && t.kind == trialManaged && !m.rt[ytIdx].managedOK {
		if m.fetch == nil || m.closed {
			m.trial = nil
		} else {
			r := m.run
			if r == nil {
				r = m.startRunLocked(runMode{forceManaged: true})
			}
			m.mu.Unlock()
			select {
			case <-r.done:
			case <-ctx.Done():
			}
			m.mu.Lock()
			if !m.rt[ytIdx].managedOK && m.run == nil && m.trial == t {
				m.trial = nil // the managed copy could not be installed: keep the PATH copy
			}
		}
	}
	if t := m.trial; t != nil && !t.given && t.kind == trialPrevious && !t.verified {
		// Only verified managed copies run (§6.4): the previous copy is hashed again
		// before it is offered, outside the lock; a changed one ends the trial.
		var prev entry
		if p := m.st.YtDlp.Previous; p != nil {
			prev = *p
		}
		m.mu.Unlock()
		ok := m.verifyEntry(ToolYtDlp, prev)
		m.mu.Lock()
		if m.trial == t {
			if p := m.st.YtDlp.Previous; ok && p != nil && p.Version == prev.Version {
				t.verified = true
			} else {
				m.trial = nil
			}
		}
	}
	defer m.mu.Unlock()
	return m.leaseLocked(true)
}

// AcquireReady is Acquire for a look-ahead (docs/SITE-MEDIA-SPEC.md §5.6, §6.6): it
// returns the copies that can run now. It never installs or updates a tool and never
// waits for an install; it waits only for a discovery of the copies that has not ended
// yet, which never downloads (at ctx's end it returns ctx's error). It never hands out
// the alternative copy of a pending fallback or rollback trial (§4.5), which stays for
// the next Acquire. Tools that are not ready fail with sitemedia.ErrToolsUnavailable
// (wrapping the cause), site media off with sitemedia.ErrSiteMediaOff.
func (m *Manager) AcquireReady(ctx context.Context) (*Lease, error) {
	m.mu.Lock()
	for !m.discovered && !m.closed && m.enabled {
		ch := m.startDiscoveryLocked(false)
		m.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		m.mu.Lock()
	}
	defer m.mu.Unlock()
	if !m.closed && m.enabled && !m.readyLocked() {
		return nil, unavailable(m.notReadyLocked())
	}
	return m.leaseLocked(false)
}

// leaseLocked leases the copies that serve; with trials, a pending trial's alternative
// copy instead (Acquire).
func (m *Manager) leaseLocked(trials bool) (*Lease, error) {
	switch {
	case m.closed:
		return nil, unavailable(ErrClosed)
	case !m.enabled:
		return nil, sitemedia.ErrSiteMediaOff
	}
	l := &Lease{m: m, YtDlpCache: m.lay.ytdlpCache(), DenoCache: m.lay.denoCache(), WorkRoot: m.lay.run()}
	yt, rec := &m.rt[ytIdx], &m.st.YtDlp
	managed := func(version string) {
		l.YtDlp, l.YtDlpVersion, l.YtDlpManaged = m.lay.exe(ToolYtDlp, version), version, true
		l.dirs = append(l.dirs, m.lay.versionDir(ToolYtDlp, version))
	}
	t := m.trial
	if !trials {
		t = nil
	}
	switch {
	case t != nil && !t.given && t.kind == trialManaged && yt.managedOK:
		t.given, l.alt = true, trialManaged
		managed(rec.Version)
	case t != nil && !t.given && t.kind == trialPrevious && t.verified && rec.Previous != nil:
		t.given, l.alt = true, trialPrevious
		managed(rec.Previous.Version)
	case yt.path != nil:
		l.YtDlp, l.YtDlpVersion = yt.path.path, yt.path.version
	case yt.managedOK:
		managed(rec.Version)
	default:
		return nil, unavailable(m.notReadyLocked())
	}
	switch dn := &m.rt[denoIdx]; {
	case dn.path != nil:
		l.Deno = dn.path.path
	case dn.managedOK:
		l.Deno = m.lay.exe(ToolDeno, m.st.Deno.Version)
		l.dirs = append(l.dirs, m.lay.versionDir(ToolDeno, m.st.Deno.Version))
	}
	for _, d := range l.dirs {
		m.leases[d]++
	}
	return l, nil
}

// release drops l's holds; a trial lease released without a report ends its trial, and
// folders nobody needs any more are removed.
func (m *Manager) release(l *Lease) {
	m.mu.Lock()
	unused := false
	for _, d := range l.dirs {
		if m.leases[d]--; m.leases[d] <= 0 {
			delete(m.leases, d)
			unused = unused || !m.keptLocked(d)
		}
	}
	if l.alt != noTrial && !l.reported.Load() && m.trial != nil && m.trial.given && m.trial.kind == l.alt {
		m.trial = nil
	}
	if unused {
		m.goLocked(func() {
			if unlock, err := m.lockFolder(m.ctx); err == nil {
				m.cleanupHeld()
				unlock()
			}
		})
	}
	m.mu.Unlock()
}

// keptLocked reports whether the version folder dir is a current or previous copy.
func (m *Manager) keptLocked(dir string) bool {
	for _, tool := range toolNames {
		rec := m.st.record(tool)
		if rec.Version != "" && dir == m.lay.versionDir(tool, rec.Version) ||
			rec.Previous != nil && dir == m.lay.versionDir(tool, rec.Previous.Version) {
			return true
		}
	}
	return false
}

// goLocked runs fn in a goroutine the manager waits for at Close (not after Close).
func (m *Manager) goLocked(fn func()) {
	if m.closed {
		return
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		fn()
	}()
}

// persist writes the current state to tools.json.
func (m *Manager) persist() error {
	m.saveMu.Lock()
	defer m.saveMu.Unlock()
	m.mu.Lock()
	st := m.st.clone()
	m.mu.Unlock()
	return saveState(m.lay, st)
}

// lockFolder takes the manager's operation lock and the folder's .lock file, waiting
// for another process that holds the file lock until ctx ends.
func (m *Manager) lockFolder(ctx context.Context) (unlock func(), err error) {
	m.opMu.Lock()
	f, err := os.OpenFile(m.lay.lock(), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		m.opMu.Unlock()
		return nil, fmt.Errorf("tools: open the lock file: %w", err)
	}
	for {
		busy, err := tryLock(f)
		if err != nil {
			f.Close()
			m.opMu.Unlock()
			return nil, fmt.Errorf("tools: lock the tools folder: %w", err)
		}
		if !busy {
			break
		}
		select {
		case <-ctx.Done():
			f.Close()
			m.opMu.Unlock()
			return nil, ctx.Err()
		case <-time.After(m.h.lockPoll):
		}
	}
	return func() {
		_ = unlockFile(f)
		_ = f.Close()
		m.opMu.Unlock()
	}, nil
}
