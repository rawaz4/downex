package widgets

import (
	"time"

	"downex/ui/kit"
	"downex/ui/kit/icons"
	"downex/ui/kit/tokens"
)

// ToastTone is what a toast reports (toasts.svelte.ts's ToastTone).
type ToastTone uint8

// The tones.
const (
	ToastInfo    ToastTone = iota // a plain fact ("Nothing to pause")
	ToastSuccess                  // something done ("Settings saved"): a Mint check
	ToastError                    // a failure
)

// Toast is one toast.
type Toast struct {
	ID      int
	Message string
	Tone    ToastTone
	// Leaving: fading out, no longer announced or focusable.
	Leaving bool
}

// The toasts' timings (toasts.svelte.ts): 5 s (10 s for an error), at most 4 at once, a
// 140 ms exit.
const (
	ToastLifetime = 5 * time.Second
	ToastMax      = 4
	ToastExit     = 140 * time.Millisecond
)

// Toasts is the short toasts' store (toasts.svelte.ts): Show adds one, which goes by itself
// after its lifetime, fading out for ToastExit (at once under reduced motion); at most ToastMax
// stay, the oldest going first. Its timers are its window's.
type Toasts struct {
	w     *kit.Window
	items []Toast
	next  int
	// OnChange hears every change of the items.
	OnChange func()
}

// NewToasts makes the store of w's toasts.
func NewToasts(w *kit.Window) *Toasts { return &Toasts{w: w, next: 1} }

// Items are the toasts, oldest first (leaving ones included).
func (s *Toasts) Items() []Toast { return s.items }

func (s *Toasts) changed() {
	if s.OnChange != nil {
		s.OnChange()
	}
}

// Show shows a toast of a tone.
func (s *Toasts) Show(message string, tone ToastTone) {
	t := Toast{ID: s.next, Message: message, Tone: tone}
	s.next++
	var kept []Toast
	for _, x := range s.items {
		if !x.Leaving {
			kept = append(kept, x)
		}
	}
	dropped := len(kept) - (ToastMax - 1)
	out := make([]Toast, 0, len(s.items)+1)
	for _, x := range s.items {
		idx := -1
		for i, k := range kept {
			if k.ID == x.ID {
				idx = i
				break
			}
		}
		if x.Leaving || dropped <= 0 || idx >= dropped {
			out = append(out, x)
		}
	}
	s.items = append(out, t)
	life := ToastLifetime
	if tone == ToastError {
		life *= 2
	}
	id := t.ID
	s.w.After(life, func() { s.Dismiss(id) })
	s.changed()
}

// Success shows something that was done.
func (s *Toasts) Success(message string) { s.Show(message, ToastSuccess) }

// Error shows a failure.
func (s *Toasts) Error(message string) { s.Show(message, ToastError) }

// Dismiss lets a toast go: it fades out (at once under reduced motion).
func (s *Toasts) Dismiss(id int) {
	i := s.index(id)
	if i < 0 || s.items[i].Leaving {
		return
	}
	if !s.w.MotionOK() {
		s.items = append(s.items[:i:i], s.items[i+1:]...)
		s.changed()
		return
	}
	s.items[i].Leaving = true
	s.changed()
	s.w.After(ToastExit, func() {
		if j := s.index(id); j >= 0 {
			s.items = append(s.items[:j:j], s.items[j+1:]...)
			s.changed()
		}
	})
}

func (s *Toasts) index(id int) int {
	for i, t := range s.items {
		if t.ID == id {
			return i
		}
	}
	return -1
}

// NoticeAction is one of a notice's buttons.
type NoticeAction struct {
	Label   string
	Primary bool
	Run     func()
}

// Notice is a toast with actions (a download that arrived from the browser, or awaits
// confirmation; Toasts.svelte's Notice).
type Notice struct {
	Key   string
	Title string
	// Detail is the download's name, shown isolated on its own line.
	Detail  string
	Actions []NoticeAction
}

// The notices' timings: 9 s, at most 3.
const (
	NoticeLifetime = 9 * time.Second
	NoticeMax      = 3
)

// Notices are the notices with a lifetime that waits while the pointer or the focus is on
// them (Toasts.svelte's Notices).
type Notices struct {
	w        *kit.Window
	items    []Notice
	leaving  map[string]bool
	timers   map[string]*kit.Timer
	deadline map[string]time.Time
	left     map[string]time.Duration
	OnChange func()
}

