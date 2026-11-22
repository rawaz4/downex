//go:build darwin && cgo

// Package progtest tests ui/mac's program part (program.go) with real AppKit windows: its own
// package, so that its TestMain (the main thread serves the tests) is the only one.
package progtest

import (
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"downex/ui/kit"
	"downex/ui/mac"
)

func init() { runtime.LockOSThread() }

var (
	app   *mac.App
	calls = make(chan func())
	menu  = make(chan struct {
		c mac.MenuCommand
		w *mac.Window
	}, 8)
)

// TestMain serves the tests' AppKit calls on the main thread (onMain).
func TestMain(m *testing.M) {
	a, err := mac.NewApp(mac.AppOptions{Regular: os.Getenv(envRegular) != ""})
	if err != nil {
		panic(err)
	}
	app = a
	a.InstallProgram(mac.ProgramOptions{
		Settings: true, AboutTitle: "Downex", AboutMessage: "Version test\n\nA download manager.",
		OnMenu: func(c mac.MenuCommand, w *mac.Window) {
			menu <- struct {
				c mac.MenuCommand
				w *mac.Window
			}{c, w}
		},
	})
	done := make(chan int)
	go func() { done <- m.Run() }()
	for {
		select {
		case f := <-calls:
			f()
		case code := <-done:
			os.Exit(code)
		}
	}
}

// onMain runs f on the main thread and waits.
func onMain(f func()) {
	ok := make(chan struct{})
	calls <- func() { f(); close(ok) }
	<-ok
}

// runFor runs the application's event loop (a window must be open) for d.
func runFor(t *testing.T, w *mac.Window, d time.Duration) {
	t.Helper()
	run++
	this := run
	// Only this run's own end: a timer of an earlier run that ended sooner must not end it.
	time.AfterFunc(d, func() {
		app.Post(func() {
			if run == this {
				app.Quit()
			}
		})
	})
	onMain(func() { _ = app.Run() })
	onMain(func() { run++ })
}

// run counts runFor's runs (main thread).
var run int

// root is a kit.Root that paints nothing and counts its frames.
type root struct {
	h      kit.Host
	frames int
	call   func(h kit.Host)
}

func (r *root) Attach(h kit.Host)                  { r.h = h }
func (r *root) Detach()                            {}
func (r *root) Frame(time.Time)                    { r.frames++ }
func (r *root) Paint(c kit.Canvas, dirty kit.Rect) {}
func (r *root) Event(e kit.Event) bool             { return false }
func (r *root) Semantics() kit.SemanticTree        { return nil }
func (r *root) FrameHit(p kit.Point) kit.FramePart { return 0 }

func open(t *testing.T, o mac.WindowOptions) (*mac.Window, *root) {
	t.Helper()
	r := &root{}
	var w *mac.Window
	var err error
	o.NoActivate = true
	onMain(func() { w, err = app.NewWindow(r, o) })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { onMain(func() { w.CloseNow() }) })
	return w, r
}

// TestTheMenuBarIsTodays: the menu bar of ui/menu.go's macMenu with Wails' Edit and Window
// menus.
func TestTheMenuBarIsTodays(t *testing.T) {
	var got string
	onMain(func() { got = mac.MenuBar() })
	const cmd, shift, opt, ctrl = 1 << 20, 1 << 17, 1 << 19, 1 << 18
	want := []string{
		"Downex\tAbout Downex\t\t0\tabout:",
		"Downex\t-\t\t0\t",
		"Downex\tSettings…\t,\t1048576\tsettings:",
		"Downex\t-\t\t0\t",
		"Downex\tHide Downex\th\t1048576\thide:",
		"Downex\t-\t\t0\t",
		"Downex\tQuit Downex\tq\t1048576\tquit:",
		"File\tClose Window\tw\t1048576\tcloseWindow:",
		"Edit\tUndo\tz\t1048576\tundo:",
		"Edit\tRedo\tz\t1179648\tredo:",
		"Edit\t-\t\t0\t",
		"Edit\tCut\tx\t1048576\tcut:",
		"Edit\tCopy\tc\t1048576\tcopy:",
		"Edit\tPaste\tv\t1048576\tpaste:",
		"Edit\tPaste and Match Style\tv\t1703936\tpasteAsRichText:",
		"Edit\tDelete\t\b\t0\tdelete:",
		"Edit\tSelect All\ta\t1048576\tselectAll:",
		"Edit\t-\t\t0\t",
		"Edit\tSpeech\t\t1048576\tsubmenuAction:",
		"Edit > Speech\tStart Speaking\t\t0\tstartSpeaking:",
		"Edit > Speech\tStop Speaking\t\t0\tstopSpeaking:",
		"Window\tMinimize\tm\t1048576\tperformMiniaturize:",
		"Window\tZoom\t\t0\tperformZoom:",
		"Window\t-\t\t0\t",
		"Window\tFull Screen\tf\t1310720\tenterFullScreenMode:",
	}
	_ = []int{cmd, shift, opt, ctrl}
	// AppKit adds its own items to a menu titled Edit (AutoFill, Dictation, Emoji & Symbols,
	// after a separator) and the Fn-F twin of Full Screen (a hidden alternate), as it did to
	// Wails' menus.
	appKit := func(l string) bool {
		for _, s := range []string{"AutoFill", "startDictation:", "orderFrontCharacterPalette:", "\tf\t8388608\t"} {
			if strings.Contains(l, s) {
				return true
			}
		}
		return false
	}
	// Nothing else: AppKit adds neither a window list nor its tab and tiling items to a Window
	// menu that is not NSApp.windowsMenu (Wails never sets it).
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	var ours []string
	for i, l := range lines {
		if appKit(l) || (l == "Edit\t-\t\t0\t" && i+1 < len(lines) && appKit(lines[i+1])) {
			continue
		}
		ours = append(ours, l)
	}
	if strings.Join(ours, "\n") != strings.Join(want, "\n") {
		t.Errorf("menu bar:\n%s\nwant:\n%s", strings.Join(ours, "\n"), strings.Join(want, "\n"))
	}
	// Wails' menus keep every item enabled (WailsMenu: autoenablesItems NO).
	var auto string
	onMain(func() { auto = mac.MenuAutoenabling() })
	auto = strings.ReplaceAll(auto, "Edit > AutoFill\n", "") // AppKit's own submenu
	if auto != "" {
		t.Errorf("menus that AppKit enables by itself:\n%s", auto)
	}
}

