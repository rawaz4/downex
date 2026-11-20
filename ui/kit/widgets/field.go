package widgets

import (
	"math"
	"time"

	"downex/ui/core/queues"
	"downex/ui/kit"
	"downex/ui/kit/tokens"
)

// FieldSize is an input's height (app.css .input, .input.sm, .input.xs).
type FieldSize uint8

// The sizes.
const (
	FieldMd FieldSize = iota // 36 px, 9 px corners (dialog fields)
	FieldSm                  // 30 px, 8 px corners
	FieldXs                  // 26 px, 6 px corners, 8 px padding
)

// InputOptions describe an input.
type InputOptions struct {
	Placeholder string
	// Name is the accessible name (a <label for>'s text or aria-label); Description its hint
	// or error (aria-describedby).
	Name, Description string
	Size              FieldSize
	// Mono is .input.mono: addresses and paths.
	Mono     bool
	Invalid  bool
	Disabled bool
	ReadOnly bool
	// MaxLength is HTML's maxlength (UTF-16 code units); 0 is none.
	MaxLength int
	// Password is type=password: the system's password mark, no input methods, and the text
	// never exposed to assistive technology.
	Password bool
	// AutoDirection is dir="auto" (a file, queue or category name): the field takes its text's
	// direction, so a Kurdish or Arabic name sits at the right and reads right to left
	// (kit.TextFieldOptions.AutoDirection).
	AutoDirection bool
	// Align is the text's CSS text-align (start when zero; end for numbers and limits, such
	// as Settings' .s-num and .s-rate and the Queues' .repeat), in the window's direction.
	Align kit.TextAlign
	// Number is type=number: Chromium keeps its inner spin button's room (NumberSpinRoom) at
	// the end of the content box, shown or not, so the value ends (an end-aligned one) or is
	// cut off (a long one) that far before the end padding. Downex's number fields never show
	// the button; only its room is kept.
	Number bool
}

// NumberSpinRoom is the room (CSS px) Chromium keeps for an <input type="number">'s inner spin
// button at the end of its content box, whatever the text size or scale (measured in the 0.9.2
// references: Settings' .s-num, the Queues' .repeat).
const NumberSpinRoom = 15

// Input is app.css's .input around the system's own text field (kit.TextField): --field with a
// 1 px --line-field border, 13 px text, a --faint placeholder; focused, a 2 px --focus edge (the
// border and a 1 px ring) and a 4 px --accent-soft spread; --danger while invalid; --surface-2
// while disabled or read-only. Typing, input methods, the caret, selection, undo, the clipboard
// and the field's menu are the system's.
type Input struct {
	kit.TextField
	o    InputOptions
	look transition
	// OnEnter hears Enter in the field; it reports whether it used it (else a dialog around
	// the field submits, as a form does).
	OnEnter func() bool
	scale   kit.Scale
	applied bool
}

// NewInput makes an input.
func NewInput(text string, o InputOptions, onChange func(string)) *Input {
	in := &Input{o: o}
	in.TextField = *kit.NewTextField(kit.TextFieldOptions{})
	in.OnChange = onChange
	in.SetText(text)
	in.Ring = kit.Ring{None: true} // the border and ring show the focus
	in.Semantics.Name = o.Name
	in.Semantics.Description = o.Description
	in.Disabled = o.Disabled
	h, pad := kit.DIP(36), kit.DIP(11)
	switch o.Size {
	case FieldSm:
		h = 30
	case FieldXs:
		h, pad = 26, 8
	}
	in.Style = kit.Style{
		Width: kit.Pct(100), Height: kit.Px(h), MinHeight: kit.Px(h), MinWidth: kit.Px(0),
		Padding: kit.EdgesInline(kit.Px(pad)), Border: kit.EdgesAll(kit.Px(1)),
	}
	if o.Number {
		in.Style.Padding.End = kit.Px(pad + NumberSpinRoom)
	}
	return in
}

// InputOptions are the input's options.
func (in *Input) InputOptions() InputOptions { return in.o }

// SetReadOnly shows the value as read-only (aria-readonly, the --surface-2 ground); the
// owner keeps the text as it is meanwhile (the system field has no read-only mode).
func (in *Input) SetReadOnly(r bool) {
	if r != in.o.ReadOnly {
		in.o.ReadOnly = r
		in.Invalidate()
		in.SemanticsChanged()
	}
}