// NewNotices makes the store of w's notices.
func NewNotices(w *kit.Window) *Notices {
	return &Notices{w: w, leaving: map[string]bool{}, timers: map[string]*kit.Timer{}, deadline: map[string]time.Time{}, left: map[string]time.Duration{}}
}

// Items are the notices, oldest first (leaving ones included).
func (n *Notices) Items() []Notice { return n.items }

// Leaving reports whether a notice fades out.
func (n *Notices) Leaving(key string) bool { return n.leaving[key] }

// Held reports whether a notice's lifetime waits (the pointer or the focus is on it).
func (n *Notices) Held(key string) bool {
	_, ok := n.left[key]
	return ok
}

// Remaining is how long a notice has left (frozen while held).
func (n *Notices) Remaining(key string) time.Duration {
	if l, ok := n.left[key]; ok {
		return l
	}
	if d, ok := n.deadline[key]; ok {
		return max(d.Sub(n.w.Now()), 0)
	}
	return 0
}

func (n *Notices) changed() {
	if n.OnChange != nil {
		n.OnChange()
	}
}

// Show shows a notice (one with the same key goes first); the oldest live ones beyond
// NoticeMax go at once.
func (n *Notices) Show(no Notice) {
	n.remove(no.Key, false)
	kept := append(append([]Notice{}, n.items...), no)
	var live []Notice
	for _, x := range kept {
		if !n.leaving[x.Key] {
			live = append(live, x)
		}
	}
	drop := map[string]bool{}
	for _, x := range live[:max(0, len(live)-NoticeMax)] {
		drop[x.Key] = true
		n.forget(x.Key)
	}
	out := kept[:0]
	for _, x := range kept {
		if !drop[x.Key] {
			out = append(out, x)
		}
	}
	n.items = out
	n.arm(no.Key, NoticeLifetime)
	n.changed()
}

// Dismiss lets a notice go: it fades out (at once under reduced motion).
func (n *Notices) Dismiss(key string) {
	if n.leaving[key] || n.index(key) < 0 {
		n.forget(key)
		return
	}
	n.forget(key)
	if !n.w.MotionOK() {
		n.remove(key, true)
		return
	}
	n.leaving[key] = true
	n.timers[key] = n.w.After(ToastExit, func() { n.remove(key, true) })
	n.changed()
}

// Hold stops a notice's lifetime (the pointer or the focus came onto it).
func (n *Notices) Hold(key string) {
	t, ok := n.timers[key]
	if !ok || n.leaving[key] {
		return
	}
	t.Stop()
	delete(n.timers, key)
	n.left[key] = max(n.deadline[key].Sub(n.w.Now()), 0)
	n.changed()
}

// Release starts a held notice's lifetime again with the time it had left.
func (n *Notices) Release(key string) {
	left, ok := n.left[key]
	if !ok {
		return
	}
	if _, running := n.timers[key]; running {
		return
	}
	delete(n.left, key)
	n.arm(key, left)
	n.changed()
}

// Clear takes every notice away at once.
func (n *Notices) Clear() {
	for _, t := range n.timers {
		t.Stop()
	}
	n.timers, n.deadline, n.left, n.leaving = map[string]*kit.Timer{}, map[string]time.Time{}, map[string]time.Duration{}, map[string]bool{}
	n.items = nil
	n.changed()
}

func (n *Notices) forget(key string) {
	if t, ok := n.timers[key]; ok {
		t.Stop()
	}
	delete(n.timers, key)
	delete(n.deadline, key)
	delete(n.left, key)
}

func (n *Notices) remove(key string, notify bool) {
	n.forget(key)
	delete(n.leaving, key)
	if i := n.index(key); i >= 0 {
		n.items = append(n.items[:i:i], n.items[i+1:]...)
	}
	if notify {
		n.changed()
	}
}

func (n *Notices) arm(key string, d time.Duration) {
	n.deadline[key] = n.w.Now().Add(d)
	n.timers[key] = n.w.After(d, func() { n.Dismiss(key) })
}

func (n *Notices) index(key string) int {
	for i, x := range n.items {
		if x.Key == key {
			return i
		}
	}
	return -1
}

