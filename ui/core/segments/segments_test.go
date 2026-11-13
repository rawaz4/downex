package segments

import (
	"math"
	"reflect"
	"testing"

	downexv1 "downex/internal/rpc/downex/v1"
)

// The cases of ui/frontend/src/lib/segments.test.ts; its SegmentBar render block is held here
// by the cells and tone a bar draws.

const (
	MiB  = 1 << 20
	SIZE = 64 * MiB
)

var (
	downloading = downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING
	paused      = downexv1.DownloadState_DOWNLOAD_STATE_PAUSED
	failed      = downexv1.DownloadState_DOWNLOAD_STATE_FAILED
	queued      = downexv1.DownloadState_DOWNLOAD_STATE_QUEUED
	completed   = downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED
)

// split is n equal segments of the file, each with received(i, len) bytes.
func split(n int, received func(i int, length float64) float64, size int64) []*downexv1.Segment {
	step := size / int64(n)
	out := make([]*downexv1.Segment, n)
	for i := range n {
		start := int64(i) * step
		end := int64(i+1)*step - 1
		if i == n-1 {
			end = size - 1
		}
		out[i] = &downexv1.Segment{Start: start, End: end, Received: int64(math.Round(received(i, float64(end-start+1))))}
	}
	return out
}

func none(int, float64) float64 { return 0 }

func file(id int64, size, received int64, state downexv1.DownloadState, segs []*downexv1.Segment) *downexv1.Download {
	return &downexv1.Download{Id: id, Size: size, Received: received, State: state, Segments: segs, Kind: downexv1.DownloadKind_DOWNLOAD_KIND_FILE}
}

func stream(id int64, state downexv1.DownloadState, received int64, total, done int32) *downexv1.Download {
	return &downexv1.Download{
		Id: id, Size: -1, Received: received, State: state, Kind: downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA,
		Media: &downexv1.MediaInfo{Format: "hls", FragmentsTotal: total, FragmentsDone: done, Origin: downexv1.MediaOrigin_MEDIA_ORIGIN_MANIFEST},
	}
}

func fractions(cells []SegCell) []float64 {
	out := make([]float64, len(cells))
	for i, c := range cells {
		out[i] = c.Fraction
	}
	return out
}

func lives(cells []SegCell) []bool {
	out := make([]bool, len(cells))
	for i, c := range cells {
		out[i] = c.Live
	}
	return out
}

func near(a, b float64, digits int) bool { return math.Abs(a-b) < math.Pow(10, -float64(digits))/2 }

