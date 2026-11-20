package shell_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/categories"
	"downex/ui/core/display"
	"downex/ui/core/query"
	"downex/ui/kit"
	"downex/ui/kit/widgets"
	"downex/ui/state"
	"downex/ui/state/statetest"
	"downex/ui/views"
	"downex/ui/views/shell"
	"downex/ui/views/viewstest"
)

const mb = 1000 * 1000
const gb = 1000 * mb

// rig is a harness with a component shown.
func rig(t *testing.T, w kit.Widget) *viewstest.Harness {
	t.Helper()
	h := viewstest.New(t, viewstest.Options{Daemon: statetest.NewDaemon()})
	box := &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, Axis: kit.AxisColumn, Justify: kit.JustifyEnd, Width: kit.Pct(100), Height: kit.Pct(100)}}
	box.Add(w)
	h.Show(box)
	// Close the menus first: kit's Detach walks the layers while a closing menu takes its own
	// out (a kit issue, listed in the result's needs).
	t.Cleanup(func() { closeMenus(h) })
	return h
}

func closeMenus(h *viewstest.Harness) {
	for i := 0; i < 4 && len(h.Host.Open()) > 0; i++ {
		h.Key(kit.KeyEscape, 0)
	}
}

// nodes are the semantic nodes of a role in a window.
func nodes(w *kit.Window, role kit.Role) []kit.SemanticNode {
	tree := w.Semantics()
	var out []kit.SemanticNode
	var walk func(id kit.NodeID)
	walk = func(id kit.NodeID) {
		n, ok := tree.Node(id)
		if !ok {
			return
		}
		if n.Role == role {
			out = append(out, n)
		}
		for _, c := range tree.AppendChildren(nil, id) {
			walk(c)
		}
	}
	walk(tree.Root())
	return out
}

func node(t *testing.T, e *kit.Element) kit.SemanticNode {
	t.Helper()
	n, ok := e.Window().Semantics().Node(e.NodeID())
	if !ok {
		t.Fatalf("%T has no node", e.Widget())
	}
	return n
}

// clickItem clicks the open menu's item named name.
func clickItem(t *testing.T, h *viewstest.Harness, m *widgets.Menu, name string) {
	t.Helper()
	for _, role := range []kit.Role{kit.RoleMenuItem, kit.RoleMenuItemRadio, kit.RoleMenuItemCheckBox} {
		for _, n := range nodes(m.Root(), role) {
			if n.Name == name || strings.HasPrefix(n.Name, name+" ") {
				h.Host.Top().Click(n.Bounds.Center())
				h.Settle()
				return
			}
		}
	}
	t.Fatalf("no menu item %q", name)
}

func itemNames(m *widgets.Menu, role kit.Role) []string {
	var out []string
	for _, n := range nodes(m.Root(), role) {
		out = append(out, n.Name)
	}
	return out
}

var status = &downexv1.StatusResponse{Version: "1.2.3"}

func barState(edit func(s *shell.StatusState)) shell.StatusState {
	s := shell.StatusState{
		Daemon: &state.DaemonState{OK: true, Status: status},
		Totals: state.Totals{Speed: 3 * mb, Active: 3, Queued: 7, Count: 28},
		Shown:  28, Selected: 1, LimitKnown: true, PostActionKnown: true,
		PostActionChoices: []downexv1.PostAction{0, downexv1.PostAction_POST_ACTION_QUIT_DOWNEX, downexv1.PostAction_POST_ACTION_SLEEP, downexv1.PostAction_POST_ACTION_HIBERNATE, downexv1.PostAction_POST_ACTION_SHUTDOWN},
		FreeBytes:         214 * gb, FreeKnown: true, DriveLabel: "D:",
		History: []display.SpeedSample{{T: 0, V: 1 * mb}, {T: 30_000, V: 3 * mb}},
	}
	if edit != nil {
		edit(&s)
	}
	return s
}

type barRig struct {
	h        *viewstest.Harness
	b        *shell.StatusBar
	limits   []views.LimitChoice
	postActs []views.WhenDoneChoice
}

func newBar(t *testing.T, edit func(s *shell.StatusState)) *barRig {
	r := &barRig{}
	r.b = shell.NewStatusBar(shell.StatusOptions{
		OnLimit:      func(c views.LimitChoice) { r.limits = append(r.limits, c) },
		OnPostAction: func(c views.WhenDoneChoice) { r.postActs = append(r.postActs, c) },
	})
	r.h = rig(t, r.b)
	r.b.Update(barState(edit))
	r.h.Settle()
	return r
}

