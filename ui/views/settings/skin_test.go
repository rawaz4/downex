package settings_test

import (
	"fmt"
	"testing"

	"downex/ui/kit"
	"downex/ui/views/viewstest"
)

// The Settings window's own design since 2026-11-10 (the owner's decision): Windows 11's
// Settings on Windows, macOS's System Settings on the Mac. The two earlier pixel comparisons
// with the WebView window's references no longer apply to it.

// skins are the two looks: the Windows host's environment and the macOS host's.
var skins = []struct {
	name   string
	webKit bool
}{{"windows", false}, {"mac", true}}

// TestStatesFitTheWindow: every Settings state, in both looks, at the reference window sizes
// and text sizes, keeps the dialog inside the room the window has, at its width (or the room),
// and nothing in the pane reaches past the pane's content box (a control or text cut at the
// end). One size per state is also drawn with the platform's renderer.
func TestStatesFitTheWindow(t *testing.T) {
	sizes := []struct{ w, h, font int }{{1200, 760, 16}, {900, 600, 16}, {1920, 1080, 20}, {1200, 760, 20}, {900, 600, 20}}
	render := viewstest.CanRender() == nil
	for _, sk := range skins {
		for _, name := range settingsStates {
			for i, sz := range sizes {
				t.Run(fmt.Sprintf("%s/%s/%dx%d@%dpx", sk.name, name, sz.w, sz.h, sz.font), func(t *testing.T) {
					real := render && i == 0
					h := viewstest.New(t, viewstest.Options{Width: sz.w, Height: sz.h, Font: sz.font, Theme: "dark", Fixture: name, WebKit: sk.webKit, Real: real})
					d := openState(t, h, name)
					card := d.Card().WindowBounds()
					if card.Min.Y < h.App.ModalTop()-0.5 || card.Max.Y > kit.DIP(sz.h)+0.5 || card.Min.X < -0.5 || card.Max.X > kit.DIP(sz.w)+0.5 {
						t.Errorf("the dialog %v leaves the %dx%d window", card, sz.w, sz.h)
					}
					rem := kit.DIP(sz.font)
					want := min(60*rem, kit.DIP(sz.w-32))
					if sk.webKit {
						want = min(52*rem, kit.DIP(sz.w-32))
					}
					if d := card.Width() - want; d > 0.5 || d < -0.5 {
						t.Errorf("the dialog is %.1f wide, want %.1f", card.Width(), want)
					}
					// narrow (the tabs in a row), the dialog's own header shows the title and close
					if narrow := card.Width() <= 44*rem+0.5; d.HeaderHidden() == narrow {
						t.Errorf("narrow %v: the dialog's header hidden %v", narrow, d.HeaderHidden())
					}
					pane := d.Pane().Base()
					pr := pane.WindowBounds()
					right := pr.Max.X - kit.DIP(d.Pane().Scrollbar().Metrics.Thickness)
					walk(pane, func(e *kit.Element) bool {
						if e == pane || e.Style.Hidden {
							return !e.Style.Hidden
						}
						if _, bar := e.Widget().(*kit.Scrollbar); bar {
							return false
						}
						// the pane's own boxes start at its edge; the page's content is inset
						if b := e.WindowBounds(); b.Width() > 0 && b.Min.X > pr.Min.X+1 && b.Max.X > right+0.5 {
							t.Errorf("%T %q reaches %.1f, past the pane's %.1f", e.Widget(), textOf(e), b.Max.X, right)
							return false
						}
						return true
					})
					if real {
						if _, err := h.Render(); err != nil {
							t.Fatal(err)
						}
					}
				})
			}
		}
	}
}

