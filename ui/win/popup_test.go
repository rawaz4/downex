//go:build windows

package win

import (
	stdimage "image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"downex/ui/kit"
	"downex/ui/kit/hostcap"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/win/internal/w32"
)

var (
	tpGetWindowLongPtr = user32.NewProc("GetWindowLongPtrW")
	tpGetWindow        = user32.NewProc("GetWindow")
)

func windowLong(h w32.HWND, index int32) uintptr {
	r, _, _ := tpGetWindowLongPtr.Call(uintptr(h), uintptr(index))
	return r
}

// widgetWindow is a real window (not activated) showing widgets in a theme.
type widgetWindow struct {
	a     *App
	w     *Window
	kw    *kit.Window
	scope *widgets.Scope
}

func openWidgets(t *testing.T, class string, dark bool, size kit.Size, content ...kit.Widget) *widgetWindow {
	t.Helper()
	runtimeLock(t)
	a, err := NewApp(AppOptions{ClassName: class})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	choice := tokens.ChoiceLight
	if dark {
		choice = tokens.ChoiceDark
	}
	theme := &widgets.Choice{Choice: choice}
	scope := widgets.NewScope(theme, content...)
	scope.Style = kit.Style{Display: kit.DisplayBlock, Padding: kit.EdgesAll(kit.Px(16)), RowGap: kit.Px(12)}
	kw := kit.NewWindow(scope, kit.WindowOptions{Title: "Downex widget test", Palette: func(env kit.Environment) kit.Palette { return theme.Tokens(env).Palette() }})
	mon := w32.MonitorFromPoint(w32.Point{}, w32.MONITOR_DEFAULTTOPRIMARY)
	mi, _ := w32.GetMonitorInfo(mon)
	w, err := a.NewWindow(kw, WindowOptions{Size: size, NoActivate: true, Placed: true, At: w32.Point{X: mi.Work.Left + 80, Y: mi.Work.Top + 80}})
	if err != nil {
		t.Fatal(err)
	}
	w.overWindow = func() bool { return false }
	pump(t, a, w, "the first frame", 2*time.Second, func() bool { return w.painted })
	return &widgetWindow{a: a, w: w, kw: kw, scope: scope}
}

// popup is the window's only open popup.
func (ww *widgetWindow) popup(t *testing.T) *popupWindow {
	t.Helper()
	ps := popupsOf[ww.w]
	if len(ps) != 1 {
		t.Fatalf("%d popups open", len(ps))
	}
	return ps[0]
}

// dibAt is the popup's premultiplied BGRA pixel at (x, y).
func (p *popupWindow) dibAt(x, y int) color.RGBA {
	i := (y*int(p.px.X) + x) * 4
	b := unsafe.Slice((*byte)(p.bits), int(p.px.X)*int(p.px.Y)*4)
	return color.RGBA{R: b[i+2], G: b[i+1], B: b[i], A: b[i+3]}
}

// image is the popup's pixels over a ground (what the screen shows under it).
func (p *popupWindow) image(ground color.RGBA) *stdimage.RGBA {
	img := stdimage.NewRGBA(stdimage.Rect(0, 0, int(p.px.X), int(p.px.Y)))
	for y := 0; y < int(p.px.Y); y++ {
		for x := 0; x < int(p.px.X); x++ {
			c := p.dibAt(x, y)
			k := 255 - int(c.A)
			img.SetRGBA(x, y, color.RGBA{
				R: uint8(int(c.R) + int(ground.R)*k/255), G: uint8(int(c.G) + int(ground.G)*k/255),
				B: uint8(int(c.B) + int(ground.B)*k/255), A: 255,
			})
		}
	}
	return img
}

// clickPopup clicks a popup at a point of its DIPs, as the system sends a click.
func clickPopup(p *popupWindow, at kit.Point) {
	lp := lparam(int(at.X*p.ppd+0.5), int(at.Y*p.ppd+0.5))
	w32.SendMessage(p.hwnd, popWMMouseMove, 0, lp)
	w32.SendMessage(p.hwnd, popWMLButtonDown, 1, lp)
	w32.SendMessage(p.hwnd, popWMLButtonUp, 0, lp)
}

