package engine

import "downex/internal/model"

// SegmentView is one segment in a progress event. Ranges are inclusive; End is -1
// for the open-ended segment of a single-mode download of unknown size.
type SegmentView struct {
	Start    int64
	End      int64
	Received int64 // bytes received from Start, including bytes resumed from disk
}

// Progress is a snapshot of a running download. Run emits it at most 4 times per
// second, plus once when the download stops. Its slices are fresh for every event:
// the receiver may keep them.
type Progress struct {
	ID          model.DownloadID
	Mode        model.Mode
	Size        int64   // -1 when unknown
	Received    int64   // bytes received so far, including bytes resumed from disk
	SpeedBps    float64 // the shown speed in bytes per second (docs/ENGINE-SPEC.md §9); 0 once stopped
	Connections int     // live connections
	Segments    []SegmentView
	// Workers has one entry per connection slot, ordered by Slot (1-based; a slot keeps
	// its number while its connection lives and is reused by later connections).
	// Live connections report what they do; a slot whose connection finished its range
	// stays listed as WorkerDone, one whose connection failed the download as
	// WorkerError, and a slot whose connection was stopped (a lowered connection limit,
	// a stall, the server asking to slow down) is left out. Start/End is the current
	// range (-1/-1 when none; End -1 for an open-ended range), Received the bytes of
	// that range from Start (including resumed bytes), SpeedBps the connection's own
	// shown speed (§9). Fragment is always -1. Detail is short text without URLs or
	// header values: while retrying the reason and the wait left ("HTTP 503, retrying
	// in 4 s", RetryAt the time of the next attempt), the reason after a failure, the
	// step while connecting.
	Workers []model.WorkerView
}
