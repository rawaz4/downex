//go:build darwin && cgo

package parity

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"downex/ui/kit"
	"downex/ui/state/scene"
	"downex/ui/views"
	"downex/ui/views/add"
	"downex/ui/views/app"
	"downex/ui/views/viewstest"
)

// The window the comparisons use (points) and its scale.
const (
	width  = 1200
	height = 760
	scale  = 2
)

// harnessStates are the dialog states the harness plays natively (the generic player below
// knows their steps); the others are listed as not covered.
var harnessStates = []string{
	"confirm-file", "confirm-file-waiting", "confirm-file-invalid", "confirm-file-fail",
	"confirm-video-1", "confirm-video-6-owner", "confirm-video-8", "confirm-video-6-ar", "confirm-hls", "confirm-dash", "confirm-stream-audio",
	"add-files", "add-files-more", "add-files-manifest", "add-stream", "add-video-6", "add-hls", "add-video-error",
	"again-keep", "restart", "remove", "remove-several", "open-program",
	"move-queue", "queues-main", "queues-night", "queues-new",
	"settings-general", "settings-downloads", "settings-network", "settings-integration", "settings-video",
	"settings-appearance", "settings-updates", "settings-about", "settings-network-full", "palette",
}

// programStates are drawn by the native program itself (-fixture studio): the main window
// and the progress windows of the step-less progress states.
var programStates = []string{"main", "pw-status", "pw-site", "pw-paused", "pw-failed", "pw-complete", "pw-complete-media"}

func pick(env string) func(string) bool {
	v := os.Getenv(env)
	if v == "" {
		return func(string) bool { return true }
	}
	set := strings.Split(v, ",")
	return func(s string) bool { return slices.Contains(set, s) }
}

type setup struct {
	demo, out, wkplay string
	wantState         func(string) bool
	themes            []string
}

func newSetup(t *testing.T) *setup {
	demo := os.Getenv("DOWNEX_PARITY_DEMO")
	if demo == "" {
		t.Skip("DOWNEX_PARITY_DEMO names the built demo's address")
	}
	out := os.Getenv("DOWNEX_PARITY_OUT")
	if out == "" {
		out = t.TempDir()
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "wkplay")
	_, here, _, _ := runtime.Caller(0)
	cmd := exec.Command("swiftc", "-O", "-o", bin, filepath.Join(filepath.Dir(here), "testdata", "wkplay.swift"))
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("swiftc: %v\n%s", err, b)
	}
	themes := []string{"light", "dark"}
	if v := os.Getenv("DOWNEX_PARITY_THEMES"); v != "" {
		themes = strings.Split(v, ",")
	}
	return &setup{demo: demo, out: out, wkplay: bin, wantState: pick("DOWNEX_PARITY_STATES"), themes: themes}
}

// probe is what the reference's last step measured.
type probe struct {
	Dialog *struct{ X, Y, W, H float64 } `json:"dialog"`
}

// reference renders state's page in WebKit; query is the page's query after "?".
func (s *setup) reference(t *testing.T, query string, steps []string, w, h int, theme, out string) (probe, error) {
	steps = append(steps, `const d = document.querySelector('[role=dialog],[role=alertdialog]'); if (!d) return {}; const r = d.getBoundingClientRect(); return {dialog: {X: r.left, Y: r.top, W: r.width, H: r.height}};`)
	js, _ := json.Marshal(steps)
	sf := filepath.Join(t.TempDir(), "steps.json")
	if err := os.WriteFile(sf, js, 0o644); err != nil {
		return probe{}, err
	}
	url := strings.TrimRight(s.demo, "/") + "/?" + query
	b, err := exec.Command(s.wkplay, url, out, fmt.Sprint(w), fmt.Sprint(h), theme, sf).CombinedOutput()
	var p probe
	for _, line := range strings.Split(string(b), "\n") {
		if j, ok := strings.CutPrefix(line, "probe: "); ok {
			_ = json.Unmarshal([]byte(j), &p)
		}
	}
	if err != nil || !bytes.Contains(b, []byte("\nok")) && !bytes.HasPrefix(b, []byte("ok")) {
		return p, fmt.Errorf("wkplay %s: %v: %s", query, err, b)
	}
	return p, nil
}

func readPNG(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return png.Decode(f)
}