// SetInvalid marks the value wrong (aria-invalid).
func (in *Input) SetInvalid(v bool) {
	if v != in.o.Invalid {
		in.o.Invalid = v
		in.Invalidate()
		in.SemanticsChanged()
	}
}

// SetDisabled makes the input unavailable.
func (in *Input) SetDisabled(d bool) {
	if d == in.Disabled {
		return
	}
	in.Disabled, in.o.Disabled = d, d
	if d && in.Focused() {
		if w := in.Window(); w != nil {
			w.Blur()
		}
	}
	in.applyOptions()
	in.Invalidate()
	in.SemanticsChanged()
}

// SetPlaceholder changes the placeholder.
func (in *Input) SetPlaceholder(s string) {
	if s != in.o.Placeholder {
		in.o.Placeholder = s
		in.applyOptions()
	}
}

func (in *Input) font() kit.Font {
	if in.o.Mono {
		return FontMono
	}
	return FontBody
}

func (in *Input) radius() kit.DIP {
	switch in.o.Size {
	case FieldSm:
		return 8
	case FieldXs:
		return 6
	}
	return 9
}

func (in *Input) ground(t *tokens.Theme) kit.Color {
	if in.Disabled || in.o.ReadOnly {
		return t.Surface2
	}
	return t.Field
}

// applyOptions gives the native field the theme's colours and the font at the window's scale.
func (in *Input) applyOptions() {
	w := in.Window()
	if w == nil {
		return
	}
	t := tokensOf(&in.Element)
	ink := t.Ink
	if in.Disabled {
		ink = t.Faint
	}
	in.SetOptions(kit.TextFieldOptions{
		Style: in.font().Style(w.Environment().Scale), Color: ink, Background: in.ground(t),
		Placeholder: in.o.Placeholder, PlaceholderColor: t.Faint, Name: in.o.Name,
		Align: in.o.Align, Direction: w.Direction(), AutoDirection: in.o.AutoDirection,
	})
	if fc, ok := in.Native().(kit.FieldControl); ok && !in.applied {
		in.applied = true
		if in.o.MaxLength > 0 {
			fc.SetMaxLength(in.o.MaxLength)
		}
		if in.o.Password {
			fc.SetPassword(true)
		}
	}
}

// Attached is kit.Attacher's: the native field, in the theme's colours.
func (in *Input) Attached() {
	in.TextField.Attached()
	in.applied = false
	in.applyOptions()
	in.look.jump(&in.Element, in.state())
}

// LayoutContent is kit.Layouter's: a new scale or text size gives the field its font again.
func (in *Input) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	if ctx.Scale != in.scale {
		in.scale = ctx.Scale
		in.applyOptions()
	}
	in.TextField.LayoutContent(ctx, box)
}

func (in *Input) state() uint8 {
	if in.Focused() {
		return 1
	}
	return 0
}

// FocusChanged is kit.FocusHandler's.
func (in *Input) FocusChanged(f bool) {
	in.TextField.FocusChanged(f)
	in.look.moveTo(&in.Element, in.state(), tokens.Dur2)
}

// HandleKey is kit.KeyHandler's: Enter is OnEnter's (or the dialog's).
func (in *Input) HandleKey(e kit.KeyEvent) bool {
	if e.Action == kit.KeyDown && e.Key == kit.KeyEnter && e.Mods&(kit.ModCtrl|kit.ModAlt|kit.ModMeta|kit.ModShift) == 0 && in.OnEnter != nil {
		return in.OnEnter()
	}
	return false
}

// implicitSubmit marks a single-line field: Enter in it submits the dialog's form.
func (in *Input) implicitSubmit() bool { return true }

// FillSemantics is kit.SemanticFiller's: a password's value is never exposed.
func (in *Input) FillSemantics(n *kit.SemanticNode) {
	in.TextField.FillSemantics(n)
	if in.o.Password {
		n.States |= kit.NodeProtected
		n.Value = ""
	}
	if in.o.Invalid {
		n.States |= kit.NodeInvalid
	}
	if in.o.ReadOnly {
		n.States |= kit.NodeReadOnly
	}
}

