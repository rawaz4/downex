// Package notify decides which desktop notifications the daemon shows
// (docs/DESKTOP-SPEC.md §3–§4) and what they say. It is pure policy: it is told what
// happened (a download completed, failed, or awaits a confirmation the window could
// not ask for) and returns notifications; it also formats the tray tooltip. Showing
// them is the tray's job (internal/tray).
//
//   - Completed and failed downloads produce "Download complete" / "Download failed"
//     (Settings.notify_on_complete / notify_on_failure), with the file name (bidi and
//     control characters stripped) and, for failures, a short error that never
//     repeats a URL's path or query.
//   - A download awaiting confirmation that the window could not be opened for
//     produces "Confirm download" with its name.
//   - A failure because the link expired reads "Link expired" with a hint to give it a
//     new link; a download that continues with a new link found for it reads "Download
//     continues" (notify_on_complete) (docs/DESKTOP-SPEC.md §10).
//   - Bursts are coalesced: more than three notifications within five seconds become
//     one "N downloads completed" message.
//   - The tooltip reads "Downex — N active, X/s".
//
// Time is always passed in by the caller, so the policy is deterministic under test.
package notify

import (
	"time"

	downexv1 "downex/internal/rpc/downex/v1"
)

// Coalescing bounds (docs/DESKTOP-SPEC.md §4).
const (
	// BurstWindow is the span within which notifications count as one burst.
	BurstWindow = 5 * time.Second
	// BurstLimit is how many notifications of one burst are shown individually.
	BurstLimit = 3
)

// Kind is what a notification reports.
type Kind uint8

const (
	// KindCompleted reports finished downloads.
	KindCompleted Kind = iota + 1
	// KindFailed reports failed downloads (a coalesced message with any failure, too).
	KindFailed
	// KindConfirm reports downloads waiting for the user's confirmation.
	KindConfirm
	// KindRenewed reports a download whose link expired that continues with a new link
	// (docs/DESKTOP-SPEC.md §10). It is never coalesced.
	KindRenewed
)

// Notification is one message for the user.
type Notification struct {
	Kind  Kind
	Title string
	Text  string // may hold two lines (file name, then the error)
	// ID is the download reported, 0 for a coalesced message.
	ID int64
	// Count is how many downloads the message stands for: 1, or more when coalesced.
	Count int
	// Error marks a message that reports a failure.
	Error bool
}

// Policy selects the notifications that are produced.
type Policy struct {
	OnComplete bool // Settings.notify_on_complete
	OnFailure  bool // Settings.notify_on_failure
}

// PolicyFromSettings returns the policy of s.
func PolicyFromSettings(s *downexv1.Settings) Policy {
	return Policy{OnComplete: s.GetNotifyOnComplete(), OnFailure: s.GetNotifyOnFailure()}
}

// Summary is the aggregate state shown in the tray tooltip.
type Summary struct {
	Active int     // downloads probing or downloading
	Speed  float64 // their total speed, bytes/s
	// PoliteSlowed: Polite mode holds the downloads back (docs/POLITE-SPEC.md §8).
	PoliteSlowed bool
}
