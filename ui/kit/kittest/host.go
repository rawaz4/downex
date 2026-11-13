package kittest

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"sync"
	"time"

	"downex/ui/kit"
)

// Host is a kit.Host without a window. It records what the root asks of it; its fields
// are what a test sets (size, environment) and reads.
type Host struct {
	SizeV  kit.Size
	Env    kit.Environment
	State  kit.WindowState
	Res    *Resources
	Cursor kit.Cursor

	Title       string
	MinSize     kit.Size
	Resized     []kit.Size
	FrameColors []kit.FrameColors
	Commands    []kit.WindowCommand
	TextInputs  []kit.TextInput
	Semantics   []kit.SemanticEvent
	Clipboard   string
	// Closed reports a Close. Close writes it under the lock Post reads it under, since
	// Post may be called from any goroutine; read it from the test's goroutine, or IsClosed.
	Closed   bool
	Captured bool
	// Fields are the native text fields the root made (kit.NativeFields).
	Fields []*Field

	// Invalid is the union of the areas asked to be painted since the last paint;
	// Invalidations lists each of them (a test may clear it).
	Invalid       kit.Rect
	Invalidations []kit.Rect
	// FrameWanted reports a RequestFrame not yet used up; FrameRequests counts them.
	FrameWanted   bool
	FrameRequests int
	// Quiet keeps the host from listing Invalidations, Semantics and TextInputs (a long
	// run whose memory is measured); SemanticCount still counts the events.
	Quiet         bool
	SemanticCount int

	mu     sync.Mutex
	posted []func()
}

// NewHost makes a host of a window size and environment, with fresh resources.
func NewHost(size kit.Size, env kit.Environment) *Host {
	return &Host{SizeV: size, Env: env, Res: NewResources()}
}

var _ kit.Host = (*Host)(nil)

func (h *Host) Environment() kit.Environment     { return h.Env }
func (h *Host) Size() kit.Size                   { return h.SizeV }
func (h *Host) WindowState() kit.WindowState     { return h.State }
func (h *Host) Resources() kit.Resources         { return h.Res }
func (h *Host) SetCursor(c kit.Cursor)           { h.Cursor = c }
func (h *Host) CapturePointer()                  { h.Captured = true }
func (h *Host) ReleasePointer()                  { h.Captured = false }
func (h *Host) SetTitle(t string)                { h.Title = t }
func (h *Host) SetMinSize(s kit.Size)            { h.MinSize = s }
func (h *Host) Resize(s kit.Size)                { h.Resized = append(h.Resized, s) }
func (h *Host) SetFrameColors(c kit.FrameColors) { h.FrameColors = append(h.FrameColors, c) }
func (h *Host) Command(c kit.WindowCommand)      { h.Commands = append(h.Commands, c) }

// Close is kit.Host's.
func (h *Host) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.Closed = true
}

// IsClosed reports Closed under the lock: for any goroutine.
func (h *Host) IsClosed() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.Closed
}

// Invalidate is kit.Host's.
func (h *Host) Invalidate(r kit.Rect) {
	if r.Empty() {
		return
	}
	h.Invalid = h.Invalid.Union(r)
	if !h.Quiet {
		h.Invalidations = append(h.Invalidations, r)
	}
}

// SetTextInput is kit.Host's.
func (h *Host) SetTextInput(t kit.TextInput) {
	if !h.Quiet {
		h.TextInputs = append(h.TextInputs, t)
	}
}

// RequestFrame is kit.Host's.
func (h *Host) RequestFrame() {
	h.FrameWanted = true
	h.FrameRequests++
}

// ClipboardText is kit.Host's.
func (h *Host) ClipboardText() (string, error) {
	if h.IsClosed() {
		return "", kit.ErrClosed
	}
	return h.Clipboard, nil
}

// SetClipboardText is kit.Host's.
func (h *Host) SetClipboardText(s string) error {
	if h.IsClosed() {
		return kit.ErrClosed
	}
	h.Clipboard = s
	return nil
}

// Post is kit.Host's: f waits until RunPosted. Any goroutine may call it.
func (h *Host) Post(f func()) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.Closed {
		return kit.ErrClosed
	}
	h.posted = append(h.posted, f)
	return nil
}

// NotifySemantics is kit.Host's.
func (h *Host) NotifySemantics(e kit.SemanticEvent) {
	h.SemanticCount++
	if !h.Quiet {
		h.Semantics = append(h.Semantics, e)
	}
}