// ToastLayer shows a window's toasts (Toasts.svelte, DESIGN-SYSTEM.md §3.17): two stacks at
// the bottom right, 16 px above the status bar (StatusBarHeight) and from the end, at most 360
// px wide: the plain toasts, and the notices on top of them. Each toast has a tone icon, its
// text and its actions (primary last); they rise in (240 ms) and fade out (140 ms). The plain
// stack is one layer (Plain), the notices another (Notices: exempt from a dialog's trap, Tab
// reaches them): a dialog's layer goes between them, so plain toasts sit under its scrim while
// notices stay usable above it (Front raises the notices again).
type ToastLayer struct {
	toasts  *Toasts
	notices *Notices
	plain   *toastStack
	notes   *toastStack
	// StatusBarHeight is --statusbar-h (32 px).
	StatusBarHeight kit.DIP
}

// NewToastLayer shows toasts and notices (either may be nil).
func NewToastLayer(toasts *Toasts, notices *Notices) *ToastLayer {
	l := &ToastLayer{toasts: toasts, notices: notices, StatusBarHeight: 32}
	l.plain = &toastStack{l: l}
	l.notes = &toastStack{l: l, notices: true}
	for _, s := range []*toastStack{l.plain, l.notes} {
		s.PassThrough = true
		s.Semantics.Role = kit.RoleStatus
		s.Semantics.Live = kit.LivePolite
	}
	l.notes.Exempt = kit.ExemptTab
	if toasts != nil {
		prev := toasts.OnChange
		toasts.OnChange = func() {
			if prev != nil {
				prev()
			}
			l.plain.sync()
			l.notes.Relayout()
		}
	}
	if notices != nil {
		prev := notices.OnChange
		notices.OnChange = func() {
			if prev != nil {
				prev()
			}
			l.notes.sync()
		}
	}
	return l
}

// Plain is the plain toasts' layer and Notices the notices'; push Plain, then Notices.
func (l *ToastLayer) Plain() kit.Widget   { return l.plain }
func (l *ToastLayer) Notices() kit.Widget { return l.notes }

// Push puts both layers in w, the notices on top.
func (l *ToastLayer) Push(w *kit.Window) {
	w.PushLayer(l.plain)
	w.PushLayer(l.notes)
}

// Front puts the notices' layer above every other layer again (after a dialog opened).
func (l *ToastLayer) Front(w *kit.Window) {
	w.RemoveLayer(l.notes)
	w.PushLayer(l.notes)
}

// toastStack is one stack's layer: the toasts placed from the bottom up.
type toastStack struct {
	kit.Element
	l       *ToastLayer
	notices bool
	views   map[string]*toastView
	order   []*toastView
}

func (s *toastStack) key(t Toast) string { return "t" + itoa(t.ID) }

// sync makes a view for each item, lets the gone ones go, and announces the new ones.
func (s *toastStack) sync() {
	if s.views == nil {
		s.views = map[string]*toastView{}
	}
	seen := map[string]bool{}
	var order []*toastView
	add := func(k string, mk func() *toastView, leaving bool) {
		seen[k] = true
		v, ok := s.views[k]
		if !ok {
			v = mk()
			s.views[k] = v
			s.Add(v)
			if w := s.Window(); w != nil && !leaving {
				w.Announce(v.announce, kit.LivePolite, v)
			}
		}
		v.setLeaving(leaving)
		order = append(order, v)
	}
	if s.notices && s.l.notices != nil {
		for _, n := range s.l.notices.Items() {
			n := n
			add("n"+n.Key, func() *toastView { return newNoticeView(s.l, n) }, s.l.notices.Leaving(n.Key))
		}
	}
	if !s.notices && s.l.toasts != nil {
		for _, t := range s.l.toasts.Items() {
			t := t
			add(s.key(t), func() *toastView { return newToastView(s.l, t) }, t.Leaving)
		}
	}
	for k, v := range s.views {
		if !seen[k] {
			s.Remove(v)
			delete(s.views, k)
		}
	}
	s.order = order
	s.Relayout()
}

// height is the stack's height in its last layout.
func (s *toastStack) height() kit.DIP {
	var h kit.DIP
	for i, v := range s.order {
		if i > 0 {
			h += 8
		}
		h += v.Size().H
	}
	return h
}

