package platform

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procPowerCreateRequest = modKernel32Wake.NewProc("PowerCreateRequest")
	procPowerSetRequest    = modKernel32Wake.NewProc("PowerSetRequest")
	procPowerClearRequest  = modKernel32Wake.NewProc("PowerClearRequest")
)

// Power request constants (winnt.h, minwinbase.h).
const (
	powerRequestContextVersion      = 0   // POWER_REQUEST_CONTEXT_VERSION
	powerRequestContextSimpleString = 0x1 // POWER_REQUEST_CONTEXT_SIMPLE_STRING
	powerRequestSystemRequired      = 1   // PowerRequestSystemRequired
)

// reasonContext mirrors REASON_CONTEXT with a simple reason string. Its union is as
// large as the Detailed member {HMODULE; ULONG; ULONG; LPWSTR *}, whose first field the
// SimpleReasonString overlays.
type reasonContext struct {
	version uint32
	flags   uint32
	reason  *uint16 // SimpleReasonString
	_       uint32  // Detailed.LocalizedReasonId
	_       uint32  // Detailed.ReasonStringCount
	_       uintptr // Detailed.ReasonStrings
}

// AwakeRequest keeps the computer from sleeping while idle as long as it is held: a
// power request of type PowerRequestSystemRequired (PowerCreateRequest, PowerSetRequest),
// which also keeps a computer that a wake timer woke from going back to sleep after the
// unattended idle timeout. The display may still turn off, and an explicit sleep or
// shutdown (Sleep, Shutdown) still happens. Windows lists the reason in
// "powercfg /requests". Safe for concurrent use.
type AwakeRequest struct {
	mu     sync.Mutex
	h      windows.Handle
	held   bool
	closed bool
}

// NewAwakeRequest creates a released power request that gives reason.
func NewAwakeRequest(reason string) (*AwakeRequest, error) {
	text, err := windows.UTF16PtrFromString(reason)
	if err != nil {
		return nil, fmt.Errorf("platform: awake request reason: %w", err)
	}
	rc := reasonContext{version: powerRequestContextVersion, flags: powerRequestContextSimpleString, reason: text}
	r, _, errno := procPowerCreateRequest.Call(uintptr(unsafe.Pointer(&rc)))
	runtime.KeepAlive(&rc)
	if h := windows.Handle(r); h == windows.InvalidHandle || h == 0 {
		return nil, fmt.Errorf("platform: create power request: %w", errno)
	}
	return &AwakeRequest{h: windows.Handle(r)}, nil
}

// Hold keeps the computer awake until Release (holding twice is holding once).
func (a *AwakeRequest) Hold() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case a.closed:
		return errors.New("platform: awake request is closed")
	case a.held:
		return nil
	}
	if r, _, errno := procPowerSetRequest.Call(uintptr(a.h), powerRequestSystemRequired); r == 0 {
		return fmt.Errorf("platform: set power request: %w", errno)
	}
	a.held = true
	return nil
}

// Release lets the computer sleep while idle again.
func (a *AwakeRequest) Release() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.releaseLocked()
}

func (a *AwakeRequest) releaseLocked() error {
	if a.closed || !a.held {
		return nil
	}
	if r, _, errno := procPowerClearRequest.Call(uintptr(a.h), powerRequestSystemRequired); r == 0 {
		return fmt.Errorf("platform: clear power request: %w", errno)
	}
	a.held = false
	return nil
}

// Close releases the request and its handle.
func (a *AwakeRequest) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil
	}
	err := a.releaseLocked()
	a.closed = true
	return errors.Join(err, windows.CloseHandle(a.h))
}
