package kit_test

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"testing"
	"time"

	"downex/ui/kit"
)

// The stubs implement every interface of the contracts outside the package, so the
// method sets compile there and need nothing unexported. They record what they are asked
// to do, which TestOneFrame checks.

type stubCanvas struct{ calls []string }

func (c *stubCanvas) log(s string)                                       { c.calls = append(c.calls, s) }
func (c *stubCanvas) FillRect(kit.Rect, kit.Color)                       { c.log("FillRect") }
func (c *stubCanvas) StrokeRect(kit.Rect, kit.Stroke, kit.Color)         { c.log("StrokeRect") }
func (c *stubCanvas) FillRoundedRect(kit.RRect, kit.Color)               { c.log("FillRoundedRect") }
func (c *stubCanvas) StrokeRoundedRect(kit.RRect, kit.Stroke, kit.Color) { c.log("StrokeRoundedRect") }
func (c *stubCanvas) FillEllipse(kit.Rect, kit.Color)                    { c.log("FillEllipse") }
func (c *stubCanvas) StrokeEllipse(kit.Rect, kit.Stroke, kit.Color)      { c.log("StrokeEllipse") }
func (c *stubCanvas) DrawLine(_, _ kit.Point, _ kit.Stroke, _ kit.Color) { c.log("DrawLine") }
func (c *stubCanvas) FillPath(kit.Path, kit.Color)                       { c.log("FillPath") }
func (c *stubCanvas) StrokePath(kit.Path, kit.Stroke, kit.Color)         { c.log("StrokePath") }
func (c *stubCanvas) FillGradient(kit.Rect, kit.LinearGradient)          { c.log("FillGradient") }
func (c *stubCanvas) DrawShadow(kit.RRect, kit.Shadow)                   { c.log("DrawShadow") }
func (c *stubCanvas) DrawText(kit.TextLayout, kit.Point, kit.Color)      { c.log("DrawText") }
func (c *stubCanvas) DrawImage(_ kit.Image, _, _ kit.Rect)               { c.log("DrawImage") }
func (c *stubCanvas) PushClip(kit.Rect)                                  { c.log("PushClip") }
func (c *stubCanvas) PushRoundedClip(kit.RRect)                          { c.log("PushRoundedClip") }
func (c *stubCanvas) PushOpacity(float32)                                { c.log("PushOpacity") }
func (c *stubCanvas) PushTransform(kit.Affine)                           { c.log("PushTransform") }
func (c *stubCanvas) Pop()                                               { c.log("Pop") }

type stubTarget struct {
	canvas stubCanvas
	lost   bool
}

func (t *stubTarget) BeginFrame(kit.Rect) (kit.Canvas, error) { return &t.canvas, nil }

func (t *stubTarget) EndFrame() error {
	if t.lost {
		return fmt.Errorf("EndDraw: HRESULT 0x8899000C: %w", kit.ErrDeviceLost)
	}
	return nil
}

type stubLayout struct{ text string }

func (l stubLayout) Metrics() kit.TextMetrics {
	return kit.TextMetrics{Width: 7 * float32(len(l.text)), Height: 18, Baseline: 14, Lines: 1}
}
func (l stubLayout) HitTest(kit.Point) kit.TextHit        { return kit.TextHit{} }
func (l stubLayout) Caret(int) (top kit.Point, h kit.DIP) { return kit.Point{}, 18 }
func (l stubLayout) AppendRangeRects(dst []kit.Rect, _, _ int) []kit.Rect {
	return append(dst, kit.R(0, 0, 7, 18))
}
func (l stubLayout) Release() {}

type stubPath struct{}

func (stubPath) Release() {}

type stubImage struct{}

func (stubImage) PixelSize() (w, h int) { return 96, 54 }
func (stubImage) Release()              {}

type stubResources struct{}

