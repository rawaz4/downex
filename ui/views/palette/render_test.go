package palette_test

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"downex/ui/kit"
	"downex/ui/views/dialogs/inkmeasure"
	"downex/ui/views/palette"
	"downex/ui/views/viewstest"
)

// ShotResult is one comparison's measures, for the report.
type ShotResult struct {
	Shot       string  `json:"shot"`
	DX         float32 `json:"dx"`
	DY         float32 `json:"dy"`
	DW         float32 `json:"dw"`
	DH         float32 `json:"dh"`
	Content    float32 `json:"content"`    // the list's content height (DIP)
	RefContent float64 `json:"refContent"` // the reference's (measure.vscroll)
	View       float32 `json:"view"`
	RefView    float64 `json:"refView"`
	Mean       float64 `json:"mean"`
	Differing  float64 `json:"differing"`
	Field      [2]int  `json:"field"`
	List       [2]int  `json:"list"`
	Foot       [2]int  `json:"foot"`
	Sheet      string  `json:"sheet,omitempty"`
	// Texts are the palette's texts' ink against the reference's (inkmeasure).
	Texts []inkmeasure.Delta `json:"texts"`
}

// TestPaletteMatchesReferences renders the palette state ("every command": Ctrl+K in the
// Studio scene) at every size, scale, text size and theme of the 0.9.2 references and compares
// it with them: the palette's box with the index's measured one (at most 1 DIP apart), the
// list's scroll content and view heights with the index's (measure.vscroll), and the pixels
// (reference | native | difference, written with DOWNEX_VIEWS_WRITE=1 as
// native-s2-palette-palette[-20px]-<theme>-<width>-<scale>.png). DOWNEX_PALETTE_REPORT writes
// the measures as JSON.
func TestPaletteMatchesReferences(t *testing.T) {
	if err := viewstest.CanRender(); err != nil {
		t.Skipf("no offscreen renderer: %v", err)
	}
	refs, err := viewstest.LoadRefs()
	if err != nil {
		t.Skipf("references: %v", err)
	}
	write := os.Getenv("DOWNEX_VIEWS_WRITE") != ""
	var results []ShotResult
	for _, shot := range refs.Shots {
		if shot.Kind != "state" || shot.Name != "palette" {
			continue
		}
		name := strings.TrimSuffix(filepath.Base(shot.File), ".png")
		t.Run(name, func(t *testing.T) {
			o := viewstest.Options{Width: shot.CSS.W, Height: shot.CSS.H, Scale: float32(shot.Scale), Font: shot.Font, Theme: shot.Theme, Fixture: shot.Name, Real: true}
			h := viewstest.New(t, o)
			p := palette.New(h.App, palette.Options{})
			h.Window.SetFocusVisible(true)
			h.App.Dialogs.Show(p)
			h.Settle()
			img, err := h.Render()
			if err != nil {
				t.Fatal(err)
			}
			ref, err := refs.Image(shot)
			if err != nil {
				t.Fatal(err)
			}
			card := p.Card().WindowBounds()
			d := viewstest.BoxDeltas(shot, []viewstest.Probe{{Name: "palette", Dialog: true, Native: card}})[0]
			if !d.Found {
				t.Fatal("no measured box")
			}
			area := kit.R(d.Ref.Min.X-4, d.Ref.Min.Y-4, d.Ref.Max.X+4, d.Ref.Max.Y+4)
			c := viewstest.Compare(ref, img, area, kit.Pt(card.Min.X-4, card.Min.Y-4), float32(shot.Scale))
			_, content, view := p.List().ScrollRange()
			r := ShotResult{Shot: name, DX: float32(d.DX), DY: float32(d.DY), DW: float32(d.DW), DH: float32(d.DH),
				Content: float32(content), View: float32(view), Mean: c.Mean, Differing: c.Differing}
			if vs := shot.Measure; vs != nil {
				r.RefContent, r.RefView = refScroll(shot)
			}
			sc := float32(shot.Scale)
			shift := func(top, bottom kit.DIP) [2]int {
				ra := kit.R(d.Ref.Min.X+8, top, d.Ref.Max.X-8, bottom)
				dx, dy, _, _ := viewstest.BestShift(ref, img, ra, kit.Pt(card.Min.X+8, top-d.Ref.Min.Y+card.Min.Y), sc, 3)
				return [2]int{dx, dy}
			}
			r.Field = shift(d.Ref.Min.Y+2, d.Ref.Min.Y+46)
			r.List = shift(d.Ref.Min.Y+48, d.Ref.Max.Y-40)
			r.Foot = shift(d.Ref.Max.Y-38, d.Ref.Max.Y-2)
			t.Logf("%v; list content %.1f (ref %.0f), view %.1f (ref %.0f); mean %.2f, %.1f%% differ; shifts field %v list %v foot %v",
				d, content, r.RefContent, view, r.RefView, c.Mean, 100*c.Differing, r.Field, r.List, r.Foot)
			// the list's view, then the field and the footer around it
			r.Texts = append(inkmeasure.Texts(ref, img, &p.List().Element, card, p.List().WindowBounds(), d.Ref, sc, nil),
				inkmeasure.Texts(ref, img, p.Card(), card, card, d.Ref, sc, &p.List().Element)...)
			for _, td := range r.Texts {
				t.Logf("text %q: dx %.2f dy %.2f dw %.2f width %.4f found %v", td.Name, td.DX, td.DY, td.DW, td.Width, td.Found)
			}
			if d.Max() > 1 {
				t.Errorf("the palette's box is %v", d)
			}
			if math.Abs(float64(view)-r.RefView) > 1 {
				t.Errorf("the list's view is %.1f DIP, the reference's %.0f", view, r.RefView)
			}
			if write {
				state := shot.Name
				if shot.Font != 16 {
					state += fmt.Sprintf("-%dpx", shot.Font)
				}
				file := filepath.Join(viewstest.ShotDir(), viewstest.NativeName("palette", state, shot.Theme, shot.CSS.W, shot.Scale))
				if err := viewstest.WritePNG(file, c.Sheet); err != nil {
					t.Fatal(err)
				}
				r.Sheet = file
			}
			results = append(results, r)
		})
	}
	if path := os.Getenv("DOWNEX_PALETTE_REPORT"); path != "" {
		b, _ := json.MarshalIndent(results, "", " ")
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// refScroll is the reference list's scroll content and client heights (measure.vscroll of
// ul.list), read from the index's raw entry.
func refScroll(shot *viewstest.Shot) (content, client float64) {
	scrollOnce.Do(func() {
		b, err := os.ReadFile(filepath.Join(viewstest.RefDir(), "index.json"))
		if err != nil {
			return
		}
		if len(b) >= 3 && b[0] == 0xEF {
			b = b[3:]
		}
		_ = json.Unmarshal(b, &scrollIndex)
	})
	for _, s := range scrollIndex.Shots {
		if s.File == shot.File {
			for _, v := range s.Measure.VScroll {
				if v.El == "ul.list" {
					return v.Content, v.Client
				}
			}
		}
	}
	return 0, 0
}

var (
	scrollOnce  sync.Once
	scrollIndex struct {
		Shots []struct {
			File    string `json:"file"`
			Measure struct {
				VScroll []struct {
					El      string  `json:"el"`
					Client  float64 `json:"client"`
					Content float64 `json:"content"`
				} `json:"vscroll"`
			} `json:"measure"`
		} `json:"shots"`
	}
)
