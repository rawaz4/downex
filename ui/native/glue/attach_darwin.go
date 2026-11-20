//go:build darwin && cgo

package glue

import (
	"path/filepath"
	"time"

	"downex/internal/client"
	"downex/ui/kit"
	"downex/ui/mac"
	"downex/ui/state"
	"downex/ui/state/prefs"
	"downex/ui/views"
)

func clientLayout(exe string) (client.Layout, error) {
	return client.FindLayout(func() (string, error) { return exe, nil })
}

// Glue is a Window's glue to its mac.Window: made by Attach, ended by Window.Detach.
type Glue struct {
	p  *Window
	w  *mac.Window
	kw *kit.Window

	theme  *state.Theme
	prefs  *prefs.File
	watch  *fileWatch
	follow func()
	dlg    *mac.Dialogs // the open panels and the Yes/No alert, one at a time per window
}

// AttachOptions are what Attach connects besides the window.
type AttachOptions struct {
	Theme *state.Theme
	Prefs *prefs.File
}

// Attach connects the window's part to its window w (kw is w's kit window), on the main
// thread: the open panels and the risky-file Yes/No alert of today's Wails window, and
// ui-theme.json and ui-prefs.json followed.
func (p *Window) Attach(w *mac.Window, kw *kit.Window, o AttachOptions) (*Glue, error) {
	if p == nil || p.Session == nil {
		return nil, ErrClosed
	}
	if w == nil || kw == nil {
		return nil, errorf("Attach without a window")
	}
	if p.glue != nil {
		return nil, errorf("Attach called twice")
	}
	dlg, err := mac.NewDialogs(w)
	if err != nil {
		return nil, err
	}
	g := &Glue{p: p, w: w, kw: kw, theme: o.Theme, prefs: o.Prefs, dlg: dlg}
	p.glue = g
	p.win = w
	p.Session.Files().SetConfirm(dlg.Confirm)
	p.ui.attach(kw.Post)
	g.followFiles()
	return g, nil
}

// Pickers are views.HostPlatform's: the window's open panels (a sheet on the window, as
// Wails shows them), the browser for an https address, and the window's clipboard.
func (g *Glue) Pickers() views.Pickers {
	return views.Pickers{
		PickFolder:   g.dlg.PickFolder,
		PickFile:     g.dlg.PickFile,
		OpenExternal: mac.OpenExternal,
		OpenPage:     mac.OpenPage,
		CopyText:     views.WindowClipboard(g.kw),
	}
}

// Platform is the views' Platform of this process.
func (g *Glue) Platform() *views.HostPlatform {
	return &views.HostPlatform{Session: g.p.Session, Pickers: g.Pickers()}
}

// Environment takes the system's settings: the System theme follows macOS's appearance.
func (g *Glue) Environment(e kit.Environment) {
	if g != nil && g.theme != nil {
		g.theme.SetSystemDark(e.Dark)
	}
}

// followFiles watches ui-theme.json and ui-prefs.json (the folder's kqueue events, else a
// poll every followPoll).
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
	g.watch = watchFiles(dir, names, followPoll, func(name string) {
		switch name {
		case themeName:
			_ = kw.Post(g.follow)
		case prefsName:
			pf.Reload()
		}
	})
}

// followPoll is how often the watched files are read when the folder cannot be watched.
const followPoll = 500 * time.Millisecond

func (g *Glue) close() {
	g.watch.Stop()
	g.p.ui.detach()
	g.p.Session.Files().SetConfirm(nil)
	g.w.DisableDrop()
	g.dlg.Close()
}