// TestSkinRules: General at 1200 × 760 in each look: the sizes that make it Windows 11's or
// macOS's.
func TestSkinRules(t *testing.T) {
	for _, sk := range skins {
		t.Run(sk.name, func(t *testing.T) {
			h := viewstest.New(t, viewstest.Options{Width: 1200, Height: 760, Theme: "dark", Fixture: "settings-general", WebKit: sk.webKit})
			d := openState(t, h, "settings-general")
			var switches, titles []*kit.Element
			walk(d.Pane().Base(), func(e *kit.Element) bool {
				switch {
				case e.Semantics.Role == kit.RoleSwitch:
					switches = append(switches, e)
				case e.Semantics.Role == kit.RoleHeading && e.Semantics.Level == 2:
					titles = append(titles, e)
				}
				return true
			})
			if len(switches) != 6 {
				t.Fatalf("%d switches, want General's 6", len(switches))
			}
			wantSw, rowH, tabH := kit.Size{W: 40, H: 20}, kit.DIP(62), kit.DIP(36)
			if sk.webKit {
				wantSw, rowH, tabH = kit.Size{W: 38, H: 22}, 44, 30
			}
			for _, s := range switches {
				if s.Size() != wantSw {
					t.Errorf("a switch is %v, want %v", s.Size(), wantSw)
				}
				row := s.Parent().Parent() // the switch's SwitchRow, then its row
				if r := row.WindowBounds(); r.Height() < rowH-0.5 {
					t.Errorf("a row is %.1f high, want at least %.0f", r.Height(), rowH)
				}
				if sb, rb := s.WindowBounds(), row.WindowBounds(); (sb.Min.Y-rb.Min.Y)-(rb.Max.Y-sb.Max.Y) > 1 || (rb.Max.Y-sb.Max.Y)-(sb.Min.Y-rb.Min.Y) > 1 {
					t.Errorf("a switch %v is not centred in its row %v", sb, rb)
				}
			}
			if th := tab(t, d, "general").WindowBounds().Height(); th != tabH {
				t.Errorf("a page tab is %.1f high, want %.0f", th, tabH)
			}
			if sk.webKit { // System Settings has no page title in the pane (§3.20)
				if len(titles) != 0 {
					t.Fatalf("the Mac pane shows %d page titles, want none", len(titles))
				}
			} else if len(titles) != 1 || textOf(titles[0]) != "General" {
				t.Fatalf("the page title: %d headings", len(titles))
			}
			// "Settings" heads the navigation's column; the dialog's own header is hidden
			if !d.HeaderHidden() {
				t.Error("the dialog's header shows beside the navigation's title")
			}
			if sk.webKit {
				return
			}
			// the rows are each their own card, 3 px apart
			a, b := switches[0].Parent().Parent().WindowBounds(), switches[1].Parent().Parent().WindowBounds()
			if gap := b.Min.Y - a.Max.Y; gap < 2.5 || gap > 3.5 {
				t.Errorf("the rows are %.1f apart, want 3", gap)
			}
			// the content fills the pane, 32 px from each side (no empty bands)
			pane := d.Pane().Base().WindowBounds()
			right := pane.Max.X - kit.DIP(d.Pane().Scrollbar().Metrics.Thickness)
			if l, r := a.Min.X-pane.Min.X, right-a.Max.X; l < 31.5 || l > 32.5 || r < 31.5 || r > 32.5 {
				t.Errorf("the content is %.1f from the pane's start and %.1f from its end, want 32", l, r)
			}
		})
	}
}

// TestSkinSwitchesOnEveryPage: every switch of every page has the look's size (the
// Downloads page's "Save downloads into their category's folder" was the app.css switch).
func TestSkinSwitchesOnEveryPage(t *testing.T) {
	for _, sk := range skins {
		want := kit.Size{W: 40, H: 20}
		if sk.webKit {
			want = kit.Size{W: 38, H: 22}
		}
		for _, page := range []string{"general", "downloads", "network", "integration", "video", "appearance", "updates", "about"} {
			t.Run(sk.name+"/"+page, func(t *testing.T) {
				h := viewstest.New(t, viewstest.Options{Width: 1200, Height: 760, Theme: "light", Fixture: "settings-" + page, WebKit: sk.webKit})
				d := openState(t, h, "settings-"+page)
				walk(d.Pane().Base(), func(e *kit.Element) bool {
					if e.Semantics.Role == kit.RoleSwitch && e.Size() != want {
						t.Errorf("a switch %q is %v, want %v", textOf(e), e.Size(), want)
					}
					return true
				})
			})
		}
	}
}
