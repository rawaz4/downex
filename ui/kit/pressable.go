package kit

// Pressable is the behaviour of a button, as HTML's <button> has it: a primary press and
// release on it (the release inside) presses it, Enter presses it when the key goes down
// (again while it repeats), Space when the key comes up after going down on it, and
// assistive technology's Invoke presses it. A disabled one does nothing. Widgets that look
// like buttons embed it and paint from Hovered, Down and the window's focus state; one that
// embeds it sets what NewPressable sets: OnPress, Focus = FocusTab and Semantics.Role =
// RoleButton.
type Pressable struct {
	Element
	// OnPress is what the button does.
	OnPress func()

	down      bool // pressed by the pointer, which is still over it
	pointer   bool // a pointer press is in progress (captured)
	spaceDown bool
}

// NewPressable makes a button with role RoleButton that Tab reaches.
func NewPressable(onPress func()) *Pressable {
	p := &Pressable{OnPress: onPress}
	p.self = p
	p.Focus = FocusTab
	p.Semantics.Role = RoleButton
	return p
}

// Down reports whether the button shows as pressed (CSS :active).
func (p *Pressable) Down() bool { return p.down || p.spaceDown }

func (p *Pressable) setDown(v bool) {
	if p.down != v {
		p.down = v
		p.Invalidate()
	}
}

// Press does what the button does, unless it is disabled.
func (p *Pressable) Press() {
	if !p.enabled() || p.OnPress == nil {
		return
	}
	p.OnPress()
}

// HandlePointer is PointerHandler's.
func (p *Pressable) HandlePointer(in *PointerInput) bool {
	inside := RectAt(Point{}, p.bounds.Size()).Contains(in.Local)
	switch in.Action {
	case PointerDown:
		if in.Button != ButtonLeft {
			return false
		}
		p.pointer = true
		p.setDown(true)
		if p.win != nil {
			p.win.Capture(&p.Element)
		}
		return true
	case PointerMove:
		if p.pointer {
			p.setDown(inside)
			return true
		}
	case PointerUp:
		if !p.pointer || in.Button != ButtonLeft {
			return false
		}
		p.pointer = false
		p.setDown(false)
		if inside {
			p.Press()
		}
		return true
	case PointerCancel:
		p.pointer = false
		p.setDown(false)
	}
	return false
}

// HandleKey is KeyHandler's.
func (p *Pressable) HandleKey(e KeyEvent) bool {
	if e.Mods&(ModCtrl|ModAlt|ModMeta) != 0 {
		return false
	}
	switch {
	case e.Key == KeyEnter && e.Action == KeyDown:
		p.Press()
		return true
	case e.Key == KeySpace && e.Action == KeyDown:
		if !e.Repeat {
			p.spaceDown = true
			p.Invalidate()
		}
		return true
	case e.Key == KeySpace && e.Action == KeyUp && p.spaceDown:
		p.spaceDown = false
		p.Invalidate()
		p.Press()
		return true
	}
	return false
}

// FocusChanged is FocusHandler's: a Space held while the focus leaves does not press.
func (p *Pressable) FocusChanged(focused bool) {
	if !focused && p.spaceDown {
		p.spaceDown = false
		p.Invalidate()
	}
}

// HoverChanged is HoverHandler's: the hover look repaints.
func (p *Pressable) HoverChanged(bool) { p.Invalidate() }

// FillSemantics is SemanticFiller's: a button can be invoked.
func (p *Pressable) FillSemantics(n *SemanticNode) { n.Actions |= ActionInvoke }

// PerformAction is ActionHandler's: Invoke presses the button.
func (p *Pressable) PerformAction(r ActionRequest) error {
	if r.Action == ActionInvoke {
		p.Press()
		return nil
	}
	return ErrUnsupported
}
