package platform

import (
	"errors"
	"fmt"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modKernel32Wake          = windows.NewLazySystemDLL("kernel32.dll")
	procCreateWaitableTimerW = modKernel32Wake.NewProc("CreateWaitableTimerW")
	procSetWaitableTimer     = modKernel32Wake.NewProc("SetWaitableTimer")
	procCancelWaitableTimer  = modKernel32Wake.NewProc("CancelWaitableTimer")
)

// WakeTimer wakes the computer from sleep at a set time: a waitable timer armed with
// resume (docs/M6-SPEC.md §1 wake_computer). It works while the process runs and the
// power plan allows wake timers. Safe for concurrent use.
type WakeTimer struct {
	mu     sync.Mutex
	h      windows.Handle
	closed bool
}

// NewWakeTimer creates an unarmed wake timer.
func NewWakeTimer() (*WakeTimer, error) {
	// CreateWaitableTimerW(no security attributes, auto reset, unnamed).
	h, _, err := procCreateWaitableTimerW.Call(0, 0, 0)
	if h == 0 {
		return nil, fmt.Errorf("platform: create wake timer: %w", err)
	}
	return &WakeTimer{h: windows.Handle(h)}, nil
}

// WakeAt arms the timer to wake the computer at t (replacing an earlier time). It
// returns an error wrapping errors.ErrUnsupported when the system cannot resume from a
// timer (the timer is armed anyway, without waking the computer).
func (w *WakeTimer) WakeAt(t time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return errors.New("platform: wake timer is closed")
	}
	due := fileTimeOf(t) // positive: an absolute UTC time
	// SetWaitableTimer(h, &due, no period, no completion routine, resume = TRUE).
	r, _, errno := procSetWaitableTimer.Call(uintptr(w.h), uintptr(unsafe.Pointer(&due)), 0, 0, 0, 1)
	if r == 0 {
		return fmt.Errorf("platform: set wake timer: %w", errno)
	}
	if errors.Is(errno, windows.ERROR_NOT_SUPPORTED) {
		return fmt.Errorf("platform: this computer cannot wake from a timer: %w", errors.ErrUnsupported)
	}
	return nil
}

// Cancel disarms the timer.
func (w *WakeTimer) Cancel() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	if r, _, errno := procCancelWaitableTimer.Call(uintptr(w.h)); r == 0 {
		return fmt.Errorf("platform: cancel wake timer: %w", errno)
	}
	return nil
}

// Close disarms and releases the timer.
func (w *WakeTimer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	_, _, _ = procCancelWaitableTimer.Call(uintptr(w.h))
	return windows.CloseHandle(w.h)
}
