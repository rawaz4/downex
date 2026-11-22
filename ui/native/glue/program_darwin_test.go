//go:build darwin && cgo

package glue

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/host"
	"downex/ui/kit"
	"downex/ui/mac"
	"downex/ui/mac/popuptest"
	"downex/ui/state/statetest"
	"downex/ui/views"
)

// The program's real paths on this Mac, each in a child process of this test binary (TestMain
// runs its part, by $GLUE_HELPER): a host as the window process runs it (Start, Run, a
// window attached with its prefs and theme, the fake daemon of statetest), driven by
// commands on its standard input. Every file, lock and socket is the test's own; nothing
// talks to downexd, the user's pasteboard or their Downex data.

const (
	envHelper = "GLUE_HELPER" // the child's part: host, second, wails-standin
	envLock   = "GLUE_LOCK"   // the lock id
	envDir    = "GLUE_DIR"    // the files' folder
	envLocks  = "GLUE_LOCKS"  // the locks' folder
	envArgs   = "GLUE_ARGS"   // the child's arguments, JSON
)

func init() {
	if os.Getenv(envHelper) != "" {
		runtime.LockOSThread() // the child's main goroutine is its AppKit thread
	}
}

func TestMain(m *testing.M) {
	if h := os.Getenv(envHelper); h != "" {
		os.Exit(helperMain(h))
	}
	os.Exit(m.Run())
}

func helperMain(part string) int {
	var args []string
	if s := os.Getenv(envArgs); s != "" {
		if err := json.Unmarshal([]byte(s), &args); err != nil {
			return 11
		}
	}
	switch part {
	case "host":
		return hostMain(args)
	case "second":
		_, handed, err := Start(Config{
			Args: args, LockID: os.Getenv(envLock), Dir: os.Getenv(envDir), LogDir: os.Getenv(envDir),
			LockDir: os.Getenv(envLocks), Daemon: statetest.NewDaemon(), NoMemoryTrim: true,
			Role: ptr(RoleAll), Wait: 300 * time.Millisecond, WebView2Dir: os.Getenv(envDir),
		})
		fmt.Printf("handed=%v err=%v\n", handed, err)
		if !handed || err != nil {
			return 4
		}
		return 0
	case "wails-standin":
		return wailsStandin(args)
	case "attach-panic":
		return attachPanicMain(args)
	}
	return 12
}

func ptr[T any](v T) *T { return &v }

// riskyID is the download the host's fake daemon has completed: an installer package.
const riskyID = 7

// hostMain is a window process: Start, then Run with one window, which answers the commands
// read from standard input, one line of output each.
func hostMain(args []string) int {
	dir := os.Getenv(envDir)
	file := filepath.Join(dir, "setup.pkg")
	if err := os.WriteFile(file, []byte("xar!"), 0o644); err != nil {
		return 20
	}
	d := statetest.NewDaemon(&downexv1.Download{Id: riskyID, Dir: dir, Filename: "setup.pkg", Size: 4, Received: 4,
		State: downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED})
	h, handed, err := Start(Config{
		Args: args, Version: "0.0.0-test", LockID: os.Getenv(envLock), Dir: dir, LogDir: dir,
		LockDir: os.Getenv(envLocks), Daemon: d, NoMemoryTrim: true, Role: ptr(RoleAll), WebView2Dir: dir,
	})
	if err != nil || handed {
		fmt.Println("start:", handed, err)
		return 21
	}
	defer h.Close()
	started := false
	err = h.Run(RunOptions{
		AboutTitle: "Downex", AboutMessage: "Version test",
		Open: func(a *mac.App, gw *Window) (*mac.Window, error) {
			pf, err := gw.OpenPrefs()
			if err != nil {
				return nil, err
			}
			theme := gw.NewTheme(pf)
			gw.Defer(func() { theme.Close(); pf.Close() })
			kw := kit.NewWindow(kit.NewLabel("glue", views.FontBody, kit.TextOptions{}), kit.WindowOptions{Title: "Downex glue test"})
			w, err := a.NewWindow(kw, mac.WindowOptions{Title: "Downex glue test", Size: kit.Size{W: 520, H: 360}})
			if err != nil {
				return nil, err
			}
			g, err := gw.Attach(w, kw, AttachOptions{Theme: theme, Prefs: pf})
			if err != nil {
				return nil, err
			}
			if !started {
				started = true
				onUI := func(f func()) {
					done := make(chan struct{})
					a.Post(func() { f(); close(done) })
					<-done
				}
				go func() {
					time.Sleep(1200 * time.Millisecond) // the window shows (and RaiseIfInactive's 0.5 s are over)
					fmt.Println("ready")
					hostCommands(gw, w, g, onUI)
				}()
			}
			return w, nil
		},
	})
	if err != nil {
		fmt.Println("run:", err)
		return 22
	}
	return 0
}

// hostCommands answers the commands that come in on standard input, one line each.
func hostCommands(gw *Window, w *mac.Window, g *Glue, onUI func(func())) {
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 {
			continue
		}
		fmt.Println(strings.ReplaceAll(hostCommand(f, gw, w, g, onUI), "\n", `\n`))
	}
	// Standard input ended (the test is done): the windows close as their own close does
	// and Run returns.
	onUI(gw.h.CloseAll)
}