// TestQuitAndCloseWindowGoToTheKeyWindow: ⌘Q, ⌘W and ⌘, reach Go with the key window.
func TestQuitAndCloseWindowGoToTheKeyWindow(t *testing.T) {
	w, _ := open(t, mac.WindowOptions{Title: "t", Size: kit.Size{W: 400, H: 300}})
	onMain(func() { w.BringToFront(true) })
	runFor(t, w, 300*time.Millisecond) // the window becomes key
	for _, k := range []struct {
		key  string
		want mac.MenuCommand
	}{{"q", mac.MenuQuit}, {"w", mac.MenuCloseWindow}, {",", mac.MenuSettings}} {
		var took bool
		onMain(func() { took = mac.PerformMenuKey(k.key, 1<<20) })
		if !took {
			t.Errorf("⌘%s not taken by the menu bar", k.key)
			continue
		}
		select {
		case got := <-menu:
			if got.c != k.want {
				t.Errorf("⌘%s: command %d", k.key, got.c)
			}
			if got.w != w && k.want != mac.MenuSettings {
				t.Errorf("⌘%s: window %p, want the key window %p", k.key, got.w, w)
			}
		case <-time.After(time.Second):
			t.Errorf("⌘%s did not reach Go", k.key)
		}
	}
}

// TestPlacementKeepsTheRestoredSize: a zoomed window reports the size it restores to, and a
// window opened zoomed restores to the size it was asked with.
func TestPlacementKeepsTheRestoredSize(t *testing.T) {
	w, _ := open(t, mac.WindowOptions{Title: "t", Size: kit.Size{W: 640, H: 420}})
	var p mac.Placement
	onMain(func() { p, _ = w.Placement() })
	if p.Normal != (kit.Size{W: 640, H: 420}) || p.Maximized || p.Minimized {
		t.Errorf("placement %+v", p)
	}
	onMain(func() { w.Command(kit.WindowMaximize) })
	runFor(t, w, 800*time.Millisecond)
	onMain(func() { p, _ = w.Placement() })
	if !p.Maximized || p.Normal != (kit.Size{W: 640, H: 420}) {
		t.Errorf("zoomed: placement %+v, size %v", p, w.Size())
	}

	z, _ := open(t, mac.WindowOptions{Title: "z", Size: kit.Size{W: 700, H: 500}, Zoomed: true})
	onMain(func() { p, _ = z.Placement() })
	if !p.Maximized || p.Normal != (kit.Size{W: 700, H: 500}) {
		t.Errorf("opened zoomed: placement %+v, size %v", p, z.Size())
	}
	if s := z.Size(); s.W <= 700 && s.H <= 500 {
		t.Errorf("opened zoomed at %v", s)
	}
}