func TestSegmentCells(t *testing.T) {
	t.Run("at most 8 equal spans however many connections there are", func(t *testing.T) {
		for _, n := range []int{1, 3, 8, 16, 32, 64} {
			d := file(1, SIZE, 0, downloading, split(n, none, SIZE))
			if got := len(SegmentCells(d, MaxCells)); got != min(n, MaxCells) {
				t.Errorf("%d segments: %d cells", n, got)
			}
		}
		sixteen := file(1, SIZE, 0, downloading, split(16, none, SIZE))
		if got := len(SegmentCells(sixteen, 4)); got != 4 {
			t.Errorf("max 4: %d", got)
		}
		if got := len(SegmentCells(sixteen, 0)); got != 1 {
			t.Errorf("max 0: %d", got)
		}
	})
	t.Run("fills each span with the received bytes that overlap it", func(t *testing.T) {
		// 16 connections, every other one done: each of the 8 spans holds one done and one empty half.
		d := file(1, SIZE, 0, paused, split(16, func(i int, l float64) float64 {
			if i%2 == 0 {
				return l
			}
			return 0
		}, SIZE))
		cells := SegmentCells(d, MaxCells)
		if len(cells) != 8 {
			t.Fatalf("%d cells", len(cells))
		}
		for _, c := range cells {
			if !near(c.Fraction, 0.5, 5) || c.Live {
				t.Errorf("cell %+v", c)
			}
		}
	})
	t.Run("sums a range that crosses span borders into each span it covers", func(t *testing.T) {
		const e = SIZE / 8
		// Eight segments of unequal length; the bar still shows eight equal spans of e bytes.
		segs := []*downexv1.Segment{
			{Start: 0, End: 3*e - 1, Received: 5 * e / 2}, // spans 0 and 1 full, half of 2
			{Start: 3 * e, End: 4*e - 1, Received: 0},
			{Start: 4 * e, End: 5*e - 1, Received: e},
			{Start: 5 * e, End: 6*e - 1, Received: e / 2},
			{Start: 6 * e, End: 13*e/2 - 1, Received: 0},
			{Start: 13 * e / 2, End: 7*e - 1, Received: 0},
			{Start: 7 * e, End: 15*e/2 - 1, Received: 0},
			{Start: 15 * e / 2, End: 8*e - 1, Received: e / 4},
		}
		d := file(1, SIZE, 0, paused, segs)
		if got := fractions(SegmentCells(d, MaxCells)); !reflect.DeepEqual(got, []float64{1, 1, 0.5, 0, 1, 0.5, 0, 0.25}) {
			t.Errorf("8 spans = %v", got)
		}
		// Four spans of the same map: each sums the two spans above.
		if got := fractions(SegmentCells(d, 4)); !reflect.DeepEqual(got, []float64{1, 0.25, 0.75, 0.125}) {
			t.Errorf("4 spans = %v", got)
		}
	})
	t.Run("marks the spans where a connection is writing, only while downloading", func(t *testing.T) {
		// 4 connections, each a quarter into its range: fronts at 1/16, 5/16, 9/16, 13/16 of the file.
		quarter := func(_ int, l float64) float64 { return l / 4 }
		running := SegmentCells(file(1, SIZE, 0, downloading, split(4, quarter, SIZE)), MaxCells)
		if !reflect.DeepEqual(lives(running), []bool{true, true, true, true}) || !reflect.DeepEqual(fractions(running), []float64{0.25, 0.25, 0.25, 0.25}) {
			t.Errorf("running = %+v", running)
		}
		eight := SegmentCells(file(2, SIZE, 0, downloading, split(8, func(i int, l float64) float64 {
			if i%2 == 0 {
				return l / 2
			}
			return 0
		}, SIZE)), MaxCells)
		if !reflect.DeepEqual(lives(eight), []bool{true, true, true, true, true, true, true, true}) {
			t.Errorf("eight = %v", lives(eight))
		}
		for _, c := range SegmentCells(file(3, SIZE, 0, paused, split(4, quarter, SIZE)), MaxCells) {
			if c.Live {
				t.Error("a paused cell is live")
			}
		}
		// a finished range has no front
		done := SegmentCells(file(4, SIZE, 0, downloading, split(2, func(i int, l float64) float64 {
			if i == 0 {
				return l
			}
			return 0
		}, SIZE)), MaxCells)
		if !reflect.DeepEqual(done, []SegCell{{Fraction: 1}, {Fraction: 0, Live: true}}) {
			t.Errorf("done = %+v", done)
		}
	})
	t.Run("snaps an almost complete span to full and reads an open end as the file end", func(t *testing.T) {
		d := file(1, 1000, 0, downloading, []*downexv1.Segment{{Start: 0, End: 499, Received: 498}, {Start: 500, End: -1, Received: 250}})
		cells := SegmentCells(d, MaxCells)
		if cells[0] != (SegCell{Fraction: 1, Live: true}) || !near(cells[1].Fraction, 0.5, 5) {
			t.Errorf("cells = %+v", cells)
		}
	})
	t.Run("gives a stream and a download without segments one cell of its progress", func(t *testing.T) {
		if got := SegmentCells(stream(1, downloading, 30*MiB, 200, 50), MaxCells); !reflect.DeepEqual(got, []SegCell{{Fraction: 0.25, Live: true}}) {
			t.Errorf("stream = %+v", got)
		}
		if got := SegmentCells(file(2, 100, 40, paused, nil), MaxCells); !reflect.DeepEqual(got, []SegCell{{Fraction: 0.4}}) {
			t.Errorf("plain = %+v", got)
		}
	})
	t.Run("shows an unknown size as one indeterminate cell, hatched while bytes arrive", func(t *testing.T) {
		arriving := file(1, -1, 5*MiB, downloading, []*downexv1.Segment{{Start: 0, End: -1, Received: 5 * MiB}})
		if got := SegmentCells(arriving, MaxCells); !reflect.DeepEqual(got, []SegCell{{Live: true, Indeterminate: true}}) {
			t.Errorf("arriving = %+v", got)
		}
		if got := SegmentCells(file(2, -1, 0, queued, nil), MaxCells); !reflect.DeepEqual(got, []SegCell{{Indeterminate: true}}) {
			t.Errorf("waiting = %+v", got)
		}
		if got := SegmentCells(stream(3, downloading, MiB, 0, 0), MaxCells)[0]; !got.Indeterminate || !got.Live {
			t.Errorf("stream = %+v", got)
		}
	})
	t.Run("fills every cell of a completed download", func(t *testing.T) {
		d := file(1, SIZE, SIZE, completed, split(4, func(_ int, l float64) float64 { return l - 1 }, SIZE))
		if got := SegmentCells(d, MaxCells); !reflect.DeepEqual(got, []SegCell{{Fraction: 1}, {Fraction: 1}, {Fraction: 1}, {Fraction: 1}}) {
			t.Errorf("completed = %+v", got)
		}
	})
}

