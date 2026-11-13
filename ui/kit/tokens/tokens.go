// Package tokens is the "Studio" design's tokens for the native window: app.css's colours,
// shadows, type sizes, spacing, radii, window metrics, durations and easings, in the light
// and the dark theme, generated from ui/frontend/src/app.css (tokens_gen.go; go generate
// writes it, and the drift test fails when app.css and it disagree), plus the forced-colours
// theme of a Windows contrast theme and the few values the WebView window derives rather
// than reads from app.css (measured, with their source). It is platform-free.
package tokens

//go:generate go run ../internal/gen -tokens tokens_gen.go

import (
	"sync/atomic"

	"downex/ui/kit"
)

// Override is a :root length that applies only under a condition: inside a width media query
// (When), or in the compact density.
type Override struct {
	Token  string
	When   kit.Query
	Length kit.Length
}

// Rule and Decl are a CSS rule as app.css writes it (ForcedRules).
type Rule struct {
	Selectors []string
	Decls     []Decl
}

// Decl is one declaration of a Rule.
type Decl struct {
	Prop, Value string
}

// Bezier is a cubic-bezier() easing's x1, y1, x2, y2.
type Bezier [4]float32

// Easing is the curve as the kit's easing function.
func (b Bezier) Easing() kit.Easing { return kit.CubicBezier(b[0], b[1], b[2], b[3]) }

// Choice is the app's theme setting: follow the system, or light or dark.
type Choice uint8

// The theme choices, in the order the title bar's button cycles through them
// (stores/theme.svelte.ts: system, light, dark).
const (
	System Choice = iota
	ChoiceLight
	ChoiceDark
)

// String is the choice as ui-theme.json and the -theme flag write it.
func (c Choice) String() string {
	switch c {
	case ChoiceLight:
		return "light"
	case ChoiceDark:
		return "dark"
	}
	return "system"
}

// ParseChoice reads "system", "light" or "dark".
func ParseChoice(s string) (Choice, bool) {
	switch s {
	case "system":
		return System, true
	case "light":
		return ChoiceLight, true
	case "dark":
		return ChoiceDark, true
	}
	return System, false
}

// Next is the choice the theme button moves to.
func (c Choice) Next() Choice { return (c + 1) % 3 }

// For is the theme a window shows for the choice in an environment: forced colours while a
// contrast theme is on (app.css follows forced-colors whatever the choice), else light or
// dark, System following the system's app mode. The views ask for it in every Paint, so
// the forced theme is made once per set of system colours and shared until they change.
func For(c Choice, env kit.Environment) *Theme {
	switch {
	case env.HighContrast:
		if f := lastForced.Load(); f != nil && f.colors == env.SystemColors {
			return &f.theme
		}
		f := &forcedTheme{colors: env.SystemColors, theme: Forced(env.SystemColors)}
		lastForced.Store(f)
		return &f.theme
	case c == ChoiceDark, c == System && env.Dark:
		return &Dark
	}
	return &Light
}

// forcedTheme is the forced theme For made last, and the system colours it is made of.
type forcedTheme struct {
	colors kit.SystemColors
	theme  Theme
}

var lastForced atomic.Pointer[forcedTheme]

// Palette is the kit's own colours in the theme: the window's ground, the default text,
// the focus ring and the scroll bar's thumb.
func (t *Theme) Palette() kit.Palette {
	p := kit.Palette{Ground: t.Chrome, Text: t.Ink, Focus: t.Focus, ScrollThumb: t.LineStrong}
	switch {
	case t.forced:
		p.ScrollThumbActive = t.Focus
	case t.Dark:
		p.ScrollThumbActive = ScrollThumbActiveDark
	default:
		p.ScrollThumbActive = ScrollThumbActiveLight
	}
	return p
}

// MainFrame is the main window's frame in the theme: the chrome under the first frame,
// and the system's dark or light frame and menus. The custom frame has no caption, and the
// window's 1 px border keeps the system's colour, as the WebView window's frameless one does.
func (t *Theme) MainFrame() kit.FrameColors {
	return kit.FrameColors{Background: t.Chrome, Dark: t.Dark}
}

// CaptionFrame is a system-framed window's frame (the progress windows): the surface as
// the caption, the ink as its title (faint while inactive), line-strong as the border (line
// while inactive), as ui/theme.go's captionTheme gives them.
func (t *Theme) CaptionFrame() kit.FrameColors {
	return kit.FrameColors{
		Background: t.Surface, Caption: t.Surface, Text: t.Ink, InactiveText: t.Faint,
		Border: t.LineStrong, InactiveBorder: t.Line, Dark: t.Dark,
	}
}

// Color is the colour token name ("--chrome") in the theme, and whether there is one.
func (t *Theme) Color(name string) (kit.Color, bool) {
	for _, c := range colorTokens {
		if c.name == name {
			return *c.field(t), true
		}
	}
	return kit.Color{}, false
}

// ColorNames are the colour tokens, in app.css's order.
func ColorNames() []string {
	out := make([]string, len(colorTokens))
	for i, c := range colorTokens {
		out[i] = c.name
	}
	return out
}

// The values the WebView window shows that app.css does not hold.
var (
	// ScrollThumbActiveLight and ScrollThumbActiveDark are the scroll bar thumb under the
	// pointer or dragged: WebView2 derives it from app.css's scrollbar-color thumb
	// (--line-strong); measured in headless Edge 156, 2026-11-03 (ui/kit's scroll bar
	// measurements).
	ScrollThumbActiveLight = kit.RGB(0x959b98)
	ScrollThumbActiveDark  = kit.RGB(0x656f6a)
)
