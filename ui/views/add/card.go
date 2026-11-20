package add

import (
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	imedia "downex/internal/media"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/media"
	"downex/ui/core/text"
	"downex/ui/kit"
	"downex/ui/kit/icons"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/views"
)

// VideoCard.svelte's module functions.

// ShortClassName is the short class of a quality: "4K (2160p)" → "4K", "1080p (Full HD) 60fps"
// → "1080p".
func ShortClassName(name string) string {
	if cut := strings.Index(name, " ("); cut >= 0 {
		name = name[:cut]
	}
	return text.TrimJS(name)
}

// bestClassText is "Up to 4K": the best class a stream or page offers ("" without a named
// video quality).
func bestClassText(info *downexv1.MediaInfo) string {
	list := media.VideoTracks(info.GetTracks(), media.IsSite(info), info.GetSelectedVideo())
	if len(list) == 0 {
		return ""
	}
	if cls := ShortClassName(media.QualityName(list[0])); cls != "" {
		return "Up to " + cls
	}
	return ""
}

// displayHost is the host of an address for display, without "www." ("" when it does not
// parse).
func displayHost(url string) string {
	u, err := text.ParseURL(url)
	if err != nil {
		return ""
	}
	h := u.Hostname()
	if len(h) >= 4 && strings.EqualFold(h[:4], "www.") {
		h = h[4:]
	}
	return h
}

// siteLine is "YouTube · youtube.com"; a manifest's "HLS stream · media.example.org". pageURL
// and manifestURL stand for info's (siteName passes "" for both).
func siteLine(info *downexv1.MediaInfo, pageURL, manifestURL, url string) string {
	addr := pageURL
	if addr == "" {
		addr = manifestURL
	}
	if addr == "" {
		addr = url
	}
	host := displayHost(addr)
	var name string
	switch {
	case media.IsSite(info):
		name = text.TrimJS(info.GetSite())
		if name == "" {
			name = "Video page"
		}
	case media.FormatLabel(info.GetFormat()) != "":
		name = media.FormatLabel(info.GetFormat()) + " stream"
	default:
		name = "Stream"
	}
	if host == "" {
		return name
	}
	return name + " · " + host
}

// CardOptions are VideoCard's props beside its info.
type CardOptions struct {
	// FallbackTitle shows when the stream has no title (a proposed file name).
	FallbackTitle string
	// URL is the address the card is about, when the info names none (a download's url).
	URL string
	// NoCookies shows the "No cookies sent" chip (only where no cookie is sent).
	NoCookies bool
	// ShowAddress shows URL in the card after the site's name as a read-only field, where the
	// dialog has no address field (Confirm download); AddressLabel names it ("Page",
	// "Address").
	ShowAddress  bool
	AddressLabel string
}

// VideoCard is VideoCard.svelte (DESIGN-SYSTEM.md §3.19): a 96×54 poster placeholder (never a
// page image), the title on up to two lines, then one line with the site (and the address
// where the dialog has none) and the fact chips, which wrap under it when the card is narrow.
// Its tooltip is the chosen quality's picture (SetPicture).
type VideoCard struct {
	kit.Element
	Title   *views.Text
	Address *addressText
	chips   *kit.Element
	// length is the length chip (nil while the duration is unknown), part what it shows.
	length     *fact
	durationMs int64
	part       imedia.Clip
}

