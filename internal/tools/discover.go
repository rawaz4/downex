package tools

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Discovery (docs/SITE-MEDIA-SPEC.md §4.1) finds the copies to run, PATH first:
//
//   - yt-dlp on PATH (on Windows an .exe, never .bat/.cmd/.ps1) whose --version answers
//     within 10 s with YYYY.MM.DD[.N], at most 90 days old and not marked unsuitable;
//     else the managed copy, whose size and SHA-256 must match tools.json (an onedir
//     copy: also every file of its file list).
//   - Deno on PATH whose deno --version reports at least 2.3.0; else the managed copy.
//     Node, Bun and QuickJS are never used.
//
// It runs when the manager is first configured or used, after a settings change, after
// an install, with the daily check and when a PATH copy is marked unsuitable. Copies
// inside the tools folder are never taken from PATH.

// startDiscoveryLocked starts a discovery unless one runs and returns the channel
// closed when it ends; again makes a running discovery repeat once more, so that it
// sees changes made after it started.
func (m *Manager) startDiscoveryLocked(again bool) chan struct{} {
	if m.disc != nil {
		m.discAgain = m.discAgain || again
		return m.disc
	}
	done := make(chan struct{})
	if m.closed {
		close(done)
		return done
	}
	m.disc = done
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		for {
			m.discover(m.ctx)
			m.mu.Lock()
			if !m.discAgain || m.closed {
				m.disc, m.discovered = nil, true
				m.trySwitchLocked()
				m.mu.Unlock()
				break
			}
			m.discAgain = false
			m.mu.Unlock()
		}
		close(done)
		m.notify()
	}()
	return done
}

// rediscover runs a discovery that starts after this call (or joins one that will
// repeat) and waits for it until ctx ends.
func (m *Manager) rediscover(ctx context.Context) error {
	m.mu.Lock()
	ch := m.startDiscoveryLocked(true)
	m.mu.Unlock()
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// discover looks at PATH and checks the managed copies, then records what serves.
func (m *Manager) discover(ctx context.Context) {
	m.mu.Lock()
	st := m.st.clone()
	m.mu.Unlock()
	yt := m.findYtDlpOnPath(ctx)
	dn := m.findDenoOnPath(ctx)
	ytOK := m.verifyEntry(ToolYtDlp, st.YtDlp.entry)
	dnOK := m.verifyEntry(ToolDeno, st.Deno.entry)
	m.mu.Lock()
	if yt != nil && m.unsuitableLocked(yt) {
		yt = nil // marked while this discovery ran
	}
	m.rt[ytIdx].path, m.rt[denoIdx].path = yt, dn
	// A copy switched to meanwhile was checked by its install.
	if sameCopy(m.st.YtDlp.entry, st.YtDlp.entry) {
		m.rt[ytIdx].managedOK = ytOK
	}
	if sameCopy(m.st.Deno.entry, st.Deno.entry) {
		m.rt[denoIdx].managedOK = dnOK
	}
	m.mu.Unlock()
}

// unsuitableLocked reports a PATH yt-dlp marked unsuitable (§4.5).
func (m *Manager) unsuitableLocked(c *pathCopy) bool {
	for _, u := range m.st.YtDlp.Unsuitable {
		if samePath(u.Path, c.path) && u.Version == c.version {
			return true
		}
	}
	return false
}

// lookPath finds name on PATH: an absolute path outside the tools folder, an .exe on
// Windows.
func (m *Manager) lookPath(name string) (string, bool) {
	p, err := m.cfg.LookPath(name)
	if err != nil || !filepath.IsAbs(p) {
		return "", false
	}
	if m.cfg.GOOS == "windows" && !strings.EqualFold(filepath.Ext(p), ".exe") {
		return "", false
	}
	if rel, err := filepath.Rel(m.lay.dir, p); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return p, true
}

// findYtDlpOnPath returns the PATH yt-dlp if it is usable.
func (m *Manager) findYtDlpOnPath(ctx context.Context) *pathCopy {
	p, ok := m.lookPath(ToolYtDlp)
	if !ok {
		return nil
	}
	out, err := m.runVersion(ctx, p, m.h.pathTimeout)
	if err != nil {
		return nil
	}
	v, ok := parseYtDlpVersion(out)
	if !ok || m.cfg.Now().Sub(v.date) > maxPathAge {
		return nil
	}
	return &pathCopy{path: p, version: out}
}

// findDenoOnPath returns the PATH Deno if it is at least 2.3.0.
func (m *Manager) findDenoOnPath(ctx context.Context) *pathCopy {
	p, ok := m.lookPath(ToolDeno)
	if !ok {
		return nil
	}
	out, err := m.runVersion(ctx, p, m.h.pathTimeout)
	if err != nil {
		return nil
	}
	sub := denoVersionLineRE.FindStringSubmatch(out)
	if sub == nil {
		return nil
	}
	if v, ok := parseDenoVersion(sub[1]); !ok || v.compare(minDeno) < 0 {
		return nil
	}
	return &pathCopy{path: p, version: sub[1]}
}

// runVersion runs exe --version in a private work folder with the allow-listed
// environment and returns the first line it printed. A non-zero exit, a timeout or
// nothing printed is an error.
func (m *Manager) runVersion(ctx context.Context, exe string, timeout time.Duration) (string, error) {
	dir, err := NewRunDir(m.lay.run())
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	res, err := Exec(ctx, ExecSpec{
		Path:        exe,
		Args:        []string{"--version"},
		Env:         m.Env(EnvSpec{DenoDir: "", TempDir: filepath.Join(dir, tmpName), DenoCache: m.lay.denoCache()}),
		Dir:         dir,
		Timeout:     timeout,
		MaxStdout:   4 << 10,
		MaxStderr:   4 << 10,
		MemoryLimit: DefaultMemoryLimit,
	})
	switch {
	case err != nil:
		return "", err
	case res.TimedOut:
		return "", errors.New("--version timed out")
	case res.ExitCode != 0:
		return "", fmt.Errorf("--version exited with code %d", res.ExitCode)
	}
	line, _, _ := strings.Cut(string(res.Stdout), "\n")
	line = strings.TrimSpace(line)
	if line == "" {
		return "", errors.New("--version printed nothing")
	}
	return line, nil
}

// verifyEntry checks a managed copy: its executable exists with the recorded size and
// SHA-256, and so does every file of an onedir copy's file list (verifyFolder).
func (m *Manager) verifyEntry(tool string, e entry) bool {
	if e.Version == "" {
		return false
	}
	return verifyFolder(m.lay.versionDir(tool, e.Version), exeName(tool, m.cfg.GOOS), e)
}

// sameCopy reports whether two entries describe the same files.
func sameCopy(a, b entry) bool {
	return a.Version == b.Version && a.SHA256 == b.SHA256 && a.Size == b.Size && a.Layout == b.Layout && a.Manifest == b.Manifest
}

// fileSum returns the SHA-256 and size of a regular file.
func fileSum(p string) (sum [sha256.Size]byte, size int64, err error) {
	f, err := os.Open(p)
	if err != nil {
		return sum, 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return sum, 0, err
	}
	if !fi.Mode().IsRegular() {
		return sum, 0, errors.New("not a regular file")
	}
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return sum, 0, err
	}
	copy(sum[:], h.Sum(nil))
	return sum, n, nil
}

// samePath compares two absolute paths (case-insensitively on Windows).
func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if filepath.Separator == '\\' {
		return strings.EqualFold(a, b)
	}
	return a == b
}
