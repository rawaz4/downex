package state

import (
	"context"
	"slices"
	"strconv"
	"sync"
	"time"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/categories"
	"downex/ui/core/confirm"
	"downex/ui/core/display"
	"downex/ui/core/query"
	"downex/ui/core/selection"
	"downex/ui/host"
)

// Limits of the download list (stores/downloads.svelte.ts).
const (
	// HistoryMS is the sparkline window (display.HistoryMS).
	HistoryMS = display.HistoryMS
	// MaxSamples bounds a speed history.
	MaxSamples = 400
	// BulkConcurrency is the number of calls a bulk action runs at once (selecting 10,000
	// rows must not open 10,000 calls).
	BulkConcurrency = 8
	// StatusInterval is the daemon status check interval (also the reconnect retry interval).
	StatusInterval = 3 * time.Second
)

// SpeedSample is one point of a speed history (display.SpeedSample).
type SpeedSample = display.SpeedSample

// Outcome is what a bulk action did to one download: Err nil when it worked.
type Outcome struct {
	ID  int64
	Err *host.CallError
}

// OK reports that the action worked.
func (o Outcome) OK() bool { return o.Err == nil }

// AddOutcome is what adding one address did: the download, or why it failed.
type AddOutcome struct {
	URL      string
	Download *Download
	Err      *host.CallError
}

// OK reports that the address was added.
func (o AddOutcome) OK() bool { return o.Err == nil }

// DaemonState is the last status check: the daemon answered (Status) or did not (Error).
type DaemonState struct {
	OK     bool
	Status *downexv1.StatusResponse
	Error  string
}

// Totals are the status bar's numbers.
type Totals struct {
	// Speed is the total of the running downloads; 0 while the list is stale.
	Speed  float64
	Active int
	Queued int
	Count  int
}

// ResumeOptions are a resume's options (api.ts ResumeOptions).
type ResumeOptions struct {
	// Restart discards saved data and downloads from the start; a completed download is
	// resumed only with it (downloaded again).
	Restart bool
	// ExistingFile is for a restart of a completed download (never sent without Restart).
	ExistingFile   downexv1.ExistingFile
	MaxConnections *int32
	SpeedLimit     *int64
}

// The choice for a completed download's file when it is downloaded again.
const (
	KeepBoth = downexv1.ExistingFile_EXISTING_FILE_KEEP_BOTH
	Replace  = downexv1.ExistingFile_EXISTING_FILE_REPLACE
)

// FileActions opens a download's file or its folder (host.Files).
type FileActions interface {
	OpenFile(ctx context.Context, id int64) error
	OpenFolder(ctx context.Context, id int64) error
}

// DownloadsOptions are what a DownloadsStore works with.
type DownloadsOptions struct {
	Daemon host.Daemon
	Events Events
	// Files opens files and folders (nil: those actions answer unavailable).
	Files FileActions
	// CopyText puts text on the clipboard (nil: CopyURLs copies nothing and answers
	// unavailable).
	CopyText func(ctx context.Context, text string) error
	Clock    Clock
	Frames   Frames
	// Call gives the store's own calls their context (status checks, reloads).
	Call CallContext
	// Collator orders names (nil: the root order); query.NewCollator(format.Locale().Tag())
	// follows the user's locale as today's window does.
	Collator query.Collator
}

// DownloadsStore is the download list (stores/downloads.svelte.ts). Downloads live in a
// map replaced entry by entry from event batches; a revision makes the derived views
// (sorted and filtered rows, sidebar counts, selection) recompute once per batch. The
// sorted order is patched incrementally (query.SortedView), and the answers of bulk
// actions are coalesced to one revision per frame.
type DownloadsStore struct {
	Changes Signal

	daemon   host.Daemon
	events   Events
	files    FileActions
	copyText func(context.Context, string) error
	clock    Clock
	frames   Frames
	call     CallContext

	// seq orders the application of event batches (the replay after the first load).
	seq       sync.Mutex
	buffering bool
	buffer    []stampedBatch

	mu      sync.Mutex
	items   map[int64]*Download
	ids     []int64 // insertion order, as a JavaScript Map keeps it
	history map[int64][]SpeedSample
	reports reportOrder
	order   query.SortedView
	search  query.Searcher
	unsub   func()

	rev, filterGen, sortGen, catGen int

	totalHistory []SpeedSample
	ready        bool
	loadError    string
	daemonState  *DaemonState
	checking     bool
	nextCheckAt  time.Time

	category   categories.Filter
	status     categories.Status
	queue      int64
	searchText string
	sort       query.SortSpec
	sel        *selection.Selection
	cats       []*downexv1.Category
	index      *categories.Index

	// status polling
	check     chan struct{} // the check in flight; closed when it ends
	polling   bool
	pollEvery time.Duration
	timer     Timer
	disposed  bool

	// bulk actions
	bulkDepth   int
	frameCancel func()
	frameToken  int

	c cache
}

type cache struct {
	allRev                int
	all                   []*Download
	sortedRev, sortedSort int
	sorted                []*Download
	visKey                [4]int
	visible               []*Download
	visibleIDs            []int64
	indexOf               selection.IndexOf
	countsKey             [3]int
	counts                query.Counts
	queueCountsRev        int
	queueCounts           map[int64]int
	awaitingRev           int
	awaiting              []*Download
	valid                 uint8
}

const (
	validAll uint8 = 1 << iota
	validSorted
	validVisible
	validCounts
	validQueueCounts
	validAwaiting
)