// NewVideoCard makes the card of info.
func NewVideoCard(info *downexv1.MediaInfo, o CardOptions) *VideoCard {
	c := &VideoCard{}
	c.Style = kit.Style{
		Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: tokens.Sp6, MinWidth: kit.Px(0),
		Padding: kit.EdgesAll(tokens.Sp5), Border: kit.EdgesAll(kit.Px(1)), Radius: kit.CornersAll(tokens.RXl),
	}
	title := text.TrimJS(info.GetTitle())
	if title == "" {
		title = text.TrimJS(o.FallbackTitle)
	}
	if title == "" {
		title = "Untitled video"
	}
	c.durationMs = info.GetDurationMs()
	duration, _ := media.LengthText(imedia.Clip{}, c.durationMs)
	best := bestClassText(info)
	container := media.ContainerLabel(info.GetContainer())
	if container == "" && media.IsSite(info) {
		container = "MP4"
	}

	c.Add(newPoster())
	about := &kit.Element{Style: kit.Style{Display: kit.DisplayBlock, RowGap: kit.Px(6), Flex: kit.FlexAuto, MinWidth: kit.Px(0)}}
	shown := text.EscapeBidi(title)
	c.Title = views.NewText(shown, views.SectionFont(1.3), views.InkInk, kit.TextOptions{Wrap: kit.TextWrapAnywhere, MaxLines: 2, Truncate: kit.TruncateEnd})
	c.Title.Style.MinWidth = kit.Px(0)
	c.Title.Tooltip = shown
	c.Title.Semantics.Role = kit.RoleText
	about.Add(c.Title)

	meta := &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, Wrap: true, AlignItems: kit.AlignCenter, RowGap: kit.Px(6), ColumnGap: kit.Px(12), MinWidth: kit.Px(0)}}
	site := &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, Flex: kit.FlexOf(1, 1, kit.Rem(12)), AlignItems: kit.AlignCenter, ColumnGap: kit.Px(6), MinWidth: kit.Px(0)}}
	site.Add(icon("globe", 14, faint))
	if o.ShowAddress && o.URL != "" {
		name := views.NewLine(text.EscapeBidi(siteLine(info, "", "", "")), views.FontCell, views.InkMuted)
		name.Style.Flex = kit.FlexNone
		name.Style.MaxWidth = kit.Pct(50)
		sep := views.NewText("·", views.FontCell, views.InkFaint, kit.TextOptions{})
		sep.Style.Flex = kit.FlexNone
		sep.Semantics.Hidden = true
		label := o.AddressLabel
		if label == "" {
			label = "Address"
		}
		c.Address = newAddressText(text.EscapeBidi(o.URL), label)
		site.Add(name, sep, c.Address)
	} else {
		line := views.NewLine(text.EscapeBidi(siteLine(info, info.GetPageUrl(), info.GetManifestUrl(), o.URL)), views.FontCell, views.InkMuted)
		site.Add(line)
	}
	meta.Add(site)

	c.chips = &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, Flex: kit.FlexOf(0, 1, kit.Length{}), Wrap: true, RowGap: kit.Px(6), ColumnGap: kit.Px(6), MinWidth: kit.Px(0)}}
	c.chips.Semantics.Role = kit.RoleList
	c.chips.Semantics.Name = "About this video"
	// The length chip is there even when the whole length is unknown (Instagram): a part's own
	// length still shows in it (SetPart); it is hidden while it has nothing to say.
	c.length = newFact(duration, "clock")
	c.length.Style.Hidden = duration == ""
	c.chips.Add(c.length)
	if best != "" {
		c.chips.Add(newFact(best, ""))
	}
	if container != "" {
		c.chips.Add(newFact("Saved as "+container, ""))
	}
	if o.NoCookies {
		c.chips.Add(newFact("No cookies sent", "shield"))
	}
	meta.Add(c.chips)
	about.Add(meta)
	c.Add(about)
	return c
}

// SetPart shows the part's length in the length chip ("8:28 of 31:54", read "8:28 selected of
// 31:54"; the whole's for the zero Clip), as the browser panel's "8:28 selected".
func (c *VideoCard) SetPart(part imedia.Clip) {
	if c.length == nil || part == c.part {
		return
	}
	c.part = part
	shown, name := media.LengthText(part, c.durationMs)
	c.length.label.SetText(shown)
	c.length.SetHidden(shown == "")
	c.length.Semantics.Name = name
	c.length.Tooltip = ""
	if !part.IsZero() {
		c.length.Tooltip = "Part " + imedia.FormatClipRange(part, time.Duration(c.durationMs)*time.Millisecond) + ": " + name
	}
	c.length.SemanticsChanged()
	c.Relayout()
}

// SetPicture sets the card's tooltip, the chosen quality's picture ("1920×1080").
func (c *VideoCard) SetPicture(s string) { c.Tooltip = s }

// Facts are the texts of the card's fact chips, in order ("10:00", "Up to 1080p", …).
func (c *VideoCard) Facts() []string {
	var out []string
	for _, k := range c.chips.Children() {
		if f, ok := k.Widget().(*fact); ok && !f.Style.Hidden {
			out = append(out, f.label.Text())
		}
	}
	return out
}

