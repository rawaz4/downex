package store

import "errors"

// Errors returned by both Store implementations in addition to ErrNotFound.
// Match them with errors.Is; they are always wrapped with context.
var (
	// ErrInvalid reports an argument the store cannot persist faithfully: a nil
	// download, duplicate segment IDs in one checkpoint, or a timestamp outside
	// the storable range (see validate.go).
	ErrInvalid = errors.New("invalid store argument")

	// ErrClosed reports an operation on a store after Close.
	ErrClosed = errors.New("store closed")

	// ErrUnsupportedSchema reports a database whose schema version is newer than
	// the migrations this build knows. Migrations are forward-only, so such a
	// database is never opened (and never modified) by an older build.
	ErrUnsupportedSchema = errors.New("unsupported database schema version")

	// ErrQueueNotFound reports a queue that does not exist (QueueStore, and
	// CreateDownload into a queue that does not exist).
	ErrQueueNotFound = errors.New("queue not found")
)
