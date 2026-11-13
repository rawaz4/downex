// Package segments is the geometry of progress bars (DESIGN-SYSTEM.md §3.13), ported from
// ui/frontend/src/lib/segments.ts and the module scripts of SegmentBar.svelte and
// SegmentMap.svelte. List rows draw RowCell: one solid fill of the overall progress. The
// progress window draws BarCells, a connection map of at most MaxCells equal byte spans of the
// file, each filled with the share of its bytes received: one cell for a download that is not
// running, and never more cells than the width and the received ink allow, so that every cell is
// at least MinCellPx wide and holds at least FillPxPerCell px of fill. The Append forms write into
// the caller's slice, so a frame that keeps its buffer allocates nothing.
package segments

import (
	"math"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/format"
)

// SegCell is one cell of a bar.
type SegCell struct {
	// Fraction is 0..1 of the span received (0 for an indeterminate cell).
	Fraction float64
	// Live: a connection's front lies in this span while the download runs (the cell gets a
	// static live edge). For an indeterminate cell: bytes are arriving (it is hatched).
	Live bool
	// Indeterminate: the size is unknown, the bar cannot show how much is there.
	Indeterminate bool
}

const (
	// MaxCells is the most cells a bar shows.
	MaxCells = 8
	// MinCellPx is the narrowest a cell is drawn, CellGapPx the gap between cells (px).
	MinCellPx = 12
	CellGapPx = 2
	// FillPxPerCell is the received ink each cell must at least have room for (px): never more
	// cells than 10 px chunks of fill.
	FillPxPerCell = 10
	// snapAt: a span received up to this share counts as complete (no sliver of track left).
	snapAt = 0.995
)

const (
	stateDownloading = downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING
	stateCompleted   = downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED
)

func isMedia(d *downexv1.Download) bool {
	return d.GetKind() == downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA
}

// cellCount is clamp(segments, 1, max(1, maxCells)).
func cellCount(segments, maxCells int) int {
	return min(max(segments, 1), max(1, maxCells))
}

// SegmentCells is AppendSegmentCells into a new slice.
func SegmentCells(d *downexv1.Download, maxCells int) []SegCell {
	return AppendSegmentCells(nil, d, maxCells)
}

// AppendSegmentCells appends at most maxCells equal byte spans of the file; each span's fill is
// its received bytes over its length.
//
//   - Media downloads and downloads without segments: one cell with the download's progress
//     (format.ProgressFraction, fragments for a stream); indeterminate when that is unknown.
//   - Unknown size (size <= 0): one indeterminate cell, live (hatched) while downloading with
//     bytes received.
//   - Otherwise n = clamp(segments, 1, maxCells) spans [k·size/n, (k+1)·size/n). A span
//     receives the overlap of every segment's received range [start, start + min(received,
//     len)) (an open end means size − 1). Shares from 0.995 snap to 1. A span is live while
//     the download runs and some unfinished segment's front start + received lies in it.
//
// A completed download's cells are full.
func AppendSegmentCells(dst []SegCell, d *downexv1.Download, maxCells int) []SegCell {
	downloading := d.GetState() == stateDownloading
	size := float64(d.GetSize())
	segs := d.GetSegments()
	if d.GetState() == stateCompleted {
		n := 1
		if !isMedia(d) && size > 0 {
			n = cellCount(len(segs), maxCells)
		}
		for range n {
			dst = append(dst, SegCell{Fraction: 1})
		}
		return dst
	}
	if isMedia(d) || len(segs) == 0 {
		return append(dst, fractionCell(format.ProgressFraction(d), downloading, d.GetReceived() > 0))
	}
	if !(size > 0) {
		return append(dst, SegCell{Live: downloading && d.GetReceived() > 0, Indeterminate: true})
	}

	n := cellCount(len(segs), maxCells)
	var gotBuf [MaxCells]float64
	var liveBuf [MaxCells]bool
	got, live := gotBuf[:0], liveBuf[:0]
	if n > MaxCells {
		got, live = make([]float64, 0, n), make([]bool, 0, n)
	}
	got, live = got[:n], live[:n]
	nf := float64(n)
	bound := func(k int) float64 { return float64(k) * size / nf }
	for _, s := range segs {
		start := float64(s.GetStart())
		end := float64(s.GetEnd())
		if end < 0 {
			end = size - 1
		} else {
			end = math.Min(end, size-1)
		}
		length := end - start + 1
		if !(length > 0) {
			continue
		}
		received := math.Min(math.Max(0, float64(s.GetReceived())), length)
		from, to := start, start+received
		if to > from {
			// Only the spans this range touches.
			first := max(0, int(math.Floor(from*nf/size)))
			last := min(n-1, int(math.Floor((to-1)*nf/size)))
			for k := first; k <= last; k++ {
				if overlap := math.Min(to, bound(k+1)) - math.Max(from, bound(k)); overlap > 0 {
					got[k] += overlap
				}
			}
		}
		if downloading && received < length {
			front := start + received
			k := min(n-1, max(0, int(math.Floor(front*nf/size))))
			live[k] = true
		}
	}
	for k := range n {
		dst = append(dst, SegCell{Fraction: snap(got[k] / (bound(k+1) - bound(k))), Live: live[k]})
	}
	return dst
}