// Paint is kit.Painter's: --surface-2 with a --line border, 12 px corners.
func (c *VideoCard) Paint(p *kit.PaintContext) {
	t := views.TokensOf(&c.Element)
	r := views.SnapBox(p, 12)
	border := t.Line
	if t.IsForced() {
		border = t.Ink
	}
	p.Canvas.FillRoundedRect(r, border)
	w := p.Scale.SnapWidth(1)
	p.Canvas.FillRoundedRect(kit.RoundRect(r.Rect.Inset(kit.UniformInsets(w)), 12-w), t.Surface2)
}

// poster is the card's .poster: a pale --c-video tint with a 1 px inner ring and the 28 px
// play disc.
type poster struct {
	kit.Element
	play kit.Path
	res  kit.Resources
}

const playPath = "M9.5 6.6v14.8a1.1 1.1 0 0 0 1.68.93l11.7-7.4a1.1 1.1 0 0 0 0-1.86L11.18 5.67A1.1 1.1 0 0 0 9.5 6.6z"

func newPoster() *poster {
	p := &poster{}
	p.Semantics.Hidden = true
	p.Style = kit.Style{Width: kit.Px(96), Height: kit.Px(54), Flex: kit.FlexNone, AlignSelf: kit.AlignStart}
	return p
}

// Detached is kit.Detacher's.
func (p *poster) Detached() {
	if p.play != nil {
		p.play.Release()
		p.play, p.res = nil, nil
	}
}

// Paint is kit.Painter's.
func (p *poster) Paint(pc *kit.PaintContext) {
	t := views.TokensOf(&p.Element)
	r := views.SnapBox(pc, 8)
	pc.Canvas.FillRoundedRect(r, kit.Mix(t.CVideo, 0.12, t.Surface))
	w := pc.Scale.SnapWidth(1)
	ring := kit.Mix(t.CVideo, 0.22, kit.Color{})
	if t.IsForced() {
		ring = t.Ink
	}
	pc.Canvas.StrokeRoundedRect(r.Inset(w/2), kit.Stroke{Width: w}, ring)
	c := r.Rect.Center()
	disc := kit.R(c.X-14, c.Y-14, c.X+14, c.Y+14)
	pc.Canvas.FillEllipse(disc, t.CVideo)
	win := p.Window()
	if win == nil || win.Host() == nil {
		return
	}
	if res := win.Host().Resources(); res != p.res || p.play == nil {
		p.Detached()
		path, err := res.NewSVGPath(playPath, kit.FillNonZero)
		if err != nil {
			return
		}
		p.play, p.res = path, res
	}
	// the 14 px glyph (viewBox 28) centred in the disc, 2 px towards the end (margin-inline-start)
	at := kit.Pt(c.X-7+1, c.Y-7)
	pc.Canvas.PushTransform(kit.Scaling(0.5, 0.5).Then(kit.Translation(at.X, at.Y)))
	pc.Canvas.FillPath(p.play, t.TileGlyph)
	pc.Canvas.Pop()
}

// fact is a .chips .chip: a 22 px pill of 12 px --ink-2 text on --raised with a --line
// border, its optional 12 px icon in --muted.
type fact struct {
	kit.Element
	label *kit.Label
}

func newFact(s, iconName string) *fact {
	f := &fact{}
	f.Semantics.Role = kit.RoleListItem
	f.Semantics.Name = s
	f.Style = kit.Style{
		Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, Flex: kit.FlexNone, ColumnGap: kit.Px(6),
		Height: kit.Px(22), Padding: kit.EdgesInline(kit.Px(8)), Border: kit.EdgesAll(kit.Px(1)),
	}
	if iconName != "" {
		f.Add(icon(iconName, 12, func(t *tokens.Theme) kit.Color { return t.Muted }))
	}
	fnt := views.FontHint
	fnt.LineHeight = 1
	f.label = kit.NewLabel(s, fnt, kit.TextOptions{})
	f.label.Semantics.Role = kit.RoleNone
	f.Add(f.label)
	return f
}

