//go:build windows

package glue

import (
	"path/filepath"
	"time"

	"downex/ui/kit"
	"downex/ui/state"
	"downex/ui/state/prefs"
	"downex/ui/views"
	"downex/ui/win"
)

// Glue is a Window's glue to its win.Window: made by Attach, ended once the window has
// closed (Window.Detach).
type Glue struct {
	p   *Window
	w   *win.Window
	kw  *kit.Window
	dlg *win.Dialogs

	theme  *state.Theme
	prefs  *prefs.File
	watch  *win.FileWatch
	follow func() // posted on a change of ui-theme.json (one function for every change)
}

// AttachOptions are what Attach connects besides the window.
type AttachOptions struct {
	// Theme is NewTheme's store: Attach follows ui-theme.json into it, and the system's
	// light or dark apps (Environment).
	Theme *state.Theme
	// Prefs is OpenPrefs' file: a change another process makes is taken at once (it polls
	// every prefs.PollInterval besides).
	Prefs *prefs.File
}

// Attach connects the window's part to its window w (kw is w's kit window), on the UI
// thread, once the window exists:
//   - the system dialogs: the folder and file pickers (Pickers, Platform) and the
//     risky-file Yes/No box (host Files.SetConfirm), owned by the window while it shows;
//   - ui-theme.json and ui-prefs.json followed (o.Theme, o.Prefs).
//
// The host brings the window to the front when a later start asks for it, and gives the
// main window that start's "--confirm <id>" requests (Session.SecondInstance; the
// composition calls Confirmations().Ready).
func (p *Window) Attach(w *win.Window, kw *kit.Window, o AttachOptions) (*Glue, error) {
	if p == nil || p.Session == nil {
		return nil, ErrClosed
	}
	if w == nil || kw == nil {
		return nil, errorf("Attach without a window")
	}
	if p.glue != nil {
		return nil, errorf("Attach called twice")
	}
	d, err := win.NewDialogs(w)
	if err != nil {
		return nil, err
	}
	g := &Glue{p: p, w: w, kw: kw, dlg: d, theme: o.Theme, prefs: o.Prefs}
	p.glue = g
	p.win = w
	p.Session.Files().SetConfirm(d.Confirm)
	p.ui.attach(kw.Post) // the stores hear the prefs file's changes on the UI thread
	g.followFiles()
	return g, nil
}

// Dialogs are the window's system dialogs.
func (g *Glue) Dialogs() *win.Dialogs { return g.dlg }

// Pickers are views.HostPlatform's: the window's folder and file pickers, the browser for
// an https address, and the window's clipboard.
func (g *Glue) Pickers() views.Pickers {
	return views.Pickers{
		PickFolder:   g.dlg.PickFolder,
		PickFile:     g.dlg.PickFile,
		OpenExternal: win.OpenExternal,
		OpenPage:     win.OpenPage,
		CopyText:     views.WindowClipboard(g.kw),
	}
}

// Platform is the views' Platform of this process (views.Options.Platform).
func (g *Glue) Platform() *views.HostPlatform {
	return &views.HostPlatform{Session: g.p.Session, Pickers: g.Pickers()}
}

// Environment takes the system's settings (kit.WindowOptions.OnEnvironment): the System
// theme follows the system's light or dark apps. A contrast theme, the animation effects and
// the text size reach the views through the window's environment itself.
func (g *Glue) Environment(e kit.Environment) {
	if g != nil && g.theme != nil {
		g.theme.SetSystemDark(e.Dark)
	}
}

// followFiles watches ui-theme.json and ui-prefs.json: another process's change reaches
// this window at once (the folder's change notification), within a second at the latest.
func (g *Glue) followFiles() {
	f := g.p.Files
	dir := filepath.Dir(f.Theme)
	if f.Theme == "" {
		dir = filepath.Dir(f.Prefs)
		if f.Prefs == "" {
			return
		}
	}
	themeName, prefsName := filepath.Base(f.Theme), filepath.Base(f.Prefs)
	var names []string
	if f.Theme != "" && g.theme != nil && g.p.theme != nil {
		names = append(names, themeName)
	}
	if f.Prefs != "" && g.prefs != nil && filepath.Dir(f.Prefs) == dir {
		names = append(names, prefsName)
	}
	if len(names) == 0 {
		return
	}
	link, theme, kw := g.p.theme, g.theme, g.kw
	g.follow = func() { link.follow(theme) }
	pf := g.prefs
	g.watch = win.WatchFiles(dir, names, followPoll, func(name string) {
		switch name {
		case themeName:
			_ = kw.Post(g.follow) // a closed window takes nothing
		case prefsName:
			pf.Reload() // the file's IO here; its watchers hear it on the UI thread (UIStorage)
		}
	})
}

// followPoll is how often the watched files are read when the folder sends no notification.
const followPoll = 500 * time.Millisecond

func (g *Glue) close() {
	g.watch.Stop()
	g.p.ui.detach()
	g.p.Session.Files().SetConfirm(nil)
	g.dlg.Close()
	g.w.DisableDrop()
}
