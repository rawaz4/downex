package state_test

import (
	"slices"
	"testing"
	"time"

	"downex/ui/state"
	"downex/ui/state/statetest"
)

func keys(n *state.Notices) []string {
	var out []string
	for _, x := range n.Items() {
		k := x.Key
		if n.Leaving(x.Key) {
			k += " (leaving)"
		}
		out = append(out, k)
	}
	return out
}

func TestNoticesKeepAtMostThreeAndReplaceByKey(t *testing.T) {
	c := statetest.NewClock(0)
	n := state.NewNotices(c, nil)
	for _, k := range []string{"dl-1", "dl-2", "dl-3", "dl-4"} {
		n.Show(state.Notice{Key: k, Title: "Download added from the browser"})
	}
	if !slices.Equal(keys(n), []string{"dl-2", "dl-3", "dl-4"}) {
		t.Fatal(keys(n))
	}
	n.Show(state.Notice{Key: "dl-3", Title: "Download from the browser to confirm"})
	if !slices.Equal(keys(n), []string{"dl-2", "dl-4", "dl-3"}) || n.Items()[2].Title != "Download from the browser to confirm" {
		t.Fatal(keys(n))
	}
	c.Advance(state.NoticeLifetime)
	if len(n.Items()) != 0 {
		t.Fatal(keys(n))
	}
}

func TestNoticesWaitWhileHeld(t *testing.T) {
	c := statetest.NewClock(0)
	n := state.NewNotices(c, func() bool { return true })
	n.Show(state.Notice{Key: "tools", Title: "Downex could not set up yt-dlp/Deno"})
	c.Advance(5 * time.Second)
	n.Hold("tools")
	c.Advance(time.Minute)
	if !slices.Equal(keys(n), []string{"tools"}) {
		t.Fatal(keys(n))
	}
	n.Release("tools")
	c.Advance(4*time.Second - time.Millisecond)
	if !slices.Equal(keys(n), []string{"tools"}) {
		t.Fatal(keys(n))
	}
	c.Advance(time.Millisecond)
	if !slices.Equal(keys(n), []string{"tools (leaving)"}) {
		t.Fatal(keys(n))
	}
	// a leaving notice is not held and does not count towards the three
	n.Hold("tools")
	for _, k := range []string{"a", "b", "c"} {
		n.Show(state.Notice{Key: k})
	}
	if len(n.Items()) != 4 {
		t.Fatal(keys(n))
	}
	c.Advance(state.ToastExit)
	if !slices.Equal(keys(n), []string{"a", "b", "c"}) {
		t.Fatal(keys(n))
	}
	n.Clear()
	c.Advance(time.Hour)
	if len(n.Items()) != 0 || c.Pending() != 0 {
		t.Fatal(keys(n), c.Pending())
	}
}
