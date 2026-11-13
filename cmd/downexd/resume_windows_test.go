//go:build windows

package main

import (
	"sync/atomic"
	"testing"
	"time"
)

// The session watcher's window reports a resume from sleep and a clock change (the queue
// scheduler re-evaluates its schedules then); other power events are ignored.
func TestSessionWatcherReportsResume(t *testing.T) {
	w, err := watchSessionEnd(func() {})
	if err != nil {
		t.Fatal(err)
	}
	defer w.close()
	var n atomic.Int32
	sendMessage(w.hwnd, wmPowerBroadcast, pbtResumeAutomatic, 0) // before setOnResume: nothing to call
	w.setOnResume(func() { n.Add(1) })
	const pbtSuspend = 0x0004 // PBT_APMSUSPEND
	if r := sendMessage(w.hwnd, wmPowerBroadcast, pbtSuspend, 0); r != 1 {
		t.Fatalf("WM_POWERBROADCAST returned %d, want TRUE", r)
	}
	sendMessage(w.hwnd, wmPowerBroadcast, pbtResumeAutomatic, 0)
	sendMessage(w.hwnd, wmPowerBroadcast, pbtResumeSuspend, 0)
	sendMessage(w.hwnd, wmTimeChange, 0, 0)
	deadline := time.Now().Add(5 * time.Second)
	for n.Load() != 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := n.Load(); got != 3 {
		t.Fatalf("resume reported %d times, want 3", got)
	}
	var nilWatcher *sessionWatcher
	nilWatcher.setOnResume(func() {}) // safe on nil
}
