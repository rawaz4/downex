package downloads

import (
	"math"
	"regexp"
	"slices"
	"strings"
	"testing"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/display"
	. "downex/ui/core/format/formattest"
	"downex/ui/kit"
	"downex/ui/state"
	"downex/ui/state/statetest"
	"downex/ui/views/viewstest"
)

// conn is ConnectionBoxes.test's conn(index, partial): connection index on its own 10 MB
// range, 4 MB into it, at 700 KB/s plus 10 KB/s per index.
func conn(index int32, edit func(c *downexv1.ConnectionInfo)) *downexv1.ConnectionInfo {
	c := &downexv1.ConnectionInfo{
		Index:    index,
		State:    downexv1.ConnectionState_CONNECTION_STATE_RECEIVING,
		Start:    int64(index-1) * 10 * MB,
		End:      int64(index)*10*MB - 1,
		Received: 4 * MB,
		SpeedBps: 700*KB + float64(index)*10*KB,
		Fragment: -1,
	}
	if edit != nil {
		edit(c)
	}
	return c
}

func conns(n int, edit func(c *downexv1.ConnectionInfo)) []*downexv1.ConnectionInfo {
	out := make([]*downexv1.ConnectionInfo, n)
	for i := range out {
		out[i] = conn(int32(i+1), edit)
	}
	return out
}

// ---- ConnectionBoxes.test.ts: connectionBoxes -------------------------------------------------

func TestConnectionBoxesSummary(t *testing.T) {
	t.Run("gives each connection a box with its own speed, adding up to Together", func(t *testing.T) {
		infos := []*downexv1.ConnectionInfo{conn(3, nil), conn(1, nil), conn(2, func(c *downexv1.ConnectionInfo) {
			c.State, c.SpeedBps, c.Detail = downexv1.ConnectionState_CONNECTION_STATE_RETRYING, 0, "HTTP 503, retrying in 3 s"
		}), conn(4, nil)}
		s := display.ConnectionBoxes(infos, display.BoxOptions{})
		var keys, labels []string
		var tones []display.BoxTone
		sum := 0.0
		for _, b := range s.Boxes {
			keys, labels, tones = append(keys, b.Key), append(labels, b.Label), append(tones, b.Tone)
			sum += b.Speed
		}
		if !slices.Equal(keys, []string{"c1", "c2", "c3", "c4"}) {
			t.Error(keys)
		}
		if !slices.Equal(tones, []display.BoxTone{display.BoxLive, display.BoxRetry, display.BoxLive, display.BoxLive}) {
			t.Error(tones)
		}
		if s.Together != sum || s.Together != (710+730+740)*KB {
			t.Errorf("together %v, boxes %v", s.Together, sum)
		}
		// under 1 MB/s each: whole KB/s, the unit said once (decimal units)
		if s.Unit != "KB/s" {
			t.Error(s.Unit)
		}
		if !slices.Equal(labels, []string{"710", "0", "730", "740"}) {
			t.Error(labels)
		}
		if math.Abs(s.Boxes[0].Fraction-0.4) > 0.005 {
			t.Error(s.Boxes[0].Fraction)
		}
		if s.Boxes[1].Title != "Connection 2: retrying (HTTP 503, retrying in 3 s)" {
			t.Error(s.Boxes[1].Title)
		}
	})

	t.Run("ends in a +N box beyond 16 connections that still adds up", func(t *testing.T) {
		s := display.ConnectionBoxes(conns(20, func(c *downexv1.ConnectionInfo) { c.SpeedBps = MB }), display.BoxOptions{})
		if len(s.Boxes) != display.MaxBoxes {
			t.Fatal(len(s.Boxes))
		}
		more := s.Boxes[len(s.Boxes)-1]
		if more.More != 5 || more.Speed != 5*MB {
			t.Errorf("more %+v", more)
		}
		if s.Together != 20*MB || s.Unit != "MB/s" || s.Boxes[0].Label != "1.0" {
			t.Errorf("%v %q %q", s.Together, s.Unit, s.Boxes[0].Label)
		}
	})

	t.Run("picks the unit for the +N box too, so it never reads 1000 or more KB/s", func(t *testing.T) {
		s := display.ConnectionBoxes(conns(32, func(c *downexv1.ConnectionInfo) { c.SpeedBps = 300 * KB }), display.BoxOptions{})
		more := s.Boxes[len(s.Boxes)-1]
		if more.More != 17 || more.Speed != 17*300*KB {
			t.Errorf("more %+v", more)
		}
		if s.Unit != "MB/s" || more.Label != "5.1" || s.Boxes[0].Label != "0.3" {
			t.Errorf("%q %q %q", s.Unit, more.Label, s.Boxes[0].Label)
		}
	})

	t.Run("gives a stream’s connections their speeds too, the fragment in the tooltip", func(t *testing.T) {
		s := display.ConnectionBoxes([]*downexv1.ConnectionInfo{
			conn(1, func(c *downexv1.ConnectionInfo) { c.Fragment = 11 }),
			conn(2, func(c *downexv1.ConnectionInfo) {
				c.Fragment, c.State, c.SpeedBps = 12, downexv1.ConnectionState_CONNECTION_STATE_REQUESTING, 0
			}),
			conn(3, func(c *downexv1.ConnectionInfo) { c.Fragment = 13 }),
		}, display.BoxOptions{Media: true, Piece: "Fragment"})
		var labels []string
		sum := 0.0
		for _, b := range s.Boxes {
			labels = append(labels, b.Label)
			sum += b.Speed
		}
		if !slices.Equal(labels, []string{"710", "0", "730"}) {
			t.Error(labels)
		}
		if s.Boxes[0].Title != "Connection 1 · fragment 12: receiving" || s.Boxes[1].Title != "Connection 2 · fragment 13: connecting" {
			t.Errorf("%q %q", s.Boxes[0].Title, s.Boxes[1].Title)
		}
		if s.Unit != "KB/s" {
			t.Error(s.Unit)
		}
		// the boxes add up to Together
		if s.Together != sum || s.Together != (710+730)*KB {
			t.Error(s.Together)
		}
	})

	t.Run("formats per-connection speeds in one unit", func(t *testing.T) {
		if u := display.SpeedUnit([]float64{300 * KB, 2 * MB}); u != "MB/s" {
			t.Error(u)
		}
		if s := display.SpeedFormatter("MB/s")(1.25 * MB); s != "1.3" {
			t.Error(s)
		}
		if s := display.SpeedFormatter("KB/s")(742.4 * KB); s != "742" {
			t.Error(s)
		}
		// 999.5 KB/s would read "1000": MB/s from there
		if u := display.SpeedUnit([]float64{999_499}); u != "KB/s" {
			t.Error(u)
		}
		if u := display.SpeedUnit([]float64{999_500}); u != "MB/s" {
			t.Error(u)
		}
		if s := display.SpeedFormatter("MB/s")(999_500); s != "1.0" {
			t.Error(s)
		}
		// 9.67 MiB/s
		if s := display.SpeedFormatter("MB/s")(10_139_730); s != "10.1" {
			t.Error(s)
		}
	})

	t.Run("shows live connections only for a running download downexd reports", func(t *testing.T) {
		running := Download(1, func(d *downexv1.Download) {
			d.State, d.ConnectionInfo = state.StateDownloading, []*downexv1.ConnectionInfo{conn(1, nil)}
		})
		if !display.ShowsConnections(running, false) {
			t.Error("running")
		}
		if display.ShowsConnections(running, true) {
			t.Error("stale")
		}
		if display.ShowsConnections(Download(1, func(d *downexv1.Download) { d.State = state.StatePaused }), false) {
			t.Error("paused")
		}
	})
}