// Paint is kit.Painter's: the box, its focus look, and the field's colours when the theme
// changed under it.
func (in *Input) Paint(p *kit.PaintContext) {
	t := tokensOf(&in.Element)
	r := snapBox(p, in.radius())
	share := in.look.amountOf(1) // how focused it looks (the transition of border and shadow)
	ring, soft := t.Focus, t.AccentSoft
	if in.o.Invalid {
		ring, soft = t.Danger, t.DangerSoft
	}
	if share > 0 && !t.IsForced() {
		drawFieldFocus(p.Canvas, r, ring.WithAlpha(ring.A*share), soft.WithAlpha(soft.A*share))
	}
	p.Canvas.FillRoundedRect(r, in.ground(t))
	rest, focus := t.LineField, t.Focus
	if in.o.Invalid {
		rest, focus = t.Danger, t.Danger
	}
	if t.IsForced() {
		rest, focus = t.Ink, t.Ink
	}
	border := kit.Mix(focus, share, rest)
	strokeInside(p.Canvas, r, p.Scale.SnapWidth(1), border)
	if in.Focused() && t.IsForced() {
		w := p.Scale.SnapWidth(2) // outline: 2px solid Highlight; outline-offset: 1px
		p.Canvas.StrokeRoundedRect(r.Inset(-1-w/2), kit.Stroke{Width: w}, t.Focus)
	}
	if o := in.Options(); o.Background != in.ground(t) && in.Window() != nil {
		w := in.Window()
		_ = w.Post(in.applyOptions)
	}
	in.PaintStandIn(p) // its text, while its native field is hidden (kit.TextField.SetStandIn)
}

// NumberField is an input whose value applies itself as it is typed (the progress window's
// speed limit and connections, ProgressWindow.svelte): 600 ms after the last change, at once on
// Enter, and when the field is left.
type NumberField struct {
	Input
	// Apply applies the text.
	Apply func(text string)
	// Delay is how long typing waits to apply (applyDelayMs: 600 ms when 0).
	Delay   time.Duration
	timer   *kit.Timer
	pending bool
}

// NewNumberField makes an apply-on-type field.
func NewNumberField(text string, o InputOptions, apply func(string)) *NumberField {
	n := &NumberField{Apply: apply}
	n.Input = *NewInput(text, o, nil)
	n.OnChange = n.changed
	n.OnEnter = func() bool {
		n.applyNow()
		return true // onEnter: preventDefault
	}
	return n
}

func (n *NumberField) delay() time.Duration {
	if n.Delay > 0 {
		return n.Delay
	}
	return 600 * time.Millisecond
}

// changed is the input event: apply soon.
func (n *NumberField) changed(string) {
	n.pending = true
	if n.timer != nil {
		n.timer.Stop()
	}
	if w := n.Window(); w != nil {
		n.timer = w.After(n.delay(), n.applyNow)
	}
}

func (n *NumberField) applyNow() {
	if n.timer != nil {
		n.timer.Stop()
		n.timer = nil
	}
	n.pending = false
	if n.Apply != nil {
		n.Apply(n.Text())
	}
}

// FocusChanged is kit.FocusHandler's: leaving the field applies it (onblur).
func (n *NumberField) FocusChanged(f bool) {
	n.Input.FocusChanged(f)
	if !f {
		n.applyNow()
	}
}

// Detached is kit.Detacher's.
func (n *NumberField) Detached() {
	if n.timer != nil {
		n.timer.Stop()
		n.timer = nil
	}
	n.Input.Detached()
}

// DateTimeField is DateTimeField.svelte (DESIGN-SYSTEM.md §3.5): a time (24-hour hh:mm) or a
// date (yyyy-mm-dd) in a small input, in the app's own format, never the system's 12-hour
// picker or calendar. Typing works as in any field; Up and Down step the part at the caret
// (hour or minute; year, month or day), Page Up and Page Down by 10, and the part stays
// selected. A value it can read is written out in full when the field is left ("7:5" stays as
// typed, "7:05" becomes "07:05").
type DateTimeField struct {
	Input
	Kind queues.FieldKind
	// Fallback is where Up and Down start from when the field holds nothing it can read.
	Fallback string
}

// NewDateTimeField makes a time or date field named name.
func NewDateTimeField(kind queues.FieldKind, value, name string, onChange func(string)) *DateTimeField {
	o := InputOptions{Name: name, Size: FieldSm, Placeholder: "hh:mm", MaxLength: 5,
		Description: "24-hour time. Up and Down change the hour or the minute at the cursor."}
	if kind == queues.DateField {
		o.Placeholder, o.MaxLength = "yyyy-mm-dd", 10
		o.Description = "Year, month, day. Up and Down change the part at the cursor."
	}
	d := &DateTimeField{Kind: kind}
	d.Input = *NewInput(value, o, onChange)
	d.Style.Width = kit.Rem(6.5)
	if kind == queues.DateField {
		d.Style.Width = kit.Rem(8.5)
	}
	return d
}