// RunPosted runs the functions posted so far, in order, and reports how many ran.
func (h *Host) RunPosted() int {
	n := 0
	for {
		h.mu.Lock()
		fs := h.posted
		h.posted = nil
		h.mu.Unlock()
		if len(fs) == 0 {
			return n
		}
		for _, f := range fs {
			f()
			n++
		}
	}
}

// Pending reports whether functions are posted and not run.
func (h *Host) Pending() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.posted) > 0
}

// ---- resources ------------------------------------------------------------------------------

// Resources are kit.Resources without a graphics device: the fake Measurer, and paths and
// images that only check their input and count what is alive.
type Resources struct {
	Measurer
	PathsMade, PathsLive   int
	ImagesMade, ImagesLive int
}

// NewResources makes resources.
func NewResources() *Resources { return &Resources{} }

var _ kit.Resources = (*Resources)(nil)

// Path is a fake path.
type Path struct {
	Data     kit.PathData
	SVG      string
	r        *Resources
	released bool
}

// Release is kit.Path's; it is idempotent.
func (p *Path) Release() {
	if !p.released {
		p.released = true
		p.r.PathsLive--
	}
}

// Released reports whether the path was released.
func (p *Path) Released() bool { return p.released }

// NewPath is kit.Resources'.
func (r *Resources) NewPath(d kit.PathData) (kit.Path, error) {
	for i, op := range d.Ops {
		for _, pt := range op.P {
			if !fin(pt.X) || !fin(pt.Y) {
				return nil, fmt.Errorf("kittest: path op %d: point not finite: %w", i, kit.ErrInvalid)
			}
		}
	}
	r.PathsMade++
	r.PathsLive++
	return &Path{Data: d, r: r}, nil
}

// NewSVGPath is kit.Resources'; it takes any non-empty data.
func (r *Resources) NewSVGPath(d string, _ kit.FillRule) (kit.Path, error) {
	if d == "" {
		return nil, fmt.Errorf("kittest: empty path data at offset 0: %w", kit.ErrInvalid)
	}
	r.PathsMade++
	r.PathsLive++
	return &Path{SVG: d, r: r}, nil
}

// Image is a fake image.
type Image struct {
	W, H     int
	r        *Resources
	released bool
}

// PixelSize is kit.Image's.
func (i *Image) PixelSize() (int, int) { return i.W, i.H }

// Release is kit.Image's.
func (i *Image) Release() {
	if !i.released {
		i.released = true
		i.r.ImagesLive--
	}
}

// NewImage is kit.Resources': any data of 4 bytes or more is a 96×54 image.
func (r *Resources) NewImage(b []byte) (kit.Image, error) {
	if len(b) < 4 {
		return nil, fmt.Errorf("kittest: image data too short: %w", kit.ErrInvalid)
	}
	r.ImagesMade++
	r.ImagesLive++
	return &Image{W: 96, H: 54, r: r}, nil
}

func fin(v float32) bool { return !math.IsNaN(float64(v)) && !math.IsInf(float64(v), 0) }

// ---- canvas ---------------------------------------------------------------------------------

// Op is one drawing call a Canvas recorded.
type Op struct {
	Kind   string
	Rect   kit.Rect
	RRect  kit.RRect
	Color  kit.Color
	Stroke kit.Stroke
	Text   string // DrawText: the layout's text
	At     kit.Point
	Depth  int // the push depth it was drawn at
}

// Canvas is a kit.Canvas that records its calls and checks that pushes and pops balance.
type Canvas struct {
	Ops      []Op
	depth    int
	MaxDepth int
	// Unbalanced counts Pops without a Push.
	Unbalanced int
	// Quiet keeps the calls from being listed (a long run whose memory is measured);
	// Calls still counts them.
	Quiet bool
	Calls int
}

var _ kit.Canvas = (*Canvas)(nil)

func (c *Canvas) op(o Op) {
	c.Calls++
	if c.Quiet {
		return
	}
	o.Depth = c.depth
	c.Ops = append(c.Ops, o)
}

