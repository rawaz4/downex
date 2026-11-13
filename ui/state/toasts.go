package state

import (
	"slices"
	"sync"
	"time"
)

// ToastTone is what a toast says: a done thing (a Mint check), a plain fact or a failure.
type ToastTone uint8

// The tones (DESIGN-SYSTEM.md §3.17).
const (
	ToastInfo ToastTone = iota
	ToastSuccess
	ToastError
)

func (t ToastTone) String() string {
	switch t {
	case ToastSuccess:
		return "success"
	case ToastError:
		return "error"
	}
	return "info"
}

// Toast is one transient notice.
type Toast struct {
	ID      int
	Message string
	Tone    ToastTone
	// Leaving: fading out, no longer announced or focusable.
	Leaving bool
}

// Toast lifetimes (stores/toasts.svelte.ts).
const (
	ToastLifetime = 5 * time.Second // an error stays twice as long
	MaxToasts     = 4
	// ToastExit is how long a toast takes to fade out (DESIGN-SYSTEM.md §2.7: exit 140 ms).
	ToastExit = 140 * time.Millisecond
)

// Toasts are the transient notices, announced through a polite live region. A toast that
// goes fades out for ToastExit (none under reduced motion); while it does, it is hidden
// from assistive technology.
type Toasts struct {
	Changes Signal

	clock Clock
	// motionOK reports that animations may run (the platform can animate and reduced
	// motion was not asked for); nil: they may not.
	motionOK func() bool

	mu    sync.Mutex
	items []Toast
	next  int
}

// NewToasts makes the notices; motionOK nil means no fade-outs.
func NewToasts(clock Clock, motionOK func() bool) *Toasts {
	return &Toasts{clock: clockOr(clock), motionOK: motionOK, next: 1}
}

// Items are the toasts shown, oldest first (never modified once handed out).
func (t *Toasts) Items() []Toast { t.mu.Lock(); defer t.mu.Unlock(); return t.items }

// Show shows a plain fact ("Nothing to pause") or, with another tone, a done thing or a
// failure.
func (t *Toasts) Show(message string, tone ToastTone) int {
	t.mu.Lock()
	toast := Toast{ID: t.next, Message: message, Tone: tone}
	t.next++
	var kept []Toast
	for _, x := range t.items {
		if !x.Leaving {
			kept = append(kept, x)
		}
	}
	dropped := len(kept) - (MaxToasts - 1)
	next := make([]Toast, 0, len(t.items)+1)
	for _, x := range t.items {
		if x.Leaving || dropped <= 0 || slices.IndexFunc(kept, func(k Toast) bool { return k.ID == x.ID }) >= dropped {
			next = append(next, x)
		}
	}
	t.items = append(next, toast)
	life := ToastLifetime
	if tone == ToastError {
		life *= 2
	}
	t.mu.Unlock()
	t.clock.AfterFunc(life, func() { t.Dismiss(toast.ID) })
	t.Changes.Notify()
	return toast.ID
}

// Success shows something that was done ("Settings saved", "2 downloads paused").
func (t *Toasts) Success(message string) int { return t.Show(message, ToastSuccess) }

// Error shows a failure.
func (t *Toasts) Error(message string) int { return t.Show(message, ToastError) }

// Dismiss lets a toast go.
func (t *Toasts) Dismiss(id int) {
	t.mu.Lock()
	i := slices.IndexFunc(t.items, func(x Toast) bool { return x.ID == id })
	if i < 0 || t.items[i].Leaving {
		t.mu.Unlock()
		return
	}
	if t.motionOK == nil || !t.motionOK() {
		t.items = slices.Delete(slices.Clone(t.items), i, i+1)
		t.mu.Unlock()
		t.Changes.Notify()
		return
	}
	next := slices.Clone(t.items)
	next[i].Leaving = true
	t.items = next
	t.mu.Unlock()
	t.clock.AfterFunc(ToastExit, func() {
		t.mu.Lock()
		t.items = slices.DeleteFunc(slices.Clone(t.items), func(x Toast) bool { return x.ID == id })
		t.mu.Unlock()
		t.Changes.Notify()
	})
	t.Changes.Notify()
}
