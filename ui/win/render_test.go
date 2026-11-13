//go:build windows

package win

import (
	"bytes"
	"errors"
	stdimage "image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"downex/ui/kit"
)

// shotsDir is where tests write their PNGs: $DOWNEX_WIN_SHOTS when set (to look at them),
// else a temporary folder.
func shotsDir(t testing.TB) string {
	if d := os.Getenv("DOWNEX_WIN_SHOTS"); d != "" {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		return d
	}
	return t.TempDir()
}

func newOffscreen(t testing.TB, r *Resources, w, h int, ppd float32) *Offscreen {
	t.Helper()
	o, err := NewOffscreen(r, w, h, ppd, OffscreenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(o.Close)
	return o
}

// frame draws one whole frame of o with paint.
func frame(t testing.TB, o *Offscreen, paint func(c kit.Canvas)) {
	t.Helper()
	c, err := o.BeginFrame(o.Bounds())
	if err != nil {
		t.Fatal(err)
	}
	paint(c)
	if err := o.EndFrame(); err != nil {
		t.Fatal(err)
	}
}

func pixels(t testing.TB, o *Offscreen) *stdimage.RGBA {
	t.Helper()
	img, err := o.RGBA()
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// Studio tokens (app.css, light theme) used by the test scenes.
var (
	surface = kit.RGB(0xffffff)
	ink     = kit.RGB(0x0f1613)
	muted   = kit.RGB(0x56625c)
	line    = kit.RGB(0xe4e9e6)
	accent  = kit.RGB(0x1b7f4c)
)

// The international rows of the risk judge's run (judge-risk\intl-rows.png): Chinese,
// Arabic, Korean and colour emoji names next to a Latin one. Every character must come out
// of a font that has it (no .notdef boxes) and the emoji in colour.
func TestInternationalRows(t *testing.T) {
	r := newRes(t)
	rows := []struct{ name, host string }{
		{"会议记录-2026年第三季度.dat", "files.example.com · Other"},
		{"تقرير-الميزانية-٢٠٢٦.xlsx", "files.example.com · Documents"},
		{"city-timelapse-4k.mp4", "media.example.org · Video"},
		{"📦 setup-x64 🚀 한국어.exe", "downloads.example.com · Programs"},
	}
	nameStyle := kit.TextStyle{Family: fontUI, Size: 13, Weight: 500, LineHeight: 1.4, Tabular: true}
	metaStyle := kit.TextStyle{Family: fontUI, Size: 11.5, LineHeight: 1.4, Tabular: true}
	for _, ppd := range []float32{1, 1.25} {
		w, h := int(420*ppd), int(4*46*ppd)
		o := newOffscreen(t, r, w, h, ppd)
		var emoji []kit.Rect
		frame(t, o, func(c kit.Canvas) {
			c.FillRect(o.Bounds(), surface)
			for i, row := range rows {
				y := float32(i * 46)
				name := layout(t, r, row.name, nameStyle, kit.TextOptions{}) // content-sized, as the row's flex item
				meta := layout(t, r, row.host, metaStyle, kit.TextOptions{MaxWidth: 300, Truncate: kit.TruncateEnd})
				c.DrawText(name, kit.Pt(56, y+5), ink)
				c.DrawText(meta, kit.Pt(56, y+24), muted)
				c.FillRoundedRect(kit.RoundRect(kit.R(16, y+10, 42, y+36), 7), accent)
				c.FillRect(kit.R(0, y+45, 420, y+46), line)
				if i == 3 {
					for _, rc := range name.AppendRangeRects(nil, 0, len("📦")) {
						emoji = append(emoji, rc.Add(kit.Pt(56, y+5)))
					}
				}
			}
		})
		name := filepath.Join(shotsDir(t), "intl-rows@"+ppdName(ppd)+".png")
		if err := o.WritePNG(name); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", name)
		img := pixels(t, o)
		if len(emoji) == 0 || !colourful(img, emoji[0], ppd) {
			t.Errorf("at %v×: the 📦 emoji at %v is not drawn in colour", ppd, emoji)
		}
	}
	for _, row := range rows {
		if bad := tofu(t, r, row.name, nameStyle); len(bad) > 0 {
			t.Errorf("%q: these characters fall back to the missing-glyph box: %q", row.name, bad)
		}
	}
}

func ppdName(ppd float32) string {
	if ppd == 1 {
		return "100"
	}
	return "125"
}

// colourful reports a saturated pixel inside rc (DIPs): colour glyphs, not grey text.
func colourful(img *stdimage.RGBA, rc kit.Rect, ppd float32) bool {
	for y := int(rc.Min.Y * ppd); y < int(rc.Max.Y*ppd); y++ {
		for x := int(rc.Min.X * ppd); x < int(rc.Max.X*ppd); x++ {
			c := img.RGBAAt(x, y)
			hi, lo := max(c.R, c.G, c.B), min(c.R, c.G, c.B)
			if int(hi)-int(lo) > 80 {
				return true
			}
		}
	}
	return false
}

// tofu is the characters of text that render exactly as a code point no font has (U+10FFFD):
// the primary font's .notdef box, which system font fallback should have avoided.
func tofu(t *testing.T, r *Resources, text string, st kit.TextStyle) []string {
	t.Helper()
	draw := func(s string) []byte {
		o := newOffscreen(t, r, 40, 30, 1)
		l := layout(t, r, s, st, kit.TextOptions{})
		frame(t, o, func(c kit.Canvas) {
			c.FillRect(o.Bounds(), surface)
			c.DrawText(l, kit.Pt(4, 4), ink)
		})
		return pixels(t, o).Pix
	}
	box := draw("\U0010FFFD")
	var bad []string
	for _, ch := range text {
		if ch == ' ' {
			continue
		}
		if bytes.Equal(draw(string(ch)), box) {
			bad = append(bad, string(ch))
		}
	}
	// The detector itself: a code point no font has is caught.
	if !bytes.Equal(draw("\U0010FFFC"), box) {
		t.Errorf("tofu detector: U+10FFFC does not draw the missing-glyph box")
	}
	return bad
}

// Every primitive of the canvas draws where it is asked to, in device pixels at 125%.
func TestCanvasPrimitives(t *testing.T) {
	r := newRes(t)
	const ppd = 1.25
	o := newOffscreen(t, r, 500, 375, ppd)                               // 400 × 300 DIPs
	icon, err := r.NewSVGPath("M5 12.5l4.5 4.5L19 7.5", kit.FillNonZero) // Icon.svelte's check
	if err != nil {
		t.Fatal(err)
	}
	defer icon.Release()
	tri, err := r.NewSVGPath("M0 0h20v20z", kit.FillNonZero)
	if err != nil {
		t.Fatal(err)
	}
	defer tri.Release()
	label := layout(t, r, "Downex", body, kit.TextOptions{})
	red, blue, green := kit.RGB(0xd03030), kit.RGB(0x3050d0), kit.RGB(0x20a050)
	frame(t, o, func(c kit.Canvas) {
		c.FillRect(o.Bounds(), surface)
		c.FillRect(kit.R(10, 10, 50, 50), red)
		c.StrokeRect(kit.R(60, 10, 100, 50).Inset(kit.UniformInsets(1)), kit.Stroke{Width: 2}, blue)
		c.FillRoundedRect(kit.RoundRect(kit.R(110, 10, 150, 50), 999), green) // a pill: radii fit
		c.FillRoundedRect(kit.RRect{Rect: kit.R(160, 10, 200, 50), Radii: kit.Radii{TopLeft: 20}}, red)
		c.StrokeRoundedRect(kit.RoundRect(kit.R(210, 10, 250, 50), 8).Inset(1), kit.Stroke{Width: 2}, blue)
		c.FillEllipse(kit.R(260, 10, 300, 50), green)
		c.StrokeEllipse(kit.R(310, 10, 350, 50), kit.Stroke{Width: 3}, red)
		c.DrawLine(kit.Pt(360, 30), kit.Pt(390, 30), kit.Stroke{Width: 4, Cap: kit.CapRound}, blue)
		c.DrawLine(kit.Pt(10, 70), kit.Pt(190, 70), kit.Stroke{Width: 2, Dash: kit.Dash{On: 3, Off: 3}}, ink)

		c.PushTransform(kit.Scaling(2, 2).Then(kit.Translation(10, 80))) // a 24-unit icon at 48 DIPs
		c.StrokePath(icon, kit.Stroke{Width: 1.6, Cap: kit.CapRound, Join: kit.JoinRound}, accent)
		c.Pop()
		c.PushTransform(kit.Translation(70, 85))
		c.FillPath(tri, red)
		c.Pop()

		c.PushClip(kit.R(110, 80, 130, 120))
		c.FillRect(kit.R(100, 80, 150, 120), blue) // only 110..130 shows
		c.Pop()
		c.PushRoundedClip(kit.RoundRect(kit.R(160, 80, 200, 120), 20))
		c.FillRect(kit.R(150, 70, 210, 130), green) // a circle shows
		c.Pop()
		c.PushOpacity(0.5)
		c.FillRect(kit.R(210, 80, 250, 120), ink)
		c.Pop()
		c.FillGradient(kit.R(260, 80, 390, 120), kit.LinearGradient{Start: kit.Pt(260, 0), End: kit.Pt(390, 0),
			Stops: []kit.GradientStop{{Offset: 0, Color: red}, {Offset: 1, Color: blue}}})

		c.DrawShadow(kit.RoundRect(kit.R(20, 160, 120, 220), 12), kit.Shadow{Offset: kit.Pt(0, 4), Blur: 12, Color: kit.RGBA(16, 28, 22, 0.5)})
		c.FillRoundedRect(kit.RoundRect(kit.R(20, 160, 120, 220), 12), surface)
		// app.css's focus ring, 0 0 0 1px --focus over 0 0 0 4px --accent-soft, on whole pixels at 125%
		c.DrawShadow(kit.RoundRect(kit.R(152, 160, 248, 220), 8), kit.Shadow{Spread: 4, Color: kit.RGBA(27, 127, 76, 0.4)})
		c.DrawShadow(kit.RoundRect(kit.R(152, 160, 248, 220), 8), kit.Shadow{Spread: 1, Color: accent})
		c.DrawShadow(kit.RoundRect(kit.R(280, 160, 380, 220), 8), kit.Shadow{Spread: 1, Inset: true, Color: blue})
		c.DrawText(label, kit.Pt(20, 240), ink)
	})
	if err := o.WritePNG(filepath.Join(shotsDir(t), "primitives@125.png")); err != nil {
		t.Fatal(err)
	}
	img := pixels(t, o)
	at := func(x, y float32) color.RGBA { return img.RGBAAt(int(x*ppd), int(y*ppd)) }
	near := func(name string, got color.RGBA, want kit.Color, tol int) {
		t.Helper()
		w := color.RGBA{uint8(want.R*255 + 0.5), uint8(want.G*255 + 0.5), uint8(want.B*255 + 0.5), 255}
		d := func(a, b uint8) int {
			if a > b {
				return int(a - b)
			}
			return int(b - a)
		}
		if d(got.R, w.R) > tol || d(got.G, w.G) > tol || d(got.B, w.B) > tol {
			t.Errorf("%s: pixel %v, want %v", name, got, w)
		}
	}
	white := surface
	near("fill", at(30, 30), red, 2)
	near("outside the fill", at(55, 30), white, 2)
	near("stroke", at(61, 30), blue, 2)
	near("inside the stroke", at(80, 30), white, 2)
	near("pill", at(130, 30), green, 2)
	near("pill's rounded-off corner", at(111, 11), white, 8)
	near("one round corner", at(161, 11), white, 8)
	near("square corner", at(199, 11), red, 2)
	near("ellipse", at(280, 30), green, 2)
	near("ellipse corner", at(261, 11), white, 8)
	near("clip keeps", at(120, 100), blue, 2)
	near("clip cuts", at(105, 100), white, 2)
	near("rounded clip keeps", at(180, 100), green, 2)
	near("rounded clip cuts", at(162, 82), white, 8)
	near("opacity 0.5", at(230, 100), kit.Mix(ink, 0.5, white), 3)
	near("gradient start", at(261, 100), red, 12)
	near("gradient end", at(389, 100), blue, 12)
	near("transformed path", at(75, 88), red, 4) // the triangle's upper-right half
	near("transformed path's other half", at(72, 102), white, 8)
	near("shadow below the card", at(70, 226), kit.Mix(kit.RGBA(16, 28, 22, 1), 0.2, white), 40)
	near("card over its shadow", at(70, 190), white, 1)
	near("focus ring, 1 px", at(151.6, 190), accent, 3)
	near("focus ring's soft spread", at(149, 190), kit.Mix(kit.RGB(0x1b7f4c), 0.4, white), 3)
	near("inside the ring", at(153, 190), white, 2)
	near("inset hairline", at(280.4, 190), blue, 30)
	near("inside the inset hairline", at(282, 190), white, 2)
}

// A device lost at EndFrame (D2DERR_RECREATE_TARGET, simulated) is reported as
// kit.ErrDeviceLost, drops the target and everything made for it, and the next frame makes
// them again and draws the same picture.
func TestDeviceLossRecovers(t *testing.T) {
	r := newRes(t)
	o := newOffscreen(t, r, 200, 120, 1)
	img := testImage(t, r)
	defer img.Release()
	label := layout(t, r, "Downex 1.2 GB", body, kit.TextOptions{})
	scene := func(c kit.Canvas) {
		c.FillRect(o.Bounds(), surface)
		c.FillGradient(kit.R(0, 0, 200, 20), kit.LinearGradient{Start: kit.Pt(0, 0), End: kit.Pt(200, 0),
			Stops: []kit.GradientStop{{Offset: 0, Color: accent}, {Offset: 1, Color: surface}}})
		c.DrawShadow(kit.RoundRect(kit.R(20, 40, 80, 80), 8), kit.Shadow{Offset: kit.Pt(0, 2), Blur: 6, Color: kit.RGBA(0, 0, 0, 0.3)})
		c.FillRoundedRect(kit.RRect{Rect: kit.R(20, 40, 80, 80), Radii: kit.Radii{TopLeft: 8, BottomRight: 8}}, surface)
		c.DrawImage(img, kit.Rect{}, kit.R(100, 40, 132, 72))
		c.DrawText(label, kit.Pt(100, 85), ink)
	}
	frame(t, o, scene)
	before := pixels(t, o)
	if o.dev.targetsMade != 1 {
		t.Fatalf("targets made: %d", o.dev.targetsMade)
	}

	o.SimulateDeviceLoss()
	c, err := o.BeginFrame(o.Bounds())
	if err != nil {
		t.Fatal(err)
	}
	c.FillRect(o.Bounds(), kit.RGB(0xff00ff)) // this frame is lost
	err = o.EndFrame()
	if !errors.Is(err, kit.ErrDeviceLost) {
		t.Fatalf("EndFrame after a loss: %v, want ErrDeviceLost", err)
	}
	t.Logf("loss reported as: %v", err)
	if o.dev.rt != nil || o.dev.brush != nil || len(o.dev.gradients) != 0 || len(o.dev.shadows) != 0 || len(o.dev.images) != 0 || len(img.(*image).devs) != 0 {
		t.Fatal("the lost device's resources were not all released")
	}

	frame(t, o, scene) // the caller repaints everything
	if o.dev.targetsMade != 2 || o.dev.losses != 1 {
		t.Fatalf("targets made %d, losses %d", o.dev.targetsMade, o.dev.losses)
	}
	after := pixels(t, o)
	if !bytes.Equal(before.Pix, after.Pix) {
		t.Fatal("the frame after the recovery differs from the one before the loss")
	}
	if err := o.WritePNG(filepath.Join(shotsDir(t), "device-loss-recovered.png")); err != nil {
		t.Fatal(err)
	}
}

// A shadow whose mask cannot be made (a negative spread that empties its box) is cached as
// an empty mask, so that it is not tried every frame, and ages out with the others once the
// cache is full; aging such an entry once crashed EndFrame.
func TestShadowMasksThatFailAge(t *testing.T) {
	r := newRes(t)
	o := newOffscreen(t, r, 200, 120, 1)
	shadow := func(w kit.DIP, spread kit.DIP) func(c kit.Canvas) {
		return func(c kit.Canvas) {
			c.DrawShadow(kit.RoundRect(kit.R(20, 20, 20+w, 60), 8), kit.Shadow{Blur: 6, Spread: spread, Color: kit.RGBA(0, 0, 0, 0.3)})
		}
	}
	frame(t, o, shadow(60, -100))
	if len(o.dev.shadows) != 1 {
		t.Fatalf("%d masks cached after one shadow, want 1", len(o.dev.shadows))
	}
	for _, m := range o.dev.shadows {
		if m == nil || m.bmp != nil {
			t.Fatalf("an emptied shadow is cached as %+v, want an empty mask", m)
		}
	}
	frame(t, o, shadow(60, -100)) // found in the cache, not made again
	if len(o.dev.shadows) != 1 {
		t.Fatal("the failed mask was not reused")
	}
	for i := range 40 { // a size animation: one mask per step, past the cache's limit
		frame(t, o, shadow(kit.DIP(20+i), 2))
	}
	if len(o.dev.shadows) <= 32 {
		t.Fatalf("only %d masks cached; the test needs more than 32", len(o.dev.shadows))
	}
	o.dev.frame += 1000 // none was used for longer than the cache keeps them
	frame(t, o, func(kit.Canvas) {})
	if n := len(o.dev.shadows); n != 0 {
		t.Errorf("%d masks left after they all aged out", n)
	}
}

// Push and Pop that do not balance are closed and reported; the frame is still shown.
func TestUnbalancedPushes(t *testing.T) {
	r := newRes(t)
	o := newOffscreen(t, r, 40, 40, 1)
	c, _ := o.BeginFrame(o.Bounds())
	c.PushClip(kit.R(0, 0, 10, 10))
	c.PushOpacity(0.5)
	c.PushTransform(kit.Translation(1, 1))
	c.FillRect(o.Bounds(), accent)
	if err := o.EndFrame(); !errors.Is(err, kit.ErrInvalid) || errors.Is(err, kit.ErrDeviceLost) {
		t.Fatalf("EndFrame with three pushes open: %v", err)
	}
	c, _ = o.BeginFrame(o.Bounds())
	c.Pop()
	if err := o.EndFrame(); !errors.Is(err, kit.ErrInvalid) {
		t.Fatalf("EndFrame after a pop without push: %v", err)
	}
	frame(t, o, func(c kit.Canvas) { c.FillRect(o.Bounds(), accent) })
	if p := pixels(t, o).RGBAAt(20, 20); p.G < 100 {
		t.Fatalf("the next frame did not draw: %v", p)
	}
	// Only the dirty area is redrawn; the rest keeps the previous frame.
	c, _ = o.BeginFrame(kit.R(0, 0, 10, 10))
	c.FillRect(o.Bounds(), kit.RGB(0xff0000))
	if err := o.EndFrame(); err != nil {
		t.Fatal(err)
	}
	img := pixels(t, o)
	if p := img.RGBAAt(5, 5); p.R != 255 || p.G != 0 {
		t.Errorf("inside the dirty area: %v", p)
	}
	if p := img.RGBAAt(20, 20); p.R == 255 {
		t.Errorf("outside the dirty area the frame drew: %v", p)
	}
}

// testImage is a 4×4 PNG: red, with a blue top-left pixel.
func testImage(t testing.TB, r *Resources) kit.Image {
	t.Helper()
	src := stdimage.NewNRGBA(stdimage.Rect(0, 0, 4, 4))
	for i := 0; i < 16; i++ {
		src.Set(i%4, i/4, color.NRGBA{200, 30, 30, 255})
	}
	src.Set(0, 0, color.NRGBA{30, 30, 200, 255})
	var b bytes.Buffer
	if err := png.Encode(&b, src); err != nil {
		t.Fatal(err)
	}
	img, err := r.NewImage(b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func TestImages(t *testing.T) {
	r := newRes(t)
	img := testImage(t, r)
	if w, h := img.PixelSize(); w != 4 || h != 4 {
		t.Fatalf("size %d×%d", w, h)
	}
	o := newOffscreen(t, r, 40, 40, 1)
	frame(t, o, func(c kit.Canvas) {
		c.FillRect(o.Bounds(), surface)
		c.DrawImage(img, kit.R(0, 0, 1, 1), kit.R(0, 0, 20, 20)) // the blue pixel, scaled
		c.DrawImage(img, kit.Rect{}, kit.R(20, 20, 40, 40))      // all of it
	})
	px := pixels(t, o)
	if p := px.RGBAAt(10, 10); p.B < 150 || p.R > 80 {
		t.Errorf("src part: %v, want blue", p)
	}
	if p := px.RGBAAt(35, 35); p.R < 150 || p.B > 80 {
		t.Errorf("whole image: %v, want red", p)
	}
	img.Release()
	img.Release()
	if w, h := img.PixelSize(); w != 0 || h != 0 {
		t.Error("a released image keeps its size")
	}
	frame(t, o, func(c kit.Canvas) { c.DrawImage(img, kit.Rect{}, o.Bounds()) }) // draws nothing, no crash

	if _, err := r.NewImage([]byte("definitely not an image")); !errors.Is(err, kit.ErrUnsupported) {
		t.Errorf("unknown format: %v, want ErrUnsupported", err)
	}
	var b bytes.Buffer
	png.Encode(&b, stdimage.NewNRGBA(stdimage.Rect(0, 0, 64, 64)))
	if _, err := r.NewImage(b.Bytes()[:40]); !errors.Is(err, kit.ErrInvalid) {
		t.Errorf("truncated PNG: %v, want ErrInvalid", err)
	}
	if _, err := r.NewImage(nil); !errors.Is(err, kit.ErrInvalid) {
		t.Errorf("no data: %v", err)
	}
}

// The PNG an offscreen target writes decodes to its pixels.
func TestEncodePNG(t *testing.T) {
	r := newRes(t)
	for _, tr := range []bool{false, true} {
		o, err := NewOffscreen(r, 16, 8, 1, OffscreenOptions{Transparent: tr})
		if err != nil {
			t.Fatal(err)
		}
		frame(t, o, func(c kit.Canvas) { c.FillRect(kit.R(0, 0, 8, 8), kit.RGBA(255, 0, 0, 1)) })
		data, err := o.EncodePNG()
		if err != nil {
			t.Fatal(err)
		}
		dec, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		if got := color.NRGBAModel.Convert(dec.At(2, 2)).(color.NRGBA); got != (color.NRGBA{255, 0, 0, 255}) {
			t.Errorf("transparent=%v: red pixel decodes as %v", tr, got)
		}
		right := color.NRGBAModel.Convert(dec.At(12, 2)).(color.NRGBA)
		if tr && right.A != 0 || !tr && right != (color.NRGBA{0, 0, 0, 255}) {
			t.Errorf("transparent=%v: unpainted pixel decodes as %v", tr, right)
		}
		o.Close()
	}
}