// Paint is kit.Painter's.
func (f *fact) Paint(p *kit.PaintContext) {
	t := views.TokensOf(&f.Element)
	r := views.SnapBox(p, p.Size().H/2)
	border := t.Line
	if t.IsForced() {
		border = t.Ink
	}
	p.Canvas.FillRoundedRect(r, border)
	w := p.Scale.SnapWidth(1)
	p.Canvas.FillRoundedRect(kit.RoundRect(r.Rect.Inset(kit.UniformInsets(w)), r.Rect.Height()/2-w), t.Raised)
	f.label.SetColor(t.Ink2)
}

// addressText is VideoCard's .address: <input class="address mono" readonly dir="ltr">, the
// address as text that can be selected, copied and reached with Tab, borderless on the card's
// ground: var(--mono-weight) 0.75rem/1.4 mono, --ink-2, 1 px 3 px padding, --r-xs corners,
// --hover under the pointer, the focus outline at 0 offset, the whole address as its title.
// The kit draws it rather than a system EDIT, which has no text-overflow: ellipsis, shows the
// end of a long prefilled value and is not drawn offscreen: an ellipsis while it is not
// focused, as Chromium draws a text field's text-overflow; focused, the whole line, scrolled
// to keep the selection's moving end in view. Tab selects it all (a text field's focusing
// steps), a press puts the caret, a drag, a double and a triple press select; Shift with the
// arrows, Home and End extends; Ctrl+A selects all; Ctrl+C and Ctrl+Insert copy; Enter submits
// the dialog's form (implicit submission, which a read-only field keeps). The selection is
// app.css's ::selection (--sel-hover) behind the text.
type addressText struct {
	*views.Text
	anchor, caret int     // the selection: byte offsets of the anchor and the moving end
	scroll        kit.DIP // how far the focused line is scrolled from its start
	dragging      bool
	rects         []kit.Rect // the selection's rectangles, kept for the next paint
	// baseShift moves the line to the baseline Chromium gives a text field's line box: the
	// font's ascent and descent rounded, the leading split with its floored half above
	// (inline_box_state.cc), where DirectWrite keeps the exact metrics.
	baseShift kit.DIP
}

func newAddressText(value, name string) *addressText {
	t := views.NewText(value, views.FontMono, views.InkInk2, kit.TextOptions{Truncate: kit.TruncateEnd, Direction: kit.DirLTR})
	a := &addressText{Text: t}
	a.Tooltip = value
	a.Focus = kit.FocusTab
	a.Cursor = kit.CursorText
	a.Ring = kit.Ring{Offset: kit.Px(0)}
	a.Semantics.Role = kit.RoleTextField
	a.Semantics.Name = name
	a.Style.Flex = kit.FlexAuto
	a.Style.MinWidth = kit.Rem(4)
	a.Style.Padding = kit.EdgesOf(kit.Px(1), kit.Px(3))
	a.Style.Radius = kit.CornersAll(tokens.RXs)
	return a
}

// LayoutContent is kit.Layouter's: the label's, and the baseline Chromium gives the line.
func (a *addressText) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	a.Text.LayoutContent(ctx, box)
	a.baseShift = 0
	if ctx.Text == nil {
		return
	}
	if fm, err := ctx.Text.FontMetrics(a.Font().Style(ctx.Scale)); err == nil {
		asc := kit.DIP(math.Round(float64(fm.Ascent)))
		des := kit.DIP(math.Round(float64(fm.Descent)))
		lead := fm.LineHeight - asc - des
		a.baseShift = asc + kit.DIP(math.Floor(float64(lead/2))) - fm.Baseline
	}
}

// Value is the field's value: the address as shown.
func (a *addressText) Value() string { return a.Text.Text() }

// ReadOnly reports the readonly attribute (always: the address is the download's).
func (a *addressText) ReadOnly() bool { return true }

// Selection is the selected range, start <= end, as byte offsets into Value.
func (a *addressText) Selection() (start, end int) {
	return min(a.anchor, a.caret), max(a.anchor, a.caret)
}

// SelectedText is the selected part of the value.
func (a *addressText) SelectedText() string {
	lo, hi := a.Selection()
	return a.Value()[lo:hi]
}

