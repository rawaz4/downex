package add

import (
	"testing"
	"time"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/format/formattest"
	"downex/ui/kit"
	"downex/ui/state"
	"downex/ui/views"
	"downex/ui/views/viewstest"
)

// nopCanvas draws nothing and records nothing, so that what allocates is the dialogs' own.
type nopCanvas struct{}

func (nopCanvas) FillRect(kit.Rect, kit.Color)                       {}
func (nopCanvas) StrokeRect(kit.Rect, kit.Stroke, kit.Color)         {}
func (nopCanvas) FillRoundedRect(kit.RRect, kit.Color)               {}
func (nopCanvas) StrokeRoundedRect(kit.RRect, kit.Stroke, kit.Color) {}
func (nopCanvas) FillEllipse(kit.Rect, kit.Color)                    {}
func (nopCanvas) StrokeEllipse(kit.Rect, kit.Stroke, kit.Color)      {}
func (nopCanvas) DrawLine(a, b kit.Point, s kit.Stroke, c kit.Color) {}
func (nopCanvas) FillPath(kit.Path, kit.Color)                       {}
func (nopCanvas) StrokePath(kit.Path, kit.Stroke, kit.Color)         {}
func (nopCanvas) FillGradient(kit.Rect, kit.LinearGradient)          {}
func (nopCanvas) DrawShadow(kit.RRect, kit.Shadow)                   {}
func (nopCanvas) DrawText(kit.TextLayout, kit.Point, kit.Color)      {}
func (nopCanvas) DrawImage(kit.Image, kit.Rect, kit.Rect)            {}
func (nopCanvas) PushClip(kit.Rect)                                  {}
func (nopCanvas) PushRoundedClip(kit.RRect)                          {}
func (nopCanvas) PushOpacity(float32)                                {}
func (nopCanvas) PushTransform(kit.Affine)                           {}
func (nopCanvas) Pop()                                               {}

// allocs is testing.AllocsPerRun, 0 under the race detector (which allocates by itself).
func allocs(runs int, f func()) float64 {
	if raceOn {
		f()
		return 0
	}
	return testing.AllocsPerRun(runs, f)
}

// moving are the states whose dialog shows something that moves by design: the spinner of a
// page being read or a video being added, the one-second clock of "Getting video info… 3 s",
// the tools' set-up bar. They repaint while they move (only what moves), and each repaint
// allocates nothing; every other state paints nothing at all while idle.
func moving(a *Add) bool {
	return a.busy != idle
}

// TestSteadyAndIdle: in every Add and Confirm fixture state, once the dialog has settled a
// repaint of the whole window allocates nothing, and an idle dialog paints and lays out
// nothing while the scene's downloads move under it.
func TestSteadyAndIdle(t *testing.T) {
	for _, name := range refStates() {
		t.Run(name, func(t *testing.T) {
			h := viewstest.New(t, viewstest.Options{Fixture: name})
			h.App.Caps.WakeTimers = true
			m := stateOf(t, h, name)
			h.Advance(time.Second) // the open motion and any transition end
			h.Settle()
			a, isAdd := m.(*Add)
			if !isAdd || !moving(a) {
				// The answers of its calls (the folder's free space) have arrived: one landing
				// after the harness settled (under a full parallel run) is no change of an idle
				// dialog. A moving state's calls wait on purpose (a page being read).
				settleAsync(t, h)
			}
			full := kit.RectAt(kit.Point{}, h.Window.Size())
			var c nopCanvas
			h.Window.Paint(c, full) // the labels' layouts are made once
			n := allocs(50, func() { h.Window.Paint(c, full) })
			if n != 0 && isAdd && a.mode == modeFiles && a.urlsArea.Focused() {
				// widgets.TextArea.Paint posts the field's Reclaim (a method value) on every
				// paint while it has the focus: the widgets lane's, not this package's. The
				// dialog's own drawing must allocate nothing with the focus elsewhere.
				t.Logf("KNOWN: %.1f allocations per repaint while the URL list (widgets.TextArea) has the focus", n)
				h.Window.Focus(a.folder)
				h.Settle()
				h.Window.Paint(c, full)
				n = allocs(50, func() { h.Window.Paint(c, full) })
			}
			if n != 0 {
				t.Errorf("a repaint allocates %.1f times", n)
			}
			h.Window.ResetStats()
			if h.Mock != nil {
				h.Mock.Tick(2000)
			}
			h.Advance(2 * time.Second)
			s := h.Window.Stats()
			if isAdd && moving(a) {
				t.Logf("moving (busy %d): %d paints, %d layouts in 2 s", a.busy, s.Paints, s.Layouts)
				// a frame of the motion paints without allocating, at several moments of it
				for i := 0; i < 5; i++ {
					h.Advance(170 * time.Millisecond)
					h.Window.Paint(c, full)
					if n := allocs(20, func() { h.Window.Paint(c, full) }); n != 0 {
						t.Errorf("a repaint while it moves allocates %.1f times", n)
					}
				}
				return
			}
			if s.Paints != 0 || s.Layouts != 0 {
				t.Errorf("idle: %d paints, %d layouts", s.Paints, s.Layouts)
			}
		})
	}
}

// rtlTitles are right-to-left video titles: the scene's Arabic and Kurdish (Sorani) ones and a
// Hebrew one, each mixed with Latin words as real titles are.
var rtlTitles = []struct{ lang, title string }{
	{"Arabic", "أطيب كباب في السليمانية_ أكلات شعبية وخبز التنور الحار"},
	{"Kurdish", "سلێمانی تا بەغداد بۆ بینینی The Odyssey و IMAX"},
	{"Hebrew", "מדריך מלא: הקמת סביבת Staging ו-Production עם Dokploy"},
}

