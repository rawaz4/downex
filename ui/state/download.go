package state

import (
	"context"
	"strconv"
	"strings"
	"sync"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	downexv1 "downex/internal/rpc/downex/v1"
)

// Download is a downex.v1.Download as the stores hold it.
type Download = downexv1.Download

// The download states, named as the window names them.
const (
	StateQueued      = downexv1.DownloadState_DOWNLOAD_STATE_QUEUED
	StateProbing     = downexv1.DownloadState_DOWNLOAD_STATE_PROBING
	StateDownloading = downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING
	StatePaused      = downexv1.DownloadState_DOWNLOAD_STATE_PAUSED
	StateCompleted   = downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED
	StateFailed      = downexv1.DownloadState_DOWNLOAD_STATE_FAILED
	StateCanceled    = downexv1.DownloadState_DOWNLOAD_STATE_CANCELED
	StateAwaiting    = downexv1.DownloadState_DOWNLOAD_STATE_AWAITING_CONFIRMATION
)

// MainQueueID is the main queue's id (api.ts MAIN_QUEUE_ID).
const MainQueueID int64 = 1

// IsActive reports a state that holds a scheduler slot or waits for one (api.ts isActive).
func IsActive(s downexv1.DownloadState) bool {
	return s == StateDownloading || s == StateProbing || s == StateQueued
}

// IsResumable reports a state Resume accepts (api.ts isResumable: failed downloads can be
// resumed).
func IsResumable(s downexv1.DownloadState) bool {
	return s == StatePaused || s == StateFailed || s == StateCanceled
}

// tsNanos is a timestamp in nanoseconds since the epoch; 0 for none.
func tsNanos(ts *timestamppb.Timestamp) int64 {
	if ts == nil {
		return 0
	}
	return ts.GetSeconds()*1_000_000_000 + int64(ts.GetNanos())
}

// reportOrder decides which of two reports of a download is the newer one: answers race
// with events, and the newer report wins. downexd's updated_at decides first (an answer
// without one is never older). Reports with the same updated_at are ordered by when the
// window learned them, since downexd changes some fields without moving updated_at (the
// queue_position of every download a MoveDownloads or an admission passes): an event counts
// from its arrival, an answer from when its call was sent (downexd read it after that), and
// a stream's snapshot from the stream's previous batch (the bridge subscribes again only
// after it, and downexd takes the snapshot after the subscription). A report never brings
// back a download whose removal the window learned after the report's call was sent.
//
// A store holds one under its own lock.
type reportOrder struct {
	clock  uint64           // counts the batches received, the calls sent and the removals
	stream uint64           // when the last batch arrived
	held   map[int64]uint64 // when each held report was learned
	// gone holds when downloads were removed, while a call is out (its answer may still
	// report them); out counts those calls.
	gone map[int64]uint64
	out  int
}

// sent is the order of the answer to a call sent now; landed must follow when it is back
// (or failed).
func (o *reportOrder) sent() uint64 {
	o.clock++
	o.out++
	return o.clock
}

// landed ends a call of sent.
func (o *reportOrder) landed() {
	if o.out--; o.out == 0 {
		o.gone = nil
	}
}

// arrived is the order of a batch arriving now (events), and that of its snapshot events.
func (o *reportOrder) arrived() (events, snapshot uint64) {
	snapshot = o.stream
	o.clock++
	o.stream = o.clock
	return o.clock, snapshot
}

// newer reports whether d, learned at at, replaces prev (nil when none is held).
func (o *reportOrder) newer(d, prev *Download, at uint64) bool {
	id := d.GetId()
	if g, ok := o.gone[id]; ok && at < g {
		return false
	}
	if prev == nil {
		return true
	}
	t := tsNanos(d.GetUpdatedAt())
	if t == 0 {
		return true
	}
	if pt := tsNanos(prev.GetUpdatedAt()); pt != t {
		return t > pt
	}
	return at >= o.held[id]
}

// hold records that the report of id learned at at is the one held.
func (o *reportOrder) hold(id int64, at uint64) {
	if o.held == nil {
		o.held = map[int64]uint64{}
	}
	o.held[id] = at
}

// remove records that id was removed, as learned at at (0: now).
func (o *reportOrder) remove(id int64, at uint64) {
	if at == 0 {
		o.clock++
		at = o.clock
	}
	delete(o.held, id)
	if o.out > 0 {
		if o.gone == nil {
			o.gone = map[int64]uint64{}
		}
		o.gone[id] = at
	}
}

// MapLimit calls fn for every item, at most limit at a time, and returns the results in the
// items' order (downloads.svelte.ts mapLimit).
func MapLimit[T, R any](items []T, limit int, fn func(T) R) []R {
	out := make([]R, len(items))
	if len(items) == 0 {
		return out
	}
	workers := min(max(1, limit), len(items))
	var (
		mu   sync.Mutex
		next int
		wg   sync.WaitGroup
	)
	for range workers {
		wg.Go(func() {
			for {
				mu.Lock()
				i := next
				next++
				mu.Unlock()
				if i >= len(items) {
					return
				}
				out[i] = fn(items[i])
			}
		})
	}
	wg.Wait()
	return out
}

// callWith runs a call in a context of the store's own (a timer's check, a read after an
// event): call gives it.
func callWith[T any](call CallContext, fn func(ctx context.Context) (T, error)) (T, error) {
	ctx, cancel := callOr(call)()
	defer cancel()
	return fn(ctx)
}

// cloneDownload is a copy of d to change (messages handed out are never modified).
func cloneDownload(d *Download) *Download { return proto.Clone(d).(*Download) }

// NumberedName is "name (n).ext" (api.ts numberedName): what keep both names a second copy.
func NumberedName(name string, n int) string {
	if n == 0 {
		return name
	}
	dot := strings.LastIndex(name, ".")
	if dot <= 0 {
		return name + " (" + strconv.Itoa(n) + ")"
	}
	return name[:dot] + " (" + strconv.Itoa(n) + ")" + name[dot:]
}

// DefaultSettings are the daemon's defaults (api.ts DEFAULT_SETTINGS).
func DefaultSettings() *downexv1.Settings {
	return &downexv1.Settings{
		MaxConcurrentDownloads:  3,
		DefaultMaxConnections:   32,
		Categories:              DefaultCategories(),
		UseCategoryFolders:      true,
		ConfirmBrowserDownloads: true,
		NotifyOnComplete:        true,
		NotifyOnFailure:         true,
		ShowTrayIcon:            true,
		ShowProgressWindow:      true,
		ShowCompleteDialog:      true,
		Proxy:                   &downexv1.ProxySettings{Mode: downexv1.ProxyMode_PROXY_MODE_SYSTEM},
		MarkDownloadedFiles:     true,
		AvScanArgs:              "[File]",
		AutoCheckUpdates:        true,
		SiteMedia:               true,
		ToolsAutoUpdate:         true,
		ToolsChannel:            "nightly",
	}
}