// Select selects from anchor to caret (byte offsets, moved back to rune starts).
func (a *addressText) Select(anchor, caret int) {
	a.anchor, a.caret = a.clamp(anchor), a.clamp(caret)
	a.keepInView()
	a.Invalidate()
	a.SemanticsChanged()
}

func (a *addressText) clamp(i int) int {
	s := a.Value()
	i = min(max(i, 0), len(s))
	for i > 0 && i < len(s) && !utf8.RuneStart(s[i]) {
		i--
	}
	return i
}

// TooltipText is kit.TooltipSource's: the whole address, cut or not (title={shownUrl}).
func (a *addressText) TooltipText() string { return a.Tooltip }

// FillSemantics is kit.SemanticFiller's: a read-only text field whose value is the address.
func (a *addressText) FillSemantics(n *kit.SemanticNode) {
	n.Value = a.Value()
	n.States |= kit.NodeReadOnly
}

// HoverChanged is kit.HoverHandler's: the --hover ground.
func (a *addressText) HoverChanged(bool) { a.Invalidate() }

// FocusChanged is kit.FocusHandler's. Focused from the keyboard, the field selects its
// whole value; a press has placed the caret already. Focused, the line is drawn whole (no
// ellipsis); unfocused, the selection is not shown and the line starts at its start again.
func (a *addressText) FocusChanged(focused bool) {
	o := a.Options()
	if focused {
		o.Truncate = kit.TruncateNone
		if w := a.Window(); w != nil && w.FocusVisible() && !a.dragging {
			a.anchor, a.caret = 0, len(a.Value())
		}
	} else {
		o.Truncate = kit.TruncateEnd
		a.anchor, a.caret, a.scroll, a.dragging = 0, 0, 0, false
	}
	a.SetOptions(o)
	if w := a.Window(); w != nil && focused {
		w.AfterLayout(a.keepInView)
	}
	a.Invalidate()
	a.SemanticsChanged()
}

// keepInView scrolls the focused line so that the selection's moving end shows.
func (a *addressText) keepInView() {
	l := a.TextLayout()
	if l == nil || !a.Focused() {
		return
	}
	view := a.ContentRect().Width()
	widest := max(l.Metrics().Width-view, 0)
	top, _ := l.Caret(a.caret)
	switch x := top.X; {
	case x-a.scroll > view:
		a.scroll = x - view
	case x-a.scroll < 0:
		a.scroll = x
	}
	a.scroll = min(max(a.scroll, 0), widest)
	a.Invalidate()
}

// at is the caret position nearest a point of the element's space.
func (a *addressText) at(p kit.Point) int {
	l := a.TextLayout()
	if l == nil {
		return a.caret
	}
	o := a.TextOrigin()
	o.X -= a.scroll
	return a.clamp(l.HitTest(kit.Pt(p.X-o.X, p.Y-o.Y)).Pos)
}

// HandlePointer is kit.PointerHandler's: a press puts the caret (Shift extends), a double
// press selects a word, a triple press all of it, and a drag selects.
func (a *addressText) HandlePointer(in *kit.PointerInput) bool {
	w := a.Window()
	switch in.Action {
	case kit.PointerDown:
		if in.Button != kit.ButtonLeft || w == nil {
			return false
		}
		pos := a.at(in.Local)
		switch {
		case in.Clicks >= 3:
			a.Select(0, len(a.Value()))
		case in.Clicks == 2:
			lo, hi := wordAt(a.Value(), pos)
			a.Select(lo, hi)
		case in.Mods&kit.ModShift != 0 && a.Focused():
			a.Select(a.anchor, pos)
		default:
			a.Select(pos, pos)
		}
		a.dragging = in.Clicks <= 1
		w.Capture(a.Base())
		return true
	case kit.PointerMove:
		if a.dragging && in.Buttons&kit.ButtonLeft != 0 {
			a.Select(a.anchor, a.at(in.Local))
			return true
		}
	case kit.PointerUp, kit.PointerCancel:
		if a.dragging {
			a.dragging = false
			return true
		}
	}
	return false
}

