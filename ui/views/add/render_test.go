package add

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"downex/ui/state/scene"
	"downex/ui/views"
	"downex/ui/views/viewstest"
)

// refStates are the Add and Confirm reference states (ref-0.9.2's index), in the script's order.
func refStates() []string {
	var out []string
	for _, fx := range scene.Fixtures() {
		if fx.Dialog == "Add" || fx.Dialog == "Confirm" {
			out = append(out, fx.Name)
		}
	}
	return out
}

// refSizes are the references' windows: CSS px, scale.
var refSizes = []struct {
	w, h  int
	scale float32
}{
	{1200, 760, 1}, {1200, 760, 1.25}, {1190, 752, 1}, {1190, 752, 1.25}, {960, 608, 1}, {960, 608, 1.25},
	{900, 600, 1}, {900, 600, 1.25}, {1920, 1080, 1}, {1920, 1080, 1.25},
}

// fullEnv runs the whole reference matrix (every state at every size, scale, text size and
// theme: 1920 renders, about 22 minutes alone) instead of the default sample, which keeps
// "go test ./..." inside Go's default 10-minute timeout.
const fullEnv = "DOWNEX_VIEWS_FULL"

// sampled are the default run's combinations for every state: one size per scale (the default
// window at 1x and the smallest at 1.25x, where the fit rule is tightest), each with both text
// sizes, and both themes across them. The DOWNEX_ADD_* variables or DOWNEX_VIEWS_FULL=1 select
// other combinations.
var sampled = []string{
	"1200x760@1/16px/light",
	"1200x760@1/20px/dark",
	"900x600@1.25/16px/dark",
	"900x600@1.25/20px/light",
}

// raceEvery thins the default run under the race detector, which makes a render about 20 times
// slower (14 s): every raceEvery-th state runs, in one of the sampled combinations in turn.
const raceEvery = 8

// inSample reports whether state number i in the given combination runs: always with
// DOWNEX_VIEWS_FULL=1 or when a DOWNEX_ADD_* variable narrows the run explicitly, else when the
// combination is sampled (and, under the race detector, when it is the state's turn).
func inSample(i int, tag string, font int, theme string, narrowed bool) bool {
	if narrowed || os.Getenv(fullEnv) != "" {
		return true
	}
	combo := fmt.Sprintf("%s/%dpx/%s", tag, font, theme)
	if raceOn {
		return i%raceEvery == 0 && combo == sampled[(i/raceEvery)%len(sampled)]
	}
	for _, c := range sampled {
		if c == combo {
			return true
		}
	}
	return false
}

func pick2(env string) map[string]bool {
	v := os.Getenv(env)
	if v == "" {
		return nil
	}
	out := map[string]bool{}
	for _, s := range strings.Split(v, ",") {
		out[strings.TrimSpace(s)] = true
	}
	return out
}

