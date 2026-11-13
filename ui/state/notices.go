package state

import (
	"slices"
	"sync"
	"time"
)

// Notice lifetimes (components/Toasts.svelte Notices).
const (
	NoticeLifetime = 9 * time.Second
	MaxNotices     = 3
)

// NoticeAction is a button of a notice.
type NoticeAction struct {
	Label   string
	Primary bool
	Run     func()
}

// Notice is a toast with actions: a download that arrived from the browser, one that
// awaits confirmation, a failed first install of yt-dlp and Deno.
type Notice struct {
	Key   string
	Title string
	// Detail is the download's name (shown isolated, on its own line).
	Detail  string
	Actions []NoticeAction
}

// Notices are the toasts with actions (Toasts.svelte's Notices): at most MaxNotices, each
// gone after NoticeLifetime, a lifetime that waits while the pointer or the focus is on
// it (Hold, Release). One that goes fades out for ToastExit (none without motion), still
// drawn but hidden from assistive technology meanwhile.
type Notices struct {
	Changes Signal

	clock    Clock
	motionOK func() bool

	mu       sync.Mutex
	items    []Notice
	leaving  map[string]bool
	timers   map[string]Timer
	deadline map[string]time.Time
	left     map[string]time.Duration
	gen      map[string]int
	next     int
}

// NewNotices makes the notices; motionOK nil means no fade-outs.
func NewNotices(clock Clock, motionOK func() bool) *Notices {
	return &Notices{clock: clockOr(clock), motionOK: motionOK, leaving: map[string]bool{}, timers: map[string]Timer{},
		deadline: map[string]time.Time{}, left: map[string]time.Duration{}, gen: map[string]int{}}
}

// Items are the notices shown, oldest first (also those fading out).
func (n *Notices) Items() []Notice { n.mu.Lock(); defer n.mu.Unlock(); return n.items }

// Leaving reports that a notice is fading out.
func (n *Notices) Leaving(key string) bool { n.mu.Lock(); defer n.mu.Unlock(); return n.leaving[key] }

// Show shows a notice, in place of one with its key.
func (n *Notices) Show(notice Notice) {
	n.mu.Lock()
	n.removeLocked(notice.Key)
	kept := append(slices.Clone(n.items), notice)
	var live []Notice
	for _, x := range kept {
		if !n.leaving[x.Key] {
			live = append(live, x)
		}
	}
	drop := map[string]bool{}
	for _, x := range live[:max(0, len(live)-MaxNotices)] {
		drop[x.Key] = true
		n.forgetLocked(x.Key)
	}
	n.items = slices.DeleteFunc(kept, func(x Notice) bool { return drop[x.Key] })
	n.armLocked(notice.Key, NoticeLifetime)
	n.mu.Unlock()
	n.Changes.Notify()
}

// Dismiss lets a notice go.
func (n *Notices) Dismiss(key string) {
	n.mu.Lock()
	if n.leaving[key] || !slices.ContainsFunc(n.items, func(x Notice) bool { return x.Key == key }) {
		n.forgetLocked(key)
		n.mu.Unlock()
		return
	}
	n.forgetLocked(key)
	if n.motionOK == nil || !n.motionOK() {
		n.removeLocked(key)
		n.mu.Unlock()
		n.Changes.Notify()
		return
	}
	n.leaving[key] = true
	gen := n.bumpLocked(key)
	n.timers[key] = n.clock.AfterFunc(ToastExit, func() {
		n.mu.Lock()
		if n.gen[key] == gen {
			n.removeLocked(key)
		}
		n.mu.Unlock()
		n.Changes.Notify()
	})
	n.mu.Unlock()
	n.Changes.Notify()
}

// Hold stops a notice's lifetime while the pointer or the focus is on it.
func (n *Notices) Hold(key string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	t, ok := n.timers[key]
	if !ok || n.leaving[key] {
		return
	}
	t.Stop()
	delete(n.timers, key)
	n.bumpLocked(key)
	n.left[key] = max(0, n.deadline[key].Sub(n.clock.Now()))
}

// Release lets a held notice's lifetime go on.
func (n *Notices) Release(key string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	left, ok := n.left[key]
	if _, armed := n.timers[key]; !ok || armed {
		return
	}
	delete(n.left, key)
	n.armLocked(key, left)
}

// Clear takes every notice away.
func (n *Notices) Clear() {
	n.mu.Lock()
	for k, t := range n.timers {
		t.Stop()
		n.bumpLocked(k)
	}
	clear(n.timers)
	clear(n.deadline)
	clear(n.left)
	n.items = nil
	n.leaving = map[string]bool{}
	n.mu.Unlock()
	n.Changes.Notify()
}

func (n *Notices) bumpLocked(key string) int {
	n.gen[key]++
	return n.gen[key]
}

func (n *Notices) forgetLocked(key string) {
	if t, ok := n.timers[key]; ok {
		t.Stop()
	}
	n.bumpLocked(key)
	delete(n.timers, key)
	delete(n.deadline, key)
	delete(n.left, key)
}

func (n *Notices) removeLocked(key string) {
	n.forgetLocked(key)
	delete(n.leaving, key)
	n.items = slices.DeleteFunc(slices.Clone(n.items), func(x Notice) bool { return x.Key == key })
}

func (n *Notices) armLocked(key string, d time.Duration) {
	n.deadline[key] = n.clock.Now().Add(d)
	gen := n.bumpLocked(key)
	n.timers[key] = n.clock.AfterFunc(d, func() {
		n.mu.Lock()
		current := n.gen[key] == gen
		n.mu.Unlock()
		if current {
			n.Dismiss(key)
		}
	})
}
