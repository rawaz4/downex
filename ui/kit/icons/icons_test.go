package icons

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"downex/internal/brand"
	"downex/ui/kit"
	"downex/ui/kit/frontend"
	"downex/ui/kit/kittest"
	"downex/ui/kit/tokens"
)

// The drift test: icons_gen.go is what Icon.svelte, FileIcon.svelte and StatusMark.svelte
// give today; go generate ./ui/kit/icons brings them together again.
func TestGeneratedIconsMatchComponents(t *testing.T) {
	src, err := frontend.Dir()
	if err != nil {
		t.Fatal(err)
	}
	want, err := frontend.GenerateIcons(src)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile("icons_gen.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.ReplaceAll(got, []byte("\r\n"), []byte("\n")), want) {
		t.Fatal("icons_gen.go is out of date with the Svelte icons: run go generate ./ui/kit/icons")
	}
	if numIcons < 60 || numTiles != 7 || numMarks != 7 {
		t.Errorf("%d icons, %d tiles, %d marks: the components were read wrong", numIcons, numTiles, numMarks)
	}
	for _, n := range []string{"search", "gear", "system", "sun", "moon", "win-minimise", "win-maximise", "win-restore", "win-close"} {
		if _, ok := ByName(n); !ok {
			t.Errorf("the title bar's icon %s is missing", n)
		}
	}
	if WinMinimise.String() != "win-minimise" || lineIcons[WinClose].stroke != 1.3 || lineIcons[Search].stroke != 1.6 || !lineIcons[Resume].filled {
		t.Error("stroke widths or fills not read from Icon.svelte")
	}
}

