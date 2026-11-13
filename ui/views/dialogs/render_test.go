package dialogs_test

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"downex/ui/kit"
	"downex/ui/kit/widgets"
	"downex/ui/views"
	"downex/ui/views/dialogs/inkmeasure"
	"downex/ui/views/viewstest"
)

// carded is a modal whose box is a dialog card.
type carded interface {
	views.Modal
	Card() *kit.Element
}

// ShotResult is one comparison's measures, for the report.
type ShotResult struct {
	Shot string  `json:"shot"`
	DX   float32 `json:"dx"`
	DY   float32 `json:"dy"`
	DW   float32 `json:"dw"`
	DH   float32 `json:"dh"`
	Mean float64 `json:"mean"`
	// Head, Body and Foot are the best-matching shifts (device px) of the header, body and
	// footer crops: [dx, dy].
	Head       [2]int  `json:"head"`
	Body       [2]int  `json:"body"`
	Foot       [2]int  `json:"foot"`
	Differing  float64 `json:"differing"`
	Scrolls    bool    `json:"scrolls"`
	RefScrolls bool    `json:"refScrolls"`
	Sheet      string  `json:"sheet,omitempty"`
	// Texts are the dialog's texts' ink against the reference's (inkmeasure).
	Texts []inkmeasure.Delta `json:"texts"`
}

// bodied is a dialog whose body scrolls in a pane.
type bodied interface {
	BodyPane() (*widgets.ScrollPane, bool)
}

