// Package widgets is the native window's widget layer: the Studio controls of the WebView
// window (ui/frontend/src/app.css and lib/components) rebuilt on ui/kit, with their looks,
// sizes, colours, motion, keyboard models, wording and semantic nodes. Buttons, icon and
// split buttons, menus and Select lists in popup windows, segmented controls, tabs, switches,
// check boxes, radio rows, chips and pills, toasts and notices, the dialog shell, native text
// fields of every kind, a scroll pane, and the progress and segment bars. Tooltips are the
// system's, as the WebView window's title tooltips are (EnableTooltips).
//
// It is platform-free: popups, field kinds and tooltips come from the host through the
// host capabilities (kit.Popups, kit.Tooltips, kit.NativeFieldKinds), and every widget still works (without them) on a host that has none.
//
// Colours come from the theme of the nearest ancestor that implements ThemeSource (a screen's
// root, or a Scope), else the System theme; a widget reads them at paint time, so a theme
// change needs nothing but a repaint.
package widgets

import (
	"downex/ui/kit"
	"downex/ui/kit/tokens"
)

// Theme gives the colours to draw with in an environment: the app's System, Light or Dark
// choice, or forced colours while a contrast theme is on (ui/views.Theme is one).
type Theme interface {
	Tokens(env kit.Environment) *tokens.Theme
}

// ThemeSource is a widget that gives the widgets inside it their theme.
type ThemeSource interface {
	WidgetTheme() Theme
}

// Choice is a Theme for a fixed choice; SystemDark, when set, stands in for the system's app
// mode (screenshots compared with references made in one mode).
type Choice struct {
	Choice     tokens.Choice
	SystemDark *bool
}

// Tokens is Theme's.
func (c *Choice) Tokens(env kit.Environment) *tokens.Theme {
	if c.SystemDark != nil {
		env.Dark = *c.SystemDark
	}
	return tokens.For(c.Choice, env)
}

var systemTheme = &Choice{Choice: tokens.System}

// Scope is a plain block box that gives the widgets inside it a theme: a screen's root, a
// popup's content.
type Scope struct {
	kit.Element
	Theme Theme
}

// NewScope makes a scope for t.
func NewScope(t Theme, children ...kit.Widget) *Scope {
	s := &Scope{Theme: t}
	s.Add(children...)
	return s
}

// WidgetTheme is ThemeSource's.
func (s *Scope) WidgetTheme() Theme { return s.Theme }

// ThemeOf is the theme e draws with: the nearest ThemeSource's; for a layer above the window's
// content (a dialog, the toasts), whose ancestors are not the content's, the content's when it
// is a ThemeSource (a screen's root); else System.
func ThemeOf(e *kit.Element) Theme {
	for p := e; p != nil; p = p.Parent() {
		if s, ok := p.Widget().(ThemeSource); ok {
			if t := s.WidgetTheme(); t != nil {
				return t
			}
		}
	}
	if w := e.Window(); w != nil {
		if s, ok := w.Content().(ThemeSource); ok {
			if t := s.WidgetTheme(); t != nil {
				return t
			}
		}
	}
	return systemTheme
}

// tokensOf is e's theme's tokens in its window's environment.
func tokensOf(e *kit.Element) *tokens.Theme {
	var env kit.Environment
	if w := e.Window(); w != nil {
		env = w.Environment()
	}
	return ThemeOf(e).Tokens(env)
}

// The fonts of app.css and the components, with Chromium's font-optical-sizing: auto and
// body's tabular figures (font-variant-numeric is inherited; the font shorthand resets it).
var (
	// FontBody is body's 400 var(--type-body) / 1.4 var(--font-ui).
	FontBody = font(tokens.TypeBody, 400)
	// FontButton is .btn's 500 var(--type-body), line-height 1.
	FontButton = kit.Font{Family: tokens.FontUI, Size: tokens.TypeBody, Weight: 500, LineHeight: 1, Tabular: true, AutoOptical: true}
	// FontMono is .mono and the textarea's var(--mono-weight) 0.75rem var(--font-mono),
	// -0.01em.
	FontMono = kit.Font{Family: tokens.FontMono, Size: kit.Rem(0.75), Weight: tokens.MonoWeight, LineHeight: 1.4, LetterSpacing: -0.01, Tabular: true, AutoOptical: true}
)

// font is the UI font at a size and weight, with body's line height and figures.
func font(size kit.Length, weight float32) kit.Font {
	return kit.Font{Family: tokens.FontUI, Size: size, Weight: weight, LineHeight: 1.4, Tabular: true, AutoOptical: true}
}

// caps is app.css's .caps: var(--type-caps), 600, +0.07em, upper case (the text is given in
// upper case: text-transform is the caller's).
var fontCaps = kit.Font{Family: tokens.FontUI, Size: tokens.TypeCaps, Weight: 600, LetterSpacing: 0.07, LineHeight: 1.4, Tabular: true, AutoOptical: true}

// label makes a one-line label that ends in an ellipsis when it does not fit, hidden from
// the semantic tree (its widget names itself from it).
func label(text string, f kit.Font) *kit.Label {
	l := kit.NewLabel(text, f, kit.TextOptions{Truncate: kit.TruncateEnd})
	l.Semantics.Role = kit.RoleNone
	l.Style.MinWidth = kit.Px(0)
	return l
}
