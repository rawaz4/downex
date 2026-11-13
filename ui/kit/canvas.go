package kit

// A RenderTarget is a surface that frames are drawn onto: a window's render target, or an
// offscreen bitmap for screenshot tests. A window's host drives its own target (it calls
// Root.Paint between BeginFrame and EndFrame); whoever holds another target drives it the
// same way.
type RenderTarget interface {
	// BeginFrame starts a frame that redraws dirty (window DIPs) rounded out to whole
	// device pixels, and returns the canvas that draws it: clipped to that rounded area,
	// with the identity transform. What the area showed before is undefined, so the frame
	// paints all of it (a host passes the rounded area to Root.Paint); outside it the target
	// keeps the previous frame. BeginFrame makes the device when there is none (after a
	// loss too: hardware first, then the software renderer) and fails only when none can be
	// made.
	BeginFrame(dirty Rect) (Canvas, error)
	// EndFrame finishes the frame begun by BeginFrame and shows it; the canvas is invalid
	// afterwards. An error that wraps ErrDeviceLost means the device was lost or reset:
	// nothing was shown, the target has dropped its device resources (the next BeginFrame
	// makes new ones) and no longer holds a previous frame, so the caller repaints the
	// whole target. Pushes still open are popped and reported with an error that wraps
	// ErrInvalid; the frame is shown all the same.
	EndFrame() error
}

// Canvas draws one frame (see RenderTarget) in window DIPs, through the current transform and
// inside the current clip. Shapes are anti-aliased and drawn exactly where they are given:
// the kit snaps edges to device pixels with Scale where it wants them sharp. Every colour
// is blended over what is already drawn (source-over). Coordinates that are not finite,
// and resources that are released or come from another backend, draw nothing.
//
// The Push methods start a state that lasts until the matching Pop. They nest, and Pop
// ends the most recent one whatever its kind, so clips, layers and transforms always
// close in order.
type Canvas interface {
	// FillRect fills r with c.
	FillRect(r Rect, c Color)
	// StrokeRect strokes the outline of r, centred on its edges as SVG strokes are: a CSS
	// border of width w inside box b is
	// StrokeRect(b.Inset(UniformInsets(w/2)), Stroke{Width: w}, c).
	StrokeRect(r Rect, s Stroke, c Color)
	// FillRoundedRect fills r with c.
	FillRoundedRect(r RRect, c Color)
	// StrokeRoundedRect strokes the outline of r, centred on it: a CSS border of width w
	// inside the rounded box b is StrokeRoundedRect(b.Inset(w/2), Stroke{Width: w}, c).
	StrokeRoundedRect(r RRect, s Stroke, c Color)
	// FillEllipse fills the ellipse inscribed in r (a circle when r is a square).
	FillEllipse(r Rect, c Color)
	// StrokeEllipse strokes the ellipse inscribed in r, centred on it.
	StrokeEllipse(r Rect, s Stroke, c Color)
	// DrawLine strokes the segment from a to b.
	DrawLine(a, b Point, s Stroke, c Color)
	// FillPath fills p by its fill rule.
	FillPath(p Path, c Color)
	// StrokePath strokes p.
	StrokePath(p Path, s Stroke, c Color)
	// FillGradient fills r with g.
	FillGradient(r Rect, g LinearGradient)
	// DrawShadow draws the CSS box-shadow s of the rounded box r (see Shadow).
	DrawShadow(r RRect, s Shadow)
	// DrawText draws l with the top-left corner of its box at origin and its text in c;
	// colour glyphs (emoji) keep their own colours. The layout's Antialias decides
	// between subpixel and grayscale antialiasing for this text alone.
	DrawText(l TextLayout, origin Point, c Color)
	// DrawImage draws the part src of img (in the image's pixels, (0, 0) at its top-left;
	// an empty src is the whole image) scaled into dst, smoothly interpolated.
	DrawImage(img Image, src, dst Rect)
	// PushClip clips what follows to r in current coordinates (to the bounds of r on the
	// screen when the transform rotates), within the clip already in effect.
	PushClip(r Rect)
	// PushRoundedClip clips what follows to r, anti-aliased, within the clip already in
	// effect.
	PushRoundedClip(r RRect)
	// PushOpacity draws what follows into a layer that is blended at alpha (0 to 1) when
	// popped, as CSS opacity. Text inside a layer may be antialiased in grayscale.
	PushOpacity(alpha float32)
	// PushTransform makes m.Then(t) the transform, where t is the one in effect: m
	// applies to what follows in its own coordinates, as a CSS transform on a child does.
	PushTransform(m Affine)
	// Pop ends the most recent Push. A Pop without a Push is ignored, and EndFrame
	// reports it.
	Pop()
}

