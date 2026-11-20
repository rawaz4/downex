package downloads

import (
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"time"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/categories"
	"downex/ui/core/validate"
	"downex/ui/kit"
	"downex/ui/state"
	"downex/ui/views"
)

// ErrNoStores is New's answer for an App without the main window's stores (a progress window).
var ErrNoStores = errors.New("downloads: the App has no download store")

// Downloads owns the main window's download grid and details pane: the widgets, their
// subscriptions to the stores, and what they ask of the composition (App.Open and App.Act).
type Downloads struct {
	app     *views.App
	store   *state.DownloadsStore
	grid    *Grid
	details *Details

	arrivals *arrivals
	// fresh are the rows that just arrived and when (their arrival plays for 900 ms).
	fresh     map[int64]time.Time
	freshAnim *freshAnim
	// rendered are the realised rows of the last refresh, for the ghosts of removed ones.
	rendered     map[int64]renderedRow
	renderedNext map[int64]renderedRow
	// announced is the last I-key announcement (the grid's role=status text).
	announced string

	pending atomic.Bool
	stopMu  sync.Mutex
	stops   []func()
	closed  bool
}

type renderedRow struct {
	d     *downexv1.Download
	index int
}

// arriveFor is how long a new row plays its arrival (Arrivals' holdMs, the arrive keyframes).
const arriveFor = 900 * time.Millisecond

// New makes the grid and the details pane over app's stores and starts following them. The
// composition places Grid and Details (Slots) and fills app.Open and app.Act, which they call.
func New(app *views.App) (*Downloads, error) {
	if app == nil || app.Main == nil || app.Main.Downloads == nil {
		return nil, ErrNoStores
	}
	dl := &Downloads{
		app: app, store: app.Main.Downloads, arrivals: newArrivals(), fresh: map[int64]time.Time{},
		rendered: map[int64]renderedRow{}, renderedNext: map[int64]renderedRow{},
	}
	dl.grid = newGrid(dl)
	dl.details = newDetails(dl)
	dl.grid.table.compact = app.Compact()
	dl.subscribe(&dl.store.Changes)
	if app.Main.Queues != nil {
		dl.subscribe(&app.Main.Queues.Changes)
	}
	if app.Density != nil {
		dl.subscribe(&app.Density.Changes)
	}
	if app.Details != nil {
		dl.subscribe(&app.Details.Changes)
	}
	dl.refresh()
	return dl, nil
}

// subscribe follows a store: its changes run one refresh on the UI thread.
func (dl *Downloads) subscribe(sig *state.Signal) {
	stop := sig.Subscribe(dl.changed)
	dl.stopMu.Lock()
	dl.stops = append(dl.stops, stop)
	dl.stopMu.Unlock()
}

// changed asks for a refresh on the UI thread; changes before it runs share it.
func (dl *Downloads) changed() {
	if dl.pending.CompareAndSwap(false, true) {
		dl.app.UI(func() {
			dl.pending.Store(false)
			dl.refresh()
		})
	}
}

// Grid is the views.DownloadGrid slot.
func (dl *Downloads) Grid() *Grid { return dl.grid }

// Details is the views.DetailsPane slot.
func (dl *Downloads) Details() *Details { return dl.details }

// Slots are the grid and the pane with the status bar the composition has.
func (dl *Downloads) Slots(status views.StatusBar) views.Slots {
	return views.Slots{Grid: dl.grid, Details: dl.details, StatusBar: status}
}

// Dispose stops following the stores.
func (dl *Downloads) Dispose() {
	dl.stopMu.Lock()
	stops := dl.stops
	dl.stops, dl.closed = nil, true
	dl.stopMu.Unlock()
	for _, s := range stops {
		s()
	}
	if dl.freshAnim != nil {
		dl.freshAnim.dead = true
		dl.freshAnim = nil
	}
}

// Refresh brings the grid and the pane up to date with the stores now (the composition and
// tests; the stores' changes do it by themselves).
func (dl *Downloads) Refresh() { dl.refresh() }

func (dl *Downloads) refresh() {
	dl.stopMu.Lock()
	closed := dl.closed
	dl.stopMu.Unlock()
	if closed {
		return
	}
	s := dl.store
	t := dl.grid.table
	t.setCompact(dl.app.Compact())

	// Arrivals and departures: a new row plays its arrival; a removed one that was on screen
	// fades out where it was (both only while motion is allowed).
	dl.arrivals.update(s.All(), s.Ready())
	w := dl.app.Window
	motion := w != nil && w.MotionOK()
	if motion && len(dl.arrivals.added) > 0 {
		now := w.Now()
		for _, id := range dl.arrivals.added {
			dl.fresh[id] = now
		}
		if dl.freshAnim == nil {
			dl.freshAnim = &freshAnim{dl: dl}
			w.Animate(dl.freshAnim)
		}
	}
	if motion && len(dl.arrivals.removed) > 0 {
		var ds []*downexv1.Download
		var idx []int
		for _, id := range dl.arrivals.removed {
			if r, ok := dl.rendered[id]; ok {
				ds = append(ds, r.d)
				idx = append(idx, r.index)
			}
		}
		if len(ds) > 0 {
			dl.grid.ghosts.add(ds, idx)
		}
	}

	t.refresh()
	dl.noteRendered()

	ready := s.Ready()
	search := s.Search()
	cat := s.Category()
	status := s.StatusFilter()
	queue := s.QueueFilter()
	filtered := search != "" && trimmedNonEmpty(search) || cat != categories.All || status != categories.AnyStatus || queue != 0
	title := filterTitle(search, cat, s.Index(), status, queue)
	dl.grid.empty.update(t.model.Len(), ready, s.LoadError(), filtered, title, t.stale)
	dl.details.refresh()
}