// SetDescription adds the field's own description to its hint (aria-describedby: the form's
// first, then the keys').
func (d *DateTimeField) SetDescription(s string) {
	keys := "24-hour time. Up and Down change the hour or the minute at the cursor."
	if d.Kind == queues.DateField {
		keys = "Year, month, day. Up and Down change the part at the cursor."
	}
	if s != "" {
		keys = s + " " + keys
	}
	d.Semantics.Description = keys
	d.SemanticsChanged()
}

// HandleKey is kit.KeyHandler's: the steps.
func (d *DateTimeField) HandleKey(e kit.KeyEvent) bool {
	if e.Action != kit.KeyDown || e.Mods&(kit.ModAlt|kit.ModCtrl|kit.ModMeta) != 0 {
		return d.Input.HandleKey(e)
	}
	by := 0
	switch e.Key {
	case kit.KeyArrowUp:
		by = 1
	case kit.KeyArrowDown:
		by = -1
	case kit.KeyPageUp:
		by = 10
	case kit.KeyPageDown:
		by = -10
	default:
		return d.Input.HandleKey(e)
	}
	parts := queues.ParseParts(d.Kind, d.Text())
	if parts == nil {
		parts = queues.ParseParts(d.Kind, d.Fallback)
	}
	if parts == nil {
		if d.Kind == queues.TimeField {
			parts = []int{0, 0}
		} else {
			parts = todayParts(popupNow(d.Window()).Local())
		}
	}
	caret := 0
	fc, _ := d.Native().(kit.FieldControl)
	if fc != nil {
		caret, _ = fc.Selection()
	}
	part := queues.PartAt(d.Kind, caret)
	v := queues.FormatParts(d.Kind, queues.StepParts(d.Kind, parts, part, by))
	d.set(v)
	if fc != nil {
		fc.SetSelection(queues.PartRange(d.Kind, part))
	}
	return true
}

func (d *DateTimeField) set(v string) {
	if v == d.Text() {
		return
	}
	d.SetText(v)
	if d.OnChange != nil {
		d.OnChange(v)
	}
}

// FocusChanged is kit.FocusHandler's: leaving writes a readable value out in full.
func (d *DateTimeField) FocusChanged(f bool) {
	d.Input.FocusChanged(f)
	if !f {
		if parts := queues.ParseParts(d.Kind, d.Text()); parts != nil {
			d.set(queues.FormatParts(d.Kind, parts))
		}
	}
}

// TextArea is app.css's textarea.input: several lines of monospace text (var(--mono-weight)
// 0.75rem/1.55 var(--font-mono), -0.01em) in a field box with 8 px padding above and below,
// rows lines high (Add's links: 4). It wraps at its width and scrolls vertically. Enter breaks
// the line; Ctrl+Enter is left to the dialog (Add submits with it).
type TextArea struct {
	kit.Element
	o        InputOptions
	Rows     int
	OnChange func(string)
	// OnSubmit hears Ctrl+Enter (AddDialog: "Ctrl+Enter adds").
	OnSubmit func()
	text     string
	field    kit.NativeField
	err      error
	placed   kit.Rect
	focused  bool // the native field has the system's focus
	scale    kit.Scale
	look     transition
	// grip is the resizer's drag (resize: vertical): the border-box height and the
	// pointer's window Y at the press, while dragging; overGrip that the pointer is over it.
	grip struct {
		dragging     bool
		from, height kit.DIP
	}
	overGrip bool
}

// NewTextArea makes a text area of rows lines.
func NewTextArea(text string, rows int, o InputOptions, onChange func(string)) *TextArea {
	a := &TextArea{o: o, Rows: rows, text: text, OnChange: onChange}
	a.Focus = kit.FocusTab
	a.Cursor = kit.CursorText
	a.Ring = kit.Ring{None: true}
	a.Disabled = o.Disabled
	a.Semantics.Role = kit.RoleTextField
	a.Semantics.Name = o.Name
	a.Semantics.Description = o.Description
	// .input's min-block-size: 36px bounds the resizer's drag (Chromium's own least is 15 px).
	a.Style = kit.Style{
		Width: kit.Pct(100), MinWidth: kit.Px(0), MinHeight: kit.Px(textAreaMinHeight), Padding: kit.EdgesOf(kit.Px(8), kit.Px(11)), Border: kit.EdgesAll(kit.Px(1)),
	}
	return a
}