func (stubResources) LayoutText(text string, style kit.TextStyle, _ kit.TextOptions) (kit.TextLayout, error) {
	if !(style.Size > 0) {
		return nil, fmt.Errorf("font size %v: %w", style.Size, kit.ErrInvalid)
	}
	return stubLayout{text}, nil
}
func (stubResources) FontMetrics(kit.TextStyle) (kit.FontMetrics, error) {
	return kit.FontMetrics{LineHeight: 18}, nil
}
func (stubResources) NewPath(kit.PathData) (kit.Path, error)            { return stubPath{}, nil }
func (stubResources) NewSVGPath(string, kit.FillRule) (kit.Path, error) { return stubPath{}, nil }
func (stubResources) NewImage([]byte) (kit.Image, error)                { return stubImage{}, nil }

type stubHost struct {
	size    kit.Size
	invalid kit.Rect
	frames  int
	sem     []kit.SemanticEvent
}

func (h *stubHost) Environment() kit.Environment {
	return kit.Environment{Scale: kit.Scale{PixelsPerDIP: 1.25, Text: 1}}
}
func (h *stubHost) Size() kit.Size                      { return h.size }
func (h *stubHost) WindowState() kit.WindowState        { return kit.WindowState{} }
func (h *stubHost) Resources() kit.Resources            { return stubResources{} }
func (h *stubHost) Invalidate(r kit.Rect)               { h.invalid = h.invalid.Union(r) }
func (h *stubHost) RequestFrame()                       { h.frames++ }
func (h *stubHost) SetCursor(kit.Cursor)                {}
func (h *stubHost) CapturePointer()                     {}
func (h *stubHost) ReleasePointer()                     {}
func (h *stubHost) SetTextInput(kit.TextInput)          {}
func (h *stubHost) ClipboardText() (string, error)      { return "", kit.ErrUnsupported }
func (h *stubHost) SetClipboardText(string) error       { return kit.ErrUnsupported }
func (h *stubHost) SetTitle(string)                     {}
func (h *stubHost) SetMinSize(kit.Size)                 {}
func (h *stubHost) Resize(kit.Size)                     {}
func (h *stubHost) SetFrameColors(kit.FrameColors)      {}
func (h *stubHost) Command(kit.WindowCommand)           {}
func (h *stubHost) Close()                              {}
func (h *stubHost) Post(f func()) error                 { f(); return nil }
func (h *stubHost) NotifySemantics(e kit.SemanticEvent) { h.sem = append(h.sem, e) }

// stubRoot paints a card with a label, and keeps a tree of one button.
type stubRoot struct {
	host   kit.Host
	label  kit.TextLayout
	events []kit.Event
}

func (r *stubRoot) Attach(h kit.Host) {
	r.host = h
	r.label, _ = h.Resources().LayoutText("Downex", kit.TextStyle{Size: 13}, kit.TextOptions{})
}
func (r *stubRoot) Detach()         { r.label.Release() }
func (r *stubRoot) Frame(time.Time) {}
func (r *stubRoot) Event(e kit.Event) bool {
	r.events = append(r.events, e)
	k, ok := e.(kit.KeyEvent)
	return ok && k.Key == kit.KeyK && k.Mods == kit.ModCtrl
}
func (r *stubRoot) FrameHit(p kit.Point) kit.FramePart {
	if p.Y < 44 {
		return kit.FrameCaption
	}
	return kit.FrameClient
}
func (r *stubRoot) Semantics() kit.SemanticTree { return stubTree{} }
func (r *stubRoot) Paint(c kit.Canvas, dirty kit.Rect) {
	c.FillRect(dirty, kit.RGB(0xeef2f0))
	card := kit.RoundRect(kit.R(224, 44, 900, 568), 12)
	c.DrawShadow(card, kit.Shadow{Offset: kit.Pt(0, 1), Blur: 2, Color: kit.RGBA(16, 28, 22, 0.06)})
	c.PushRoundedClip(card)
	c.FillRoundedRect(card, kit.RGB(0xffffff))
	c.DrawText(r.label, kit.Pt(47, 13), kit.RGB(0x0f1613))
	c.Pop()
}

type stubTree struct{}