// fractionCell is one cell of a download's progress f (NaN: indeterminate).
func fractionCell(f float64, downloading, received bool) SegCell {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return SegCell{Live: downloading && received, Indeterminate: true}
	}
	return SegCell{Fraction: snap(f), Live: downloading}
}

// RowCell is the one cell a list row draws (DESIGN-SYSTEM.md §3.13): the download's overall
// progress as a single solid fill, however many connections it has, so a row never reads as a
// dashed line. The connection map is the progress window's (BarCells) and the details pane's
// Parts boxes.
func RowCell(d *downexv1.Download) SegCell {
	if d.GetState() == stateCompleted {
		return SegCell{Fraction: 1}
	}
	return fractionCell(format.ProgressFraction(d), d.GetState() == stateDownloading, d.GetReceived() > 0)
}

// BarCells is AppendBarCells into a new slice.
func BarCells(d *downexv1.Download, width float64, maxCells int) []SegCell {
	return AppendBarCells(nil, d, width, maxCells)
}

// AppendBarCells appends the cells the progress window's bar draws (DESIGN-SYSTEM.md §3.13,
// owner's choice 2026-11-10): one solid fill of the overall progress, its RowCell, in every
// state and for every kind of download, as the list rows draw it. The per-connection view is
// the details' segment map. width and maxCells are kept for the callers and not used.
func AppendBarCells(dst []SegCell, d *downexv1.Download, width float64, maxCells int) []SegCell {
	return append(dst, RowCell(d))
}

// CellsForWidth is how many cells fit in width px with every cell at least MinCellPx wide (no
// width known: maxCells).
func CellsForWidth(width float64, maxCells int) int {
	if !(width > 0) {
		return max(1, maxCells)
	}
	fit := int(math.Floor((width + CellGapPx) / (MinCellPx + CellGapPx)))
	return min(max(fit, 1), max(1, maxCells))
}

func snap(f float64) float64 {
	if math.IsNaN(f) || math.IsInf(f, 0) || f <= 0 {
		return 0
	}
	if f >= snapAt {
		return 1
	}
	return f
}

// Tone is the fill colour of a bar (§3.12).
type Tone uint8

const (
	// ToneFill is Mint: live and done.
	ToneFill Tone = iota
	ToneIdle
	// TonePaused is amber, ToneFailed red.
	TonePaused
	ToneFailed
)

// String is the tone's name as today's window names its classes ("fill", "paused").
func (t Tone) String() string {
	switch t {
	case ToneFill:
		return "fill"
	case TonePaused:
		return "paused"
	case ToneFailed:
		return "failed"
	}
	return "idle"
}

// ToneOf is the fill colour of a state: live and done Mint, paused amber, failed red, waiting
// grey.
func ToneOf(state downexv1.DownloadState) Tone {
	switch state {
	case stateDownloading, downexv1.DownloadState_DOWNLOAD_STATE_PROBING, stateCompleted:
		return ToneFill
	case downexv1.DownloadState_DOWNLOAD_STATE_PAUSED:
		return TonePaused
	case downexv1.DownloadState_DOWNLOAD_STATE_FAILED:
		return ToneFailed
	}
	return ToneIdle
}
