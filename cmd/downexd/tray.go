package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"connectrpc.com/connect"

	"downex/internal/client"
	"downex/internal/daemon"
	"downex/internal/filecheck"
	"downex/internal/model"
	"downex/internal/notify"
	"downex/internal/platform"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/tray"
)

// The notification-area icon of downexd (docs/DESKTOP-SPEC.md §3–§4). A runner
// follows the daemon in-process: the daemon's notices (Manager.Subscribe) produce the
// notifications (internal/notify) and carry settings changes (show_tray_icon and the
// notification switches apply live, without polling); Watch only signals that
// something changed, and the tooltip is then refreshed from Manager.Activity, at most
// once per trayTipInterval. The menu calls PauseAll / ResumeAll, opens the window
// (uiPath: downex-ui next to downexd, or the window of the macOS bundle) or stops the
// daemon gracefully.

// Tray timing.
const (
	// trayTipInterval spaces tooltip updates (at most one per second).
	trayTipInterval = time.Second
	// trayRetryInterval is the first delay before a failed Watch is reopened; it
	// doubles up to trayRetryMax (docs/DESKTOP-SPEC.md §3: nothing faster than 5 s).
	trayRetryInterval = 5 * time.Second
	trayRetryMax      = time.Minute
	// trayCommandTimeout bounds a menu action's call to the daemon.
	trayCommandTimeout = 30 * time.Second
	// trayOpenDebounce ignores repeated requests to open the window (a double-click
	// and Enter on the icon may both arrive).
	trayOpenDebounce = time.Second
)

// trayDaemon is what the tray needs from the daemon: the in-process API of
// *daemon.Manager.
type trayDaemon interface {
	Settings() daemon.Settings
	Subscribe() (<-chan daemon.Notice, func())
	ShowNotices(ch <-chan daemon.Notice, shows bool)
	Activity() daemon.Activity
	Watch(ctx context.Context, ids []int64, send func(*downexv1.WatchResponse) error) error
	PauseAll(ctx context.Context) (int, error)
	ResumeAll(ctx context.Context) (int, error)
}

var _ trayDaemon = (*daemon.Manager)(nil)

// trayIcon is the notification-area icon the runner drives (*tray.Tray).
type trayIcon interface {
	SetVisible(bool)
	SetTooltip(string)
	Notify(tray.Balloon)
	Close() error
}

// newTrayIcon creates the icon; tests replace it.
var newTrayIcon = func(opts tray.Options) (trayIcon, error) {
	t, err := tray.New(opts)
	if err != nil {
		return nil, err
	}
	return t, nil
}

// trayConfig configures startTray.
type trayConfig struct {
	daemon trayDaemon
	log    *slog.Logger
	// quit stops the daemon gracefully (as the Shutdown RPC does).
	quit func()
	// openUI starts the desktop window.
	openUI func() error
	// Timing; zero values select the tray* constants.
	tipInterval, retryInterval time.Duration
}

// startTray shows the notification-area icon and follows the daemon until ctx ends or
// stop is called; stop removes the icon and waits for the runner. Where there is no
// notification area (tray.ErrNotSupported) it does nothing.
func startTray(ctx context.Context, cfg trayConfig) (stop func()) {
	if cfg.log == nil {
		cfg.log = slog.New(slog.DiscardHandler)
	}
	if cfg.tipInterval <= 0 {
		cfg.tipInterval = trayTipInterval
	}
	if cfg.retryInterval <= 0 {
		cfg.retryInterval = trayRetryInterval
	}
	ctx, cancel := context.WithCancel(ctx)
	r := &trayRunner{cfg: cfg, ctx: ctx}
	// A command may arrive before newTrayIcon returns (macOS: the click on a
	// notification that launched the agent): it waits until the runner has the icon.
	ready := make(chan struct{})
	icon, err := newTrayIcon(tray.Options{
		Tooltip:   tooltip(cfg.daemon.Activity()),
		OnCommand: func(c tray.Command) { <-ready; r.command(c) },
		Logger:    cfg.log,
	})
	if err != nil {
		cancel()
		if errors.Is(err, tray.ErrNotSupported) {
			cfg.log.Debug("no notification-area icon on this system")
		} else {
			cfg.log.Warn("the notification-area icon is not available", "err", err)
		}
		return func() {}
	}
	r.icon = icon
	close(ready)
	// Subscribe before reading the settings, so that no change in between is lost, and
	// apply them before returning, so that the daemon knows from its first browser
	// hand-off whether the icon shows notifications (the hand-off is held for
	// confirmation only when something can ask the user).
	r.notices, r.unsubscribe = cfg.daemon.Subscribe()
	r.center = notify.NewCenter(notify.Policy{})
	r.apply(cfg.daemon.Settings())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.run()
	}()
	return sync.OnceFunc(func() {
		cancel()
		<-done
		if err := icon.Close(); err != nil {
			cfg.log.Warn("remove the notification-area icon", "err", err)
		}
	})
}

