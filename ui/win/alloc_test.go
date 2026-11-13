//go:build windows

package win

import (
	"testing"

	"downex/ui/kit"
	"downex/ui/win/internal/com"
	"downex/ui/win/internal/d2d"
	"downex/ui/win/internal/dwrite"
)

// The COM call stubs allocate nothing: pointer arguments stay on the caller's stack.
func TestCOMCallsDoNotAllocate(t *testing.T) {
	r := newRes(t)
	o := newOffscreen(t, r, 64, 64, 1)
	l := layout(t, r, "Downex 12.7 MB/s", body, kit.TextOptions{})
	if _, err := o.BeginFrame(o.Bounds()); err != nil {
		t.Fatal(err)
	}
	rt, brush := o.dev.rt, &o.dev.brush.Brush
	f, err := r.format(mustNorm(t, kit.TextStyle{Family: fontUI, Size: 13, Weight: 650}))
	if err != nil {
		t.Fatal(err)
	}
	tf3, hr := com.Query[dwrite.TextFormat3](&f.format.Object, &dwrite.IID_IDWriteTextFormat3)
	if hr.Failed() {
		t.Fatal(hr)
	}
	defer tf3.Release()
	for name, call := range map[string]func(){
		"ID2D1RenderTarget::FillRectangle": func() {
			rc := d2d.RectF{Left: 1, Top: 2, Right: 30, Bottom: 40}
			rt.FillRectangle(&rc, brush)
		},
		"ID2D1RenderTarget::DrawTextLayout": func() {
			rt.DrawTextLayout(2, 3, &l.layout.Object, brush, d2d.DRAW_TEXT_OPTIONS_ENABLE_COLOR_FONT)
		},
		"IDWriteTextFormat3::SetFontAxisValues": func() {
			axes := [2]dwrite.FontAxisValue{{Tag: dwrite.FourCC("wght"), Value: 650}, {Tag: dwrite.FourCC("opsz"), Value: 10.5}}
			tf3.SetFontAxisValues(&axes[0], 2)
		},
		"ID2D1SolidColorBrush::SetColor": func() {
			c := d2d.ColorF{R: 0.5, A: 1}
			o.dev.brush.SetColor(&c)
		},
		"IDWriteTextLayout::HitTestPoint": func() {
			var m dwrite.HitTestMetrics
			l.layout.HitTestPoint(10, 5, &m)
		},
	} {
		if n := testing.AllocsPerRun(200, call); n != 0 {
			t.Errorf("%s: %v allocations per call", name, n)
		}
	}
	if err := o.EndFrame(); err != nil {
		t.Fatal(err)
	}
}

// A frame that draws nothing new (every layout, path, gradient and shadow made before)
// allocates nothing.
func TestSteadyFrameDoesNotAllocate(t *testing.T) {
	r := newRes(t)
	o := newOffscreen(t, r, 200, 120, 1.25)
	l := layout(t, r, "conference-keynote.mp4", body, kit.TextOptions{})
	p, err := r.NewSVGPath("M12 5v14M5 12h14", kit.FillNonZero)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Release()
	img := testImage(t, r)
	defer img.Release()
	stops := []kit.GradientStop{{Offset: 0, Color: accent}, {Offset: 1, Color: kit.Color{}}}
	paint := func(c kit.Canvas) {
		c.FillRect(kit.R(0, 0, 160, 96), surface)
		c.DrawShadow(kit.RoundRect(kit.R(10, 10, 80, 50), 8), kit.Shadow{Offset: kit.Pt(0, 1), Blur: 2, Color: kit.RGBA(16, 28, 22, 0.06)})
		c.DrawShadow(kit.RoundRect(kit.R(10, 10, 80, 50), 8), kit.Shadow{Spread: 1, Color: accent})
		c.FillRoundedRect(kit.RoundRect(kit.R(10, 10, 80, 50), 8), surface)
		c.FillRoundedRect(kit.RRect{Rect: kit.R(90, 10, 150, 50), Radii: kit.Radii{TopLeft: 6, BottomLeft: 6}}, accent)
		c.StrokeRoundedRect(kit.RoundRect(kit.R(10, 60, 80, 90), 4).Inset(0.5), kit.Stroke{Width: 1}, line)
		c.PushClip(kit.R(0, 0, 150, 96))
		c.PushTransform(kit.Scaling(0.75, 0.75).Then(kit.Translation(90, 60)))
		c.StrokePath(p, kit.Stroke{Width: 1.6, Cap: kit.CapRound, Join: kit.JoinRound}, ink)
		c.Pop()
		c.DrawText(l, kit.Pt(12, 18), ink)
		c.Pop()
		c.PushRoundedClip(kit.RoundRect(kit.R(100, 60, 140, 90), 6))
		c.PushOpacity(0.5)
		c.DrawImage(img, kit.Rect{}, kit.R(100, 60, 140, 90))
		c.Pop()
		c.Pop()
		c.FillGradient(kit.R(0, 100, 160, 110), kit.LinearGradient{Start: kit.Pt(0, 0), End: kit.Pt(160, 0), Stops: stops})
		c.DrawLine(kit.Pt(0, 115), kit.Pt(160, 115), kit.Stroke{Width: 1, Dash: kit.Dash{On: 3, Off: 3}}, line)
		c.FillEllipse(kit.R(150, 0, 160, 10), accent)
	}
	frame(t, o, paint) // makes the gradient, the shadow mask, the image's bitmap
	n := testing.AllocsPerRun(50, func() {
		c, err := o.BeginFrame(o.Bounds())
		if err != nil {
			t.Fatal(err)
		}
		paint(c)
		if err := o.EndFrame(); err != nil {
			t.Fatal(err)
		}
	})
	if n != 0 {
		t.Errorf("a frame of cached resources allocated %v times", n)
	}
}