// ---- ConnectionBoxes.test.ts: ConnectionBoxes ---------------------------------------------------

// boxesOf is render(ConnectionBoxes, {download, maxConnections}): the column laid out 300 px
// wide in a harness window.
func boxesOf(t *testing.T, d *downexv1.Download, maxConnections int) *connections {
	t.Helper()
	h := viewstest.New(t, viewstest.Options{Daemon: statetest.NewDaemon(d)})
	dl, err := New(h.App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dl.Dispose)
	c := newConnections(dl)
	s := &placed{boxes: []kit.Rect{kit.R(0, 0, 300, 200)}}
	s.Style = kit.Style{Width: kit.Pct(100), Height: kit.Pct(100)}
	s.Add(c)
	h.Show(s)
	c.set(d, false, maxConnections, false)
	h.Settle()
	return c
}

func (c *connections) heading() string { return textOf(c.head) }

func (c *connections) shownSlots() []*connSlot {
	var out []*connSlot
	for _, s := range c.boxes.slots {
		if !s.Style.Hidden {
			out = append(out, s)
		}
	}
	return out
}

func TestConnectionBoxesView(t *testing.T) {
	t.Run("draws one box per connection and the sum", func(t *testing.T) {
		d := Download(1, func(d *downexv1.Download) {
			d.State, d.Size, d.Received, d.Connections = state.StateDownloading, 80*MB, 32*MB, 8
			d.ConnectionInfo = conns(8, func(c *downexv1.ConnectionInfo) { c.SpeedBps = 0.8 * MB })
		})
		c := boxesOf(t, d, 0)
		if n := len(c.shownSlots()); n != 8 {
			t.Errorf("boxes %d", n)
		}
		// the heading is its two spans: "Connections" and the sub-line
		if c.title.Text() != "Connections" || c.sub.Text() != "8 connections · 32.0 of 80.0 MB" {
			t.Errorf("heading %q %q", c.title.Text(), c.sub.Text())
		}
		if !regexp.MustCompile(`^Connections\s+8 connections · 32\.0 of 80\.0 MB$`).MatchString(c.head.Semantics.Name) {
			t.Errorf("heading name %q", c.head.Semantics.Name)
		}
		if s := strings.Join(strings.Fields(c.left.Semantics.Name), " "); s != "Together 6.4 MB/s · each in KB/s" {
			t.Errorf("together %q", s)
		}
		if c.boxes.Semantics.Role != kit.RoleProgressBar || c.boxes.Semantics.Name != "Received" || c.boxes.Semantics.Range.Value != 40 {
			t.Errorf("progressbar %q %v", c.boxes.Semantics.Name, c.boxes.Semantics.Range)
		}
	})

	t.Run("takes two rows beyond 8 connections, without the legend", func(t *testing.T) {
		d := Download(1, func(d *downexv1.Download) {
			d.State, d.Connections, d.ConnectionInfo = state.StateDownloading, 12, conns(12, nil)
		})
		c := boxesOf(t, d, 0)
		if n := len(c.shownSlots()); n != 12 {
			t.Errorf("boxes %d", n)
		}
		if !c.view.twoRows {
			t.Error("one row")
		}
		// two rows of 8 and 4 boxes
		sl := c.shownSlots()
		if sl[8].Bounds().Min.Y <= sl[0].Bounds().Min.Y || sl[7].Bounds().Min.Y != sl[0].Bounds().Min.Y {
			t.Errorf("rows %v %v %v", sl[0].Bounds(), sl[7].Bounds(), sl[8].Bounds())
		}
		if !c.legend.Style.Hidden {
			t.Error("the legend shows")
		}
	})

	t.Run(`shows the segment overview and "Not running" without live connections`, func(t *testing.T) {
		d := Download(1, func(d *downexv1.Download) {
			d.State, d.Size, d.Received = state.StatePaused, 80*MB, 20*MB
			for i := range int64(16) {
				d.Segments = append(d.Segments, &downexv1.Segment{Start: i * 5 * MB, End: (i+1)*5*MB - 1, Received: 5 * MB / 4})
			}
		})
		c := boxesOf(t, d, 16)
		// 16 segments merge into at most 8 boxes
		if n := len(c.shownSlots()); n != 8 {
			t.Errorf("boxes %d", n)
		}
		if !strings.Contains(textOf(&c.Element), "Not running") {
			t.Errorf("text %q", textOf(&c.Element))
		}
		if !strings.Contains(c.heading(), "Up to 16 · 20.0 of 80.0 MB") {
			t.Errorf("heading %q", c.heading())
		}
	})

	t.Run("counts a stream’s fragments", func(t *testing.T) {
		d := MediaDownload(2, func(d *downexv1.Download) {
			d.State, d.Received = state.StateDownloading, 30*MB
			d.Media = MediaInfo(func(m *downexv1.MediaInfo) { m.FragmentsDone, m.FragmentsTotal = 12, 340 })
			d.ConnectionInfo = []*downexv1.ConnectionInfo{
				conn(1, func(c *downexv1.ConnectionInfo) { c.Fragment = 12 }),
				conn(2, func(c *downexv1.ConnectionInfo) { c.Fragment = 13 }),
			}
		})
		c := boxesOf(t, d, 0)
		if !regexp.MustCompile(`^Fragments\s+12 of 340$`).MatchString(c.head.Semantics.Name) || c.heading() != "Fragments12 of 340" {
			t.Errorf("heading %q", c.head.Semantics.Name)
		}
		var speeds, titles []string
		for _, s := range c.shownSlots() {
			speeds, titles = append(speeds, s.speed.Text()), append(titles, s.Tooltip)
		}
		if !slices.Equal(speeds, []string{"710", "720"}) {
			t.Error(speeds)
		}
		if !slices.Equal(titles, []string{"Connection 1 · fragment 13: receiving", "Connection 2 · fragment 14: receiving"}) {
			t.Error(titles)
		}
		if s := c.left.Semantics.Name; !strings.Contains(s, "Together 1.4 MB/s · each in KB/s") {
			t.Errorf("foot %q", s)
		}
		if c.boxes.Semantics.Role != kit.RoleProgressBar || c.boxes.Semantics.Name != "Fragments downloaded" {
			t.Errorf("progressbar %q", c.boxes.Semantics.Name)
		}
	})

	t.Run("says a completed download is all there", func(t *testing.T) {
		c := boxesOf(t, Download(3, func(d *downexv1.Download) { d.State, d.Received = state.StateCompleted, 10*MB }), 0)
		if !strings.Contains(textOf(&c.Element), "All received") {
			t.Errorf("text %q", textOf(&c.Element))
		}
	})
}