// tooltip renders the daemon's activity.
func tooltip(a daemon.Activity) string {
	return notify.Tooltip(notify.Summary{Active: a.Active, Speed: a.SpeedBps, PoliteSlowed: a.PoliteSlowed})
}

// trayRunner connects the daemon to the icon.
type trayRunner struct {
	cfg  trayConfig
	ctx  context.Context // ends when the tray stops
	icon trayIcon

	// Set up by startTray, then owned by run.
	notices     <-chan daemon.Notice
	unsubscribe func()
	center      *notify.Center
	visible     bool

	mu       sync.Mutex
	lastOpen time.Time
}

// apply follows the settings: the icon's visibility and the notification switches.
// Notifications appear only while the icon is shown, so the daemon is told whether the
// tray can ask the user to confirm a browser hand-off (daemon.Manager.ShowNotices).
func (r *trayRunner) apply(s daemon.Settings) {
	if s.ShowTrayIcon != r.visible {
		r.visible = s.ShowTrayIcon
		r.icon.SetVisible(r.visible)
		r.cfg.daemon.ShowNotices(r.notices, r.visible)
	}
	r.center.SetPolicy(notify.Policy{OnComplete: s.NotifyOnComplete, OnFailure: s.NotifyOnFailure})
}

func (r *trayRunner) run() {
	ctx, d := r.ctx, r.cfg.daemon
	defer r.unsubscribe()
	notices, center := r.notices, r.center

	changes := make(chan struct{}, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); r.watch(ctx, changes) }()
	defer wg.Wait()

	// Notifications held for coalescing are released by flush. The tooltip changes at
	// most once per tipInterval, and only when its text changed.
	flush, tipTimer := &timer{}, &timer{}
	defer flush.stop()
	defer tipTimer.stop()
	shownTip := tooltip(d.Activity())
	wantTip := shownTip
	var lastTip time.Time
	updateTip := func(now time.Time) {
		switch {
		case wantTip == shownTip:
			tipTimer.stop()
		case now.Sub(lastTip) >= r.cfg.tipInterval:
			tipTimer.stop()
			r.icon.SetTooltip(wantTip)
			shownTip, lastTip = wantTip, now
		case !tipTimer.armed():
			tipTimer.reset(lastTip.Add(r.cfg.tipInterval).Sub(now))
		}
	}
	show := func(ns []notify.Notification) {
		for _, n := range ns {
			r.icon.Notify(tray.Balloon{Title: n.Title, Text: n.Text, Error: n.Error})
		}
		if due, ok := center.Deadline(); ok {
			flush.reset(time.Until(due))
		} else {
			flush.stop()
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case n, ok := <-notices:
			if !ok { // the daemon is shutting down
				notices = nil
				continue
			}
			now := time.Now()
			switch n.Kind {
			case daemon.NoticeCompleted:
				if !n.InWindow { // its progress window shows the completion
					show(center.Completed(now, n.Download))
				}
			case daemon.NoticeFailed:
				if !n.InWindow { // its progress window shows the failure and Retry
					show(center.Failed(now, n.Download))
				}
			case daemon.NoticeLinkRenewed:
				show(center.Renewed(now, n.Download))
			case daemon.NoticeAwaiting:
				if !n.UILaunched { // the window could not be asked to show it
					show(center.Awaiting(now, n.Download))
				}
			case daemon.NoticeSettings:
				r.apply(n.Settings)
			case daemon.NoticePostAction:
				if b, ok := postActionBalloon(n.PostAction, now); ok {
					r.icon.Notify(b)
				}
			case daemon.NoticeScanProblem:
				// Shown whatever notify_on_failure says: the user turned scanning on.
				r.icon.Notify(tray.Balloon{Title: "Antivirus scan", Text: notify.Clean(n.Text), Error: true})
			case daemon.NoticeFileCheck:
				// The daemon decides who hears of it (docs/DESKTOP-SPEC.md §9.7).
				r.icon.Notify(fileCheckBalloon(n))
			case daemon.NoticeUpdateAvailable:
				r.icon.Notify(updateBalloon(n.Update))
			}
		case <-changes:
			wantTip = tooltip(d.Activity())
			updateTip(time.Now())
		case <-flush.c():
			flush.fired()
			show(center.Flush(time.Now()))
		case <-tipTimer.c():
			tipTimer.fired()
			updateTip(time.Now())
		}
	}
}