// DefaultCategories are the daemon's default categories (api.ts DEFAULT_CATEGORIES).
func DefaultCategories() []*downexv1.Category {
	return []*downexv1.Category{
		{Name: "Video", Extensions: []string{"mp4", "mkv", "avi", "mov", "webm", "wmv", "flv", "m4v", "mpg", "mpeg", "ts", "3gp"}},
		{Name: "Audio", Extensions: []string{"mp3", "flac", "m4a", "aac", "wav", "ogg", "opus", "wma"}},
		{Name: "Archives", Extensions: []string{"zip", "rar", "7z", "gz", "xz", "bz2", "tar", "tgz", "zst", "iso", "img", "dmg"}},
		{Name: "Documents", Extensions: []string{"pdf", "epub", "doc", "docx", "xls", "xlsx", "ppt", "pptx", "odt", "ods", "txt", "csv", "md"}},
		{Name: "Programs", Extensions: []string{"exe", "msi", "msix", "appx", "apk", "deb", "rpm", "appimage", "bat", "cmd", "ps1", "jar"}},
	}
}

// NewDownloads makes the store; Start subscribes and loads the list.
func NewDownloads(o DownloadsOptions) *DownloadsStore {
	cats := DefaultCategories()
	s := &DownloadsStore{
		daemon:    o.Daemon,
		events:    o.Events,
		files:     o.Files,
		copyText:  o.CopyText,
		clock:     clockOr(o.Clock),
		call:      callOr(o.Call),
		items:     map[int64]*Download{},
		history:   map[int64][]SpeedSample{},
		category:  categories.All,
		status:    categories.AnyStatus,
		sort:      query.DefaultSort,
		sel:       selection.Empty,
		cats:      cats,
		index:     categories.NewIndex(cats),
		pollEvery: StatusInterval,
	}
	s.frames = o.Frames
	if s.frames == nil {
		s.frames = TimerFrames{Clock: s.clock}
	}
	s.order.Collator = o.Collator
	return s
}

// ---- reading -------------------------------------------------------------------------

// Get is the download id as last reported.
func (s *DownloadsStore) Get(id int64) (*Download, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.items[id]
	return d, ok
}

// HistoryOf is the speed history of a running download over the last HistoryMS.
func (s *DownloadsStore) HistoryOf(id int64) []SpeedSample {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.history[id]
}

// TotalHistory is the total speed of every running download over the last HistoryMS (the
// status bar's sparkline): one sample per event batch that changed something.
func (s *DownloadsStore) TotalHistory() []SpeedSample {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.totalHistory
}

// Ready reports that the list was loaded once.
func (s *DownloadsStore) Ready() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.ready }

// LoadError is why the list could not be loaded ("" when it was).
func (s *DownloadsStore) LoadError() string { s.mu.Lock(); defer s.mu.Unlock(); return s.loadError }

// Daemon is the last status check; nil before the first one.
func (s *DownloadsStore) Daemon() *DaemonState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.daemonState
}

// Checking reports a status check in flight ("Retry now" shows progress).
func (s *DownloadsStore) Checking() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.checking }

// NextCheckAt is when the next automatic status check runs (zero: none is scheduled).
func (s *DownloadsStore) NextCheckAt() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nextCheckAt
}

// Category is the sidebar's category filter.
func (s *DownloadsStore) Category() categories.Filter {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.category
}

// StatusFilter is the sidebar's status filter.
func (s *DownloadsStore) StatusFilter() categories.Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// QueueFilter is the sidebar's queue filter (0: every queue).
func (s *DownloadsStore) QueueFilter() int64 { s.mu.Lock(); defer s.mu.Unlock(); return s.queue }

// Search is the search text.
func (s *DownloadsStore) Search() string { s.mu.Lock(); defer s.mu.Unlock(); return s.searchText }

// Sort is the list's sort.
func (s *DownloadsStore) Sort() query.SortSpec { s.mu.Lock(); defer s.mu.Unlock(); return s.sort }

// Selection is the selection (never modified once handed out).
func (s *DownloadsStore) Selection() *selection.Selection {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sel
}

// Categories are Settings.categories (the defaults until settings are loaded).
func (s *DownloadsStore) Categories() []*downexv1.Category {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cats
}

// Index groups the downloads into the configured categories and "other".
func (s *DownloadsStore) Index() *categories.Index { s.mu.Lock(); defer s.mu.Unlock(); return s.index }

// Stale reports that the daemon is unreachable (or the list could not be loaded): what is
// shown is the last known state, live values are not current, and actions cannot be
// carried out.
func (s *DownloadsStore) Stale() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.staleLocked() }

func (s *DownloadsStore) staleLocked() bool {
	return (s.daemonState != nil && !s.daemonState.OK) || s.loadError != ""
}

// All is every download, in the order they became known.
func (s *DownloadsStore) All() []*Download { s.mu.Lock(); defer s.mu.Unlock(); return s.allLocked() }

func (s *DownloadsStore) allLocked() []*Download {
	if s.c.valid&validAll != 0 && s.c.allRev == s.rev {
		return s.c.all
	}
	out := make([]*Download, 0, len(s.ids))
	for _, id := range s.ids {
		out = append(out, s.items[id])
	}
	s.c.all, s.c.allRev = out, s.rev
	s.c.valid |= validAll
	return out
}