// ContentWidths and ContentHeight are kit.Layouter's: a layer takes the window.
func (s *toastStack) ContentWidths(*kit.LayoutContext) (min, max kit.DIP) { return 0, 0 }
func (s *toastStack) ContentHeight(*kit.LayoutContext, kit.DIP) kit.DIP   { return 0 }

// LayoutContent is kit.Layouter's: the toasts from the bottom up, 8 px apart, at the end.
func (s *toastStack) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	w := min(360, box.Width()-32)
	bottom := box.Max.Y - s.l.StatusBarHeight - 16
	if s.notices {
		if ph := s.l.plain.height(); ph > 0 {
			bottom -= ph + 8
		}
	}
	x := box.Max.X - 16 - w
	if ctx.RTL {
		x = box.Min.X + 16
	}
	hs := make([]kit.DIP, len(s.order))
	var total kit.DIP
	for i, v := range s.order {
		hs[i] = ctx.HeightFor(v, w, w)
		total += hs[i]
		if i > 0 {
			total += 8
		}
	}
	y := bottom - total
	for i, v := range s.order {
		ctx.Place(v, kit.R(x, y, x+w, y+hs[i]), w)
		y += hs[i] + 8
	}
}

// toastView is one toast or notice.
type toastView struct {
	kit.Element
	l        *ToastLayer
	notice   string // the notice's key ("" for a plain toast)
	tone     ToastTone
	announce string
	leaving  bool
	rise     float32 // 0 entering .. 1 in place; then 1 .. 0 while leaving
	tween    *kit.Tween
	pushed   bool
	timer    *timerAnim
	msg      *kit.Label
	title    *kit.Label
	detail   *kit.Label
	icon     *Icon
}

func newToastView(l *ToastLayer, t Toast) *toastView {
	v := &toastView{l: l, tone: t.Tone, announce: t.Message}
	v.Style = kit.Style{
		Display: kit.DisplayFlex, AlignItems: kit.AlignStart, ColumnGap: kit.Px(10),
		Padding: kit.Edges{Top: kit.Px(10), End: kit.Px(6), Bottom: kit.Px(10), Start: kit.Px(12)},
		Border:  kit.EdgesAll(kit.Px(1)), Radius: kit.CornersAll(kit.Px(11)), Overflow: kit.OverflowHidden,
	}
	name := map[ToastTone]icons.Name{ToastSuccess: icons.Check, ToastInfo: icons.Info, ToastError: icons.Alert}[t.Tone]
	tone := v.toneCircle(name)
	tone.Style.Margin = kit.Edges{Top: kit.Px(1)}
	f := FontBody
	f.LineHeight = 1.35
	v.msg = kit.NewLabel(t.Message, f, kit.TextOptions{Wrap: kit.TextWrapAnywhere})
	v.msg.Style.Flex = kit.Flex1
	v.msg.Style.MinWidth = kit.Px(0)
	v.msg.Style.Padding = kit.EdgesBlock(kit.Px(3))
	dismiss := NewIconButton("close", 14, "Dismiss", true, func() {
		if l.toasts != nil {
			l.toasts.Dismiss(t.ID)
		}
	})
	dismiss.Tooltip = "Dismiss"
	dismiss.faint = true
	v.Add(tone, v.msg, dismiss)
	return v
}

