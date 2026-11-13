package tokens

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"downex/ui/kit"
	"downex/ui/kit/frontend"
)

// The drift test: tokens_gen.go is what app.css gives today. A token changed in one UI and
// not the other fails here; go generate ./ui/kit/tokens brings them together again.
func TestGeneratedTokensMatchAppCSS(t *testing.T) {
	src, err := frontend.Dir()
	if err != nil {
		t.Fatal(err)
	}
	want, err := frontend.GenerateTokens(src)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile("tokens_gen.go")
	if err != nil {
		t.Fatal(err)
	}
	got = bytes.ReplaceAll(got, []byte("\r\n"), []byte("\n"))
	if !bytes.Equal(got, want) {
		t.Fatalf("tokens_gen.go is out of date with %s: run go generate ./ui/kit/tokens\n%s",
			filepath.Join(src, "app.css"), firstDiff(string(got), string(want)))
	}
}

// The guard itself: a token changed in app.css changes the generated file.
func TestDriftIsCaught(t *testing.T) {
	src, err := frontend.Dir()
	if err != nil {
		t.Fatal(err)
	}
	css, err := os.ReadFile(filepath.Join(src, "app.css"))
	if err != nil {
		t.Fatal(err)
	}
	base, err := frontend.ReadTokens(string(css))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := base.Generate()
	for _, edit := range []struct{ from, to string }{
		{"--chrome: #eef2f0;", "--chrome: #eef2f1;"}, // a light colour
		{"--focus: #6fdcaa;", "--focus: #6fdcab;"},   // a dark colour: both dark blocks must change
		{"--titlebar-h: 44px;", "--titlebar-h: 46px;"},
		{"--dur-2: 120ms;", "--dur-2: 130ms;"},
	} {
		changed := strings.ReplaceAll(string(css), edit.from, edit.to)
		if changed == string(css) {
			t.Fatalf("app.css has no %q any more", edit.from)
		}
		tok, err := frontend.ReadTokens(changed)
		if err != nil {
			t.Fatalf("%s: %v", edit.to, err)
		}
		got, err := tok.Generate()
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(got, want) {
			t.Errorf("changing %q to %q left the generated tokens the same", edit.from, edit.to)
		}
	}
	// One dark copy changed without the other is an error, not a silent pick.
	once := strings.Replace(string(css), "--focus: #6fdcaa;", "--focus: #6fdcab;", 1)
	if _, err := frontend.ReadTokens(once); err == nil || !strings.Contains(err.Error(), "two dark blocks differ") {
		t.Errorf("one dark block changed: %v", err)
	}
}

func firstDiff(a, b string) string {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := 0; i < len(al) || i < len(bl); i++ {
		var x, y string
		if i < len(al) {
			x = al[i]
		}
		if i < len(bl) {
			y = bl[i]
		}
		if x != y {
			return "line " + itoa(i+1) + ":\n  checked in: " + x + "\n  app.css:    " + y
		}
	}
	return ""
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for ; i > 0; i /= 10 {
		b = append([]byte{byte('0' + i%10)}, b...)
	}
	return string(b)
}

// The kit's own motion constants are app.css's.
func TestKitMotionIsAppCSS(t *testing.T) {
	for _, c := range []struct {
		kit, css time.Duration
		name     string
	}{
		{kit.DurPress, Dur1, "--dur-1"}, {kit.DurHover, Dur2, "--dur-2"}, {kit.DurMenu, Dur3, "--dur-3"},
		{kit.DurDialog, Dur4, "--dur-4"}, {kit.DurRow, Dur5, "--dur-5"}, {kit.DurExit, DurExit, "--dur-exit"},
	} {
		if c.kit != c.css {
			t.Errorf("kit's %s is %v, app.css %v", c.name, c.kit, c.css)
		}
	}
	for _, c := range []struct {
		kit  kit.Easing
		css  Bezier
		name string
	}{{kit.EaseOut, EaseOut, "--ease-out"}, {kit.EaseIn, EaseIn, "--ease-in"}} {
		e := c.css.Easing()
		for x := float32(0); x <= 1; x += 0.05 {
			if d := c.kit(x) - e(x); d > 1e-6 || d < -1e-6 {
				t.Errorf("%s at %v: kit %v, app.css %v", c.name, x, c.kit(x), e(x))
				break
			}
		}
	}
}