// Text is the text.
func (a *TextArea) Text() string { return a.text }

// SetText replaces the text without OnChange.
func (a *TextArea) SetText(s string) {
	if s == a.text {
		return
	}
	a.text = s
	if a.field != nil {
		a.field.SetText(s)
	}
	a.SemanticsChanged()
}

// Native is the host's field, or nil; Err says why it could not be made.
func (a *TextArea) Native() kit.NativeField { return a.field }
func (a *TextArea) Err() error              { return a.err }

func (a *TextArea) options() kit.TextFieldOptions {
	t := tokensOf(&a.Element)
	var s kit.Scale
	if w := a.Window(); w != nil {
		s = w.Environment().Scale
	}
	ink, ground := t.Ink, t.Field
	if a.Disabled {
		ink, ground = t.Faint, t.Surface2
	}
	f := FontMono
	f.LineHeight = 1.55
	return kit.TextFieldOptions{Style: f.Style(s), Color: ink, Background: ground, Placeholder: a.o.Placeholder, PlaceholderColor: t.Faint, Name: a.o.Name}
}

// Attached is kit.Attacher's: the host's multi-line field.
func (a *TextArea) Attached() {
	w := a.Window()
	kinds, ok := w.Host().(kit.NativeFieldKinds)
	if !ok || a.field != nil {
		return
	}
	f, err := kinds.NewField(a.options(), kit.FieldOptions{Kind: kit.FieldMultiline, MaxLength: a.o.MaxLength, LineHeight: 1.55},
		kit.TextFieldCallbacks{Changed: a.changed, Focused: a.nativeFocus})
	if err != nil {
		a.err = err
		return
	}
	a.field, a.err, a.placed = f, nil, kit.Rect{}
	if a.text != "" {
		f.SetText(a.text)
	}
	a.look.jump(&a.Element, 0)
}

// Detached is kit.Detacher's.
func (a *TextArea) Detached() {
	if a.field != nil {
		a.field.Release()
		a.field = nil
	}
	a.focused, a.placed = false, kit.Rect{}
}

// ContentWidths is kit.Layouter's: the field's content never sizes it.
func (a *TextArea) ContentWidths(*kit.LayoutContext) (min, max kit.DIP) { return 0, 0 }

// ContentHeight is kit.Layouter's: rows lines of the font's 1.55 line height.
func (a *TextArea) ContentHeight(ctx *kit.LayoutContext, _ kit.DIP) kit.DIP {
	rows := a.Rows
	if rows <= 0 {
		rows = 2 // HTML's default
	}
	return kit.DIP(rows) * ctx.Scale.Rem(0.75) * 1.55
}

// LayoutContent is kit.Layouter's: the field takes a new font after a scale change.
func (a *TextArea) LayoutContent(ctx *kit.LayoutContext, _ kit.Rect) {
	if ctx.Scale != a.scale && a.field != nil {
		a.scale = ctx.Scale
		a.field.SetOptions(a.options())
		a.placed = kit.Rect{} // its pixels changed: place it again
	}
}

// LaidOut is kit.LayoutFollower's: after every layout pass of the window the field follows its
// box, also when the pass only moved it along with something else (a scrolled dialog body).
func (a *TextArea) LaidOut() { a.place() }

// place puts the host's field on the content box (or hides it).
func (a *TextArea) place() {
	if a.field == nil {
		return
	}
	var r kit.Rect
	if a.Window() != nil && !a.hiddenUp() {
		r = a.ContentRect().Add(a.WindowBounds().Min)
	}
	if r != a.placed {
		a.placed = r
		a.field.SetBounds(r)
	}
}

// hiddenUp reports whether the area or an ancestor is hidden.
func (a *TextArea) hiddenUp() bool {
	for p := &a.Element; p != nil; p = p.Parent() {
		if p.Style.Hidden {
			return true
		}
	}
	return false
}

func (a *TextArea) changed(text string) {
	if text == a.text {
		return
	}
	a.text = text
	a.SemanticsChanged()
	if a.OnChange != nil {
		a.OnChange(text)
	}
}

