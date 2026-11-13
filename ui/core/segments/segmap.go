package segments

import (
	"strconv"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/format"
)

// The progress window's byte-range map (SegmentMap.svelte): each segment's received part from
// its start offset, optionally with a tick at each start.

// Summary counts a download's segments: all of them, those still receiving, those done.
type Summary struct {
	Total, Active, Done int
}

func segmentLength(s *downexv1.Segment, size int64) int64 {
	end := s.GetEnd()
	if end < 0 {
		end = size - 1
	}
	return end - s.GetStart() + 1
}

// Summarize counts d's segments.
func Summarize(d *downexv1.Download) Summary {
	var out Summary
	size := d.GetSize()
	for _, s := range d.GetSegments() {
		if size > 0 && s.GetReceived() >= segmentLength(s, size) {
			out.Done++
		} else if d.GetState() == stateDownloading {
			out.Active++
		}
	}
	out.Total = len(d.GetSegments())
	return out
}

// MapLabel is the map's accessible name: "Segment map: 8 segments, 3 active, 42% received".
func MapLabel(d *downexv1.Download) string {
	if d.GetState() == stateCompleted {
		return "Segment map: download complete"
	}
	if d.GetSize() <= 0 {
		return "Segment map: size unknown, single connection"
	}
	s := Summarize(d)
	total := s.Total
	if total == 0 {
		total = 1
	}
	parts := strconv.Itoa(total) + " segment"
	if s.Total != 1 {
		parts += "s"
	}
	if s.Active > 0 {
		parts += ", " + strconv.Itoa(s.Active) + " active"
	}
	return "Segment map: " + parts + ", " + format.Percent(format.ProgressFraction(d)) + " received"
}

// Paint is the colour of a map shape, the token the view fills it with.
type Paint uint8

const (
	// PaintTrack is the bar's ground (--seg-track).
	PaintTrack Paint = iota
	// PaintDone is received bytes (--seg-fill), PaintFront a connection's front (--seg-edge).
	PaintDone
	PaintFront
	// PaintGap is the surface between segments (--surface), PaintMarker a start tick
	// (--seg-marker, else the front's colour).
	PaintGap
	PaintMarker
)

// ShapeKind is a rectangle, a hatch stroke or a marker's notch.
type ShapeKind uint8

const (
	// ShapeRect fills X, Y, W, H.
	ShapeRect ShapeKind = iota
	// ShapeStroke is a 3 px line from (X, Y) to (X+W, Y+H).
	ShapeStroke
	// ShapeNotch is the triangle (X, Y), (X+8, Y), (X+4, Y+5): a marker's notch.
	ShapeNotch
)

// Shape is one thing the map draws, in left-to-right coordinates from the map's top left;
// a right-to-left view mirrors x (x' = width − x − w).
type Shape struct {
	Kind       ShapeKind
	Paint      Paint
	X, Y, W, H float64
}

// HatchStroke is the width of the hatch lines of an unknown size.
const HatchStroke = 3

// AppendMapShapes appends what the map of d draws in width × height (DIPs), in painting order.
// With markers, each segment's start is marked with a tick and a notch (the progress window's
// "start positions" view) instead of a gap.
func AppendMapShapes(dst []Shape, d *downexv1.Download, width, height float64, markers bool) []Shape {
	dst = append(dst, Shape{Kind: ShapeRect, Paint: PaintTrack, W: width, H: height})
	if d.GetState() == stateCompleted {
		return append(dst, Shape{Kind: ShapeRect, Paint: PaintDone, W: width, H: height})
	}
	size := d.GetSize()
	if size <= 0 {
		// Unknown size: hatch the bar to show bytes are arriving without a known end.
		if d.GetReceived() <= 0 {
			return dst
		}
		for x := -height; x < width+height; x += 9 {
			dst = append(dst, Shape{Kind: ShapeStroke, Paint: PaintDone, X: x, Y: height, W: height, H: -height})
		}
		return dst
	}
	scale := width / float64(size)
	segs := d.GetSegments()
	// span is a segment's start, length and received bytes; a download without segments is one.
	span := func(i int) (start, length, received float64) {
		if len(segs) == 0 {
			return 0, float64(size), float64(d.GetReceived())
		}
		s := segs[i]
		return float64(s.GetStart()), float64(segmentLength(s, size)), float64(s.GetReceived())
	}
	count := max(len(segs), 1)
	for i := range count {
		start, length, received := span(i)
		x0 := start * scale
		x1 := (start + length) * scale
		xr := min(x1, x0+max(0, received)*scale)
		dst = append(dst, Shape{Kind: ShapeRect, Paint: PaintDone, X: x0, W: xr - x0, H: height})
		if d.GetState() == stateDownloading && received < length {
			dst = append(dst, Shape{Kind: ShapeRect, Paint: PaintFront, X: max(x0, xr-2), W: min(2, x1-x0), H: height})
		}
		if start > 0 && x1-x0 >= 4 && !markers {
			dst = append(dst, Shape{Kind: ShapeRect, Paint: PaintGap, X: x0 - 0.5, W: 1, H: height})
		}
	}
	if markers {
		for i := range count {
			start, _, _ := span(i)
			x := min(width-2, max(0, start*scale-1))
			dst = append(dst,
				Shape{Kind: ShapeRect, Paint: PaintMarker, X: x, W: 2, H: height},
				Shape{Kind: ShapeNotch, Paint: PaintMarker, X: x - 3})
		}
	}
	return dst
}