func (r *barRig) text() string { return strings.Join(r.b.Texts(), " | ") }

// StatusBar.test.ts
func TestStatusBar(t *testing.T) {
	t.Run("shows the connection, the total speed, the counts and the selection", func(t *testing.T) {
		r := newBar(t, nil)
		txt := r.text()
		for _, want := range []string{"Connected", "Total speed 3.0 MB/s", "3 active · 7 queued", "28 items · 1 selected"} {
			if !strings.Contains(txt, want) {
				t.Errorf("%q lacks %q", txt, want)
			}
		}
		if p := r.b.Parts()["conn"]; p.Tooltip != "Connected to downexd 1.2.3" {
			t.Errorf("tooltip %q", p.Tooltip)
		}
		if n := node(t, r.b.Sparkline().Base()); n.Role != kit.RoleImage || n.Name != "Total speed over the last minute" {
			t.Errorf("sparkline %v %q", n.Role, n.Name)
		}
		if pts := r.b.Sparkline().Points(); len(pts) != 2 {
			t.Errorf("sparkline points %v", pts)
		}
	})
	t.Run("says when downexd is unreachable, connecting or simulated", func(t *testing.T) {
		r := newBar(t, func(s *shell.StatusState) { s.Daemon = &state.DaemonState{Error: "connection refused"} })
		if !strings.Contains(r.text(), "downexd unavailable") || !strings.Contains(r.b.Parts()["conn"].Tooltip, "connection refused") ||
			!strings.HasPrefix(r.b.Parts()["conn"].Tooltip, "downexd unavailable: ") {
			t.Errorf("down: %q / %q", r.text(), r.b.Parts()["conn"].Tooltip)
		}
		r.b.Update(barState(func(s *shell.StatusState) { s.Daemon = nil }))
		if !strings.Contains(r.text(), "Connecting…") {
			t.Errorf("pending: %q", r.text())
		}
		r.b.Update(barState(func(s *shell.StatusState) { s.Daemon = nil; s.Demo = true }))
		if !strings.Contains(r.text(), "Demo mode") {
			t.Errorf("demo: %q", r.text())
		}
	})
	t.Run("chooses the global speed limit from a menu that opens upward", func(t *testing.T) {
		r := newBar(t, nil)
		lm := r.b.LimitMenu()
		trigger := lm.Trigger.Base()
		if !strings.Contains(trigger.Semantics.Name, "Unlimited") || node(t, trigger).States&kit.NodeHasPopup == 0 {
			t.Fatalf("trigger %q", trigger.Semantics.Name)
		}
		r.h.Click(trigger)
		m := lm.Menu()
		if m == nil {
			t.Fatal("no menu")
		}
		if g := nodes(m.Root(), kit.RoleGroup); len(g) != 1 || g[0].Name != "Global speed limit" {
			t.Errorf("groups %v", g)
		}
		if got := itemNames(m, kit.RoleMenuItemRadio); !slices.Equal(got, []string{"Unlimited", "20 MB/s", "10 MB/s", "5 MB/s", "1 MB/s"}) {
			t.Errorf("radios %q", got)
		}
		if rs := nodes(m.Root(), kit.RoleMenuItemRadio); rs[0].Toggle != kit.ToggleOn {
			t.Error("Unlimited is not checked")
		}
		if d := nodes(m.Root(), kit.RoleMenu)[0].Description; d != "Per-download and queue limits still apply." {
			t.Errorf("footnote %q", d)
		}
		if m.Bounds().Max.Y > trigger.WindowBounds().Min.Y+0.5 {
			t.Errorf("the menu %v does not open above its trigger %v", m.Bounds(), trigger.WindowBounds())
		}
		clickItem(t, r.h, m, "10 MB/s")
		if !slices.Equal(r.limits, []views.LimitChoice{{BytesPerSecond: 10 * mb}}) || lm.Menu() != nil {
			t.Errorf("limits %v, open %v", r.limits, lm.Menu() != nil)
		}
		// Custom… opens Settings (the caller does)
		r.h.Click(trigger)
		clickItem(t, r.h, lm.Menu(), "Custom…")
		if r.limits[len(r.limits)-1] != (views.LimitChoice{Custom: true}) {
			t.Errorf("custom: %v", r.limits)
		}
		// a limit in effect reads as a warning; one set elsewhere is listed as checked
		r.b.Update(barState(func(s *shell.StatusState) { s.Limit = 3 * mb }))
		if !strings.Contains(trigger.Semantics.Name, "Limit 3.0 MB/s") {
			t.Errorf("limited: %q", trigger.Semantics.Name)
		}
		if items := shell.LimitItems(3 * mb); !items[5].Checked || items[5].Label != "3.0 MB/s" || items[5].Note != "custom" {
			t.Errorf("custom item %+v", items[5])
		}
	})
	t.Run("keeps the menus closed while downexd is unreachable or the settings are unknown", func(t *testing.T) {
		r := newBar(t, func(s *shell.StatusState) { s.Stale = true })
		for _, mbtn := range []*widgets.MenuButton{r.b.LimitMenu(), r.b.WhenDoneMenu()} {
			if node(t, mbtn.Trigger.Base()).States&kit.NodeDisabled == 0 {
				t.Error("not disabled while stale")
			}
			r.h.Click(mbtn.Trigger.Base())
			if mbtn.Menu() != nil {
				t.Error("a menu opened while stale")
			}
		}
		r.b.Update(barState(func(s *shell.StatusState) { s.LimitKnown = false }))
		if node(t, r.b.LimitMenu().Trigger.Base()).States&kit.NodeDisabled == 0 || len(r.limits) != 0 {
			t.Error("the limit menu is usable before the settings are read")
		}
	})
	t.Run("says what happens when the main queue is done, in words, and changes it", func(t *testing.T) {
		r := newBar(t, nil)
		wm := r.b.WhenDoneMenu()
		trigger := wm.Trigger.Base()
		if trigger.Semantics.Name != "When done: keep running" {
			t.Errorf("trigger %q", trigger.Semantics.Name)
		}
		r.h.Click(trigger)
		if got := itemNames(wm.Menu(), kit.RoleMenuItemRadio); !slices.Equal(got, []string{"Keep running", "Quit Downex", "Sleep", "Hibernate", "Shut down"}) {
			t.Errorf("radios %q", got)
		}
		clickItem(t, r.h, wm.Menu(), "Shut down")
		if r.postActs[0] != (views.WhenDoneChoice{Action: downexv1.PostAction_POST_ACTION_SHUTDOWN}) {
			t.Errorf("chose %v", r.postActs)
		}
		r.h.Click(trigger)
		clickItem(t, r.h, wm.Menu(), "Other queues…")
		if r.postActs[1] != (views.WhenDoneChoice{Other: true}) {
			t.Errorf("other: %v", r.postActs)
		}
		r.b.Update(barState(func(s *shell.StatusState) { s.PostAction = downexv1.PostAction_POST_ACTION_SHUTDOWN }))
		if trigger.Semantics.Name != "When done: shut down" {
			t.Errorf("%q", trigger.Semantics.Name)
		}
		r.b.Update(barState(func(s *shell.StatusState) { s.PostAction = downexv1.PostAction_POST_ACTION_QUIT_DOWNEX }))
		if trigger.Semantics.Name != "When done: Quit Downex" {
			t.Errorf("%q", trigger.Semantics.Name)
		}
		// no queues (an older downexd): no control
		r.b.Update(barState(func(s *shell.StatusState) { s.PostActionKnown = false }))
		r.h.Settle()
		if !wm.Style.Hidden {
			t.Error("the When done control shows without queues")
		}
	})
	t.Run("opens its menus for the command palette", func(t *testing.T) {
		r := newBar(t, nil)
		r.b.OpenLimitMenu()
		r.h.Settle()
		if m := r.b.LimitMenu().Menu(); m == nil || m.Active() != "Unlimited" {
			t.Fatal("the limit menu did not open on its checked item")
		}
		r.h.Key(kit.KeyEscape, 0)
		if r.b.LimitMenu().Menu() != nil {
			t.Fatal("Escape left it open")
		}
		if !r.b.OpenWhenDone() {
			t.Fatal("OpenWhenDone reported false")
		}
		r.h.Settle()
		if r.b.WhenDoneMenu().Menu() == nil {
			t.Error("the when-done menu did not open")
		}
	})
	t.Run("shows the free space of the download drive, warns when it is too small and hides when unknown", func(t *testing.T) {
		r := newBar(t, nil)
		free := r.b.Free()
		if !strings.Contains(free.Semantics.Name, "D:") || !strings.Contains(free.Semantics.Name, "214 GB free") || r.b.Short() {
			t.Errorf("free %q", free.Semantics.Name)
		}
		if free.Tooltip != "214 GB free on the drive of the download folder" {
			t.Errorf("title %q", free.Tooltip)
		}
		r.b.Update(barState(func(s *shell.StatusState) { s.FreeBytes, s.NeedBytes = 2*gb, 5*gb }))
		warning := "Not enough free space for the active and queued downloads (about 3.0 GB more needed)"
		if !r.b.Short() || free.Tooltip != warning || !strings.Contains(free.Semantics.Name, warning) {
			t.Errorf("short: %v %q %q", r.b.Short(), free.Tooltip, free.Semantics.Name)
		}
		r.b.Update(barState(func(s *shell.StatusState) { s.FreeKnown = false }))
		if !free.Style.Hidden || strings.Contains(r.text(), "free") {
			t.Errorf("unknown free space still shows: %q", r.text())
		}
	})
}

