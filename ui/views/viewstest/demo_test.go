package viewstest_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"downex/ui/kit"
	"downex/ui/kit/icons"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/views"
	"downex/ui/views/viewstest"
)

// toneBox is RemoveDialog's .tone.danger: a 32 px --danger-soft square (--r-md) with the
// 18 px remove icon in --danger.
type toneBox struct{ kit.Element }

func (b *toneBox) Paint(p *kit.PaintContext) {
	p.Canvas.FillRoundedRect(views.SnapBox(p, 8), views.TokensOf(&b.Element).DangerSoft)
}

// removeShell is a trivial screen for the harness demo: the Remove dialog's shell (title,
// description, tone icon, footer buttons) with an empty body. The real Remove dialog is the
// dialogs lane's (views/dialogs).
func removeShell(app *views.App, height kit.DIP) *views.Dialog {
	d := app.NewDialog(views.ModalRemove, widgets.DialogOptions{
		Title: "Remove download?", Description: "The download is removed from the list.", Alert: true, Size: widgets.DialogSm,
		FixedHeight: kit.Px(height),
	})
	tone := &toneBox{}
	tone.Style = kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, Justify: kit.JustifyCenter, Width: kit.Px(32), Height: kit.Px(32)}
	name, _ := icons.ByName("remove")
	tone.Add(widgets.NewIcon(name, 18, func(t *tokens.Theme) kit.Color { return t.Danger }))
	d.SetIcon(tone)
	d.SetFooter(nil,
		widgets.NewButton("Cancel", "", widgets.Secondary, widgets.SizeMd, nil),
		widgets.NewButton("Remove", "", widgets.Danger, widgets.SizeMd, nil))
	return d
}

// TestDemoRenderAndCompare renders the trivial screen through the harness at the "remove"
// reference's sizes, scales and themes, and compares its header and footer with the
// reference's crops (reference | native | difference, written as native-s2-base-*.png) and its
// dialog box with the index's measured box.
func TestDemoRenderAndCompare(t *testing.T) {
	if err := viewstest.CanRender(); err != nil {
		t.Skipf("no offscreen renderer: %v", err)
	}
	refs, err := viewstest.LoadRefs()
	if err != nil {
		t.Skipf("references: %v", err)
	}
	out := viewstest.ShotDir()
	write := os.Getenv("DOWNEX_VIEWS_WRITE") != ""
	for _, theme := range []string{"light", "dark"} {
		for _, scale := range []float32{1, 1.25} {
			name := fmt.Sprintf("%s-%v", theme, scale)
			t.Run(name, func(t *testing.T) {
				o := viewstest.Options{Width: 1200, Height: 760, Scale: scale, Font: 16, Theme: theme, Fixture: "remove", Real: true}
				h := viewstest.New(t, o)
				shot, ok := refs.For(o)
				if !ok {
					t.Fatal("no reference for", o)
				}
				// The body (names, check box) is the dialogs lane's: the shell takes the measured
				// height so that it sits where today's dialog does.
				d := removeShell(h.App, kit.DIP(shot.Measure.H))
				h.App.Dialogs.Show(d)
				h.Settle()
				img, err := h.Render()
				if err != nil {
					t.Fatal(err)
				}
				ref, err := refs.Image(shot)
				if err != nil {
					t.Fatal(err)
				}
				if b := img.Bounds(); b.Dx() != ref.Bounds().Dx() || b.Dy() != ref.Bounds().Dy() {
					t.Fatalf("render %v, reference %v", b, ref.Bounds())
				}
				card := d.Card().WindowBounds()
				deltas := viewstest.BoxDeltas(shot, []viewstest.Probe{{Name: "dialog", Dialog: true, Native: card}})
				dlg := deltas[0]
				t.Log(dlg)
				if !dlg.Found {
					t.Fatal("no measured dialog box in the index")
				}
				if dlg.Max() > 0.5 {
					t.Errorf("dialog width/x differ: %v", dlg)
				}
				const headH, footH = 72, 62
				head := viewstest.Compare(ref, img, kit.R(dlg.Ref.Min.X, dlg.Ref.Min.Y, dlg.Ref.Max.X, dlg.Ref.Min.Y+headH), card.Min, scale)
				foot := viewstest.Compare(ref, img, kit.R(dlg.Ref.Min.X, dlg.Ref.Max.Y-footH, dlg.Ref.Max.X, dlg.Ref.Max.Y), kit.Pt(card.Min.X, card.Max.Y-footH), scale)
				t.Logf("head: mean %.2f, %.1f%% of pixels differ by more than %d", head.Mean, 100*head.Differing, head.Threshold)
				t.Logf("foot: mean %.2f, %.1f%% of pixels differ by more than %d", foot.Mean, 100*foot.Differing, foot.Threshold)
				// the title and description, without the close button and the scrim at the corners
				text := kit.R(dlg.Ref.Min.X+60, dlg.Ref.Min.Y+12, dlg.Ref.Min.X+300, dlg.Ref.Min.Y+62)
				dx, dy, best, plain := viewstest.BestShift(ref, img, text, kit.Pt(card.Min.X+60, card.Min.Y+12), scale, 4)
				t.Logf("head text: best match with the native shifted by (%d, %d) px: mean %.2f (unshifted %.2f)", dx, dy, best, plain)
				btns := kit.R(dlg.Ref.Max.X-200, dlg.Ref.Max.Y-footH+8, dlg.Ref.Max.X-8, dlg.Ref.Max.Y-8)
				dx, dy, best, plain = viewstest.BestShift(ref, img, btns, kit.Pt(card.Max.X-200, card.Max.Y-footH+8), scale, 4)
				t.Logf("footer buttons: best match with the native shifted by (%d, %d) px: mean %.2f (unshifted %.2f)", dx, dy, best, plain)
				if write {
					sc := strings.TrimSuffix(viewstest.ScaleText(float64(scale)), "x")
					for part, c := range map[string]viewstest.Comparison{"head": head, "foot": foot} {
						file := filepath.Join(out, viewstest.NativeName("base", "remove-"+part, theme, 1200, float64(scale)))
						if err := viewstest.WritePNG(file, c.Sheet); err != nil {
							t.Fatal(err)
						}
						t.Logf("wrote %s (scale %s)", file, sc)
					}
				}
			})
		}
	}
}

func abs(v kit.DIP) kit.DIP {
	if v < 0 {
		return -v
	}
	return v
}