func (stubTree) Root() kit.NodeID { return 1 }
func (stubTree) Node(id kit.NodeID) (kit.SemanticNode, bool) {
	switch id {
	case 1:
		return kit.SemanticNode{ID: 1, Role: kit.RoleWindow, Name: "Downex"}, true
	case 2:
		return kit.SemanticNode{ID: 2, Parent: 1, Role: kit.RoleButton, Name: "Settings",
			States: kit.NodeFocusable, Actions: kit.ActionInvoke | kit.ActionFocus,
			Shortcut: "Ctrl+,"}, true
	}
	return kit.SemanticNode{}, false
}
func (stubTree) AppendChildren(dst []kit.NodeID, id kit.NodeID) []kit.NodeID {
	if id == 1 {
		return append(dst, 2)
	}
	return dst
}
func (stubTree) Focused() kit.NodeID                                         { return 0 }
func (stubTree) HitTest(kit.Point) kit.NodeID                                { return 1 }
func (stubTree) Cell(kit.NodeID, int, int) kit.NodeID                        { return 0 }
func (stubTree) AppendSelection(dst []kit.NodeID, _ kit.NodeID) []kit.NodeID { return dst }
func (stubTree) Perform(r kit.ActionRequest) error {
	n, ok := stubTree{}.Node(r.Node)
	switch {
	case !ok:
		return kit.ErrNoNode
	case n.States&kit.NodeDisabled != 0:
		return kit.ErrNotEnabled
	case n.Actions&r.Action == 0:
		return kit.ErrUnsupported
	}
	return nil
}

// stubFieldHost is a host with the optional capabilities: native text fields and a named
// renderer.
type stubFieldHost struct {
	stubHost
	fields []*stubField
}

func (h *stubFieldHost) Renderer() string { return "software" }

func (h *stubFieldHost) NewTextField(o kit.TextFieldOptions, cb kit.TextFieldCallbacks) (kit.NativeField, error) {
	f := &stubField{opts: o, cb: cb}
	h.fields = append(h.fields, f)
	return f, nil
}

type stubField struct {
	opts           kit.TextFieldOptions
	cb             kit.TextFieldCallbacks
	bounds         kit.Rect
	text           string
	focused, freed bool
}

func (f *stubField) SetBounds(r kit.Rect)              { f.bounds = r }
func (f *stubField) SetOptions(o kit.TextFieldOptions) { f.opts = o }
func (f *stubField) Text() string                      { return f.text }
func (f *stubField) SetText(s string)                  { f.text = s }
func (f *stubField) Focus()                            { f.focused = true }
func (f *stubField) Blur()                             { f.focused = false }
func (f *stubField) SelectAll()                        {}
func (f *stubField) Release()                          { f.freed = true }

var (
	_ kit.RenderTarget = (*stubTarget)(nil)
	_ kit.Canvas       = (*stubCanvas)(nil)
	_ kit.TextLayout   = stubLayout{}
	_ kit.TextMeasurer = stubResources{}
	_ kit.Resources    = stubResources{}
	_ kit.Path         = stubPath{}
	_ kit.Image        = stubImage{}
	_ kit.Host         = (*stubHost)(nil)
	_ kit.Root         = (*stubRoot)(nil)
	_ kit.SemanticTree = stubTree{}
	_ kit.Host         = (*stubFieldHost)(nil)
	_ kit.NativeFields = (*stubFieldHost)(nil)
	_ kit.RendererInfo = (*stubFieldHost)(nil)
	_ kit.NativeField  = (*stubField)(nil)
)

// The capabilities are found with a type assertion on the Host.
func TestCapabilities(t *testing.T) {
	var h kit.Host = &stubFieldHost{}
	if r, ok := h.(kit.RendererInfo); !ok || r.Renderer() != "software" {
		t.Error("RendererInfo not found on the host")
	}
	nf, ok := h.(kit.NativeFields)
	if !ok {
		t.Fatal("NativeFields not found on the host")
	}
	f, err := nf.NewTextField(kit.TextFieldOptions{Placeholder: "Search downloads"}, kit.TextFieldCallbacks{})
	if err != nil || f == nil {
		t.Fatalf("NewTextField = %v, %v", f, err)
	}
	if _, ok := kit.Host(&stubHost{}).(kit.NativeFields); ok {
		t.Error("a host without the capability reports it")
	}
}