func nodesOf(w *kit.Window, role kit.Role) []kit.SemanticNode {
	tree := w.Semantics()
	var out []kit.SemanticNode
	var walk func(id kit.NodeID)
	walk = func(id kit.NodeID) {
		n, ok := tree.Node(id)
		if !ok {
			return
		}
		if n.Role == role {
			out = append(out, n)
		}
		for _, c := range tree.AppendChildren(nil, id) {
			walk(c)
		}
	}
	walk(tree.Root())
	return out
}

// TestPopupMenuWindow opens a menu in a real popup window: a layered, owned, never activated
// WS_POPUP window placed under its trigger with the shadow's room around the box, drawn with
// per-pixel alpha; the owner hands it its keys; a click in it chooses an item; a press outside
// it and the owner losing the activation dismiss it.
func TestPopupMenuWindow(t *testing.T) {
	var selected []string
	trigger := widgets.NewButton("Unlimited", "gauge", widgets.Secondary, widgets.SizeMd, nil)
	mb := widgets.NewMenuButton(trigger, "Global speed limit", []widgets.MenuItem{
		{ID: "0", Label: "Unlimited", Kind: widgets.ItemRadio, Checked: true, Heading: "Global speed limit"},
		{ID: "20", Label: "20 MiB/s", Kind: widgets.ItemRadio},
		{ID: "10", Label: "10 MiB/s", Kind: widgets.ItemRadio},
		{ID: "custom", Label: "Custom…", Separated: true},
	}, func(id string) { selected = append(selected, id) })
	mb.Footnote = "Per-download and queue limits still apply."
	ww := openWidgets(t, "DownexPopupTest", false, kit.Size{W: 480, H: 420}, mb)
	a, w := ww.a, ww.w
	fg := getForegroundWindow()

	w.call(func() { ww.kw.Focus(trigger); mb.OpenMenu(widgets.FocusFirst) })
	pump(t, a, w, "the menu's first frame", 2*time.Second, func() bool { ps := popupsOf[w]; return len(ps) == 1 && ps[0].paints > 0 })
	p := ww.popup(t)
	settle(t, a, w, p)
	ex := windowLong(p.hwnd, -20) // GWL_EXSTYLE
	if ex&popWSExLayered == 0 || ex&w32.WS_EX_NOACTIVATE == 0 || ex&popWSExToolWin == 0 {
		t.Errorf("extended styles %#x", ex)
	}
	if owner, _, _ := tpGetWindow.Call(uintptr(p.hwnd), 4); w32.HWND(owner) != w.hwnd { // GW_OWNER
		t.Errorf("owner %#x, want the window %#x", owner, w.hwnd)
	}
	if got := getForegroundWindow(); got != fg {
		t.Errorf("the popup took the activation: foreground %#x, was %#x", got, fg)
	}
	// placed 4 px under the trigger, the shadow's room around the box
	m := mb.Menu()
	box := m.Bounds()
	tb := trigger.Base().WindowBounds()
	if box.Min.Y != tb.Max.Y+4 {
		t.Errorf("menu box at %v under a trigger at %v", box, tb)
	}
	wr := w32.GetWindowRect(p.hwnd)
	boxScreen := w32.ClientToScreen(w.hwnd, w32.Point{X: int32(box.Min.X*p.ppd + 0.5), Y: int32(box.Min.Y*p.ppd + 0.5)})
	if dx, dy := boxScreen.X-wr.Left, boxScreen.Y-wr.Top; dx < 60 || dy < 30 {
		t.Errorf("the box is %d, %d px inside the popup: no room for --shadow-lg", dx, dy)
	}
	// per-pixel alpha: the shadow's room is (nearly) transparent, the box opaque --raised
	if c := p.dibAt(0, 0); c.A > 8 {
		t.Errorf("the popup's corner is %v: not transparent", c)
	}
	in := p.dibAt(int(boxScreen.X-wr.Left)+8, int(boxScreen.Y-wr.Top)+4)
	if in.A != 255 || in.R != 0xff || in.G != 0xff || in.B != 0xff {
		t.Errorf("inside the box %v, want opaque #ffffff (--raised)", in)
	}
	// a shadow below the box: some alpha, not opaque
	below := p.dibAt(int(boxScreen.X-wr.Left)+40, int(p.px.Y)-int(float32(popupShadowBelow)*p.ppd))
	t.Logf("shadow pixel below the box: %v", below)

	// the owner hands the menu its keys
	if m.Active() != "Unlimited" {
		t.Fatalf("first focus %q", m.Active())
	}
	postKey(w.hwnd, uintptr(kit.KeyArrowDown))
	pump(t, a, w, "ArrowDown in the menu", time.Second, func() bool { return m.Active() == "20 MiB/s" })

	// a click in the popup chooses an item: the menu closes, then the choice is told
	var item kit.SemanticNode
	for _, n := range nodesOf(m.Root(), kit.RoleMenuItemRadio) {
		if n.Name == "10 MiB/s" {
			item = n
		}
	}
	clickPopup(p, item.Bounds.Center())
	pump(t, a, w, "the choice", time.Second, func() bool { return len(selected) == 1 })
	if selected[0] != "10" || mb.IsOpen() || len(popupsOf[w]) != 0 || isWindow(p.hwnd) {
		t.Errorf("selected %q, open %v, popups %d, window alive %v", selected, mb.IsOpen(), len(popupsOf[w]), isWindow(p.hwnd))
	}
	if !trigger.Base().Focused() {
		t.Error("the focus did not go back to the trigger")
	}

	// a press outside (the hook sees it before it is dispatched) dismisses it
	w.call(func() { mb.OpenMenu(widgets.FocusFirst) })
	pump(t, a, w, "the menu again", time.Second, func() bool { return len(popupsOf[w]) == 1 })
	outside := w32.ClientToScreen(w.hwnd, w32.Point{X: int32(400 * w.ppd()), Y: int32(400 * w.ppd())})
	a.onMouse(popWMLButtonDown, &popMouseHook{Pt: outside, Hwnd: w.hwnd, HitTest: popHTClient})
	pump(t, a, w, "the outside press", time.Second, func() bool { return !mb.IsOpen() && len(popupsOf[w]) == 0 })
	// a press on the trigger itself is the trigger's
	w.call(func() { mb.OpenMenu(widgets.FocusFirst) })
	pump(t, a, w, "the menu again", time.Second, func() bool { return len(popupsOf[w]) == 1 })
	c := tb.Center()
	onTrigger := w32.ClientToScreen(w.hwnd, w32.Point{X: int32(c.X * w.ppd()), Y: int32(c.Y * w.ppd())})
	a.onMouse(popWMLButtonDown, &popMouseHook{Pt: onTrigger, Hwnd: w.hwnd, HitTest: popHTClient})
	pumpFor(t, a, w, 50*time.Millisecond, func() bool { return false })
	if !mb.IsOpen() {
		t.Fatal("a press on the trigger dismissed the menu as an outside press")
	}
	// the owner losing the activation dismisses it
	a.onSent(&popCWP{Message: popWMActivate, WParam: popWAInactive, Hwnd: w.hwnd})
	pump(t, a, w, "the deactivation", time.Second, func() bool { return !mb.IsOpen() && len(popupsOf[w]) == 0 })
	if hooksOf[a].refs != 0 || hooksOf[a].mouse != 0 {
		t.Errorf("the hooks outlived the popups: %+v", hooksOf[a])
	}
}

