package daemon

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"downex/internal/clipboard"
	downexv1 "downex/internal/rpc/downex/v1"
)

// ClipboardMonitor starts a platform clipboard monitor that calls onText with every
// new clipboard text that is not marked as excluded or sensitive, and returns the
// function that stops it. SystemClipboard is the real one; it fails with
// clipboard.ErrNotSupported where there is none.
type ClipboardMonitor func(onText func(text string)) (stop func() error, err error)

// SystemClipboard returns the platform's clipboard monitor (internal/clipboard).
func SystemClipboard(log *slog.Logger) ClipboardMonitor {
	return func(onText func(string)) (func() error, error) {
		m, err := clipboard.Start(onText, log)
		if err != nil {
			return nil, err
		}
		return m.Close, nil
	}
}

// clipManager is what the clipboard watcher needs of the Manager.
type clipManager interface {
	Add(ctx context.Context, r *downexv1.AddRequest) (*downexv1.Download, error)
	Settings() Settings
	Subscribe() (<-chan Notice, func())
}

// WatchClipboard runs the clipboard monitor while clipboard_monitor is on
// (docs/M6-SPEC.md §3) until ctx ends or the Manager shuts down. A new clipboard text
// that consists only of http(s) URLs (at most clipboard.MaxURLs lines) is offered URL
// by URL when the URL's path ends with an extension of a category: Add with source
// ADD_SOURCE_CLIPBOARD, so that the confirmation applies as for browser hand-offs. The
// same URL is not offered twice within clipboard.DedupeWindow. downexd calls it on a
// goroutine of its own for the user's own daemon only (never for --db/--endpoint
// daemons of tests), with SystemClipboard.
func (m *Manager) WatchClipboard(ctx context.Context, monitor ClipboardMonitor) {
	w := newClipWatcher(m, m.log, time.Now)
	w.run(ctx, monitor)
}

// clipWatcher turns clipboard texts into downloads.
type clipWatcher struct {
	mgr    clipManager
	log    *slog.Logger
	now    func() time.Time
	dedupe *clipboard.Deduper
}

func newClipWatcher(mgr clipManager, log *slog.Logger, now func() time.Time) *clipWatcher {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &clipWatcher{mgr: mgr, log: log, now: now, dedupe: clipboard.NewDeduper(clipboard.DedupeWindow)}
}

// run starts and stops the monitor as the setting changes until ctx ends or the
// notices end (shutdown).
func (w *clipWatcher) run(ctx context.Context, monitor ClipboardMonitor) {
	// Subscribed before the settings are read: no change is missed in between.
	notices, cancel := w.mgr.Subscribe()
	defer cancel()
	ctx, stopAdds := context.WithCancel(ctx)
	defer stopAdds()

	var stop func() error
	unsupported := false
	apply := func(on bool) {
		switch {
		case on && stop == nil && !unsupported:
			s, err := monitor(func(text string) { w.handle(ctx, text) })
			switch {
			case errors.Is(err, clipboard.ErrNotSupported):
				unsupported = true
				w.log.Info("the clipboard cannot be monitored here; clipboard_monitor has no effect", "err", err)
			case err != nil:
				w.log.Warn("cannot start the clipboard monitor", "err", err)
			default:
				stop = s
				w.log.Info("clipboard monitor started")
			}
		case !on && stop != nil:
			if err := stop(); err != nil {
				w.log.Warn("cannot stop the clipboard monitor", "err", err)
			}
			stop = nil
			w.log.Info("clipboard monitor stopped")
		}
	}
	defer apply(false)
	apply(w.mgr.Settings().Desktop.ClipboardMonitor)
	for {
		select {
		case <-ctx.Done():
			return
		case n, ok := <-notices:
			if !ok { // the Manager shut down
				return
			}
			if n.Kind == NoticeSettings {
				apply(n.Settings.Desktop.ClipboardMonitor)
			}
		}
	}
}

// handle offers the download links of a new clipboard text.
func (w *clipWatcher) handle(ctx context.Context, text string) {
	s := w.mgr.Settings()
	if !s.Desktop.ClipboardMonitor || ctx.Err() != nil { // turned off meanwhile
		return
	}
	urls := clipboard.Extract(text, func(ext string) bool { return categoryForExtension(s.Categories, ext) != nil })
	for _, u := range urls {
		if ctx.Err() != nil {
			return
		}
		if !w.dedupe.Offer(u, w.now()) {
			w.log.Debug("clipboard link offered recently; ignored", "url", redactURL(u))
			continue
		}
		d, err := w.mgr.Add(ctx, &downexv1.AddRequest{Url: u, Source: downexv1.AddSource_ADD_SOURCE_CLIPBOARD})
		if err != nil {
			w.log.Warn("cannot add the download link from the clipboard", "url", redactURL(u), "err", err)
			continue
		}
		w.log.Info("download link taken from the clipboard", "download", d.GetId(), "url", redactURL(u))
	}
}