// CSS's font-optical-sizing: auto sets 'opsz' to the resolved size, also with a larger text
// size; without it the face's own stays.
func TestFontAutoOptical(t *testing.T) {
	f := kit.Font{Size: kit.Rem(1.25), Weight: 650, AutoOptical: true}
	if got := f.Style(kit.Scale{Text: 1}).OpticalSize; got != 20 {
		t.Errorf("auto optical size at 100%% text = %v, want 20", got)
	}
	if got := f.Style(kit.Scale{Text: 1.25}).OpticalSize; got != 25 {
		t.Errorf("auto optical size at 125%% text = %v, want 25", got)
	}
	f.AutoOptical, f.OpticalSize = false, 0
	if got := f.Style(kit.Scale{}).OpticalSize; got != 0 {
		t.Errorf("optical size without auto = %v, want the face's own (0)", got)
	}
}

// The values that key caches and the ones a host diffs must stay comparable: these map
// types do not compile otherwise.
var (
	_ map[kit.TextStyle]bool
	_ map[kit.TextOptions]bool
	_ map[kit.SemanticNode]bool
	_ map[kit.Environment]bool
	_ map[kit.Affine]bool
)

// TestOneFrame drives a root through a host and a target as a backend does: attach,
// events, one frame, a device loss, the semantic tree and its actions.
func TestOneFrame(t *testing.T) {
	host := &stubHost{size: kit.Size{W: 900, H: 600}}
	var root kit.Root = &stubRoot{}
	root.Attach(host)

	if !root.Event(kit.KeyEvent{Action: kit.KeyDown, Key: kit.KeyK, Mods: kit.ModCtrl}) {
		t.Error("Ctrl+K was not used")
	}
	if root.Event(kit.KeyEvent{Action: kit.KeyDown, Key: kit.KeyF4, Mods: kit.ModAlt}) {
		t.Error("Alt+F4 was used; it must keep its system meaning")
	}
	if got := root.FrameHit(kit.Pt(400, 20)); got != kit.FrameCaption {
		t.Errorf("FrameHit in the title bar = %v, want FrameCaption", got)
	}

	target := &stubTarget{}
	dirty := kit.RectAt(kit.Point{}, host.Size())
	root.Frame(time.Now())
	c, err := target.BeginFrame(dirty)
	if err != nil {
		t.Fatal(err)
	}
	root.Paint(c, dirty)
	if err := target.EndFrame(); err != nil {
		t.Fatal(err)
	}
	want := []string{"FillRect", "DrawShadow", "PushRoundedClip", "FillRoundedRect", "DrawText", "Pop"}
	if !slices.Equal(target.canvas.calls, want) {
		t.Errorf("paint calls = %v, want %v", target.canvas.calls, want)
	}

	target.lost = true
	if err := target.EndFrame(); !errors.Is(err, kit.ErrDeviceLost) {
		t.Errorf("EndFrame after a loss = %v, want ErrDeviceLost", err)
	}

	tree := root.Semantics()
	kids := tree.AppendChildren(nil, tree.Root())
	if len(kids) != 1 {
		t.Fatalf("root children = %v", kids)
	}
	if n, ok := tree.Node(kids[0]); !ok || n.Role != kit.RoleButton || n.Parent != tree.Root() {
		t.Errorf("child = %+v, %v", n, ok)
	}
	for _, c := range []struct {
		r    kit.ActionRequest
		want error
	}{
		{kit.ActionRequest{Node: 2, Action: kit.ActionInvoke}, nil},
		{kit.ActionRequest{Node: 2, Action: kit.ActionToggle}, kit.ErrUnsupported},
		{kit.ActionRequest{Node: 9, Action: kit.ActionInvoke}, kit.ErrNoNode},
	} {
		if err := tree.Perform(c.r); !errors.Is(err, c.want) {
			t.Errorf("Perform(%+v) = %v, want %v", c.r, err, c.want)
		}
	}
	root.Detach()

	if _, err := (stubResources{}).LayoutText("x", kit.TextStyle{}, kit.TextOptions{}); !errors.Is(err, kit.ErrInvalid) {
		t.Errorf("a zero font size gave %v, want ErrInvalid", err)
	}
}