// Every colour token has a forced-colours role, and Forced paints them in the system's
// colours without shadows.
func TestForcedTheme(t *testing.T) {
	for _, name := range ColorNames() {
		if _, ok := forcedRoles[name]; !ok {
			t.Errorf("colour token %s has no forced-colours role (tokens/forced.go)", name)
		}
	}
	for name := range forcedRoles {
		if _, ok := Light.Color(name); !ok {
			t.Errorf("forced-colours role for %s, which app.css no longer has", name)
		}
	}
	sc := kit.SystemColors{
		Canvas: kit.RGB(0x000000), CanvasText: kit.RGB(0xffffff), Highlight: kit.RGB(0x1aebff),
		HighlightText: kit.RGB(0x000000), GrayText: kit.RGB(0x3ff23f), ButtonFace: kit.RGB(0x000000),
		ButtonText: kit.RGB(0xffffff), LinkText: kit.RGB(0xffff00),
	}
	f := Forced(sc)
	if !f.IsForced() || !f.Dark || f.Chrome != sc.Canvas || f.Ink != sc.CanvasText || f.Focus != sc.Highlight ||
		f.LineStrong != sc.CanvasText || f.AccentOn != sc.HighlightText || f.CVideo != Light.CVideo {
		t.Errorf("forced theme: %+v", f)
	}
	if f.ShadowSm != nil || f.PrimaryLift != nil {
		t.Error("the forced theme has shadows")
	}
	if Light.IsForced() || Dark.IsForced() {
		t.Error("light or dark marked forced")
	}
	if p := f.Palette(); p.Ground != sc.Canvas || p.Text != sc.CanvasText || p.Focus != sc.Highlight {
		t.Errorf("forced palette %+v", p)
	}
}

func TestThemeChoice(t *testing.T) {
	light := kit.Environment{}
	dark := kit.Environment{Dark: true}
	hc := kit.Environment{HighContrast: true, SystemColors: kit.SystemColors{Canvas: kit.RGB(0xffffff)}}
	for _, c := range []struct {
		choice Choice
		env    kit.Environment
		want   *Theme
	}{
		{System, light, &Light}, {System, dark, &Dark}, {ChoiceLight, dark, &Light}, {ChoiceDark, light, &Dark},
	} {
		if got := For(c.choice, c.env); got != c.want {
			t.Errorf("For(%v, dark=%v) is the %v theme", c.choice, c.env.Dark, got.Dark)
		}
	}
	f := For(ChoiceDark, hc)
	if !f.IsForced() || f.Dark {
		t.Error("a white contrast theme does not give the forced light theme")
	}
	// Asked for in every Paint: the same theme while the colours stay, without allocating.
	if For(System, hc) != f {
		t.Error("For made the forced theme again for the same system colours")
	}
	if n := testing.AllocsPerRun(20, func() { For(System, hc) }); n != 0 {
		t.Errorf("For under a contrast theme allocated %v times", n)
	}
	black := kit.Environment{HighContrast: true, SystemColors: kit.SystemColors{Canvas: kit.RGB(0x000000), CanvasText: kit.RGB(0xffffff)}}
	if g := For(System, black); g == f || !g.Dark || g.Ink != kit.RGB(0xffffff) {
		t.Errorf("new system colours kept the old forced theme (dark %v, ink %v)", g.Dark, g.Ink)
	}
	order := []Choice{System, ChoiceLight, ChoiceDark, System}
	for i := 0; i+1 < len(order); i++ {
		if order[i].Next() != order[i+1] {
			t.Errorf("%v.Next() = %v, want %v (stores/theme.svelte.ts ORDER)", order[i], order[i].Next(), order[i+1])
		}
	}
	for _, s := range []string{"system", "light", "dark"} {
		if c, ok := ParseChoice(s); !ok || c.String() != s {
			t.Errorf("ParseChoice(%q) = %v, %v", s, c, ok)
		}
	}
	if _, ok := ParseChoice("Dark"); ok {
		t.Error("ParseChoice took a wrong spelling")
	}
}

func TestPalettesAndFrames(t *testing.T) {
	if p := Light.Palette(); p.Ground != kit.RGB(0xeef2f0) || p.Text != kit.RGB(0x0f1613) || p.Focus != kit.RGB(0x10804f) ||
		p.ScrollThumb != kit.RGB(0xc9d1cd) || p.ScrollThumbActive != kit.RGB(0x959b98) {
		t.Errorf("light palette %+v", p)
	}
	if p := Dark.Palette(); p.Ground != kit.RGB(0x0a0e0c) || p.ScrollThumbActive != kit.RGB(0x656f6a) {
		t.Errorf("dark palette %+v", p)
	}
	if f := Dark.MainFrame(); f.Background != Dark.Chrome || !f.Dark || f.Border != (kit.Color{}) {
		t.Errorf("dark main frame %+v", f)
	}
	if f := Light.CaptionFrame(); f.Caption != Light.Surface || f.Text != Light.Ink || f.InactiveText != Light.Faint ||
		f.Border != Light.LineStrong || f.InactiveBorder != Light.Line || f.Dark {
		t.Errorf("light caption frame %+v", f)
	}
	// --brand-ring is set by the dark theme only: transparent in light.
	if Light.BrandRing != (kit.Color{}) || Dark.BrandRing != Dark.BrandHairline {
		t.Errorf("brand ring light %v, dark %v", Light.BrandRing, Dark.BrandRing)
	}
}
