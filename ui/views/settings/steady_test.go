package settings_test

import (
	"strings"
	"testing"
	"time"

	"downex/ui/kit"
	"downex/ui/kit/icons"
	"downex/ui/views/viewstest"
)

// nopCanvas draws nothing and records nothing, so that what allocates is the pages' own.
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

// TestSteadyAndIdle: once a Settings page has settled, a repaint of the window allocates
// nothing, and an idle window paints and lays out nothing (every Settings state, in its
// scene, light and dark, at 16 and 20 px text).
func TestSteadyAndIdle(t *testing.T) {
	for _, name := range settingsStates {
		for _, v := range []struct {
			theme string
			font  int
		}{{"light", 16}, {"dark", 20}} {
			t.Run(name+"-"+v.theme, func(t *testing.T) {
				h := viewstest.New(t, viewstest.Options{Fixture: name, Theme: v.theme, Font: v.font})
				openState(t, h, name)
				h.Advance(time.Second)
				full := kit.RectAt(kit.Point{}, h.Window.Size())
				var c nopCanvas
				// About draws the logo with kit/icons' DrawBrand, which builds its bars' path
				// keys with fmt on every call (kit/icons' own; listed in needs): those
				// allocations are measured alone and set apart, so that the pages' own must be 0.
				var brand float64
				if strings.HasPrefix(name, "settings-about") && name != "settings-about-notices" {
					set := icons.NewSet(h.Window.Host().Resources())
					defer set.Release()
					tk := h.App.Tokens()
					brand = testing.AllocsPerRun(50, func() { set.DrawBrand(c, kit.Point{}, 44, tk) })
				}
				if raceEnabled {
					// the detector's own allocations: counted only without -race
				} else if n := testing.AllocsPerRun(50, func() { h.Window.Paint(c, full) }); n != brand {
					t.Errorf("a repaint allocates %.1f times (%.1f of them kit/icons' DrawBrand)", n, brand)
				} else if brand > 0 {
					t.Logf("a repaint allocates only in kit/icons' DrawBrand (%.0f times)", brand)
				}
				// the scene's downloads keep moving under the dialog: none of it reaches it
				h.Window.ResetStats()
				h.Advance(2 * time.Second)
				if s := h.Window.Stats(); s.Paints != 0 || s.Layouts != 0 {
					t.Errorf("idle: %d paints, %d layouts", s.Paints, s.Layouts)
				}
			})
		}
	}
}
