package progress

import (
	"math"

	"downex/ui/core/progress"
	"downex/ui/kit"
)

// The window fits what it shows (DESIGN-SYSTEM.md §4.8; ProgressWindow.svelte's fitWindow).
// The completion view, and the progress view without its details, get the window height
// their content needs: measured from the layout's boxes (fractional), plus the frame and a
// little slack (the host rounds window sizes to device pixels), so neither shows a needless
// scroll bar at 100, 125 or 150 % scaling. It fits again when the content changes size (a
// message, a fact, larger text, a narrower window: what the ResizeObserver saw) and when the
// display's scaling changes; not when the user resizes the window. Both are fitted exactly,
// the collapsed view never below its usual 300 px; on the Speed limit tab and on a switch
// between the tabs it only grows, so switching never moves the window or its buttons. The
// expanded view keeps its 560 px, where the connections scroll.

// fitState is fitWindow's memory.
type fitState struct {
	// due is a fit the effects asked for (queueFit); exact the collapsed view's fit after
	// the details were hidden (it fits even without a measure).
	due, exact bool
	// frame is the title bar and frame, taken once and again after the scaling changed.
	frame      kit.Size
	frameKnown bool
	// fittedTab is the tab the collapsed view was last fitted on.
	fittedTab string
	// observed is the needed height the last pass measured (the ResizeObserver's view of the
	// blocks), for the view observedView.
	observed     float64
	observedView progress.FitView
	observing    bool
	// sizes are the outer sizes asked for (tests and measurements read them).
	sizes []kit.Size
	// asked is the last outer size asked for while the window still has the client size
	// askedFrom: the host has it, the page not yet (a backend resizes after the call).
	asked, askedFrom kit.Size
	askedValid       bool
	// askedClient is the client size the host makes of asked (hostClient): the window has
	// the size asked for while its client area is that.
	askedClient kit.Size
}

// fitView is what the window shows now, if it is fitted to it ("" when not).
func (s *Screen) fitView() progress.FitView {
	if s.complete {
		return progress.ViewComplete
	}
	if !s.expanded && s.d() != nil {
		return progress.ViewCollapsed
	}
	return ""
}

// queueFit asks for a fit after the next layout pass.
func (s *Screen) queueFit(exact bool) {
	if s.destroyed {
		return
	}
	s.fit.due = true
	s.fit.exact = s.fit.exact || exact
	s.Relayout()
}

// LaidOut is kit.LayoutFollower's: after every layout pass, the fit the effects asked for,
// then the one a change of the measured content asks for.
func (s *Screen) LaidOut() {
	if s.destroyed || s.Window() == nil || s.Window().Host() == nil {
		return
	}
	view := s.fitView()
	if view == "" {
		s.fit.observing = false
		if s.fit.due && s.expanded && !s.complete {
			s.fit.due, s.fit.exact = false, false
		}
		return
	}
	content := s.neededHeight(view)
	changed := !s.fit.observing || s.fit.observedView != view || math.Abs(content-s.fit.observed) > 0.001
	s.fit.observed, s.fit.observedView, s.fit.observing = content, view, true
	if s.fit.due {
		exact := s.fit.exact
		s.fit.due, s.fit.exact = false, false
		s.fitWindow(view, content, exact)
	}
	if changed {
		s.fitWindow(view, content, false)
	}
}

// frameNow is the frame, taken once (and again after a scaling change).
func (s *Screen) frameNow() kit.Size {
	if !s.fit.frameKnown {
		s.fit.frame = s.pw.frameSize()
		s.fit.frameKnown = true
	}
	return s.fit.frame
}

// outer is the window's outer size as the host has it: the size last asked for until the
// window's size changes, else its client area and the frame.
func (s *Screen) outer() kit.Size {
	f := s.frameNow()
	c := s.pw.Kit.Size()
	if s.fit.askedValid {
		if c == s.fit.askedFrom || sameSize(c, s.fit.askedClient) {
			return s.fit.asked
		}
		s.fit.askedValid = false
	}
	return kit.Size{W: c.W + f.W, H: c.H + f.H}
}

// fitWindow is the script's fitWindow(exact) for the view shown and its measured content.
func (s *Screen) fitWindow(view progress.FitView, content float64, exact bool) {
	// Nothing measurable (no layout yet): the collapsed window keeps the size it opened with.
	if view == progress.ViewCollapsed && !(content > 0) && !exact {
		return
	}
	frame := s.frameNow()
	size := s.outer()
	height := progress.FitWindowHeight(view, content, float64(frame.H))
	// The page is today's 520 px wide (or wider when the user widened the window): the
	// client width, whatever the frame.
	width := s.outerWidth(size)
	// On the Speed limit tab, and on a switch between the tabs, it only grows: the tabs and
	// the buttons below them do not jump.
	switched := view == progress.ViewCollapsed && s.fit.fittedTab != "" && s.fit.fittedTab != s.tab
	if view == progress.ViewCollapsed {
		s.fit.fittedTab = s.tab
	}
	// The client area the host gives that size (device pixels truncated, as today's host).
	client := s.hostClient(kit.Size{W: kit.DIP(width), H: kit.DIP(height)}, frame)
	now := s.pw.Kit.Size()
	if view == progress.ViewCollapsed && !exact && (s.tab != tabStatus || switched) {
		if float64(now.H) >= float64(client.H)-0.01 {
			return
		}
	} else if same(float64(size.W), width) && same(float64(size.H), height) || sameSize(now, client) {
		return
	}
	s.setSize(width, height)
}

