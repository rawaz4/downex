package kit

// TextField is a single-line text field whose typing, input methods, caret, selection,
// undo, clipboard and context menu are the system's: the host's native field
// (NativeFields) fills the widget's content box. The widget is the field's box: its owner
// (a widget that embeds it) paints the ground, border and focus look from Hovered and
// Focused, and handles the keys the field should not keep (HandleKey: Escape that clears a
// search). On a host without the capability (tests, a backend that has none) the widget
// keeps its text and shows none of it.
//
// It takes the focus with Tab, and from the keyboard selects its text, as a browser's input
// does; a click into the field focuses it too, through the host.
type TextField struct {
	Element
	// OnChange hears every change the user makes to the text (HTML's input event); SetText
	// does not call it.
	OnChange func(text string)

	opts    TextFieldOptions
	field   NativeField
	err     error
	text    string
	placed  Rect
	focused bool // the native field has the system's focus
	covered bool // a layer above the field covers it (SetCovered)
	standIn bool // the kit draws the field's text in its place (SetStandIn)
	// clipped: the field is scrolled partly out of view and not typed in, so the kit draws its
	// text, clipped as the page is. The host's field cannot be clipped: given the part that
	// shows, it centred a whole line on it, over what lies past the scroll pane's edge.
	clipped bool

	// still is the text or placeholder the kit draws while the host's field is hidden
	// (covered or stood in for), laid out in the content box; stillInk is its colour and
	// stillBase the font's ascent and descent, which place its baseline as the host's field
	// places its own (in the middle of the content box).
	still              TextLayout
	stillInk           Color
	stillAsc, stillDsc DIP
}

// NewTextField makes a field that looks as opts says (its Placeholder, colours and font).
func NewTextField(opts TextFieldOptions) *TextField {
	f := &TextField{opts: opts}
	f.self = f
	f.Focus = FocusTab
	f.Cursor = CursorText
	f.Semantics.Role = RoleTextField
	return f
}

// Text is the field's text.
func (f *TextField) Text() string { return f.text }

// SetCovered says whether a layer drawn above the field (the command palette over the title
// bar) covers it. The host's field is a native child window that would draw over that layer,
// so it hides while covered; the widget's own box is still painted, under the layer.
func (f *TextField) SetCovered(c bool) {
	if c == f.covered {
		return
	}
	f.covered = c
	f.place()
	f.stillChanged()
}

// Covered reports what SetCovered last set.
func (f *TextField) Covered() bool { return f.covered }

// SetStandIn says whether the kit draws the field's text in the host's field's place: the host's
// field, a native child window, hides, and PaintStandIn draws its text (or placeholder) as the
// window's own text, which takes the opacity and the layers the window paints around it (the
// title bar's search at 0.45 opacity while a modal is open: the child window could not fade).
// The field is inert meanwhile in the owner's use (a modal's backdrop takes the input).
func (f *TextField) SetStandIn(on bool) {
	if on == f.standIn {
		return
	}
	f.standIn = on
	f.place()
	f.stillChanged()
}

// StandIn reports what SetStandIn last set.
func (f *TextField) StandIn() bool { return f.standIn }

// hidden reports whether the host's field hides while the widget shows: a layer covers it, or
// the kit draws its text in its place.
func (f *TextField) hidden() bool { return f.covered || f.standIn || f.clipped }

// stillChanged drops the text the kit draws for the hidden host's field; the next layout
// makes it again while the field stays hidden.
func (f *TextField) stillChanged() {
	if f.still != nil {
		f.still.Release()
		f.still = nil
	}
	f.Invalidate()
	if f.hidden() {
		f.RelayoutContent()
	}
}