func (s *DownloadsStore) sortedLocked() []*Download {
	if s.c.valid&validSorted != 0 && s.c.sortedRev == s.rev && s.c.sortedSort == s.sortGen {
		return s.c.sorted
	}
	s.c.sorted, _ = s.order.Update(s.items, s.sort)
	s.c.sortedRev, s.c.sortedSort = s.rev, s.sortGen
	s.c.valid |= validSorted
	return s.c.sorted
}

func (s *DownloadsStore) visibleLocked() []*Download {
	key := [4]int{s.rev, s.sortGen, s.filterGen, s.catGen}
	if s.c.valid&validVisible != 0 && s.c.visKey == key {
		return s.c.visible
	}
	vis := s.search.Filter(s.sortedLocked(), query.FilterSpec{
		Category: s.category, Status: s.status, Search: s.searchText, Queue: s.queue,
	}, s.index)
	ids := make([]int64, len(vis))
	for i, d := range vis {
		ids[i] = d.GetId()
	}
	s.c.visible, s.c.visibleIDs, s.c.indexOf, s.c.visKey = vis, ids, selection.Indexer(ids), key
	s.c.valid |= validVisible
	return vis
}

// Counts are the sidebar's faceted counts.
func (s *DownloadsStore) Counts() query.Counts {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := [3]int{s.rev, s.filterGen, s.catGen}
	if s.c.valid&validCounts == 0 || s.c.countsKey != key {
		s.c.counts = query.CountFacets(s.allLocked(), s.category, s.status, s.index, s.queue)
		s.c.countsKey = key
		s.c.valid |= validCounts
	}
	return s.c.counts
}

// QueueCounts is, per queue, its downloads that are not completed (the sidebar's queue
// counts).
func (s *DownloadsStore) QueueCounts() map[int64]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.c.valid&validQueueCounts == 0 || s.c.queueCountsRev != s.rev {
		s.c.queueCounts = query.CountQueues(s.allLocked())
		s.c.queueCountsRev = s.rev
		s.c.valid |= validQueueCounts
	}
	return s.c.queueCounts
}

// Visible is the rows shown, in display order.
func (s *DownloadsStore) Visible() []*Download {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.visibleLocked()
}

// VisibleIDs are the ids of Visible.
func (s *DownloadsStore) VisibleIDs() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.visibleLocked()
	return s.c.visibleIDs
}

// IndexOf is a row's index in Visible, -1 when it is not shown.
func (s *DownloadsStore) IndexOf(id int64) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.indexOfLocked(id)
}

func (s *DownloadsStore) indexOfLocked(id int64) int {
	s.visibleLocked()
	return s.c.indexOf(id)
}

// Awaiting are the downloads awaiting confirmation, oldest first.
func (s *DownloadsStore) Awaiting() []*Download {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.c.valid&validAwaiting == 0 || s.c.awaitingRev != s.rev {
		s.c.awaiting = confirm.AwaitingInOrder(s.allLocked())
		s.c.awaitingRev = s.rev
		s.c.valid |= validAwaiting
	}
	return s.c.awaiting
}

// Selected are the selected downloads in display order; rows hidden by filters are never
// acted on.
func (s *DownloadsStore) Selected() []*Download {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.selectedLocked()
}

func (s *DownloadsStore) selectedLocked() []*Download {
	if s.sel.Len() == 0 {
		return nil
	}
	var out []*Download
	for _, d := range s.visibleLocked() {
		if s.sel.Has(d.GetId()) {
			out = append(out, d)
		}
	}
	return out
}

// Detail is the download the details panel shows: the cursor row when it is selected and
// shown, else the only selected one.
func (s *DownloadsStore) Detail() (*Download, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if focus, ok := s.sel.Focus(); ok && s.sel.Has(focus) && s.indexOfLocked(focus) >= 0 {
		d, ok := s.items[focus]
		return d, ok
	}
	if sel := s.selectedLocked(); len(sel) == 1 {
		return sel[0], true
	}
	return nil, false
}

// Totals are the status bar's numbers.
func (s *DownloadsStore) Totals() Totals {
	s.mu.Lock()
	defer s.mu.Unlock()
	var t Totals
	for _, d := range s.allLocked() {
		switch d.GetState() {
		case StateDownloading:
			t.Speed += d.GetSpeedBps()
			t.Active++
		case StateProbing:
			t.Active++
		case StateQueued:
			t.Queued++
		}
	}
	if s.staleLocked() {
		t.Speed = 0 // a stale list must not claim data is still arriving
	}
	t.Count = len(s.ids)
	return t
}

// NameOf is a download's display name (bidi controls made visible); "#<id>" for an
// unknown id.
func (s *DownloadsStore) NameOf(id int64) string {
	s.mu.Lock()
	d := s.items[id]
	s.mu.Unlock()
	if d == nil {
		return "#" + strconv.FormatInt(id, 10)
	}
	return categories.DisplayName(d)
}

// ---- loading and events --------------------------------------------------------------

// Start subscribes to events, then loads the list; batches that arrive meanwhile are
// applied after it. A second Start does nothing.
func (s *DownloadsStore) Start(ctx context.Context) {
	s.seq.Lock()
	s.mu.Lock()
	if s.unsub != nil || s.disposed {
		s.mu.Unlock()
		s.seq.Unlock()
		return
	}
	s.buffering = true
	s.buffer = nil
	s.unsub = func() {}
	s.mu.Unlock()
	s.seq.Unlock()
	if s.events != nil {
		unsub := s.events.Subscribe(s.onBatch)
		s.mu.Lock()
		s.unsub = unsub
		s.mu.Unlock()
	}
	s.Reload(ctx)
	s.seq.Lock()
	pending := s.buffer
	s.buffer, s.buffering = nil, false
	for _, b := range pending {
		s.apply(b.batch, &b)
	}
	s.seq.Unlock()
}

