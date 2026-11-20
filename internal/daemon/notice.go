package daemon

import (
	"sync"

	"downex/internal/model"
	"downex/internal/polite"
	downexv1 "downex/internal/rpc/downex/v1"
)

// NoticeKind is what a Notice reports.
type NoticeKind uint8

const (
	// NoticeCompleted: Download has just completed.
	NoticeCompleted NoticeKind = iota + 1
	// NoticeFailed: Download has just failed (Download.error and error_code say why).
	NoticeFailed
	// NoticeAwaiting: Download, a browser hand-off, awaits confirmation. UILaunched
	// reports whether the desktop UI was asked to show the confirmation (false: there is
	// no UI next to downexd, or it could not be started, and the user must be told).
	NoticeAwaiting
	// NoticeSettings: the settings changed; Settings holds the new ones.
	NoticeSettings
	// NoticePostAction: a queue finished and its post-queue action counts down
	// (PostAction.deadline; the tray shows "Shutting down in 60 s" with a way to
	// cancel, Manager.CancelPostAction), or the countdown was cancelled or aborted
	// (PostAction.cancelled) (docs/M6-SPEC.md §1).
	NoticePostAction
	// NoticeScanProblem: the antivirus scan of Download, which has just completed,
	// reported a problem or did not finish (docs/M6-SPEC.md §3); Text is the notification
	// ("Scanner reported a problem with <name>"). The download stays completed and its
	// file is never opened automatically.
	NoticeScanProblem
	// NoticeUpdateAvailable: an automatic check found a newer version with a package for
	// this system (docs/M6-SPEC.md §4); Update describes it. Sent after every automatic
	// check that finds one (at most one a day, and one after downexd starts), so that the
	// user who chose Later hears of it again.
	NoticeUpdateAvailable
	// NoticeFileCheck: the final file check of Download, which has just completed, found
	// that the file is not what its name says (docs/DESKTOP-SPEC.md §9.7): DANGER always,
	// CAUTION only while notify_on_complete is on. Text is the notification ("<name> is
	// not what its name says."), Level the check's level. The download stays completed
	// and its file is never opened automatically.
	NoticeFileCheck
	// NoticeLinkRenewed: Download, whose link had expired, continues with the link of a
	// new add that proved to serve the same file (docs/DESKTOP-SPEC.md §10; the new add
	// was removed). The tray says "Download continues" while notify_on_complete is on.
	NoticeLinkRenewed
)

// Notice is a one-off occurrence for in-process observers such as the tray and its
// notifications (docs/DESKTOP-SPEC.md §3–4). Progress and state changes are observed
// with Watch, which can be called in-process too.
type Notice struct {
	Kind       NoticeKind
	Download   *downexv1.Download // NoticeCompleted, NoticeFailed, NoticeAwaiting, NoticeLinkRenewed: the download then
	UILaunched bool               // NoticeAwaiting
	// InWindow (NoticeCompleted, NoticeFailed): the download's progress window shows the
	// outcome (the download complete dialog); the tray shows no notification for it.
	InWindow bool
	Settings Settings // NoticeSettings (a copy)
	// NoticePostAction: the countdown (or its cancellation).
	PostAction *downexv1.PostActionPending
	// NoticeScanProblem, NoticeFileCheck: the notification text.
	Text string
	// NoticeFileCheck: the check's level.
	Level model.FileCheckLevel
	// NoticeUpdateAvailable: the available version.
	Update *downexv1.UpdateInfo
}

// noticeBuffer bounds each subscriber's undelivered notices; later ones are dropped.
const noticeBuffer = 64

// noticeHub fans notices out to subscribers without ever blocking the daemon.
type noticeHub struct {
	mu     sync.Mutex
	subs   map[<-chan Notice]*noticeSub
	closed bool
}

// noticeSub is one subscription of the notice hub.
type noticeSub struct {
	ch    chan Notice
	shows bool // the subscriber shows the user its notices (ShowNotices)
}

// Subscribe returns a channel that receives every later notice, and a function that
// ends the subscription and closes the channel. Shutdown closes it too. Delivery never
// blocks the daemon: while the subscriber has noticeBuffer (64) undelivered notices,
// newer ones are dropped. Safe for concurrent use; cancel may be called more than once.
func (m *Manager) Subscribe() (<-chan Notice, func()) {
	h := &m.notices
	s := &noticeSub{ch: make(chan Notice, noticeBuffer)}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		close(s.ch)
		return s.ch, func() {}
	}
	if h.subs == nil {
		h.subs = make(map[<-chan Notice]*noticeSub)
	}
	h.subs[s.ch] = s
	return s.ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, ok := h.subs[s.ch]; ok {
			delete(h.subs, s.ch)
			close(s.ch)
		}
	}
}

// ShowNotices records whether the subscriber of ch (a channel from Subscribe) shows the
// user the notices it receives: the tray does while its icon is visible, as its
// notifications appear only then. A browser hand-off is held for confirmation only while
// the daemon can ask the user (docs/DESKTOP-SPEC.md §1): it can open the confirmation
// window (Config.OpenConfirmation), or a subscriber shows the NoticeAwaiting. Otherwise
// it is queued. An ended subscription is ignored. Safe for concurrent use.
func (m *Manager) ShowNotices(ch <-chan Notice, shows bool) {
	h := &m.notices
	h.mu.Lock()
	defer h.mu.Unlock()
	if s, ok := h.subs[ch]; ok {
		s.shows = shows
	}
}

// noticesShown reports whether a subscriber shows the user its notices.
func (m *Manager) noticesShown() bool {
	h := &m.notices
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, s := range h.subs {
		if s.shows {
			return true
		}
	}
	return false
}

// notify delivers n to every subscriber that has room for it.
func (m *Manager) notify(n Notice) {
	h := &m.notices
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, s := range h.subs {
		select {
		case s.ch <- n:
		default:
			m.log.Debug("notice dropped: the subscriber is not keeping up", "kind", int(n.Kind))
		}
	}
}

// closeNotices ends every subscription (Shutdown).
func (m *Manager) closeNotices() {
	h := &m.notices
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for _, s := range h.subs {
		close(s.ch)
	}
	h.subs = nil
}

// Activity is a summary of the queue, for the tray's tooltip.
type Activity struct {
	Active   int     // downloads running (probing or downloading), including ones stopping
	Queued   int     // downloads waiting for a free slot or for their queue to run
	Awaiting int     // browser hand-offs awaiting confirmation
	SpeedBps float64 // total speed of the running downloads, bytes per second
	// PoliteSlowed: Polite mode holds the downloads back now (docs/POLITE-SPEC.md §8).
	PoliteSlowed bool
}

// Activity returns the current queue summary. It is cheap; call it when Watch reports
// a change rather than polling.
func (m *Manager) Activity() Activity {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := Activity{Active: m.running, Queued: len(m.waiting), PoliteSlowed: m.politeStatus().State == polite.Slowed}
	for _, e := range m.entries {
		if e.run != nil {
			if p := e.run.progress.Load(); p != nil {
				a.SpeedBps += p.SpeedBps
			}
			if p := e.run.mprogress.Load(); p != nil {
				a.SpeedBps += p.SpeedBps
			}
		} else if e.rec.State == model.StateAwaitingConfirmation {
			a.Awaiting++
		}
	}
	return a
}
