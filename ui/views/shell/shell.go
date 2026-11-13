package shell

import (
	"errors"

	"downex/ui/kit"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/views"
)

// Options are what New builds the main window's chrome from.
type Options struct {
	// App is the window's App (required).
	App *views.App
	// Slots are the download grid (required), the details pane and the status bar the
	// downloads lane makes; a nil StatusBar is the shell's own (NewStatusBar with Status), a
	// nil Details no details pane.
	Slots views.Slots
	// Frameless is a window whose title bar the shell draws with the window buttons
	// (Capabilities.CustomTitleBar); false keeps the system frame.
	Frameless bool

	TitleBar TitleBarOptions
	Sidebar  SidebarOptions
	Header   HeaderOptions
	Status   StatusOptions
	// OnRetry is the connection banner's Retry now.
	OnRetry func()
}

// TitleBarOptions are the title bar's actions.
type TitleBarOptions struct {
	OnSearch   func(string)
	OnCommands func()
	OnTheme    func()
	OnSettings func()
}

// Shell is the main window's chrome (App.svelte's layout, DESIGN-SYSTEM.md §2.3, §4): the
// post-queue countdown at the very top, the 44 px title bar, then the grid of the sidebar
// (14rem; a 56 px rail from 721 to 999 px) beside the work card (the connection banner's slot,
// the page header, the download grid, the details pane while something is selected) and the
// 32 px status bar, all on the chrome. Wrap it in widgets.NewScope(app.WidgetTheme, shell)
// (Root does). It is presentational: the composition (views/app) feeds its parts.
type Shell struct {
	kit.Element
	app  *views.App
	opts Options
	root *widgets.Scope

	bannerSpace *kit.Element
	titleSpace  *kit.Element
	titleLayer  *titleLayer
	titleBar    *TitleBar
	appArea     *kit.Element
	body        *bodyGrid
	sidebar     *Sidebar
	card        *card
	bannerSlot  *kit.Element
	connBanner  *ConnectionBanner
	header      *Header
	gridArea    *gridArea
	grid        views.DownloadGrid
	details     views.DetailsPane
	detailsOn   bool
	statusBar   views.StatusBar
	ownStatus   *StatusBar

	postLayer  *postLayer
	postBanner *PostActionBanner
	// postPrev had the focus when the banner appeared (PostActionBanner's previous).
	postPrev   *kit.Element
	tools      *ToolsToast
	toolsShown bool
}

// ErrNoSlots is New's answer without an App or a grid slot.
var ErrNoSlots = errors.New("views/shell: the chrome needs an App and a download grid")