// stampedBatch is a batch that arrived during the first load, with its orders (reportOrder).
type stampedBatch struct {
	batch            Batch
	events, snapshot uint64
}

func (s *DownloadsStore) onBatch(b Batch) {
	s.seq.Lock()
	defer s.seq.Unlock()
	if s.buffering {
		s.mu.Lock()
		events, snapshot := s.reports.arrived()
		s.mu.Unlock()
		s.buffer = append(s.buffer, stampedBatch{b, events, snapshot})
		return
	}
	s.Apply(b)
}

// Reload replaces the whole list from List (the first load, or after the daemon
// reconnects). What the window learned after sending List is newer and stays: a report
// newer than the listed one, a download reported meanwhile, a removal (reportOrder).
func (s *DownloadsStore) Reload(ctx context.Context) {
	s.mu.Lock()
	at := s.reports.sent()
	s.mu.Unlock()
	resp, err := s.daemon.List(ctx)
	s.mu.Lock()
	if err != nil {
		s.reports.landed()
		s.loadError = Message(err)
		s.mu.Unlock()
		s.Changes.Notify()
		return
	}
	listed := make(map[int64]*Download, len(resp.GetDownloads()))
	order := make([]int64, 0, len(resp.GetDownloads()))
	for _, d := range resp.GetDownloads() {
		if _, dup := listed[d.GetId()]; !dup {
			order = append(order, d.GetId())
		}
		listed[d.GetId()] = d
	}
	items := make(map[int64]*Download, len(order))
	ids := make([]int64, 0, len(order))
	held := make(map[int64]uint64, len(order))
	for _, id := range order {
		d, prev := listed[id], s.items[id]
		switch {
		case s.reports.newer(d, prev, at):
			items[id], held[id] = d, at
		case prev != nil:
			items[id], held[id] = prev, s.reports.held[id]
		default:
			continue // removed after List was sent
		}
		ids = append(ids, id)
	}
	for _, id := range s.ids {
		if _, ok := listed[id]; !ok && s.reports.held[id] > at {
			items[id], held[id] = s.items[id], s.reports.held[id] // reported after List was sent
			ids = append(ids, id)
		}
	}
	for id := range s.history {
		if _, ok := items[id]; !ok {
			delete(s.history, id)
		}
	}
	s.items, s.ids, s.reports.held = items, ids, held
	s.reports.landed()
	s.order.Invalidate()
	s.loadError = ""
	s.ready = true
	s.bumpLocked()
	s.mu.Unlock()
	s.Changes.Notify()
}

// Apply takes one batch of events (queue and tools events are other stores').
func (s *DownloadsStore) Apply(batch Batch) {
	s.apply(batch, nil)
}

// apply takes a batch with its orders (reportOrder.arrived; nil: it arrives now).
func (s *DownloadsStore) apply(batch Batch, stamp *stampedBatch) {
	s.mu.Lock()
	var events, snapshot uint64
	if stamp != nil {
		events, snapshot = stamp.events, stamp.snapshot
	} else {
		events, snapshot = s.reports.arrived()
	}
	changed := false
	t := millis(s.clock.Now())
	for _, ev := range batch {
		switch k := ev.GetKind().(type) {
		case *downexv1.Event_Removed:
			if s.deleteLocked(k.Removed, events) {
				changed = true
			}
		case *downexv1.Event_Snapshot:
			if s.putLocked(k.Snapshot, t, snapshot) {
				changed = true
			}
		case *downexv1.Event_Updated:
			if s.putLocked(k.Updated, t, events) {
				changed = true
			}
		}
	}
	if changed {
		s.bumpLocked()
		s.sampleTotalLocked(t)
	}
	s.mu.Unlock()
	if changed {
		s.Changes.Notify()
	}
}

// bumpLocked is a new revision: derived views recompute and the selection drops ids
// that are gone.
func (s *DownloadsStore) bumpLocked() {
	s.rev++
	items := s.items
	s.sel = selection.Prune(s.sel, func(id int64) bool { _, ok := items[id]; return ok })
}

func trimHistory(old []SpeedSample, cutoff float64) []SpeedSample {
	from := 0
	for from < len(old) && old[from].T < cutoff {
		from++
	}
	return old[max(from, len(old)+1-MaxSamples):]
}

// sampleTotalLocked adds the total speed now to TotalHistory.
func (s *DownloadsStore) sampleTotalLocked(t float64) {
	speed := 0.0
	for _, d := range s.items {
		if d.GetState() == StateDownloading && d.GetSpeedBps() > 0 {
			speed += d.GetSpeedBps()
		}
	}
	kept := trimHistory(s.totalHistory, t-HistoryMS)
	next := make([]SpeedSample, len(kept), len(kept)+1)
	copy(next, kept)
	s.totalHistory = append(next, SpeedSample{T: t, V: speed})
}

// putLocked stores a download, learned at at (reportOrder), unless the one held is newer.
func (s *DownloadsStore) putLocked(d *Download, t float64, at uint64) bool {
	prev := s.items[d.GetId()]
	if !s.reports.newer(d, prev, at) {
		return false
	}
	s.reports.hold(d.GetId(), at)
	s.order.Touch(d.GetId(), prev)
	if prev == nil {
		s.ids = append(s.ids, d.GetId())
	}
	s.items[d.GetId()] = d
	if d.GetState() == StateDownloading {
		kept := trimHistory(s.history[d.GetId()], t-HistoryMS)
		next := make([]SpeedSample, len(kept), len(kept)+1)
		copy(next, kept)
		s.history[d.GetId()] = append(next, SpeedSample{T: t, V: d.GetSpeedBps()})
	}
	return true
}