func newNoticeView(l *ToastLayer, n Notice) *toastView {
	v := &toastView{l: l, notice: n.Key, tone: ToastSuccess, announce: n.Title}
	v.Semantics.Role = kit.RoleGroup
	v.Semantics.Name = n.Title
	v.Style = kit.Style{
		Display: kit.DisplayFlex, AlignItems: kit.AlignStart, ColumnGap: kit.Px(10), Padding: kit.EdgesAll(kit.Px(12)),
		Border: kit.EdgesAll(kit.Px(1)), Radius: kit.CornersAll(kit.Px(11)), Overflow: kit.OverflowHidden,
	}
	tone := v.toneCircle(icons.ArrowDown)
	body := box(kit.Style{Display: kit.DisplayBlock, RowGap: kit.Px(2), Flex: kit.Flex1, MinWidth: kit.Px(0)})
	tf := font(tokens.TypeBody, 600)
	tf.LineHeight = 22.0 / 13
	v.title = kit.NewLabel(n.Title, tf, kit.TextOptions{Wrap: kit.TextWrapWords})
	v.title.Semantics.Role = kit.RoleNone
	body.Add(v.title)
	if n.Detail != "" {
		v.detail = kit.NewFileName(n.Detail, font(tokens.TypeSmall, 400), kit.TextOptions{Truncate: kit.TruncateEnd})
		v.detail.Style.MinWidth = kit.Px(0)
		body.Add(v.detail)
	}
	actions := box(kit.Style{Display: kit.DisplayFlex, Justify: kit.JustifyEnd, ColumnGap: kit.Px(6), Margin: kit.Edges{Top: kit.Px(8)}})
	for _, a := range n.Actions {
		a := a
		variant := Secondary
		if a.Primary {
			variant = Primary
		}
		b := NewButton(a.Label, "", variant, SizeSm, func() {
			if l.notices != nil {
				l.notices.Dismiss(n.Key)
			}
			if a.Run != nil {
				a.Run()
			}
		})
		b.onFocus = func(bool) {
			if w := v.Window(); w != nil {
				w.Defer(func() { v.hold(v.Hovered() || v.focusWithin()) })
			}
		}
		actions.Add(b)
	}
	body.Add(actions)
	v.Add(tone, body)
	return v
}

// toneCircle is the 22 px tone circle with its 15 px icon.
func (v *toastView) toneCircle(name icons.Name) *toneDot {
	c := &toneDot{v: v}
	c.Style = kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, Justify: kit.JustifyCenter, Flex: kit.FlexNone, Width: kit.Px(22), Height: kit.Px(22)}
	c.Semantics.Hidden = true
	v.icon = NewIcon(name, 15, func(t *tokens.Theme) kit.Color {
		_, ink := v.toneColors(t)
		return ink
	})
	c.Add(v.icon)
	return c
}

func (v *toastView) toneColors(t *tokens.Theme) (bg, ink kit.Color) {
	switch {
	case v.notice != "":
		return t.AccentSoft, t.AccentText
	case v.tone == ToastInfo:
		return t.InfoSoft, t.Info
	case v.tone == ToastError:
		return t.DangerSoft, t.Danger
	}
	return t.AccentSoft, t.AccentText
}

// toneDot is the tone's circle.
type toneDot struct {
	kit.Element
	v *toastView
}

// Paint is kit.Painter's.
func (d *toneDot) Paint(p *kit.PaintContext) {
	bg, _ := d.v.toneColors(tokensOf(&d.Element))
	p.Canvas.FillEllipse(p.Scale.SnapRect(kit.RectAt(p.Origin, p.Size())), bg)
}

// Attached is kit.Attacher's: the rise (opacity 0 and 8 px lower, 240 ms, --ease-out).
func (v *toastView) Attached() {
	w := v.Window()
	w.Defer(v.syncTimer)
	v.rise = 1
	if !w.MotionOK() {
		return
	}
	v.rise = 0
	v.tween = &kit.Tween{From: 0, To: 1, Duration: tokens.Dur4, Ease: tokens.EaseOut.Easing(), Set: v.setRise}
	w.Animate(v.tween)
}

func (v *toastView) setRise(r float32) {
	v.rise = r
	if w := v.Window(); w != nil {
		w.InvalidateRect(v.WindowBounds().Inset(kit.UniformInsets(-90)))
	}
}

// setLeaving fades the toast out (140 ms, --ease-in): hidden from assistive technology, inert.
func (v *toastView) setLeaving(leaving bool) {
	if leaving == v.leaving {
		return
	}
	v.leaving = leaving
	v.Inert = leaving
	v.Semantics.Hidden = leaving
	v.SemanticsChanged()
	w := v.Window()
	if !leaving || w == nil {
		return
	}
	if v.tween != nil {
		w.StopAnimation(v.tween)
	}
	v.tween = &kit.Tween{From: 1, To: 0, Duration: tokens.DurExit, Ease: tokens.EaseIn.Easing(), Set: v.setRise}
	w.Animate(v.tween)
}

