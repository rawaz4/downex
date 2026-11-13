//go:build darwin && cgo

package mac

// [popups] The Select list of today's views in a popup window: the Confirm dialog's
// Category, over the simulated daemon, as macspike shows it.

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"downex/ui/host"
	"downex/ui/kit"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/state"
	"downex/ui/state/scene"
	"downex/ui/views"
	"downex/ui/views/app"
	"downex/ui/views/fixture"
)

// openMainWindow opens the main window of the views over the studio simulation, with the
// light theme, at 1200 × 760 (the Confirm dialog of a waiting download opens by itself).
func openMainWindow(t *testing.T) (*Window, *kit.Window) {
	t.Helper()
	a := testApp(t)
	mock, err := scene.New(scene.Options{Scene: scene.Studio})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mock.Close)
	var w *Window
	var kw *kit.Window
	mainDo(func() {
		view := host.ViewOf(nil)
		spec := view.Window()
		fp := &fixture.Platform{Mock: mock}
		themeState := state.NewTheme(state.ThemeOptions{})
		c, _ := tokens.ParseChoice("light")
		fixed := &c
		vt := &views.Theme{State: themeState, Fixed: fixed}
		var va *views.App
		var m *app.Main
		widgetTheme := func() *views.Theme {
			if va != nil {
				return va.WidgetTheme
			}
			return vt
		}
		density := state.NewDensity(nil)
		stores := state.NewMain(state.MainOptions{
			Daemon: mock, Events: mock, Files: fp, CopyText: fp.CopyText,
			Clock: state.SystemClock, Frames: state.TimerFrames{Clock: state.SystemClock},
			Collator: state.UseUserLocale(),
		})
		holder := widgets.NewScope(vt)
		holder.Style = kit.Style{Display: kit.DisplayBlock, Width: kit.Pct(100), Height: kit.Pct(100)}
		minSize := kit.Size{W: float32(spec.MinWidth), H: float32(spec.MinHeight)}
		kw = kit.NewWindow(holder, kit.WindowOptions{
			Title: spec.Title, MinSize: minSize,
			Palette:     func(env kit.Environment) kit.Palette { return widgetTheme().Palette(env) },
			FrameColors: func(env kit.Environment) kit.FrameColors { return widgetTheme().MainFrame(env) },
			OnKey:       func(e kit.KeyEvent) bool { return m != nil && m.HandleKey(e) },
		})
		va, err = views.NewApp(views.Options{
			Window: kw, Daemon: mock, Platform: fp, Main: stores, Theme: themeState, Density: density,
			Clock: state.SystemClock, Caps: view.Capabilities(), Build: host.CurrentBuild("test"), Demo: true,
			Log: slog.New(slog.DiscardHandler), FixedTheme: fixed,
		})
		if err != nil {
			return
		}
		holder.Theme = va.WidgetTheme
		m, err = app.New(va, app.Options{})
		if err != nil {
			return
		}
		holder.Add(m.Root())
		w, err = a.NewWindow(kw, WindowOptions{Title: spec.Title, Size: kit.Size{W: 1200, H: 760}, MinSize: minSize})
		if err != nil {
			return
		}
		stores.Start()
		t.Cleanup(func() {
			mainDo(func() {
				w.Close()
				stores.Dispose()
				m.Dispose()
				va.Dispose()
				density.Close()
				themeState.Close()
			})
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	mainDo(w.makeKey)
	pump(300 * time.Millisecond)
	return w, kw
}

// findNode is the first node of tree that ok accepts, depth first.
func findNode(tree kit.SemanticTree, ok func(kit.SemanticNode) bool) (kit.SemanticNode, bool) {
	var walk func(id kit.NodeID) (kit.SemanticNode, bool)
	walk = func(id kit.NodeID) (kit.SemanticNode, bool) {
		n, found := tree.Node(id)
		if !found {
			return kit.SemanticNode{}, false
		}
		if ok(n) {
			return n, true
		}
		for _, c := range tree.AppendChildren(nil, id) {
			if r, f := walk(c); f {
				return r, true
			}
		}
		return kit.SemanticNode{}, false
	}
	return walk(tree.Root())
}

// press presses and releases the left button at p: AppKit's own events when the window is
// key, else the same events given to the window as its view gives them (a test process
// may not activate itself, and AppKit keeps a first click on an inactive window).
func press(w *Window, p kit.Point) {
	mainDo(func() {
		if w.isKeyNow() {
			w.postMouse(evLeftDown, p, 1)
			w.postMouse(evLeftUp, p, 1)
			return
		}
		w.event(kit.PointerEvent{Action: kit.PointerMove, Pos: p})
		w.event(kit.PointerEvent{Action: kit.PointerDown, Pos: p, Button: kit.ButtonLeft, Buttons: kit.ButtonLeft, Clicks: 1})
		w.event(kit.PointerEvent{Action: kit.PointerUp, Pos: p, Button: kit.ButtonLeft})
	})
}

// key presses a key on the window: AppKit's key events when it is key, else the KeyEvent
// its view would give the root.
func key(w *Window, code int, k kit.Key) {
	mainDo(func() {
		if w.isKeyNow() {
			w.postKey(code, true, "")
			w.postKey(code, false, "")
			return
		}
		w.event(kit.KeyEvent{Action: kit.KeyDown, Key: k})
		w.event(kit.KeyEvent{Action: kit.KeyUp, Key: k})
	})
}

func onlyPopup(w *Window) *popupWindow {
	if l := popupsOf[w]; len(l) == 1 {
		return l[0]
	}
	return nil
}

func TestPopupSelectList(t *testing.T) {
	w, kw := openMainWindow(t)
	var trigger kit.SemanticNode
	if !pumpUntil(5*time.Second, func() bool {
		n, ok := findNode(kw.Semantics(), func(n kit.SemanticNode) bool {
			return n.Role == kit.RoleComboBox && strings.Contains(n.Name, "Category")
		})
		trigger = n
		return ok && !n.Bounds.Empty()
	}) {
		t.Fatal("no Category select (the Confirm dialog did not open)")
	}
	t.Logf("Category trigger at %v (value %q), window key: %v", trigger.Bounds, trigger.Value, w.isKeyNow())

	press(w, trigger.Bounds.Center())
	var p *popupWindow
	if !pumpUntil(2*time.Second, func() bool { p = onlyPopup(w); return p != nil && p.shown }) {
		t.Fatal("the list did not open")
	}
	pump(400 * time.Millisecond) // the rise
	var focused0 string
	mainDo(func() {
		box := p.hit.Add(p.origin())
		gap := box.Min.Y - trigger.Bounds.Max.Y
		t.Logf("list box %v (popup bounds %v, hit %v), gap under the trigger %.2f px", box, p.bounds, p.hit, gap)
		if gap < 2 || gap > 8 {
			t.Errorf("the list opens %.2f px under its trigger, want 2-8", gap)
		}
		if d := box.Min.X - trigger.Bounds.Min.X; d < -1 || d > 1 {
			t.Errorf("the list's left edge is %.2f px from the trigger's", d)
		}
		// The panel is where the bounds say on the screen.
		x, y, _, ph := p.screenFrame()
		o := w.screenToContent(x, y+ph)
		if d := o.Add(p.shift).Sub(p.origin()); d.X*d.X+d.Y*d.Y > 0.01 || p.shift.X >= 1 || p.shift.Y >= 1 {
			t.Errorf("panel at %v + shift %v on the owner, want %v", o, p.shift, p.origin())
		}
		if pk, ok := p.root.(*kit.Window); ok {
			if n, ok := pk.Semantics().Node(pk.Semantics().Focused()); ok {
				focused0 = n.Name
			}
		}
		dir := os.Getenv("DOWNEX_SHOTS")
		if dir != "" {
			_ = w.WritePNG(filepath.Join(dir, "select-owner.png"))
			_ = (&Offscreen{t: p.target}).WritePNG(filepath.Join(dir, "select-popup.png"))
		}
	})

	// Its accessibility: the panel shows the list's own tree, its options named, the active
	// one focused (a client reading it makes the bridge).
	var focusPosts int
	mainDo(func() {
		el := p.AXWindow()
		if el == nil {
			t.Fatal("no accessibility element for the panel")
		}
		dump := AXDump(el, 6)
		el.Release()
		t.Logf("the list as VoiceOver reads it:\n%s", dump)
		if !strings.Contains(dump, "AXList") || strings.Count(dump, "AXStaticText") < 2 && strings.Count(dump, "AXCell") < 2 {
			t.Errorf("the panel exposes no list of options:\n%s", dump)
		}
		if focused0 != "" && !strings.Contains(dump, focused0) {
			t.Errorf("the active option %q is not in the panel's tree", focused0)
		}
		if a := axByHost[p]; a == nil || a.b == nil || a.owner == nil {
			t.Errorf("the popup's accessibility is not registered with its owner: %+v", a)
		}
		focusPosts = AXStats().Posted["AXFocusedUIElementChanged"]
	})

	// The arrows move the list's highlight (the owner's root hands the keys over), and
	// VoiceOver hears the focus move (the popup's NotifySemantics).
	key(w, 125, kit.KeyArrowDown)
	pump(150 * time.Millisecond)
	mainDo(func() {
		pk := p.root.(*kit.Window)
		n, _ := pk.Semantics().Node(pk.Semantics().Focused())
		t.Logf("active item %q -> %q", focused0, n.Name)
		if n.Name == "" || n.Name == focused0 {
			t.Errorf("ArrowDown did not move the highlight: %q -> %q", focused0, n.Name)
		}
		if got := AXStats().Posted["AXFocusedUIElementChanged"]; got <= focusPosts {
			t.Errorf("no focus change posted for the popup's new active item (%d -> %d)", focusPosts, got)
		}
	})

	// Escape closes it; its accessibility goes with it.
	key(w, 53, kit.KeyEscape)
	if !pumpUntil(time.Second, func() bool { return onlyPopup(w) == nil && p.closed }) {
		t.Fatal("Escape did not close the list")
	}
	mainDo(func() {
		if axByHost[p] != nil {
			t.Error("the closed popup's accessibility is still registered")
		}
	})

	// A press outside closes it (AppKit's own event through the application's monitor).
	press(w, trigger.Bounds.Center())
	if !pumpUntil(2*time.Second, func() bool { p = onlyPopup(w); return p != nil && p.shown }) {
		t.Fatal("the list did not open again")
	}
	pump(200 * time.Millisecond)
	mainDo(func() { w.postMouse(evRightDown, kit.Pt(40, 700), 1); w.postMouse(evRightUp, kit.Pt(40, 700), 1) })
	if !pumpUntil(time.Second, func() bool { return onlyPopup(w) == nil && p.closed }) {
		t.Fatal("a press outside did not close the list")
	}
}