func (c *Canvas) FillRect(r kit.Rect, col kit.Color) { c.op(Op{Kind: "FillRect", Rect: r, Color: col}) }
func (c *Canvas) StrokeRect(r kit.Rect, s kit.Stroke, col kit.Color) {
	c.op(Op{Kind: "StrokeRect", Rect: r, Stroke: s, Color: col})
}
func (c *Canvas) FillRoundedRect(r kit.RRect, col kit.Color) {
	c.op(Op{Kind: "FillRoundedRect", Rect: r.Rect, RRect: r, Color: col})
}
func (c *Canvas) StrokeRoundedRect(r kit.RRect, s kit.Stroke, col kit.Color) {
	c.op(Op{Kind: "StrokeRoundedRect", Rect: r.Rect, RRect: r, Stroke: s, Color: col})
}
func (c *Canvas) FillEllipse(r kit.Rect, col kit.Color) {
	c.op(Op{Kind: "FillEllipse", Rect: r, Color: col})
}
func (c *Canvas) StrokeEllipse(r kit.Rect, s kit.Stroke, col kit.Color) {
	c.op(Op{Kind: "StrokeEllipse", Rect: r, Stroke: s, Color: col})
}
func (c *Canvas) DrawLine(a, b kit.Point, s kit.Stroke, col kit.Color) {
	c.op(Op{Kind: "DrawLine", Rect: kit.Rect{Min: a, Max: b}, Stroke: s, Color: col})
}
func (c *Canvas) FillPath(_ kit.Path, col kit.Color) { c.op(Op{Kind: "FillPath", Color: col}) }
func (c *Canvas) StrokePath(_ kit.Path, s kit.Stroke, col kit.Color) {
	c.op(Op{Kind: "StrokePath", Stroke: s, Color: col})
}
func (c *Canvas) FillGradient(r kit.Rect, _ kit.LinearGradient) {
	c.op(Op{Kind: "FillGradient", Rect: r})
}
func (c *Canvas) DrawShadow(r kit.RRect, s kit.Shadow) {
	c.op(Op{Kind: "DrawShadow", Rect: r.Rect, RRect: r, Color: s.Color})
}
func (c *Canvas) DrawText(l kit.TextLayout, at kit.Point, col kit.Color) {
	o := Op{Kind: "DrawText", At: at, Color: col}
	if fl, ok := l.(*Layout); ok {
		o.Text = fl.Text
		if !fl.released {
			m := fl.metrics
			o.Rect = kit.RectAt(at, kit.Size{W: m.Width, H: m.Height})
		}
	}
	c.op(o)
}
func (c *Canvas) DrawImage(_ kit.Image, _, dst kit.Rect) { c.op(Op{Kind: "DrawImage", Rect: dst}) }
func (c *Canvas) PushClip(r kit.Rect)                    { c.push(Op{Kind: "PushClip", Rect: r}) }
func (c *Canvas) PushRoundedClip(r kit.RRect) {
	c.push(Op{Kind: "PushRoundedClip", Rect: r.Rect, RRect: r})
}
func (c *Canvas) PushOpacity(float32)      { c.push(Op{Kind: "PushOpacity"}) }
func (c *Canvas) PushTransform(kit.Affine) { c.push(Op{Kind: "PushTransform"}) }

func (c *Canvas) push(o Op) {
	c.op(o)
	c.depth++
	c.MaxDepth = max(c.MaxDepth, c.depth)
}

// Pop is kit.Canvas's.
func (c *Canvas) Pop() {
	if c.depth == 0 {
		c.Unbalanced++
		return
	}
	c.depth--
	c.op(Op{Kind: "Pop"})
}

// Depth is the number of pushes still open.
func (c *Canvas) Depth() int { return c.depth }

// Reset forgets the recorded calls.
func (c *Canvas) Reset() { c.Ops, c.depth, c.MaxDepth, c.Unbalanced = nil, 0, 0, 0 }

// Count is the number of recorded calls of a kind.
func (c *Canvas) Count(kind string) int {
	n := 0
	for _, o := range c.Ops {
		if o.Kind == kind {
			n++
		}
	}
	return n
}

// Texts are the texts drawn, in order.
func (c *Canvas) Texts() []string {
	var out []string
	for _, o := range c.Ops {
		if o.Kind == "DrawText" {
			out = append(out, o.Text)
		}
	}
	return out
}

// ---- clock and timers --------------------------------------------------------------------

// Clock is a fake clock that also runs the kit's timers (a kit.Scheduler): their
// functions run on the goroutine that advances it, in deadline order.
type Clock struct {
	mu     sync.Mutex
	now    time.Time
	seq    int
	timers []*clockTimer
}

type clockTimer struct {
	at   time.Time
	seq  int
	f    func()
	dead bool
}

// NewClock makes a clock at a fixed time.
func NewClock() *Clock { return &Clock{now: time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)} }

