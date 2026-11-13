//go:build darwin && cgo

package mac

// [render] Tests of the IOSurface presentation (present.go) and of the drawing fixes of
// the rendering part (shadow seams, EXIF orientation, very many gradient periods, hit tests
// far away). They need no window: IOSurfaces exist without one.

import (
	"bytes"
	"errors"
	stdimage "image"
	"image/color"
	"image/jpeg"
	"math"
	"testing"
	"time"

	"downex/ui/kit"
)

func rndSurfaceOff(t testing.TB, r *Resources, w, h int, ppd float32) *Offscreen {
	t.Helper()
	tgt, err := newSurfaceTarget(r, w, h, ppd, true)
	if err != nil {
		t.Fatal(err)
	}
	o := &Offscreen{t: tgt}
	t.Cleanup(o.Close)
	return o
}

func rndFrame(t testing.TB, o *Offscreen, dirty kit.Rect, paint func(c kit.Canvas)) {
	t.Helper()
	c, err := o.BeginFrame(dirty)
	if err != nil {
		t.Fatal(err)
	}
	paint(c)
	if err := o.EndFrame(); err != nil {
		t.Fatal(err)
	}
}

func TestRndSubtractAndHistory(t *testing.T) {
	a := rndPixRect{0, 0, 100, 100}
	parts, n := rndSubtract(a, rndPixRect{10, 20, 30, 40})
	area := 0
	for _, p := range parts[:n] {
		area += (p.x1 - p.x0) * (p.y1 - p.y0)
	}
	if n != 4 || area != 100*100-20*20 {
		t.Errorf("subtract: %d parts of %d pixels", n, area)
	}
	if _, n := rndSubtract(a, a); n != 0 {
		t.Errorf("a less a: %d parts", n)
	}
	if p, n := rndSubtract(a, rndPixRect{200, 200, 300, 300}); n != 1 || p[0] != a {
		t.Errorf("disjoint: %v %d", p, n)
	}
	ch := &rndChain{w: 100, h: 100}
	since := func(f uint64) []rndPixRect { out, n := ch.since(f); return out[:n] }
	if got := since(0); len(got) != 1 || got[0] != a {
		t.Errorf("never drawn: %v", got)
	}
	for i := 1; i <= 3; i++ {
		ch.frame = uint64(i)
		ch.hist[i%rndHistory].frame, ch.hist[i%rndHistory].r = uint64(i), rndPixRect{i, i, i + 1, i + 1}
	}
	// Two small areas apart: each is copied, not their bounds.
	if got := since(1); len(got) != 2 || got[0] != (rndPixRect{2, 2, 3, 3}) || got[1] != (rndPixRect{3, 3, 4, 4}) {
		t.Errorf("since 1: %v", got)
	}
	if got := since(3); len(got) != 0 {
		t.Errorf("up to date: %v", got)
	}
	// Areas inside one another count once; bounds no larger than the areas are one.
	ch.frame = 6
	ch.hist[4%rndHistory].frame, ch.hist[4%rndHistory].r = 4, rndPixRect{0, 0, 50, 10}
	ch.hist[5%rndHistory].frame, ch.hist[5%rndHistory].r = 5, rndPixRect{10, 0, 20, 5}
	ch.hist[6%rndHistory].frame, ch.hist[6%rndHistory].r = 6, rndPixRect{0, 10, 50, 20}
	if got := since(3); len(got) != 1 || got[0] != (rndPixRect{0, 0, 50, 20}) {
		t.Errorf("since 3: %v", got)
	}
	ch.hist[6%rndHistory].r = rndPixRect{0, 90, 100, 100}
	ch.hist[5%rndHistory].r = rndPixRect{0, 0, 100, 10}
	if got := since(4); len(got) != 2 {
		t.Errorf("since 4: %v", got)
	}
	ch.frame = 3 + rndHistory
	if got := since(3); len(got) != 1 || got[0] != a {
		t.Errorf("older than the history: %v", got)
	}
}

