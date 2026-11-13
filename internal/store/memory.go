package store

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"downex/internal/model"
)

// Memory is an in-memory Store for tests and ephemeral use. It mirrors the
// SQLite store's observable behaviour: IDs increase and are never reused,
// timestamps read back in UTC without a monotonic reading, segments come back
// ordered by ID, and every read and write deep-copies so callers can never
// alias internal state.
type Memory struct {
	mu          sync.RWMutex
	closed      bool
	lastID      model.DownloadID
	downloads   map[model.DownloadID]*memEntry
	lastQueueID model.QueueID
	queues      map[model.QueueID]*model.Queue // without DownloadIDs (see memEntry.pos)
}

// memEntry is one stored download with its latest checkpoint.
type memEntry struct {
	d    model.Download  // d.QueueID is its queue
	segs []model.Segment // sorted by ID, never nil
	pos  int64           // order within its queue (ascending, ties by ID)
}

var (
	_ Store      = (*Memory)(nil)
	_ QueueStore = (*Memory)(nil)
)

// NewMemory returns an in-memory store without downloads, holding only the main queue
// (running), as a new database does.
func NewMemory() *Memory {
	return &Memory{
		downloads:   make(map[model.DownloadID]*memEntry),
		lastQueueID: model.MainQueueID,
		queues:      map[model.QueueID]*model.Queue{model.MainQueueID: {ID: model.MainQueueID, Name: MainQueueName, Running: true}},
	}
}

// CreateDownload assigns d.ID and stores a copy of d at the end of queue d.QueueID
// (0 = the main queue, then stored in d.QueueID). Zero CreatedAt and UpdatedAt are set
// to the current time; d is only modified on success.
func (m *Memory) CreateDownload(ctx context.Context, d *model.Download) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateDownload(d); err != nil {
		return fmt.Errorf("store: create download: %w", err)
	}
	now := canonicalTime(time.Now())
	rec := memoryRecord(*d)
	stampNew(&rec, now)
	rec.QueueID = createQueueID(rec.QueueID)

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	if _, ok := m.queues[rec.QueueID]; !ok {
		return fmt.Errorf("store: create download: queue %d: %w", rec.QueueID, ErrQueueNotFound)
	}
	var last int64
	for _, e := range m.downloads {
		if e.d.QueueID == rec.QueueID {
			last = max(last, e.pos)
		}
	}
	m.lastID++
	rec.ID = m.lastID
	m.downloads[rec.ID] = &memEntry{d: rec, segs: []model.Segment{}, pos: last + 1}

	d.ID = rec.ID
	d.QueueID = rec.QueueID
	stampNew(d, now)
	return nil
}

// UpdateDownload replaces the stored record with a copy of d.
func (m *Memory) UpdateDownload(ctx context.Context, d *model.Download) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateDownload(d); err != nil {
		return fmt.Errorf("store: update download: %w", err)
	}
	rec := memoryRecord(*d)

	m.mu.Lock()
	defer m.mu.Unlock()
	e, err := m.entry(rec.ID)
	if err != nil {
		return fmt.Errorf("store: update download: %w", err)
	}
	rec.QueueID = e.d.QueueID // changed only by the queue operations
	e.d = rec
	return nil
}

// GetDownload returns a copy of the stored record.
func (m *Memory) GetDownload(ctx context.Context, id model.DownloadID) (*model.Download, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, err := m.entry(id)
	if err != nil {
		return nil, fmt.Errorf("store: get download: %w", err)
	}
	d := cloneDownload(e.d)
	return &d, nil
}

// ListDownloads returns copies of all records ordered by ID. The result is
// never nil.
func (m *Memory) ListDownloads(ctx context.Context) ([]model.Download, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return nil, ErrClosed
	}
	out := make([]model.Download, 0, len(m.downloads))
	for _, e := range m.downloads {
		out = append(out, cloneDownload(e.d))
	}
	slices.SortFunc(out, func(a, b model.Download) int { return cmp.Compare(a.ID, b.ID) })
	return out, nil
}

// DeleteDownload removes a record and its segments.
func (m *Memory) DeleteDownload(ctx context.Context, id model.DownloadID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.entry(id); err != nil {
		return fmt.Errorf("store: delete download: %w", err)
	}
	delete(m.downloads, id)
	return nil
}

// SaveCheckpoint replaces the record and all its segments in one step.
func (m *Memory) SaveCheckpoint(ctx context.Context, d *model.Download, segs []model.Segment) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateDownload(d); err != nil {
		return fmt.Errorf("store: save checkpoint: %w", err)
	}
	if err := validateSegments(segs); err != nil {
		return fmt.Errorf("store: save checkpoint for download %d: %w", d.ID, err)
	}
	rec := memoryRecord(*d)
	sorted := sortedSegments(segs)

	m.mu.Lock()
	defer m.mu.Unlock()
	e, err := m.entry(rec.ID)
	if err != nil {
		return fmt.Errorf("store: save checkpoint: %w", err)
	}
	rec.QueueID = e.d.QueueID // changed only by the queue operations
	e.d, e.segs = rec, sorted
	return nil
}

// LoadSegments returns a copy of the latest checkpoint ordered by segment ID.
// The result is never nil.
func (m *Memory) LoadSegments(ctx context.Context, id model.DownloadID) ([]model.Segment, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, err := m.entry(id)
	if err != nil {
		return nil, fmt.Errorf("store: load segments: %w", err)
	}
	return slices.Clone(e.segs), nil
}