// deleteLocked removes a download, as learned at at (0: now).
func (s *DownloadsStore) deleteLocked(id int64, at uint64) bool {
	prev := s.items[id]
	s.reports.remove(id, at)
	delete(s.history, id)
	if prev == nil {
		return false
	}
	s.order.Touch(id, prev)
	delete(s.items, id)
	if i := slices.Index(s.ids, id); i >= 0 {
		s.ids = slices.Delete(slices.Clone(s.ids), i, i+1)
	}
	return true
}

// changedLocked is one revision now or, during a bulk action, at most one per frame. It
// reports whether the change shows now (the caller notifies).
func (s *DownloadsStore) changedLocked() bool {
	if s.bulkDepth == 0 {
		s.bumpLocked()
		return true
	}
	if s.frameCancel == nil {
		s.frameToken++
		token := s.frameToken
		s.frameCancel = s.frames.Next(func() {
			s.mu.Lock()
			if s.frameCancel == nil || s.frameToken != token {
				s.mu.Unlock()
				return
			}
			s.frameCancel = nil
			s.bumpLocked()
			s.mu.Unlock()
			s.Changes.Notify()
		})
	}
	return false
}

// answer sends call, whose answer reports a download (report picks it out), and stores
// that report unless the window learned a newer one meanwhile (reportOrder).
func answer[R any](s *DownloadsStore, call func() (R, error), report func(R) *Download) (R, error) {
	s.mu.Lock()
	at := s.reports.sent()
	s.mu.Unlock()
	r, err := call()
	var d *Download
	if err == nil {
		d = report(r)
	}
	s.mu.Lock()
	show := d != nil && s.putLocked(d, millis(s.clock.Now()), at) && s.changedLocked()
	s.reports.landed()
	s.mu.Unlock()
	if show {
		s.Changes.Notify()
	}
	return r, err
}

// Reread reads the downloads ids again, at most BulkConcurrency at a time, for a change
// downexd publishes no event for (the queue_position of the downloads a move passes). An
// answer replaces the download held only when it is newer (reportOrder); a download that
// is gone or a read that failed is left to its own event or the next reload.
func (s *DownloadsStore) Reread(ctx context.Context, ids []int64) {
	if s.daemon == nil {
		return
	}
	MapLimit(ids, BulkConcurrency, func(id int64) struct{} {
		_, _ = answer(s, func() (*Download, error) { return s.daemon.Get(ctx, id) }, func(d *Download) *Download { return d })
		return struct{}{}
	})
}

func (s *DownloadsStore) removed(id int64) {
	s.mu.Lock()
	show := s.deleteLocked(id, 0) && s.changedLocked()
	s.mu.Unlock()
	if show {
		s.Changes.Notify()
	}
}

// bulk runs fn with the store's revisions coalesced into frames; when the last bulk
// action ends, the final state is shown at once.
func bulk[T any](s *DownloadsStore, fn func() T) T {
	s.mu.Lock()
	s.bulkDepth++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.bulkDepth--
		settle := s.bulkDepth == 0 && s.frameCancel != nil
		if settle {
			s.frameCancel()
			s.frameCancel = nil
			s.bumpLocked()
		}
		s.mu.Unlock()
		if settle {
			s.Changes.Notify()
		}
	}()
	return fn()
}

// ---- daemon status -------------------------------------------------------------------

// RefreshStatus checks the daemon once and reloads the list after it comes back.
// Concurrent calls share one check.
func (s *DownloadsStore) RefreshStatus() {
	s.mu.Lock()
	if s.check != nil {
		ch := s.check
		s.mu.Unlock()
		<-ch
		return
	}
	ch := make(chan struct{})
	s.check = ch
	wasDown := s.daemonState != nil && !s.daemonState.OK
	s.checking = true
	s.mu.Unlock()
	s.Changes.Notify()

	status, err := callWith(s.call, s.daemon.Status)
	s.mu.Lock()
	if err != nil {
		s.daemonState = &DaemonState{Error: Message(err)}
	} else {
		s.daemonState = &DaemonState{OK: true, Status: status}
	}
	reload := err == nil && (wasDown || s.loadError != "" || !s.ready)
	s.mu.Unlock()
	if reload {
		ctx, cancel := s.call()
		s.Reload(ctx)
		cancel()
	}
	s.mu.Lock()
	s.checking = false
	s.check = nil
	s.mu.Unlock()
	close(ch)
	s.Changes.Notify()
}

// StartStatusPolling checks the daemon now and then every interval (StatusInterval when
// 0) until Dispose.
func (s *DownloadsStore) StartStatusPolling(interval time.Duration) {
	if interval <= 0 {
		interval = StatusInterval
	}
	s.mu.Lock()
	if s.polling || s.disposed {
		s.mu.Unlock()
		return
	}
	s.polling = true
	s.pollEvery = interval
	s.mu.Unlock()
	s.clock.AfterFunc(0, s.poll)
}