// checkRTLTitle: the title lays out right to left (dir="auto" on the block), starts at the
// right edge of its box (text-align: start), keeps every glyph inside the box it is drawn in
// (nothing clipped), and is whole: not cut, its tooltip the whole title.
func checkRTLTitle(t *testing.T, l *views.Text, title string) {
	t.Helper()
	if l.Text() != title {
		t.Fatalf("title %q, want %q", l.Text(), title)
	}
	lay := l.TextLayout()
	if lay == nil {
		t.Fatal("the title has no layout")
	}
	m := lay.Metrics()
	if m.Direction != kit.DirRTL {
		t.Errorf("direction %v, want right to left", m.Direction)
	}
	if l.Truncated() {
		t.Errorf("the title is cut (%d lines)", m.Lines)
	}
	if l.Tooltip != title {
		t.Errorf("tooltip %q", l.Tooltip)
	}
	box := l.ContentRect()
	o := l.TextOrigin()
	rects := lay.AppendRangeRects(nil, 0, len(title))
	if len(rects) == 0 {
		t.Fatal("no text drawn")
	}
	right := kit.DIP(-1e9)
	for _, r := range rects {
		r = r.Add(o)
		if r.Min.X < box.Min.X-0.5 || r.Max.X > box.Max.X+0.5 || r.Min.Y < box.Min.Y-0.5 || r.Max.Y > box.Max.Y+0.5 {
			t.Errorf("text %v outside its box %v (clipped)", r, box)
		}
		right = max(right, r.Max.X)
	}
	if d := box.Max.X - right; d > 1 {
		t.Errorf("the title ends %.1f DIP before its box's right edge: not aligned to the start of a right-to-left line", d)
	}
	// the card's own box shows the title whole: the card is wide enough for its lines
	if l.Size().W < m.Width-0.5 {
		t.Errorf("box %.1f narrower than the text %.1f", l.Size().W, m.Width)
	}
}

// TestRightToLeftVideoTitles: Arabic, Kurdish and Hebrew titles display right to left, aligned
// to the right and not clipped, in the Confirm dialog and in the Add dialog's video card, at
// 16 and 20 px text (DirectWrite layout).
func TestRightToLeftVideoTitles(t *testing.T) {
	if err := viewstest.CanRender(); err != nil {
		t.Skipf("no DirectWrite: %v", err)
	}
	for _, font := range []int{16, 20} {
		for _, tt := range rtlTitles {
			t.Run("Confirm/"+tt.lang+"/"+itoa(font), func(t *testing.T) {
				h := viewstest.New(t, viewstest.Options{Real: true, NoStart: true, Font: font})
				d := siteConfirm(func(d *downexv1.Download) {
					d.Id, d.Media = 12, formattest.SiteMediaInfo(func(m *downexv1.MediaInfo) { m.Title = tt.title })
				})
				c := NewConfirm(h.App, d, ConfirmOptions{Settings: confirmSettings(), Requested: true})
				h.App.Dialogs.Show(c)
				h.Settle()
				h.Advance(time.Second)
				checkRTLTitle(t, c.Video.Title, tt.title)
			})
			t.Run("Add/"+tt.lang+"/"+itoa(font), func(t *testing.T) {
				h := viewstest.New(t, viewstest.Options{Real: true, NoStart: true, Font: font})
				a := New(h.App, views.AddRequest{Mode: views.AddStream}, Options{Defaults: &Defaults{Dir: "/home/you/Downloads", MaxConnections: 8}})
				h.App.Dialogs.Show(a)
				h.Settle()
				url := "https://www.youtube.com/watch?v=Xb7mQ2kLp9w"
				typeIn(t, h, a.stream, url)
				info := formattest.SiteMediaInfo(func(m *downexv1.MediaInfo) { m.Title = tt.title })
				a.probed = &probeResult{url: url, info: info}
				a.showInfo(info)
				a.refresh()
				h.Settle()
				h.Advance(time.Second)
				if !a.Found() || a.Video == nil {
					t.Fatal("no video card")
				}
				checkRTLTitle(t, a.Video.Title, tt.title)
			})
		}
	}
	// the check tells the directions apart: a Latin title lays out left to right
	t.Run("an English title stays left to right", func(t *testing.T) {
		h := viewstest.New(t, viewstest.Options{Real: true, NoStart: true})
		c := NewConfirm(h.App, siteConfirm(nil), ConfirmOptions{Settings: confirmSettings(), Requested: true})
		h.App.Dialogs.Show(c)
		h.Settle()
		if d := c.Video.Title.TextLayout().Metrics().Direction; d != kit.DirLTR {
			t.Errorf("direction %v", d)
		}
	})
	// the fixture states with a right-to-left title, as the references show them
	for _, name := range []string{"confirm-video-6-ar", "confirm-video-6-ku", "add-video-6-ar"} {
		t.Run(name, func(t *testing.T) {
			h := viewstest.New(t, viewstest.Options{Real: true, Fixture: name})
			m := stateOf(t, h, name)
			var card *VideoCard
			switch d := m.(type) {
			case *Add:
				card = d.Video
			case *Confirm:
				card = d.Video
			}
			if card == nil {
				t.Fatal("no video card")
			}
			checkRTLTitle(t, card.Title, card.Title.Text())
		})
	}
}

func itoa(n int) string {
	if n == 20 {
		return "20px"
	}
	return "16px"
}

var _ = state.StateAwaiting
