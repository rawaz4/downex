// Package engine downloads one HTTP(S) resource over one or more byte-range
// connections, as specified in docs/ENGINE-SPEC.md §2–§11.
//
// # Concurrency model
//
// Run drives three kinds of goroutines for one download:
//
//   - The coordinator (an actor running on the caller's goroutine) owns the segment
//     list and every scheduling decision: probing, ramp-up, splitting, takeover,
//     stall handling, checkpoints and progress events.
//   - Workers each own one transport.Fetcher slot and at most one segment at a time.
//     A worker reserves bytes of its segment under the segment's mutex before it
//     hands them to the writer, re-checking the segment's current end on every
//     slice, so a byte range is never written by two workers.
//   - One writer performs every WriteAt into "<name>.dxpart", flushes the file and
//     reports which offsets are durable. Only flushed offsets are ever committed to
//     the store.
//
// Each worker reports what its connection does (connecting, requesting, receiving,
// retrying, idle) through a small lock-protected view that net/http/httptrace
// callbacks advance; the coordinator adds ranges and per-connection speeds and lists
// the connection slots in Progress.Workers. Engine.SetMaxConnections reaches the
// coordinator of a running download through a wake channel and is applied between
// events like every other scheduling decision.
package engine