// PaintStandIn draws the field's text, or its placeholder while it is empty, where the host's
// field shows it, while the host's field is hidden (SetCovered, SetStandIn); else nothing. The
// owner's Paint calls it after the box: the text is the field's content.
func (f *TextField) PaintStandIn(p *PaintContext) {
	if f.still == nil || !f.hidden() || f.field == nil {
		return
	}
	cr := f.ContentRect()
	// The host's field puts the font's ascent and descent in the middle of the content box,
	// its baseline on a device pixel.
	base := p.Scale.Snap(p.Origin.Y+cr.Min.Y+(cr.Height()-f.stillAsc-f.stillDsc)/2+f.stillAsc) - p.Origin.Y
	p.Canvas.PushClip(cr.Add(p.Origin))
	p.DrawText(f.still, Pt(cr.Min.X, base-f.still.Metrics().Baseline), f.stillInk)
	p.Canvas.Pop()
}

// SetText replaces the text, without OnChange.
func (f *TextField) SetText(s string) {
	if s == f.text {
		return
	}
	f.text = s
	if f.field != nil {
		f.field.SetText(s)
	}
	if f.hidden() {
		f.stillChanged()
	}
	f.SemanticsChanged()
}

// SelectAll selects the whole text (Ctrl+F on the search: focus, then select).
func (f *TextField) SelectAll() {
	if f.field != nil {
		f.field.SelectAll()
	}
}

// Options are how the field looks.
func (f *TextField) Options() TextFieldOptions { return f.opts }

// SetOptions changes how the field looks (a theme change).
func (f *TextField) SetOptions(o TextFieldOptions) {
	if o == f.opts {
		return
	}
	f.opts = o
	if f.field != nil {
		f.field.SetOptions(o)
	}
	if f.hidden() {
		f.stillChanged()
	}
	f.SemanticsChanged()
}

// Native is the host's field, or nil without one; Err says why it could not be made.
func (f *TextField) Native() NativeField { return f.field }

// Err is why the host's field could not be made, or nil.
func (f *TextField) Err() error { return f.err }

// Attached makes the host's field (Attacher).
func (f *TextField) Attached() {
	w := f.win
	nf, ok := w.host.(NativeFields)
	if !ok || f.field != nil {
		return
	}
	field, err := nf.NewTextField(f.opts, TextFieldCallbacks{Changed: f.changed, Focused: f.nativeFocus})
	if err != nil {
		f.err = err
		return
	}
	f.field, f.err, f.placed = field, nil, Rect{}
	if f.text != "" {
		field.SetText(f.text)
	}
}

// Detached releases the host's field (Detacher).
func (f *TextField) Detached() {
	if f.field != nil {
		f.field.Release()
		f.field = nil
	}
	if f.still != nil {
		f.still.Release()
		f.still = nil
	}
	f.focused, f.placed = false, Rect{}
}

// LaidOut is LayoutFollower's: the host's field goes on the widget's content box after every
// layout pass, or hides while the widget is not shown.
func (f *TextField) LaidOut() { f.place() }

// place puts the host's field on the widget's content box, or hides it while the widget is
// not shown.
func (f *TextField) place() {
	if f.field == nil {
		return
	}
	var r Rect
	clipped := false
	if f.visible() && !f.covered && !f.standIn {
		r = f.ContentRect().Add(f.WindowBounds().Min)
		if vis, ok := f.visibleRect(); !ok {
			r = Rect{}
		} else if r.Intersect(vis) != r {
			if f.focused {
				r = r.Intersect(vis) // typed in: the host's field stays, on what shows
			} else {
				r, clipped = Rect{}, true // the kit draws its text, clipped (PaintStandIn)
			}
		}
	}
	if r != f.placed {
		f.placed = r
		f.field.SetBounds(r)
	}
	if clipped != f.clipped {
		f.clipped = clipped
		f.stillChanged()
	}
}

// changed is the field's Changed callback. Like every call from the host into the kit, it
// runs as a call into the window: what it defers runs once it has returned, and the
// semantic events it queues (the text, and what OnChange changes, such as a list's rows
// after a search) go out then.
func (f *TextField) changed(text string) {
	if w := f.win; w != nil {
		w.enter()
		defer w.leave()
	}
	if text == f.text {
		return
	}
	f.text = text
	if f.hidden() {
		f.stillChanged()
	}
	f.SemanticsChanged()
	if f.OnChange != nil {
		f.OnChange(text)
	}
}