// TestRaiseWindowFollowsForItsSeconds: RaiseWindow (a "--raise" window: a download the user
// just started in the browser) shows the window on the active Space without activating, and
// follows the other apps' activations for its seconds only; a later call starts them again,
// 0 seconds orders it front once, and a closed window is left alone.
func TestRaiseWindowFollowsForItsSeconds(t *testing.T) {
	w, _ := open(t, mac.WindowOptions{Title: "raised", Size: kit.Size{W: 300, H: 200}})
	onMain(func() { w.Command(kit.WindowMinimize) })
	runFor(t, w, 800*time.Millisecond)
	var following, space, key, visible, mini bool
	onMain(func() {
		mac.RaiseWindow(w, 0.6)
		following, space = w.RaiseState()
		key, _, _ = w.FrontState()
	})
	if !space {
		t.Error("a raised window does not move to the active Space")
	}
	if !following && !key { // a key window has nothing to follow
		t.Error("a raised window that is not key does not follow the activations")
	}
	runFor(t, w, 500*time.Millisecond) // deminiaturised
	onMain(func() { key, visible, mini = w.FrontState() })
	if !visible || mini {
		t.Errorf("raised: visible %v, minimised %v", visible, mini)
	}
	onMain(func() { mac.RaiseWindow(w, 0.6) }) // its 0.6 s start again
	runFor(t, w, 300*time.Millisecond)         // past the first call's 0.6 s
	onMain(func() { following, _ = w.RaiseState(); key, _, _ = w.FrontState() })
	if !following && !key {
		t.Error("the first call's end stopped the second call's following")
	}
	runFor(t, w, 600*time.Millisecond)
	onMain(func() { following, space = w.RaiseState() })
	if following || !space {
		t.Errorf("after its seconds: following %v (want false), on the active Space %v", following, space)
	}
	onMain(func() {
		mac.RaiseWindow(w, 0)
		following, _ = w.RaiseState()
	})
	if following {
		t.Error("RaiseWindow with 0 seconds follows the activations")
	}
	onMain(func() {
		mac.RaiseWindow(w, 5)
		w.CloseNow()
		mac.RaiseWindow(w, 5) // closed: nothing
		following, _ = w.RaiseState()
	})
	if following {
		t.Error("a closed window still follows the activations")
	}
}

// TestFrameSizeIsTheTitleBar: the frame is the title bar's height, before and after a
// window exists.
func TestFrameSizeIsTheTitleBar(t *testing.T) {
	w, _ := open(t, mac.WindowOptions{Title: "t", Size: kit.Size{W: 400, H: 300}})
	var f, s kit.Size
	onMain(func() { f, s = w.FrameSize(), mac.SystemFrameSize() })
	if f != s || f.W != 0 || f.H < 20 || f.H > 40 {
		t.Errorf("frame %v, system %v", f, s)
	}
}

// TestCloseNowWaitsForACall: a window inside a call into its root does not close under it.
func TestCloseNowWaitsForACall(t *testing.T) {
	w, _ := open(t, mac.WindowOptions{Title: "t", Size: kit.Size{W: 400, H: 300}})
	got := make(chan bool, 1)
	_ = w.Post(func() { got <- w.CloseNow() })
	runFor(t, w, 200*time.Millisecond)
	if <-got {
		t.Error("CloseNow closed a window inside a call")
	}
	var closed bool
	onMain(func() { closed = w.CloseNow() && w.Closed() })
	if !closed {
		t.Error("CloseNow outside a call")
	}
}

// TestAResizeInsideACallKeepsDrawing: the progress window's fit resizes inside a call; the
// window went on drawing (it once queued its tick for ever and hung the process).
func TestAResizeInsideACallKeepsDrawing(t *testing.T) {
	w, r := open(t, mac.WindowOptions{Title: "t", Size: kit.Size{W: 400, H: 300}})
	_ = w.Post(func() { w.Resize(kit.Size{W: 420, H: 280}) })
	ran := make(chan struct{}, 1)
	time.AfterFunc(100*time.Millisecond, func() { _ = w.Post(func() { ran <- struct{}{} }) })
	runFor(t, w, 400*time.Millisecond)
	select {
	case <-ran:
	default:
		t.Fatal("the window stopped serving its calls after a Resize inside one")
	}
	if s := w.Size(); s != (kit.Size{W: 420, H: 280}) {
		t.Errorf("size %v", s)
	}
	if r.frames < 2 {
		t.Errorf("%d frames", r.frames)
	}
}

// TestDistributedNotificationsReachTheObserver: Wails' hand-off (a distributed notification
// whose object is the JSON) reaches the host on the main thread.
func TestDistributedNotificationsReachTheObserver(t *testing.T) {
	w, _ := open(t, mac.WindowOptions{Title: "t", Size: kit.Size{W: 300, H: 200}})
	name := "downex-progtest-" + time.Now().Format("150405.000000")
	got := make(chan string, 1)
	var cancel func()
	onMain(func() {
		cancel = mac.ObserveDistributed(name, func(o string) {
			got <- o
			app.Quit()
		})
	})
	defer onMain(func() { cancel() })
	go mac.PostDistributed(name, `{"Args":["--confirm","5"],"WorkingDirectory":"/"}`)
	runFor(t, w, 3*time.Second)
	select {
	case o := <-got:
		if !strings.Contains(o, `"--confirm","5"`) {
			t.Errorf("object %q", o)
		}
	default:
		t.Fatal("the notification did not arrive")
	}
}

// TestTempDirIsTheUsersOwn: Wails' lock folder (NSTemporaryDirectory()).
func TestTempDirIsTheUsersOwn(t *testing.T) {
	d := mac.TempDir()
	if !strings.HasPrefix(d, "/var/folders/") && !strings.HasPrefix(d, "/private/var/folders/") {
		t.Errorf("temporary folder %q", d)
	}
	if mac.PreferredLanguage() == "" {
		t.Error("no preferred language")
	}
}