// report is one comparison's line.
type report struct {
	State, Theme     string
	Mean, Differing  float64
	DialogRef        string
	DialogNative     string
	Notes            []string
	Sheet, RefShot   string
	NativeShot       string
	ShiftDX, ShiftDY int
}

func (s *setup) sheet(t *testing.T, name, theme string, ref, nat image.Image, area kit.Rect, natAt kit.Point) report {
	c := viewstest.Compare(ref, nat, area, natAt, scale)
	path := filepath.Join(s.out, fmt.Sprintf("%s-%s.png", name, theme))
	if err := viewstest.WritePNG(path, c.Sheet); err != nil {
		t.Error(err)
	}
	return report{State: name, Theme: theme, Mean: c.Mean, Differing: c.Differing, Sheet: path}
}

func writeReports(t *testing.T, s *setup, file string, rs []report) {
	b, _ := json.MarshalIndent(rs, "", "  ")
	if err := os.WriteFile(filepath.Join(s.out, file), b, 0o644); err != nil {
		t.Error(err)
	}
	for _, r := range rs {
		t.Logf("%-28s %-5s mean %5.2f differing %5.1f%% dialog ref %s native %s %v", r.State, r.Theme, r.Mean, 100*r.Differing, r.DialogRef, r.DialogNative, r.Notes)
	}
}

// TestParityStates renders the dialog states natively (viewstest) and in WebKit.
func TestParityStates(t *testing.T) {
	s := newSetup(t)
	if err := viewstest.CanRender(); err != nil {
		t.Fatal(err)
	}
	var rs []report
	for _, name := range harnessStates {
		if !s.wantState(name) {
			continue
		}
		fx, ok := scene.Fixture(name)
		if !ok {
			t.Errorf("no state %q", name)
			continue
		}
		for _, theme := range s.themes {
			t.Run(name+"/"+theme, func(t *testing.T) {
				refPath := filepath.Join(s.out, "ref", name+"-"+theme+".png")
				natPath := filepath.Join(s.out, "native", name+"-"+theme+".png")
				_ = os.MkdirAll(filepath.Dir(refPath), 0o755)
				_ = os.MkdirAll(filepath.Dir(natPath), 0o755)
				q := "mac=1"
				if fx.Fixture != "" {
					q += "&fixture=" + fx.Fixture
				}
				p, err := s.reference(t, q, jsSteps(fx), width, height, theme, refPath)
				if err != nil {
					t.Fatal(err)
				}
				nat, card, notes := renderNative(t, fx, theme)
				if err := viewstest.WritePNG(natPath, nat); err != nil {
					t.Fatal(err)
				}
				ref, err := readPNG(refPath)
				if err != nil {
					t.Fatal(err)
				}
				r := s.sheet(t, name, theme, ref, nat, kit.Rect{Max: kit.Point{X: width, Y: height}}, kit.Point{})
				r.RefShot, r.NativeShot, r.Notes = refPath, natPath, notes
				if p.Dialog != nil {
					r.DialogRef = fmt.Sprintf("%.1f,%.1f %.1fx%.1f", p.Dialog.X, p.Dialog.Y, p.Dialog.W, p.Dialog.H)
				}
				if card != (kit.Rect{}) {
					r.DialogNative = fmt.Sprintf("%.1f,%.1f %.1fx%.1f", card.Min.X, card.Min.Y, card.Width(), card.Height())
				}
				if p.Dialog != nil && card != (kit.Rect{}) {
					area := kit.Rect{Min: kit.Point{X: float32(p.Dialog.X), Y: float32(p.Dialog.Y)}}
					area.Max = area.Min.Add(kit.Point{X: float32(p.Dialog.W), Y: float32(p.Dialog.H)})
					c := s.sheet(t, name+"-dialog", theme, ref, nat, area, card.Min)
					r.Mean, r.Differing = c.Mean, c.Differing
					r.ShiftDX, r.ShiftDY, _, _ = viewstest.BestShift(ref, nat, area, card.Min, scale, 6)
				}
				rs = append(rs, r)
			})
		}
	}
	writeReports(t, s, "states.json", rs)
}

