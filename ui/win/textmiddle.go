//go:build windows

package win

import (
	"downex/ui/kit"
	"downex/ui/win/internal/dwrite"
)

// ellipsisWidth is the advance of "…" in f, letter spacing included.
func (r *Resources) ellipsisWidth(f *textFormat) float32 {
	if f.ellWOK {
		return f.ellW
	}
	e := &textLayout{res: r}
	u := [2]uint16{0x2026, 0}
	if e.build(f, u[:], 1, kit.TextOptions{}, kit.DirLTR) == nil {
		f.ellW = e.m.Advance
	}
	e.release()
	f.ellWOK = true
	return f.ellW
}

// cutMiddle replaces the middle of a one-line layout that is wider than its box with "…",
// keeping whole clusters from both ends so that the ellipsis sits near the middle
// (kit.TruncateMiddle: addresses and paths). Whitespace next to the cut goes with it.
func (l *textLayout) cutMiddle(f *textFormat, opts kit.TextOptions, dir kit.Direction) error {
	r := l.res
	type cluster struct {
		w  float32
		n  int
		ws bool
	}
	src := l.clusterMetrics()
	cls := make([]cluster, len(src))
	for i, c := range src {
		cls[i] = cluster{c.Width, int(c.Length), c.Flags&dwrite.ClusterWhitespace != 0}
	}
	avail := opts.MaxWidth - r.ellipsisWidth(f)
	head, tail := 0, len(cls) // kept: cls[:head] and cls[tail:]
	var hw, tw float32
fill:
	for head < tail {
		// Add to the shorter side what still fits, else to the other side, else stop.
		switch {
		case hw <= tw && hw+tw+cls[head].w <= avail:
			hw += cls[head].w
			head++
		case hw+tw+cls[tail-1].w <= avail:
			tail--
			tw += cls[tail].w
		case hw+tw+cls[head].w <= avail:
			hw += cls[head].w
			head++
		default:
			break fill
		}
	}
	if head == tail { // everything fits after all
		return nil
	}
	opts.Truncate = kit.TruncateNone // the shown text fits, or is cut again below
	for try := 0; try < 8; try++ {
		h, t := head, tail
		for h > 0 && cls[h-1].ws {
			h--
		}
		for t < len(cls) && cls[t].ws {
			t++
		}
		from, to := 0, l.n16
		for _, c := range cls[:h] {
			from += c.n
		}
		for _, c := range cls[t:] {
			to -= c.n
		}
		orig, n := r.utf16Of(l.text)
		shown := make([]uint16, 0, from+1+n-to+1)
		shown = append(shown, orig[:from]...)
		shown = append(shown, 0x2026)
		shown = append(shown, orig[to:n]...)
		ns := len(shown)
		shown = append(shown, 0)
		if err := l.build(f, shown, ns, opts, dir); err != nil {
			return err
		}
		l.cut, l.cutFrom, l.cutTo, l.shownN16 = true, from, to, ns
		l.m.Truncated = true
		if l.m.Advance <= opts.MaxWidth+0.01 || (head == 0 && tail == len(cls)) {
			return nil
		}
		// Shaping across the cut made it wider than the clusters added up to: drop one more
		// cluster from the longer side.
		if hw >= tw && head > 0 {
			head--
			hw -= cls[head].w
		} else if tail < len(cls) {
			tw -= cls[tail].w
			tail++
		} else if head > 0 {
			head--
		}
	}
	return nil
}
