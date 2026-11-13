// Package query filters, sorts and counts the download list, ported from
// ui/frontend/src/lib/query.ts. The store refreshes these at downexd's 4 batches per second,
// usually with only a few downloads changed, so the search keys are kept per Download message
// (messages are not modified: every update replaces one) and the sorted order is patched instead
// of rebuilt (SortedView).
package query

import (
	"math"
	"slices"
	"strings"
	"sync"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"
	"google.golang.org/protobuf/types/known/timestamppb"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/categories"
	"downex/ui/core/format"
	"downex/ui/core/text"
)

// SortKey is a column the list sorts by.
type SortKey string

const (
	SortName        SortKey = "name"
	SortSize        SortKey = "size"
	SortProgress    SortKey = "progress"
	SortSpeed       SortKey = "speed"
	SortETA         SortKey = "eta"
	SortConnections SortKey = "connections"
	SortStatus      SortKey = "status"
	SortAdded       SortKey = "added"
)

// SortDir is ascending or descending.
type SortDir string

const (
	Asc  SortDir = "asc"
	Desc SortDir = "desc"
)

// SortSpec is a column and its direction.
type SortSpec struct {
	Key SortKey
	Dir SortDir
}

// DefaultSort is the newest first.
var DefaultSort = SortSpec{Key: SortAdded, Dir: Desc}

// mainQueueID is the queue of a download that names none (an older downexd).
const mainQueueID = 1

// QueueOf is the queue a download belongs to: its queue_id, the main queue when it names none.
func QueueOf(d *downexv1.Download) int64 {
	if q := d.GetQueueId(); q > 0 {
		return q
	}
	return mainQueueID
}

// FilterSpec is what the list shows.
type FilterSpec struct {
	Category categories.Filter
	Status   categories.Status
	Search   string
	// Queue shows only the downloads of this queue (M6); 0 = every queue.
	Queue int64
}

type searchKeys struct{ name, url, finalURL string }

// Searcher keeps the lower-case search keys of downloads from one filtering to the next. It is
// not safe for concurrent use; its zero value is ready.
type Searcher struct {
	keys map[*downexv1.Download]searchKeys
}

func (s *Searcher) keysOf(d *downexv1.Download, next map[*downexv1.Download]searchKeys) searchKeys {
	k, ok := s.keys[d]
	if !ok {
		k = searchKeys{
			name:     text.LowerJS(categories.DisplayName(d)),
			url:      text.LowerJS(d.GetUrl()),
			finalURL: text.LowerJS(d.GetFinalUrl()),
		}
	}
	next[d] = k
	return k
}

// Filter keeps the downloads that f shows, in their order; ix groups them into categories.
// The keys of downloads no longer listed are dropped.
func (s *Searcher) Filter(list []*downexv1.Download, f FilterSpec, ix *categories.Index) []*downexv1.Download {
	needle := text.LowerJS(text.TrimJS(f.Search))
	if f.Category == categories.All && f.Status == categories.AnyStatus && needle == "" && f.Queue == 0 {
		return slices.Clone(list)
	}
	var next map[*downexv1.Download]searchKeys
	if needle != "" {
		next = make(map[*downexv1.Download]searchKeys, len(list))
	}
	var out []*downexv1.Download
	for _, d := range list {
		if f.Queue != 0 && QueueOf(d) != f.Queue {
			continue
		}
		if f.Category != categories.All && ix.BucketOf(d) != f.Category {
			continue
		}
		if !categories.MatchesStatus(d.GetState(), f.Status) {
			continue
		}
		if needle != "" {
			k := s.keysOf(d, next)
			if !strings.Contains(k.name, needle) && !strings.Contains(k.url, needle) && (k.finalURL == "" || !strings.Contains(k.finalURL, needle)) {
				continue
			}
		}
		out = append(out, d)
	}
	if next != nil {
		s.keys = next
	}
	return out
}

// FilterDownloads is Filter without kept keys.
func FilterDownloads(list []*downexv1.Download, f FilterSpec, ix *categories.Index) []*downexv1.Download {
	var s Searcher
	return s.Filter(list, f, ix)
}