// Close releases the stored data. Later calls fail with ErrClosed; closing
// twice is a no-op.
func (m *Memory) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	m.downloads = nil
	m.queues = nil
	return nil
}

// entry returns the live entry for id. The caller must hold m.mu.
func (m *Memory) entry(id model.DownloadID) (*memEntry, error) {
	if m.closed {
		return nil, ErrClosed
	}
	e, ok := m.downloads[id]
	if !ok {
		return nil, fmt.Errorf("download %d: %w", id, ErrNotFound)
	}
	return e, nil
}

// memoryRecord deep-copies d and canonicalises its timestamps the way the
// SQLite encoding does, so both stores read back identical values.
func memoryRecord(d model.Download) model.Download {
	d = cloneDownload(d)
	d.CreatedAt = canonicalTime(d.CreatedAt)
	d.UpdatedAt = canonicalTime(d.UpdatedAt)
	d.CompletedAt = canonicalTime(d.CompletedAt)
	d.StartedAt = canonicalTime(d.StartedAt)
	return d
}

// ListQueues returns copies of every queue ordered by ID, each with its downloads in
// queue order. The result is never nil.
func (m *Memory) ListQueues(ctx context.Context) ([]model.Queue, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return nil, ErrClosed
	}
	out := make([]model.Queue, 0, len(m.queues))
	for _, q := range m.queues {
		c := q.Clone()
		c.DownloadIDs = m.orderLocked(q.ID)
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b model.Queue) int { return cmp.Compare(a.ID, b.ID) })
	return out, nil
}

// CreateQueue assigns q.ID and stores a copy of q without downloads.
func (m *Memory) CreateQueue(ctx context.Context, q *model.Queue) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateQueue(q); err != nil {
		return fmt.Errorf("store: create queue: %w", err)
	}
	rec := memoryQueue(*q)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	m.lastQueueID++
	rec.ID = m.lastQueueID
	m.queues[rec.ID] = &rec
	q.ID = rec.ID
	return nil
}

// UpdateQueue replaces the stored queue q.ID with a copy of q (its downloads are kept).
func (m *Memory) UpdateQueue(ctx context.Context, q *model.Queue) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateQueue(q); err != nil {
		return fmt.Errorf("store: update queue: %w", err)
	}
	rec := memoryQueue(*q)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	if _, ok := m.queues[rec.ID]; !ok {
		return fmt.Errorf("store: update queue: queue %d: %w", rec.ID, ErrQueueNotFound)
	}
	m.queues[rec.ID] = &rec
	return nil
}

// DeleteQueue removes queue id and appends its downloads, in their order, to the main
// queue.
func (m *Memory) DeleteQueue(ctx context.Context, id model.QueueID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if id == model.MainQueueID {
		return fmt.Errorf("store: delete queue: %w: the main queue cannot be deleted", ErrInvalid)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	if _, ok := m.queues[id]; !ok {
		return fmt.Errorf("store: delete queue: queue %d: %w", id, ErrQueueNotFound)
	}
	moved := m.orderLocked(id)
	m.placeLocked(model.MainQueueID, model.PlaceDownloads(m.orderLocked(model.MainQueueID), moved, -1))
	delete(m.queues, id)
	return nil
}

// MoveDownloads moves ids into queue to at position and renumbers that queue's order.
func (m *Memory) MoveDownloads(ctx context.Context, ids []model.DownloadID, to model.QueueID, position int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	moved := model.UniqueDownloadIDs(ids)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	if _, ok := m.queues[to]; !ok {
		return fmt.Errorf("store: move downloads: queue %d: %w", to, ErrQueueNotFound)
	}
	for _, id := range moved {
		if _, ok := m.downloads[id]; !ok {
			return fmt.Errorf("store: move downloads: download %d: %w", id, ErrNotFound)
		}
	}
	m.placeLocked(to, model.PlaceDownloads(m.orderLocked(to), moved, position))
	return nil
}

// orderLocked returns the downloads of queue id in queue order. The caller holds m.mu.
func (m *Memory) orderLocked(id model.QueueID) []model.DownloadID {
	var es []*memEntry
	for _, e := range m.downloads {
		if e.d.QueueID == id {
			es = append(es, e)
		}
	}
	slices.SortFunc(es, func(a, b *memEntry) int {
		return cmp.Or(cmp.Compare(a.pos, b.pos), cmp.Compare(a.d.ID, b.d.ID))
	})
	out := make([]model.DownloadID, len(es))
	for i, e := range es {
		out[i] = e.d.ID
	}
	return out
}

// placeLocked makes order the downloads of queue id, in that order. The caller holds
// m.mu for writing.
func (m *Memory) placeLocked(id model.QueueID, order []model.DownloadID) {
	for i, d := range order {
		e := m.downloads[d]
		e.d.QueueID, e.pos = id, int64(i+1)
	}
}

// memoryQueue copies q the way the SQLite store reads it back: without downloads and
// with a canonical schedule mark.
func memoryQueue(q model.Queue) model.Queue {
	q.DownloadIDs = nil
	q.ScheduleMark = canonicalTime(q.ScheduleMark)
	return q
}
