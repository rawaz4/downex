package frontend

import (
	"strings"
	"testing"
)

func TestParseColor(t *testing.T) {
	for _, c := range []struct {
		in   string
		want Color
	}{
		{"#eef2f0", Color{R: 0xee / 255.0, G: 0xf2 / 255.0, B: 0xf0 / 255.0, A: 1, Bytes: [3]uint8{0xee, 0xf2, 0xf0}, Hex: 0xeef2f0}},
		{"#fff", Color{R: 1, G: 1, B: 1, A: 1, Bytes: [3]uint8{255, 255, 255}, Hex: 0xffffff}},
		{"rgb(60 203 139 / 0.14)", Color{R: 60 / 255.0, G: 203 / 255.0, B: 139 / 255.0, A: 0.14, Bytes: [3]uint8{60, 203, 139}, Hex: -1}},
		{"rgb(0, 0, 0)", Color{A: 1, Hex: -1}},
		{"rgba(255, 255, 255, 50%)", Color{R: 1, G: 1, B: 1, A: 0.5, Bytes: [3]uint8{255, 255, 255}, Hex: -1}},
		{"transparent", Color{Hex: -1}},
	} {
		got, err := ParseColor(c.in)
		if err != nil || got != c.want {
			t.Errorf("ParseColor(%q) = %+v, %v; want %+v", c.in, got, err, c.want)
		}
	}
	for _, bad := range []string{"#12345", "rgb(256 0 0)", "rgb(1 2)", "rgb(0 0 0 / 2)", "red", "color-mix(in srgb, red, blue)"} {
		if _, err := ParseColor(bad); err == nil {
			t.Errorf("ParseColor(%q) took it", bad)
		}
	}
}

func TestParseShadows(t *testing.T) {
	got, err := ParseShadows("inset 0 1px 0 rgb(255 255 255 / 0.18), 0 1px 2px rgb(8 60 36 / 0.25)")
	if err != nil || len(got) != 2 || !got[0].Inset || got[0].Y != 1 || got[1].Blur != 2 || got[1].Color.A != 0.25 {
		t.Errorf("primary-lift = %+v, %v", got, err)
	}
	got, err = ParseShadows("0 24px 64px rgb(0 0 0 / 0.6), 0 0 0 1px rgb(255 255 255 / 0.05)")
	if err != nil || got[1].Spread != 1 || got[0].Y != 24 || got[0].Blur != 64 {
		t.Errorf("shadow-lg = %+v, %v", got, err)
	}
	if got, err := ParseShadows("none"); err != nil || got != nil {
		t.Errorf("none = %v, %v", got, err)
	}
	if _, err := ParseShadows("0 1px"); err == nil {
		t.Error("a shadow without a colour was taken")
	}
}

// JavaScript's toFixed rounds the exact binary value, half away from zero.
func TestToFixed(t *testing.T) {
	for _, c := range []struct {
		v    float64
		d    int
		want string
	}{
		{1.005, 2, "1.00"}, // 1.00499999999999989...
		{1.125, 2, "1.13"}, // an exact tie
		{2.5, 0, "3"},
		{-1.125, 2, "-1.13"},
		{21.2, 2, "21.20"},
		{0, 2, "0.00"},
		{12 + 9.2*0.7071067811865476, 2, "18.51"},
	} {
		if got := ToFixed(c.v, c.d); got != c.want {
			t.Errorf("ToFixed(%v, %d) = %s, want %s", c.v, c.d, got, c.want)
		}
	}
}

func TestMarkup(t *testing.T) {
	src := `<script lang="ts">let x = 1;</script>
<svg class="tile kind-{shown}" viewBox={TILE_VIEWBOX} aria-hidden="true">
  {#if shown === 'video'}
    <g transform="rotate(-11 4.5 10.5)">
      <rect x="4.5" y="5" width="24" height="5.6" rx="1.6" fill="var(--deep)" />
    </g>
  {:else if shown === 'audio'}
    <!-- a comment -->
    <circle cx="16" cy="16" r="13.5" fill="var(--hue)" />
  {:else}
    <path d="M1 1h2" fill="var(--hue)" />
  {/if}
</svg>
<style>.tile { display: block; }</style>`
	nodes, err := Markup(src)
	if err != nil {
		t.Fatal(err)
	}
	svg := find(nodes, "svg")
	if svg == nil || svg.Attrs["viewBox"] != "{TILE_VIEWBOX}" || svg.Attrs["class"] != "tile kind-{shown}" {
		t.Fatalf("svg %+v", svg)
	}
	ifb := svg.Children[0]
	if ifb.Tag != "#if" || len(ifb.Branches) != 3 || ifb.Branches[1].Cond != "shown === 'audio'" || ifb.Branches[2].Cond != "" {
		t.Fatalf("if block %+v", ifb)
	}
	if g := ifb.Branches[0].Children[0]; g.Tag != "g" || g.Children[0].Attrs["rx"] != "1.6" {
		t.Errorf("video branch %+v", g)
	}
	for _, bad := range []string{
		`<svg><rect x="1"></svg>`,         // an unclosed element
		`<svg>text</svg>`,                 // text
		`<svg>{#if a}<path d="M"/>`,       // an if that never ends
		`<svg><path d="M" fill=x/></svg>`, // an unquoted attribute
	} {
		if _, err := Markup(bad); err == nil {
			t.Errorf("Markup(%q) took it", bad)
		}
	}
}

func TestRectAndCirclePaths(t *testing.T) {
	if got := RectPath(3.4, 2.8, 2.6, 8.4, 0.9); got != "M4.3 2.8H5.1A0.9 0.9 0 0 1 6 3.7V10.3A0.9 0.9 0 0 1 5.1 11.2H4.3A0.9 0.9 0 0 1 3.4 10.3V3.7A0.9 0.9 0 0 1 4.3 2.8Z" {
		t.Errorf("rect: %s", got)
	}
	if got := RectPath(0, 0, 4, 2, 3); !strings.Contains(got, "A1 1") {
		t.Errorf("rx is not clamped to half the height: %s", got)
	}
	if got := CirclePath(7, 7, 6.2); got != "M0.8 7A6.2 6.2 0 1 0 13.2 7A6.2 6.2 0 1 0 0.8 7Z" {
		t.Errorf("circle: %s", got)
	}
}

func TestGoName(t *testing.T) {
	for in, want := range map[string]string{
		"--line-strong": "LineStrong", "--surface-2": "Surface2", "--font-ui": "FontUI", "win-close": "WinClose",
		"--r-2xl": "R2xl", "c-video-deep": "CVideoDeep",
	} {
		if got := GoName(in); got != want {
			t.Errorf("GoName(%q) = %q, want %q", in, got, want)
		}
	}
}