// TestParityProgram draws the main window and the progress windows with the native program
// (its own Mac window, its DOWNEX_UI_SHOTS picture) and the same views in WebKit at the
// program window's size.
func TestParityProgram(t *testing.T) {
	s := newSetup(t)
	bin := filepath.Join(t.TempDir(), "downex-ui")
	_, here, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(here), "..", "..", "..", "..")
	b, err := exec.Command("go", "build", "-o", bin, filepath.Join(root, "ui", "native")).CombinedOutput()
	if err != nil {
		t.Fatalf("build: %v\n%s", err, b)
	}
	var rs []report
	for _, name := range programStates {
		if !s.wantState(name) {
			continue
		}
		var id int64
		if name != "main" {
			fx, ok := scene.Fixture(name)
			if !ok {
				t.Fatalf("no state %q", name)
			}
			id = fx.Progress
		}
		for _, theme := range s.themes {
			t.Run(name+"/"+theme, func(t *testing.T) {
				dir := t.TempDir()
				args := []string{"-fixture", "studio", "-theme", theme, "-noactivate", "-exit-after", "6s"}
				q := "mac=1"
				if id > 0 {
					args = append(args, "--progress", fmt.Sprint(id))
					q += fmt.Sprintf("&progress=%d", id)
				} else {
					args = append(args, "-size", fmt.Sprintf("%dx%d", width, height))
				}
				cmd := exec.Command(bin, args...)
				cmd.Env = append(os.Environ(), "DOWNEX_UI_SHOTS="+filepath.Join(dir, "shot"))
				if b, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("downex-ui: %v\n%s", err, b)
				}
				shots, _ := filepath.Glob(filepath.Join(dir, "shot.*.png"))
				if len(shots) != 1 {
					t.Fatalf("shots: %v", shots)
				}
				nat, err := readPNG(shots[0])
				if err != nil {
					t.Fatal(err)
				}
				natPath := filepath.Join(s.out, "native", name+"-"+theme+".png")
				refPath := filepath.Join(s.out, "ref", name+"-"+theme+".png")
				_ = os.MkdirAll(filepath.Dir(natPath), 0o755)
				_ = os.MkdirAll(filepath.Dir(refPath), 0o755)
				_ = viewstest.WritePNG(natPath, nat)
				w, h := nat.Bounds().Dx()/scale, nat.Bounds().Dy()/scale
				var steps []string
				if id > 0 {
					steps = append(steps, `localStorage.setItem('downex.progress.details','0'); return true;`)
				}
				if _, err := s.reference(t, q, steps, w, h, theme, refPath); err != nil {
					t.Fatal(err)
				}
				ref, err := readPNG(refPath)
				if err != nil {
					t.Fatal(err)
				}
				r := s.sheet(t, name, theme, ref, nat, kit.Rect{Max: kit.Point{X: float32(w), Y: float32(h)}}, kit.Point{})
				r.RefShot, r.NativeShot = refPath, natPath
				r.Notes = []string{fmt.Sprintf("window %dx%d", w, h)}
				rs = append(rs, r)
			})
		}
	}
	writeReports(t, s, "program.json", rs)
}

