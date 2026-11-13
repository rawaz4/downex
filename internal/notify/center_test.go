package notify

import (
	"fmt"
	"slices"
	"testing"
	"time"

	downexv1 "downex/internal/rpc/downex/v1"
)

// clock is a fake clock: tests move it explicitly.
type clock struct{ now time.Time }

func newClock() *clock { return &clock{now: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)} }

func (c *clock) advance(d time.Duration) time.Time { c.now = c.now.Add(d); return c.now }

func dl(id int64) *downexv1.Download {
	return &downexv1.Download{Id: id, Filename: fmt.Sprintf("file%d.bin", id)}
}

var all = Policy{OnComplete: true, OnFailure: true}

// texts renders notifications as "Title|Text" for compact comparisons.
func texts(ns []Notification) []string {
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = n.Title + "|" + n.Text
	}
	return out
}

func expect(t *testing.T, what string, got []Notification, want ...string) {
	t.Helper()
	if g := texts(got); !slices.Equal(g, want) {
		t.Errorf("%s: got %q, want %q", what, g, want)
	}
}

func TestNotificationTexts(t *testing.T) {
	clk := newClock()
	c := NewCenter(all)
	got := c.Completed(clk.now, dl(1))
	expect(t, "completed", got, "Download complete|file1.bin")
	if n := got[0]; n.Kind != KindCompleted || n.Error || n.ID != 1 || n.Count != 1 {
		t.Errorf("completed = %+v", n)
	}

	f := dl(2)
	f.ErrorCode, f.Error = "disk_full", "write: no space left"
	got = c.Failed(clk.advance(time.Minute), f)
	expect(t, "failed", got, "Download failed|file2.bin\nThe disk is full.")
	if n := got[0]; n.Kind != KindFailed || !n.Error || n.ID != 2 || n.Count != 1 {
		t.Errorf("failed = %+v", n)
	}

	// Bidi controls cannot disguise a name.
	a := dl(3)
	a.Filename = "invoice‮fdp.exe"
	got = c.Awaiting(clk.advance(time.Minute), a)
	expect(t, "awaiting", got, "Confirm download|invoicefdp.exe")
	if n := got[0]; n.Kind != KindConfirm || n.Error || n.ID != 3 {
		t.Errorf("awaiting = %+v", n)
	}

	// A failure without a code or message still says something.
	expect(t, "unknown failure", c.Failed(clk.advance(time.Minute), dl(4)), "Download failed|file4.bin\nUnknown error.")
}

func TestPolicyFilters(t *testing.T) {
	clk := newClock()
	c := NewCenter(Policy{})
	expect(t, "completions off", c.Completed(clk.now, dl(1)))
	expect(t, "failures off", c.Failed(clk.now, dl(2)))
	// Confirmations cannot be turned off: the download waits for the user.
	expect(t, "confirmations", c.Awaiting(clk.now, dl(3)), "Confirm download|file3.bin")

	c.SetPolicy(PolicyFromSettings(&downexv1.Settings{NotifyOnFailure: true}))
	if p := c.Policy(); p.OnComplete || !p.OnFailure {
		t.Fatalf("PolicyFromSettings = %+v", p)
	}
	expect(t, "failures only", append(c.Completed(clk.advance(time.Minute), dl(4)), c.Failed(clk.now, dl(5))...),
		"Download failed|file5.bin\nUnknown error.")
	c.SetPolicy(PolicyFromSettings(&downexv1.Settings{NotifyOnComplete: true}))
	expect(t, "completions only", append(c.Completed(clk.advance(time.Minute), dl(6)), c.Failed(clk.now, dl(7))...),
		"Download complete|file6.bin")
	// Filtered occurrences do not count towards a burst.
	c.SetPolicy(Policy{OnComplete: true})
	clk.advance(time.Minute)
	for i := int64(10); i < 20; i++ {
		c.Failed(clk.now, dl(i))
	}
	for i := int64(20); i < 23; i++ {
		expect(t, "after filtered failures", c.Completed(clk.now, dl(i)), fmt.Sprintf("Download complete|file%d.bin", i))
	}
}

func TestBurstCoalescing(t *testing.T) {
	clk := newClock()
	c := NewCenter(all)
	if _, ok := c.Deadline(); ok {
		t.Fatal("deadline without held notifications")
	}
	// Ten downloads complete within a second: the first three are shown at once, the
	// rest is held until the burst window ends and becomes one message for all ten.
	var shown []Notification
	start := clk.now.Add(100 * time.Millisecond)
	for i := int64(1); i <= 10; i++ {
		shown = append(shown, c.Completed(clk.advance(100*time.Millisecond), dl(i))...)
	}
	expect(t, "burst start", shown, "Download complete|file1.bin", "Download complete|file2.bin", "Download complete|file3.bin")
	due, ok := c.Deadline()
	if want := start.Add(BurstWindow); !ok || !due.Equal(want) {
		t.Fatalf("deadline = %v %v, want %v", due, ok, want)
	}
	expect(t, "flush early", c.Flush(due.Add(-time.Millisecond)))
	clk.now = due
	got := c.Flush(clk.now)
	expect(t, "flush", got, "Downloads complete|10 downloads completed")
	if len(got) != 1 || got[0].Count != 10 || got[0].ID != 0 || got[0].Error || got[0].Kind != KindCompleted {
		t.Fatalf("summary = %+v", got)
	}
	expect(t, "flush twice", c.Flush(clk.advance(time.Hour)))
}

