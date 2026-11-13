package display

import (
	"math"
	"slices"
	"strconv"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/format"
	"downex/ui/core/text"
)

// ---- the speed chart (DESIGN-SYSTEM.md §3.15) ----

// SpeedAxis is the y-axis of the speed chart: its top in bytes per second and the labels top,
// half and 0.
type SpeedAxis struct {
	Max    float64
	Labels [3]string
}

var axisUnits = [...]struct {
	size float64
	unit string
}{{1000, "KB/s"}, {1000 * 1000, "MB/s"}, {1000 * 1000 * 1000, "GB/s"}}

// log10 is JavaScript's Math.log10, exact for the powers of ten (Go's math.Log10(1000) is
// 2.9999999999999996).
func log10(v float64) float64 {
	if v > 0 && !math.IsInf(v, 0) {
		if e := math.Round(math.Log10(v)); math.Pow(10, e) == v {
			return e
		}
	}
	return math.Log10(v)
}

// niceCeil is the smallest 1, 2 or 5 × 10^k that is at least v (> 0).
func niceCeil(v float64) float64 {
	p := math.Pow(10, math.Floor(log10(v)))
	for _, m := range []float64{1, 2, 5, 10} {
		if m*p >= v*(1-1e-9) {
			return m * p
		}
	}
	return 10 * p
}

// axisNum is String(Math.round(n * 100) / 100).
func axisNum(n float64) string { return format.Num(format.Round(n*100) / 100) }

// SpeedAxisFor is the axis for a peak speed: the top is the peak rounded up to 1, 2 or 5 of
// the unit that keeps it under 1000 ("10 MB/s" for 5.9 MB/s, "1 MB/s" for 931 KB/s); the
// middle label is half of it. No speed yet: up to 1 MB/s.
func SpeedAxisFor(peak float64) SpeedAxis {
	if !(peak > 0) || math.IsInf(peak, 0) {
		return SpeedAxis{Max: mb, Labels: [3]string{"1 MB/s", "0.5 MB/s", "0"}}
	}
	for i, u := range axisUnits {
		top := niceCeil(peak / u.size)
		if top < 1000 || i == len(axisUnits)-1 {
			return SpeedAxis{Max: top * u.size, Labels: [3]string{axisNum(top) + " " + u.unit, axisNum(top/2) + " " + u.unit, "0"}}
		}
	}
	return SpeedAxis{Max: peak, Labels: [3]string{"", "", "0"}}
}

// HistoryMS is how far back the speed chart and the status bar's sparkline look
// (stores/downloads.svelte.ts HISTORY_MS).
const HistoryMS = 60_000

// SpeedSample is one point of a speed history: when (epoch milliseconds) and how fast (bytes
// per second).
type SpeedSample struct {
	T float64
	V float64
}

// SpeedStats are the samples of the last HistoryMS before the newest one, with their
// average and peak.
type SpeedStats struct {
	Avg   float64
	Peak  float64
	Shown []SpeedSample
}

// SpeedStatsOf picks the samples the chart shows and measures them.
func SpeedStatsOf(samples []SpeedSample) SpeedStats {
	if len(samples) == 0 {
		return SpeedStats{Shown: []SpeedSample{}}
	}
	from := samples[len(samples)-1].T - HistoryMS
	shown := []SpeedSample{}
	for _, s := range samples {
		if s.T >= from {
			shown = append(shown, s)
		}
	}
	sum, peak := 0.0, 0.0
	for _, s := range shown {
		sum += s.V
		peak = jsMax(peak, s.V)
	}
	avg := 0.0
	if len(shown) > 0 {
		avg = sum / float64(len(shown))
	}
	return SpeedStats{Avg: avg, Peak: peak, Shown: shown}
}

// ---- connections (DESIGN-SYSTEM.md §3.15, from B) ----

// MaxBoxes is the most boxes the panel draws (two rows of 8); more connections end in a "+N"
// box.
const MaxBoxes = 16

// BoxTone is how a connection's box is drawn.
type BoxTone string

// The box tones.
const (
	BoxLive  BoxTone = "live"
	BoxWait  BoxTone = "wait"
	BoxRetry BoxTone = "retry"
	BoxError BoxTone = "error"
	BoxIdle  BoxTone = "idle"
	BoxDone  BoxTone = "done"
)

// ConnBox is one box of the connections panel.
type ConnBox struct {
	Key string
	// Fraction is 0..1 of its current range received.
	Fraction float64
	Tone     BoxTone
	Speed    float64
	// Label is under the box: the speed in the summary's unit.
	Label string
	Title string
	// More is N for the "+N" box (N connections not drawn one by one), else 0.
	More int
}

// ConnSummary is the connections panel's boxes.
type ConnSummary struct {
	Boxes []ConnBox
	// Together is the sum of the boxes' speeds: every connection's, so the boxes add up to it.
	Together float64
	// Unit is the unit of the speeds under the boxes.
	Unit string
}

func toneOf(c *downexv1.ConnectionInfo) BoxTone {
	switch c.GetState() {
	case downexv1.ConnectionState_CONNECTION_STATE_RECEIVING:
		return BoxLive
	case downexv1.ConnectionState_CONNECTION_STATE_CONNECTING, downexv1.ConnectionState_CONNECTION_STATE_REQUESTING:
		return BoxWait
	case downexv1.ConnectionState_CONNECTION_STATE_RETRYING:
		return BoxRetry
	case downexv1.ConnectionState_CONNECTION_STATE_ERROR:
		return BoxError
	case downexv1.ConnectionState_CONNECTION_STATE_DONE:
		return BoxDone
	}
	return BoxIdle
}

