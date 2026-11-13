// Package store persists downloads and their segment checkpoints.
// The store is the source of truth for resume (docs/ENGINE-SPEC.md §6–7).
package store

import (
	"context"
	"errors"

	"downex/internal/model"
)

// ErrNotFound is returned when a download does not exist.
var ErrNotFound = errors.New("download not found")

// MainQueueName is the name the main queue gets in a new store (and in a database
// migrated from a build without queues).
const MainQueueName = "Main queue"

// Store is implemented by the SQLite store and an in-memory store for tests.
// All methods are safe for concurrent use.
type Store interface {
	// CreateDownload assigns d.ID and persists d. Stores that implement QueueStore
	// append it to the end of queue d.QueueID (0 = the main queue, stored in d.QueueID);
	// a queue that does not exist fails with ErrQueueNotFound.
	CreateDownload(ctx context.Context, d *model.Download) error
	// UpdateDownload replaces the persisted record with d (matched by d.ID), except
	// its queue membership (d.QueueID is ignored; see QueueStore.MoveDownloads).
	UpdateDownload(ctx context.Context, d *model.Download) error
	GetDownload(ctx context.Context, id model.DownloadID) (*model.Download, error)
	ListDownloads(ctx context.Context) ([]model.Download, error)
	DeleteDownload(ctx context.Context, id model.DownloadID) error

	// SaveCheckpoint atomically replaces all segments of a download and updates its
	// record in the same transaction. Callers only pass durable (flushed) offsets.
	SaveCheckpoint(ctx context.Context, d *model.Download, segs []model.Segment) error
	LoadSegments(ctx context.Context, id model.DownloadID) ([]model.Segment, error)

	Close() error
}

// QueueStore persists download queues and the queue order of their downloads
// (docs/M6-SPEC.md §1). Both Store implementations implement it; the main queue
// (model.MainQueueID) always exists and cannot be deleted. Queue IDs increase and are
// never reused. Every download belongs to exactly one queue. All methods are safe for
// concurrent use.
type QueueStore interface {
	// ListQueues returns every queue ordered by ID, each with its downloads in queue
	// order (Queue.DownloadIDs), read from one snapshot. The result is never nil.
	ListQueues(ctx context.Context) ([]model.Queue, error)
	// CreateQueue assigns q.ID and persists q without downloads (q.DownloadIDs is
	// ignored).
	CreateQueue(ctx context.Context, q *model.Queue) error
	// UpdateQueue replaces the persisted queue q.ID with q, except its downloads
	// (q.DownloadIDs is ignored). A missing queue is ErrQueueNotFound.
	UpdateQueue(ctx context.Context, q *model.Queue) error
	// DeleteQueue removes queue id and moves its downloads, in their order, to the end
	// of the main queue, in one transaction. The main queue is ErrInvalid, a missing
	// queue ErrQueueNotFound.
	DeleteQueue(ctx context.Context, id model.QueueID) error
	// MoveDownloads moves the downloads ids (duplicates count once), in the given
	// order, into queue to at position: the 0-based index among the downloads the
	// queue keeps (the moved ones taken out first); a negative position or one past the
	// end appends them. A missing download is ErrNotFound and a missing queue
	// ErrQueueNotFound; nothing changes then.
	MoveDownloads(ctx context.Context, ids []model.DownloadID, to model.QueueID, position int) error
}