func TestBarCells(t *testing.T) {
	// the Comfortable row's bar cell
	const rowPx = 118
	at := func(state downexv1.DownloadState, share float64, n int) *downexv1.Download {
		return file(1, SIZE, int64(math.Round(SIZE*share)), state, split(n, func(_ int, l float64) float64 { return l * share }, SIZE))
	}
	t.Run("draws a download that is not running as one cell of its progress", func(t *testing.T) {
		for _, state := range []downexv1.DownloadState{paused, failed, queued} {
			cells := BarCells(at(state, 0.18, 8), rowPx, MaxCells)
			if len(cells) != 1 || !near(cells[0].Fraction, 0.18, 2) || cells[0].Live {
				t.Errorf("%v: %+v", state, cells)
			}
			// with no width known as well
			if got := len(BarCells(at(state, 0.18, 8), 0, MaxCells)); got != 1 {
				t.Errorf("%v without a width: %d cells", state, got)
			}
		}
		if got := BarCells(file(2, -1, 5, paused, nil), rowPx, MaxCells); !reflect.DeepEqual(got, []SegCell{{Indeterminate: true}}) {
			t.Errorf("unknown size = %+v", got)
		}
	})
	t.Run("draws a running file as one solid fill of its progress, whatever the width", func(t *testing.T) {
		for _, w := range []float64{0, 40, rowPx, 600} {
			for _, share := range []float64{0.02, 0.18, 0.71} {
				d := at(downloading, share, 8)
				got := BarCells(d, w, MaxCells)
				if len(got) != 1 || got[0] != RowCell(d) {
					t.Errorf("%v px at %v: %+v, want the row cell %+v", w, share, got, RowCell(d))
				}
			}
		}
	})
	t.Run("keeps a completed file one full cell", func(t *testing.T) {
		d := file(1, SIZE, SIZE, completed, split(4, func(_ int, l float64) float64 { return l }, SIZE))
		if got := BarCells(d, rowPx, MaxCells); !reflect.DeepEqual(got, []SegCell{{Fraction: 1}}) {
			t.Errorf("completed = %+v", got)
		}
	})
}

func TestCellsForWidth(t *testing.T) {
	if MinCellPx != 12 || CellGapPx != 2 {
		t.Fatal("cell sizes")
	}
	for _, c := range []struct {
		w    float64
		max  int
		want int
	}{{0, 8, 8}, {10, 8, 1}, {12, 8, 1}, {26, 8, 2}, {90, 8, 6}, {110, 8, 8}, {400, 8, 8}, {400, 4, 4}} {
		if got := CellsForWidth(c.w, c.max); got != c.want {
			t.Errorf("CellsForWidth(%v, %d) = %d, want %d", c.w, c.max, got, c.want)
		}
	}
	for w := 1; w < 200; w++ {
		if n := CellsForWidth(float64(w), MaxCells); n > 1 && float64(w-(n-1)*CellGapPx)/float64(n) < MinCellPx {
			t.Errorf("%d px: %d cells are too narrow", w, n)
		}
	}
}

func TestRowCell(t *testing.T) {
	t.Run("a running file is one solid fill of its overall progress, however many connections", func(t *testing.T) {
		for _, n := range []int{1, 8, 16, 32} {
			d := file(1, SIZE, int64(math.Round(SIZE*0.71)), downloading, split(n, func(_ int, l float64) float64 { return l * 0.71 }, SIZE))
			if c := RowCell(d); !near(c.Fraction, 0.71, 2) || !c.Live {
				t.Errorf("%d connections: %+v", n, c)
			}
		}
	})
	t.Run("follows the state", func(t *testing.T) {
		for _, c := range []struct {
			d    *downexv1.Download
			want SegCell
		}{
			{file(1, SIZE, SIZE, completed, split(4, func(_ int, l float64) float64 { return l - 1 }, SIZE)), SegCell{Fraction: 1}},
			{file(2, 100, 40, paused, nil), SegCell{Fraction: 0.4}},
			{file(3, -1, 5, downloading, nil), SegCell{Live: true, Indeterminate: true}},
			{file(4, -1, 0, queued, nil), SegCell{Indeterminate: true}},
			{stream(5, downloading, 30*MiB, 200, 50), SegCell{Fraction: 0.25, Live: true}},
		} {
			if got := RowCell(c.d); got != c.want {
				t.Errorf("RowCell(%d) = %+v, want %+v", c.d.GetId(), got, c.want)
			}
		}
	})
}