// Frames that redraw only a part keep the rest of the last frame although they draw into
// another surface, and only the changed areas are copied forward.
func TestRndSurfacesKeepTheRest(t *testing.T) {
	r := cvRes(t)
	o := rndSurfaceOff(t, r, 400, 200, 2)
	ch := o.t.chain
	red, blue, green := kit.RGB(0xff0000), kit.RGB(0x0000ff), kit.RGB(0x00ff00)
	rndFrame(t, o, o.Bounds(), func(c kit.Canvas) { c.FillRect(o.Bounds(), red) })
	rndFrame(t, o, kit.R(10, 10, 20, 20), func(c kit.Canvas) { c.FillRect(kit.R(0, 0, 100, 100), blue) })
	rndFrame(t, o, kit.R(50, 10, 60, 20), func(c kit.Canvas) { c.FillRect(kit.R(0, 0, 100, 100), green) })
	rndFrame(t, o, kit.R(80, 80, 90, 90), func(c kit.Canvas) { c.FillRect(kit.R(0, 0, 100, 100), blue) })
	img := cvPix(t, o)
	at := func(x, y float32) color.RGBA { return img.RGBAAt(int(x*2)+1, int(y*2)+1) }
	cvNear(t, "outside every dirty area", at(150, 50), red, 0)
	cvNear(t, "frame 2's area", at(15, 15), blue, 0)
	cvNear(t, "frame 3's area", at(55, 15), green, 0)
	cvNear(t, "frame 4's area", at(85, 85), blue, 0)
	cvNear(t, "next to frame 2's area", at(25, 15), red, 0)
	n := 0
	for _, b := range ch.bufs {
		if b.s != 0 {
			n++
		}
	}
	if n < 2 {
		t.Errorf("%d surfaces, want the frames to alternate between at least two", n)
	}
	// After the first full frame, every copy is a few small dirty areas, never the window.
	if full := 400 * 200; ch.copied > 2*full {
		t.Errorf("%d pixels copied forward", ch.copied)
	}

	// Idle: the spare surfaces may be taken back. One taken back (as the system would)
	// is brought up to date from the shown one.
	ch.idle()
	for i := range ch.bufs {
		if i != ch.front && ch.bufs[i].s != 0 {
			if !ch.bufs[i].volatile {
				t.Errorf("surface %d not volatile when idle", i)
			}
			ch.bufs[i].frame = 0 // as if purged
		}
	}
	rndFrame(t, o, kit.R(150, 80, 160, 90), func(c kit.Canvas) { c.FillRect(o.Bounds(), green) })
	img = cvPix(t, o)
	cvNear(t, "after a purge: outside", at(150, 50), red, 0)
	cvNear(t, "after a purge: frame 3's area", at(55, 15), green, 0)
	cvNear(t, "after a purge: frame 4's area", at(85, 85), blue, 0)
	cvNear(t, "after a purge: the new area", at(155, 85), green, 0)

	// Idle for longer: only the shown surface is kept, and the next frame is right.
	ch.release()
	if n := ch.surfaces(); n != 1 {
		t.Errorf("%d surfaces after a long idle, want 1", n)
	}
	rndFrame(t, o, kit.R(170, 80, 180, 90), func(c kit.Canvas) { c.FillRect(o.Bounds(), blue) })
	img = cvPix(t, o)
	cvNear(t, "after a release: outside", at(150, 50), red, 0)
	cvNear(t, "after a release: frame 3's area", at(55, 15), green, 0)
	cvNear(t, "after a release: the earlier area", at(155, 85), green, 0)
	cvNear(t, "after a release: the new area", at(175, 85), blue, 0)

	// A resize starts over at the new size.
	if err := o.t.Resize(100, 60, 2); err != nil {
		t.Fatal(err)
	}
	rndFrame(t, o, o.Bounds(), func(c kit.Canvas) { c.FillRect(o.Bounds(), blue) })
	img = cvPix(t, o)
	if img.Bounds().Dx() != 100 || img.Bounds().Dy() != 60 {
		t.Errorf("resized to %v", img.Bounds())
	}
	cvNear(t, "after a resize", img.RGBAAt(99, 59), blue, 0)
}