// settle runs the loop until the popup's rise (160 ms) is over and it asks for no frame.
func settle(t *testing.T, a *App, w *Window, p *popupWindow) {
	t.Helper()
	end := time.Now().Add(300 * time.Millisecond)
	pump(t, a, w, "the popup's rise", 2*time.Second, func() bool { return time.Now().After(end) && !p.frameWanted })
}

// popupShadowBelow is how far below the box --shadow-lg reaches (24 px down, 64 px blur).
const popupShadowBelow = 40

// TestPopupSelectList opens a Select's list with the keyboard: the trigger keeps the focus, the
// list is a popup 4 px under it, and Enter chooses the active option.
func TestPopupSelectList(t *testing.T) {
	var changes []string
	s := widgets.NewSelect([]widgets.SelectOption{
		{Value: "none", Label: "Unlimited"}, {Value: "20", Label: "20 MiB/s"}, {Value: "10", Label: "10 MiB/s", Detail: "half"},
	}, "none", widgets.SelectOptions{Label: "Speed limit", Size: widgets.SelectSm}, func(v string) { changes = append(changes, v) })
	s.Style.Width = kit.Px(200)
	ww := openWidgets(t, "DownexSelectTest", true, kit.Size{W: 420, H: 360}, s)
	a, w := ww.a, ww.w
	w.call(func() { ww.kw.Focus(s) })
	postKey(w.hwnd, uintptr(kit.KeyArrowDown))
	pump(t, a, w, "the list", 2*time.Second, func() bool { ps := popupsOf[w]; return len(ps) == 1 && ps[0].paints > 0 })
	if !s.Base().Focused() || s.Active() != "none" {
		t.Fatalf("focused %v active %q", s.Base().Focused(), s.Active())
	}
	if b, tb := s.ListBounds(), s.Base().WindowBounds(); b.Min.Y != tb.Max.Y+4 || b.Width() < tb.Width() {
		t.Errorf("list at %v under a trigger at %v", b, tb)
	}
	p := ww.popup(t)
	settle(t, a, w, p)
	img := p.image(color.RGBA{0x0a, 0x0e, 0x0c, 255})
	saveShot(t, "native-s1-widget-select-list-dark-live.png", img)
	hit := p.hit
	boxPx := stdimage.Rect(int(hit.Min.X*p.ppd)+4, int(hit.Min.Y*p.ppd)+4, int(hit.Max.X*p.ppd)-4, int(hit.Max.Y*p.ppd)-4)
	if n := fringes(img, boxPx); n < 20 {
		t.Errorf("the list's text has %d coloured fringe pixels: grayscale, not ClearType", n)
	} else {
		t.Logf("ClearType in the popup: %d fringe pixels in the box", n)
	}
	postKey(w.hwnd, uintptr(kit.KeyArrowDown))
	postKey(w.hwnd, uintptr(kit.KeyEnter))
	pump(t, a, w, "the choice", time.Second, func() bool { return len(changes) == 1 })
	if changes[0] != "20" || s.IsOpen() || len(popupsOf[w]) != 0 {
		t.Errorf("changes %q open %v", changes, s.IsOpen())
	}
}

