package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// An install run (docs/SITE-MEDIA-SPEC.md §4.2, §4.4) installs or updates yt-dlp and
// Deno in parallel and independently. For each tool it finds the latest release from
// the redirect of <base>/<repo>/releases/latest, fetches every file from that tag,
// downloads to tmp/<random>.part (flushed), verifies (§4.3), fills
// <tool>/<version>.new-<random>/ (yt-dlp's onedir zip: unpacked, with its file list),
// renames it to <tool>/<version>/ (a folder of that version with other contents is
// set aside as <version>.old-<random>/ and put back if the new copy fails), smoke-tests
// the executable there, then writes tools.json and publishes the change. One run exists
// at a time, owned by the manager; it holds the folder's lock throughout.

// runMode is what a run does.
type runMode struct {
	installMissing bool // install tools that have no copy (Ensure, Install)
	update         bool // look for newer releases of the managed copies
	auto           bool // the daily check: Deno only every 30 days
	switchChannel  bool // the yt-dlp channel changed
	forceManaged   bool // install the managed yt-dlp although a PATH copy serves (§4.5)
}

// installRun is the running install.
type installRun struct {
	mode   runMode
	done   chan struct{}
	cancel context.CancelFunc
	err    error // set before done is closed
}

// startRunLocked starts an install run (the caller checked that none runs and that the
// manager downloads).
func (m *Manager) startRunLocked(mode runMode) *installRun {
	ctx, cancel := context.WithTimeout(m.ctx, m.h.installTimeout)
	r := &installRun{mode: mode, done: make(chan struct{}), cancel: cancel}
	if m.closed {
		cancel()
		r.err = ErrClosed
		close(r.done)
		return r
	}
	m.run = r
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer cancel()
		err := m.runInstall(ctx, mode)
		m.mu.Lock()
		r.err = err
		m.run = nil
		switch {
		case m.readyLocked():
			m.failures, m.retryAt, m.lastErr = 0, time.Time{}, nil
		case err != nil && !errors.Is(err, context.Canceled): // not stopped by Close or Configure
			m.failures++
			m.retryAt = m.cfg.Now().Add(m.backoffLocked())
			m.lastErr = err
		}
		m.trySwitchLocked()
		m.mu.Unlock()
		close(r.done)
		m.notify()
	}()
	return r
}

// backoffLocked is the wait before the next automatic install: 30 s, doubling with
// each failure, at most 30 min.
func (m *Manager) backoffLocked() time.Duration {
	d := m.h.backoffBase
	for i := 1; i < m.failures && d < m.h.backoffMax; i++ {
		d *= 2
	}
	return min(d, m.h.backoffMax)
}

// job is the install or update of one tool.
type job struct {
	idx int
	run func(ctx context.Context) error
}