// TestEvents lists every event type: each is an Event.
func TestEvents(t *testing.T) {
	events := []kit.Event{
		kit.PointerEvent{Action: kit.PointerDown, Button: kit.ButtonLeft, Buttons: kit.ButtonLeft, Clicks: 2},
		kit.WheelEvent{Delta: kit.Pt(0, 100), Mode: kit.WheelLines},
		kit.KeyEvent{Action: kit.KeyUp, Key: kit.KeySpace},
		kit.TextEvent{Text: "é"},
		kit.FocusEvent{In: true},
		kit.SizeEvent{Size: kit.Size{W: 1200, H: 760}},
		kit.EnvironmentEvent{Env: kit.Environment{Dark: true}},
		kit.WindowStateEvent{State: kit.WindowState{Maximized: true}},
		kit.CloseEvent{},
	}
	seen := map[string]bool{}
	for _, e := range events {
		seen[fmt.Sprintf("%T", e)] = true
	}
	if len(seen) != len(events) {
		t.Errorf("event types listed twice: %v", seen)
	}
}

// TestKeys pins the key codes to the Windows virtual-key codes they promise.
func TestKeys(t *testing.T) {
	for _, c := range []struct {
		key  kit.Key
		want uint16
	}{
		{kit.Key0, '0'}, {kit.Key9, '9'}, {kit.KeyA, 'A'}, {kit.KeyK, 'K'}, {kit.KeyZ, 'Z'},
		{kit.KeyNumpad0, 0x60}, {kit.KeyNumpadDivide, 0x6F}, {kit.KeyF1, 0x70}, {kit.KeyF10, 0x79},
		{kit.KeyF24, 0x87}, {kit.KeyArrowDown, 0x28}, {kit.KeyContextMenu, 0x5D}, {kit.KeyComma, 0xBC},
	} {
		if uint16(c.key) != c.want {
			t.Errorf("key %#x, want %#x", uint16(c.key), c.want)
		}
	}
}

func TestRect(t *testing.T) {
	a, b := kit.R(0, 0, 10, 10), kit.R(5, 5, 20, 20)
	if got, want := a.Intersect(b), kit.R(5, 5, 10, 10); got != want {
		t.Errorf("Intersect = %v, want %v", got, want)
	}
	if got := a.Intersect(kit.R(10, 0, 20, 10)); got != (kit.Rect{}) {
		t.Errorf("rectangles that only touch share %v", got)
	}
	if got, want := a.Union(b), kit.R(0, 0, 20, 20); got != want {
		t.Errorf("Union = %v, want %v", got, want)
	}
	if got := a.Union(kit.Rect{}); got != a {
		t.Errorf("an empty rectangle added %v", got)
	}
	if !a.Contains(kit.Pt(0, 0)) || a.Contains(kit.Pt(10, 5)) {
		t.Error("Contains: the top-left edge is inside, the right edge outside")
	}
	nan := float32(math.NaN())
	if !kit.R(0, 0, nan, 10).Empty() {
		t.Error("a NaN rectangle is not empty")
	}
	if got, want := kit.R(0, 0, 100, 40).Inset(kit.Insets{Top: 1, Right: 2, Bottom: 3, Left: 4}), kit.R(4, 1, 98, 37); got != want {
		t.Errorf("Inset = %v, want %v", got, want)
	}
}

func TestRRect(t *testing.T) {
	// app.css .chip: a 999px pill radius on a 22 px tall chip is 11.
	pill := kit.RoundRect(kit.R(0, 0, 80, 22), 999).Normalized()
	if r, ok := pill.Uniform(); !ok || r != 11 {
		t.Errorf("pill radius = %v, %v; want 11", r, ok)
	}
	// Two 30 px radii along a 40 px side shrink every radius by 40/60.
	q := kit.RRect{Rect: kit.R(0, 0, 40, 100), Radii: kit.Radii{TopLeft: 30, TopRight: 30, BottomRight: 6, BottomLeft: -1}}.Normalized().Radii
	want := kit.Radii{TopLeft: 20, TopRight: 20, BottomRight: 4, BottomLeft: 0}
	if math.Abs(float64(q.TopLeft-want.TopLeft)) > 1e-4 || math.Abs(float64(q.BottomRight-want.BottomRight)) > 1e-4 ||
		q.TopRight != q.TopLeft || q.BottomLeft != 0 {
		t.Errorf("radii = %+v, want %+v", q, want)
	}
	// A 1 px border inside a box with radius 8 is stroked on the box inset by 0.5 with
	// radius 7.5; a selected row's square corners stay square.
	in := kit.RRect{Rect: kit.R(0, 0, 30, 30), Radii: kit.Radii{TopLeft: 8, BottomRight: 3}}.Inset(0.5)
	if in.Rect != kit.R(0.5, 0.5, 29.5, 29.5) || in.Radii != (kit.Radii{TopLeft: 7.5, BottomRight: 2.5}) {
		t.Errorf("Inset = %+v", in)
	}
	if out := kit.RoundRect(kit.R(0, 0, 10, 10), 8).Inset(-3); out.Rect != kit.R(-3, -3, 13, 13) || out.Radii != kit.UniformRadii(11) {
		t.Errorf("Inset(-3) = %+v", out)
	}
}

