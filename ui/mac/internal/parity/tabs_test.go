//go:build darwin && cgo

package parity

// [mac webkit] Tab labels with the Mac's real text (kittest's metrics ignore the weight).

import (
	"math"
	"testing"

	"downex/ui/kit"
	"downex/ui/kit/hostcap/hostcaptest"
	"downex/ui/kit/kittest"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/mac"
)

func labelIn(e *kit.Element, text string) *kit.Element {
	for _, c := range e.Children() {
		if l, ok := c.Widget().(*kit.Label); ok && l.Text() == text {
			return c
		}
		if f := labelIn(c, text); f != nil {
			return f
		}
	}
	return nil
}

// An inactive tab's 500-weight label starts at its bold twin's start in the Chromium look
// (Windows, unchanged) and is centred in the twin's width under WebKit, as Tabs.svelte's
// inline-grid .label in a centred button is: "Speed limit" moves (70.92 − 69.27) / 2 pt.
func TestTabLabelCentredWebKit(t *testing.T) {
	res, err := mac.NewResources()
	if err != nil {
		t.Skip(err)
	}
	for _, webkit := range []bool{false, true} {
		choice := &widgets.Choice{Choice: tokens.ChoiceLight}
		tabs := widgets.NewTabs([]widgets.TabOption{{Value: "status", Label: "Status"}, {Value: "limit", Label: "Speed limit"}}, "status",
			widgets.TabsOptions{AriaLabel: "Progress view", IDPrefix: "pw", Panel: &kit.Element{}}, nil)
		root := widgets.NewScope(choice, tabs)
		clock := kittest.NewClock()
		win := kit.NewWindow(root, kit.WindowOptions{Title: "tabs", Palette: func(env kit.Environment) kit.Palette { return choice.Tokens(env).Palette() }, Now: clock.Now, Scheduler: clock})
		h := hostcaptest.NewHost(kit.Size{W: 500, H: 200}, kit.Environment{Scale: kit.Scale{PixelsPerDIP: 2, Text: 1}, WebKit: webkit}, clock)
		loop := &kittest.Loop{Host: h.Host, Root: win, Canvas: &kittest.Canvas{}, Clock: clock}
		win.Attach(&realHost{Host: h, res: res})
		loop.Settle(60)
		lab := labelIn(tabs.TabWidget("limit").Base(), "Speed limit")
		box, got := lab.Parent().WindowBounds(), lab.WindowBounds()
		shift := got.Min.X - box.Min.X
		t.Logf("webkit %v: twin %.2f wide, label %.2f, shift %.3f", webkit, box.Width(), got.Width(), shift)
		want := kit.DIP(0)
		if webkit {
			want = (box.Width() - got.Width()) / 2
			if math.Abs(float64(want-0.82)) > 0.05 {
				t.Errorf("WebKit look: shift %.3f, the reference's 0.82", want)
			}
		}
		if math.Abs(float64(shift-want)) > 0.001 {
			t.Errorf("webkit %v: label shifted %.3f, want %.3f", webkit, shift, want)
		}
		win.Detach()
	}
}