func trimmedNonEmpty(s string) bool {
	for _, r := range s {
		if !isSpace(r) {
			return true
		}
	}
	return false
}

func isSpace(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r', '\f', '\v', 0xa0, 0xfeff, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

// noteRendered keeps the rows the list realises now (its window and overscan): their downloads
// and places are where a removed one's ghost shows.
func (dl *Downloads) noteRendered() {
	t := dl.grid.table
	n := t.model.Len()
	rh := float64(t.RowHeightNow())
	clear(dl.renderedNext)
	if n > 0 && rh > 0 {
		top := float64(t.ScrollOffset())
		body := math.Max(0, float64(t.Size().H)-headerHeight(t.layout.Rem))
		first := max(0, int(math.Floor(top/rh))-t.Overscan)
		last := min(n, int(math.Ceil((top+body)/rh))+t.Overscan)
		for i := first; i < last; i++ {
			id := t.model.ids[i]
			if d, ok := dl.store.Get(id); ok {
				dl.renderedNext[id] = renderedRow{d: d, index: i}
			}
		}
	}
	dl.rendered, dl.renderedNext = dl.renderedNext, dl.rendered
}

// freshAnim repaints the arriving rows for as long as one arrives.
type freshAnim struct {
	dl   *Downloads
	dead bool
}

func (a *freshAnim) Step(now time.Time) bool {
	dl := a.dl
	if a.dead {
		return false
	}
	t := dl.grid.table
	for id, at := range dl.fresh {
		if now.Sub(at) >= arriveFor {
			delete(dl.fresh, id)
		}
		if r, ok := t.RowWidget(kit.ListKey(id)).(*row); ok {
			r.invalidateMoved(6)
		}
	}
	if len(dl.fresh) == 0 {
		dl.freshAnim = nil
		return false
	}
	return true
}

func (a *freshAnim) Finish() {
	dl := a.dl
	t := dl.grid.table
	for id := range dl.fresh {
		if r, ok := t.RowWidget(kit.ListKey(id)).(*row); ok {
			r.Invalidate()
		}
		delete(dl.fresh, id)
	}
	dl.freshAnim = nil
	a.dead = true
}

// arrival is a fresh row's arrive keyframes now: opacity and offset ease in over the first 35%
// on --accent-soft, which then eases to the row's ground.
func (dl *Downloads) arrival(id int64) (alpha, dy, tint float32) {
	at, ok := dl.fresh[id]
	w := dl.app.Window
	if !ok || w == nil {
		return 1, 0, 0
	}
	share := float32(w.Now().Sub(at)) / float32(arriveFor)
	switch {
	case share >= 1:
		return 1, 0, 0
	case share < 0.35:
		e := kit.EaseOut(share / 0.35)
		return e, -6 * (1 - e), 1
	default:
		e := kit.EaseOut((share - 0.35) / 0.65)
		return 1, 0, 1 - e
	}
}

// ---- what the grid and the pane ask of the composition -----------------------------------------

// open is a row's Enter or double click (onopen: App.svelte's openDownload).
func (dl *Downloads) open(d *downexv1.Download) {
	if f := dl.app.Act.OpenDownload; f != nil {
		f(d.GetId())
	}
}

// openMenu is the row menu at a window point (onmenu).
func (dl *Downloads) openMenu(at kit.Point) {
	if f := dl.app.Act.OpenMenu; f != nil {
		f(at)
	}
}

// openAdd is the empty list's "Add download" and "Add video or stream".
func (dl *Downloads) openAdd(video bool) {
	if f := dl.app.Open.Add; f != nil {
		r := views.AddRequest{}
		if video {
			r.Mode = views.AddStream
		}
		f(r)
	}
}

// openExtension is "Get the browser extension": Settings → Integration.
func (dl *Downloads) openExtension() {
	if f := dl.app.Open.Settings; f != nil {
		f(views.SettingsRequest{Page: validate.PageIntegration})
	}
}

// clearFilters is the filtered list's "Clear filters".
func (dl *Downloads) clearFilters() {
	if f := dl.app.Act.ClearFilters; f != nil {
		f()
	}
}

// rowAction is a row's hover or inline action (App.svelte's onRowAction): unavailable while
// stale, else the same as the toolbar's and the menu's for that one download.
func (dl *Downloads) rowAction(id RowAction, d *downexv1.Download) {
	a := dl.app
	if a.Stale() {
		a.Unavailable()
		return
	}
	ids := []int64{d.GetId()}
	switch id {
	case ActPause:
		if f := a.Act.Pause; f != nil {
			f(ids)
		}
	case ActResume:
		if f := a.Act.Resume; f != nil {
			f(ids)
		}
	case ActConfirm:
		if f := a.Open.Confirm; f != nil {
			f(views.ConfirmRequest{ID: d.GetId(), Requested: true})
		}
	case ActFolder:
		if f := a.Act.OpenFolder; f != nil {
			f(d.GetId())
		}
	case ActRemove:
		if f := a.Open.Remove; f != nil {
			f(views.RemoveRequest{IDs: ids})
		}
	case ActRestart:
		dl.restart(d)
	case ActRenew:
		if f := a.Open.Renew; f != nil {
			f(views.RenewRequest{ID: d.GetId()})
		}
	}
}

// restart asks to restart one download (requestRestart with needsRestart's reason).
func (dl *Downloads) restart(d *downexv1.Download) {
	if f := dl.app.Open.Restart; f != nil {
		reason := views.RestartManual
		if views.NeedsRestart(d) {
			reason = views.RestartValidatorChanged
		}
		f(views.RestartRequest{IDs: []int64{d.GetId()}, Reason: reason})
	}
}
