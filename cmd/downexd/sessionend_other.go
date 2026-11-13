//go:build !windows

package main

import "sync"

// sessionWatcher outside Windows: the end of a session reaches the daemon as a signal
// (SIGHUP, SIGTERM), and resume from sleep or a clock change is noticed by comparing the
// wall clock with the monotonic clock (clockjump.go).
type sessionWatcher struct {
	stop     chan struct{}
	stopOnce sync.Once
}

func watchSessionEnd(func()) (*sessionWatcher, error) {
	return &sessionWatcher{stop: make(chan struct{})}, nil
}

func (*sessionWatcher) finished() {}

func (w *sessionWatcher) close() { w.stopOnce.Do(func() { close(w.stop) }) }

// setOnResume calls onResume (which must not block) after the system resumed from sleep
// or its clock changed, within clockCheckInterval, until close.
func (w *sessionWatcher) setOnResume(onResume func()) {
	go watchClockJumps(w.stop, clockCheckInterval, onResume)
}