// refBodyScroll is the reference body's scroll content and client heights (measure.vscroll of
// div.body; 0, 0 when it does not scroll).
func refBodyScroll(shot *viewstest.Shot) (content, client float64) {
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
				if v.El == "div.body" {
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

// TestStatesMatchReferences renders every dialog state of this package at every size, scale,
// text size and theme the 0.9.2 references have, and compares the dialog with the
// reference's: its box with the index's measured box (at most 1 DIP apart), and its pixels
// (reference | native | difference, written with DOWNEX_VIEWS_WRITE=1 as
// native-s2-dialogs-<state>[-20px]-<theme>-<width>-<scale>.png). DOWNEX_DIALOGS_ONLY limits it
// to the states whose names contain its text; DOWNEX_DIALOGS_REPORT writes the measures as
// JSON.
func TestStatesMatchReferences(t *testing.T) {
	if err := viewstest.CanRender(); err != nil {
		t.Skipf("no offscreen renderer: %v", err)
	}
	refs, err := viewstest.LoadRefs()
	if err != nil {
		t.Skipf("references: %v", err)
	}
	only := os.Getenv("DOWNEX_DIALOGS_ONLY")
	write := os.Getenv("DOWNEX_VIEWS_WRITE") != ""
	var results []ShotResult
	for _, shot := range refs.Shots {
		if shot.Kind != "state" || !slices.Contains(dialogStates, shot.Name) || (only != "" && !strings.Contains(shot.Name, only)) {
			continue
		}
		name := strings.TrimSuffix(filepath.Base(shot.File), ".png")
		t.Run(name, func(t *testing.T) {
			o := viewstest.Options{Width: shot.CSS.W, Height: shot.CSS.H, Scale: float32(shot.Scale), Font: shot.Font, Theme: shot.Theme, Fixture: shot.Name, Real: true}
			h := viewstest.New(t, o)
			m := openState(t, h, shot.Name).(carded)
			img, err := h.Render()
			if err != nil {
				t.Fatal(err)
			}
			ref, err := refs.Image(shot)
			if err != nil {
				t.Fatal(err)
			}
			card := m.Card().WindowBounds()
			d := viewstest.BoxDeltas(shot, []viewstest.Probe{{Name: "dialog", Dialog: true, Native: card}})[0]
			if !d.Found {
				t.Fatal("no measured dialog box")
			}
			area := d.Ref
			area.Min.X, area.Min.Y, area.Max.X, area.Max.Y = area.Min.X-4, area.Min.Y-4, area.Max.X+4, area.Max.Y+4
			at := kit.Pt(card.Min.X-4, card.Min.Y-4)
			c := viewstest.Compare(ref, img, area, at, float32(shot.Scale))
			r := ShotResult{Shot: name, DX: float32(d.DX), DY: float32(d.DY), DW: float32(d.DW), DH: float32(d.DH), Mean: c.Mean, Differing: c.Differing}
			sc := float32(shot.Scale)
			shift := func(top, bottom kit.DIP) [2]int {
				ra := kit.R(d.Ref.Min.X+8, top, d.Ref.Max.X-8, bottom)
				dx, dy, _, _ := viewstest.BestShift(ref, img, ra, kit.Pt(card.Min.X+8, top-d.Ref.Min.Y+card.Min.Y), sc, 3)
				return [2]int{dx, dy}
			}
			const headH, footH = 60, 56
			r.Head = shift(d.Ref.Min.Y+4, d.Ref.Min.Y+headH)
			r.Body = shift(d.Ref.Min.Y+headH, d.Ref.Max.Y-footH)
			r.Foot = shift(d.Ref.Max.Y-footH, d.Ref.Max.Y-4)
			t.Logf("%v; mean %.2f, %.1f%% differ by more than %d; best shifts head %v body %v foot %v", d, c.Mean, 100*c.Differing, c.Threshold, r.Head, r.Body, r.Foot)
			if d.Max() > 1 {
				t.Errorf("the dialog box is %v", d)
			}
			// the fit rule (measure-dialogs.mjs): inside the room under the title bar, and the
			// body scrolls where today's does (by as much), nowhere else
			if card.Min.Y < h.App.ModalTop() || card.Max.Y > kit.DIP(shot.CSS.H) {
				t.Errorf("the dialog %v leaves the window", card)
			}
			pane, _ := m.(bodied).BodyPane()
			if pane.Overflows() {
				// the body's view, then the header and footer around it
				r.Texts = append(inkmeasure.Texts(ref, img, &pane.Element, card, pane.WindowBounds(), d.Ref, sc, nil),
					inkmeasure.Texts(ref, img, m.Card(), card, card, d.Ref, sc, &pane.Element)...)
			} else {
				r.Texts = inkmeasure.Texts(ref, img, m.Card(), card, card, d.Ref, sc, nil)
			}
			for _, td := range r.Texts {
				t.Logf("text %q: dx %.2f dy %.2f dw %.2f width %.4f found %v", td.Name, td.DX, td.DY, td.DW, td.Width, td.Found)
			}
			_, content, view := pane.ScrollRange()
			refContent, refView := refBodyScroll(shot)
			r.Scrolls, r.RefScrolls = pane.Overflows(), refView > 0
			if r.Scrolls != r.RefScrolls {
				t.Errorf("the body scrolls: %v (content %.1f in %.1f), today's: %v", r.Scrolls, content, view, r.RefScrolls)
			} else if r.Scrolls && (math.Abs(float64(content-view)-(refContent-refView)) > 1.5) {
				t.Errorf("the body scrolls by %.1f, today's by %.0f", content-view, refContent-refView)
			}
			if write {
				state := shot.Name
				if shot.Font != 16 {
					state += fmt.Sprintf("-%dpx", shot.Font)
				}
				file := filepath.Join(viewstest.ShotDir(), viewstest.NativeName("dialogs", state, shot.Theme, shot.CSS.W, shot.Scale))
				if err := viewstest.WritePNG(file, c.Sheet); err != nil {
					t.Fatal(err)
				}
				r.Sheet = file
			}
			results = append(results, r)
		})
	}
	if p := os.Getenv("DOWNEX_DIALOGS_REPORT"); p != "" {
		b, _ := json.MarshalIndent(results, "", " ")
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
