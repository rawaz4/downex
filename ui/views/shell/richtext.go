package shell

import (
	"strings"

	"downex/ui/kit"
	"downex/ui/views"
)

// Run is a piece of a paragraph in its own font and ink (a <code> or <b> inside a sentence).
type Run struct {
	Text string
	Font kit.Font
	Ink  views.Ink
}

// richText is a paragraph of runs in several fonts that wraps at spaces as inline text does:
// each word is a label of its run's font, words are placed one after the other on lines as
// wide as the box, and each line's words share its baseline. Its text content is the runs'
// text.
type richText struct {
	kit.Element
	runs  []Run
	words []*richWord
	// lineHeight is the paragraph's line box (CSS line-height of the paragraph's font): the
	// first run's font sets it.
	text string
}

type richWord struct {
	*views.Text
	space bool // ends with a space: the next word may go on the next line
}

func newRichText(runs ...Run) *richText {
	r := &richText{}
	r.Semantics.Role = kit.RoleText
	r.set(runs)
	return r
}

// set replaces the runs.
func (r *richText) set(runs []Run) {
	var b strings.Builder
	for _, run := range runs {
		b.WriteString(run.Text)
	}
	if b.String() == r.text && len(runs) == len(r.runs) {
		return
	}
	r.runs, r.text = runs, b.String()
	r.RemoveAll()
	r.words = r.words[:0]
	for _, run := range runs {
		s := run.Text
		for s != "" {
			i := strings.IndexByte(s, ' ')
			var w string
			if i < 0 {
				w, s = s, ""
			} else {
				w, s = s[:i+1], s[i+1:]
			}
			t := views.NewText(w, run.Font, run.Ink, kit.TextOptions{})
			t.Semantics.Role = kit.RoleNone
			t.PassThrough = true
			rw := &richWord{Text: t, space: strings.HasSuffix(w, " ")}
			r.words = append(r.words, rw)
			r.Add(t)
		}
	}
	r.Semantics.Name = r.text
	r.SemanticsChanged()
	r.Relayout()
}

// Text is the paragraph's text.
func (r *richText) Text() string { return r.text }

// TextContent is the paragraph's text (tests read it as textContent).
func (r *richText) TextContent() string { return r.text }

// ContentWidths is kit.Layouter's: the widest word, and the whole paragraph on one line
// (each word's trailing space counted, the last one's hanging).
func (r *richText) ContentWidths(ctx *kit.LayoutContext) (lo, hi kit.DIP) {
	var pen kit.DIP
	for _, w := range r.words {
		_, ww := ctx.IntrinsicWidths(w.Text)
		lo = max(lo, ww)
		hi = max(hi, pen+ww)
		pen += ctx.InlineAdvance(w.Text)
	}
	return lo, hi
}

// ContentHeight is kit.Layouter's.
func (r *richText) ContentHeight(ctx *kit.LayoutContext, w kit.DIP) kit.DIP {
	return r.flow(ctx, kit.Rect{Max: kit.Point{X: w}}, false)
}

// LayoutContent is kit.Layouter's.
func (r *richText) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) { r.flow(ctx, box, true) }

// flow lays the words out in lines and returns the height they take. A word goes on the
// line while its text fits (its trailing space may hang past the end, as inline text's
// does), and the next word starts after its trailing space (kit's InlineAdvance).
func (r *richText) flow(ctx *kit.LayoutContext, box kit.Rect, place bool) kit.DIP {
	width := box.Width()
	y := box.Min.Y
	start := 0
	for start < len(r.words) {
		// Fill a line.
		end := start
		var x kit.DIP
		for end < len(r.words) {
			_, ww := ctx.IntrinsicWidths(r.words[end].Text)
			// A line breaks only at a space: a word glued to the one before it (",", a
			// <code> run's end) stays on its line.
			if end > start && r.words[end-1].space && x+ww > width+0.01 {
				break
			}
			x += ctx.InlineAdvance(r.words[end].Text)
			end++
		}
		// The line's height and baseline.
		var asc, desc kit.DIP
		for i := start; i < end; i++ {
			w := r.words[i]
			_, ww := ctx.IntrinsicWidths(w.Text)
			h := ctx.HeightFor(w.Text, ww, width)
			base := h * 0.8
			if place {
				ctx.Place(w.Text, kit.RectAt(kit.Pt(0, 0), kit.Size{W: ww, H: h}), width)
				if b, ok := w.Label.ContentBaseline(); ok {
					base = b
				}
			}
			asc = max(asc, base)
			desc = max(desc, h-base)
		}
		if place {
			x := box.Min.X
			for i := start; i < end; i++ {
				w := r.words[i]
				base := w.Size().H * 0.8
				if b, ok := w.Label.ContentBaseline(); ok {
					base = b
				}
				ctx.Move(w.Text, kit.Pt(x, y+asc-base))
				x += ctx.InlineAdvance(w.Text)
			}
		}
		y += asc + desc
		start = end
	}
	return y - box.Min.Y
}
