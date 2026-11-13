package platform

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
)

// AwakeRequest keeps the Mac from sleeping while idle as long as it is held: a
// caffeinate process (macCaffeinateCommand) whose assertion prevents idle sleep and that
// ends with this process. An explicit sleep or shutdown (Sleep, Shutdown) still happens.
// Safe for concurrent use.
type AwakeRequest struct {
	mu     sync.Mutex
	cmd    *exec.Cmd // the running caffeinate, nil while released
	closed bool
}

// NewAwakeRequest returns a released request; reason is not shown on macOS (pmset
// -g assertions names caffeinate).
func NewAwakeRequest(string) (*AwakeRequest, error) { return &AwakeRequest{}, nil }

// Hold keeps the Mac awake until Release (holding twice is holding once).
func (a *AwakeRequest) Hold() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case a.closed:
		return errors.New("platform: awake request is closed")
	case a.cmd != nil:
		return nil
	}
	exe, args := macCaffeinateCommand(os.Getpid())
	cmd := exec.Command(exe, args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("platform: start caffeinate: %w", err)
	}
	a.cmd = cmd
	return nil
}

// Release lets the Mac sleep while idle again.
func (a *AwakeRequest) Release() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.releaseLocked()
}

func (a *AwakeRequest) releaseLocked() error {
	cmd := a.cmd
	if cmd == nil {
		return nil
	}
	a.cmd = nil
	if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("platform: stop caffeinate: %w", err)
	}
	_ = cmd.Wait() // killed: its exit status says so
	return nil
}

// Close releases the request.
func (a *AwakeRequest) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closed = true
	return a.releaseLocked()
}