// Now is the clock's time.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// AfterFunc is kit.Scheduler's.
func (c *Clock) AfterFunc(d time.Duration, f func()) func() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	t := &clockTimer{at: c.now.Add(d), seq: c.seq, f: f}
	c.timers = append(c.timers, t)
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		was := !t.dead
		t.dead = true
		return was
	}
}

// Timers is the number of timers waiting.
func (c *Clock) Timers() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, t := range c.timers {
		if !t.dead {
			n++
		}
	}
	return n
}

// Advance moves the clock on by d, running each timer that falls due on the way at its
// time.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	end := c.now.Add(d)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		sort.SliceStable(c.timers, func(i, j int) bool {
			if !c.timers[i].at.Equal(c.timers[j].at) {
				return c.timers[i].at.Before(c.timers[j].at)
			}
			return c.timers[i].seq < c.timers[j].seq
		})
		c.timers = slices.DeleteFunc(c.timers, func(t *clockTimer) bool { return t.dead })
		if len(c.timers) == 0 || c.timers[0].at.After(end) {
			c.now = end
			c.mu.Unlock()
			return
		}
		t := c.timers[0]
		t.dead = true
		c.now = t.at
		c.mu.Unlock()
		t.f()
	}
}

// ---- the loop -------------------------------------------------------------------------------

// Loop drives a root as a backend's message loop does: posted functions first, then at
// each display refresh a Frame when one was asked for or an area is invalid, and a Paint
// of the invalid area rounded out to device pixels.
type Loop struct {
	Host   *Host
	Root   kit.Root
	Canvas *Canvas
	Clock  *Clock
	// Refresh is the display's refresh interval (60 Hz).
	Refresh time.Duration
	// Frames and Paints count what the loop did; Painted is the last dirty area.
	Frames, Paints int
	Painted        kit.Rect
}

// NewLoop attaches root to a new host of size and environment and returns the loop
// that drives it. The root should take the loop's clock (kit.WindowOptions Now and
// Scheduler).
func NewLoop(root kit.Root, clock *Clock, size kit.Size, env kit.Environment) *Loop {
	if clock == nil {
		clock = NewClock()
	}
	l := &Loop{Host: NewHost(size, env), Root: root, Canvas: &Canvas{}, Clock: clock, Refresh: time.Second / 60}
	root.Attach(l.Host)
	return l
}

// Send gives the root an event and runs what it posted; it reports whether the root used
// the event.
func (l *Loop) Send(e kit.Event) bool {
	used := l.Root.Event(e)
	l.Host.RunPosted()
	return used
}

// Step is one display refresh.
func (l *Loop) Step() {
	l.Host.RunPosted()
	h := l.Host
	if h.FrameWanted || !h.Invalid.Empty() {
		h.FrameWanted = false
		l.Frames++
		l.Root.Frame(l.Clock.Now())
		if !h.Invalid.Empty() {
			dirty := roundOut(h.Invalid, h.Env.Scale)
			h.Invalid = kit.Rect{}
			l.Paints++
			l.Painted = dirty
			l.Root.Paint(l.Canvas, dirty)
		}
	}
	l.Clock.Advance(l.Refresh)
	l.Host.RunPosted()
}

// Run steps the loop for d of display refreshes.
func (l *Loop) Run(d time.Duration) {
	for end := l.Clock.Now().Add(d); l.Clock.Now().Before(end); {
		l.Step()
	}
}

// Settle steps until nothing is asked for (at most max refreshes) and reports whether the
// loop went idle.
func (l *Loop) Settle(max int) bool {
	for range max {
		if l.Idle() {
			return true
		}
		l.Step()
	}
	return l.Idle()
}

// Idle reports whether the root asked for nothing: no frame, no invalid area, nothing
// posted.
func (l *Loop) Idle() bool {
	return !l.Host.FrameWanted && l.Host.Invalid.Empty() && !l.Host.Pending()
}

// roundOut is r rounded out to whole device pixels, as a host hands dirty areas to Paint.
func roundOut(r kit.Rect, s kit.Scale) kit.Rect {
	p := s.PixelsPerDIP
	if p <= 0 {
		p = 1
	}
	f := func(v float32, up bool) float32 {
		x := float64(v * p)
		if up {
			x = math.Ceil(x - 1e-4)
		} else {
			x = math.Floor(x + 1e-4)
		}
		return float32(x) / p
	}
	return kit.R(f(r.Min.X, false), f(r.Min.Y, false), f(r.Max.X, true), f(r.Max.Y, true))
}