// MenuButton.test.ts, as the status bar's and the page header's menus use it.
func TestMenuButtonUse(t *testing.T) {
	t.Run("opens its menu on click with the focus on the first item, and closes back to the trigger", func(t *testing.T) {
		r := newBar(t, nil)
		lm := r.b.LimitMenu()
		r.h.Click(lm.Trigger.Base())
		if lm.Menu() == nil || lm.Menu().Active() != "Unlimited" || node(t, lm.Trigger.Base()).Expand != kit.ExpandOpen {
			t.Fatal("not open on its first item")
		}
		r.h.Key(kit.KeyEscape, 0)
		if lm.Menu() != nil || !lm.Trigger.Base().Focused() {
			t.Error("Escape did not close it back to the trigger")
		}
	})
	t.Run("opens with Enter, Space and ArrowDown on the first item, and ArrowUp on the last", func(t *testing.T) {
		for _, c := range []struct {
			k    kit.Key
			want string
		}{{kit.KeyEnter, "Unlimited"}, {kit.KeySpace, "Unlimited"}, {kit.KeyArrowDown, "Unlimited"}, {kit.KeyArrowUp, "Custom…"}} {
			r := newBar(t, nil)
			lm := r.b.LimitMenu()
			r.h.Window.Focus(lm.Trigger)
			r.h.Key(c.k, 0)
			if lm.Menu() == nil || lm.Menu().Active() != c.want {
				t.Errorf("%v: open %v", c.k, lm.Menu() != nil)
			}
		}
	})
	t.Run("shows its open ground while the menu is open, also when no pointer is over it", func(t *testing.T) {
		// .sb-menu[aria-expanded='true'] { background: var(--hover) } (light: #f2f6f4 on the
		// #eef2f0 chrome, a step the stage-3 review missed), from the keyboard and the palette.
		for _, open := range []func(r *barRig){
			func(r *barRig) { r.h.Window.Focus(r.b.LimitMenu().Trigger); r.h.Key(kit.KeyEnter, 0) },
			func(r *barRig) { r.b.OpenLimitMenu(); r.h.Settle() },
		} {
			r := newBar(t, nil)
			trigger := r.b.LimitMenu().Trigger.Base()
			hover := views.TokensOf(trigger).Hover
			grounds := func() int {
				n := 0
				for _, op := range r.h.Loop.Canvas.Ops {
					if op.Kind == "FillRoundedRect" && op.Color == hover && op.RRect.Rect.Size() == trigger.Size() {
						n++
					}
				}
				return n
			}
			r.h.Loop.Canvas.Reset()
			open(r)
			r.h.Advance(time.Second) // the ground's 160 ms fade
			if r.b.LimitMenu().Menu() == nil || grounds() == 0 {
				t.Errorf("open %v: the trigger was not painted with --hover (%v)", r.b.LimitMenu().Menu() != nil, hover)
			}
			r.h.Key(kit.KeyEscape, 0)
			r.h.Loop.Canvas.Reset()
			r.h.Advance(time.Second)
			if r.b.LimitMenu().Menu() != nil || grounds() != 0 {
				t.Error("the closed menu's trigger kept its open ground")
			}
		}
	})
	t.Run("has radio items with aria-checked under a heading, and a footnote that describes it", func(t *testing.T) {
		r := newBar(t, nil)
		r.b.OpenLimitMenu()
		r.h.Settle()
		m := r.b.LimitMenu().Menu()
		var checked []kit.ToggleState
		for _, n := range nodes(m.Root(), kit.RoleMenuItemRadio) {
			checked = append(checked, n.Toggle)
		}
		if !slices.Equal(checked, []kit.ToggleState{kit.ToggleOn, kit.ToggleOff, kit.ToggleOff, kit.ToggleOff, kit.ToggleOff}) {
			t.Errorf("checked %v", checked)
		}
	})
	t.Run("chooses an item: the menu closes, the focus goes back, then the choice is told", func(t *testing.T) {
		r := newBar(t, nil)
		lm := r.b.LimitMenu()
		r.h.Click(lm.Trigger.Base())
		clickItem(t, r.h, lm.Menu(), "5 MB/s")
		if lm.Menu() != nil || !lm.Trigger.Base().Focused() || len(r.limits) != 1 || r.limits[0].BytesPerSecond != 5*mb {
			t.Errorf("open %v focused %v limits %v", lm.Menu() != nil, lm.Trigger.Base().Focused(), r.limits)
		}
	})
	t.Run("toggles from its trigger, and closes on an outside press", func(t *testing.T) {
		r := newBar(t, nil)
		lm := r.b.LimitMenu()
		r.h.Click(lm.Trigger.Base())
		r.h.Click(lm.Trigger.Base())
		if lm.Menu() != nil {
			t.Fatal("a second click left it open")
		}
		r.h.Click(lm.Trigger.Base())
		r.h.Host.Press(kit.Pt(10, 10))
		r.h.Settle()
		if lm.Menu() != nil {
			t.Error("an outside press left it open")
		}
	})
	t.Run("stays closed while unavailable but keeps the focus", func(t *testing.T) {
		r := newBar(t, func(s *shell.StatusState) { s.Stale = true })
		lm := r.b.LimitMenu()
		if !r.h.Window.Focus(lm.Trigger) {
			t.Fatal("an unavailable trigger cannot take the focus")
		}
		r.h.Key(kit.KeyArrowDown, 0)
		if lm.Menu() != nil {
			t.Error("it opened")
		}
	})
	t.Run("can be opened by its owner (openMenu) on the checked item", func(t *testing.T) {
		r := newBar(t, func(s *shell.StatusState) { s.Limit = 10 * mb })
		r.b.OpenLimitMenu()
		r.h.Settle()
		if m := r.b.LimitMenu().Menu(); m == nil || m.Active() != "10 MB/s" {
			t.Error("not on the checked item")
		}
	})
	// SplitButton cases, on the page header's Add.
	t.Run("SplitButton: runs its action from the main half and offers the other ways from the chevron", func(t *testing.T) {
		var calls []string
		hd := shell.NewHeader(shell.HeaderOptions{OnAdd: func() { calls = append(calls, "add") }, OnAddVideo: func() { calls = append(calls, "video") }})
		h := rig(t, hd)
		h.Click(hd.AddButton().Main.Base())
		h.Click(hd.AddButton().More.Trigger.Base())
		m := hd.AddButton().More.Menu()
		if m == nil || !slices.Equal(itemNames(m, kit.RoleMenuItem), []string{"Add downloads… Ctrl+N", "Add video or stream…", "Rescue partial file…"}) {
			t.Fatalf("menu %v", m != nil)
		}
		clickItem(t, h, m, "Add video or stream…")
		if !slices.Equal(calls, []string{"add", "video"}) {
			t.Errorf("calls %v", calls)
		}
	})
	t.Run("SplitButton: its third way is Rescue partial file (DESKTOP-SPEC §11)", func(t *testing.T) {
		var calls []string
		hd := shell.NewHeader(shell.HeaderOptions{OnAdd: func() { calls = append(calls, "add") },
			OnRescue: func() { calls = append(calls, "rescue") }})
		h := rig(t, hd)
		h.Click(hd.AddButton().More.Trigger.Base())
		m := hd.AddButton().More.Menu()
		if m == nil {
			t.Fatal("no menu")
		}
		clickItem(t, h, m, "Rescue partial file…")
		if !slices.Equal(calls, []string{"rescue"}) {
			t.Errorf("calls %v", calls)
		}
	})
	t.Run("SplitButton: does nothing while unavailable", func(t *testing.T) {
		calls := 0
		hd := shell.NewHeader(shell.HeaderOptions{OnAdd: func() { calls++ }})
		h := rig(t, hd)
		hd.Update(shell.HeaderState{Title: "All downloads", Status: categories.AnyStatus})
		h.Settle()
		h.Click(hd.AddButton().Main.Base())
		h.Click(hd.AddButton().More.Trigger.Base())
		if calls != 0 || hd.AddButton().More.Menu() != nil {
			t.Errorf("calls %d, menu %v", calls, hd.AddButton().More.Menu() != nil)
		}
		if hd.AddButton().Main.Tooltip != "Unavailable while downexd is unreachable" {
			t.Errorf("title %q", hd.AddButton().Main.Tooltip)
		}
	})
}