// Paint is kit.Painter's: the motion (opacity, and 8 px in or 4 px out), the shadow, ground
// and border (--danger-line for an error).
func (v *toastView) Paint(p *kit.PaintContext) {
	t := tokensOf(&v.Element)
	v.pushed = v.rise < 1
	if v.pushed {
		dy := 8 * (1 - v.rise)
		if v.leaving {
			dy = 4 * (1 - v.rise)
		}
		p.Canvas.PushOpacity(v.rise)
		p.Canvas.PushTransform(kit.Translation(0, dy))
	}
	r := snapBox(p, 11)
	drawShadows(p.Canvas, r, t.ShadowLg)
	p.Canvas.FillRoundedRect(r, t.Raised)
	border := t.Line
	switch {
	case t.IsForced():
		border = t.Ink
	case v.notice == "" && v.tone == ToastError:
		border = t.DangerLine
	}
	strokeInside(p.Canvas, r, p.Scale.SnapWidth(1), border)
	if v.msg != nil {
		v.msg.SetColor(t.Ink)
	}
	if v.title != nil {
		v.title.SetColor(t.Ink)
	}
	if v.detail != nil {
		v.detail.SetColor(t.Muted)
	}
}

// PaintOver is kit.OverPainter's: the notice's timer bar, then the end of the motion.
func (v *toastView) PaintOver(p *kit.PaintContext) {
	v.paintTimer(p)
	if v.pushed {
		p.Canvas.Pop()
		p.Canvas.Pop()
		v.pushed = false
	}
}

// HoverChanged is kit.HoverHandler's: a notice waits while the pointer is on it.
func (v *toastView) HoverChanged(h bool) { v.hold(h || v.focusWithin()) }

func (v *toastView) focusWithin() bool {
	w := v.Window()
	return w != nil && w.Focused() != nil && v.Contains(w.Focused())
}

func (v *toastView) hold(on bool) {
	if v.notice == "" || v.l.notices == nil {
		return
	}
	if on {
		v.l.notices.Hold(v.notice)
	} else {
		v.l.notices.Release(v.notice)
	}
	v.syncTimer()
}

// HandlePointer is kit.PointerHandler's: a toast takes the pointer (the layer does not).
func (v *toastView) HandlePointer(in *kit.PointerInput) bool {
	return in.Action == kit.PointerDown
}

// timerAnim repaints a notice's timer bar while its lifetime runs.
type timerAnim struct {
	v    *toastView
	dead bool
}

// Step is kit.Animation's.
func (a *timerAnim) Step(time.Time) bool {
	if a.dead {
		return false
	}
	if w := a.v.Window(); w != nil {
		b := a.v.WindowBounds()
		w.InvalidateRect(kit.R(b.Min.X, b.Min.Y, b.Max.X, b.Min.Y+3))
	}
	return true
}

// Finish is kit.Animation's.
func (a *timerAnim) Finish() { a.dead = true }

// syncTimer runs the timer bar's repaints while the notice's lifetime runs (not while held,
// leaving, or under reduced motion).
func (v *toastView) syncTimer() {
	w := v.Window()
	if w == nil || v.notice == "" {
		return
	}
	n := v.l.notices
	running := n != nil && !n.Held(v.notice) && !n.Leaving(v.notice) && w.MotionOK()
	if running && (v.timer == nil || v.timer.dead) {
		v.timer = &timerAnim{v: v}
		w.Animate(v.timer)
	} else if !running && v.timer != nil {
		v.timer.dead = true
		v.timer = nil
	}
	v.Invalidate()
}

// Detached is kit.Detacher's.
func (v *toastView) Detached() {
	if v.timer != nil {
		v.timer.dead = true
		v.timer = nil
	}
}

// paintTimer draws a notice's time left: a 2 px --accent bar at its top that shrinks to its
// start over 9 s (hidden under reduced motion).
func (v *toastView) paintTimer(p *kit.PaintContext) {
	w := v.Window()
	n := v.l.notices
	if v.notice == "" || w == nil || n == nil || !w.MotionOK() {
		return
	}
	share := float32(n.Remaining(v.notice)) / float32(NoticeLifetime)
	if share <= 0 {
		return
	}
	t := tokensOf(&v.Element)
	sz := p.Size()
	width := sz.W * share
	x := kit.DIP(0)
	if isRTL(w) {
		x = sz.W - width
	}
	p.FillRect(kit.R(x, 0, x+width, 2), t.Accent)
}
