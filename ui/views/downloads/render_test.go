package downloads

import (
	"fmt"
	"image"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"downex/ui/core/display"
	"downex/ui/core/format"
	"downex/ui/core/locale"
	"downex/ui/core/selection"

	"downex/ui/kit"
	"downex/ui/views/viewstest"
)

// mainShots are the main-window references (the Studio scene, nothing selected): every width,
// scale, text size and theme they were taken at.
func mainShots(t *testing.T) (*viewstest.Refs, []*viewstest.Shot) {
	t.Helper()
	if err := viewstest.CanRender(); err != nil {
		t.Skipf("no offscreen renderer: %v", err)
	}
	refs, err := viewstest.LoadRefs()
	if err != nil {
		t.Skipf("references: %v", err)
	}
	var out []*viewstest.Shot
	for _, s := range refs.Shots {
		if s.Kind == "main" {
			out = append(out, s)
		}
	}
	return refs, out
}

// wrapBox is the reference's div.wrap (the grid's area).
func wrapBox(s *viewstest.Shot) (kit.Rect, bool) {
	ns := s.Nodes("div.wrap", "")
	if len(ns) == 0 {
		return kit.Rect{}, false
	}
	return ns[0].Rect(), true
}

// TestRenderMainGrid renders the grid at every main reference's size, scale, text size and
// theme, where the reference has it, and compares it: reference | native | difference sheets
// (native-s2-downloads-grid-<theme>-<width>-<scale>.png with DOWNEX_VIEWS_WRITE=1), the box
// deltas of the grid, its header and its body, and the mean difference.
func TestRenderMainGrid(t *testing.T) {
	refs, shots := mainShots(t)
	write := os.Getenv("DOWNEX_VIEWS_WRITE") != ""
	// the references' page: en-GB in Asia/Baghdad (index.json engine.page)
	if f, err := locale.New("en-GB", "Asia/Baghdad"); err == nil {
		prev := format.Locale()
		format.SetLocale(f)
		t.Cleanup(func() { format.SetLocale(prev) })
	}
	for _, s := range shots {
		name := fmt.Sprintf("%dx%d-%vx-%dpx-%s", s.CSS.W, s.CSS.H, s.Scale, s.Font, s.Theme)
		t.Run(name, func(t *testing.T) {
			box, ok := wrapBox(s)
			if !ok {
				t.Skip("no div.wrap")
			}
			o := viewstest.Options{Width: s.CSS.W, Height: s.CSS.H, Scale: float32(s.Scale), Font: s.Font, Theme: s.Theme, Real: true}
			r := newRig(t, o, box, 0)
			if s.Font != 16 {
				// The references set the root text size after the page loaded: the table had
				// measured 16 px per rem when it mounted (onMount) and kept it.
				r.table.FixRem(16)
				r.Settle()
			}
			img, err := r.Render()
			if err != nil {
				t.Fatal(err)
			}
			ref, err := refs.Image(s)
			if err != nil {
				t.Fatal(err)
			}
			hb := r.table.HeaderBounds()
			deltas := viewstest.BoxDeltas(s, []viewstest.Probe{
				{Name: "grid", Selector: "div.grid", Native: r.grid.WindowBounds()},
				{Name: "header", Selector: "div.header", Native: hb},
				{Name: "body", Selector: "div.body", Nth: 1, Native: r.table.BodyBounds()},
			})
			for _, d := range deltas {
				t.Log(d)
			}
			c := viewstest.Compare(ref, img, box, box.Min, float32(s.Scale))
			t.Logf("mean %.2f, differing %.1f%%", c.Mean, c.Differing*100)
			writeCrops(t, ref, img, float32(s.Scale), name, map[string]kit.Rect{
				"left":  kit.R(box.Min.X, box.Min.Y, box.Min.X+420, box.Min.Y+130),
				"right": kit.R(box.Max.X-480, box.Min.Y, box.Max.X, box.Min.Y+130),
			})
			// The same columns show as in the reference, and Name ends where it does: the
			// header around Name's end edge (Size's label) matches without a shift.
			var cols []string
			for _, c := range r.table.layout.Shown {
				cols = append(cols, c.Label)
			}
			if s.Layout != nil && !slices.Equal(cols, s.Layout.Columns) {
				t.Errorf("columns %v, reference %v", cols, s.Layout.Columns)
			}
			nameEnd := hb.Min.X + kit.DIP(display.RowPadStart+r.table.layout.W[0])
			around := kit.R(nameEnd-60, hb.Min.Y+1, nameEnd+60, hb.Max.Y-1)
			dx, dy, best, plain := viewstest.BestShift(ref, img, around, around.Min, float32(s.Scale), 4)
			t.Logf("Name %.1f px wide; header at its end: best shift %d,%d px (%.2f vs %.2f unshifted)", r.table.layout.W[0], dx, dy, best, plain)
			if dx != 0 {
				t.Errorf("Name's end is %d device px off the reference", dx)
			}
			row := kit.R(box.Min.X, hb.Max.Y, box.Max.X-16, hb.Max.Y+kit.DIP(display.RowHeightFor(16, false))*3)
			rdx, rdy, rbest, rplain := viewstest.BestShift(ref, img, row, row.Min, float32(s.Scale), 4)
			t.Logf("first 3 rows: best shift %d,%d px (%.2f vs %.2f unshifted)", rdx, rdy, rbest, rplain)
			if write {
				state := "grid"
				if s.Font != 16 {
					state = "grid-20px"
				}
				fn := viewstest.NativeName("downloads", state, s.Theme, s.CSS.W, s.Scale)
				if err := viewstest.WritePNG(filepath.Join(viewstest.ShotDir(), fn), c.Sheet); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

// zoom scales img up by k (nearest neighbour), for looking at crops.
func zoom(img image.Image, k int) *image.RGBA {
	b := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx()*k, b.Dy()*k))
	for y := 0; y < b.Dy()*k; y++ {
		for x := 0; x < b.Dx()*k; x++ {
			out.Set(x, y, img.At(b.Min.X+x/k, b.Min.Y+y/k))
		}
	}
	return out
}

// writeCrops writes zoomed comparisons of areas (window DIPs) when DOWNEX_DL_ZOOM is set.
func writeCrops(t *testing.T, ref, img image.Image, scale float32, prefix string, areas map[string]kit.Rect) {
	if os.Getenv("DOWNEX_DL_ZOOM") == "" {
		return
	}
	for name, a := range areas {
		c := viewstest.Compare(ref, img, a, a.Min, scale)
		dx, dy, best, plain := viewstest.BestShift(ref, img, a, a.Min, scale, 4)
		t.Logf("crop %s: mean %.2f; best shift %d,%d px (%.2f vs %.2f)", name, c.Mean, dx, dy, best, plain)
		if err := viewstest.WritePNG(filepath.Join(viewstest.ShotDir(), prefix+"-zoom-"+name+".png"), zoom(c.Sheet, 3)); err != nil {
			t.Fatal(err)
		}
	}
}

// TestRenderPane renders the main window's grid with the pane under it for the Studio scene's
// first row (running), its awaiting row, a failed row, and three selected, light and dark, and
// writes them to $DOWNEX_DL_PANE (there are no 0.9.2 references of the pane to compare).
func TestRenderPane(t *testing.T) {
	dir := os.Getenv("DOWNEX_DL_PANE")
	if dir == "" {
		t.Skip("set DOWNEX_DL_PANE to a folder")
	}
	if err := viewstest.CanRender(); err != nil {
		t.Skip(err)
	}
	if f, err := locale.New("en-GB", "Asia/Baghdad"); err == nil {
		prev := format.Locale()
		format.SetLocale(f)
		t.Cleanup(func() { format.SetLocale(prev) })
	}
	for _, theme := range []string{"light", "dark"} {
		for _, scale := range []float32{1, 1.25} {
			for i, pick := range []string{"first", "awaiting", "failed", "several"} {
				r := newRig(t, viewstest.Options{Theme: theme, Scale: scale, Real: true}, gridBox, 226)
				ids := r.table.model.ids
				switch pick {
				case "first":
					r.Main.Downloads.SelectOnly(ids[0])
				case "awaiting":
					r.Main.Downloads.SelectOnly(ids[3])
				case "failed":
					r.Main.Downloads.SelectOnly(ids[7])
				case "several":
					r.Main.Downloads.SelectOnly(ids[0])
					r.Main.Downloads.Click(ids[2], selection.Modifiers{Shift: true})
				}
				r.refresh()
				r.Advance(time.Second)
				img, err := r.Render()
				if err != nil {
					t.Fatal(err)
				}
				name := fmt.Sprintf("pane-%d-%s-%s-%vx.png", i, pick, theme, scale)
				if err := viewstest.WritePNG(filepath.Join(dir, name), img); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}