// ConnectionBanner.test.ts
func TestConnectionBanner(t *testing.T) {
	t.Run("explains the outage once assertively, with retry, countdown and raw details", func(t *testing.T) {
		retries := 0
		now := time.UnixMilli(1_000)
		b := shell.NewConnectionBanner(func() time.Time { return now }, func() { retries++ })
		h := rig(t, b)
		b.Update(shell.ConnState{Daemon: &state.DaemonState{OK: true, Status: status}})
		if !b.Style.Hidden || b.Alert != "" {
			t.Fatal("the banner shows while downexd is reachable")
		}
		b.Update(shell.ConnState{Daemon: &state.DaemonState{Error: "dial tcp 127.0.0.1:47820: connect: connection refused"}, NextCheckAt: time.UnixMilli(3_000)})
		h.Settle()
		if b.Style.Hidden || !strings.Contains(b.TitleText(), "can’t reach downexd") || node(t, b.Base()).Role != kit.RoleRegion {
			t.Fatalf("banner %q", b.TitleText())
		}
		if p := b.Paragraph(); !strings.Contains(p, "downex daemon start") || !strings.Contains(p, "every 3 seconds") || !strings.Contains(p, "last known state") {
			t.Errorf("text %q", p)
		}
		if b.CountdownText() != "Next try in 2 s" {
			t.Errorf("countdown %q", b.CountdownText())
		}
		if !strings.HasPrefix(b.Alert, "Lost connection to downexd") {
			t.Errorf("alert %q", b.Alert)
		}
		// the countdown is not in a live region
		if node(t, b.Base()).Live != kit.LiveOff {
			t.Error("the banner is a live region")
		}
		// the raw error is kept, behind a disclosure
		if !strings.Contains(b.Raw(), "connection refused") {
			t.Errorf("raw %q", b.Raw())
		}
		h.Click(b.Retry().Base())
		if retries != 1 {
			t.Errorf("retries %d", retries)
		}
		b.Update(shell.ConnState{Daemon: &state.DaemonState{Error: "refused"}, Checking: true, NextCheckAt: time.UnixMilli(3_000)})
		h.Settle()
		if node(t, b.Retry().Base()).States&kit.NodeDisabled == 0 || b.CountdownText() != "Checking…" {
			t.Errorf("checking: %q", b.CountdownText())
		}
		h.Click(b.Retry().Base())
		if retries != 1 {
			t.Error("Retry ran while checking")
		}
		b.Update(shell.ConnState{Daemon: &state.DaemonState{OK: true, Status: status}})
		if !b.Style.Hidden || b.Alert != "" || b.Status != "Reconnected to downexd." {
			t.Errorf("back: hidden %v alert %q status %q", b.Style.Hidden, b.Alert, b.Status)
		}
	})
	t.Run("covers a failed first load", func(t *testing.T) {
		b := shell.NewConnectionBanner(time.Now, func() {})
		rig(t, b)
		b.Update(shell.ConnState{Daemon: &state.DaemonState{OK: true, Status: status}, LoadError: "internal: list failed"})
		if b.Style.Hidden || !strings.Contains(b.TitleText(), "couldn’t load your downloads") || !strings.Contains(b.Alert, "couldn’t load") {
			t.Errorf("%q %q", b.TitleText(), b.Alert)
		}
	})
}