var stateText = map[BoxTone]string{
	BoxLive:  "receiving",
	BoxWait:  "connecting",
	BoxRetry: "retrying",
	BoxError: "failed",
	BoxIdle:  "idle",
	BoxDone:  "done",
}

// rangeFraction is a connection's share of its current range; a finished one is full, one
// without a range empty.
func rangeFraction(c *downexv1.ConnectionInfo) float64 {
	if c.GetState() == downexv1.ConnectionState_CONNECTION_STATE_DONE {
		return 1
	}
	start, end := float64(c.GetStart()), float64(c.GetEnd())
	if !(start >= 0) || !(end >= start) {
		return 0
	}
	return math.Min(1, math.Max(0, float64(c.GetReceived())/(end-start+1)))
}

// SpeedUnit is the one unit the per-connection speeds under the boxes share, said once by
// "Together": MB/s with one decimal when any label (a connection, or the +N box's sum) reaches
// 1 MB/s (or would show as 1000 KB/s), else KB/s in whole numbers.
func SpeedUnit(speeds []float64) string {
	top := 0.0
	for _, s := range speeds {
		top = jsMax(top, s)
	}
	if top >= mb-500 {
		return "MB/s"
	}
	return "KB/s"
}

// SpeedFormatter writes speeds in a unit for the boxes: "5.1" in MB/s, "931" in KB/s.
func SpeedFormatter(unit string) func(bps float64) string {
	if unit == "MB/s" {
		return func(bps float64) string { return format.Fixed(bps/mb, 1) }
	}
	return func(bps float64) string { return format.Num(format.Round(bps / 1000)) }
}

// BoxOptions are ConnectionBoxes' options.
type BoxOptions struct {
	// Media boxes name the fragment each connection is on in their title.
	Media bool
	// Piece is what a media download's pieces are called ("Fragment" when "").
	Piece string
	// Max is the most boxes drawn (MaxBoxes when 0; at least 2).
	Max int
	// Format writes the speeds under the boxes (SpeedFormatter of the unit when nil).
	Format func(bps float64) string
}

// ConnectionBoxes is one box per connection, in slot order, at most opts.Max: beyond that the
// last box is "+N" with the speed of the connections it stands for. Together is the sum of
// every connection's speed, which is also the sum of the boxes'.
func ConnectionBoxes(infos []*downexv1.ConnectionInfo, opts BoxOptions) ConnSummary {
	limit := MaxBoxes
	if opts.Max != 0 {
		limit = opts.Max
	}
	limit = max(2, limit)
	piece := opts.Piece
	if piece == "" {
		piece = "Fragment"
	}
	sorted := slices.Clone(infos)
	slices.SortStableFunc(sorted, func(a, b *downexv1.ConnectionInfo) int { return int(a.GetIndex()) - int(b.GetIndex()) })
	speedOf := func(c *downexv1.ConnectionInfo) float64 {
		if c.GetState() == downexv1.ConnectionState_CONNECTION_STATE_RECEIVING && c.GetSpeedBps() > 0 {
			return c.GetSpeedBps()
		}
		return 0
	}
	drawn := sorted
	if len(sorted) > limit {
		drawn = sorted[:limit-1]
	}
	rest := sorted[len(drawn):]
	restSpeed := 0.0
	for _, c := range rest {
		restSpeed += speedOf(c)
	}
	// the unit covers every label drawn, the +N box's sum too (never "5100" KB/s)
	speeds := make([]float64, 0, len(drawn)+1)
	for _, c := range drawn {
		speeds = append(speeds, speedOf(c))
	}
	unit := SpeedUnit(append(speeds, restSpeed))
	fmtSpeed := opts.Format
	if fmtSpeed == nil {
		fmtSpeed = SpeedFormatter(unit)
	}
	boxes := make([]ConnBox, 0, len(drawn)+1)
	for _, c := range drawn {
		speed := speedOf(c)
		tone := toneOf(c)
		what := ""
		if opts.Media && c.GetFragment() >= 0 {
			what = " · " + text.LowerJS(piece) + " " + strconv.Itoa(int(c.GetFragment())+1)
		}
		detail := ""
		if c.GetDetail() != "" {
			detail = " (" + c.GetDetail() + ")"
		}
		boxes = append(boxes, ConnBox{
			Key:      "c" + strconv.Itoa(int(c.GetIndex())),
			Fraction: rangeFraction(c),
			Tone:     tone,
			Speed:    speed,
			// a stream's boxes show their speeds as well (they add up to Together); the
			// fragment each connection is on is in the tooltip
			Label: fmtSpeed(speed),
			Title: "Connection " + strconv.Itoa(int(c.GetIndex())) + what + ": " + stateText[tone] + detail,
		})
	}
	if len(rest) > 0 {
		boxes = append(boxes, ConnBox{
			Key:   "more",
			Tone:  BoxIdle,
			Speed: restSpeed,
			Label: fmtSpeed(restSpeed),
			Title: strconv.Itoa(len(rest)) + " more connections",
			More:  len(rest),
		})
	}
	together := 0.0
	for _, b := range boxes {
		together += b.Speed
	}
	return ConnSummary{Boxes: boxes, Together: together, Unit: unit}
}
