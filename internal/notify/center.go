package notify

import (
	"time"

	downexv1 "downex/internal/rpc/downex/v1"
)

// Center turns the daemon's one-off occurrences (a download completed, failed, or
// awaits a confirmation the window could not ask for) into notifications, subject to
// the policy and to coalescing. It is not safe for concurrent use.
//
// Call Flush at Deadline to release coalesced messages.
type Center struct {
	policy   Policy
	finished *coalescer // completed and failed
	confirms *coalescer // awaiting confirmation
}

// NewCenter returns a center applying p.
func NewCenter(p Policy) *Center {
	return &Center{policy: p, finished: newCoalescer(finishedSummary), confirms: newCoalescer(confirmSummary)}
}

// SetPolicy replaces the policy; it applies to later occurrences.
func (c *Center) SetPolicy(p Policy) { c.policy = p }

// Policy returns the current policy.
func (c *Center) Policy() Policy { return c.policy }

// Completed reports that d has just completed: "Download complete" with its name
// (Policy.OnComplete).
func (c *Center) Completed(now time.Time, d *downexv1.Download) []Notification {
	if !c.policy.OnComplete {
		return nil
	}
	return c.finished.add(now, Notification{
		Kind: KindCompleted, Title: titleCompleted, Text: DisplayName(d), ID: d.GetId(), Count: 1,
	})
}

// Failed reports that d has just failed: "Download failed" with its name and a short
// error (Policy.OnFailure).
func (c *Center) Failed(now time.Time, d *downexv1.Download) []Notification {
	if !c.policy.OnFailure {
		return nil
	}
	if d.GetErrorCode() == "link_expired" {
		text := DisplayName(d) + "\n" + textLinkExpired
		if d.GetReceived() > 0 {
			text += " " + textKept
		}
		return c.finished.add(now, Notification{Kind: KindFailed, Title: titleLinkExpired, ID: d.GetId(), Count: 1,
			Error: true, Text: text})
	}
	return c.finished.add(now, Notification{
		Kind: KindFailed, Title: titleFailed, ID: d.GetId(), Count: 1, Error: true,
		Text: DisplayName(d) + "\n" + ShortError(d.GetErrorCode(), d.GetError()),
	})
}

// Renewed reports that d, whose link expired, continues with a new link that proved to
// serve the same file: "Download continues" (Policy.OnComplete). It is shown at once.
func (c *Center) Renewed(now time.Time, d *downexv1.Download) []Notification {
	if !c.policy.OnComplete {
		return nil
	}
	return []Notification{{Kind: KindRenewed, Title: titleRenewed, Text: DisplayName(d) + "\n" + textRenewed,
		ID: d.GetId(), Count: 1}}
}

// Awaiting reports that d awaits confirmation and the window could not be opened to
// ask: "Confirm download" with its name. There is no setting to turn this off: the
// download waits for the user.
func (c *Center) Awaiting(now time.Time, d *downexv1.Download) []Notification {
	return c.confirms.add(now, Notification{
		Kind: KindConfirm, Title: titleConfirm, Text: DisplayName(d), ID: d.GetId(), Count: 1,
	})
}

// Deadline is when Flush has held notifications to release, if any are held.
func (c *Center) Deadline() (time.Time, bool) {
	a, okA := c.finished.deadline()
	b, okB := c.confirms.deadline()
	switch {
	case okA && okB:
		if b.Before(a) {
			return b, true
		}
		return a, true
	case okA:
		return a, true
	case okB:
		return b, true
	}
	return time.Time{}, false
}

// Flush returns the coalesced messages whose burst window ended by now.
func (c *Center) Flush(now time.Time) []Notification {
	return append(c.finished.flush(now), c.confirms.flush(now)...)
}