// watch signals changes of any download on changes (capacity 1), reopening the
// stream after a failure.
func (r *trayRunner) watch(ctx context.Context, changes chan<- struct{}) {
	delay := r.cfg.retryInterval
	for {
		worked := false
		err := r.cfg.daemon.Watch(ctx, nil, func(*downexv1.WatchResponse) error {
			worked = true
			select {
			case changes <- struct{}{}:
			default:
			}
			return nil
		})
		if ctx.Err() != nil {
			return
		}
		if worked { // the stream worked: start over with the shortest delay
			delay = r.cfg.retryInterval
		}
		r.cfg.log.Debug("tray: watch ended; reopening", "err", err, "in", delay)
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = min(2*delay, max(trayRetryMax, r.cfg.retryInterval))
	}
}

// postActionBalloon is the notification of a post-queue action's countdown or of its end
// (docs/M6-SPEC.md §1): "Shutting down in 60 s"; clicking it opens the window, whose
// banner can cancel it. ok is false for an action without a notification.
func postActionBalloon(p *downexv1.PostActionPending, now time.Time) (b tray.Balloon, ok bool) {
	var doing, done string
	switch p.GetAction() {
	case downexv1.PostAction_POST_ACTION_QUIT_DOWNEX:
		doing, done = "Quitting Downex", "Quitting Downex"
	case downexv1.PostAction_POST_ACTION_SLEEP:
		doing, done = "Sleeping", "Sleep"
	case downexv1.PostAction_POST_ACTION_HIBERNATE:
		doing, done = "Hibernating", "Hibernation"
	case downexv1.PostAction_POST_ACTION_SHUTDOWN:
		doing, done = "Shutting down", "Shutdown"
	default:
		return b, false
	}
	if p.GetCancelled() {
		return tray.Balloon{Title: done + " cancelled", Text: "The queue's action after it finished does not happen."}, true
	}
	secs := 0
	if d := p.GetDeadline(); d != nil {
		secs = max(int(d.AsTime().Sub(now).Round(time.Second)/time.Second), 0)
	}
	return tray.Balloon{
		Title: fmt.Sprintf("%s in %d s", doing, secs),
		Text:  "A queue has finished. Open Downex to cancel.",
	}, true
}

// updateBalloon is the notification of an available update (docs/M6-SPEC.md §4), after
// each automatic check that finds one. Clicking it opens the window (CommandOpen), whose
// pop-up offers the update unless the user put this check's offer off already.
func updateBalloon(u *downexv1.UpdateInfo) tray.Balloon {
	return tray.Balloon{
		Title: "Downex " + notify.Clean(u.GetLatestVersion()) + " is available",
		Text:  "Click to open Downex and install it.",
	}
}

// fileCheckBalloon is the notification of a final file check (docs/DESKTOP-SPEC.md
// §9.7): "Check this download", in the error style for DANGER. Clicking it opens the
// window, as every notification does.
func fileCheckBalloon(n daemon.Notice) tray.Balloon {
	return tray.Balloon{
		Title: filecheck.NotificationTitle,
		Text:  notify.Clean(n.Text),
		Error: n.Level == model.FileCheckDanger,
	}
}

// command carries out a choice from the icon; the tray calls it one at a time.
func (r *trayRunner) command(c tray.Command) {
	switch c {
	case tray.CommandOpen, tray.CommandSettings:
		r.open()
	case tray.CommandPauseAll:
		r.bulk("pause", "Could not pause downloads", r.cfg.daemon.PauseAll)
	case tray.CommandResumeAll:
		r.bulk("resume", "Could not resume downloads", r.cfg.daemon.ResumeAll)
	case tray.CommandQuit:
		r.cfg.log.Info("quit chosen in the notification area")
		r.cfg.quit()
	}
}