// stateRank orders states for the status column: activity first.
var stateRank = map[downexv1.DownloadState]float64{
	downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING:           0,
	downexv1.DownloadState_DOWNLOAD_STATE_PROBING:               1,
	downexv1.DownloadState_DOWNLOAD_STATE_AWAITING_CONFIRMATION: 2,
	downexv1.DownloadState_DOWNLOAD_STATE_QUEUED:                3,
	downexv1.DownloadState_DOWNLOAD_STATE_PAUSED:                4,
	downexv1.DownloadState_DOWNLOAD_STATE_FAILED:                5,
	downexv1.DownloadState_DOWNLOAD_STATE_CANCELED:              6,
	downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED:             7,
	downexv1.DownloadState_DOWNLOAD_STATE_UNSPECIFIED:           8,
}

// LiveSpeed is a download's speed while it downloads, else 0.
func LiveSpeed(d *downexv1.Download) float64 {
	if d.GetState() == downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING {
		return d.GetSpeedBps()
	}
	return 0
}

// LiveConnections is a download's connections while it downloads, else 0.
func LiveConnections(d *downexv1.Download) int32 {
	if d.GetState() == downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING {
		return d.GetConnections()
	}
	return 0
}

// millis is a timestamp in epoch milliseconds, 0 when unset or not after the epoch.
func millis(ts *timestamppb.Timestamp) float64 {
	if ts == nil {
		return 0
	}
	if ms := ts.AsTime().UnixMilli(); ms > 0 {
		return float64(ms)
	}
	return 0
}

// numericValue is the number a download is ordered by; NaN = unknown (sorts last in both
// directions).
func numericValue(d *downexv1.Download, key SortKey) float64 {
	switch key {
	case SortSize:
		return float64(d.GetSize())
	case SortProgress:
		return format.ProgressFraction(d)
	case SortSpeed:
		return LiveSpeed(d)
	case SortETA:
		return format.EtaSeconds(d)
	case SortConnections:
		return float64(LiveConnections(d))
	case SortStatus:
		// activity first, then queue position (always far below 1e9)
		rank, ok := stateRank[d.GetState()]
		if !ok {
			return math.NaN()
		}
		return rank*1e9 + float64(d.GetQueuePosition())
	case SortAdded:
		return millis(d.GetCreatedAt())
	}
	return math.NaN()
}

// sameNumber is Object.is on numbers: NaN is NaN, 0 is not -0.
func sameNumber(a, b float64) bool {
	if math.IsNaN(a) || math.IsNaN(b) {
		return math.IsNaN(a) && math.IsNaN(b)
	}
	return a == b && math.Signbit(a) == math.Signbit(b)
}

// SortValueChanged reports whether an update moves a download under key (only then does the
// order change).
func SortValueChanged(prev, next *downexv1.Download, key SortKey) bool {
	if key == SortName {
		return categories.DisplayName(prev) != categories.DisplayName(next)
	}
	return !sameNumber(numericValue(prev, key), numericValue(next, key))
}

// Collator compares two names for the name column.
type Collator interface {
	Compare(a, b string) int
}

// NewCollator is today's name order for a language tag: Intl.Collator(tag, {numeric: true,
// sensitivity: "base"}): digits as numbers ("file2" before "file10"), case and accents
// ignored. An unknown tag reads as the root order.
func NewCollator(tag string) Collator {
	t, err := language.Parse(tag)
	if err != nil {
		t = language.Und
	}
	return &pooledCollator{pool: sync.Pool{New: func() any { return collate.New(t, collate.Numeric, collate.Loose) }}}
}

// pooledCollator keeps collators (not safe for concurrent use) in a pool.
type pooledCollator struct{ pool sync.Pool }

func (c *pooledCollator) Compare(a, b string) int {
	if a == b {
		return 0
	}
	coll := c.pool.Get().(*collate.Collator)
	defer c.pool.Put(coll)
	return coll.CompareString(a, b)
}

var rootCollator = NewCollator("und")

func collatorOr(c Collator) Collator {
	if c == nil {
		return rootCollator
	}
	return c
}

// Comparator is a total order of downloads.
type Comparator func(a, b *downexv1.Download) int

func signOf(dir SortDir) int {
	if dir == Asc {
		return 1
	}
	return -1
}

