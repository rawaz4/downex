//go:build windows

package win

import (
	stdimage "image"
	"testing"
	"time"

	"downex/ui/kit"
	"downex/ui/win/internal/w32"
)

// clipRoot draws the same dark text on a light ground twice: once in the open and once
// inside a rounded clip, as the main window's list card clips its rows.
type clipRoot struct {
	testRoot
	text kit.TextLayout
}

var (
	openTextAt = kit.Pt(24, 40)
	clipTextAt = kit.Pt(24, 140)
	clipBox    = kit.RoundRect(kit.R(12, 120, 420, 200), 8)
)

func (r *clipRoot) Attach(h kit.Host) {
	r.testRoot.Attach(h)
	r.text, _ = h.Resources().LayoutText("Queued #1 · os-image-26.04-amd64.iso", kit.TextStyle{Family: fontUI, Size: 14}, kit.TextOptions{})
}

func (r *clipRoot) Detach() {
	if r.text != nil {
		r.text.Release()
	}
	r.testRoot.Detach()
}

func (r *clipRoot) Paint(c kit.Canvas, dirty kit.Rect) {
	r.testRoot.Paint(c, dirty)
	c.FillRect(kit.R(0, 0, 480, 240), kit.RGB(0xffffff))
	c.DrawText(r.text, openTextAt, kit.RGB(0x0f1613))
	c.PushRoundedClip(clipBox)
	c.DrawText(r.text, clipTextAt, kit.RGB(0x0f1613))
	c.Pop()
}

// colouredInk counts the pixels of dark text on white in r whose channels differ, which
// ClearType's sub-pixel fringes make and grayscale antialiasing never does, and the inked
// pixels in all.
func colouredInk(img *stdimage.RGBA, r stdimage.Rectangle) (coloured, ink int) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			c := img.RGBAAt(x, y)
			lo, hi := min(c.R, c.G, c.B), max(c.R, c.G, c.B)
			if lo > 235 {
				continue // the ground
			}
			ink++
			if hi-lo > 24 {
				coloured++
			}
		}
	}
	return coloured, ink
}

// Text inside a rounded clip keeps ClearType on the window's target. A Direct2D layer drops
// to grayscale unless it is made for ClearType, and the main window's list card (a rounded
// clip) drew every row in grayscale while the title bar outside it had ClearType.
func TestClearTypeInsideRoundedClip(t *testing.T) {
	runtimeLock(t)
	a, err := NewApp(AppOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	root := &clipRoot{}
	root.t = t
	mon := w32.MonitorFromPoint(w32.Point{}, w32.MONITOR_DEFAULTTOPRIMARY)
	mi, _ := w32.GetMonitorInfo(mon)
	w, err := a.NewWindow(root, WindowOptions{At: w32.Point{X: mi.Work.Left + 40, Y: mi.Work.Top + 40}, Placed: true,
		NoActivate: true, Size: kit.Size{W: 480, H: 240}})
	if err != nil {
		t.Fatal(err)
	}
	if root.text == nil {
		t.Fatal("no text layout")
	}
	pump(t, a, w, "the first frame", 2*time.Second, func() bool { return w.painted })
	// On top of the others, without activation, so that the copy has the window's pixels.
	w32.SetWindowPos(w.hwnd, 0, 0, 0, 0, w32.SWP_NOMOVE|w32.SWP_NOSIZE|w32.SWP_NOACTIVATE)
	if got := w.Renderer(); got != "hardware" {
		skipOrRequire(t, "renderer is "+got+", not hardware")
	}
	img, err := capture(w)
	if err != nil {
		t.Fatal(err)
	}
	ppd := w.ppd()
	band := func(at kit.Point) stdimage.Rectangle {
		return stdimage.Rect(int(at.X*ppd), int(at.Y*ppd), int((at.X+380)*ppd), int((at.Y+24)*ppd))
	}
	openC, openInk := colouredInk(img, band(openTextAt))
	clipC, clipInk := colouredInk(img, band(clipTextAt))
	t.Logf("coloured/ink pixels: open %d/%d, inside the rounded clip %d/%d", openC, openInk, clipC, clipInk)
	if openInk == 0 || clipInk == 0 {
		t.Fatalf("no text drawn (open %d, clipped %d inked pixels)", openInk, clipInk)
	}
	if openC*10 < openInk {
		skipOrRequire(t, "the text in the open is not ClearType: font smoothing is not ClearType on this PC")
	}
	if clipC*2 < openC {
		t.Errorf("text inside the rounded clip has %d coloured pixels against %d in the open: grayscale, not ClearType", clipC, openC)
	}
}

// An opaque offscreen target (the views' references renders) keeps ClearType inside a
// rounded clip too, as the window's does (before, the clip's layer drew its text in
// grayscale, a difference from every reference: the progress lane's table worked around it).
func TestClearTypeInsideRoundedClipOffscreen(t *testing.T) {
	res, err := NewResources()
	if err != nil {
		t.Skip(err)
	}
	defer res.Close()
	o, err := NewOffscreen(res, 480, 240, 1, OffscreenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	text, err := res.LayoutText("Queued #1 · os-image-26.04-amd64.iso", kit.TextStyle{Family: fontUI, Size: 14}, kit.TextOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer text.Release()
	c, err := o.BeginFrame(o.Bounds())
	if err != nil {
		t.Fatal(err)
	}
	c.FillRect(kit.R(0, 0, 480, 240), kit.RGB(0xffffff))
	c.DrawText(text, openTextAt, kit.RGB(0x0f1613))
	c.PushRoundedClip(clipBox)
	c.DrawText(text, clipTextAt, kit.RGB(0x0f1613))
	c.Pop()
	if err := o.EndFrame(); err != nil {
		t.Fatal(err)
	}
	img, err := o.RGBA()
	if err != nil {
		t.Fatal(err)
	}
	band := func(at kit.Point) stdimage.Rectangle {
		return stdimage.Rect(int(at.X), int(at.Y), int(at.X+380), int(at.Y+24))
	}
	openC, openInk := colouredInk(img, band(openTextAt))
	clipC, clipInk := colouredInk(img, band(clipTextAt))
	t.Logf("offscreen coloured/ink pixels: open %d/%d, inside the rounded clip %d/%d", openC, openInk, clipC, clipInk)
	if openInk == 0 || clipInk == 0 {
		t.Fatalf("no text drawn (open %d, clipped %d inked pixels)", openInk, clipInk)
	}
	if openC*10 < openInk {
		skipOrRequire(t, "the offscreen text in the open is not ClearType")
	}
	if clipC*2 < openC {
		t.Errorf("offscreen text inside the rounded clip has %d coloured pixels against %d in the open: grayscale", clipC, openC)
	}
}