// BrandMark.svelte's layout is ported by hand (brand.go): its source is compared, and the
// port draws what internal/brand draws for the exe's icon.
func TestBrandMatchesComponentAndInternalBrand(t *testing.T) {
	src, err := frontend.Dir()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(src, "lib", "components", "BrandMark.svelte"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := frontend.FunctionSources(string(b), "fit", "brandLayout", "barPath")
	if err != nil {
		t.Fatal(err)
	}
	if got != brandSource {
		t.Fatalf("BrandMark.svelte's layout changed: port it to brand.go and copy its source into brandSource:\n%s", got)
	}
	for _, size := range []int{16, 20, 22, 24, 32, 40, 48, 64} {
		img := brand.App(size, brand.Dark)
		l := Brand(float64(size))
		at := func(x, y float64) kit.Color {
			c := img.NRGBAAt(int(x), int(y))
			return kit.RGBA(c.R, c.G, c.B, 1)
		}
		for i, bar := range l.Bars {
			want := tokens.Light.BrandBar
			if bar.Head {
				want = tokens.Light.BrandHead
			}
			// Every pixel well inside the bar's square part.
			for y := bar.Y; y < bar.Y+bar.H; y++ {
				for x := bar.X; x < bar.X+bar.W-bar.R; x++ {
					if c := at(x, y); c != want {
						t.Fatalf("%d px, bar %d: internal/brand has %v at %v,%v, the port a bar of %v", size, i, c, x, y, want)
					}
				}
			}
		}
		// The gap under the first bar is the tile (the bars do not run into each other).
		gapY := l.Bars[0].Y + l.Bars[0].H
		if c := at(l.Bars[0].X+1, gapY); c != tokens.Light.BrandTile {
			t.Errorf("%d px: the gap under the first bar is %v in internal/brand, want the tile", size, c)
		}
	}
}

// Icon.svelte's gear() runs in Node where it is installed: the Go port gives the same
// path, toFixed's rounding included.
func TestGearMatchesJavaScript(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	src, err := frontend.Dir()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(src, "lib", "components", "Icon.svelte"))
	if err != nil {
		t.Fatal(err)
	}
	fn, err := frontend.FunctionSources(string(b), "gear")
	if err != nil {
		t.Fatal(err)
	}
	// The TypeScript annotations gear() uses, removed so plain JavaScript runs it.
	js := strings.NewReplacer("(): string", "()", "const pts: string[]", "const pts", "const seq: [number, number][]", "const seq").Replace(fn)
	file := filepath.Join(t.TempDir(), "gear.mjs")
	if err := os.WriteFile(file, []byte(js+"\nprocess.stdout.write(gear());\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, file).Output()
	if err != nil {
		t.Skipf("node could not run gear(): %v", err)
	}
	if got := frontend.GearPath(); string(out) != got {
		t.Errorf("gear():\nJavaScript %s\nGo         %s", out, got)
	}
	if lineIcons[Gear].d != string(out) {
		t.Error("the generated gear differs from JavaScript's")
	}
}

// The drawing calls: an icon is stroked (and filled when filled) through one transform, a
// tile picks its small variant at 20 px and below, a mark uses its kind's colour, and in
// forced colours CanvasText with the glyph in Canvas.
func TestDraw(t *testing.T) {
	res := kittest.NewResources()
	s := NewSet(res)
	c := &kittest.Canvas{}
	s.DrawIcon(c, Resume, kit.Pt(10, 10), 17, tokens.Light.Muted)
	if got := kinds(c); got != "PushTransform FillPath StrokePath Pop" {
		t.Errorf("filled icon: %s", got)
	}
	c.Reset()
	s.DrawIcon(c, Search, kit.Pt(0, 0), 15, tokens.Light.Faint)
	if got := kinds(c); got != "PushTransform StrokePath Pop" {
		t.Errorf("line icon: %s", got)
	}
	made := s.Len()
	s.DrawIcon(c, Search, kit.Pt(5, 5), 20, tokens.Light.Faint)
	if s.Len() != made {
		t.Error("drawing an icon again made its path again")
	}

	c.Reset()
	s.DrawTile(c, TileAudio, kit.Pt(0, 0), 26, &tokens.Light)
	big := c.Count("StrokePath")
	c.Reset()
	s.DrawTile(c, TileAudio, kit.Pt(0, 0), 16, &tokens.Light)
	if big != 1 || c.Count("StrokePath") != 1 || res.PathsMade == 0 {
		t.Errorf("audio tile strokes: %d at 26 px, %d at 16 px", big, c.Count("StrokePath"))
	}
	c.Reset()
	s.DrawTile(c, TilePrograms, kit.Pt(0, 0), 26, &tokens.Dark)
	if c.Count("PushOpacity") != 1 {
		t.Error("the programs tile's deep face is not drawn at its opacity")
	}
	c.Reset()
	s.DrawTile(c, TileVideo, kit.Pt(0, 0), 26, &tokens.Light)
	if c.Count("PushTransform") != 3 { // the tile, and the clapper's two rotated shapes
		t.Errorf("video tile transforms: %d", c.Count("PushTransform"))
	}

	c.Reset()
	s.DrawMark(c, MarkDownloading, kit.Pt(0, 0), 14, &tokens.Light)
	var fills, strokes []kit.Color
	for _, o := range c.Ops {
		switch o.Kind {
		case "FillPath":
			fills = append(fills, o.Color)
		case "StrokePath":
			strokes = append(strokes, o.Color)
		}
	}
	if len(fills) != 1 || len(strokes) != 1 || fills[0] != tokens.Light.Accent || strokes[0] != tokens.Light.AccentOn {
		t.Errorf("downloading mark: fills %v, strokes %v", fills, strokes)
	}
	forced := tokens.Forced(kit.SystemColors{Canvas: kit.RGB(0x000000), CanvasText: kit.RGB(0xffffff), Highlight: kit.RGB(0x1aebff)})
	c.Reset()
	s.DrawMark(c, MarkDownloading, kit.Pt(0, 0), 14, &forced)
	fills, strokes = nil, nil
	for _, o := range c.Ops {
		switch o.Kind {
		case "FillPath":
			fills = append(fills, o.Color)
		case "StrokePath":
			strokes = append(strokes, o.Color)
		}
	}
	if len(fills) != 1 || fills[0] != kit.RGB(0xffffff) || strokes[0] != kit.RGB(0x000000) {
		t.Errorf("forced downloading mark: fills %v, strokes %v, want CanvasText and Canvas", fills, strokes)
	}
	if MarkOf("probing") != MarkDownloading || MarkOf("awaiting_confirmation") != MarkAwaiting || MarkOf("queued") != MarkQueued {
		t.Error("MarkOf")
	}

	c.Reset()
	s.DrawBrand(c, kit.Pt(14, 10), 24, &tokens.Dark)
	if c.Count("FillRoundedRect") != 1 || c.Count("StrokeRoundedRect") != 1 || c.Count("FillPath") != 5 {
		t.Errorf("dark 24 px logo: %s", kinds(c))
	}
	c.Reset()
	s.DrawBrand(c, kit.Pt(14, 10), 24, &tokens.Light)
	if c.Count("StrokeRoundedRect") != 0 {
		t.Error("the light logo has the dark theme's ring")
	}
	// A steady repaint of the logo allocates nothing (the title bar repaints it): its bars'
	// path data is made once (before, 68 allocations a call).
	c.Quiet = true
	for _, size := range []float64{24, 44} {
		s.DrawBrand(c, kit.Pt(14, 10), size, &tokens.Dark)
		if n := testing.AllocsPerRun(50, func() { s.DrawBrand(c, kit.Pt(14, 10), size, &tokens.Dark) }); n != 0 {
			t.Errorf("DrawBrand at %v px allocates %.1f times a call", size, n)
		}
	}
	c.Quiet = false
	// Path data that failed is remembered as nil; Release must step over it.
	if s.path("") != nil || s.Err() == nil {
		t.Fatal("empty path data made a path")
	}
	s.Release()
	if res.PathsLive != 0 {
		t.Errorf("%d paths alive after Release", res.PathsLive)
	}
}

func kinds(c *kittest.Canvas) string {
	var out []string
	for _, o := range c.Ops {
		out = append(out, o.Kind)
	}
	return strings.Join(out, " ")
}