// outerWidth is the outer width of a window whose client area is today's page width
// (progress.Width, 520 px) or the wider one it has (outer less the frame).
func (s *Screen) outerWidth(outer kit.Size) float64 {
	f := float64(s.frameNow().W)
	return math.Max(float64(outer.W)-f, progress.Width) + f
}

func same(a, b float64) bool { return math.Abs(a-b) < 0.01 }

func sameSize(a, b kit.Size) bool {
	return same(float64(a.W), float64(b.W)) && same(float64(a.H), float64(b.H))
}

// resize is the details' fixed height, or the collapsed view fitted to its facts.
func (s *Screen) resize(details bool) {
	if !details {
		s.queueFit(true)
		return
	}
	s.setSize(s.outerWidth(s.outer()), progress.ExpandedHeight)
}

// setSize asks the host for an outer size (the client area is that less the frame).
func (s *Screen) setSize(width, height float64) {
	h := s.pw.Kit.Host()
	if h == nil {
		return
	}
	f := s.frameNow()
	out := kit.Size{W: kit.DIP(width), H: kit.DIP(height)}
	client := s.hostClient(out, f)
	s.fit.sizes = append(s.fit.sizes, out)
	s.fit.asked, s.fit.askedFrom, s.fit.askedClient, s.fit.askedValid = out, s.pw.Kit.Size(), client, true
	h.Resize(client)
}

// hostClient is the client area today's host gives a window asked to be out (DIPs) with
// the frame f. The width is the page's: out less the frame, exactly (outerWidth asks for
// today's 520 px page plus the frame; the host makes the outer width from it). The height:
// Wails' SetSize truncates the outer size to device pixels (winc ScaleWithDPI, (px × dpi) /
// 96) and the frame is whole device pixels, so the client is the truncated outer
// size less the frame's device pixels (lib/progress.ts fitSlack; refshots.mjs playHost). At
// 125 % a 302 px window is 377 device pixels, not 377.5 rounded up; the page is 1 device pixel
// shorter than the outer size less the frame in DIPs.
func (s *Screen) hostClient(out, f kit.Size) kit.Size {
	ppd := float64(s.pw.Kit.Environment().Scale.PixelsPerDIP)
	if !(ppd > 0) || math.IsInf(ppd, 0) {
		return kit.Size{W: out.W - f.W, H: out.H - f.H}
	}
	dev := func(o, fr kit.DIP) kit.DIP {
		// 1e-6: a DIP size that is a whole number of device pixels in exact arithmetic
		// stays that number after float32 rounding.
		px := math.Floor(float64(o)*ppd+1e-6) - math.Round(float64(fr)*ppd)
		return kit.DIP(math.Max(px, 1) / ppd)
	}
	return kit.Size{W: out.W - f.W, H: dev(out.H, f.H)}
}

// neededHeight is the page height that shows all of the scrolling part's content without
// scrolling: the page's other blocks as laid out, the scroller's content, the padding and the
// gaps (0 = not measurable).
func (s *Screen) neededHeight(view progress.FitView) float64 {
	var scroller *kit.Element
	switch view {
	case progress.ViewComplete:
		scroller = &s.done.Element
	case progress.ViewCollapsed:
		scroller = &s.panel.Element
	}
	var sum float64
	n := 0
	for _, c := range s.Children() {
		if c.Style.Hidden {
			continue
		}
		n++
		if c == scroller {
			_, content, _ := scrollContent(c)
			sum += float64(content) + float64(marginBlock(c))
			continue
		}
		sum += float64(c.Size().H)
	}
	if !(sum > 0) {
		return 0
	}
	return sum + 12 + 12 + 10*float64(max(0, n-1))
}

// marginBlock is e's top and bottom margins in DIPs (the complete view's negative ones give
// back the room its body keeps for the focus ring, so the window keeps its size).
func marginBlock(e *kit.Element) kit.DIP {
	var sum kit.DIP
	for _, m := range []kit.Length{e.Style.Margin.Top, e.Style.Margin.Bottom} {
		if v, ok := m.Resolve(kit.Scale{}, -1); ok {
			sum += v
		}
	}
	return sum
}

// scrollContent is a scroll pane's range.
func scrollContent(e *kit.Element) (offset, content, view kit.DIP) {
	if t, ok := e.Widget().(kit.ScrollTarget); ok {
		return t.ScrollRange()
	}
	return 0, e.Size().H, e.Size().H
}