// A frame the compositor would show over a surface it still holds waits a refresh once
// (errRndBusy, nothing drawn); held again, a third surface is made, and it goes once
// frames have not needed it for rndThirdKeep. Every frame keeps the rest of the last one,
// and a frame copies forward only the small areas that changed, not their bounds.
func TestRndHeldSurfaces(t *testing.T) {
	r := cvRes(t)
	o := rndSurfaceOff(t, r, 400, 200, 2)
	ch := o.t.chain
	now := time.Unix(1000, 0)
	ch.now = func() time.Time { return now }
	held := map[int]bool{}
	ch.held = func(i int) bool { return held[i] }
	red, blue, green := kit.RGB(0xff0000), kit.RGB(0x0000ff), kit.RGB(0x00ff00)
	rndFrame(t, o, o.Bounds(), func(c kit.Canvas) { c.FillRect(o.Bounds(), red) })
	rndFrame(t, o, kit.R(0, 0, 10, 10), func(c kit.Canvas) { c.FillRect(o.Bounds(), blue) })
	if ch.surfaces() != 2 {
		t.Fatalf("%d surfaces", ch.surfaces())
	}
	spare := 1 - ch.front
	held[spare] = true
	if _, err := o.BeginFrame(kit.R(190, 90, 200, 100)); !errors.Is(err, errRndBusy) {
		t.Fatalf("a held spare: %v, want errRndBusy", err)
	}
	if ch.cur >= 0 || o.t.inFrame || ch.surfaces() != 2 {
		t.Errorf("a waiting frame started: cur %d, %d surfaces", ch.cur, ch.surfaces())
	}
	// Held at the next refresh too: a third surface.
	rndFrame(t, o, kit.R(190, 90, 200, 100), func(c kit.Canvas) { c.FillRect(o.Bounds(), green) })
	if ch.surfaces() != 3 || ch.waits != 1 {
		t.Errorf("%d surfaces, %d waits; want 3 and 1", ch.surfaces(), ch.waits)
	}
	img := cvPix(t, o)
	at := func(x, y float32) color.RGBA { return img.RGBAAt(int(x*2)+1, int(y*2)+1) }
	cvNear(t, "third: outside", at(150, 50), red, 0)
	cvNear(t, "third: frame 2's area", at(5, 5), blue, 0)
	cvNear(t, "third: its own area", at(195, 95), green, 0)
	// Small areas far apart: the next frames copy them, never the area between them.
	clear(held)
	before := ch.copied
	for i := 0; i < 6; i++ {
		x := float32(i%2) * 180
		rndFrame(t, o, kit.R(x, 0, x+10, 10), func(c kit.Canvas) { c.FillRect(o.Bounds(), blue) })
	}
	if n := ch.copied - before; n > 6*3*(20*20) {
		t.Errorf("%d pixels copied forward for frames of 20×20 pixels", n)
	}
	// Frames that no longer need the third let it go after rndThirdKeep.
	now = now.Add(rndThirdKeep / 2)
	rndFrame(t, o, kit.R(0, 0, 10, 10), func(c kit.Canvas) { c.FillRect(o.Bounds(), red) })
	if ch.surfaces() != 3 {
		t.Errorf("the third went after %v", rndThirdKeep/2)
	}
	now = now.Add(rndThirdKeep)
	rndFrame(t, o, kit.R(0, 0, 10, 10), func(c kit.Canvas) { c.FillRect(o.Bounds(), blue) })
	if ch.surfaces() != 2 {
		t.Errorf("%d surfaces after frames that do not need the third, want 2", ch.surfaces())
	}
	img = cvPix(t, o)
	cvNear(t, "after the third went: outside", at(150, 50), red, 0)
	cvNear(t, "after the third went: the far corner", at(195, 95), green, 0)
	cvNear(t, "after the third went: the last area", at(5, 5), blue, 0)
	// A frame on time (a refresh after the last one) does not wait: it takes a third.
	now = now.Add(16 * time.Millisecond)
	held[1-ch.front] = true
	for i := range ch.bufs {
		if i != ch.front && ch.bufs[i].s != 0 {
			held[i] = true
		}
	}
	waits := ch.waits
	rndFrame(t, o, kit.R(0, 0, 10, 10), func(c kit.Canvas) { c.FillRect(o.Bounds(), green) })
	if ch.waits != waits || ch.surfaces() != 3 {
		t.Errorf("a frame on time: %d waits, %d surfaces; want no wait and 3", ch.waits-waits, ch.surfaces())
	}
}