// PostActionBanner: the countdown and its Cancel (App.m6.test.ts's banner parts).
func TestPostActionBanner(t *testing.T) {
	now := time.UnixMilli(1_000_000)
	p := &downexv1.PostActionPending{QueueId: 2, Action: downexv1.PostAction_POST_ACTION_SHUTDOWN, Deadline: statetest.Ms(now.UnixMilli() + 60_000)}
	var dones []func(error)
	b := shell.NewPostActionBanner(p, "Night", func() time.Time { return now }, func(done func(error)) { dones = append(dones, done) })
	h := rig(t, b)
	if b.Text() != "Shutting down in 60 s" || b.Detail() != "Night finished its downloads." {
		t.Errorf("%q / %q", b.Text(), b.Detail())
	}
	if !strings.HasPrefix(b.Announced, "Shutting down in about 60 seconds, because the queue Night finished.") {
		t.Errorf("announced %q", b.Announced)
	}
	if b.Exempt != kit.ExemptKeep {
		t.Error("not exempt from a dialog's trap")
	}
	now = now.Add(18 * time.Second)
	h.Advance(time.Second)
	if b.Text() != "Shutting down in 42 s" {
		t.Errorf("after 18 s: %q", b.Text())
	}
	b.Focus()
	h.Key(kit.KeyEscape, 0)
	if len(dones) != 1 || b.CancelButton().Label().Text() != "Cancelling…" {
		t.Fatalf("Escape: %d, %q", len(dones), b.CancelButton().Label().Text())
	}
	dones[0](errors.New("internal: no"))
	if b.Failure() == "" || b.CancelButton().Label().Text() != "Cancel" {
		t.Errorf("failure %q", b.Failure())
	}
	now = now.Add(60 * time.Second)
	h.Advance(time.Second)
	if b.Text() != "Shutting down now…" {
		t.Errorf("at the deadline: %q", b.Text())
	}
}

