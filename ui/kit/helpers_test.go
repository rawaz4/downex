package kit_test

import (
	"math"
	"testing"

	"downex/ui/kit"
	"downex/ui/kit/kittest"
)

// env100 is a 100% monitor with the default text size; env125 the owner's primary monitor.
var (
	env100 = kit.Environment{Scale: kit.Scale{PixelsPerDIP: 1, Text: 1}}
	env125 = kit.Environment{Scale: kit.Scale{PixelsPerDIP: 1.25, Text: 1}}
)

// testPalette has a colour for everything the kit draws.
var testPalette = kit.Palette{
	Ground:            kit.RGB(0xeef2f0),
	Text:              kit.RGB(0x0f1613),
	Focus:             kit.RGB(0x10804f),
	ScrollThumb:       kit.RGB(0xc9d1cd),
	ScrollThumbActive: kit.RGB(0x959b98),
}

// open makes a window of content, attaches it to a fake host through a loop and lets the
// first frame happen.
func open(t testing.TB, content kit.Widget, size kit.Size, env kit.Environment, tune ...func(*kit.WindowOptions)) (*kit.Window, *kittest.Loop) {
	t.Helper()
	clock := kittest.NewClock()
	opts := kit.WindowOptions{
		Title:     "Downex",
		Now:       clock.Now,
		Scheduler: clock,
		Palette:   func(kit.Environment) kit.Palette { return testPalette },
	}
	for _, f := range tune {
		f(&opts)
	}
	w := kit.NewWindow(content, opts)
	loop := kittest.NewLoop(w, clock, size, env)
	loop.Step()
	return w, loop
}

// box is an element with a style.
func box(st kit.Style, kids ...kit.Widget) *kit.Element {
	e := &kit.Element{Style: st}
	e.Add(kids...)
	return e
}

// leaf is a fixed-size box.
func leaf(w, h float32) *kit.Element {
	return &kit.Element{Style: kit.Style{Width: kit.Px(w), Height: kit.Px(h)}}
}

func approx(a, b float32) bool { return math.Abs(float64(a-b)) < 0.01 }

func approxRect(a, b kit.Rect) bool {
	return approx(a.Min.X, b.Min.X) && approx(a.Min.Y, b.Min.Y) && approx(a.Max.X, b.Max.X) && approx(a.Max.Y, b.Max.Y)
}

// font13 is app.css's body text: 13 px (0.8125rem) at 400 with a 1.4 line height.
var font13 = kit.Font{Family: "'Segoe UI Variable Text', 'Segoe UI', system-ui", Size: kit.Rem(0.8125), LineHeight: 1.4}