func (s *DownloadsStore) poll() {
	s.mu.Lock()
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	s.nextCheckAt = time.Time{}
	s.mu.Unlock()
	s.RefreshStatus()
	s.mu.Lock()
	if !s.polling || s.timer != nil {
		s.mu.Unlock()
		return
	}
	s.nextCheckAt = s.clock.Now().Add(s.pollEvery)
	s.timer = s.clock.AfterFunc(s.pollEvery, s.poll)
	s.mu.Unlock()
	s.Changes.Notify()
}

// RetryNow is "Retry now": it checks at once and restarts the retry countdown (a check
// in flight is joined).
func (s *DownloadsStore) RetryNow() {
	s.mu.Lock()
	inFlight := s.check
	polling := s.polling
	s.mu.Unlock()
	switch {
	case inFlight != nil:
		<-inFlight
	case polling:
		s.poll()
	default:
		s.RefreshStatus()
	}
}

// Dispose unsubscribes and stops the checks and a pending frame.
func (s *DownloadsStore) Dispose() {
	s.mu.Lock()
	unsub := s.unsub
	s.unsub = nil
	s.disposed = true
	s.polling = false
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	if s.frameCancel != nil {
		s.frameCancel()
		s.frameCancel = nil
	}
	s.mu.Unlock()
	if unsub != nil {
		unsub()
	}
}

// ---- filters -------------------------------------------------------------------------

func (s *DownloadsStore) filterChanged() {
	s.filterGen++
	s.pruneToVisibleLocked()
	s.mu.Unlock()
	s.Changes.Notify()
}

func (s *DownloadsStore) pruneToVisibleLocked() {
	s.visibleLocked()
	indexOf := s.c.indexOf
	s.sel = selection.Prune(s.sel, func(id int64) bool { return indexOf(id) >= 0 })
}

// SetCategory shows one category.
func (s *DownloadsStore) SetCategory(f categories.Filter) {
	s.mu.Lock()
	s.category = f
	s.filterChanged()
}

// SetCategories takes the categories of new settings; a filter on a category that is
// gone shows all.
func (s *DownloadsStore) SetCategories(cats []*downexv1.Category) {
	s.mu.Lock()
	s.cats = cats
	s.index = categories.NewIndex(cats)
	s.catGen++
	if !s.index.Has(s.category) {
		s.category = categories.All
	}
	s.filterChanged()
}

// SetStatusFilter shows one status.
func (s *DownloadsStore) SetStatusFilter(f categories.Status) {
	s.mu.Lock()
	s.status = f
	s.filterChanged()
}

// SetQueueFilter shows the downloads of one queue (0: of every queue).
func (s *DownloadsStore) SetQueueFilter(queue int64) {
	s.mu.Lock()
	s.queue = queue
	s.filterChanged()
}

// SetQueues takes the current queues: a filter on a queue that is gone shows every queue.
func (s *DownloadsStore) SetQueues(ids map[int64]bool) {
	s.mu.Lock()
	if s.queue == 0 || ids[s.queue] {
		s.mu.Unlock()
		return
	}
	s.queue = 0
	s.filterChanged()
}

// SetSearch filters by name and address.
func (s *DownloadsStore) SetSearch(text string) {
	s.mu.Lock()
	s.searchText = text
	s.filterChanged()
}

// SortBy is a click on a column header.
func (s *DownloadsStore) SortBy(key query.SortKey) {
	s.mu.Lock()
	s.sort = query.ToggleSort(s.sort, key)
	s.sortGen++
	s.mu.Unlock()
	s.Changes.Notify()
}

// ---- selection -----------------------------------------------------------------------

func (s *DownloadsStore) selectWith(f func() *selection.Selection) {
	s.mu.Lock()
	next := f()
	changed := next != s.sel
	s.sel = next
	s.mu.Unlock()
	if changed {
		s.Changes.Notify()
	}
}

// Click is a click on a row.
func (s *DownloadsStore) Click(id int64, mods selection.Modifiers) {
	s.selectWith(func() *selection.Selection {
		s.visibleLocked()
		return selection.Click(s.sel, s.c.visibleIDs, s.c.indexOf, id, mods)
	})
}

// ContextClick is a right-click on a row.
func (s *DownloadsStore) ContextClick(id int64) {
	s.selectWith(func() *selection.Selection { return selection.Context(s.sel, id) })
}

// Move is keyboard movement.
func (s *DownloadsStore) Move(move selection.Move, mods selection.Modifiers) {
	s.selectWith(func() *selection.Selection {
		s.visibleLocked()
		return selection.MoveFocus(s.sel, s.c.visibleIDs, s.c.indexOf, move, mods)
	})
}

// SetCursor puts the keyboard cursor on a shown row without changing the selection.
func (s *DownloadsStore) SetCursor(id int64) {
	s.selectWith(func() *selection.Selection {
		i := s.indexOfLocked(id)
		if i < 0 {
			return s.sel
		}
		// {...selection, focus: id}: moving the cursor with Toggle keeps ids and anchor.
		cursor := selection.Modifiers{Toggle: true}
		next := selection.MoveFocus(s.sel, s.c.visibleIDs, s.c.indexOf, selection.First, cursor)
		return selection.MoveFocus(next, s.c.visibleIDs, s.c.indexOf, selection.By(i), cursor)
	})
}

// ToggleFocused is Ctrl+Space.
func (s *DownloadsStore) ToggleFocused() {
	s.selectWith(func() *selection.Selection { return selection.ToggleFocused(s.sel) })
}

// SelectAll selects every shown row.
func (s *DownloadsStore) SelectAll() {
	s.selectWith(func() *selection.Selection {
		s.visibleLocked()
		return selection.All(s.sel, s.c.visibleIDs)
	})
}