// HandleKey is kit.KeyHandler's: the keys of a read-only text field.
func (a *addressText) HandleKey(e kit.KeyEvent) bool {
	// [mac webkit]
	if w := a.Window(); w != nil && w.Environment().WebKit && e.Action == kit.KeyDown {
		if used, ok := a.macKey(e); ok {
			return used
		}
	}
	// [/mac webkit]
	if e.Action != kit.KeyDown || e.Mods&^(kit.ModShift|kit.ModCtrl) != 0 {
		return false
	}
	s := a.Value()
	shift := e.Mods&kit.ModShift != 0
	ctrl := e.Mods&kit.ModCtrl != 0
	lo, hi := a.Selection()
	move := func(to int) {
		if shift {
			a.Select(a.anchor, to)
		} else {
			a.Select(to, to)
		}
	}
	switch e.Key {
	case kit.KeyA:
		if ctrl && !shift {
			a.Select(0, len(s))
			return true
		}
	case kit.KeyC, kit.KeyInsert:
		if ctrl && !shift {
			if w := a.Window(); lo < hi && w != nil && w.Host() != nil {
				_ = w.Host().SetClipboardText(s[lo:hi])
			}
			return true
		}
	case kit.KeyArrowLeft:
		switch {
		case ctrl:
			move(prevWord(s, a.caret))
		case !shift && lo < hi:
			a.Select(lo, lo)
		default:
			move(prevRune(s, a.caret))
		}
		return true
	case kit.KeyArrowRight:
		switch {
		case ctrl:
			move(nextWord(s, a.caret))
		case !shift && lo < hi:
			a.Select(hi, hi)
		default:
			move(nextRune(s, a.caret))
		}
		return true
	case kit.KeyHome:
		move(0)
		return true
	case kit.KeyEnd:
		move(len(s))
		return true
	case kit.KeyEnter:
		if e.Mods == 0 {
			return a.submit()
		}
	}
	return false
}

// [mac webkit]
// EditCommand runs the Edit menu's "copy" or "selectAll" chosen with the mouse, as WebKit's
// editing commands act on a focused read-only <input>: the ⌘C and ⌘A it takes them by.
// The macOS host sends the menu's commands only to a focused element that has this
// method, so a list never hears them (WebKit fires no keydown for a menu command).
func (a *addressText) EditCommand(cmd string) bool {
	k := map[string]kit.Key{"copy": kit.KeyC, "selectAll": kit.KeyA}[cmd]
	if k == 0 {
		return false
	}
	used, _ := a.macKey(kit.KeyEvent{Action: kit.KeyDown, Key: k, Mods: kit.ModMeta})
	return used
}

// [mac webkit]
// macKey is a key WebKit's read-only <input> takes on macOS where it differs from
// Chromium's (ok false: the shared keys below): ⌘A selects all, ⌘C copies, ⌥←/⌥→ move by
// word, ⌘←/⌘→ go to the line's ends, Control+A/Control+E too (Shift extends each); Control
// with C or the arrows is not the field's there.
func (a *addressText) macKey(e kit.KeyEvent) (used, ok bool) {
	s := a.Value()
	shift := e.Mods&kit.ModShift != 0
	mods := e.Mods &^ kit.ModShift
	lo, hi := a.Selection()
	move := func(to int) (bool, bool) {
		if shift {
			a.Select(a.anchor, to)
		} else {
			a.Select(to, to)
		}
		return true, true
	}
	switch mods {
	case kit.ModMeta:
		switch e.Key {
		case kit.KeyA:
			if !shift {
				a.Select(0, len(s))
				return true, true
			}
		case kit.KeyC:
			if !shift {
				if w := a.Window(); lo < hi && w != nil && w.Host() != nil {
					_ = w.Host().SetClipboardText(s[lo:hi])
				}
				return true, true
			}
		case kit.KeyArrowLeft, kit.KeyArrowUp:
			return move(0)
		case kit.KeyArrowRight, kit.KeyArrowDown:
			return move(len(s))
		}
		return false, true
	case kit.ModAlt:
		switch e.Key {
		case kit.KeyArrowLeft:
			return move(prevWord(s, a.caret))
		case kit.KeyArrowRight:
			return move(nextWord(s, a.caret))
		}
		return false, true
	case kit.ModCtrl:
		switch e.Key {
		case kit.KeyA:
			return move(0)
		case kit.KeyE:
			return move(len(s))
		}
		return false, true
	}
	return false, false
}

