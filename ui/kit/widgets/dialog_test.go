package widgets_test

import (
	"testing"
	"time"

	"downex/ui/kit"
	"downex/ui/kit/widgets"
)

// The focus-trap.test.ts cases, through the Dialog (the trap in use), and the Dialog's own.

type trapRig struct {
	*rig
	opener *widgets.Button
	grid   *kit.Element
	d      *widgets.Dialog
	cancel *widgets.Button
	ok     *widgets.Button
}

func setupTrap(t *testing.T, o widgets.DialogOptions) *trapRig {
	t.Helper()
	tr := &trapRig{}
	tr.opener = widgets.NewButton("Remove", "", widgets.Secondary, widgets.SizeMd, nil)
	tr.grid = &kit.Element{Focus: kit.FocusTab, Style: kit.Style{Height: kit.Px(20)}}
	tr.grid.Semantics.Role = kit.RoleGrid
	tr.rig = newRig(t, kit.Size{}, tr.opener, tr.grid)
	tr.win.Focus(tr.opener)
	if o.Fallback == nil {
		o.Fallback = func() kit.Widget { return tr.grid }
	}
	if o.Title == "" {
		o.Title = "Remove download?"
	}
	tr.d = widgets.NewDialog(o)
	tr.cancel = widgets.NewButton("Cancel", "", widgets.Secondary, widgets.SizeMd, nil)
	tr.ok = widgets.NewButton("OK", "", widgets.Primary, widgets.SizeMd, nil)
	tr.d.SetFooter(nil, tr.cancel, tr.ok)
	tr.d.CloseButton().Focus = kit.FocusNone // the trap tests' dialog holds Cancel and OK only
	tr.d.Show(tr.win)
	tr.settle()
	return tr
}

func (tr *trapRig) focused() *kit.Element { return tr.win.Focused() }

func TestDialogFocusTrapReturnsFocus(t *testing.T) {
	t.Run("returns focus to the opener when it can still take it", func(t *testing.T) {
		tr := setupTrap(t, widgets.DialogOptions{})
		if !tr.d.Card().Contains(tr.focused()) {
			t.Fatal("the focus is not in the dialog")
		}
		tr.d.Remove()
		tr.settle()
		if tr.focused() != tr.opener.Base() {
			t.Errorf("focus on %v", tr.focused())
		}
	})
	t.Run("falls back when the opener became disabled while the dialog was open", func(t *testing.T) {
		tr := setupTrap(t, widgets.DialogOptions{})
		tr.opener.SetDisabled(true, false)
		tr.d.Remove()
		tr.settle()
		if tr.focused() != tr.grid {
			t.Errorf("focus on %v", tr.focused())
		}
	})
	t.Run("falls back when the opener is inert, hidden or gone", func(t *testing.T) {
		for i, spoil := range []func(tr *trapRig){
			func(tr *trapRig) { tr.opener.Inert = true },
			func(tr *trapRig) { tr.opener.SetHidden(true) },
			func(tr *trapRig) { tr.root.Remove(tr.opener) },
		} {
			tr := setupTrap(t, widgets.DialogOptions{})
			spoil(tr)
			tr.d.Remove()
			tr.settle()
			if tr.focused() != tr.grid {
				t.Errorf("case %d: focus on %v", i, tr.focused())
			}
		}
	})
	t.Run("decides after the page updated, not when the dialog starts closing", func(t *testing.T) {
		tr := setupTrap(t, widgets.DialogOptions{})
		_ = tr.win.Post(func() {
			tr.d.Remove()
			tr.opener.SetDisabled(true, false) // the selection emptied in the same update
		})
		tr.settle()
		if tr.focused() != tr.grid {
			t.Errorf("focus on %v", tr.focused())
		}
	})
}