// renderNative opens state fx in the views over the Mac renderer and draws it: the image,
// the dialog card's box (zero without a dialog), and what the player could not do.
func renderNative(t *testing.T, fx scene.FixtureState, theme string) (*image.RGBA, kit.Rect, []string) {
	frameless := false
	o := viewstest.Options{Width: width, Height: height, Scale: scale, Theme: theme, Fixture: fx.Name, Real: true, Frameless: &frameless,
		Scene: func(so *scene.Options) {
			// ?mac=1: the startup view the Mac host gives (no Open with, hibernate or wake
			// timers; the system title bar).
			v := *so.View
			v.OpenWith, v.Hibernate, v.WakeTimers, v.CustomTitleBar = false, false, false, false
			so.View = &v
		},
	}
	h := viewstest.New(t, o)
	// The Mac window's environment (ui/mac window.go readEnvironment): WebKit matches neither
	// value of prefers-reduced-transparency, so the Mac host reports it on (no backdrop blur).
	h.Host.Host.Env.ReducedTransparency = true
	h.Host.Host.Env.WebKit = true // [mac webkit] as readEnvironment
	// [mac webkit] the reference WKWebView's bars: overlay (this Mac's system style)
	h.Host.Host.Env.ScrollBars = kit.ScrollBarsMacOverlay
	h.Loop.Send(kit.EnvironmentEvent{Env: h.Host.Host.Env})
	m, err := app.New(h.App, app.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Dispose)
	h.Show(m.Root())
	h.Advance(time.Second)
	p := &player{t: t, h: h}
	if !fx.Confirm {
		// the page's script closes a dialog the simulated daemon opened at start (the
		// Studio scene's download awaiting confirmation)
		for range 3 {
			n, ok := p.find(func(n kit.SemanticNode) bool { return n.Role == kit.RoleButton && n.Name == "Close" })
			if !ok {
				break
			}
			p.click(n.Bounds.Center(), kit.ButtonLeft, 0, 1)
			h.Advance(400 * time.Millisecond)
		}
	}
	for _, st := range fx.Steps {
		p.step(st)
		h.Advance(400 * time.Millisecond)
		if os.Getenv("DOWNEX_PARITY_DUMP") != "" {
			t.Logf("after step %+v: focus visible %v", st, h.Window.FocusVisible())
		}
	}
	h.Advance(1500 * time.Millisecond)
	img, err := h.Render()
	if err != nil {
		t.Fatal(err)
	}
	// DOWNEX_PARITY_DUMP=<prefix> logs the semantic nodes whose name starts with it (boxes
	// to compare with the page's getBoundingClientRect).
	if pre := os.Getenv("DOWNEX_PARITY_DUMP"); pre != "" {
		for _, n := range nodes(h.Window.Semantics()) {
			if strings.HasPrefix(n.Name, pre) {
				t.Logf("node %v %q %+v", n.Role, n.Name, n.Bounds)
			}
		}
	}
	var card kit.Rect
	if n, ok := p.find(func(n kit.SemanticNode) bool { return n.Role == kit.RoleDialog || n.Role == kit.RoleAlertDialog }); ok {
		card = n.Bounds
	}
	return img, card, p.notes
}

// player plays a state's steps on the native views: the openers for the shortcuts, the
// semantic tree (the accessibility bridge's view) for clicks and typing.
type player struct {
	t        *testing.T
	h        *viewstest.Harness
	notes    []string
	inFlight atomic.Int64
}

// async runs background work on a goroutine of its own (Add's Go).
func (p *player) async(f func()) {
	p.inFlight.Add(1)
	go func() {
		defer p.inFlight.Add(-1)
		f()
	}()
}

// tick moves the scene and the clocks on by d in 250 ms steps, letting background work post
// back between them, then waits (at most 10 s) for it to end.
func (p *player) tick(d time.Duration) {
	h := p.h
	for range int(d / (250 * time.Millisecond)) {
		h.Settle()
		time.Sleep(5 * time.Millisecond)
		h.Settle()
		if h.Mock != nil {
			h.Mock.Tick(250)
		}
		h.Advance(250 * time.Millisecond)
	}
	deadline := time.Now().Add(10 * time.Second)
	for p.inFlight.Load() > 0 && time.Now().Before(deadline) {
		h.Settle()
		time.Sleep(2 * time.Millisecond)
	}
	h.Settle()
}

func (p *player) note(f string, a ...any) { p.notes = append(p.notes, fmt.Sprintf(f, a...)) }

// nodes are the window's semantic nodes in reading order.
func nodes(tree kit.SemanticTree) []kit.SemanticNode {
	var out []kit.SemanticNode
	var walk func(id kit.NodeID)
	walk = func(id kit.NodeID) {
		if n, ok := tree.Node(id); ok {
			out = append(out, n)
		}
		for _, c := range tree.AppendChildren(nil, id) {
			walk(c)
		}
	}
	walk(tree.Root())
	return out
}

// find is the last node (the dialog's, after the window's) that matches.
func (p *player) find(match func(kit.SemanticNode) bool) (kit.SemanticNode, bool) {
	all := nodes(p.h.Window.Semantics())
	for i := len(all) - 1; i >= 0; i-- {
		if match(all[i]) {
			return all[i], true
		}
	}
	return kit.SemanticNode{}, false
}

func named(role kit.Role, text string) func(kit.SemanticNode) bool {
	return func(n kit.SemanticNode) bool {
		return n.Role == role && strings.HasPrefix(strings.TrimSpace(n.Name), text)
	}
}

