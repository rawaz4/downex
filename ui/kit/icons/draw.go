package icons

import (
	"downex/ui/kit"
	"downex/ui/kit/tokens"
)

// Set draws icons with the paths it makes from one kit.Resources: each shape's path is made
// once, on first use, and kept (a Path is device-independent: it survives a lost device and
// draws on every canvas of its backend). Its owner releases it with the window.
type Set struct {
	res   kit.Resources
	paths map[string]kit.Path
	// brandKeys are the logo's bars' path data, made once per bar (DrawBrand runs in every
	// title-bar repaint, which must not allocate).
	brandKeys map[BrandBar]string
	err       error
}

// NewSet makes an empty set that makes its paths with res.
func NewSet(res kit.Resources) *Set { return &Set{res: res, paths: map[string]kit.Path{}} }

// Err is the first failure to make a path, or nil; what failed is not drawn.
func (s *Set) Err() error { return s.err }

// Len is the number of paths made.
func (s *Set) Len() int { return len(s.paths) }

// Release frees every path. The set can be used again afterwards; it makes them anew.
func (s *Set) Release() {
	for k, p := range s.paths {
		if p != nil { // path data that failed is kept as nil (see path)
			p.Release()
		}
		delete(s.paths, k)
	}
}

func (s *Set) path(d string) kit.Path {
	if p, ok := s.paths[d]; ok {
		return p
	}
	if s.res == nil {
		return nil
	}
	p, err := s.res.NewSVGPath(d, kit.FillNonZero)
	if err != nil {
		if s.err == nil {
			s.err = err
		}
		s.paths[d] = nil // do not try again every frame
		return nil
	}
	s.paths[d] = p
	return p
}

// DrawIcon draws the line icon n as Icon.svelte does at size pixels (its width and height),
// its box's top-left corner at at, in col (currentColor): stroked with round caps and joins,
// filled too for the filled ones.
func (s *Set) DrawIcon(c kit.Canvas, n Name, at kit.Point, size kit.DIP, col kit.Color) {
	if n >= numIcons || col.A <= 0 {
		return
	}
	ic := &lineIcons[n]
	p := s.path(ic.d)
	if p == nil {
		return
	}
	k := size / 24
	c.PushTransform(kit.Scaling(k, k).Then(kit.Translation(at.X, at.Y)))
	if ic.filled {
		c.FillPath(p, col)
	}
	c.StrokePath(p, kit.Stroke{Width: ic.stroke, Cap: kit.CapRound, Join: kit.JoinRound}, col)
	c.Pop()
}

// DrawTile draws the file tile of kind k as FileIcon.svelte does at size pixels, its box's
// top-left corner at at, in the theme's colours for the kind. At 20 px and below the kinds
// that have one draw their small variant.
func (s *Set) DrawTile(c kit.Canvas, k TileKind, at kit.Point, size kit.DIP, t *tokens.Theme) {
	if k >= numTiles {
		k = TileOther
	}
	tl := &tiles[k]
	shapes := tl.shapes
	if size <= smallTile && tl.small != nil {
		shapes = tl.small
	}
	paintOf := func(p paint) kit.Color {
		switch p {
		case paintHue:
			return tl.hue(t)
		case paintDeep:
			return tl.deep(t)
		case paintGlyph:
			return t.TileGlyph
		}
		return kit.Color{}
	}
	scale := size / tileBox[2]
	c.PushTransform(kit.Translation(-tileBox[0], -tileBox[1]).Then(kit.Scaling(scale, scale)).Then(kit.Translation(at.X, at.Y)))
	for i := range shapes {
		sh := &shapes[i]
		var col, stroke kit.Color
		if sh.fill != paintNone {
			col = paintOf(sh.fill)
			if sh.fillOpacity > 0 {
				col.A *= sh.fillOpacity
			}
		}
		if sh.stroke != paintNone {
			stroke = paintOf(sh.stroke)
		}
		s.drawShape(c, sh, col, stroke)
	}
	c.Pop()
}