// A chain that cannot be made at a new size keeps the old one: the target keeps drawing
// and reading at its old size.
func TestRndResizeFailureKeepsTheChain(t *testing.T) {
	r := cvRes(t)
	o := rndSurfaceOff(t, r, 100, 60, 2)
	red, blue := kit.RGB(0xff0000), kit.RGB(0x0000ff)
	rndFrame(t, o, o.Bounds(), func(c kit.Canvas) { c.FillRect(o.Bounds(), red) })
	o.t.chain.failAlloc = true
	if err := o.t.Resize(300, 200, 2); err == nil {
		t.Fatal("Resize succeeded with no surface to make")
	}
	if o.t.w != 100 || o.t.h != 60 || o.t.ctx == 0 || o.t.chain.surfaces() == 0 {
		t.Fatalf("after a failed resize: %d×%d, ctx %v, %d surfaces", o.t.w, o.t.h, o.t.ctx, o.t.chain.surfaces())
	}
	if img := cvPix(t, o); img.Bounds().Dx() != 100 {
		t.Errorf("read %v", img.Bounds())
	}
	o.t.chain.failAlloc = false
	rndFrame(t, o, kit.R(0, 0, 10, 10), func(c kit.Canvas) { c.FillRect(o.Bounds(), blue) })
	img := cvPix(t, o)
	cvNear(t, "drawn after a failed resize", img.RGBAAt(5, 5), blue, 0)
	cvNear(t, "kept after a failed resize", img.RGBAAt(99, 59), red, 0)
	if err := o.t.Resize(300, 200, 2); err != nil {
		t.Fatal(err)
	}
	if o.t.chain.now == nil || o.t.w != 300 {
		t.Error("the resized chain lost its clock or size")
	}
}

// A shown surface that cannot be locked for the copy forward fails the frame as a device
// loss (the window paints everything next) instead of showing older pixels; the surface
// the frame would have drawn is unlocked again.
func TestRndLockFailure(t *testing.T) {
	r := cvRes(t)
	o := rndSurfaceOff(t, r, 100, 60, 2)
	ch := o.t.chain
	red, blue := kit.RGB(0xff0000), kit.RGB(0x0000ff)
	rndFrame(t, o, o.Bounds(), func(c kit.Canvas) { c.FillRect(o.Bounds(), red) })
	rndFrame(t, o, kit.R(0, 0, 10, 10), func(c kit.Canvas) { c.FillRect(o.Bounds(), red) })
	failFront := true
	ch.failLock = func(i int, readonly bool) bool { return failFront && readonly && i == ch.front }
	shown := ch.front
	if _, err := o.BeginFrame(kit.R(20, 20, 30, 30)); !errors.Is(err, kit.ErrDeviceLost) {
		t.Fatalf("BeginFrame with the shown surface unlockable: %v, want a device loss", err)
	}
	if ch.cur >= 0 || o.t.inFrame || ch.front != shown {
		t.Errorf("after the failure: cur %d, front %d (was %d)", ch.cur, ch.front, shown)
	}
	// A whole frame needs no copy.
	rndFrame(t, o, o.Bounds(), func(c kit.Canvas) { c.FillRect(o.Bounds(), blue) })
	failFront = false
	rndFrame(t, o, kit.R(0, 0, 10, 10), func(c kit.Canvas) { c.FillRect(o.Bounds(), red) })
	img := cvPix(t, o)
	cvNear(t, "the whole frame", img.RGBAAt(99, 59), blue, 0)
	cvNear(t, "the last frame", img.RGBAAt(5, 5), red, 0)
	// A chain with no surface reads nothing and draws nothing, without crashing.
	ch.close()
	if ch.lockRead() {
		t.Error("lockRead of a chain without surfaces")
	}
	ch.unlockRead()
	ch.failAlloc = true // nothing can be made either
	if _, err := ch.begin(rndPixRect{0, 0, 10, 10}); err == nil {
		t.Error("begin of a chain that cannot make a surface")
	}
}