func (p *player) click(at kit.Point, button kit.Buttons, mods kit.Modifiers, clicks int) {
	l := p.h.Loop
	l.Send(kit.PointerEvent{Action: kit.PointerMove, Pos: at, Mods: mods})
	for i := 1; i <= clicks; i++ {
		l.Send(kit.PointerEvent{Action: kit.PointerDown, Pos: at, Button: button, Buttons: button, Clicks: i, Mods: mods})
		l.Send(kit.PointerEvent{Action: kit.PointerUp, Pos: at, Button: button, Clicks: i, Mods: mods})
	}
	p.h.Settle()
}

func (p *player) clickNode(match func(kit.SemanticNode) bool, what string) bool {
	n, ok := p.find(match)
	if !ok {
		p.note("no %s", what)
		return false
	}
	p.click(n.Bounds.Center(), kit.ButtonLeft, 0, 1)
	return true
}

// menuItem clicks the open popup menu's item named text.
func (p *player) menuItem(text string) {
	top := p.h.Host.Top()
	if top == nil {
		p.note("no menu for %q", text)
		return
	}
	sem, ok := top.Root.(interface{ Semantics() kit.SemanticTree })
	if !ok || sem.Semantics() == nil {
		p.note("menu without semantics for %q", text)
		return
	}
	for _, n := range nodes(sem.Semantics()) {
		if (n.Role == kit.RoleMenuItem || n.Role == kit.RoleOption) && strings.HasPrefix(strings.TrimSpace(n.Name), text) {
			top.Click(n.Bounds.Center())
			p.h.Settle()
			return
		}
	}
	p.note("no menu item %q", text)
}

func (p *player) row(name string) (kit.SemanticNode, bool) {
	n, ok := p.find(func(n kit.SemanticNode) bool { return n.Role == kit.RoleRow && strings.Contains(n.Name, name) })
	if !ok {
		p.note("no row %q", name)
	}
	return n, ok
}

func (p *player) setValue(match func(kit.SemanticNode) bool, text, what string) {
	n, ok := p.find(match)
	if !ok {
		p.note("no field %s", what)
		return
	}
	if err := p.h.Window.Semantics().Perform(kit.ActionRequest{Node: n.ID, Action: kit.ActionSetValue, Text: text}); err != nil {
		p.note("set %s: %v", what, err)
	}
	p.h.Settle()
}

// setFirstField sets the first text field (or combo box field) of the open dialog, the
// field the page's selectors name in every state the player knows.
func (p *player) setFirstField(text, what string) {
	all := nodes(p.h.Window.Semantics())
	in := false
	for _, n := range all {
		if n.Role == kit.RoleDialog || n.Role == kit.RoleAlertDialog {
			in = true
		}
		if in && isField(n) && n.States&kit.NodeReadOnly == 0 {
			if err := p.h.Window.Semantics().Perform(kit.ActionRequest{Node: n.ID, Action: kit.ActionSetValue, Text: text}); err != nil {
				p.note("set %s: %v", what, err)
			}
			p.h.Settle()
			return
		}
	}
	p.note("no field %s", what)
}

func isField(n kit.SemanticNode) bool {
	return n.Role == kit.RoleTextField || n.Role == kit.RoleComboBox
}