func hostCommand(f []string, gw *Window, w *mac.Window, g *Glue, onUI func(func())) string {
	switch f[0] {
	case "state":
		active, hidden := mac.AppState()
		key, visible, mini := w.FrontState()
		var theme host.ThemeChoice
		onUI(func() { theme = g.theme.Choice() })
		return fmt.Sprintf("active=%v hidden=%v key=%v visible=%v mini=%v theme=%s", active, hidden, key, visible, mini, theme)
	case "raise": // mac.RaiseWindow's state of the window
		var following, space bool
		onUI(func() { following, space = w.RaiseState() })
		_, visible, mini := w.FrontState()
		return fmt.Sprintf("space=%v visible=%v mini=%v following=%v", space, visible, mini, following)
	case "minimize":
		onUI(func() { w.Command(kit.WindowMinimize) })
		return "ok"
	case "set":
		if err := gw.prefs.Set(f[1], f[2]); err != nil {
			return "error " + err.Error()
		}
		return "ok"
	case "quit-event":
		return fmt.Sprint("sent=", mac.SendQuitEvent())
	case "menukey": // ⌘ + f[1] to the menu bar
		var took bool
		onUI(func() { took = mac.PerformMenuKey(f[1], 1<<20) })
		return fmt.Sprint("took=", took)
	case "terminate":
		mac.Terminate()
		return "ok"
	case "pick":
		res := make(chan string, 1)
		go func() {
			p, err := g.Pickers().PickFolder(context.Background(), "Choose a folder", f[1])
			res <- fmt.Sprintf("path=%q err=%v", p, err)
		}()
		st := waitModal("sheet", 5*time.Second)
		pressed := mac.PressModalButton("Cancel")
		select {
		case r := <-res:
			return fmt.Sprintf("%s => Cancel %v: %s", st, pressed, r)
		case <-time.After(5 * time.Second):
			return fmt.Sprintf("%s => Cancel %v: no answer; front now %q", st, pressed, mac.ModalState())
		}
	case "risky":
		res := make(chan string, 1)
		go func() {
			err := gw.Session.Files().OpenFile(context.Background(), riskyID)
			res <- fmt.Sprintf("err=%v", err)
		}()
		st := waitModal("modal", 5*time.Second)
		mac.PostKey("\x1b", 53, 0) // Escape: nothing, as in Wails
		time.Sleep(500 * time.Millisecond)
		after := mac.ModalState()
		mac.PressModalButton("No")
		return st + " => after Escape: " + after + " => No: " + <-res
	case "yield":
		start := time.Now()
		mac.YieldActivation(f[1])
		return fmt.Sprint("took=", time.Since(start).Milliseconds())
	}
	return "unknown"
}

func waitModal(prefix string, d time.Duration) string {
	end := time.Now().Add(d)
	for {
		s := mac.ModalState()
		if strings.HasPrefix(s, prefix) || time.Now().After(end) {
			return s
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// wailsStandin is one of today's Wails windows: it holds its lock (Wails' flock) and prints
// the object of the distributed notification named after it, as Wails' second-start
// observer receives it.
func wailsStandin(args []string) int {
	l, held, err := claimLock(lockPath(os.Getenv(envLocks), os.Getenv(envLock)))
	if held || err != nil {
		return 30
	}
	defer l.Close()
	a, err := mac.NewApp(mac.AppOptions{})
	if err != nil {
		return 31
	}
	defer a.Close()
	kw := kit.NewWindow(kit.NewLabel("wails", views.FontBody, kit.TextOptions{}), kit.WindowOptions{})
	if _, err := a.NewWindow(kw, mac.WindowOptions{Title: "stand-in", Size: kit.Size{W: 200, H: 100}, NoActivate: true}); err != nil {
		return 32
	}
	cancel := mac.ObserveDistributed(os.Getenv(envLock), func(o string) {
		fmt.Println(o)
		a.Quit()
	})
	defer cancel()
	fmt.Println("ready")
	time.AfterFunc(15*time.Second, func() { a.Post(a.Quit) })
	_ = a.Run()
	return 0
}

// panicRoot is a window root whose Attach panics (a defect in a view).
type panicRoot struct{ kit.Root }

func (panicRoot) Attach(kit.Host) { panic("attach failed") }

// attachPanicMain is a window process whose only window's root panics in Attach: the window
// closes (windowMade gave the host the window before Attach, so panicked finds it), the open
// fails and Run returns, with no window on screen.
func attachPanicMain(args []string) int {
	dir := os.Getenv(envDir)
	h, handed, err := Start(Config{
		Args: args, Version: "0.0.0-test", LockID: os.Getenv(envLock), Dir: dir, LogDir: dir,
		LockDir: os.Getenv(envLocks), Daemon: statetest.NewDaemon(), NoMemoryTrim: true, Role: ptr(RoleAll), WebView2Dir: dir,
	})
	if err != nil || handed {
		fmt.Println("start:", handed, err)
		return 21
	}
	defer h.Close()
	visible := -1
	err = h.Run(RunOptions{
		AboutTitle: "Downex", AboutMessage: "Version test",
		Open: func(a *mac.App, gw *Window) (*mac.Window, error) {
			w, err := a.NewWindow(panicRoot{}, mac.WindowOptions{Title: "Downex glue panic", Size: kit.Size{W: 300, H: 200}})
			visible = popuptest.VisibleTitled("Downex glue panic")
			return w, err
		},
	})
	fmt.Printf("run failed=%v visible=%d windows=%d\n", err != nil, visible, len(h.windows))
	return 0
}
