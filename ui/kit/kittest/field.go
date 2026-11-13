package kittest

import "downex/ui/kit"

// Field is a fake native text field (kit.NativeFields): it records what the root asks of
// it, and Type and Click stand in for the user.
type Field struct {
	Opts     kit.TextFieldOptions
	Bounds   kit.Rect
	Value    string
	HasFocus bool
	Selected bool // SelectAll was called since the last change
	Released bool
	// SetBoundsCalls counts the moves.
	SetBoundsCalls int

	cb   kit.TextFieldCallbacks
	host *Host
}

var (
	_ kit.NativeFields = (*Host)(nil)
	_ kit.NativeField  = (*Field)(nil)
)

// NewTextField is kit.NativeFields'.
func (h *Host) NewTextField(o kit.TextFieldOptions, cb kit.TextFieldCallbacks) (kit.NativeField, error) {
	if h.IsClosed() {
		return nil, kit.ErrClosed
	}
	f := &Field{Opts: o, cb: cb, host: h}
	h.Fields = append(h.Fields, f)
	return f, nil
}

func (f *Field) SetBounds(r kit.Rect) {
	f.Bounds = r
	f.SetBoundsCalls++
}
func (f *Field) SetOptions(o kit.TextFieldOptions) { f.Opts = o }
func (f *Field) Text() string                      { return f.Value }
func (f *Field) SetText(s string)                  { f.Value, f.Selected = s, false }
func (f *Field) Focus()                            { f.HasFocus = true }
func (f *Field) Blur()                             { f.HasFocus = false }
func (f *Field) SelectAll()                        { f.Selected = true }
func (f *Field) Release()                          { f.Released = true }

// Shown reports whether the field is placed on screen.
func (f *Field) Shown() bool { return !f.Released && !f.Bounds.Empty() }

// Type stands in for the user typing s at the end of the text (or over it, when it was
// all selected): the field tells the root through its Changed callback.
func (f *Field) Type(s string) {
	if f.Selected {
		f.Value = ""
	}
	f.Value += s
	f.Selected = false
	if f.cb.Changed != nil {
		f.cb.Changed(f.Value)
	}
	f.host.RunPosted()
}

// Click stands in for a click into the field: it takes the system's focus and tells the
// root.
func (f *Field) Click() {
	f.HasFocus = true
	if f.cb.Focused != nil {
		f.cb.Focused(true)
	}
	f.host.RunPosted()
}

// LoseFocus stands in for the system moving its focus elsewhere (another window).
func (f *Field) LoseFocus() {
	f.HasFocus = false
	if f.cb.Focused != nil {
		f.cb.Focused(false)
	}
}