func (p *player) step(st scene.Step) {
	h := p.h
	switch st.Kind {
	case scene.StepKey:
		switch st.Key {
		case "n":
			// Add with its background work on goroutines, as the window runs it (the
			// harness's Go is synchronous, which the page's retry wait would block)
			h.App.Dialogs.Show(add.New(h.App, views.AddRequest{}, add.Options{Go: p.async}))
		case ",":
			h.App.Open.Settings(views.SettingsRequest{})
		case "k":
			h.App.Open.Palette()
		case "Delete":
			h.Key(kit.KeyDelete, 0)
		default:
			p.note("key %q", st.Key)
		}
		h.Settle()
	case scene.StepMoreActions:
		p.clickNode(named(kit.RoleButton, "More actions"), "More actions")
	case scene.StepClick:
		switch {
		case strings.Contains(st.Selector, "menuitem"):
			p.menuItem(st.Text)
		case strings.Contains(st.Selector, "role=tab"):
			p.clickNode(named(kit.RoleTab, st.Text), "tab "+st.Text)
		case strings.Contains(st.Selector, "role=option"):
			p.menuItem(st.Text)
		case st.Selector == "label" || strings.HasSuffix(st.Selector, " label"):
			p.clickNode(func(n kit.SemanticNode) bool {
				return (n.Role == kit.RoleRadioButton || n.Role == kit.RoleTab || n.Role == kit.RoleCheckBox) && strings.HasPrefix(strings.TrimSpace(n.Name), st.Text)
			}, "label "+st.Text)
		case strings.Contains(st.Selector, ".queue"):
			p.clickNode(func(n kit.SemanticNode) bool {
				return (n.Role == kit.RoleOption || n.Role == kit.RoleListItem || n.Role == kit.RoleButton || n.Role == kit.RoleRow) && strings.HasPrefix(strings.TrimSpace(n.Name), st.Text)
			}, "queue "+st.Text)
		default:
			p.clickNode(named(kit.RoleButton, st.Text), "button "+st.Text)
		}
	case scene.StepClickSelector:
		if strings.Contains(st.Selector, "more-toggle") {
			p.clickNode(named(kit.RoleButton, "More options"), "More options")
		} else {
			p.note("clickSel %s", st.Selector)
		}
	case scene.StepSelectRow, scene.StepCtrlRow, scene.StepContextRow, scene.StepDoubleRow:
		n, ok := p.row(st.Text)
		if !ok {
			return
		}
		if n.States&kit.NodeOffscreen != 0 {
			// the page's rows are all in its DOM; the native list scrolls a row in first
			_ = h.Window.Semantics().Perform(kit.ActionRequest{Node: n.ID, Action: kit.ActionScrollIntoView})
			h.Settle()
			if n, ok = p.row(st.Text); !ok {
				return
			}
		}
		at := n.Bounds.Min.Add(kit.Point{X: 40, Y: 10})
		switch st.Kind {
		case scene.StepSelectRow:
			p.click(at, kit.ButtonLeft, 0, 1)
		case scene.StepCtrlRow:
			p.click(at, kit.ButtonLeft, kit.ModMeta, 1)
		case scene.StepContextRow:
			p.click(at, kit.ButtonRight, 0, 1)
		case scene.StepDoubleRow:
			p.click(at, kit.ButtonLeft, 0, 2)
		}
	case scene.StepSearch:
		p.setValue(func(n kit.SemanticNode) bool {
			return n.Role == kit.RoleTextField && strings.Contains(n.Name, "Search")
		}, st.Text, "search")
	case scene.StepTypeIn:
		p.setFirstField(st.Text, st.Selector)
	case scene.StepFindMedia:
		p.setFirstField(st.Text, "stream address")
		p.clickNode(named(kit.RoleButton, "Find qualities"), "Find qualities")
		p.tick(6 * time.Second)
	case scene.StepWait:
		h.Advance(time.Duration(st.Ms) * time.Millisecond)
	case scene.StepWaitFor:
		p.tick(time.Duration(st.Ms) * time.Millisecond / 2)
	case scene.StepClearFolder:
		// the folder field: the last monospaced field (the Save to path)
		n, ok := p.find(func(n kit.SemanticNode) bool {
			return n.Role == kit.RoleTextField && strings.Contains(n.Name, "Save to")
		})
		if !ok {
			p.note("no folder field")
			return
		}
		_ = h.Window.Semantics().Perform(kit.ActionRequest{Node: n.ID, Action: kit.ActionSetValue, Text: ""})
		h.Settle()
	case scene.StepFont:
	default:
		p.note("step %v", st.Kind)
	}
}

