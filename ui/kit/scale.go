package kit

import "math"

// RemDIPs is 1rem at the default text size: the browser's default font size, 16 px.
// app.css leaves the root font size at the user's setting, so its rem tokens scale with it.
const RemDIPs = 16

// Scale maps a window's DIPs to its device pixels and rem to DIPs. The zero value acts as
// a 100% monitor with the default text size.
type Scale struct {
	// PixelsPerDIP is the monitor's DPI over 96: 1.25 on a 125% monitor. Zero counts as 1.
	PixelsPerDIP float32
	// Text is the Windows text-size factor (Settings › Accessibility › Text size, 1 to
	// 2.25). It scales 1rem, so text and rem-based sizes grow while px-based sizes stay,
	// as in the WebView window: 1rem is 16 DIPs at the default size and 20 at 125%.
	// Zero counts as 1.
	Text float32
}

func (s Scale) ppd() float32 {
	if s.PixelsPerDIP > 0 {
		return s.PixelsPerDIP
	}
	return 1
}

func (s Scale) text() float32 {
	if s.Text > 0 {
		return s.Text
	}
	return 1
}

// RemBase is 1rem in DIPs: RemDIPs × Text, which is RemDIPs × Text × PixelsPerDIP device
// pixels. It is not rounded.
func (s Scale) RemBase() DIP { return RemDIPs * s.text() }

// Rem is n rem in DIPs.
func (s Scale) Rem(n float32) DIP { return n * s.RemBase() }

// Pixels is d in device pixels.
func (s Scale) Pixels(d DIP) float32 { return d * s.ppd() }

// FromPixels is px device pixels in DIPs.
func (s Scale) FromPixels(px float32) DIP { return px / s.ppd() }

// Snap is d moved to the nearest device-pixel boundary, for an edge that must be sharp.
func (s Scale) Snap(d DIP) DIP {
	p := s.ppd()
	return float32(math.Round(float64(d*p))) / p
}

// SnapRect is r with each edge snapped (see Snap), so rectangles that share an edge
// still share it.
func (s Scale) SnapRect(r Rect) Rect {
	return Rect{Point{s.Snap(r.Min.X), s.Snap(r.Min.Y)}, Point{s.Snap(r.Max.X), s.Snap(r.Max.Y)}}
}

// SnapWidth is the width of a line or border of width w as CSS draws it ("snap as a
// border width"): rounded down to whole device pixels, but at least one device pixel for
// any w above 0, so a 1px border is one device pixel wide at 125% too. A w of 0 or less
// is 0.
func (s Scale) SnapWidth(w DIP) DIP {
	if !(w > 0) {
		return 0
	}
	p := s.ppd()
	px := max(float32(math.Floor(float64(w*p))), 1)
	return px / p
}