// The sidebar (Sidebar.svelte through App.svelte's cases): categories, queues with their
// state and counts, the statuses, and the footer.
func TestSidebar(t *testing.T) {
	var cats []categories.Filter
	var sts []categories.Status
	var qs []int64
	manage, integ := 0, 0
	s := shell.NewSidebar(shell.SidebarOptions{
		OnCategory: func(f categories.Filter) { cats = append(cats, f) }, OnStatus: func(st categories.Status) { sts = append(sts, st) },
		OnQueue: func(id int64) { qs = append(qs, id) }, OnManageQueues: func() { manage++ }, OnIntegration: func() { integ++ },
	})
	h := rig(t, s)
	ix := categories.NewIndex([]*downexv1.Category{{Name: "Video", Extensions: []string{"mp4"}}, {Name: "Comics", Extensions: []string{"cbz"}}})
	st := shell.SidebarState{
		Index: ix, Category: categories.All, Status: categories.AnyStatus,
		Counts:      query.Counts{Categories: map[categories.Filter]int{categories.All: 3, categories.Key("Comics"): 1}, Statuses: map[categories.Status]int{categories.Queued: 2}},
		Queues:      []*downexv1.Queue{statetest.Queue(1, nil), statetest.Queue(2, func(q *downexv1.Queue) { q.Name = "Night" })},
		QueueCounts: map[int64]int{1: 2, 2: 1}, Browser: shell.BrowserStart,
	}
	s.Update(st)
	h.Settle()
	if n := node(t, s.Base()); n.Role != kit.RoleNavigation || n.Name != "Filters" {
		t.Errorf("nav %v %q", n.Role, n.Name)
	}
	c, q, ss := s.Items()
	// App.test.ts "lists the configured categories, custom ones too, and Other"
	var labels []string
	for _, e := range c {
		labels = append(labels, e.Semantics.Name)
	}
	if !slices.Equal(labels, []string{"All downloads 3", "Video 0", "Comics 1", "Other 0"}) {
		t.Errorf("categories %q", labels)
	}
	h.Click(c[2])
	if !slices.Equal(cats, []categories.Filter{categories.Key("Comics")}) {
		t.Errorf("chose %v", cats)
	}
	// App.m6.test.ts "lists the queues in the sidebar": running state and what is left to do
	if q[0].Semantics.Name != "Main queue Running 2" || q[1].Semantics.Name != "Night Stopped 1" || q[1].Tooltip != "Night: stopped, 1 to do" {
		t.Errorf("queues %q %q %q", q[0].Semantics.Name, q[1].Semantics.Name, q[1].Tooltip)
	}
	// .item's flex row: label, .qnote and .count 10 px apart (gap: 10px); the note sat against
	// the count's 1.5em box, about 4 px from its digits where today's are about 12 px away.
	if kids := q[0].Children(); len(kids) != 4 {
		t.Errorf("the queue item has %d parts, want glyph, label, note, count", len(kids))
	} else {
		label, note, count := kids[1].WindowBounds(), kids[2].WindowBounds(), kids[3].WindowBounds()
		if note.Empty() || count.Min.X-note.Max.X != 10 || note.Min.X-label.Max.X != 10 {
			t.Errorf("label %v, note %v, count %v: want 10 px between each", label, note, count)
		}
	}
	h.Click(q[1])
	st.Queue = 2
	s.Update(st)
	h.Click(q[1])
	if !slices.Equal(qs, []int64{2, 0}) || node(t, q[1]).Toggle != kit.ToggleOn {
		t.Errorf("queue clicks %v", qs)
	}
	// a status toggles
	st.Status = categories.Queued
	s.Update(st)
	h.Click(ss[2])
	if !slices.Equal(sts, []categories.Status{categories.AnyStatus}) || ss[2].Semantics.Name != "Queued 2" {
		t.Errorf("statuses %v %q", sts, ss[2].Semantics.Name)
	}
	// App.m6.test.ts "opens the queue manager from the sidebar"
	h.Click(s.Manage().Base())
	if manage != 1 || s.Manage().Semantics.Name != "Manage queues" || s.Manage().Tooltip != "Manage queues…" {
		t.Errorf("manage %d", manage)
	}
	// App.shell.test.ts "says honestly what browser downloads do and opens Settings → Integration"
	if !strings.HasPrefix(s.Handoff().Semantics.Name, "Browser downloads: start at once") {
		t.Errorf("footer %q", s.Handoff().Semantics.Name)
	}
	h.Click(s.Handoff())
	if integ != 1 {
		t.Error("the footer did not open Settings")
	}
	// App.shell.test.ts "asks first by default" (once the settings are read: confirm on)
	st.Browser = shell.BrowserAsk
	s.Update(st)
	if !strings.HasPrefix(s.Handoff().Semantics.Name, "Browser downloads: ask first") {
		t.Errorf("footer %q", s.Handoff().Semantics.Name)
	}
	// no queues: no section
	st.Queues = nil
	s.Update(st)
	h.Settle()
	if _, q, _ := s.Items(); len(q) != 0 || s.Parts()["group head"].El != nil {
		t.Error("the queue section shows without queues")
	}
}