// A steady frame on a window's surfaces allocates nothing either.
func TestRndSteadyFrameDoesNotAllocate(t *testing.T) {
	r := cvRes(t)
	o := rndSurfaceOff(t, r, 400, 200, 2)
	scene := func(c kit.Canvas) {
		c.FillRect(o.Bounds(), cvSurface)
		c.DrawShadow(kit.RoundRect(kit.R(20, 20, 120, 80), 8), kit.Shadow{Offset: kit.Pt(0, 4), Blur: 12, Color: kit.RGBA(0, 0, 0, 0.2)})
		c.FillRect(kit.R(140, 20, 160, 40), cvAccent)
	}
	rndFrame(t, o, o.Bounds(), scene)
	rndFrame(t, o, kit.R(0, 0, 200, 100), scene)
	allocs := testing.AllocsPerRun(20, func() { rndFrame(t, o, kit.R(10, 10, 60, 60), scene) })
	if allocs != 0 {
		t.Errorf("%v allocations per frame, want 0", allocs)
	}
}

// A large blurred shadow drawn at a fractional position is nine pieces that meet without a
// lighter seam: along its top band the shadow changes smoothly from pixel to pixel.
func TestRndShadowPiecesHaveNoSeams(t *testing.T) {
	r := cvRes(t)
	for _, ppd := range []float32{1, 2} {
		for _, dx := range []float32{0, 0.3, 0.5, 0.71} {
			o := cvOff(t, r, int(600*ppd), int(400*ppd), ppd)
			box := kit.R(100+dx, 100+dx, 500+dx, 300+dx)
			cvFrame(t, o, func(c kit.Canvas) {
				c.FillRect(o.Bounds(), kit.RGB(0xffffff))
				c.PushTransform(kit.Translation(dx/3, 0))
				c.DrawShadow(kit.RoundRect(box, 12), kit.Shadow{Blur: 40, Color: kit.RGBA(0, 0, 0, 0.6)})
				c.Pop()
			})
			img := cvPix(t, o)
			for _, yd := range []float32{90, 80, 300 + dx + 4} {
				y := int(yd * ppd)
				worst := 0
				for x := int(130 * ppd); x < int(470*ppd); x++ {
					d := absDiff(img.RGBAAt(x, y).R, img.RGBAAt(x+1, y).R)
					worst = max(worst, d)
				}
				if worst > 1 {
					t.Errorf("ppd %v dx %v row %v: neighbours differ by %d along the shadow's side", ppd, dx, yd, worst)
				}
			}
			for _, xd := range []float32{90, 500 + dx + 6} {
				x := int(xd * ppd)
				worst := 0
				for y := int(130 * ppd); y < int(270*ppd); y++ {
					worst = max(worst, absDiff(img.RGBAAt(x, y).R, img.RGBAAt(x, y+1).R))
				}
				if worst > 1 {
					t.Errorf("ppd %v dx %v column %v: neighbours differ by %d along the shadow's side", ppd, dx, xd, worst)
				}
			}
		}
	}
}