// runInstall waits for the first discovery, takes the folder's lock, runs the jobs the
// mode calls for, removes unused folders and discovers again. It returns the first
// tool's failure (yt-dlp first).
func (m *Manager) runInstall(ctx context.Context, mode runMode) error {
	m.mu.Lock()
	ch := m.startDiscoveryLocked(false)
	m.mu.Unlock()
	select {
	case <-ch:
	case <-ctx.Done():
		return ctx.Err()
	}
	unlock, err := m.lockFolder(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	jobs := m.plan(mode)
	if len(jobs) == 0 {
		return nil
	}
	m.notify()
	errs := make([]error, len(jobs))
	if free, err := m.h.freeSpace(m.lay.dir); err == nil && free < minFreeSpace {
		for i, j := range jobs {
			errs[i] = m.finishTool(j.idx, coded(CodeDiskFull, fmt.Errorf("%d bytes free", free)))
		}
	} else {
		var wg sync.WaitGroup
		for i, j := range jobs {
			wg.Go(func() { errs[i] = m.finishTool(j.idx, j.run(ctx)) })
		}
		wg.Wait()
	}
	_ = m.persist() // the last check times
	m.cleanupHeld()
	_ = m.rediscover(m.ctx)
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// plan returns the jobs of mode and marks their tools installing.
func (m *Manager) plan(mode runMode) []job {
	m.mu.Lock()
	defer m.mu.Unlock()
	var jobs []job
	now := m.cfg.Now()
	yt, ytRec := &m.rt[ytIdx], &m.st.YtDlp
	if !yt.noBuild && (yt.path == nil || mode.forceManaged) {
		// The installed copy may come from another channel: its version and signature
		// time do not bound the new one.
		switching := ytRec.Version != "" && NormalizeChannel(ytRec.Channel) != m.channel
		p := ytPlan{channel: m.channel, current: ytRec.entry, managedOK: yt.managedOK, switching: switching}
		switch {
		case yt.managedOK && switching && (mode.update || mode.switchChannel):
			jobs = append(jobs, job{ytIdx, func(ctx context.Context) error { return m.installYtDlp(ctx, p) }})
		case yt.managedOK && mode.update:
			jobs = append(jobs, job{ytIdx, func(ctx context.Context) error { return m.installYtDlp(ctx, p) }})
		case !yt.managedOK && (mode.installMissing || mode.forceManaged):
			jobs = append(jobs, job{ytIdx, func(ctx context.Context) error { return m.installYtDlp(ctx, p) }})
		}
	}
	dn, dnRec := &m.rt[denoIdx], &m.st.Deno
	if !dn.noBuild && dn.path == nil {
		p := denoPlan{current: dnRec.entry, managedOK: dn.managedOK}
		due := !mode.auto || now.Sub(dnRec.CheckedAt) >= denoCheckInterval || compareDeno(dnRec.Version, minDenoString) < 0
		switch {
		case dn.managedOK && mode.update && due:
			jobs = append(jobs, job{denoIdx, func(ctx context.Context) error { return m.installDeno(ctx, p) }})
		case !dn.managedOK && mode.installMissing:
			jobs = append(jobs, job{denoIdx, func(ctx context.Context) error { return m.installDeno(ctx, p) }})
		}
	}
	for _, j := range jobs {
		rt := &m.rt[j.idx]
		rt.installing, rt.bytesDone, rt.bytesTotal = true, 0, -1
	}
	return jobs
}

// minDenoString is minDeno as a version string.
const minDenoString = "2.3.0"

// finishTool records the end of tool idx's job and returns its error as an Error.
func (m *Manager) finishTool(idx int, err error) error {
	m.mu.Lock()
	rt := &m.rt[idx]
	rt.installing, rt.bytesDone, rt.bytesTotal = false, 0, 0
	var out error
	switch {
	case err == nil:
		rt.err = nil
	case errors.Is(err, context.Canceled): // stopped by Close or Configure: not a failure of the tool
		out = err
	default:
		rt.err = toolError(toolNames[idx], m.cfg.GOOS, err)
		out = rt.err
	}
	m.mu.Unlock()
	m.notify()
	return out
}

// progress returns the download progress callback of tool idx.
func (m *Manager) progress(idx int) func(done, total int64) {
	return func(done, total int64) {
		m.mu.Lock()
		m.rt[idx].bytesDone, m.rt[idx].bytesTotal = done, total
		m.mu.Unlock()
		m.progressed()
	}
}

// noteLatest records the latest release of tool seen now.
func (m *Manager) noteLatest(tool, version string) {
	m.mu.Lock()
	rec := m.st.record(tool)
	rec.Latest, rec.CheckedAt = version, m.cfg.Now()
	m.mu.Unlock()
}

// ytPlan is what the yt-dlp job starts from.
type ytPlan struct {
	channel   string
	current   entry // the state's copy (Version "" = none)
	managedOK bool  // current passed its check
	switching bool  // a channel switch: the version may go down
}

// installYtDlp installs the latest yt-dlp of p.channel when it is newer than the
// current copy (or there is none, or the channel switches): SHA2-256SUMS must carry a
// valid signature by a pinned key, no older than the current copy's, and name the
// release file with its SHA-256. That file is the onedir zip where the list names one
// for this platform, else the onefile executable (ytdlpAssets); an onedir build that
// does not unpack or run here falls back to the onefile executable of the same release.
//
// An up-to-date onefile copy installed before layouts were recorded (Layout "") is
// replaced by the onedir build of its own release (migration), unless a lease holds
// its folder (the next check tries again); the onefile copy stays when the release has
// no onedir build or the build fails, and is then marked onefile so that its release
// is not looked at again.
func (m *Manager) installYtDlp(ctx context.Context, p ytPlan) error {
	if m.keyErr != nil {
		return coded(CodeSignature, m.keyErr)
	}
	repo := ytdlpRepo(p.channel)
	tag, err := m.fetch.latestTag(ctx, m.cfg.ReleaseBase, repo)
	if err != nil {
		return err
	}
	version, ok := versionOfTag(ToolYtDlp, tag)
	if !ok {
		return &Error{Code: CodeInternal, Detail: "unexpected release tag", Err: fmt.Errorf("%w: tag %.64q", ErrBadRelease, tag)}
	}
	m.noteLatest(ToolYtDlp, version)
	cur := p.current
	migrate := false
	if cur.Version != "" && !p.switching {
		switch c := compareYtDlp(version, cur.Version); {
		case c == 0 && p.managedOK:
			if cur.Layout != "" || m.assets.YtDlpZip == "" || m.held(m.lay.versionDir(ToolYtDlp, version)) {
				return nil // up to date
			}
			migrate = true
		case c < 0 && p.managedOK:
			return nil // the release is older: never go back
		case c < 0:
			return &Error{Code: CodeSignature, Err: fmt.Errorf("%w: the latest release %s is older than the installed %s", ErrBadRelease, version, cur.Version)}
		}
	}
	m.mu.Lock()
	bad := slices.Contains(m.st.YtDlp.Bad, version)
	m.mu.Unlock()
	if bad {
		if p.managedOK {
			return nil
		}
		return &Error{Code: CodeInternal, Detail: "the latest release was rolled back", Err: fmt.Errorf("%w: %s is marked unsuitable", ErrBadRelease, version)}
	}
	sums, err := m.fetch.bytes(ctx, releaseURL(m.cfg.ReleaseBase, repo, tag, ytdlpSums), maxSumsFile)
	if err != nil {
		return err
	}
	sig, err := m.fetch.bytes(ctx, releaseURL(m.cfg.ReleaseBase, repo, tag, ytdlpSumsSig), maxSigFile)
	if err != nil {
		return err
	}
	created, err := verifyDetached(m.keys, sums, sig, m.cfg.Now())
	if err != nil {
		return coded(CodeSignature, err)
	}
	if cur.Version != "" && !p.switching && created.Before(cur.SigCreated) {
		return coded(CodeSignature, fmt.Errorf("%w: older than the installed copy's", ErrSignature))
	}
	assets, err := m.ytdlpAssets(sums)
	if err != nil {
		return err
	}
	rel := ytRelease{repo: repo, tag: tag, version: version, channel: p.channel, created: created}
	var first error
	for _, a := range assets {
		if migrate && !a.onedir {
			break // migration installs only the onedir build
		}
		err := m.installYtDlpAsset(ctx, rel, a)
		if err == nil {
			return nil
		}
		if first == nil {
			first = err
		}
		if !a.onedir || ctx.Err() != nil || !brokenBuild(err) {
			break
		}
	}
	switch {
	case migrate && errors.Is(first, ErrInUse):
		return nil // an extraction started meanwhile: the next check migrates
	case migrate && (first == nil || brokenBuild(first)):
		m.markLayout(cur, layoutOnefile) // no onedir build to migrate to
	}
	return first
}

// ytRelease is the yt-dlp release an install takes its files from.
type ytRelease struct {
	repo, tag, version, channel string
	created                     time.Time // the checksum list's signature
}

// ytAsset is a yt-dlp release file and its published SHA-256.
type ytAsset struct {
	name   string
	want   [sha256.Size]byte
	onedir bool
}

// ytdlpAssets returns the release files yt-dlp can be installed from, best first, as
// the release's verified checksum list names them: the onedir zip where this platform
// has one, then the onefile executable. A list that names neither, or one of them more
// than once, fails with CodeChecksum.
func (m *Manager) ytdlpAssets(sums []byte) ([]ytAsset, error) {
	var out []ytAsset
	for _, a := range []ytAsset{{name: m.assets.YtDlpZip, onedir: true}, {name: m.assets.YtDlp}} {
		if a.name == "" || len(namedSums(sums, a.name)) == 0 {
			continue
		}
		want, err := sumFor(sums, a.name)
		if err != nil {
			return nil, coded(CodeChecksum, err)
		}
		a.want = want
		out = append(out, a)
	}
	if len(out) == 0 {
		name := m.assets.YtDlp
		if name == "" {
			name = m.assets.YtDlpZip
		}
		_, err := sumFor(sums, name)
		return nil, coded(CodeChecksum, err)
	}
	return out, nil
}

// brokenBuild reports a failure of the onedir build itself on this system: its archive
// does not unpack as expected, or the unpacked program does not run.
func brokenBuild(err error) bool {
	return errors.Is(err, ErrArchive) || ErrorCode(err) == CodeSmokeTest
}

// installYtDlpAsset downloads release file a of rel, checks its SHA-256, places it as
// the version folder (the onefile executable, or the onedir zip unpacked with its file
// list), smoke-tests it and switches to it. A failure leaves the current copy as it was.
func (m *Manager) installYtDlpAsset(ctx context.Context, rel ytRelease, a ytAsset) error {
	part, sum, size, err := m.fetch.download(ctx, releaseURL(m.cfg.ReleaseBase, rel.repo, rel.tag, a.name), maxYtDlpAsset, m.lay.tmp(), m.progress(ytIdx))
	if err != nil {
		return err
	}
	defer os.Remove(part)
	if sum != a.want {
		return coded(CodeChecksum, fmt.Errorf("%w: %s", ErrChecksum, a.name))
	}
	name := exeName(ToolYtDlp, m.cfg.GOOS)
	pl, err := m.placeVersion(ToolYtDlp, rel.version, func(dir string) (entry, error) {
		if !a.onedir {
			dst := filepath.Join(dir, name)
			if err := os.Rename(part, dst); err != nil {
				return entry{}, writeError(err)
			}
			if err := makeExecutable(dst); err != nil {
				return entry{}, writeError(err)
			}
			return entry{SHA256: hex.EncodeToString(sum[:]), Size: size, Layout: layoutOnefile}, nil
		}
		u, err := unzipTree(part, dir, m.assets.YtDlpZipExe, name, ytdlpTreeLimits)
		if errors.Is(err, ErrArchive) {
			return entry{}, &Error{Code: CodeInternal, Detail: "unexpected archive layout", Err: err}
		}
		if err != nil {
			return entry{}, err
		}
		ms, err := writeManifest(dir, u.files)
		if err != nil {
			return entry{}, writeError(err)
		}
		return entry{SHA256: hex.EncodeToString(u.exeSum[:]), Size: u.exeSize, Layout: layoutOnedir,
			Manifest: hex.EncodeToString(ms[:])}, nil
	})
	if err != nil {
		return err
	}
	if err := m.smokeTest(ctx, ToolYtDlp, rel.version); err != nil {
		pl.undo()
		return err
	}
	e := pl.files
	e.Version, e.Channel, e.File = rel.version, rel.channel, m.lay.rel(ToolYtDlp, rel.version)
	e.SigCreated, e.Verified, e.InstalledAt = rel.created, "openpgp+sha256", m.cfg.Now()
	if err := m.switchTo(ytIdx, e); err != nil {
		pl.undo()
		return err
	}
	pl.keep()
	return nil
}

// markLayout records layout for the current yt-dlp copy cur when tools.json does not
// say (it is written with the run's other changes).
func (m *Manager) markLayout(cur entry, layout string) {
	m.mu.Lock()
	if rec := &m.st.YtDlp; sameCopy(rec.entry, cur) && rec.Layout == "" {
		rec.Layout = layout
	}
	m.mu.Unlock()
}

// held reports whether a lease holds the version folder dir.
func (m *Manager) held(dir string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.leases[dir] > 0
}

// denoPlan is what the Deno job starts from.
type denoPlan struct {
	current   entry
	managedOK bool
}

// installDeno installs the latest Deno when there is no copy, or updates the managed
// copy within its major version (any version while it is below 2.3.0): the zip must
// have the SHA-256 of its .sha256sum, the executable that of deno-<target>.sha256sum
// when the release publishes it, and on Windows a trusted Authenticode signature.
func (m *Manager) installDeno(ctx context.Context, p denoPlan) error {
	tag, err := m.fetch.latestTag(ctx, m.cfg.ReleaseBase, repoDeno)
	if err != nil {
		return err
	}
	version, ok := versionOfTag(ToolDeno, tag)
	if !ok {
		return &Error{Code: CodeInternal, Detail: "unexpected release tag", Err: fmt.Errorf("%w: tag %.64q", ErrBadRelease, tag)}
	}
	m.noteLatest(ToolDeno, version)
	latest, _ := parseDenoVersion(version)
	if p.managedOK && p.current.Version != "" {
		cur, _ := parseDenoVersion(p.current.Version)
		if latest.compare(cur) <= 0 || cur.compare(minDeno) >= 0 && latest.major != cur.major {
			return nil
		}
	}
	if latest.compare(minDeno) < 0 {
		return &Error{Code: CodeInternal, Detail: "the latest Deno is older than " + minDenoString, Err: ErrBadRelease}
	}
	zipName, name := m.assets.Deno, exeName(ToolDeno, m.cfg.GOOS)
	sumData, err := m.fetch.bytes(ctx, releaseURL(m.cfg.ReleaseBase, repoDeno, tag, zipName+".sha256sum"), maxSumsFile)
	if err != nil {
		return err
	}
	wantZip, err := parseSHA256File(sumData, zipName)
	if err != nil {
		return coded(CodeChecksum, err)
	}
	var wantExe *[sha256.Size]byte
	switch data, err := m.fetch.bytes(ctx, releaseURL(m.cfg.ReleaseBase, repoDeno, tag, denoExeSums(m.assets.DenoTarget)), maxSumsFile); {
	case err == nil:
		s, err := parseSHA256File(data, name)
		if err != nil {
			return coded(CodeChecksum, err)
		}
		wantExe = &s
	case !errors.Is(err, ErrNotFound):
		return err
	}
	zipPath, zipSum, _, err := m.fetch.download(ctx, releaseURL(m.cfg.ReleaseBase, repoDeno, tag, zipName), maxDenoZip, m.lay.tmp(), m.progress(denoIdx))
	if err != nil {
		return err
	}
	defer os.Remove(zipPath)
	if zipSum != wantZip {
		return coded(CodeChecksum, fmt.Errorf("%w: %s", ErrChecksum, zipName))
	}
	verified, signer := "sha256", ""
	pl, err := m.placeVersion(ToolDeno, version, func(dir string) (entry, error) {
		exe := filepath.Join(dir, name)
		n, s, err := unzipExe(zipPath, name, exe, maxDenoUnpacked)
		if errors.Is(err, ErrArchive) {
			return entry{}, &Error{Code: CodeInternal, Detail: "unexpected archive layout", Err: err}
		}
		if err != nil {
			return entry{}, err
		}
		if wantExe != nil && s != *wantExe {
			return entry{}, coded(CodeChecksum, fmt.Errorf("%w: %s", ErrChecksum, name))
		}
		if m.cfg.GOOS == "windows" {
			subject, err := m.h.authenticode(exe)
			if err != nil {
				return entry{}, coded(CodeAuthenticode, err)
			}
			if denoSigner != "" && subject != denoSigner {
				return entry{}, coded(CodeAuthenticode, fmt.Errorf("signed by %.64q", subject))
			}
			verified, signer = "sha256+authenticode", subject
		}
		return entry{SHA256: hex.EncodeToString(s[:]), Size: n}, nil
	})
	if err != nil {
		return err
	}
	if err := m.smokeTest(ctx, ToolDeno, version); err != nil {
		pl.undo()
		return err
	}
	e := pl.files
	e.Version, e.File, e.Verified, e.Signer, e.InstalledAt = version, m.lay.rel(ToolDeno, version), verified, signer, m.cfg.Now()
	if err := m.switchTo(denoIdx, e); err != nil {
		pl.undo()
		return err
	}
	pl.keep()
	return nil
}

// placement is a version folder that placeVersion filled and moved into place. A
// folder of the same version that it replaced is kept aside (<version>.old-<random>)
// until the new copy is switched to (keep) or has failed (undo).
type placement struct {
	m             *Manager
	tool, version string
	final, old    string // old: the folder set aside ("" = none)
	files         entry  // the new copy's SHA256, Size, Layout and Manifest
}

// placeVersion makes <tool>/<version>/ hold a new copy: fill writes it into a fresh
// <version>.new-<random>/ folder and returns the fields of its entry that describe the
// files (SHA256, Size, Layout, Manifest); the folder is then renamed into place. An
// existing version folder that already holds that copy is reused. One with other
// contents is replaced only when no lease holds it (ErrInUse otherwise; also when it
// cannot be moved, as on Windows while a program in it runs): the lease check and both
// renames happen under the manager's lock, so a lease never finds the folder missing,
// and the old folder is kept aside until keep or undo.
func (m *Manager) placeVersion(tool, version string, fill func(dir string) (entry, error)) (*placement, error) {
	final := m.lay.versionDir(tool, version)
	tmp := final + newInfix + randomHex(8)
	if err := os.Mkdir(tmp, 0o700); err != nil {
		return nil, writeError(err)
	}
	files, err := fill(tmp)
	if err != nil {
		_ = os.RemoveAll(tmp)
		return nil, err
	}
	p := &placement{m: m, tool: tool, version: version, final: final, files: files}
	if fi, err := os.Stat(final); err == nil && fi.IsDir() {
		if verifyFolder(final, exeName(tool, m.cfg.GOOS), files) {
			_ = os.RemoveAll(tmp)
			return p, nil
		}
		old := final + oldInfix + randomHex(8)
		m.mu.Lock()
		err := ErrInUse
		if m.leases[final] == 0 {
			if err = renameRetry(final, old); err == nil {
				if err = renameRetry(tmp, final); err != nil {
					_ = renameRetry(old, final)
				}
			}
		}
		m.mu.Unlock()
		if err != nil {
			_ = os.RemoveAll(tmp)
			if !errors.Is(err, ErrInUse) {
				err = fmt.Errorf("%w: %v", ErrInUse, err)
			}
			return nil, &Error{Code: CodeInternal, Detail: "the version folder is in use", Err: err}
		}
		p.old = old
		return p, nil
	}
	if err := renameRetry(tmp, final); err != nil {
		_ = os.RemoveAll(tmp)
		return nil, &Error{Code: CodeInternal, Detail: "cannot move the version folder into place", Err: err}
	}
	return p, nil
}

// keep removes the folder the new copy replaced (a leftover goes with the next cleanup).
func (p *placement) keep() {
	if p.old != "" {
		_ = os.RemoveAll(p.old)
	}
}

// undo takes back a copy that failed after it was placed. A folder it replaced is put
// back, unless a lease already holds the new one (then tools.json no longer matches the
// folder and the next discovery installs again). A folder that replaced nothing is
// removed unless it is a current or previous copy or held by a lease.
func (p *placement) undo() {
	m := p.m
	if p.old == "" {
		m.discard(p.tool, p.version)
		return
	}
	failed := p.final + newInfix + randomHex(8)
	m.mu.Lock()
	if m.leases[p.final] == 0 && renameRetry(p.final, failed) == nil {
		_ = renameRetry(p.old, p.final)
	}
	m.mu.Unlock()
	_ = os.RemoveAll(failed)
}

// smokeTest runs the installed executable from its final path: yt-dlp --version must
// print the version within 60 s, deno --version answer "deno <version>" within 30 s.
func (m *Manager) smokeTest(ctx context.Context, tool, version string) error {
	timeout := m.h.smokeYtDlp
	if tool == ToolDeno {
		timeout = m.h.smokeDeno
	}
	out, err := m.runVersion(ctx, m.lay.exe(tool, version), timeout)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return coded(CodeSmokeTest, err)
	}
	got := out
	if tool == ToolDeno {
		got = ""
		if sub := denoVersionLineRE.FindStringSubmatch(out); sub != nil {
			got = sub[1]
		}
	}
	if got != version {
		return coded(CodeSmokeTest, fmt.Errorf("--version printed %.64q, want %s", out, version))
	}
	return nil
}

// discard removes a version folder that failed after it was placed, unless it is a
// current or previous copy or held by a lease.
func (m *Manager) discard(tool, version string) {
	dir := m.lay.versionDir(tool, version)
	m.mu.Lock()
	keep := m.keptLocked(dir) || m.leases[dir] > 0
	m.mu.Unlock()
	if !keep {
		_ = os.RemoveAll(dir)
	}
}

// switchTo makes e the current copy of tool idx (the old one becomes the previous
// copy): tools.json is written first, then the change is published.
func (m *Manager) switchTo(idx int, e entry) error {
	tool := toolNames[idx]
	apply := func(rec *toolRecord) {
		if rec.Version != "" && rec.Version != e.Version {
			p := rec.entry
			rec.Previous = &p
		}
		if rec.Previous != nil && rec.Previous.Version == e.Version {
			rec.Previous = nil
		}
		rec.entry = e
	}
	m.saveMu.Lock()
	m.mu.Lock()
	st := m.st.clone()
	m.mu.Unlock()
	apply(st.record(tool))
	err := saveState(m.lay, st)
	if err == nil {
		m.mu.Lock()
		rec := m.st.record(tool)
		old := rec.Version
		apply(rec)
		m.rt[idx].managedOK = true
		if idx == ytIdx && old != "" && old != e.Version && rec.Previous != nil {
			m.newVer, m.newFails = e.Version, 0 // watch the first extractions (§4.5)
		}
		m.mu.Unlock()
	}
	m.saveMu.Unlock()
	if err != nil {
		return writeError(err)
	}
	m.notify()
	return nil
}

// cleanupHeld removes, with the folder's lock held, every version folder that is
// neither a current nor a previous copy nor held by a lease, unfinished
// <version>.new-* folders and <version>.old-* folders left by a replacement.
func (m *Manager) cleanupHeld() {
	m.mu.Lock()
	keep := map[string]bool{}
	for d, n := range m.leases {
		if n > 0 {
			keep[d] = true
		}
	}
	for _, tool := range toolNames {
		rec := m.st.record(tool)
		if rec.Version != "" {
			keep[m.lay.versionDir(tool, rec.Version)] = true
		}
		if rec.Previous != nil {
			keep[m.lay.versionDir(tool, rec.Previous.Version)] = true
		}
	}
	m.mu.Unlock()
	for _, tool := range toolNames {
		entries, err := os.ReadDir(m.lay.toolDir(tool))
		if err != nil {
			continue
		}
		for _, e := range entries {
			p := filepath.Join(m.lay.toolDir(tool), e.Name())
			if keep[p] || !e.IsDir() || !isTempDir(e.Name()) && !validVersion(tool, e.Name()) {
				continue
			}
			_ = os.RemoveAll(p)
		}
	}
}

// makeExecutable gives the file at p mode 0755 on Unix.
func makeExecutable(p string) error {
	f, err := os.OpenFile(p, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	return chmodExe(f)
}