func cmpID(a, b *downexv1.Download) int {
	switch {
	case a.GetId() < b.GetId():
		return -1
	case a.GetId() > b.GetId():
		return 1
	}
	return 0
}

// cmpNumbers orders va and vb, NaN (unknown) last in both directions, ties by id; sign is the
// direction.
func cmpNumbers(va, vb float64, sign int, a, b *downexv1.Download) int {
	na, nb := math.IsNaN(va), math.IsNaN(vb)
	if na != nb {
		if na {
			return 1
		}
		return -1
	}
	c := 0
	if !na && va != vb {
		if va < vb {
			c = -1
		} else {
			c = 1
		}
	}
	if c == 0 {
		c = cmpID(a, b)
	}
	return sign * c
}

// ComparatorFor is a total order: ties break on id in the sort direction, so the order is stable
// across refreshes, and unknown values (NaN) sort last in both directions. coll orders names
// (nil: the root order).
func ComparatorFor(sort SortSpec, coll Collator) Comparator {
	sign := signOf(sort.Dir)
	if sort.Key == SortName {
		coll = collatorOr(coll)
		return func(a, b *downexv1.Download) int {
			c := coll.Compare(categories.DisplayName(a), categories.DisplayName(b))
			if c == 0 {
				c = cmpID(a, b)
			}
			return sign * c
		}
	}
	key := sort.Key
	return func(a, b *downexv1.Download) int {
		return cmpNumbers(numericValue(a, key), numericValue(b, key), sign, a, b)
	}
}

// SortDownloads sorts a copy, each download's key computed once.
func SortDownloads(list []*downexv1.Download, sort SortSpec, coll Collator) []*downexv1.Download {
	sign := signOf(sort.Dir)
	type keyed struct {
		d *downexv1.Download
		k string
		v float64
	}
	ks := make([]keyed, len(list))
	for i, d := range list {
		ks[i].d = d
		if sort.Key == SortName {
			ks[i].k = categories.DisplayName(d)
		} else {
			ks[i].v = numericValue(d, sort.Key)
		}
	}
	if sort.Key == SortName {
		coll = collatorOr(coll)
		slices.SortStableFunc(ks, func(a, b keyed) int {
			c := coll.Compare(a.k, b.k)
			if c == 0 {
				c = cmpID(a.d, b.d)
			}
			return sign * c
		})
	} else {
		slices.SortStableFunc(ks, func(a, b keyed) int { return cmpNumbers(a.v, b.v, sign, a.d, b.d) })
	}
	out := make([]*downexv1.Download, len(ks))
	for i, k := range ks {
		out[i] = k.d
	}
	return out
}