func TestBurstOfExactlyFour(t *testing.T) {
	clk := newClock()
	c := NewCenter(all)
	var shown []Notification
	for i := int64(1); i <= 4; i++ {
		shown = append(shown, c.Completed(clk.advance(time.Second), dl(i))...)
	}
	if len(shown) != 3 {
		t.Fatalf("shown %q, want three", texts(shown))
	}
	due, _ := c.Deadline()
	expect(t, "four within 5 s", c.Flush(due), "Downloads complete|4 downloads completed")
}

func TestLateBurstIsFlushedByTheNextNotification(t *testing.T) {
	// A caller that misses the deadline still gets the summary before the next one.
	clk := newClock()
	c := NewCenter(all)
	for i := int64(1); i <= 5; i++ {
		c.Completed(clk.now, dl(i))
	}
	got := c.Completed(clk.advance(time.Hour), dl(6))
	expect(t, "late", got, "Downloads complete|5 downloads completed", "Download complete|file6.bin")
}

func TestNoCoalescingWhenSpreadOut(t *testing.T) {
	clk := newClock()
	c := NewCenter(all)
	// Three per window never coalesce, however long it goes on.
	for i := int64(1); i <= 12; i++ {
		got := c.Completed(clk.advance(BurstWindow/3+time.Millisecond), dl(i))
		expect(t, fmt.Sprintf("download %d", i), got, fmt.Sprintf("Download complete|file%d.bin", i))
	}
	if _, ok := c.Deadline(); ok {
		t.Fatal("something is held")
	}
}

func TestSteadyStreamYieldsOneMessagePerWindow(t *testing.T) {
	clk := newClock()
	c := NewCenter(all)
	id := int64(0)
	next := func() []Notification {
		id++
		return c.Completed(clk.advance(500*time.Millisecond), dl(id))
	}
	// First window: 3 shown, 7 held (10 in 5 s) -> summary of 10.
	var shown []Notification
	for range 10 {
		shown = append(shown, next()...)
	}
	if len(shown) != 3 {
		t.Fatalf("first window showed %d", len(shown))
	}
	// The window ends while the stream continues: the 11th flushes it and opens a
	// quiet window that holds everything.
	expect(t, "first summary", next(), "Downloads complete|10 downloads completed")
	for range 9 {
		expect(t, "quiet window", next())
	}
	due, ok := c.Deadline()
	if !ok {
		t.Fatal("nothing held in the quiet window")
	}
	expect(t, "second summary", c.Flush(due), "Downloads complete|10 downloads completed")

	// A single straggler right after a summarised window is held, then shown as it is.
	clk.now = due.Add(time.Second)
	expect(t, "straggler held", next())
	due, _ = c.Deadline()
	expect(t, "straggler", c.Flush(due), fmt.Sprintf("Download complete|file%d.bin", id))
	// Once a window passed without a summary, notifications show at once again.
	clk.now = due.Add(BurstWindow + time.Second)
	expect(t, "calm again", next(), fmt.Sprintf("Download complete|file%d.bin", id))
}

func TestMixedBurstSummary(t *testing.T) {
	clk := newClock()
	c := NewCenter(all)
	var shown []Notification
	for i := int64(1); i <= 6; i++ {
		if i%3 == 0 {
			shown = append(shown, c.Failed(clk.now, dl(i))...)
		} else {
			shown = append(shown, c.Completed(clk.now, dl(i))...)
		}
	}
	if len(shown) != 3 {
		t.Fatalf("shown %q", texts(shown))
	}
	due, _ := c.Deadline()
	got := c.Flush(due)
	expect(t, "mixed", got, "Downloads finished|4 downloads completed, 2 failed")
	if !got[0].Error || got[0].Kind != KindFailed || got[0].Count != 6 {
		t.Errorf("mixed summary = %+v", got[0])
	}

	clk.now = due.Add(time.Hour)
	for i := int64(11); i <= 15; i++ {
		c.Failed(clk.now, dl(i))
	}
	due, _ = c.Deadline()
	expect(t, "failures", c.Flush(due), "Downloads failed|5 downloads failed")
}

func TestConfirmBurstIsSeparate(t *testing.T) {
	clk := newClock()
	c := NewCenter(all)
	var got []Notification
	for i := int64(1); i <= 5; i++ {
		got = append(got, c.Awaiting(clk.now, dl(i))...)
	}
	for i := int64(11); i <= 13; i++ {
		got = append(got, c.Completed(clk.now, dl(i))...)
	}
	// Three confirmations and three completions: each kind has its own budget.
	if len(got) != 6 {
		t.Fatalf("shown %q", texts(got))
	}
	due, ok := c.Deadline()
	if !ok {
		t.Fatal("no deadline")
	}
	expect(t, "confirm summary", c.Flush(due), "Confirm downloads|5 downloads are waiting for confirmation")

	// Both kinds held: the deadline is the earlier window's end.
	clk.now = due.Add(time.Hour)
	for i := int64(21); i <= 25; i++ {
		c.Completed(clk.now, dl(i))
	}
	clk.advance(time.Second)
	for i := int64(31); i <= 35; i++ {
		c.Awaiting(clk.now, dl(i))
	}
	first, _ := c.Deadline()
	if want := clk.now.Add(-time.Second + BurstWindow); !first.Equal(want) {
		t.Fatalf("deadline %v, want %v", first, want)
	}
	expect(t, "finished first", c.Flush(first), "Downloads complete|5 downloads completed")
	second, _ := c.Deadline()
	expect(t, "then confirmations", c.Flush(second), "Confirm downloads|5 downloads are waiting for confirmation")
}