// open starts the desktop window, at most once per trayOpenDebounce.
func (r *trayRunner) open() {
	r.mu.Lock()
	now := time.Now()
	if !r.lastOpen.IsZero() && now.Sub(r.lastOpen) < trayOpenDebounce {
		r.mu.Unlock()
		return
	}
	r.lastOpen = now
	r.mu.Unlock()
	if r.cfg.openUI == nil {
		return
	}
	if err := r.cfg.openUI(); err != nil {
		r.cfg.log.Warn("tray: open the Downex window", "err", err)
		r.icon.Notify(tray.Balloon{Title: "Cannot open Downex", Text: notify.ShortError("", err.Error()), Error: true})
	}
}

// bulk runs PauseAll or ResumeAll and reports a failure at the icon.
func (r *trayRunner) bulk(verb, failTitle string, fn func(context.Context) (int, error)) {
	ctx, cancel := context.WithTimeout(r.ctx, trayCommandTimeout)
	defer cancel()
	n, err := fn(ctx)
	if err == nil {
		r.cfg.log.Info("tray: "+verb+" all", "downloads", n)
		return
	}
	if r.ctx.Err() != nil {
		return // the daemon is stopping
	}
	r.cfg.log.Warn("tray: "+verb+" all", "downloads", n, "err", err)
	text := notify.ShortError("", err.Error())
	var ce *connect.Error
	if errors.As(err, &ce) {
		text = notify.ShortError("", ce.Message())
	}
	r.icon.Notify(tray.Balloon{Title: failTitle, Text: text, Error: true})
}

// selfPath is this executable, symbolic links resolved.
func selfPath() (string, error) {
	l, err := client.FindLayout(os.Executable)
	return l.Exe, err
}

// installLayout is where this installation keeps the window and the progress app
// (client.FindLayout of this executable, symbolic links resolved): downex-ui next to
// downexd, or inside Downex.app Contents/MacOS/Downex and Contents/Helpers/Downex
// Progress.app (docs/MACOS-SPEC.md §1); tests replace it.
var installLayout = func() (client.Layout, error) { return client.FindLayout(os.Executable) }

// loginProgram is what the sign-in entry of l starts: inside the macOS bundle the login
// item (downexd with the menu-bar icon, docs/MACOS-SPEC.md §2) when it is installed,
// else this executable.
func loginProgram(l client.Layout) string {
	if l.Agent != "" && regularFile(l.Agent) {
		return l.Agent
	}
	return l.Exe
}

func regularFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

// startApp starts the desktop window detached with a shown window (platform.StartApp:
// platform.StartDetached would hide its first window; on macOS it opens the app bundle
// that holds the executable); tests replace it.
var startApp = platform.StartApp

// openUI starts the desktop window detached with args; a running window receives them
// through its single-instance lock and comes forward.
func openUI(args ...string) error {
	return openApp(func(l client.Layout) string { return l.Window }, args...)
}

// openProgressApp starts "--progress <id>" (in args) with the window, or inside the macOS
// bundle with the progress app (no Dock icon per window).
func openProgressApp(args ...string) error {
	return openApp(func(l client.Layout) string { return l.Progress }, args...)
}

func openApp(pick func(client.Layout) string, args ...string) error {
	l, err := installLayout()
	if err != nil {
		return fmt.Errorf("locate the Downex window: %w", err)
	}
	p := pick(l)
	if _, err := os.Stat(p); err != nil {
		where := "next to downexd"
		if l.Bundle != "" {
			where = "in " + l.Bundle
		}
		return fmt.Errorf("the Downex window is not installed %s: %w", where, err)
	}
	return startApp(p, args...)
}

// timer is a one-shot timer whose channel is nil while it is not armed.
type timer struct {
	t  *time.Timer
	on bool
}

func (t *timer) armed() bool { return t.on }

// reset arms the timer for d (at once when d <= 0).
func (t *timer) reset(d time.Duration) {
	d = max(d, 0)
	if t.t == nil {
		t.t = time.NewTimer(d)
	} else {
		t.t.Reset(d) // since Go 1.23 no stale value is received after Reset
	}
	t.on = true
}

func (t *timer) stop() {
	if t.t != nil {
		t.t.Stop()
	}
	t.on = false
}

// fired records that the channel delivered.
func (t *timer) fired() { t.on = false }

// c is the channel to select on: nil (never ready) while not armed.
func (t *timer) c() <-chan time.Time {
	if !t.on {
		return nil
	}
	return t.t.C
}