func TestDialogFocusTrapExemptRegions(t *testing.T) {
	t.Run("lets an exempt region outside keep the focus and joins it to the Tab cycle", func(t *testing.T) {
		outside := widgets.NewButton("Outside", "", widgets.Secondary, widgets.SizeMd, nil)
		stop := widgets.NewButton("Stop it", "", widgets.Secondary, widgets.SizeMd, nil)
		banner := &kit.Element{Exempt: kit.ExemptKeep}
		banner.Add(stop)
		r := newRig(t, kit.Size{}, outside)
		d := widgets.NewDialog(widgets.DialogOptions{Title: "Queue done"})
		cancel := widgets.NewButton("Cancel", "", widgets.Secondary, widgets.SizeMd, nil)
		ok := widgets.NewButton("OK", "", widgets.Primary, widgets.SizeMd, nil)
		d.SetFooter(nil, cancel, ok)
		d.CloseButton().Focus = kit.FocusNone
		d.Show(r.win)
		r.win.PushLayer(banner) // the post-queue countdown, outside the dialog
		r.settle()
		if r.win.Focused() != cancel.Base() {
			t.Fatalf("first focus on %v", r.win.Focused())
		}
		r.win.Focus(stop)
		if r.win.Focused() != stop.Base() {
			t.Fatal("the exempt region cannot take the focus")
		}
		r.win.Focus(outside) // anything else goes back into the dialog
		if r.win.Focused() != cancel.Base() {
			t.Fatalf("outside focus went to %v", r.win.Focused())
		}
		tab := func(from kit.Widget, shift bool) *kit.Element {
			r.win.Focus(from)
			m := kit.Modifiers(0)
			if shift {
				m = kit.ModShift
			}
			r.key(kit.KeyTab, m)
			return r.win.Focused()
		}
		for _, c := range []struct {
			from  kit.Widget
			shift bool
			want  kit.Widget
		}{{ok, false, stop}, {stop, false, cancel}, {cancel, true, stop}, {stop, true, ok}} {
			if got := tab(c.from, c.shift); got != c.want.Base() {
				t.Errorf("Tab (shift %v) from %v went to %v", c.shift, c.from.Base().Widget(), got.Widget())
			}
		}
	})
	t.Run("leaves the focus in an exempt region to a modal that opened by itself", func(t *testing.T) {
		stop := widgets.NewButton("Stop it", "", widgets.Secondary, widgets.SizeMd, nil)
		banner := &kit.Element{Exempt: kit.ExemptKeep}
		banner.Add(stop)
		r := newRig(t, kit.Size{}, banner)
		r.win.Focus(stop)
		self := widgets.NewDialog(widgets.DialogOptions{Title: "Confirm download", FocusSelf: true})
		self.SetFooter(nil, widgets.NewButton("OK", "", widgets.Primary, widgets.SizeMd, nil))
		self.Show(r.win)
		r.settle()
		if r.win.Focused() != stop.Base() {
			t.Fatalf("a self-opening modal took the focus: %v", r.win.Focused())
		}
		self.Remove()
		asked := widgets.NewDialog(widgets.DialogOptions{Title: "Confirm download"})
		okb := widgets.NewButton("OK", "", widgets.Primary, widgets.SizeMd, nil)
		asked.SetFooter(nil, okb)
		asked.CloseButton().Focus = kit.FocusNone
		asked.Show(r.win)
		r.settle()
		if r.win.Focused() != okb.Base() {
			t.Errorf("one the user opened did not take the focus: %v", r.win.Focused())
		}
	})
	t.Run("joins a Tab-only exempt region (the window controls) to the cycle without keeping the focus there", func(t *testing.T) {
		minimise := widgets.NewIconButton("win-minimise", 16, "Minimise", false, nil)
		controls := &kit.Element{Exempt: kit.ExemptTab}
		controls.Add(minimise)
		r := newRig(t, kit.Size{}, controls)
		r.win.Focus(minimise)
		self := widgets.NewDialog(widgets.DialogOptions{Title: "Confirm download", FocusSelf: true})
		okb := widgets.NewButton("OK", "", widgets.Primary, widgets.SizeMd, nil)
		self.SetFooter(nil, okb)
		self.CloseButton().Focus = kit.FocusNone
		self.Show(r.win)
		r.settle()
		if r.win.Focused() != self.Card() {
			t.Fatalf("the dialog did not take the focus from the window controls: %v", r.win.Focused())
		}
		r.win.Focus(okb)
		r.key(kit.KeyTab, 0)
		if r.win.Focused() != minimise.Base() {
			t.Errorf("Tab from the last control went to %v", r.win.Focused())
		}
	})
	t.Run("canRestoreFocus checks ancestors too", func(t *testing.T) {
		b := widgets.NewButton("B", "", widgets.Secondary, widgets.SizeMd, nil)
		wrap := &kit.Element{}
		wrap.Add(b)
		r := newRig(t, kit.Size{}, wrap)
		if !r.win.CanRestoreFocus(b.Base()) {
			t.Fatal("a plain button cannot take the focus back")
		}
		wrap.Semantics.Hidden = true
		if r.win.CanRestoreFocus(b.Base()) {
			t.Error("a button inside aria-hidden can take the focus back")
		}
		wrap.Semantics.Hidden = false
		if r.win.CanRestoreFocus(nil) {
			t.Error("nil can take the focus back")
		}
	})
}