// SelectOnly selects one row.
func (s *DownloadsStore) SelectOnly(id int64) {
	s.selectWith(func() *selection.Selection { return selection.SelectOnly(id) })
}

// ClearSelection selects nothing.
func (s *DownloadsStore) ClearSelection() {
	s.selectWith(func() *selection.Selection { return selection.Empty })
}

// ---- actions -------------------------------------------------------------------------

func (s *DownloadsStore) each(ids []int64, fn func(id int64) error) []Outcome {
	return bulk(s, func() []Outcome {
		return MapLimit(ids, BulkConcurrency, func(id int64) Outcome {
			if err := fn(id); err != nil {
				return Outcome{ID: id, Err: ErrorOf(err)}
			}
			return Outcome{ID: id}
		})
	})
}

func (s *DownloadsStore) targets(ids []int64, keep func(*Download) bool) []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []int64
	for _, id := range ids {
		if d := s.items[id]; d != nil && keep(d) {
			out = append(out, id)
		}
	}
	return out
}

// Pause pauses the active ones of ids (the others are left out, not failed).
func (s *DownloadsStore) Pause(ctx context.Context, ids []int64) []Outcome {
	targets := s.targets(ids, func(d *Download) bool { return IsActive(d.GetState()) })
	return s.each(targets, func(id int64) error {
		_, err := answer(s, func() (*downexv1.PauseResponse, error) {
			return s.daemon.Pause(ctx, &downexv1.PauseRequest{Id: id})
		}, (*downexv1.PauseResponse).GetDownload)
		return err
	})
}

func resumeRequest(id int64, o ResumeOptions) *downexv1.ResumeRequest {
	return &downexv1.ResumeRequest{Id: id, Restart: o.Restart, ExistingFile: o.ExistingFile, MaxConnections: o.MaxConnections, SpeedLimit: o.SpeedLimit}
}

// Resume resumes the resumable ones of ids.
func (s *DownloadsStore) Resume(ctx context.Context, ids []int64, opts ResumeOptions) []Outcome {
	targets := s.targets(ids, func(d *Download) bool { return IsResumable(d.GetState()) })
	return s.each(targets, func(id int64) error {
		_, err := answer(s, func() (*downexv1.ResumeResponse, error) {
			return s.daemon.Resume(ctx, resumeRequest(id, opts))
		}, (*downexv1.ResumeResponse).GetDownload)
		return err
	})
}

// Restart discards saved data and starts again; running downloads are paused first. A
// completed one is downloaded again (ENGINE-SPEC §7.1), its file kept beside the new copy
// (KeepBoth, the default for 0) or replaced once the new copy is complete (Replace).
// Downloads awaiting confirmation have not started and are left out.
func (s *DownloadsStore) Restart(ctx context.Context, ids []int64, existing downexv1.ExistingFile) []Outcome {
	if existing == downexv1.ExistingFile_EXISTING_FILE_UNSPECIFIED {
		existing = KeepBoth
	}
	targets := s.targets(ids, func(d *Download) bool { return d.GetState() != StateAwaiting })
	return s.each(targets, func(id int64) error {
		d, _ := s.Get(id)
		if d != nil && IsActive(d.GetState()) {
			_, err := answer(s, func() (*downexv1.PauseResponse, error) {
				return s.daemon.Pause(ctx, &downexv1.PauseRequest{Id: id})
			}, (*downexv1.PauseResponse).GetDownload)
			if err != nil {
				return err
			}
		}
		opts := ResumeOptions{Restart: true}
		if d.GetState() == StateCompleted {
			opts.ExistingFile = existing
		}
		_, err := answer(s, func() (*downexv1.ResumeResponse, error) {
			return s.daemon.Resume(ctx, resumeRequest(id, opts))
		}, (*downexv1.ResumeResponse).GetDownload)
		return err
	})
}

// Remove removes downloads. When that empties the selection, the nearest surviving row
// (the one after the removed ones, else the one before) becomes the selected cursor row,
// so keyboard users keep their place in the list.
func (s *DownloadsStore) Remove(ctx context.Context, ids []int64, deleteFile bool) []Outcome {
	survivor, ok := s.nearestSurvivor(ids)
	out := s.each(ids, func(id int64) error {
		if _, err := s.daemon.Remove(ctx, &downexv1.RemoveRequest{Id: id, DeleteFile: deleteFile}); err != nil {
			return err
		}
		s.removed(id)
		return nil
	})
	if ok {
		s.selectWith(func() *selection.Selection {
			if s.sel.Len() == 0 && s.indexOfLocked(survivor) >= 0 {
				return selection.SelectOnly(survivor)
			}
			return s.sel
		})
	}
	return out
}

func (s *DownloadsStore) nearestSurvivor(ids []int64) (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.visibleLocked()
	order := s.c.visibleIDs
	removing := map[int64]bool{}
	for _, id := range ids {
		removing[id] = true
	}
	start := -1
	if focus, ok := s.sel.Focus(); ok && removing[focus] {
		start = s.c.indexOf(focus)
	}
	if start < 0 {
		for _, id := range ids {
			if i := s.c.indexOf(id); i >= 0 && (start < 0 || i < start) {
				start = i
			}
		}
	}
	if start < 0 {
		return 0, false
	}
	for i := start; i < len(order); i++ {
		if !removing[order[i]] {
			return order[i], true
		}
	}
	for i := start - 1; i >= 0; i-- {
		if !removing[order[i]] {
			return order[i], true
		}
	}
	return 0, false
}