// BackdropFilter is a Canvas that can blur what is already drawn, as CSS backdrop-filter:
// blur(Radius) does under an element. A canvas that cannot (a recorder, a target without
// Direct2D 1.1) does not implement it, and the element's backdrop stays sharp, as a
// browser without backdrop-filter shows it.
type BackdropFilter interface {
	// BlurBackdrop blurs what is drawn inside r (current coordinates; its bounds on the
	// screen when the transform rotates) with a Gaussian of standard deviation radius, CSS
	// blur()'s length, sampling only inside r and the clip, and blends the blurred copy
	// over it at alpha (0 to 1), the element's opacity. It draws nothing while a layer
	// (PushOpacity, a rounded clip) is open.
	BlurBackdrop(r Rect, radius DIP, alpha float32)
}

// Stroke is how a line is drawn: SVG's stroke-width, stroke-linecap, stroke-linejoin and
// stroke-dasharray. Widths and dashes are lengths in the current coordinates, so a
// transform that scales scales them too (SVG's default, which the icons' 24-unit grid
// relies on). Miter joins use SVG's default miter limit, 4.
type Stroke struct {
	Width DIP
	Cap   LineCap
	Join  LineJoin
	Dash  Dash
}

// LineCap is how the ends of a stroke, and of each of its dashes, are drawn.
type LineCap uint8

// The line caps.
const (
	CapButt   LineCap = iota // square, ending at the end point (SVG's default)
	CapRound                 // a half circle beyond the end point (the icons)
	CapSquare                // a half square beyond the end point
)

// LineJoin is how a stroke turns its corners.
type LineJoin uint8

// The line joins.
const (
	JoinMiter LineJoin = iota // pointed (SVG's default)
	JoinRound                 // round (the icons)
	JoinBevel                 // cut off
)

// Dash makes a stroke dashed: dashes On long and gaps Off long from the start of each
// figure, each dash ended by the stroke's cap (SVG stroke-dasharray "On Off": the speed
// chart's grid is 3 3; with round caps, 1 1 draws dots). The zero value is a solid line.
type Dash struct {
	On, Off DIP
}

// Shadow is one CSS box-shadow of a rounded box. A list of shadows (app.css --shadow-md,
// --primary-lift) is drawn in reverse, since CSS paints the first one on top.
//
// An outer shadow is the box grown by Spread (its radii grown as CSS Backgrounds 3 §7.2
// grows them), moved by Offset and blurred by Blur; it shows only outside the box, so it
// never darkens a translucent fill. An inset shadow shows only inside the box: the inner
// edge of the box shrunk by Spread, moved by Offset and blurred by Blur. A Blur of 0
// gives a sharp edge (app.css's focus spreads, 0 0 0 1px and 0 0 0 4px).
type Shadow struct {
	// Offset is CSS's offset-x and offset-y.
	Offset Point
	// Blur is CSS's blur radius: a Gaussian blur whose standard deviation is Blur/2.
	Blur DIP
	// Spread is CSS's spread distance (negative shrinks).
	Spread DIP
	Color  Color
	// Inset makes it CSS's inset shadow.
	Inset bool
}

// LinearGradient paints colours that change along a line, as CSS linear-gradient and
// repeating-linear-gradient do: the fades at a scrolled pane's edge, the probing hatch.
type LinearGradient struct {
	// Start and End are the gradient line in current coordinates: offset 0 is at Start
	// and 1 at End, and the colour is the same along each line perpendicular to it.
	Start, End Point
	// Stops are the colours at offsets along the line, in increasing Offset; two stops at
	// one offset make a hard edge. Between stops the colour is interpolated in
	// premultiplied sRGB, as CSS interpolates.
	Stops []GradientStop
	// Repeat repeats the stops from Start to End along the whole line (CSS
	// repeating-linear-gradient); without it the first and last colours carry on beyond
	// the ends.
	Repeat bool
}

// GradientStop is one colour of a LinearGradient.
type GradientStop struct {
	Offset float32
	Color  Color
}

// Path is a device-independent geometry, made by Resources once (an icon) or for each
// change (a chart's line) and drawn by any canvas of its backend through any transform.
// Its owner releases it.
type Path interface {
	// Release frees the path. It is idempotent; a released path draws nothing.
	Release()
}

// FillRule decides which areas a path encloses, as SVG's fill-rule.
type FillRule uint8

// The fill rules.
const (
	FillNonZero FillRule = iota // SVG's default
	FillEvenOdd
)

// PathData is a path built in code (a chart's line and area, the spinner's arc) for
// Resources.NewPath: figures of lines, curves and arcs in absolute coordinates, the
// commands SVG path data has once its relative and shorthand forms are resolved. Build it
// with its methods or fill Ops directly.
//
// A figure starts at a PathMove. A segment that follows no PathMove starts a figure
// where the previous segment ended ((0, 0) at the very start), and one that follows a
// PathClose starts a figure at the closed figure's start, as in SVG. Strokes draw open
// figures open; fills close every figure.
type PathData struct {
	Fill FillRule
	Ops  []PathOp
}