// [/mac webkit]

// submit is implicit submission: the form of the dialog the field is in.
func (a *addressText) submit() bool {
	for e := a.Base().Parent(); e != nil; e = e.Parent() {
		if d, ok := e.Widget().(interface {
			Card() *kit.Element
			Submit()
		}); ok && d.Card().Contains(a.Base()) {
			d.Submit()
			return true
		}
	}
	return false
}

// Paint is kit.Painter's: the hover ground, the selection while focused, then the text
// (clipped to the content box while the whole line is drawn and does not fit).
func (a *addressText) Paint(p *kit.PaintContext) {
	t := views.TokensOf(a.Base())
	if a.Hovered() {
		p.FillRoundedRect(kit.RoundRect(kit.RectAt(kit.Point{}, p.Size()), 4), t.Hover)
	}
	l := a.TextLayout()
	if l == nil {
		return
	}
	at := a.TextOrigin()
	at.X -= a.scroll
	at.Y += a.baseShift
	cr := a.ContentRect()
	clip := a.scroll > 0 || l.Metrics().Width > cr.Width()
	if clip {
		p.Canvas.PushClip(p.Window(cr))
	}
	if lo, hi := a.Selection(); lo < hi && a.Focused() {
		a.rects = l.AppendRangeRects(a.rects[:0], lo, hi)
		for _, r := range a.rects {
			p.FillRect(r.Add(at), t.SelHover)
		}
	}
	p.DrawText(l, at, a.Ink(t))
	if clip {
		p.Canvas.Pop()
	}
}

func prevRune(s string, i int) int {
	if i <= 0 {
		return 0
	}
	_, n := utf8.DecodeLastRuneInString(s[:i])
	return i - n
}

func nextRune(s string, i int) int {
	if i >= len(s) {
		return len(s)
	}
	_, n := utf8.DecodeRuneInString(s[i:])
	return i + n
}

func isWordRune(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }

// wordAt is the word around i (a double press): a run of letters and digits, or the one
// other character there.
func wordAt(s string, i int) (lo, hi int) {
	if i >= len(s) && i > 0 {
		i = prevRune(s, i)
	}
	if i >= len(s) {
		return i, i
	}
	if r, _ := utf8.DecodeRuneInString(s[i:]); !isWordRune(r) {
		return i, nextRune(s, i)
	}
	lo, hi = i, i
	for lo > 0 {
		q, n := utf8.DecodeLastRuneInString(s[:lo])
		if !isWordRune(q) {
			break
		}
		lo -= n
	}
	for hi < len(s) {
		q, n := utf8.DecodeRuneInString(s[hi:])
		if !isWordRune(q) {
			break
		}
		hi += n
	}
	return lo, hi
}

// prevWord is Ctrl+Left's stop: the start of the word before i.
func prevWord(s string, i int) int {
	for i > 0 {
		if q, _ := utf8.DecodeLastRuneInString(s[:i]); isWordRune(q) {
			break
		}
		i = prevRune(s, i)
	}
	for i > 0 {
		if q, _ := utf8.DecodeLastRuneInString(s[:i]); !isWordRune(q) {
			break
		}
		i = prevRune(s, i)
	}
	return i
}

// nextWord is Ctrl+Right's stop: the end of the word after i.
func nextWord(s string, i int) int {
	for i < len(s) {
		if q, _ := utf8.DecodeRuneInString(s[i:]); isWordRune(q) {
			break
		}
		i = nextRune(s, i)
	}
	for i < len(s) {
		if q, _ := utf8.DecodeRuneInString(s[i:]); !isWordRune(q) {
			break
		}
		i = nextRune(s, i)
	}
	return i
}

// icon is a decorative line icon of Icon.svelte by name.
func icon(name string, size kit.DIP, color func(*tokens.Theme) kit.Color) *widgets.Icon {
	n, _ := icons.ByName(name)
	return widgets.NewIcon(n, size, color)
}

func faint(t *tokens.Theme) kit.Color { return t.Faint }
func muted(t *tokens.Theme) kit.Color { return t.Muted }
