//go:build windows

package progress

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"downex/ui/core/format"
	"downex/ui/core/locale"
	"downex/ui/core/progress"
	"downex/ui/host"
	"downex/ui/kit"
	"downex/ui/kit/hostcap/hostcaptest"
	"downex/ui/kit/kittest"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/state"
	"downex/ui/state/scene"
	"downex/ui/state/statetest"
	"downex/ui/views"
	"downex/ui/views/viewstest"
	"downex/ui/win"
)

// The step-1 references of the progress window (ref-0.9.2 screens/progress: 9 views, light and
// dark, 1x and 1.25x, 16 and 20 px text), rendered natively in the same state of the demo's
// scene and compared side by side. DOWNEX_VIEWS_WRITE=1 writes the sheets to ShotDir as
// native-s2-progress-<state>-<theme>-520-<scale>.png (the 20 px runs add "-20px").

// sceneRig is a progress window over the demo's simulated daemon in a fixture's state, with
// DirectWrite's text, sized by its own fit as the reference's host sized the page (a 40 px
// frame, 520 px wide).
func sceneRig(t *testing.T, fixture string, scale float32, font int, theme string) (*rig, *scene.Mock) {
	return sceneRigAt(t, fixture, scale, font, theme, shotMeta{})
}

// sceneRigAt is sceneRig at the moment a reference was taken: the demo's scene made at the
// page's load, msSinceLoad of its 250 ms steps later (the demo's data, not the window, changes
// with them), with the wall clock at the capture (the "Done ≈" time). A zero meta is about a
// second after RefNow.
func sceneRigAt(t *testing.T, fixture string, scale float32, font int, theme string, meta shotMeta) (*rig, *scene.Mock) {
	t.Helper()
	fx, ok := scene.Fixture(fixture)
	if !ok {
		t.Fatalf("no fixture %q", fixture)
	}
	res, _, err := viewstest.RealResources()
	if err != nil {
		t.Skipf("no text engine: %v", err)
	}
	so := fx.Options()
	ticks, end := 4, viewstest.RefNow.UnixMilli()+1000
	if meta.captured > 0 {
		ticks, end = meta.ticks(), meta.captured
	}
	wall := statetest.NewClock(end - int64(ticks)*250)
	so.Now = wall.Now
	mock, err := scene.New(so)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mock.Close)
	for range ticks {
		wall.Advance(250 * time.Millisecond)
		mock.Tick(250)
	}
	wall.Set(end)
	storage := map[string]string{}
	if fx.Details {
		storage[state.ProgressDetailsKey] = "1"
	}
	r := &rig{t: t, api: newFakeAPI(nil, settings()), clock: kittest.NewClock(), wall: wall, storage: statetest.NewMemory(storage)}
	choice := tokens.ChoiceLight
	if theme == "dark" {
		choice = tokens.ChoiceDark
	}
	systemLight := false
	plat := &viewstest.ScenePlatform{Mock: mock}
	frame := kit.Size{H: progress.Frame}
	pw, err := New(Options{
		ID: fx.Progress, Daemon: mock, Events: mock, Platform: plat, Caps: host.Capabilities{OpenWith: true}, Storage: r.storage, Clock: wall,
		Go: statetest.Sync, FixedTheme: &choice, SystemDark: &systemLight, StatusInterval: time.Hour,
		Frame: func() kit.Size { return frame },
		Now:   r.clock.Now, Scheduler: r.clock, NoMotion: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	r.pw = pw
	env := kit.Environment{Scale: kit.Scale{PixelsPerDIP: scale, Text: float32(font) / 16}, HoverTime: 400 * time.Millisecond, ReducedMotion: true}
	r.host = &testHost{Host: hostcaptest.NewHost(InitialSize(frame), env, r.clock), res: res, r: r}
	r.loop = &kittest.Loop{Host: r.host.Host.Host, Root: pw.Root, Canvas: &kittest.Canvas{}, Clock: r.clock, Refresh: time.Second / 60}
	pw.Root.Attach(r.host)
	pw.Start()
	r.settle()
	for _, s := range fx.Steps {
		if s.Kind == scene.StepClick && strings.Contains(s.Selector, "tab") && s.Text == "Speed limit" {
			r.click(r.tab(tabLimits))
		}
	}
	r.settle()
	t.Cleanup(r.close)
	return r, mock
}

// render paints the window offscreen at the rig's scale, as the backend paints a window.
func (r *rig) render(scale float32) (*image.RGBA, error) {
	res, _, err := viewstest.RealResources()
	if err != nil {
		return nil, err
	}
	r.settle()
	size := r.pw.Kit.Size()
	px := func(v kit.DIP) int { return int(math.Ceil(float64(v * scale))) }
	o, err := win.NewOffscreen(res.(*win.Resources), px(size.W), px(size.H), scale, win.OffscreenOptions{})
	if err != nil {
		return nil, err
	}
	defer o.Close()
	full := kit.RectAt(kit.Point{}, size)
	c, err := o.BeginFrame(full)
	if err != nil {
		return nil, err
	}
	r.pw.Kit.Paint(c, full)
	if err := o.EndFrame(); err != nil {
		return nil, err
	}
	return o.RGBA()
}

// probe is an element whose box is measured against the reference: its native box, and where
// the reference draws it (the native box moved by the best match of its crop).
type probe struct {
	name string
	el   *kit.Element
	// pad grows the crop around the box (the crop needs some ground to match on).
	pad kit.DIP
	// text: the element is text, whose drawn width is measured too.
	text bool
}

func (r *rig) probes() []probe {
	s := r.s()
	if s.complete {
		v := s.done
		return []probe{
			{"title", &v.title.Element, 2, true},
			{"name", &v.name.Element, 2, true},
			{"figure", &v.figure.Element, 2, true},
			{"tile", &v.tile.Element, 3, false},
			{"facts", &v.facts.Element, 2, false},
			{"check", &v.dontShow.Element, 2, false},
			{"open", &v.open.Element, 3, false},
			{"folder", &v.folder.Element, 3, false},
			{"close", &v.close.Element, 3, false},
		}
	}
	p := []probe{
		{"tile", &s.head.tile.Element, 3, false},
		{"name", &s.head.name.Element, 2, true},
		{"state", s.head.state, 2, false},
		{"status", &s.head.status.Element, 2, true},
		{"bar", &s.bar.Element, 3, false},
		{"tabs", &s.tabs.Element, 2, false},
		{"toggle", &s.actions.toggle.Element, 3, false},
		{"cancel", &s.actions.cancel.Element, 3, false},
	}
	if visible(&s.head.figure.Element) {
		p = append(p, probe{"figure", &s.head.figure.Element, 2, true})
	}
	if s.tab == tabStatus {
		for _, it := range s.statusV.facts.items {
			if it.show && it.dt.Text() == "Resumable" || it.show && it.dt.Text() == "File size" || it.show && it.dt.Text() == "Address" {
				p = append(p, probe{"dt " + it.dt.Text(), &it.dt.Element, 2, true})
			}
		}
	} else {
		p = append(p, probe{"switch", &s.limitsV.toggle.sw.Element, 3, false}, probe{"switch text", &s.limitsV.toggle.text.Element, 2, true}, probe{"hint", &s.limitsV.hint.Element, 2, true})
	}
	if visible(&s.actions.pause.Element) {
		p = append(p, probe{"pause", &s.actions.pause.Element, 3, false})
	}
	if visible(&s.actions.resume.Element) {
		p = append(p, probe{"resume", &s.actions.resume.Element, 3, false})
	}
	if visible(&s.actions.retry.Element) {
		p = append(p, probe{"retry", &s.actions.retry.Element, 3, false})
	}
	if s.expanded {
		p = append(p, probe{"details title", &s.details.title.Element, 2, true}, probe{"table head", &s.details.table.head.Element, 1, false})
		if t := s.details.table; t.shown > 2 {
			p = append(p, probe{"table row 1", &t.rows[0].Element, 1, false}, probe{"table row 3", &t.rows[2].Element, 1, false},
				probe{"row 1 info", &t.rows[0].cells[3].Element, 2, true})
		}
		if visible(&s.details.smap.Element) {
			p = append(p, probe{"map", &s.details.smap.Element, 2, false})
		}
	}
	return p
}

var progressViews = []string{"pw-status", "pw-limits", "pw-site", "pw-expanded", "pw-site-expanded", "pw-paused", "pw-failed", "pw-complete", "pw-complete-media"}

// TestRenderAgainstReferences renders every progress view of the references (9 views × light
// and dark × 1x and 1.25x × 16 and 20 px text = 72) and compares each with its reference: the
// window's height (the fit), the whole window's difference, and each element's offset from
// where the reference draws it (BestShift of its crop, in DIPs).
func TestRenderAgainstReferences(t *testing.T) {
	refs, err := viewstest.LoadRefs()
	if err != nil {
		t.Skip(err)
	}
	if err := viewstest.CanRender(); err != nil {
		t.Skip(err)
	}
	write := os.Getenv("DOWNEX_VIEWS_WRITE") == "1"
	// the references' page: en-GB in Asia/Baghdad (index.json engine.page)
	if f, err := locale.New("en-GB", "Asia/Baghdad"); err == nil {
		prev := format.Locale()
		format.SetLocale(f)
		t.Cleanup(func() { format.SetLocale(prev) })
	}
	metas := shotMetas(t, refs.Dir)
	var report strings.Builder
	worst, worstText, worstTextDIP := 0.0, 0.0, 0.0
	only := os.Getenv("DOWNEX_PROGRESS_ONLY")
	for _, name := range progressViews {
		if only != "" && only != name {
			continue
		}
		for _, theme := range []string{"light", "dark"} {
			for _, scale := range []float32{1, 1.25} {
				for _, font := range []int{16, 20} {
					shot := findShot(refs, name, float64(scale), font, theme)
					if shot == nil {
						t.Errorf("%s %s %v %d: no reference", name, theme, scale, font)
						continue
					}
					meta := metas[shot.File]
					r, _ := sceneRigAt(t, name, scale, font, theme, meta)
					size := r.pw.Kit.Size()
					img, err := r.render(scale)
					if err != nil {
						t.Fatal(err)
					}
					ref, err := refs.Image(shot)
					if err != nil {
						t.Fatal(err)
					}
					area := kit.R(0, 0, kit.DIP(shot.CSS.W), kit.DIP(shot.CSS.H))
					c := viewstest.Compare(ref, img, area, kit.Point{}, scale)
					dh := float64(size.H) - float64(shot.CSS.H)
					askedRef := meta.asked
					askedNative := float64(r.s().outer().H)
					if askedRef > 0 && math.Abs(askedNative-askedRef) > 0.5 {
						t.Errorf("%s %s %vx %dpx: the window asks for %v, the reference's for %v", name, theme, scale, font, askedNative, askedRef)
					}
					// The client area today's host gives the height asked for: Wails truncates
					// the outer size to device pixels, less the 40 px frame in whole device
					// pixels (refshots.mjs playHost; the reference's page is that floored to
					// whole CSS px, which a Chrome viewport takes).
					if askedRef > 0 {
						dev := math.Floor(askedRef*float64(scale)+1e-6) - math.Round(progress.Frame*float64(scale))
						nativeDev := math.Round(float64(size.H) * float64(scale))
						if nativeDev != dev || math.Floor(float64(size.H)+1e-3) != float64(shot.CSS.H) {
							t.Errorf("%s %s %vx %dpx: client %v DIP (%v px), today's host gives %v px, the reference's page is %v",
								name, theme, scale, font, size.H, nativeDev, dev, shot.CSS.H)
						}
					}
					fmt.Fprintf(&report, "%s %s %vx %dpx: asked %v (ref %v) window %vx%v (ref %vx%v, dH %+.2f) mean %.2f differing %.1f%%",
						name, theme, scale, font, askedNative, askedRef, size.W, size.H, shot.CSS.W, shot.CSS.H, dh, c.Mean, 100*c.Differing)
					for _, p := range r.probes() {
						if !visible(p.el) {
							continue
						}
						b := p.el.WindowBounds()
						crop := kit.R(b.Min.X-p.pad, b.Min.Y-p.pad, b.Max.X+p.pad, b.Max.Y+p.pad)
						maxPx := int(math.Ceil(float64(4 * scale)))
						dx, dy, _, _ := viewstest.BestShift(ref, img, crop, crop.Min, scale, maxPx)
						ddx, ddy := -float64(dx)/float64(scale), -float64(dy)/float64(scale)
						worst = math.Max(worst, math.Max(math.Abs(ddx), math.Abs(ddy)))
						fmt.Fprintf(&report, " | %s %+.1f,%+.1f", p.name, ddx, ddy)
						if p.text {
							// the text's drawn width against the reference's at the matched place
							at := crop.Add(kit.Point{X: -kit.DIP(float64(dx) / float64(scale)), Y: -kit.DIP(float64(dy) / float64(scale))})
							rw, nw := inkWidth(ref, at, scale), inkWidth(img, crop, scale)
							if rw > 0 {
								pct := 100 * (nw - rw) / rw
								worstText = math.Max(worstText, math.Abs(pct))
								worstTextDIP = math.Max(worstTextDIP, math.Abs(nw-rw))
								fmt.Fprintf(&report, " w%+.1f%% (%+.1f)", pct, nw-rw)
							}
						}
					}
					report.WriteString("\n")
					if write {
						state := name
						if font == 20 {
							state += "-20px"
						}
						path := filepath.Join(viewstest.ShotDir(), viewstest.NativeName("progress", state, theme, 520, float64(scale)))
						if err := viewstest.WritePNG(path, c.Sheet); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
		}
	}
	fmt.Fprintf(&report, "worst element offset %.2f DIP, worst text width %.2f%% (%.2f DIP)\n", worst, worstText, worstTextDIP)
	t.Logf("%s", report.String())
	if out := os.Getenv("DOWNEX_PROGRESS_REPORT"); out != "" {
		_ = os.WriteFile(out, []byte(report.String()), 0o644)
	}
}

// findShot is the reference of a view at a scale, text size and theme (its height is the one
// the page asked for).
func findShot(refs *viewstest.Refs, name string, scale float64, font int, theme string) *viewstest.Shot {
	for _, s := range refs.Shots {
		if s.Kind == "progress" && s.Name == name && s.Scale == scale && s.Font == font && s.Theme == theme {
			return s
		}
	}
	return nil
}

var (
	_ = views.FontBody
	_ = widgets.FontBody
)

// shotMeta is what the references' index says of a shot's page: the window height it asked
// its host for, how long after its load it was taken, and when.
type shotMeta struct {
	asked    float64
	ms       float64
	captured int64
}

// ticks is the demo's 250 ms steps a reference's page had run: its scene ticks from a little
// before the page's load clock starts (the figures of pw-status and pw-site match their
// references at msSinceLoad + 550 ms).
func (m shotMeta) ticks() int { return int((m.ms + 550) / 250) }

// shotMetas are the references' shot metadata, by file.
func shotMetas(t *testing.T, dir string) map[string]shotMeta {
	b, err := os.ReadFile(filepath.Join(dir, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	b = bytes.TrimPrefix(b, []byte("\ufeff"))
	var idx struct {
		Shots []struct {
			File     string    `json:"file"`
			Ms       float64   `json:"msSinceLoad"`
			Captured time.Time `json:"capturedAt"`
			Host     *struct {
				Asked float64 `json:"askedWindowHeight"`
			} `json:"host"`
		} `json:"shots"`
	}
	if err := json.Unmarshal(b, &idx); err != nil {
		t.Fatal(err)
	}
	out := map[string]shotMeta{}
	for _, s := range idx.Shots {
		m := shotMeta{ms: s.Ms, captured: s.Captured.UnixMilli()}
		if s.Host != nil {
			m.asked = s.Host.Asked
		}
		out[s.File] = m
	}
	return out
}

// inkWidth is the width, in DIPs, of what is drawn inside area of img, to a fraction of a
// pixel: each column's ink is its largest difference from the area's ground (its corner) in a
// channel; the columns between the first and the last with at least half the strongest
// column's ink count whole, and the fainter columns outside them (the antialiased edges) count
// by their share of that strongest ink.
func inkWidth(img image.Image, area kit.Rect, scale float32) float64 {
	px := func(v kit.DIP) int { return int(math.Round(float64(v * scale))) }
	r := image.Rect(px(area.Min.X), px(area.Min.Y), px(area.Max.X), px(area.Max.Y)).Intersect(img.Bounds())
	if r.Empty() {
		return 0
	}
	ground := color.RGBAModel.Convert(img.At(r.Min.X, r.Min.Y)).(color.RGBA)
	ink := make([]float64, r.Dx())
	strongest := 0.0
	for x := r.Min.X; x < r.Max.X; x++ {
		m := 0
		for y := r.Min.Y; y < r.Max.Y; y++ {
			c := color.RGBAModel.Convert(img.At(x, y)).(color.RGBA)
			m = max(m, absDiff(c.R, ground.R), absDiff(c.G, ground.G), absDiff(c.B, ground.B))
		}
		ink[x-r.Min.X] = float64(m)
		strongest = math.Max(strongest, float64(m))
	}
	if strongest <= 48 {
		return 0
	}
	first, last := -1, -1
	for i, v := range ink {
		if v >= strongest/2 {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	w := float64(last - first + 1)
	for i := first - 1; i >= 0 && ink[i] > 8; i-- {
		w += ink[i] / strongest
	}
	for i := last + 1; i < len(ink) && ink[i] > 8; i++ {
		w += ink[i] / strongest
	}
	return w / float64(scale)
}

func absDiff(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}