// rndJPEGWithOrientation is a w × h JPEG (left half red, right half blue) with an EXIF
// orientation tag.
func rndJPEGWithOrientation(t *testing.T, w, h int, orientation uint16) []byte {
	img := stdimage.NewRGBA(stdimage.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.RGBA{255, 0, 0, 255}
			if x >= w/2 {
				c = color.RGBA{0, 0, 255, 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	// APP1 "Exif\0\0", a big-endian TIFF header and one IFD entry: Orientation (0x0112),
	// SHORT, 1 value.
	tiff := []byte{'M', 'M', 0, 42, 0, 0, 0, 8, 0, 1, 0x01, 0x12, 0, 3, 0, 0, 0, 1,
		byte(orientation >> 8), byte(orientation), 0, 0, 0, 0, 0, 0}
	payload := append([]byte("Exif\x00\x00"), tiff...)
	app1 := []byte{0xff, 0xe1, byte((len(payload) + 2) >> 8), byte(len(payload) + 2)}
	app1 = append(app1, payload...)
	j := b.Bytes()
	out := append([]byte{}, j[:2]...)
	out = append(out, app1...)
	return append(out, j[2:]...)
}

// A photo is drawn upright by its EXIF orientation, as WebKit draws an <img>.
func TestRndImageOrientation(t *testing.T) {
	r := cvRes(t)
	type want struct {
		w, h       int
		topLeft    string // the colour at the upright image's top-left quarter
		bottomLeft string
	}
	cases := map[uint16]want{
		1: {64, 32, "red", "red"},
		3: {64, 32, "blue", "blue"}, // turned half round: blue on the left
		6: {32, 64, "red", "blue"},  // turned clockwise: the left half on top
		8: {32, 64, "blue", "red"},  // turned anticlockwise: the right half on top
		2: {64, 32, "blue", "blue"}, // mirrored
		5: {32, 64, "red", "blue"},  // transposed
		7: {32, 64, "blue", "red"},  // transversed
		4: {64, 32, "red", "red"},   // flipped upside down
	}
	name := func(c color.RGBA) string {
		switch {
		case c.R > 200 && c.B < 60:
			return "red"
		case c.B > 200 && c.R < 60:
			return "blue"
		}
		return "other"
	}
	for o, w := range cases {
		im, err := r.NewImage(rndJPEGWithOrientation(t, 64, 32, o))
		if err != nil {
			t.Fatalf("orientation %d: %v", o, err)
		}
		if gw, gh := im.PixelSize(); gw != w.w || gh != w.h {
			t.Errorf("orientation %d: %d×%d, want %d×%d", o, gw, gh, w.w, w.h)
		}
		off := cvOff(t, r, w.w, w.h, 1)
		cvFrame(t, off, func(c kit.Canvas) { c.DrawImage(im, kit.Rect{}, off.Bounds()) })
		img := cvPix(t, off)
		tl, bl := name(img.RGBAAt(w.w/4, w.h/4)), name(img.RGBAAt(w.w/4, w.h*3/4))
		if tl != w.topLeft || bl != w.bottomLeft {
			t.Errorf("orientation %d: top-left %s, bottom-left %s; want %s, %s", o, tl, bl, w.topLeft, w.bottomLeft)
		}
		im.Release()
	}
}

// A repeating gradient over more periods than are laid out paints all of the rectangle
// with its average colour (before, only the first 4096 periods were painted).
func TestRndRepeatingGradientOfManyPeriods(t *testing.T) {
	r := cvRes(t)
	o := cvOff(t, r, 200, 20, 1)
	black, white := kit.RGB(0), kit.RGB(0xffffff)
	stops := []kit.GradientStop{{Offset: 0, Color: black}, {Offset: 0.5, Color: black}, {Offset: 0.5, Color: white}, {Offset: 1, Color: white}}
	cvFrame(t, o, func(c kit.Canvas) {
		c.FillRect(o.Bounds(), kit.RGB(0xff0000))
		c.FillGradient(o.Bounds(), kit.LinearGradient{Start: kit.Pt(0, 0), End: kit.Pt(0.01, 0), Repeat: true, Stops: stops})
	})
	img := cvPix(t, o)
	for _, x := range []int{1, 40, 100, 199} {
		got := img.RGBAAt(x, 10)
		if absDiff(got.R, 128) > 2 || absDiff(got.G, 128) > 2 || absDiff(got.B, 128) > 2 {
			t.Errorf("x %d: %v, want the average grey", x, got)
		}
	}
	if c := averageColor([]kit.GradientStop{{Offset: 0, Color: kit.RGB(0xff0000)}, {Offset: 1, Color: kit.RGB(0xff0000).WithAlpha(0)}}, 0, 1); math.Abs(float64(c.R-1)) > 1e-6 || math.Abs(float64(c.A-0.5)) > 1e-6 {
		t.Errorf("premultiplied average: %+v", c)
	}
}

// A point far outside a layout (or a line height near zero) hits the nearest line instead
// of overflowing.
func TestRndHitTestFarAway(t *testing.T) {
	e := newEngine(t)
	l := lay(t, e, "one two three four five six", kit.TextStyle{Size: 13}, kit.TextOptions{MaxWidth: 40, Wrap: kit.TextWrapWords})
	last := l.HitTest(kit.Pt(0, 1e30))
	first := l.HitTest(kit.Pt(0, -1e30))
	if last.Pos <= first.Pos {
		t.Errorf("far below: %d, far above: %d", last.Pos, first.Pos)
	}
	l.lineH = 1e-38
	_ = l.HitTest(kit.Pt(0, 3e38)) // no panic, no wrapped index
}