// New makes the chrome around the slots.
func New(o Options) (*Shell, error) {
	if o.App == nil || o.Slots.Grid == nil {
		return nil, ErrNoSlots
	}
	s := &Shell{app: o.App, opts: o}
	s.Style = kit.Style{Display: kit.DisplayFlex, Axis: kit.AxisColumn, Width: kit.Pct(100), Height: kit.Pct(100)}
	s.Semantics.AutomationID = "window"

	s.bannerSpace = &kit.Element{Style: kit.Style{Flex: kit.FlexNone, Height: kit.Px(0), Hidden: true}}
	s.titleBar = NewTitleBar(o.App.WidgetTheme, o.Frameless)
	s.titleBar.OnSearch = o.TitleBar.OnSearch
	s.titleBar.OnCommands = o.TitleBar.OnCommands
	s.titleBar.OnTheme = o.TitleBar.OnTheme
	s.titleBar.OnSettings = o.TitleBar.OnSettings
	s.titleBar.Style.Flex = kit.FlexNone

	s.sidebar = NewSidebar(o.Sidebar)
	s.connBanner = NewConnectionBanner(o.App.Clock.Now, o.OnRetry)
	s.bannerSlot = &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, Axis: kit.AxisColumn, MinWidth: kit.Px(0), Flex: kit.FlexNone, Container: true}}
	s.bannerSlot.Add(s.connBanner)
	s.header = NewHeader(o.Header)
	s.grid = o.Slots.Grid
	s.gridArea = &gridArea{grid: s.grid}
	s.gridArea.Style = kit.Style{Flex: kit.Flex1, MinHeight: kit.Px(0), MinWidth: kit.Px(0)}
	s.gridArea.Add(s.grid)
	s.details = o.Slots.Details
	s.card = &card{}
	s.card.Semantics = kit.Semantics{Role: kit.RoleMain}
	s.card.Style = kit.Style{
		Display: kit.DisplayFlex, Axis: kit.AxisColumn, MinHeight: kit.Px(0), MinWidth: kit.Px(0),
		Border: kit.Edges{Top: kit.Px(1), Bottom: kit.Px(1), Start: kit.Px(1)},
		Radius: kit.Corners{TopStart: tokens.RXl, BottomStart: tokens.RXl}, Overflow: kit.OverflowHidden,
	}
	s.card.Add(s.bannerSlot, s.header, s.gridArea)
	s.body = &bodyGrid{sidebar: s.sidebar, card: s.card}
	s.body.Style = kit.Style{Flex: kit.Flex1, MinHeight: kit.Px(0)}
	s.body.Add(s.sidebar, s.card)

	s.statusBar = o.Slots.StatusBar
	if s.statusBar == nil {
		s.ownStatus = NewStatusBar(o.Status)
		s.statusBar = s.ownStatus
	}
	s.statusBar.Base().Style.Flex = kit.FlexNone
	s.appArea = &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, Axis: kit.AxisColumn, Flex: kit.Flex1, MinHeight: kit.Px(0)}}
	s.appArea.Add(s.body, s.statusBar)
	if o.Frameless {
		// Above a modal dialog's scrim (z-index 45 over 40): the caption's drag region, double
		// click and window buttons keep working, as a system caption would. The bar is a layer
		// over the room it takes in the window's column.
		s.titleSpace = &kit.Element{Style: kit.Style{Flex: kit.FlexNone, Height: tokens.TitlebarH}}
		s.titleLayer = &titleLayer{shell: s}
		s.titleLayer.PassThrough = true
		s.titleLayer.Add(s.titleBar)
		s.Add(s.bannerSpace, s.titleSpace, s.appArea)
		o.App.Layers.Set(s.titleLayer, views.ZTitleBar)
	} else {
		s.Add(s.bannerSpace, s.titleBar, s.appArea)
	}

	s.root = widgets.NewScope(o.App.WidgetTheme, s)
	s.root.Style = kit.Style{Display: kit.DisplayBlock, Width: kit.Pct(100), Height: kit.Pct(100)}
	s.tools = NewToolsToast()
	s.postLayer = &postLayer{shell: s}
	s.postLayer.PassThrough = true

	o.App.FallbackFocus = s.grid.FocusTarget
	main := []*kit.Element{s.sidebar.Base(), s.card.Base(), s.statusBar.Base()}
	main = append(main, s.titleBar.Inert()...)
	o.App.Dialogs.SetMain(main...)
	return s, nil
}

// Root is the window's content: the shell in a widgets.Scope of the App's theme.
func (s *Shell) Root() *widgets.Scope { return s.root }

// Paint is kit.Painter's: the chrome under everything.
func (s *Shell) Paint(p *kit.PaintContext) {
	p.Canvas.FillRect(kit.RectAt(p.Origin, p.Size()), views.TokensOf(&s.Element).Chrome)
}

// The shell's parts.
func (s *Shell) TitleBar() *TitleBar                 { return s.titleBar }
func (s *Shell) Sidebar() *Sidebar                   { return s.sidebar }
func (s *Shell) Header() *Header                     { return s.header }
func (s *Shell) ConnectionBanner() *ConnectionBanner { return s.connBanner }
func (s *Shell) Card() *kit.Element                  { return s.card.Base() }
func (s *Shell) Body() *kit.Element                  { return s.body.Base() }
func (s *Shell) Grid() views.DownloadGrid            { return s.grid }
func (s *Shell) GridArea() *kit.Element              { return s.gridArea.Base() }
func (s *Shell) StatusBarSlot() views.StatusBar      { return s.statusBar }
func (s *Shell) ToolsToast() *ToolsToast             { return s.tools }
func (s *Shell) PostActionBanner() *PostActionBanner { return s.postBanner }
func (s *Shell) BannerSlot() *kit.Element            { return s.bannerSlot }
func (s *Shell) DetailsShown() bool                  { return s.detailsOn }

// StatusBar is the shell's own status bar (nil when the downloads lane gave one).
func (s *Shell) StatusBar() *StatusBar { return s.ownStatus }

// SetDetails puts the details pane under the grid while downloads are selected, and takes it
// away when none is (App.svelte's {#if store.selected.length > 0}).
func (s *Shell) SetDetails(on bool) {
	if s.details == nil || on == s.detailsOn {
		return
	}
	s.detailsOn = on
	if on {
		s.details.Base().Style.Flex = kit.FlexNone
		s.card.Add(s.details)
	} else {
		s.card.Remove(s.details)
	}
}

// SetModal gives the title bar its modal look (a dialog or the palette is open).
func (s *Shell) SetModal(m bool) { s.titleBar.SetModal(m) }

// ShowTools shows or hides the tools toast (the first download of yt-dlp and Deno), stacked by
// the App's layers: z 60, under a dialog's scrim while one is open (AddBehind).
func (s *Shell) ShowTools(show bool) {
	if show == s.toolsShown {
		return
	}
	s.toolsShown = show
	a := s.app
	if show {
		a.Layers.Set(s.tools, a.Dialogs.ToastsZ())
		a.Dialogs.AddBehind(s.tools)
	} else {
		a.Layers.Remove(s.tools)
	}
}