// TestPopupTooltip shows the system's tooltip with the whole text of a label an ellipsis cuts,
// once the pointer has rested on it for the hover time, and hides it on a press.
func TestPopupTooltip(t *testing.T) {
	long := "annual-report-2025-final-version-with-every-appendix.pdf"
	lbl := kit.NewLabel(long, widgets.FontBody, kit.TextOptions{Truncate: kit.TruncateEnd})
	lbl.Style = kit.Style{Width: kit.Px(120)}
	ww := openWidgets(t, "DownexTipTest", false, kit.Size{W: 320, H: 120}, lbl)
	a, w := ww.a, ww.w
	if !widgets.EnableTooltips(ww.kw) {
		t.Fatal("the window has no tooltips")
	}
	if !lbl.Truncated() {
		t.Fatal("the label is not cut")
	}
	c := lbl.WindowBounds().Center()
	pt := w32.ClientToScreen(w.hwnd, w32.Point{X: int32(c.X * w.ppd()), Y: int32(c.Y * w.ppd())})
	cursorPos = func() (w32.Point, bool) { return pt, true }
	t.Cleanup(func() { cursorPos = w32.GetCursorPos })
	a.onMouse(popWMMouseMove, &popMouseHook{Pt: pt, Hwnd: w.hwnd, HitTest: popHTClient})
	pump(t, a, w, "the tooltip", 2*time.Second, func() bool { _, ok := a.tips().shownText(w); return ok })
	if text, _ := a.tips().shownText(w); text != long {
		t.Errorf("tooltip %q, want the whole name", text)
	}
	a.onMouse(popWMLButtonDown, &popMouseHook{Pt: pt, Hwnd: w.hwnd, HitTest: popHTClient})
	if _, ok := a.tips().shownText(w); ok {
		t.Error("a press left the tooltip")
	}
	w.SetTooltipSource(nil)
	if hooksOf[a].refs != 0 {
		t.Errorf("the hooks outlived the tooltips: %d", hooksOf[a].refs)
	}
}

// saveShot writes img to the evidence folder ($DOWNEX_WIDGET_SHOTS, or the test's temp dir).
func saveShot(t *testing.T, name string, img stdimage.Image) {
	t.Helper()
	dir := os.Getenv("DOWNEX_WIDGET_SHOTS")
	if dir == "" {
		dir = t.TempDir()
	}
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

// fringes counts the pixels of an image region whose channels differ (ClearType's colour
// fringes; grayscale text has none).
func fringes(img *stdimage.RGBA, r stdimage.Rectangle) int {
	n := 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			c := img.RGBAAt(x, y)
			if d := int(c.R) - int(c.B); d > 24 || d < -24 {
				n++
			}
		}
	}
	return n
}

var _ = strings.Contains
var _ = hostcap.DismissPress
