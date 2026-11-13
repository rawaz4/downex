package tools

import (
	"context"
	"slices"
)

// Updates and rollback (docs/SITE-MEDIA-SPEC.md §4.5). Only managed copies are updated,
// never PATH copies; while a PATH copy serves, nothing is installed for that tool. The
// daily check (CheckUpdate, timed by the daemon) installs a newer yt-dlp of the channel
// and, every 30 days, a newer Deno of the same major version. A new yt-dlp must have a
// higher version and a signature at least as recent as the installed one's, except
// right after a channel change. A version whose first three extractions all fail is
// tried against the previous version and, when that succeeds, rolled back and marked
// bad. A PATH yt-dlp that rejects Downex's options, or fails where the managed copy
// then succeeds, is marked unsuitable until its --version changes.

// trialKind names a pending fallback (§4.5).
type trialKind int

const (
	noTrial       trialKind = iota
	trialManaged            // a PATH yt-dlp failed: try the managed copy
	trialPrevious           // a new managed yt-dlp failed its first extractions: try the previous one
)

// trial is a pending fallback: the next Acquire returns the alternative copy (given).
type trial struct {
	kind     trialKind
	from     *pathCopy // trialManaged: the PATH copy that failed
	given    bool
	verified bool // trialPrevious: the previous copy still has its recorded SHA-256 and size
}

// CheckUpdate is the daily check: it runs discovery again and, with automatic updates
// on and a manager that downloads, installs newer releases of the managed copies (and
// the configured channel's yt-dlp after a channel change). Missing tools are not
// installed: installs start only from a user action. It returns the update's failure,
// which is also recorded in the tool's status; the working copies stay.
func (m *Manager) CheckUpdate(ctx context.Context) error {
	m.mu.Lock()
	if m.closed || !m.enabled {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()
	if err := m.rediscover(ctx); err != nil {
		return err
	}
	m.mu.Lock()
	if m.closed || !m.enabled || !m.autoUpdate || m.fetch == nil {
		m.mu.Unlock()
		return nil
	}
	r := m.run
	if r == nil {
		r = m.startRunLocked(runMode{update: true, auto: true})
	}
	m.mu.Unlock()
	select {
	case <-r.done:
		return r.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Report tells the manager how an extraction with l's copies ended. It never blocks:
// the switches it causes run in the background.
func (m *Manager) Report(l *Lease, outcome Outcome) {
	if l == nil || l.m != m {
		return
	}
	l.reported.Store(true)
	m.mu.Lock()
	changed, save, extra := false, false, false
	rollback := ""
	yt := &m.rt[ytIdx]
	switch {
	case l.alt == trialManaged:
		t := m.trial
		m.trial = nil
		if outcome == OK && t != nil && t.from != nil {
			m.markUnsuitableLocked(*t.from)
			changed, save = true, true
		}
	case l.alt == trialPrevious:
		m.trial, m.newVer = nil, ""
		if outcome == OK {
			rollback = l.YtDlpVersion
		}
	case !l.YtDlpManaged:
		switch outcome {
		case BadOptions:
			m.markUnsuitableLocked(pathCopy{path: l.YtDlp, version: l.YtDlpVersion})
			changed, save = true, true
		case SiteError:
			if m.trial == nil && (m.fetch != nil || yt.managedOK) {
				m.trial = &trial{kind: trialManaged, from: &pathCopy{path: l.YtDlp, version: l.YtDlpVersion}}
			}
		}
	default:
		if l.YtDlpVersion == m.newVer {
			if outcome == OK {
				m.newVer = ""
			} else if m.newFails++; m.newFails >= rollbackAfter {
				m.newVer = ""
				if m.trial == nil && m.st.YtDlp.Previous != nil {
					m.trial = &trial{kind: trialPrevious}
				}
			}
		}
		now := m.cfg.Now()
		if outcome != OK && m.enabled && m.autoUpdate && m.fetch != nil && (m.lastExtra.IsZero() || now.Sub(m.lastExtra) >= extraCheckInterval) {
			m.lastExtra, extra = now, true
		}
	}
	if save {
		m.goLocked(func() { _ = m.persist() })
	}
	if rollback != "" {
		m.goLocked(func() { m.rollBack(rollback) })
	}
	if extra && m.run == nil && !m.closed {
		m.startRunLocked(runMode{update: true, auto: true})
	}
	m.mu.Unlock()
	if changed {
		m.notify()
	}
}

// markUnsuitableLocked marks a PATH yt-dlp unsuitable; it stops serving at once.
func (m *Manager) markUnsuitableLocked(c pathCopy) {
	rec := &m.st.YtDlp
	if !m.unsuitableLocked(&c) {
		rec.Unsuitable = append(rec.Unsuitable, pathVersion{Path: c.path, Version: c.version})
		if len(rec.Unsuitable) > maxUnsuitable {
			rec.Unsuitable = rec.Unsuitable[len(rec.Unsuitable)-maxUnsuitable:]
		}
	}
	if p := m.rt[ytIdx].path; p != nil && samePath(p.path, c.path) && p.version == c.version {
		m.rt[ytIdx].path = nil
	}
}

// rollBack makes the previous yt-dlp (version good, which just succeeded where the
// current one failed) current again, marks the current version bad and removes it
// once no lease holds it. tools.json is written before the switch is published.
func (m *Manager) rollBack(good string) {
	unlock, err := m.lockFolder(m.ctx)
	if err != nil {
		return
	}
	defer unlock()
	m.mu.Lock()
	rec := &m.st.YtDlp
	if rec.Previous == nil || rec.Previous.Version != good || rec.Version == "" {
		m.mu.Unlock()
		return
	}
	prev, bad := *rec.Previous, rec.Version
	m.mu.Unlock()
	if !m.verifyEntry(ToolYtDlp, prev) {
		return
	}
	apply := func(r *toolRecord) bool {
		if r.Previous == nil || r.Previous.Version != good || r.Version != bad {
			return false
		}
		r.entry, r.Previous = prev, nil
		if !slices.Contains(r.Bad, bad) {
			r.Bad = append(r.Bad, bad)
		}
		return true
	}
	m.saveMu.Lock()
	m.mu.Lock()
	st := m.st.clone()
	m.mu.Unlock()
	if !apply(&st.YtDlp) || saveState(m.lay, st) != nil {
		m.saveMu.Unlock()
		return
	}
	m.mu.Lock()
	if apply(&m.st.YtDlp) {
		m.rt[ytIdx].managedOK = true
		m.newVer = ""
	}
	m.mu.Unlock()
	m.saveMu.Unlock()
	m.cleanupHeld()
	m.notify()
}
