// Package glue is the native window process's operating-system glue (native-ui plan step 17):
// the launch contract today's window keeps (the single-instance lock and the hand-off of a
// later start's arguments, in the format Wails v2.17 speaks, so an old and a new
// downex-ui.exe hand over to each other in both directions), ui.log, the first contact with
// downexd through ui/host, the system dialogs (the folder and file pickers and the risky-file
// question), links dropped from a browser, and the theme and prefs followed across processes
// and from the system's settings.
//
// One process hosts every window (native-ui plan step 20): the first start becomes the Host,
// whatever it asks for, and holds the main window's lock; every later start of this exe
// hands its arguments to the host and exits, and the host opens, or brings to the front,
// the main window or that download's progress window (one per download). The process lives
// while any window is open: the main window and the progress windows close on their own,
// and the last one ends it. downexd, the tray, the browser hand-offs and the installer keep
// starting "downex-ui.exe", "--progress <id>" and "--confirm <id>" as before.
//
// Upgrades, both ways (Start, host.HostHandoffKind): a start sends the host its own dwData,
// which only a host answers (host.HostAnswer); today's windows (Wails v2.17, 0.9.2's native
// window) leave it alone, and the start then does what theirs would: "--progress <id>"
// takes that download's own lock (a progress process of its own, a legacy Host), the rest
// goes to today's main window as dwData 1542. The host takes 1542 from older starts on the
// main lock, and holds each open progress window's own lock, so an older build's start
// finds its window; a request for a download whose window an older build's process shows
// goes to that process. A host whose last window has closed answers host.HostClosing, and
// the start tries again until it can become the host, so no request is lost.
//
// Closing: each window closes as the user closes it (its title bar, Alt+F4, the taskbar:
// SC_CLOSE). A WM_CLOSE from another program closes every window (Host.CloseAll): the
// installer stops the window with Process.CloseMainWindow, which reaches one top-level
// window of the process (then ends it after 5 s), so every window closes, the main window
// stores its size and the process ends at once; the hand-off window's WM_CLOSE does the
// same. At the end of the session (WM_ENDSESSION) every window closes before the message
// returns, as the process may be ended at any time after it; a window with one of its
// dialogs open (inside a call into its views) cannot close under that call, so it stays and
// only its prefs are written. Once the last window has closed and the hand-offs taken
// meanwhile are shown, the host lets go of the main lock and its hand-off window at once: a
// later start becomes the next host. A start whose host's hand-off window disappears while
// it answers looks again, rather than taking it for an older build's.
//
// Isolation: each window has its own session (its "--confirm" requests, its events bridge:
// a progress window watches its download only), prefs file, theme store, system dialogs,
// DPI and title; they share the UI thread, its COM apartment, the drawing resources and the
// daemon client. A panic in one window's procedure closes that window only; a failure or a
// panic while a window opens closes the window it made (win.AppOptions.OnWindowMade), Attach
// or not.
//
// The entry points, in the order a window process calls them on its UI thread (the main
// thread, locked):
//
//	h, handedOver, err := glue.Start(glue.Config{Args: os.Args[1:], Version: version})
//	if handedOver { os.Exit(0) }       // the running host took the arguments
//	defer h.Close()
//	h.Run(glue.RunOptions{Open: func(a *win.App, w *glue.Window) (*win.Window, error) {
//		pf, _ := w.OpenPrefs()          // the window's own prefs file
//		theme := w.NewTheme(w.UIStorage(pf)) // the state.Theme its views draw with
//		... build the views.App and its kit.Window, open the win.Window on a ...
//		g, err := w.Attach(ww, kw)      // pickers, the risky-file box, files followed
//		g.EnableDrop(main)              // views/app.Main takes dropped links
//		w.Defer(...)                    // what goes once the window has closed
//		return ww, nil
//	}})
//
// Everything above is the Windows host (*_windows.go); the names (names.go) and the theme
// link (theme.go) are platform-free. macOS has its own host with the same API over ui/mac
// (*_darwin.go, darwin && cgo): one host per app bundle (start_darwin.go), Wails' flock and
// distributed-notification hand-off for today's windows, and a user-only unix socket between
// native starts and their host (instance_unix.go).
package glue