// The page header (Toolbar.svelte through App.shell.test.ts's cases).
func TestHeader(t *testing.T) {
	var chips []categories.Status
	var calls []string
	hd := shell.NewHeader(shell.HeaderOptions{
		OnChip:   func(s categories.Status) { chips = append(chips, s) },
		OnPause:  func() { calls = append(calls, "pause") },
		OnResume: func() { calls = append(calls, "resume") },
		OnDensity: func(c bool) {
			if c {
				calls = append(calls, "compact")
			}
		},
		OnToggleDetails: func() { calls = append(calls, "details") },
		OnManageQueues:  func() { calls = append(calls, "queues") },
	})
	h := rig(t, hd)
	st := shell.HeaderState{Title: "All downloads", Count: 4, Chips: shell.HeaderChips{Downloading: 1, Queued: 1, Failed: 1}, Status: categories.AnyStatus,
		Abilities: views.Abilities{CanAdd: true, CanResume: true}, More: shell.MoreState{Details: true, Queues: true}}
	hd.Update(st)
	h.Settle()
	// "names the filter shown, counts it, and filters by its chips"
	if hd.Title().Text() != "All downloads" || hd.CountText().Text() != "4" || node(t, hd.Title().Base()).Level != 1 {
		t.Errorf("title %q count %q", hd.Title().Text(), hd.CountText().Text())
	}
	for f, name := range map[categories.Status]string{categories.Active: "1 downloading", categories.Queued: "1 queued", categories.Failed: "1 failed"} {
		if hd.Chip(f).Semantics.Name != name {
			t.Errorf("chip %q, want %q", hd.Chip(f).Semantics.Name, name)
		}
	}
	h.Click(hd.Chip(categories.Active).Base())
	st.Status = categories.Active
	st.Title = "Active"
	hd.Update(st)
	if !slices.Equal(chips, []categories.Status{categories.Active}) || node(t, hd.Chip(categories.Active).Base()).Toggle != kit.ToggleOn || hd.Chip(categories.Active).Tooltip != "Show every status" {
		t.Errorf("chips %v", chips)
	}
	// unavailable actions keep the focus but do nothing (aria-disabled)
	h.Click(hd.Pause().Base())
	h.Click(hd.Resume().Base())
	if !slices.Equal(calls, []string{"resume"}) || node(t, hd.Pause().Base()).States&kit.NodeDisabled == 0 {
		t.Errorf("calls %v", calls)
	}
	// App.shell.test.ts "switches the list density from the More menu"
	h.Click(hd.More().Trigger.Base())
	m := hd.More().Menu()
	if m == nil || nodes(m.Root(), kit.RoleGroup)[0].Name != "List density" {
		t.Fatal("no More menu with its density group")
	}
	if names := itemNames(m, kit.RoleMenuItemRadio); len(names) != 2 || !strings.HasPrefix(names[0], "Comfortable") {
		t.Errorf("radios %q", names)
	}
	clickItem(t, h, m, "Compact")
	if calls[len(calls)-1] != "compact" || !hd.More().Trigger.Base().Focused() {
		t.Errorf("calls %v, focus back %v", calls, hd.More().Trigger.Base().Focused())
	}
	h.Click(hd.More().Trigger.Base())
	clickItem(t, h, hd.More().Menu(), "Manage queues…")
	if calls[len(calls)-1] != "queues" {
		t.Errorf("calls %v", calls)
	}
}