// nativeFocus is the field's Focused callback: a click into the field focuses the widget.
// It runs as a call into the window, as changed does.
func (f *TextField) nativeFocus(in bool) {
	w := f.win
	if w != nil {
		w.enter()
		defer w.leave()
	}
	f.focused = in
	if w == nil || !in || w.focused == &f.Element {
		return
	}
	w.focusVisible = false // a press, or the system giving the focus back
	if !w.Focus(f) {
		f.field.Blur() // it cannot have the focus now (inside an inert part)
	}
}

// FocusChanged moves the system's focus with the kit's (FocusHandler).
func (f *TextField) FocusChanged(focused bool) {
	if f.field == nil {
		return
	}
	// The field's own Focused callback follows later (the host queues it); the state is
	// known now.
	switch {
	case focused && !f.focused:
		f.focused = true
		f.place() // a field drawn by the kit while scrolled partly out shows the host's again
		f.field.Focus()
		if f.win != nil && f.win.focusVisible {
			f.field.SelectAll() // Tab into an input selects its text
		}
	case !focused && f.focused:
		f.focused = false
		f.field.Blur()
		f.place()
	}
	f.Invalidate()
}

// HoverChanged repaints the box (HoverHandler).
func (f *TextField) HoverChanged(bool) { f.Invalidate() }

// RestoreFocus is FocusRestorer's: it gives the system's focus back to the field when the
// window gets the focus while the field is the kit's focused element (after Alt+Tab).
func (f *TextField) RestoreFocus() {
	if f.field != nil && !f.focused {
		f.focused = true
		f.field.Focus()
	}
}

// ContentWidths is Layouter's: a field's content never sizes it.
func (f *TextField) ContentWidths(*LayoutContext) (min, max DIP) { return 0, 0 }

// ContentHeight is Layouter's.
func (f *TextField) ContentHeight(*LayoutContext, DIP) DIP { return 0 }

// LayoutContent is Layouter's; the field is placed once the pass is over. While the host's
// field is hidden, it lays out the text the kit draws in its place (PaintStandIn): the text,
// or the placeholder while there is none, on one line in the field's alignment and direction.
func (f *TextField) LayoutContent(ctx *LayoutContext, box Rect) {
	if f.still != nil {
		f.still.Release()
		f.still = nil
	}
	if !f.hidden() || f.field == nil || ctx.Text == nil {
		return
	}
	s, ink := f.text, f.opts.Color
	if s == "" {
		s, ink = f.opts.Placeholder, f.opts.PlaceholderColor
	}
	if s == "" {
		return
	}
	fm, err := ctx.Text.FontMetrics(f.opts.Style)
	if err != nil {
		return
	}
	l, err := ctx.Text.LayoutText(s, f.opts.Style, TextOptions{MaxWidth: max(box.Width(), 0), Align: f.opts.Align, Direction: f.opts.ResolveDirection(f.text)})
	if err != nil {
		return
	}
	f.still, f.stillInk, f.stillAsc, f.stillDsc = l, ink, fm.Ascent, fm.Descent
}

// FillSemantics is SemanticFiller's: the field's name and text.
func (f *TextField) FillSemantics(n *SemanticNode) {
	if n.Name == "" {
		n.Name = f.opts.Name
	}
	n.Value = f.text
	n.Actions |= ActionSetValue
}

// PerformAction is ActionHandler's: SetValue replaces the text as typing would.
func (f *TextField) PerformAction(r ActionRequest) error {
	if r.Action != ActionSetValue {
		return ErrUnsupported
	}
	f.SetText(r.Text)
	if f.OnChange != nil {
		f.OnChange(r.Text)
	}
	return nil
}

// FocusRestorer is a widget that holds the system's focus itself (a native field): when the
// window gets the focus back (FocusEvent In) while it is the kit's focused element, the
// window calls RestoreFocus once, so it can take the system's focus again.
type FocusRestorer interface {
	RestoreFocus()
}