// The SegmentBar block: what a bar draws (its cells and tone).
func TestSegmentBar(t *testing.T) {
	row := file(1, SIZE, int64(math.Round(SIZE*0.71)), downloading, split(8, func(_ int, l float64) float64 { return l * 0.71 }, SIZE))
	if c := RowCell(row); !c.Live || !near(c.Fraction, 0.71, 2) {
		t.Errorf("row cell = %+v", c)
	}
	lg := file(1, SIZE, SIZE/2, downloading, split(16, func(_ int, l float64) float64 { return l / 2 }, SIZE))
	cells := BarCells(lg, 0, MaxCells)
	if len(cells) != 1 || ToneOf(lg.State) != ToneFill { // one solid fill, as the rows draw it
		t.Errorf("lg = %+v", cells)
	}
	for _, c := range cells {
		if !c.Live || c.Fraction != 0.5 {
			t.Errorf("lg cell = %+v", c)
		}
	}
	p := file(1, SIZE, SIZE*18/100, paused, split(8, func(_ int, l float64) float64 { return l * 0.18 }, SIZE))
	if got := BarCells(p, 0, MaxCells); len(got) != 1 || ToneOf(p.State) != TonePaused {
		t.Errorf("paused = %+v", got)
	}
	for state, want := range map[downexv1.DownloadState]string{paused: "paused", failed: "failed", queued: "idle", completed: "fill", downloading: "fill"} {
		if got := ToneOf(state).String(); got != want {
			t.Errorf("ToneOf(%v) = %q", state, got)
		}
	}
	// an unknown size is hatched while bytes arrive, without a fill
	if c := RowCell(file(1, -1, 10, downloading, nil)); !c.Indeterminate || !c.Live {
		t.Errorf("hatch = %+v", c)
	}
}

func TestSegmentMap(t *testing.T) {
	d := file(1, 1000, 300, downloading, []*downexv1.Segment{{Start: 0, End: 499, Received: 500}, {Start: 500, End: -1, Received: 100}})
	if s := Summarize(d); s != (Summary{Total: 2, Active: 1, Done: 1}) {
		t.Errorf("summary = %+v", s)
	}
	for _, c := range []struct {
		d    *downexv1.Download
		want string
	}{
		{d, "Segment map: 2 segments, 1 active, 30% received"},
		{file(2, 1000, 1000, completed, nil), "Segment map: download complete"},
		{file(3, -1, 10, downloading, nil), "Segment map: size unknown, single connection"},
		// today's label counts a download without segments as 1 but reads the plural (byte for byte)
		{file(4, 1000, 250, paused, nil), "Segment map: 1 segments, 25% received"},
	} {
		if got := MapLabel(c.d); got != c.want {
			t.Errorf("MapLabel(%d) = %q", c.d.GetId(), got)
		}
	}
	shapes := AppendMapShapes(nil, d, 100, 18, false)
	want := []Shape{
		{Kind: ShapeRect, Paint: PaintTrack, W: 100, H: 18},
		{Kind: ShapeRect, Paint: PaintDone, X: 0, W: 50, H: 18},
		{Kind: ShapeRect, Paint: PaintDone, X: 50, W: 10, H: 18},
		{Kind: ShapeRect, Paint: PaintFront, X: 58, W: 2, H: 18},
		{Kind: ShapeRect, Paint: PaintGap, X: 49.5, W: 1, H: 18},
	}
	if !reflect.DeepEqual(shapes, want) {
		t.Errorf("shapes = %+v", shapes)
	}
	marked := AppendMapShapes(nil, d, 100, 18, true)
	if n := len(marked); n != 1+3+4 || marked[4] != (Shape{Kind: ShapeRect, Paint: PaintMarker, X: 0, W: 2, H: 18}) || marked[7] != (Shape{Kind: ShapeNotch, Paint: PaintMarker, X: 46}) {
		t.Errorf("markers = %+v", marked)
	}
	if got := AppendMapShapes(nil, file(5, -1, 0, downloading, nil), 100, 18, false); len(got) != 1 {
		t.Errorf("nothing arrived = %+v", got)
	}
	hatch := AppendMapShapes(nil, file(6, -1, 5, downloading, nil), 100, 18, false)
	if len(hatch) != 1+int(math.Ceil((100+18+18)/9.0)) || hatch[1] != (Shape{Kind: ShapeStroke, Paint: PaintDone, X: -18, Y: 18, W: 18, H: -18}) {
		t.Errorf("hatch = %d shapes, %+v", len(hatch), hatch[1])
	}
}

func TestNoAllocationsInAFrame(t *testing.T) {
	d := file(1, SIZE, SIZE/2, downloading, split(16, func(_ int, l float64) float64 { return l / 2 }, SIZE))
	cells := make([]SegCell, 0, MaxCells)
	shapes := make([]Shape, 0, 64)
	if n := testing.AllocsPerRun(100, func() {
		cells = AppendBarCells(cells[:0], d, 300, MaxCells)
		shapes = AppendMapShapes(shapes[:0], d, 300, 18, true)
		_ = RowCell(d)
	}); n != 0 {
		t.Errorf("%v allocations per frame", n)
	}
}
