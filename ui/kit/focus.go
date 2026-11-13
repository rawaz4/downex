package kit

// The focus model follows the WebView window's: HTML's focus rules, :focus-visible (rings
// after keyboard use only) and lib/actions/focus-trap.ts for modals.

// Focused is the element with the keyboard focus, or nil.
func (w *Window) Focused() *Element { return w.focused }

// FocusVisible reports whether the focus came from the keyboard (CSS :focus-visible).
func (w *Window) FocusVisible() bool { return w.focusVisible }

// SetFocusVisible says whether the focus counts as coming from the keyboard, for a widget
// that moves it in answer to a key.
func (w *Window) SetFocusVisible(v bool) {
	if w.focusVisible != v {
		w.focusVisible = v
		if w.focused != nil {
			w.invalidateElement(w.focused)
		}
	}
}

// Focus moves the keyboard focus to wd's element and reports whether it could take it.
// While a focus trap is on, focus outside its node and its exempt regions goes to the
// trap's first control instead, as focus-trap.ts's focusin handler sends it.
func (w *Window) Focus(wd Widget) bool {
	if wd == nil {
		return false
	}
	e := wd.Base()
	if e.win != w || !e.focusable() {
		return false
	}
	w.setFocus(e)
	return true
}

// Blur takes the focus from whatever has it (HTML's focus on the document's body).
func (w *Window) Blur() { w.setFocus(nil) }

func (w *Window) setFocus(e *Element) {
	if t := w.topTrap(); t != nil && e != nil && !t.node.contains(e) && !t.inExempt(e) {
		e = t.firstOrNode()
	}
	if e == w.focused {
		return
	}
	old := w.focused
	w.focused = e
	if old != nil {
		w.invalidateElement(old)
		if h, ok := old.self.(FocusHandler); ok {
			h.FocusChanged(false)
		}
		w.semNode(old)
	}
	if e != nil {
		w.invalidateElement(e)
		if h, ok := e.self.(FocusHandler); ok {
			h.FocusChanged(true)
		}
		w.semNode(e)
	}
	w.semFocus()
	w.updateTextInput()
}

// TextInputTarget is a focused widget that takes typed text (a field drawn by the kit):
// the window tells the system's input methods where its caret is.
type TextInputTarget interface {
	TextInput() TextInput
}

func (w *Window) updateTextInput() {
	if w.host == nil {
		return
	}
	var ti TextInput
	if w.focused != nil {
		if t, ok := w.focused.self.(TextInputTarget); ok {
			ti = t.TextInput()
		}
	}
	if ti != w.textInput {
		w.textInput = ti
		w.host.SetTextInput(ti)
	}
}

// UpdateTextInput tells the input methods again where the focused field's caret is.
func (w *Window) UpdateTextInput() { w.updateTextInput() }

// focusLost is called when the focused element can no longer have the focus (it was
// hidden): the focus goes to the document, as HTML's focus fixup rule does.
func (w *Window) focusLost() {
	if w.focused != nil && !w.focused.focusable() {
		w.setFocus(nil)
	}
}

// focusOnPress is a mouse press's default action: the focus goes to the nearest element
// under the pointer that can take it, else to the document.
func (w *Window) focusOnPress(target *Element) {
	for e := target; e != nil; e = e.parent {
		if e.focusable() {
			w.setFocus(e)
			return
		}
	}
	w.setFocus(nil)
}

// walkTabbable calls f for each element Tab reaches inside e, in tree order.
func walkTabbable(e *Element, f func(*Element)) {
	if e.hidden() || e.flags&isParked != 0 || e.Inert {
		return
	}
	if e.tabbable() {
		f(e)
	}
	for _, c := range e.kids {
		walkTabbable(c, f)
	}
}

// TabOrder is the elements Tab reaches in the window, in order.
func (w *Window) TabOrder() []*Element {
	var out []*Element
	walkTabbable(w.root, func(e *Element) { out = append(out, e) })
	return out
}