// ToolsShown reports the tools toast.
func (s *Shell) ToolsShown() bool { return s.toolsShown }

// ShowPostAction shows a countdown's banner at the very top of the window, above an open
// dialog's scrim (views.ZBanner), and the room it takes above the title bar; a banner of
// another countdown (another key) replaces it. focus gives Cancel the focus. nil takes it away.
func (s *Shell) ShowPostAction(b *PostActionBanner, focus bool) {
	if s.postBanner != nil && b != nil && s.postBanner.Key() == b.Key() {
		return
	}
	a := s.app
	if s.postBanner != nil {
		old := s.postBanner
		s.postLayer.RemoveAll()
		s.postBanner = nil
		a.Layers.Remove(s.postLayer)
		hadFocus := false
		if w := s.Window(); w != nil {
			f := w.Focused()
			hadFocus = f == nil || old.Contains(f)
		}
		s.bannerSpace.SetHidden(true)
		s.relayoutTitle()
		if hadFocus && b == nil {
			s.restoreAfterBanner()
		}
	}
	if b == nil {
		return
	}
	s.postBanner = b
	s.postPrev = nil
	if w := s.Window(); w != nil {
		s.postPrev = w.Focused()
	}
	s.postLayer.Add(b)
	a.Layers.Set(s.postLayer, views.ZBanner)
	s.bannerSpace.Style.Height = kit.Px(40)
	s.bannerSpace.SetHidden(false)
	s.relayoutTitle()
	if focus {
		if w := s.Window(); w != nil {
			w.Defer(b.Focus)
		}
	}
}

// relayoutTitle places a frameless title bar again after the room above it changed.
func (s *Shell) relayoutTitle() {
	if s.titleLayer != nil {
		s.titleLayer.RelayoutContent()
	}
}

// restoreAfterBanner: a banner leaving with the focus gives it back where it came from (when
// that is still there and, with a dialog open, inside it), else to the open dialog itself (Tab
// then enters it), else to the fallback (PostActionBanner's onMount cleanup).
func (s *Shell) restoreAfterBanner() {
	w := s.Window()
	if w == nil {
		return
	}
	prev := s.postPrev
	s.postPrev = nil
	w.Defer(func() {
		if f := w.Focused(); f != nil {
			return
		}
		// a lane's dialog embeds views.Dialog: the dialog's own element is the modal
		modal, _ := s.app.Dialogs.Dialog().(kit.Widget)
		if prev != nil && prev.Window() == w && (modal == nil || modal.Base().Contains(prev)) && w.Focus(prev.Widget()) {
			return
		}
		if modal != nil && w.Focus(modal) {
			return
		}
		if f := s.app.Fallback(); f != nil {
			w.Focus(f)
		}
	})
}

// ---- the parts ----------------------------------------------------------------------------

// bodyGrid is .body: the sidebar's column (--sidebar-w: 14rem, 56 px from 721 to 999 px)
// beside the work card.
type bodyGrid struct {
	kit.Element
	sidebar *Sidebar
	card    *card
}

// SidebarWidth is --sidebar-w in a window vw DIPs wide.
func SidebarWidth(s kit.Scale, vw kit.DIP) kit.DIP {
	if vw >= 721 && vw <= 999 {
		return 56
	}
	return s.Rem(14)
}

// ContentWidths is kit.Layouter's.
func (b *bodyGrid) ContentWidths(*kit.LayoutContext) (lo, hi kit.DIP) { return 0, 0 }

// ContentHeight is kit.Layouter's.
func (b *bodyGrid) ContentHeight(*kit.LayoutContext, kit.DIP) kit.DIP { return 0 }

// LayoutContent is kit.Layouter's.
func (b *bodyGrid) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	var vw kit.DIP
	if w := b.Window(); w != nil {
		vw = w.Size().W
	}
	sw := SidebarWidth(ctx.Scale, vw)
	h := box.Height()
	ctx.Place(b.sidebar, kit.RectAt(box.Min, kit.Size{W: sw, H: h}), box.Width())
	ctx.Place(b.card, kit.R(box.Min.X+sw, box.Min.Y, box.Max.X, box.Max.Y), box.Width())
}

// card is main.card: --surface with a 1 px --line border (none at the inline end), rounded
// --r-xl on its inline-start corners, --shadow-sm; it clips what it holds.
type card struct{ kit.Element }

