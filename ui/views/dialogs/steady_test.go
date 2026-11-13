package dialogs_test

import (
	"testing"
	"time"

	"downex/ui/kit"
	"downex/ui/views/viewstest"
)

// nopCanvas draws nothing and records nothing, so that what allocates is the views' own.
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

// TestSteadyAndIdle: once a dialog has settled, a repaint of the window allocates nothing,
// and an idle window paints nothing (every state of this package, in its scene).
func TestSteadyAndIdle(t *testing.T) {
	for _, name := range dialogStates {
		t.Run(name, func(t *testing.T) {
			h := viewstest.New(t, viewstest.Options{Fixture: name})
			openState(t, h, name)
			h.Advance(time.Second)
			full := kit.RectAt(kit.Point{}, h.Window.Size())
			var c nopCanvas
			if n := testing.AllocsPerRun(50, func() { h.Window.Paint(c, full) }); n != 0 {
				t.Errorf("a repaint allocates %.1f times", n)
			}
			// the scene's downloads keep moving under the dialog: none of it reaches the dialog
			h.Window.ResetStats()
			h.Advance(2 * time.Second)
			if s := h.Window.Stats(); s.Paints != 0 || s.Layouts != 0 {
				t.Errorf("idle: %d paints, %d layouts", s.Paints, s.Layouts)
			}
		})
	}
}