// lowerBound is the first index whose item sorts at or after d.
func lowerBound(list []*downexv1.Download, d *downexv1.Download, cmp Comparator) int {
	lo, hi := 0, len(list)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if cmp(list[mid], d) < 0 {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

// SortedView is the full list in sort order, kept up to date incrementally. The owner reports
// every change with Touch (before replacing or removing the message it holds), then reads
// Update. When no changed download's sort value changed, the order is reused and only the
// messages are swapped; otherwise just the changed ones move. Many changes, a new sort or
// Invalidate fall back to a full sort. It is not safe for concurrent use.
type SortedView struct {
	// Collator orders names (nil: the root order); set it before the first Update.
	Collator Collator
	// FullSorts counts the full sorts performed (tests and diagnostics).
	FullSorts int

	spec    SortSpec
	hasSpec bool
	cmp     Comparator
	list    []*downexv1.Download
	// dirty is id → the message list holds for it (nil: not in list), in the order touched.
	dirty    map[int64]*downexv1.Download
	dirtyIDs []int64
	fresh    bool
}

// Touch reports that the download id changes; prev is the message the owner held for it (nil
// for a new one).
func (v *SortedView) Touch(id int64, prev *downexv1.Download) {
	if !v.fresh {
		return
	}
	if v.dirty == nil {
		v.dirty = map[int64]*downexv1.Download{}
	}
	if _, ok := v.dirty[id]; !ok {
		v.dirty[id] = prev
		v.dirtyIDs = append(v.dirtyIDs, id)
	}
}

// Invalidate makes the next Update sort in full.
func (v *SortedView) Invalidate() {
	v.fresh = false
	v.clearDirty()
}

func (v *SortedView) clearDirty() {
	clear(v.dirty)
	v.dirtyIDs = v.dirtyIDs[:0]
}

// Update is the sorted list. changed is false when nothing changed: the list is then the one
// the previous Update returned. A list that was returned is never modified afterwards.
func (v *SortedView) Update(items map[int64]*downexv1.Download, spec SortSpec) (list []*downexv1.Download, changed bool) {
	sameSpec := v.hasSpec && v.spec == spec
	if sameSpec && v.fresh && len(v.dirtyIDs) == 0 {
		return v.list, false
	}
	if !sameSpec || !v.fresh || float64(len(v.dirtyIDs)) > math.Max(64, float64(len(v.list))/8) {
		return v.full(items, spec), true
	}
	out := slices.Clone(v.list)
	var moved []*downexv1.Download
	for _, id := range v.dirtyIDs {
		before := v.dirty[id]
		now := items[id]
		if before != nil {
			i := lowerBound(out, before, v.cmp)
			if i >= len(out) || out[i] != before {
				return v.full(items, spec), true // out of step: rebuild
			}
			if now != nil && !SortValueChanged(before, now, spec.Key) {
				out[i] = now
				continue
			}
			out = slices.Delete(out, i, i+1)
		}
		if now != nil {
			moved = append(moved, now)
		}
	}
	for _, d := range moved {
		i := lowerBound(out, d, v.cmp)
		out = slices.Insert(out, i, d)
	}
	v.clearDirty()
	v.list = out
	return out, true
}

func (v *SortedView) full(items map[int64]*downexv1.Download, spec SortSpec) []*downexv1.Download {
	v.FullSorts++
	v.spec, v.hasSpec = spec, true
	v.cmp = ComparatorFor(spec, v.Collator)
	all := make([]*downexv1.Download, 0, len(items))
	for _, d := range items {
		all = append(all, d)
	}
	v.list = SortDownloads(all, spec, v.Collator)
	v.clearDirty()
	v.fresh = true
	return v.list
}

// ToggleSort is a click on a column: the same column flips its direction; text columns start
// ascending, quantities and dates with the largest first.
func ToggleSort(current SortSpec, key SortKey) SortSpec {
	if current.Key == key {
		if current.Dir == Asc {
			return SortSpec{key, Desc}
		}
		return SortSpec{key, Asc}
	}
	if key == SortName || key == SortStatus {
		return SortSpec{key, Asc}
	}
	return SortSpec{key, Desc}
}

// Counts are the sidebar's faceted counts.
type Counts struct {
	// Categories are the downloads per category filter: All, every configured category and
	// OtherFilter.
	Categories map[categories.Filter]int
	// Statuses are the downloads per status filter, AnyStatus included.
	Statuses map[categories.Status]int
}

// CountFacets counts faceted: each category count applies the selected status filter, and each
// status count applies the selected category, so a click never leads to an unexpected number
// of rows. The queue filter (0 = every queue) applies to both; the search is not applied.
func CountFacets(list []*downexv1.Download, category categories.Filter, status categories.Status, ix *categories.Index, queue int64) Counts {
	cats := map[categories.Filter]int{categories.All: 0, categories.OtherFilter: 0}
	for _, e := range ix.Entries() {
		cats[e.Key] = 0
	}
	statuses := map[categories.Status]int{categories.AnyStatus: 0}
	for _, s := range categories.StatusFilters() {
		statuses[s] = 0
	}
	for _, d := range list {
		if queue != 0 && QueueOf(d) != queue {
			continue
		}
		cat := ix.BucketOf(d)
		if categories.MatchesStatus(d.GetState(), status) {
			cats[categories.All]++
			cats[cat]++
		}
		if category == categories.All || category == cat {
			statuses[categories.AnyStatus]++
			if bucket, ok := categories.StatusBucket(d.GetState()); ok {
				statuses[bucket]++
			}
		}
	}
	return Counts{Categories: cats, Statuses: statuses}
}

// CountQueues is, per queue, its downloads that still have something to do (all but completed
// ones).
func CountQueues(list []*downexv1.Download) map[int64]int {
	out := map[int64]int{}
	for _, d := range list {
		if d.GetState() == downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED {
			continue
		}
		out[QueueOf(d)]++
	}
	return out
}