// tab moves the focus to the next (or previous) element in tab order, wrapping around.
func (w *Window) tab(back bool) {
	w.focusVisible = true
	order := w.TabOrder()
	if len(order) == 0 {
		return
	}
	// The position of the focus in tree order, also when it is not in the tab order
	// itself (an element focused by a click with FocusClick).
	pos := -1
	if w.focused != nil {
		seq := 0
		var walk func(*Element) bool
		walk = func(e *Element) bool {
			if e == w.focused {
				pos = seq
				return true
			}
			if !e.hidden() && e.flags&isParked == 0 && !e.Inert && e.tabbable() {
				seq++
			}
			for _, c := range e.kids {
				if walk(c) {
					return true
				}
			}
			return false
		}
		walk(w.root)
	}
	var next *Element
	switch {
	case pos < 0 && back:
		next = order[len(order)-1]
	case pos < 0:
		next = order[0]
	case back:
		// pos counts the tabbable elements before the focus; the focused one, when
		// tabbable, is order[pos].
		i := pos - 1
		if i < 0 {
			i = len(order) - 1
		}
		next = order[i]
	default:
		i := pos
		if pos < len(order) && order[pos] == w.focused {
			i = pos + 1
		}
		if i >= len(order) {
			i = 0
		}
		next = order[i]
	}
	w.setFocus(next)
}

// ---- focus traps -------------------------------------------------------------------------

// TrapOptions configure a focus trap (focus-trap.ts's TrapOptions).
type TrapOptions struct {
	// OnEscape is called on Escape inside the trap.
	OnEscape func()
	// Fallback is where the focus goes when the trap is released and the element that had
	// the focus before it can no longer take it.
	Fallback func() Widget
	// FocusSelf focuses the trap's node itself at first (it needs a Focus mode), not its
	// first control; focus already in an ExemptKeep region stays there.
	FocusSelf bool
}

// FocusTrap keeps the keyboard inside a modal node (a dialog, the palette): Tab and
// Shift+Tab cycle through its controls and the exempt regions outside it, focus that
// would leave it comes back, and Release gives the focus back to where it was.
type FocusTrap struct {
	w        *Window
	node     *Element
	opts     TrapOptions
	previous *Element
	released bool
}

// Trap starts a focus trap on node and moves the focus into it.
func (w *Window) Trap(node Widget, opts TrapOptions) *FocusTrap {
	t := &FocusTrap{w: w, node: node.Base(), opts: opts, previous: w.focused}
	region := t.exemptRegion(t.previous)
	keeps := region != nil && region.Exempt == ExemptKeep
	w.traps = append(w.traps, t)
	if !(opts.FocusSelf && keeps) {
		var initial *Element
		if opts.FocusSelf {
			initial = t.node
		} else {
			initial = t.autofocus()
			if initial == nil {
				initial = t.firstOrNode()
			}
		}
		w.focusVisibleKeep(initial)
	}
	return t
}

// focusVisibleKeep focuses e (when it can take the focus) without changing the modality.
func (w *Window) focusVisibleKeep(e *Element) {
	if e != nil && e.focusable() {
		w.setFocus(e)
	}
}

// Update replaces the trap's options.
func (t *FocusTrap) Update(opts TrapOptions) { t.opts = opts }

// Node is the trap's node.
func (t *FocusTrap) Node() *Element { return t.node }

// Release ends the trap. Once the call in progress has finished (so that the page has
// updated: an opener that became disabled is seen as such), the focus goes back to the
// element that had it when the trap started if it can still take it, else to Fallback.
func (t *FocusTrap) Release() {
	if t.released {
		return
	}
	t.released = true
	w := t.w
	for i, x := range w.traps {
		if x == t {
			w.traps = append(w.traps[:i], w.traps[i+1:]...)
			break
		}
	}
	fallback := t.opts.Fallback
	previous := t.previous
	w.Defer(func() {
		if w.closed {
			return
		}
		if w.CanRestoreFocus(previous) {
			w.setFocus(previous)
			return
		}
		if fallback != nil {
			if f := fallback(); f != nil {
				w.Focus(f)
			}
		}
	})
}

// CanRestoreFocus reports whether the focus can go back to e: it is still in this window,
// not disabled, not inside something inert, hidden or hidden from assistive technology,
// and it can take the focus (focus-trap.ts's canRestoreFocus).
func (w *Window) CanRestoreFocus(e *Element) bool {
	if e == nil || e.win != w || (e.Disabled && !e.DisabledFocusable) {
		return false
	}
	for p := e; p != nil; p = p.parent {
		if p.Inert || p.hidden() || p.flags&isParked != 0 || p.Semantics.Hidden {
			return false
		}
	}
	return e.focusable()
}