// PathVerb is the command of a PathOp.
type PathVerb uint8

// The path commands.
const (
	// PathMove starts a figure at P[0].
	PathMove PathVerb = iota + 1
	// PathLine is a straight line to P[0].
	PathLine
	// PathQuad is a quadratic Bézier curve through the control point P[0] to P[1].
	PathQuad
	// PathCubic is a cubic Bézier curve through the control points P[0] and P[1] to P[2].
	PathCubic
	// PathArc is an elliptical arc to P[0], as SVG's A command: on the ellipse of Radii
	// whose x axis is turned by Rotation degrees, the arc of more than 180° when Large,
	// drawn towards increasing angles (clockwise on the screen) when Sweep. Radii too
	// small to reach P[0] are scaled up and a zero radius makes a straight line, as SVG's
	// implementation notes say.
	PathArc
	// PathClose draws a straight line back to the figure's start and closes the figure.
	PathClose
)

// PathOp is one command of a PathData.
type PathOp struct {
	Verb PathVerb
	// P are the command's points: the end point for PathMove, PathLine and PathArc; the
	// control point, then the end point for PathQuad; both control points, then the end
	// point for PathCubic.
	P [3]Point
	// Radii, Rotation (degrees), Large and Sweep are a PathArc's ellipse and flags.
	Radii    Size
	Rotation float32
	Large    bool
	Sweep    bool
}

// MoveTo starts a figure at p.
func (d *PathData) MoveTo(p Point) { d.Ops = append(d.Ops, PathOp{Verb: PathMove, P: [3]Point{p}}) }

// LineTo adds a straight line to p.
func (d *PathData) LineTo(p Point) { d.Ops = append(d.Ops, PathOp{Verb: PathLine, P: [3]Point{p}}) }

// QuadTo adds a quadratic Bézier curve through the control point c to p.
func (d *PathData) QuadTo(c, p Point) {
	d.Ops = append(d.Ops, PathOp{Verb: PathQuad, P: [3]Point{c, p}})
}

// CubicTo adds a cubic Bézier curve through the control points c1 and c2 to p.
func (d *PathData) CubicTo(c1, c2, p Point) {
	d.Ops = append(d.Ops, PathOp{Verb: PathCubic, P: [3]Point{c1, c2, p}})
}

// ArcTo adds an elliptical arc to p, as SVG's A command (see PathArc).
func (d *PathData) ArcTo(radii Size, rotation float32, large, sweep bool, p Point) {
	d.Ops = append(d.Ops, PathOp{
		Verb: PathArc, P: [3]Point{p}, Radii: radii, Rotation: rotation, Large: large, Sweep: sweep,
	})
}

// Close closes the figure with a straight line back to its start.
func (d *PathData) Close() { d.Ops = append(d.Ops, PathOp{Verb: PathClose}) }

// Reset empties d for reuse, keeping its fill rule and its storage.
func (d *PathData) Reset() { d.Ops = d.Ops[:0] }

// Image is a decoded bitmap made by Resources (a video's poster). It is
// device-independent like the other resources: the backend makes the device's copy when
// a canvas first draws it, and again after a device loss. Its owner releases it.
type Image interface {
	// PixelSize is the image's width and height in pixels.
	PixelSize() (w, h int)
	// Release frees the image. It is idempotent; a released image draws nothing.
	Release()
}

// Resources makes the device-independent resources the kit draws with. A backend has one
// for the whole process, which every window shares (Host.Resources); tests can make one
// without a window. It is not safe for concurrent use: it and everything it makes belong
// to the UI thread (without a window, to one goroutine at a time).
type Resources interface {
	TextMeasurer
	// NewPath makes the path d describes. Points that are not finite fail with an error
	// that wraps ErrInvalid.
	NewPath(d PathData) (Path, error)
	// NewSVGPath makes a path of SVG path data, the d attribute of <path> (the icons of
	// Icon.svelte, FileIcon.svelte and StatusMark.svelte), filled by rule. It reads SVG 2's
	// path grammar: the commands M, L, H, V, C, S, Q, T, A and Z, absolute and relative,
	// repeated without their letter, numbers written without separators (".75.75",
	// "1-2", "1e-3") and arc flags written without them ("a1 1 0 01 2 2"). Malformed data
	// fails with an error that wraps ErrInvalid and gives the offset of the fault.
	NewSVGPath(d string, rule FillRule) (Path, error)
	// NewImage decodes a PNG, JPEG, GIF or BMP image, or a WebP one where the system has
	// that codec. An unknown format fails with an error that wraps ErrUnsupported, broken
	// data with one that wraps ErrInvalid.
	NewImage(encoded []byte) (Image, error)
}