func (a *TextArea) nativeFocus(in bool) {
	a.focused = in
	w := a.Window()
	if w == nil || !in || w.Focused() == &a.Element {
		return
	}
	w.SetFocusVisible(false)
	if !w.Focus(a) {
		a.field.Blur()
	}
}

// FocusChanged is kit.FocusHandler's: the system's focus follows the kit's.
func (a *TextArea) FocusChanged(focused bool) {
	if a.field != nil {
		switch {
		case focused && !a.focused:
			a.focused = true
			a.field.Focus()
		case !focused && a.focused:
			a.focused = false
			a.field.Blur()
		}
	}
	st := uint8(0)
	if focused {
		st = 1
	}
	a.look.moveTo(&a.Element, st, tokens.Dur2)
}

// RestoreFocus is kit.FocusRestorer's: the window was activated again while the area had the
// kit's focus, so its multi-line field takes the system's focus back, once the activation is
// over (the host's field decides whether it had the focus). Before, every repaint while
// focused posted it, which allocated in steady frames.
func (a *TextArea) RestoreFocus() {
	rc, ok := a.field.(kit.FieldReclaimer)
	if w := a.Window(); ok && w != nil {
		_ = w.Post(rc.Reclaim)
	}
}

// HandleKey is kit.KeyHandler's: Enter breaks the line (one undoable edit); Ctrl+Enter goes on
// to the dialog.
func (a *TextArea) HandleKey(e kit.KeyEvent) bool {
	if e.Action == kit.KeyDown && e.Key == kit.KeyEnter && e.Mods == 0 {
		if fc, ok := a.field.(kit.FieldControl); ok {
			fc.ReplaceSelection("\r\n")
			return true
		}
	}
	return false
}

// HoverChanged is kit.HoverHandler's.
func (a *TextArea) HoverChanged(bool) { a.Invalidate() }

// FillSemantics is kit.SemanticFiller's.
func (a *TextArea) FillSemantics(n *kit.SemanticNode) {
	n.Value = a.text
	n.Actions |= kit.ActionSetValue
	if a.o.Invalid {
		n.States |= kit.NodeInvalid
	}
}

// PerformAction is kit.ActionHandler's.
func (a *TextArea) PerformAction(r kit.ActionRequest) error {
	if r.Action != kit.ActionSetValue {
		return kit.ErrUnsupported
	}
	a.SetText(r.Text)
	if a.OnChange != nil {
		a.OnChange(r.Text)
	}
	return nil
}

// SetInvalid marks the text wrong (aria-invalid).
func (a *TextArea) SetInvalid(v bool) {
	if v != a.o.Invalid {
		a.o.Invalid = v
		a.Invalidate()
		a.SemanticsChanged()
	}
}

// Paint is kit.Painter's: the field's box (as Input's, 9 px corners).
func (a *TextArea) Paint(p *kit.PaintContext) {
	t := tokensOf(&a.Element)
	r := snapBox(p, 9)
	focused := a.Focused()
	ring, soft := t.Focus, t.AccentSoft
	if a.o.Invalid {
		ring, soft = t.Danger, t.DangerSoft
	}
	if focused && !t.IsForced() {
		drawFieldFocus(p.Canvas, r, ring, soft)
	}
	ground := t.Field
	if a.Disabled {
		ground = t.Surface2
	}
	p.Canvas.FillRoundedRect(r, ground)
	border := t.LineField
	switch {
	case t.IsForced():
		border = t.Ink
	case a.o.Invalid:
		border = t.Danger
	case focused:
		border = t.Focus
	}
	strokeInside(p.Canvas, r, p.Scale.SnapWidth(1), border)
	paintResizer(p, r.Rect, p.Scale.SnapWidth(1))
	w := a.Window()
	if w == nil {
		return
	}
	if o := a.options(); o.Background != ground && a.field != nil {
		_ = w.Post(func() { a.field.SetOptions(a.options()) })
	}
}

// textAreaMinHeight is app.css's .input min-block-size, the least height the resizer drags
// the area to.
const textAreaMinHeight = 36

// resizerSize is the resizer's square at the padding box's bottom-right corner (Chromium's
// scroll corner, 15 px), where a press starts the drag.
const resizerSize = 15

// gripRect is the resizer's square in the area's coordinates.
func (a *TextArea) gripRect() kit.Rect {
	s := a.Size()
	return kit.R(s.W-1-resizerSize, s.H-1-resizerSize, s.W-1, s.H-1)
}