// Paint is kit.Painter's.
func (c *card) Paint(p *kit.PaintContext) {
	t := views.TokensOf(&c.Element)
	s := p.Scale
	box := s.SnapRect(kit.RectAt(p.Origin, p.Size()))
	r := kit.RRect{Rect: box, Radii: kit.Radii{TopLeft: 12, BottomLeft: 12}}
	for i := len(t.ShadowSm) - 1; i >= 0; i-- {
		p.Canvas.DrawShadow(r, t.ShadowSm[i])
	}
	line := t.Line
	if t.IsForced() {
		line = t.Ink // border-color: CanvasText
	}
	// The border is the box filled in --line, the surface inset by it (no border at the end).
	p.Canvas.FillRoundedRect(r, line)
	w := s.SnapWidth(1)
	inner := kit.RRect{Rect: kit.R(box.Min.X+w, box.Min.Y+w, box.Max.X, box.Max.Y-w), Radii: kit.Radii{TopLeft: 12 - w, BottomLeft: 12 - w}}
	p.Canvas.FillRoundedRect(inner, t.Surface)
}

// gridArea is the card's third row: the grid (which draws the empty list itself).
type gridArea struct {
	kit.Element
	grid views.DownloadGrid
}

// ContentWidths is kit.Layouter's.
func (g *gridArea) ContentWidths(*kit.LayoutContext) (lo, hi kit.DIP) { return 0, 0 }

// ContentHeight is kit.Layouter's.
func (g *gridArea) ContentHeight(*kit.LayoutContext, kit.DIP) kit.DIP { return 0 }

// LayoutContent is kit.Layouter's: the grid fills the row.
func (g *gridArea) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	ctx.Place(g.grid, box, box.Width())
}

// titleLayer holds a frameless window's title bar above a dialog's scrim, below the post-queue
// countdown when one shows.
type titleLayer struct {
	kit.Element
	shell *Shell
}

// ContentWidths is kit.Layouter's.
func (l *titleLayer) ContentWidths(*kit.LayoutContext) (lo, hi kit.DIP) { return 0, 0 }

// ContentHeight is kit.Layouter's.
func (l *titleLayer) ContentHeight(*kit.LayoutContext, kit.DIP) kit.DIP { return 0 }

// LayoutContent is kit.Layouter's: the bar where its room is in the column.
func (l *titleLayer) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	top := l.shell.titleSpace.WindowBounds().Min.Y
	if l.shell.bannerSpace.Style.Hidden {
		top = 0
	}
	h, _ := tokens.TitlebarH.Resolve(ctx.Scale, 0)
	ctx.Place(l.shell.titleBar, kit.R(box.Min.X, box.Min.Y+top, box.Max.X, box.Min.Y+top+h), box.Width())
}

// postLayer holds the post-queue countdown at the top of the window, above a dialog's scrim.
type postLayer struct {
	kit.Element
	shell *Shell
}

// ContentWidths is kit.Layouter's.
func (l *postLayer) ContentWidths(*kit.LayoutContext) (lo, hi kit.DIP) { return 0, 0 }

// ContentHeight is kit.Layouter's.
func (l *postLayer) ContentHeight(*kit.LayoutContext, kit.DIP) kit.DIP { return 0 }

// LayoutContent is kit.Layouter's: the banner across the top at its height; the room above the
// title bar follows it.
func (l *postLayer) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	b := l.shell.postBanner
	if b == nil {
		return
	}
	w := box.Width()
	h := ctx.HeightFor(b, w, w)
	ctx.Place(b, kit.RectAt(box.Min, kit.Size{W: w, H: h}), w)
	sp := l.shell.bannerSpace
	if cur, _ := sp.Style.Height.Resolve(ctx.Scale, 0); cur != h {
		l.Window().AfterLayout(func() {
			sp.Style.Height = kit.Px(h)
			sp.Relayout()
			l.shell.relayoutTitle()
		})
	}
}

// MenuOpen reports that a menu of the page header or the status bar is open (its keys are its
// own: App.svelte's onWindowKeydown leaves them alone).
func (s *Shell) MenuOpen() bool {
	h := s.header
	if h.more.IsOpen() || h.add.More.IsOpen() {
		return true
	}
	if sb := s.ownStatus; sb != nil {
		if (sb.limitMenu != nil && sb.limitMenu.IsOpen()) || (sb.whenMenu != nil && sb.whenMenu.IsOpen()) {
			return true
		}
	}
	return false
}

// ThemeChanged shows the chrome in the theme's colours at once: the title bar (its theme
// button too), the sidebar's items and the status bar's menus. Call it after the theme choice
// or the environment (light or dark apps, a contrast theme) changed.
func (s *Shell) ThemeChanged() {
	s.titleBar.ThemeChanged()
	s.sidebar.ThemeChanged()
	if s.ownStatus != nil {
		s.ownStatus.ThemeChanged()
	}
	s.Invalidate()
}