func TestAffine(t *testing.T) {
	var id kit.Affine
	if a, b, c, d, e, f := id.Elems(); a != 1 || b != 0 || c != 0 || d != 1 || e != 0 || f != 0 {
		t.Errorf("the zero Affine is %v %v %v %v %v %v, want the identity", a, b, c, d, e, f)
	}
	if kit.Translation(0, 0) != id || kit.Scaling(1, 1) != id || kit.AffineOf(1, 0, 0, 1, 0, 0) != id {
		t.Error("identities differ from the zero value")
	}
	// An icon on a 24-unit grid drawn 16 DIPs wide at (20, 30): scale first, then move.
	icon := kit.Scaling(16.0/24, 16.0/24).Then(kit.Translation(20, 30))
	if got := icon.Apply(kit.Pt(24, 12)); !near(got, kit.Pt(36, 38)) {
		t.Errorf("icon point = %v, want (36, 38)", got)
	}
	if got := kit.Rotation(90).Apply(kit.Pt(1, 0)); !near(got, kit.Pt(0, 1)) {
		t.Errorf("Rotation(90) moved (1, 0) to %v, want (0, 1): clockwise on the screen", got)
	}
	m := kit.Rotation(-11).Then(kit.Scaling(2, 3)).Then(kit.Translation(5, -7))
	inv, ok := m.Invert()
	if !ok {
		t.Fatal("not invertible")
	}
	if p := kit.Pt(13, 21); !near(inv.Apply(m.Apply(p)), p) {
		t.Errorf("Invert does not undo: %v", inv.Apply(m.Apply(p)))
	}
	if _, ok := kit.Scaling(0, 1).Invert(); ok {
		t.Error("a scaling by 0 inverted")
	}
}

func near(p, q kit.Point) bool {
	return math.Abs(float64(p.X-q.X)) < 1e-4 && math.Abs(float64(p.Y-q.Y)) < 1e-4
}

func TestColor(t *testing.T) {
	accent := kit.RGB(0x10804f) // app.css --accent
	if accent != (kit.Color{R: 16.0 / 255, G: 128.0 / 255, B: 79.0 / 255, A: 1}) {
		t.Errorf("RGB = %+v", accent)
	}
	if got := kit.RGBA(16, 128, 79, 0.12); got != accent.WithAlpha(0.12) { // --chart-fill
		t.Errorf("RGBA = %+v", got)
	}
	// .tag: color-mix(in srgb, var(--tag-hue) 11%, transparent) is the hue at 11%.
	hue := kit.RGB(0x6a45d4)
	if got, want := kit.Mix(hue, 0.11, kit.Color{}), hue.WithAlpha(0.11); !nearColor(got, want) {
		t.Errorf("Mix with transparent = %+v, want %+v", got, want)
	}
	// .btn.danger:hover: color-mix(in srgb, var(--danger) 88%, var(--ink)).
	danger, ink := kit.RGB(0xbf3328), kit.RGB(0x0f1613)
	want := kit.Color{
		R: danger.R*0.88 + ink.R*0.12, G: danger.G*0.88 + ink.G*0.12, B: danger.B*0.88 + ink.B*0.12, A: 1,
	}
	if got := kit.Mix(danger, 0.88, ink); !nearColor(got, want) {
		t.Errorf("Mix = %+v, want %+v", got, want)
	}
	if got := kit.Mix(kit.Color{}, 0.5, kit.Color{}); got != (kit.Color{}) {
		t.Errorf("Mix of transparents = %+v", got)
	}
}