// HandlePointer is kit.PointerHandler's: textarea.input's resize: vertical. A press on the
// resizer drags the area's height (Chromium sets the height the drag gives, at least
// .input's 36 px; the dialog lays out again around it), with the ns-resize pointer over it;
// there is no keyboard way, as in Chromium.
func (a *TextArea) HandlePointer(in *kit.PointerInput) bool {
	w := a.Window()
	if w == nil {
		return false
	}
	switch in.Action {
	case kit.PointerMove, kit.PointerEnter:
		if a.grip.dragging {
			a.dragTo(in.Pos.Y)
			return true
		}
		a.setOverGrip(a.gripRect().Contains(in.Local))
		return a.overGrip
	case kit.PointerLeave:
		if !a.grip.dragging {
			a.setOverGrip(false)
		}
		return false
	case kit.PointerDown:
		if in.Button != kit.ButtonLeft || a.grip.dragging || !a.gripRect().Contains(in.Local) {
			return false
		}
		a.grip.dragging, a.grip.from, a.grip.height = true, in.Pos.Y, a.Size().H
		a.setOverGrip(true)
		w.Capture(&a.Element)
		return true
	case kit.PointerUp, kit.PointerCancel:
		if !a.grip.dragging {
			return false
		}
		if in.Action == kit.PointerUp {
			a.dragTo(in.Pos.Y)
		}
		a.grip.dragging = false
		a.setOverGrip(in.Action == kit.PointerUp && a.gripRect().Contains(in.Local))
		return true
	}
	return false
}

// dragTo gives the area the height the drag to window Y y asks for.
func (a *TextArea) dragTo(y kit.DIP) {
	h := max(a.grip.height+y-a.grip.from, textAreaMinHeight)
	if a.Style.Height == kit.Px(h) {
		return
	}
	a.Style.Height = kit.Px(h)
	a.Relayout()
}

// ResetHeight drops the height the resizer gave (a new textarea has its rows' height again:
// AddDialog makes its URL list anew when it shows Files again).
func (a *TextArea) ResetHeight() {
	if a.Style.Height != (kit.Length{}) {
		a.Style.Height = kit.Length{}
		a.Relayout()
	}
}

func (a *TextArea) setOverGrip(over bool) {
	if over == a.overGrip {
		return
	}
	a.overGrip = over
	a.Cursor = kit.CursorText
	if over {
		a.Cursor = kit.CursorRowResize
	}
	if w := a.Window(); w != nil {
		w.UpdateCursor()
	}
}

// paintResizer draws Chromium's resizer in the bottom-right corner of the padding box of
// border box r (border bw): two diagonal strokes, dark (black at 60 %) with a light one (white
// at 60 %) below it, so it shows on any ground; whole device pixels, as Chromium paints it
// (today's Add files at 100 % and 125 %: the long stroke ends 2.5 px inside both edges and is
// 6 px long, the short one 2 px).
func paintResizer(p *kit.PaintContext, r kit.Rect, bw kit.DIP) {
	s := p.Scale
	px := func(d kit.DIP) int { return int(math.Floor(float64(s.Pixels(d)))) }
	right := px(r.Max.X - bw - 2.5)  // the strokes' right column
	bottom := px(r.Max.Y - bw - 2.5) // their bottom row
	ppd := float64(s.Pixels(1))
	thick := max(1, int(math.Ceil(ppd-1e-6))) // the stroke's width
	drop := max(1, int(math.Floor(ppd+1e-6))) // the light stroke's offset below the dark
	dark, light := kit.RGBA(0, 0, 0, 0.6), kit.RGBA(255, 255, 255, 0.6)
	for _, c := range []kit.Color{dark, light} {
		down := 0
		if c == light {
			down = drop
		}
		for _, n := range []int{int(math.Round(6 * ppd)), int(math.Round(2 * ppd))} {
			top := bottom - n - thick + 1
			for i := 0; i < n+thick; i++ {
				x0, x1 := max(right-i, right-n), min(right-i+thick-1, right)
				if x0 > x1 {
					continue
				}
				y := top + i + down
				p.Canvas.FillRect(kit.R(s.FromPixels(float32(x0)), s.FromPixels(float32(y)), s.FromPixels(float32(x1+1)), s.FromPixels(float32(y+1))), c)
			}
		}
	}
}