func (w *Window) topTrap() *FocusTrap {
	if len(w.traps) == 0 {
		return nil
	}
	return w.traps[len(w.traps)-1]
}

// items are the controls Tab reaches inside the node.
func (t *FocusTrap) items() []*Element {
	var out []*Element
	for _, c := range t.node.kids {
		walkTabbable(c, func(e *Element) { out = append(out, e) })
	}
	return out
}

func (t *FocusTrap) firstOrNode() *Element {
	if it := t.items(); len(it) > 0 {
		return it[0]
	}
	return t.node
}

// autofocus is the first enabled element inside the node marked Autofocus.
func (t *FocusTrap) autofocus() *Element {
	var found *Element
	var walk func(*Element)
	walk = func(e *Element) {
		if found != nil || e.hidden() || e.Inert {
			return
		}
		if e.Autofocus && !e.Disabled && e.focusable() {
			found = e
			return
		}
		for _, c := range e.kids {
			walk(c)
		}
	}
	walk(t.node)
	return found
}

// exemptRegion is the exempt region outside the node that e lies in, or nil.
func (t *FocusTrap) exemptRegion(e *Element) *Element {
	if e == nil || t.node.contains(e) {
		return nil
	}
	for p := e; p != nil; p = p.parent {
		if p.Exempt != ExemptNone {
			for q := p; q != nil; q = q.parent {
				if q.Inert || q.hidden() {
					return nil
				}
			}
			return p
		}
	}
	return nil
}

func (t *FocusTrap) inExempt(e *Element) bool { return t.exemptRegion(e) != nil }

// exemptItems are the controls Tab reaches in the exempt regions outside the node, in
// tree order.
func (t *FocusTrap) exemptItems() []*Element {
	var out []*Element
	var walk func(*Element)
	walk = func(e *Element) {
		if e == t.node || e.hidden() || e.flags&isParked != 0 || e.Inert {
			return
		}
		if e.Exempt != ExemptNone {
			walkTabbable(e, func(x *Element) {
				if !t.node.contains(x) {
					out = append(out, x)
				}
			})
			return
		}
		for _, c := range e.kids {
			walk(c)
		}
	}
	walk(t.w.root)
	return out
}

// keyInside handles a key pressed with the focus inside the node: Escape, and Tab at
// either end of the node's controls.
func (t *FocusTrap) keyInside(ev KeyEvent) bool {
	w := t.w
	if ev.Key == KeyEscape && t.opts.OnEscape != nil {
		t.opts.OnEscape()
		return true
	}
	if ev.Key != KeyTab || ev.Mods&(ModCtrl|ModAlt|ModMeta) != 0 {
		return false
	}
	w.focusVisible = true
	items := t.items()
	exempt := t.exemptItems()
	if len(items) == 0 {
		if len(exempt) > 0 {
			w.setFocus(exempt[0])
		} else {
			w.setFocus(t.node)
		}
		return true
	}
	first, last := items[0], items[len(items)-1]
	active := w.focused
	shift := ev.Mods&ModShift != 0
	switch {
	case shift && (active == first || active == t.node || !t.node.contains(active)):
		if len(exempt) > 0 {
			w.setFocus(exempt[len(exempt)-1])
		} else {
			w.setFocus(last)
		}
		return true
	case !shift && (active == last || !t.node.contains(active)):
		if len(exempt) > 0 {
			w.setFocus(exempt[0])
		} else {
			w.setFocus(first)
		}
		return true
	}
	return false
}

// keyOutside handles Tab with the focus in an exempt region: from its last control to the
// node's first, and back.
func (t *FocusTrap) keyOutside(ev KeyEvent) bool {
	w := t.w
	if ev.Key != KeyTab || ev.Mods&(ModCtrl|ModAlt|ModMeta) != 0 || !t.inExempt(w.focused) {
		return false
	}
	exempt := t.exemptItems()
	items := t.items()
	if len(exempt) == 0 {
		return false
	}
	shift := ev.Mods&ModShift != 0
	switch {
	case shift && w.focused == exempt[0]:
		w.focusVisible = true
		if len(items) > 0 {
			w.setFocus(items[len(items)-1])
		} else {
			w.setFocus(t.node)
		}
		return true
	case !shift && w.focused == exempt[len(exempt)-1]:
		w.focusVisible = true
		if len(items) > 0 {
			w.setFocus(items[0])
		} else {
			w.setFocus(t.node)
		}
		return true
	}
	return false
}