// Add adds addresses, at most BulkConcurrency at a time.
func (s *DownloadsStore) Add(ctx context.Context, reqs []*downexv1.AddRequest) []AddOutcome {
	return bulk(s, func() []AddOutcome {
		return MapLimit(reqs, BulkConcurrency, func(req *downexv1.AddRequest) AddOutcome {
			resp, err := answer(s, func() (*downexv1.AddResponse, error) {
				return s.daemon.Add(ctx, req)
			}, (*downexv1.AddResponse).GetDownload)
			if err != nil {
				return AddOutcome{URL: req.GetUrl(), Err: ErrorOf(err)}
			}
			return AddOutcome{URL: req.GetUrl(), Download: resp.GetDownload()}
		})
	})
}

// AddMedia adds a streaming-media download; the error is the daemon's (encrypted, live, …).
func (s *DownloadsStore) AddMedia(ctx context.Context, req *downexv1.AddMediaRequest) (*Download, error) {
	resp, err := answer(s, func() (*downexv1.AddMediaResponse, error) {
		return s.daemon.AddMedia(ctx, req)
	}, (*downexv1.AddMediaResponse).GetDownload)
	if err != nil {
		return nil, err
	}
	return resp.GetDownload(), nil
}

// ProbeMedia lists the tracks of a manifest or a web page.
func (s *DownloadsStore) ProbeMedia(ctx context.Context, req *downexv1.ProbeMediaRequest) (*downexv1.MediaInfo, error) {
	resp, err := s.daemon.ProbeMedia(ctx, req)
	if err != nil {
		return nil, err
	}
	return resp.GetMedia(), nil
}

// Confirm confirms a download awaiting confirmation.
func (s *DownloadsStore) Confirm(ctx context.Context, req *downexv1.ConfirmRequest) (*Download, error) {
	resp, err := answer(s, func() (*downexv1.ConfirmResponse, error) {
		return s.daemon.Confirm(ctx, req)
	}, (*downexv1.ConfirmResponse).GetDownload)
	if err != nil {
		return nil, err
	}
	return resp.GetDownload(), nil
}

// Rescue finishes a download a browser left unfinished from its partial file
// (docs/DESKTOP-SPEC.md §11); the new download is held at once.
func (s *DownloadsStore) Rescue(ctx context.Context, req *downexv1.RescueRequest) (*downexv1.RescueResponse, error) {
	return answer(s, func() (*downexv1.RescueResponse, error) {
		return s.daemon.Rescue(ctx, req)
	}, (*downexv1.RescueResponse).GetDownload)
}

// RenewLink gives a stopped download a new link (docs/DESKTOP-SPEC.md §10). The answer
// replaces the download held; a download whose link it took (from_download) is gone
// once the removed event arrives.
func (s *DownloadsStore) RenewLink(ctx context.Context, req *downexv1.RenewLinkRequest) (*downexv1.RenewLinkResponse, error) {
	return answer(s, func() (*downexv1.RenewLinkResponse, error) {
		return s.daemon.RenewLink(ctx, req)
	}, (*downexv1.RenewLinkResponse).GetDownload)
}

// AssignQueue records that downloads moved to a queue (after MoveDownloads), until the
// daemon's own updates arrive.
func (s *DownloadsStore) AssignQueue(ids []int64, queueID int64) {
	s.mu.Lock()
	changed := false
	for _, id := range ids {
		d := s.items[id]
		if d == nil || d.GetQueueId() == queueID {
			continue
		}
		s.order.Touch(id, d)
		next := cloneDownload(d)
		next.QueueId = queueID
		s.items[id] = next
		changed = true
	}
	show := changed && s.changedLocked()
	s.mu.Unlock()
	if show {
		s.Changes.Notify()
	}
}

// PauseAll pauses every active download; the events bring the new states.
func (s *DownloadsStore) PauseAll(ctx context.Context) (int32, error) {
	resp, err := s.daemon.PauseAll(ctx)
	return resp.GetPaused(), err
}

// ResumeAll resumes every paused download.
func (s *DownloadsStore) ResumeAll(ctx context.Context) (int32, error) {
	resp, err := s.daemon.ResumeAll(ctx)
	return resp.GetResumed(), err
}

// OpenFile opens a completed download's file.
func (s *DownloadsStore) OpenFile(ctx context.Context, id int64) error {
	if s.files == nil {
		return host.ErrNoWindow()
	}
	return s.files.OpenFile(ctx, id)
}

// OpenFolder shows a download's file in its folder.
func (s *DownloadsStore) OpenFolder(ctx context.Context, id int64) error {
	if s.files == nil {
		return host.ErrNoWindow()
	}
	return s.files.OpenFolder(ctx, id)
}

// CopyURLs copies the addresses of the given downloads, one per line, and returns how many.
func (s *DownloadsStore) CopyURLs(ctx context.Context, ids []int64) (int, error) {
	s.mu.Lock()
	var urls []string
	for _, id := range ids {
		if d := s.items[id]; d != nil {
			urls = append(urls, d.GetUrl())
		}
	}
	s.mu.Unlock()
	if len(urls) == 0 {
		return 0, nil
	}
	if s.copyText == nil {
		return 0, host.ErrNoWindow()
	}
	text := urls[0]
	for _, u := range urls[1:] {
		text += "\n" + u
	}
	if err := s.copyText(ctx, text); err != nil {
		return 0, err
	}
	return len(urls), nil
}