// jsSteps are fx's steps as the page plays them (scripts/measure-dialogs.mjs's expressions;
// a returned string is a failure).
func jsSteps(fx scene.FixtureState) []string {
	q := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	row := func(n string) string {
		return `[...document.querySelectorAll('[role=row][data-id]')].find((r) => r.textContent.includes(` + q(n) + `))`
	}
	var out []string
	// the confirm states wait for the dialog that opens by itself; the others close a
	// notice dialog the simulated daemon may open at start (unless it is a Confirm).
	if fx.Confirm {
		out = append(out, `for (let i = 0; i < 40; i++) { if (document.querySelector('[role=dialog] h2')?.textContent === 'Confirm download') return true; await new Promise(r => setTimeout(r, 100)); } return 'no Confirm dialog';`)
	} else {
		out = append(out, `await new Promise(r => setTimeout(r, 600)); for (let i = 0; i < 3; i++) { const b = document.querySelector('[role=dialog] button[aria-label=Close],[role=alertdialog] button[aria-label=Close]'); if (!b) break; b.click(); await new Promise(r => setTimeout(r, 400)); } return true;`)
	}
	for _, st := range fx.Steps {
		var js string
		switch st.Kind {
		case scene.StepKey:
			js = `document.activeElement?.blur?.(); window.dispatchEvent(new KeyboardEvent('keydown', { key: ` + q(st.Key) + `, bubbles: true, cancelable: true, ctrlKey: ` + fmt.Sprint(st.Ctrl) + ` })); return true;`
		case scene.StepClick:
			js = `const el = [...document.querySelectorAll(` + q(st.Selector) + `)].find((e) => e.textContent.trim().startsWith(` + q(st.Text) + `)); if (!el) return 'missing ' + ` + q(st.Text) + `; el.focus?.({ focusVisible: false }); el.click(); return true;`
		case scene.StepClickSelector:
			js = `const el = document.querySelector(` + q(st.Selector) + `); if (!el) return 'missing sel'; el.focus?.({ focusVisible: false }); el.click(); return true;`
		case scene.StepSelectRow:
			js = `const r = ` + row(st.Text) + `; if (!r) return 'missing row'; const c = r.querySelector('[role=gridcell]') || r; c.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, button: 0 })); c.dispatchEvent(new MouseEvent('mouseup', { bubbles: true, button: 0 })); c.click(); return true;`
		case scene.StepCtrlRow:
			js = `const r = ` + row(st.Text) + `; if (!r) return 'missing row'; (r.querySelector('[role=gridcell]') || r).dispatchEvent(new MouseEvent('click', { bubbles: true, ctrlKey: true })); return true;`
		case scene.StepContextRow:
			js = `const r = ` + row(st.Text) + `; if (!r) return 'missing row'; const b = r.getBoundingClientRect(); r.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientX: b.left + 40, clientY: b.top + 10 })); return true;`
		case scene.StepDoubleRow:
			js = `const r = ` + row(st.Text) + `; if (!r) return 'missing row'; (r.querySelector('[role=gridcell]') || r).dispatchEvent(new MouseEvent('dblclick', { bubbles: true })); return true;`
		case scene.StepSearch:
			js = `const i = document.querySelector('input[type=search]'); if (!i) return 'missing search'; i.value = ` + q(st.Text) + `; i.dispatchEvent(new Event('input', { bubbles: true })); return true;`
		case scene.StepTypeIn:
			js = `const el = document.querySelector(` + q(st.Selector) + `); if (!el) return 'missing field'; el.focus(); el.value = ` + q(st.Text) + `; el.dispatchEvent(new Event('input', { bubbles: true })); return true;`
		case scene.StepMoreActions:
			js = `const b = document.querySelector('button[aria-label="More actions"]'); if (!b) return 'missing menu'; b.click(); return true;`
		case scene.StepClearFolder:
			js = `const i = [...document.querySelectorAll('[role=dialog] input.mono')].pop(); if (!i) return 'missing folder'; i.value = ''; i.dispatchEvent(new Event('input', { bubbles: true })); return true;`
		case scene.StepWait:
			js = fmt.Sprintf(`await new Promise(r => setTimeout(r, %d)); return true;`, st.Ms)
		case scene.StepFindMedia:
			js = `const i = [...document.querySelectorAll('[role=dialog] input')].find((x) => (x.placeholder || '').startsWith('https://www.youtube.com')); if (!i) return 'missing input'; i.focus(); i.value = ` + q(st.Text) + `; i.dispatchEvent(new Event('input', { bubbles: true })); await new Promise(r => setTimeout(r, 200));` +
				` const b = [...document.querySelectorAll('[role=dialog] button')].find((e) => e.textContent.trim().startsWith('Find qualities')); if (!b) return 'missing Find'; b.click();` +
				` for (let k = 0; k < 40; k++) { await new Promise(r => setTimeout(r, 400)); if (document.querySelector('[role=dialog] ` + st.Selector + `')) { await new Promise(r => setTimeout(r, 700)); return true; } } return 'never ` + st.Selector + `';`
		case scene.StepWaitFor:
			js = fmt.Sprintf(`for (let k = 0; k < %d; k++) { await new Promise(r => setTimeout(r, 200)); if (document.querySelector('[role=dialog] %s')) return true; } return 'never';`, max(st.Ms/200, 1), st.Selector)
		case scene.StepFont:
			continue
		}
		out = append(out, js)
	}
	return out
}
