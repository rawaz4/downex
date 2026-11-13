//go:build darwin && cgo

package mac

// [popups]

import (
	"os"
	"testing"
	"time"

	"downex/ui/kit"
	"downex/ui/mac/popuptest"
)

func TestTooltips(t *testing.T) {
	owner := &recRoot{ground: kit.RGBA(240, 240, 240, 1)}
	w := openTestWindow(t, owner, kit.Size{W: 400, H: 300})
	asked := 0
	area := kit.R(20, 20, 120, 60)
	source := func(p kit.Point) (string, kit.Rect, bool) {
		asked++
		if area.Contains(p) {
			return "Whole file name.iso", area, true
		}
		return "", kit.Rect{}, false
	}
	mainDo(func() {
		w.SetTooltipSource(source)
		if tipsOf[w] == nil || tipsOf[w].view == nil {
			t.Fatal("no tooltip view")
		}
	})

	// A move over the element arms its text (the application's monitor sees AppKit's move).
	mainDo(func() { w.postMouse(evMoved, kit.Pt(50, 40), 0) })
	pump(100 * time.Millisecond)
	mainDo(func() {
		if got := w.tooltipText(); got != "Whole file name.iso" {
			t.Fatalf("armed %q after a move over the element (asked %d)", got, asked)
		}
	})
	// AppKit's tooltip manager follows the real pointer, which posted events do not move:
	// TestTooltipOnScreen checks what is shown.

	// Moves inside the area ask nothing; the same point again asks nothing.
	mainDo(func() {
		n := asked
		w.tipMoved(kit.Pt(60, 41))
		w.tipMoved(kit.Pt(60, 41))
		if asked != n {
			t.Errorf("moves inside the shown area asked the source %d times", asked-n)
		}
		// Leaving the area clears it; another element arms again.
		w.tipMoved(kit.Pt(300, 200))
		if w.tooltipText() != "" {
			t.Errorf("still armed %q outside the area", w.tooltipText())
		}
		w.tipMoved(kit.Pt(30, 30))
		if w.tooltipText() == "" {
			t.Error("not armed again over the element")
		}
		// The pointer leaving the window forgets it; coming back arms it again.
		w.tipLeft()
		if w.tooltipText() != "" {
			t.Error("armed after the pointer left")
		}
		w.tipMoved(kit.Pt(30, 30))
		if w.tooltipText() == "" {
			t.Error("not armed after the pointer came back")
		}
		// The source runs as a call into the root: never inside another. A move while one is
		// in progress (a modal loop run from it) changes nothing, and the next asks again.
		n = asked
		w.call(func() {
			w.tipMoved(kit.Pt(300, 200))
			if asked != n || w.tooltipText() == "" {
				t.Errorf("a move inside a root call asked %d times, armed %q", asked-n, w.tooltipText())
			}
		})
		if w.tooltipText() == "" {
			t.Error("the move inside the root call cleared the tooltip")
		}
		w.tipMoved(kit.Pt(300, 200))
		if asked != n+1 || w.tooltipText() != "" {
			t.Errorf("the next move: asked %d, armed %q", asked-n, w.tooltipText())
		}
	})

	mainDo(func() {
		w.SetTooltipSource(nil)
		if tipsOf[w] != nil {
			t.Error("the tooltip stayed after SetTooltipSource(nil)")
		}
		w.SetTooltipSource(source) // again, then the window's close removes it
	})
	mainDo(func() { w.Close() })
	pump(100 * time.Millisecond)
	mainDo(func() {
		if len(tipsOf) != 0 {
			t.Errorf("%d tooltips left after the window closed", len(tipsOf))
		}
	})
}

// TestTooltipOnScreen moves the real pointer (DOWNEX_REAL_POINTER=1; the application must be
// active, as for every tooltip): AppKit shows the tooltip after the system's delay when the
// pointer stops over an element with a title, and again when it moves on to another
// element with another title without leaving the window.
func TestTooltipOnScreen(t *testing.T) {
	if os.Getenv("DOWNEX_REAL_POINTER") == "" {
		t.Skip("moves the real pointer: set DOWNEX_REAL_POINTER=1")
	}
	w := openTestWindow(t, &recRoot{ground: kit.RGBA(240, 240, 240, 1)}, kit.Size{W: 400, H: 300})
	a, b := kit.R(20, 20, 120, 60), kit.R(160, 20, 260, 60)
	mainDo(func() {
		w.SetTooltipSource(func(p kit.Point) (string, kit.Rect, bool) {
			switch {
			case a.Contains(p):
				return "Whole file name.iso", a, true
			case b.Contains(p):
				return "Another name.zip", b, true
			}
			return "", kit.Rect{}, false
		})
	})
	pumpUntil(2*time.Second, popuptest.Active)
	if !popuptest.Active() {
		t.Skip("the test process is not the active application: AppKit shows no tooltips")
	}
	moveTo := func(p kit.Point) {
		mainDo(func() {
			popuptest.Warp(w.ns, float64(p.X), float64(p.Y))
			w.postMouse(evMoved, p, 0)
		})
	}
	moveTo(kit.Pt(350, 250))
	pump(300 * time.Millisecond)
	for _, c := range []struct {
		at   kit.Point
		text string
	}{{kit.Pt(50, 40), "Whole file name.iso"}, {kit.Pt(200, 40), "Another name.zip"}} {
		start := time.Now()
		moveTo(c.at)
		pump(50 * time.Millisecond)
		mainDo(func() {
			if got := w.tooltipText(); got != c.text {
				t.Errorf("armed %q at %v, want %q", got, c.at, c.text)
			}
		})
		if !pumpUntil(4*time.Second, popuptest.TooltipShown) {
			t.Fatalf("AppKit showed no tooltip for %q", c.text)
		}
		t.Logf("%q shown after %v", c.text, time.Since(start).Round(10*time.Millisecond))
	}
	// Leaving every element hides it.
	moveTo(kit.Pt(350, 250))
	if !pumpUntil(2*time.Second, func() bool { return !popuptest.TooltipShown() }) {
		t.Error("the tooltip stayed over no element")
	}
}