// drawShape fills (when fill is not transparent) and strokes (when stroke is not) one shape
// in the coordinates in effect, inside its own rotation and group opacity.
func (s *Set) drawShape(c kit.Canvas, sh *shape, fill, stroke kit.Color) {
	p := s.path(sh.d)
	if p == nil {
		return
	}
	pushes := 0
	if r := sh.rotate; r[0] != 0 {
		c.PushTransform(kit.Translation(-r[1], -r[2]).Then(kit.Rotation(r[0])).Then(kit.Translation(r[1], r[2])))
		pushes++
	}
	if sh.opacity > 0 && sh.opacity < 1 {
		c.PushOpacity(sh.opacity) // SVG's opacity applies to fill and stroke together
		pushes++
	}
	if fill.A > 0 {
		c.FillPath(p, fill)
	}
	if stroke.A > 0 {
		st := kit.Stroke{Width: sh.width}
		if sh.roundCap {
			st.Cap = kit.CapRound
		}
		if sh.roundJoin {
			st.Join = kit.JoinRound
		}
		c.StrokePath(p, st, stroke)
	}
	for ; pushes > 0; pushes-- {
		c.Pop()
	}
}

// DrawMark draws the status mark of kind k as StatusMark.svelte does at size pixels, its
// box's top-left corner at at: in the kind's colour, with the glyph on its tone in --tone-on
// (--accent-on for downloading); in forced colours in CanvasText with the glyph in Canvas.
func (s *Set) DrawMark(c kit.Canvas, k MarkKind, at kit.Point, size kit.DIP, t *tokens.Theme) {
	var m *mark
	switch {
	case k < numMarks:
		m = &marks[k]
	case k == MarkCheckWarn:
		m = &checkWarnMark
	default:
		return
	}
	scale := size / 14
	c.PushTransform(kit.Scaling(scale, scale).Then(kit.Translation(at.X, at.Y)))
	for i := range m.shapes {
		ms := &m.shapes[i]
		var fill, stroke kit.Color
		if ms.fill != nil {
			fill = markPaint(t, ms.fill, ms.onTone)
		}
		if ms.stroke != nil {
			stroke = markPaint(t, ms.stroke, ms.onTone)
		}
		s.drawShape(c, &ms.shape, fill, stroke)
	}
	c.Pop()
}

// markPaint is a mark's paint in the theme: StatusMark's forced-colours rule paints the mark
// in CanvasText and the glyph on its tone in Canvas, which the forced theme's ink and
// chrome are.
func markPaint(t *tokens.Theme, f func(*tokens.Theme) kit.Color, onTone bool) kit.Color {
	if !t.IsForced() {
		return f(t)
	}
	name := markForced
	if onTone {
		name = markForcedOn
	}
	if name == "Canvas" {
		return t.Chrome
	}
	return t.Ink // CanvasText
}

// MarkCheckWarn is the alert mark in the warn tone: a completed or running download whose
// file check says it may not be the wanted file (CAUTION, docs/DESKTOP-SPEC.md §9.7). It is
// MarkFailed's diamond and "!" in --warn; MarkFailed itself is the DANGER alert.
const MarkCheckWarn = numMarks

// checkWarnMark is MarkCheckWarn's drawing, made from MarkFailed's.
var checkWarnMark = func() mark {
	warn := func(t *tokens.Theme) kit.Color { return t.Warn }
	m := mark{color: warn}
	for _, ms := range marks[MarkFailed].shapes {
		if !ms.onTone {
			if ms.fill != nil {
				ms.fill = warn
			}
			if ms.stroke != nil {
				ms.stroke = warn
			}
		}
		m.shapes = append(m.shapes, ms)
	}
	return m
}()

// MarkOf is the mark of a download state as StatusMark's markOfState maps them.
func MarkOf(state string) MarkKind {
	switch state {
	case "downloading", "probing":
		return MarkDownloading
	case "paused":
		return MarkPaused
	case "failed":
		return MarkFailed
	case "completed":
		return MarkCompleted
	case "awaiting_confirmation":
		return MarkAwaiting
	case "canceled":
		return MarkCanceled
	}
	return MarkQueued
}
