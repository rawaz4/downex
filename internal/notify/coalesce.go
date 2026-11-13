package notify

import (
	"fmt"
	"time"
)

// coalescer limits how many notifications are shown in a burst. A burst window opens
// with the first notification and lasts BurstWindow. Its first BurstLimit
// notifications are shown at once; later ones are held, and when the window ends they
// all become one message counting the whole window ("N downloads completed"). A window
// that opens within BurstWindow of the end of a window that was summarised holds
// everything from the start (a steady stream yields one message per window); if it
// then holds only one notification, that one is shown as it is.
type coalescer struct {
	window time.Duration
	limit  int
	// summary renders a coalesced message for the counts of a window.
	summary func(counts map[Kind]int, total int) Notification

	open      bool
	start     time.Time
	quiet     bool // the window holds everything
	total     int
	counts    map[Kind]int
	held      int
	firstHeld Notification
	// summarisedEnd is the end of the last window that produced a coalesced message.
	summarisedEnd time.Time
}

func newCoalescer(summary func(map[Kind]int, int) Notification) *coalescer {
	return &coalescer{window: BurstWindow, limit: BurstLimit, summary: summary}
}

// add takes n at now and returns what is to be shown now: n itself, nothing (n is
// held), and before it the message of a window that ended before now.
func (c *coalescer) add(now time.Time, n Notification) []Notification {
	out := c.flush(now)
	if !c.open {
		c.open, c.start, c.total, c.held = true, now, 0, 0
		c.counts = make(map[Kind]int, 2)
		c.quiet = !c.summarisedEnd.IsZero() && now.Before(c.summarisedEnd.Add(c.window))
	}
	c.total++
	c.counts[n.Kind] += max(n.Count, 1)
	if !c.quiet && c.total <= c.limit {
		return append(out, n)
	}
	if c.held == 0 {
		c.firstHeld = n
	}
	c.held++
	return out
}

// deadline is when the held notifications are due, if any are held.
func (c *coalescer) deadline() (time.Time, bool) {
	if !c.open || c.held == 0 {
		return time.Time{}, false
	}
	return c.start.Add(c.window), true
}

// flush closes the window if it ended by now and returns its held notifications as
// one message (or the single held one of a quiet window).
func (c *coalescer) flush(now time.Time) []Notification {
	if !c.open || now.Before(c.start.Add(c.window)) {
		return nil
	}
	c.open = false
	switch {
	case c.held == 0:
		return nil
	case c.quiet && c.held == 1:
		return []Notification{c.firstHeld}
	}
	c.summarisedEnd = c.start.Add(c.window)
	total := 0
	for _, n := range c.counts {
		total += n
	}
	return []Notification{c.summary(c.counts, total)}
}

// finishedSummary coalesces completed and failed downloads.
func finishedSummary(counts map[Kind]int, total int) Notification {
	done, failed := counts[KindCompleted], counts[KindFailed]
	n := Notification{Kind: KindCompleted, Count: total}
	switch {
	case failed == 0:
		n.Title, n.Text = "Downloads complete", fmt.Sprintf("%d downloads completed", done)
	case done == 0:
		n.Kind, n.Error = KindFailed, true
		n.Title, n.Text = "Downloads failed", fmt.Sprintf("%d downloads failed", failed)
	default:
		n.Kind, n.Error = KindFailed, true
		n.Title, n.Text = "Downloads finished", fmt.Sprintf("%d downloads completed, %d failed", done, failed)
	}
	return n
}

// confirmSummary coalesces downloads awaiting confirmation.
func confirmSummary(counts map[Kind]int, total int) Notification {
	return Notification{Kind: KindConfirm, Count: total, Title: "Confirm downloads",
		Text: fmt.Sprintf("%d downloads are waiting for confirmation", counts[KindConfirm])}
}