func TestDialogStudio(t *testing.T) {
	t.Run("names itself by its title, or by its aria label when the visible title differs", func(t *testing.T) {
		r := newRig(t, kit.Size{})
		d := widgets.NewDialog(widgets.DialogOptions{Title: "Settings"})
		d.Show(r.win)
		r.settle()
		if n := node(t, d.Card()); n.Role != kit.RoleDialog || n.Name != "Settings" || n.States&kit.NodeModal == 0 {
			t.Fatalf("dialog %v %q %b", n.Role, n.Name, n.States)
		}
		d.Remove()
		add := widgets.NewDialog(widgets.DialogOptions{Title: "Add", AriaLabel: "Add video or stream"})
		field := widgets.NewInput("", widgets.InputOptions{Name: "Video page or stream address"}, nil)
		field.Autofocus = true
		add.Body.Add(field)
		add.SetAside(widgets.NewChip("Files · Video or stream", widgets.Plain, false, "", nil))
		add.SetIcon(widgets.NewIcon(0, 20, nil))
		add.Show(r.win)
		r.settle()
		if n := node(t, add.Card()); n.Name != "Add video or stream" {
			t.Errorf("dialog %q", n.Name)
		}
		head := add.Card().Children()[0]
		kids := head.Children()
		if len(kids) != 4 || kids[3] != add.CloseButton().Base() {
			t.Fatalf("header: icon, title, aside, close; got %d children", len(kids))
		}
		if l, ok := kids[1].Widget().(*kit.Label); !ok || l.Text() != "Add" {
			t.Errorf("the title is %T", kids[1].Widget())
		}
		if r.win.Focused() != field.Base() {
			t.Errorf("the autofocus field does not have the focus: %v", r.win.Focused())
		}
		if n := node(t, add.CloseButton().Base()); n.Name != "Close" {
			t.Errorf("close %q", n.Name)
		}
	})
	t.Run("sizes itself, keeps a fixed height, and lays out a flush body and a footer summary", func(t *testing.T) {
		r := newRig(t, kit.Size{W: 1200, H: 760})
		d := widgets.NewDialog(widgets.DialogOptions{Title: "Settings", Size: widgets.DialogXl, FixedHeight: kit.Rem(46), Flush: true})
		summary := kit.NewLabel("2 links · Main queue", widgets.FontBody, kit.TextOptions{})
		d.SetFooter(summary, widgets.NewButton("Cancel", "", widgets.Secondary, widgets.SizeMd, nil), widgets.NewButton("Save", "", widgets.Primary, widgets.SizeMd, nil))
		d.Show(r.win)
		r.settle()
		c := d.Card().WindowBounds()
		if c.Width() != 60*16 {
			t.Errorf("width %v, want min(60rem, 1200 - 32)", c.Width())
		}
		room := kit.DIP(760 - 2*16)
		if c.Height() != min(46*16, room) {
			t.Errorf("height %v, want min(46rem, %v)", c.Height(), room)
		}
		if p := d.Body.Style.Padding; p != (kit.Edges{}) || d.Body.Style.RowGap.IsSet() {
			t.Errorf("a flush body has padding %v", p)
		}
		foot := d.Card().Children()[len(d.Card().Children())-1]
		if foot.Children()[0].Children()[0] != summary.Base() {
			t.Error("the summary is not the footer's first")
		}
		var labels []string
		for _, b := range foot.Children()[1:] {
			if btn, ok := b.Widget().(*widgets.Button); ok {
				labels = append(labels, btn.Label().Text())
				if btn.Base().Size().H != 32 {
					t.Errorf("a footer button is %v high", btn.Base().Size().H)
				}
			}
		}
		if len(labels) != 2 || labels[0] != "Cancel" || labels[1] != "Save" {
			t.Errorf("buttons %q", labels)
		}
	})
	t.Run("keeps a given width over the size, and closes on Escape unless busy", func(t *testing.T) {
		r := newRig(t, kit.Size{})
		closes := 0
		d := widgets.NewDialog(widgets.DialogOptions{Title: "Move to queue", Width: kit.Rem(30), Size: widgets.DialogSm, OnClose: func() { closes++ }})
		d.SetBusy(true)
		d.Show(r.win)
		r.settle()
		if w := d.Card().Size().W; w != 30*16 {
			t.Errorf("width %v", w)
		}
		r.key(kit.KeyEscape, 0)
		r.wait(200 * time.Millisecond)
		if closes != 0 || !d.Shown() {
			t.Fatal("a busy dialog closed")
		}
		d.SetBusy(false)
		r.key(kit.KeyEscape, 0)
		if !d.Leaving() {
			t.Fatal("Escape did not start the closing animation")
		}
		r.wait(100 * time.Millisecond)
		if closes != 0 {
			t.Fatal("closed before its animation ended")
		}
		r.wait(60 * time.Millisecond)
		if closes != 1 || d.Shown() {
			t.Errorf("closes %d shown %v", closes, d.Shown())
		}
	})
	t.Run("submits on Enter in a single-line field, as a form does", func(t *testing.T) {
		r := newRig(t, kit.Size{})
		submits := 0
		d := widgets.NewDialog(widgets.DialogOptions{Title: "Rename", OnSubmit: func() { submits++ }})
		in := widgets.NewInput("x", widgets.InputOptions{Name: "Name"}, nil)
		d.Body.Add(in)
		d.Show(r.win)
		r.settle()
		r.win.Focus(in)
		r.key(kit.KeyEnter, 0)
		if submits != 1 {
			t.Errorf("%d submits", submits)
		}
	})
	t.Run("centres in the room under the title bar, 8 px from the edges in a short window", func(t *testing.T) {
		r := newRig(t, kit.Size{W: 900, H: 600})
		d := widgets.NewDialog(widgets.DialogOptions{Title: "Remove download?", Size: widgets.DialogSm, ModalTop: 44})
		d.Show(r.win)
		r.settle()
		c := d.Card().WindowBounds()
		room := kit.DIP(600 - 44 - 16)
		if want := 44 + 8 + (room-c.Height())/2; c.Min.Y != want || c.Min.X != (900-26*16)/2 {
			t.Errorf("dialog at %v, want top %v", c, want)
		}
	})
}
