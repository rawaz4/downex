package views

import (
	"sync"

	"downex/ui/kit"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/state"
)

// Theme is the widgets.Theme the screens draw with: the Theme store's choice (System, Light
// or Dark), or Fixed when set; SystemDark, when set, stands in for the system's app mode (a
// reference made in one mode, whatever this PC's mode is). Forced colours win while a contrast
// theme is on, as app.css's forced-colors rules do.
type Theme struct {
	State      *state.Theme
	Fixed      *tokens.Choice
	SystemDark *bool
}

var _ widgets.Theme = (*Theme)(nil)

// Choice is the choice drawn.
func (t *Theme) Choice() tokens.Choice {
	if t.Fixed != nil {
		return *t.Fixed
	}
	if t.State == nil {
		return tokens.System
	}
	c, _ := tokens.ParseChoice(string(t.State.Choice())) // host.ThemeChoice and tokens.Choice share their names
	return c
}

// Tokens is widgets.Theme's: the theme to draw with in env.
func (t *Theme) Tokens(env kit.Environment) *tokens.Theme {
	if t.SystemDark != nil {
		env.Dark = *t.SystemDark
	}
	return tokens.For(t.Choice(), env)
}

// Palette is the kit's colours in env (kit.WindowOptions.Palette).
func (t *Theme) Palette(env kit.Environment) kit.Palette { return t.Tokens(env).Palette() }

// MainFrame is the main window's frame colours in env (kit.WindowOptions.FrameColors);
// CaptionFrame a progress window's (a system caption on --surface).
func (t *Theme) MainFrame(env kit.Environment) kit.FrameColors { return t.Tokens(env).MainFrame() }
func (t *Theme) CaptionFrame(env kit.Environment) kit.FrameColors {
	return t.Tokens(env).CaptionFrame()
}

// Ink is a colour read from the theme when a widget paints, so that a theme change needs a
// repaint only.
type Ink func(t *tokens.Theme) kit.Color

// The inks app.css's text uses.
var (
	InkInk    Ink = func(t *tokens.Theme) kit.Color { return t.Ink }
	InkInk2   Ink = func(t *tokens.Theme) kit.Color { return t.Ink2 }
	InkMuted  Ink = func(t *tokens.Theme) kit.Color { return t.Muted }
	InkFaint  Ink = func(t *tokens.Theme) kit.Color { return t.Faint }
	InkDanger Ink = func(t *tokens.Theme) kit.Color { return t.Danger }
	InkAccent Ink = func(t *tokens.Theme) kit.Color { return t.AccentText }
	InkWarn   Ink = func(t *tokens.Theme) kit.Color { return t.Warn }
	InkInfo   Ink = func(t *tokens.Theme) kit.Color { return t.Info }
)

// TokensOf is the theme e draws with in its window's environment (the nearest
// widgets.ThemeSource's, else System).
func TokensOf(e *kit.Element) *tokens.Theme {
	var env kit.Environment
	if w := e.Window(); w != nil {
		env = w.Environment()
	}
	return widgets.ThemeOf(e).Tokens(env)
}

// DetailsState is whether the details pane is collapsed (App.svelte's detailsCollapsed, kept
// in the prefs under state.DetailsCollapsedKey). Changes tells of every change; any goroutine
// may read it.
type DetailsState struct {
	Changes state.Signal

	storage   state.Storage
	mu        sync.Mutex
	collapsed bool
}

func newDetailsState(s state.Storage) *DetailsState {
	d := &DetailsState{storage: s}
	if s != nil {
		d.collapsed = state.DetailsCollapsed(s)
	}
	return d
}

// Collapsed reports that the pane shows only its header.
func (d *DetailsState) Collapsed() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.collapsed
}

// Toggle collapses or expands the pane and keeps the choice (toggleDetails).
func (d *DetailsState) Toggle() { d.Set(!d.Collapsed()) }

// Set sets the choice and keeps it.
func (d *DetailsState) Set(collapsed bool) {
	d.mu.Lock()
	if d.collapsed == collapsed {
		d.mu.Unlock()
		return
	}
	d.collapsed = collapsed
	d.mu.Unlock()
	if d.storage != nil {
		state.SaveDetailsCollapsed(d.storage, collapsed) // storage unavailable: the choice lasts for this session
	}
	d.Changes.Notify()
}