// TestReferenceComparisons renders every Add and Confirm state at every size, scale, text size
// and theme the references have, compares the dialog with the reference's (its measured box and
// the pixels: reference | native | difference), and writes the sheets with DOWNEX_VIEWS_WRITE=1
// (DOWNEX_ADD_STATES, _SIZES (e.g. 1200x760@1), _THEMES and _FONTS narrow the run). By default
// it runs every state in the sampled combinations; DOWNEX_VIEWS_FULL=1 runs all of them.
func TestReferenceComparisons(t *testing.T) {
	if testing.Short() {
		t.Skip("renders")
	}
	if err := viewstest.CanRender(); err != nil {
		t.Skipf("no offscreen renderer: %v", err)
	}
	refs, err := viewstest.LoadRefs()
	if err != nil {
		t.Skipf("references: %v", err)
	}
	write := os.Getenv("DOWNEX_VIEWS_WRITE") != ""
	states, sizes, themes, fonts := pick2("DOWNEX_ADD_STATES"), pick2("DOWNEX_ADD_SIZES"), pick2("DOWNEX_ADD_THEMES"), pick2("DOWNEX_ADD_FONTS")
	narrowed := states != nil || sizes != nil || themes != nil || fonts != nil
	for i, name := range refStates() {
		if states != nil && !states[name] {
			continue
		}
		for _, sz := range refSizes {
			tag := fmt.Sprintf("%dx%d@%v", sz.w, sz.h, sz.scale)
			if sizes != nil && !sizes[tag] {
				continue
			}
			for _, font := range []int{16, 20} {
				if fonts != nil && !fonts[fmt.Sprint(font)] {
					continue
				}
				for _, theme := range []string{"light", "dark"} {
					if themes != nil && !themes[theme] {
						continue
					}
					if !inSample(i, tag, font, theme, narrowed) {
						continue
					}
					o := viewstest.Options{Width: sz.w, Height: sz.h, Scale: sz.scale, Font: font, Theme: theme, Fixture: name, Real: true}
					shot, ok := refs.For(o)
					if !ok {
						continue
					}
					t.Run(fmt.Sprintf("%s/%s/%dpx/%s", name, tag, font, theme), func(t *testing.T) {
						h := viewstest.New(t, o)
						m := stateOf(t, h, name)
						card := cardOf(m).WindowBounds()
						deltas := viewstest.BoxDeltas(shot, []viewstest.Probe{{Name: "dialog", Dialog: true, Native: card}})
						t.Logf("BOX %s %s %dpx %s: %s", name, tag, font, theme, deltas[0])
						img, err := h.Render()
						if err != nil {
							t.Fatal(err)
						}
						ref, err := refs.Image(shot)
						if err != nil {
							t.Fatal(err)
						}
						refBox, _ := shot.DialogBox()
						c := viewstest.Compare(ref, img, refBox, card.Min, sz.scale)
						sx, sy, best, unshifted := viewstest.BestShift(ref, img, refBox, card.Min, sz.scale, 3)
						t.Logf("PIXELS %s %s %dpx %s: mean %.2f, %.1f%% differ by more than %d; best shift %+d,%+d px (%.2f, unshifted %.2f)",
							name, tag, font, theme, c.Mean, 100*c.Differing, c.Threshold, sx, sy, best, unshifted)
						problems := fitProblems(h, m, name, sz.w, sz.h, sz.scale, font)
						t.Logf("FIT %s %s %dpx %s: %v", name, tag, font, theme, problems)
						if len(problems) > 0 {
							t.Errorf("fit rule: %v", problems)
						}
						if d := withoutPart(deltas[0], m); d.Max() > 1 {
							t.Errorf("dialog box differs by more than 1 DIP: %v", d)
						}
						if write && font == 16 {
							file := filepath.Join(viewstest.ShotDir(), viewstest.NativeName("add", name, theme, sz.w, float64(sz.scale)))
							if err := viewstest.WritePNG(file, c.Sheet); err != nil {
								t.Fatal(err)
							}
						}
					})
				}
			}
		}
	}
}

// withoutPart takes out of d what the "Save only part of the video" row adds to a dialog,
// which the references (0.9.2, before parts of a video, docs/MEDIA-SPEC.md §8) do not have:
// the row's height and the gap before it, the dialog growing about its centre (at most to
// the room the window has). The rest must still match within 1 DIP.
func withoutPart(d viewstest.Delta, m views.Modal) viewstest.Delta {
	var c *ClipSection
	switch x := m.(type) {
	case *Add:
		c = x.Clip
	case *Confirm:
		c = x.Clip
	}
	if c == nil || c.Base().Style.Hidden || c.Size().H <= 0 {
		return d
	}
	grow := c.Size().H + 24 // the row, and its margin or the column's gap (at most 2 × 12 DIP)
	switch {
	case d.DH > 0 && d.DH <= grow+1:
		d.DY += d.DH / 2
		d.DH = 0
	}
	return d
}