func nearColor(a, b kit.Color) bool {
	d := max(abs(a.R-b.R), abs(a.G-b.G), abs(a.B-b.B), abs(a.A-b.A))
	return d < 1e-5
}

func abs(v float32) float32 { return float32(math.Abs(float64(v))) }

func TestScale(t *testing.T) {
	var zero kit.Scale
	if zero.RemBase() != 16 || zero.Pixels(10) != 10 || zero.Snap(0.4) != 0 {
		t.Errorf("the zero Scale does not act as 100%%: rem %v", zero.RemBase())
	}
	text := kit.Scale{PixelsPerDIP: 1.25, Text: 1.25}
	if got := text.RemBase(); got != 20 {
		t.Errorf("1rem at 125%% text = %v, want 20", got)
	}
	// app.css --details-h: 14.125rem is 226 DIPs at the default text size.
	if got := (kit.Scale{Text: 1}).Rem(14.125); got != 226 {
		t.Errorf("14.125rem = %v, want 226", got)
	}
	s125 := kit.Scale{PixelsPerDIP: 1.25}
	if got := s125.Pixels(44); got != 55 {
		t.Errorf("44 DIPs at 125%% = %v px, want 55", got)
	}
	if got := s125.FromPixels(55); got != 44 {
		t.Errorf("55 px at 125%% = %v DIPs, want 44", got)
	}
	if got := s125.Snap(0.5); got != 0.8 { // 0.625 px rounds to 1 px
		t.Errorf("Snap(0.5) = %v, want 0.8", got)
	}
	if got, want := s125.SnapRect(kit.R(0.3, 0.5, 10.1, 10.5)), kit.R(0.0, 0.8, 10.4, 10.4); got != want {
		t.Errorf("SnapRect = %v, want %v", got, want)
	}
	for _, c := range []struct {
		ppd, w, want float32
	}{
		{1, 1, 1}, {1.25, 1, 0.8}, {1.5, 1, 1 / 1.5}, {2, 1, 1}, {1, 0.2, 1}, {1.25, 2, 1.6}, {1, 0, 0}, {1, -1, 0},
	} {
		if got := (kit.Scale{PixelsPerDIP: c.ppd}).SnapWidth(c.w); math.Abs(float64(got-c.want)) > 1e-6 {
			t.Errorf("SnapWidth(%v) at %v = %v, want %v", c.w, c.ppd, got, c.want)
		}
	}
}

func TestPathData(t *testing.T) {
	var d kit.PathData
	d.MoveTo(kit.Pt(12, 5))
	d.LineTo(kit.Pt(12, 19))
	d.QuadTo(kit.Pt(1, 2), kit.Pt(3, 4))
	d.CubicTo(kit.Pt(1, 1), kit.Pt(2, 2), kit.Pt(3, 3))
	d.ArcTo(kit.Size{W: 3, H: 3}, 0, true, true, kit.Pt(12, 9))
	d.Close()
	verbs := make([]kit.PathVerb, len(d.Ops))
	for i, op := range d.Ops {
		verbs[i] = op.Verb
	}
	if want := []kit.PathVerb{kit.PathMove, kit.PathLine, kit.PathQuad, kit.PathCubic, kit.PathArc, kit.PathClose}; !slices.Equal(verbs, want) {
		t.Errorf("verbs = %v, want %v", verbs, want)
	}
	if arc := d.Ops[4]; arc.P[0] != kit.Pt(12, 9) || !arc.Large || !arc.Sweep || arc.Radii != (kit.Size{W: 3, H: 3}) {
		t.Errorf("arc = %+v", arc)
	}
	if d.Ops[2].P[1] != kit.Pt(3, 4) || d.Ops[3].P[2] != kit.Pt(3, 3) {
		t.Error("the end point is not after the control points")
	}
	d.Reset()
	if len(d.Ops) != 0 || cap(d.Ops) == 0 {
		t.Error("Reset did not empty the path and keep its storage")
	}
}
