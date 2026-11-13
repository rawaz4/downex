package platform

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modPowrprof               = windows.NewLazySystemDLL("powrprof.dll")
	procSetSuspendState       = modPowrprof.NewProc("SetSuspendState")
	procIsPwrHibernateAllowed = modPowrprof.NewProc("IsPwrHibernateAllowed")

	modAdvapi32Power          = windows.NewLazySystemDLL("advapi32.dll")
	procInitiateShutdownW     = modAdvapi32Power.NewProc("InitiateShutdownW")
	procAdjustTokenPrivileges = modAdvapi32Power.NewProc("AdjustTokenPrivileges")
)

// InitiateShutdownW flags and reason (winuser.h, reason.h).
const (
	shutdownPoweroff      = 0x00000008 // SHUTDOWN_POWEROFF; no SHUTDOWN_FORCE_*: applications may refuse
	shutdownReasonPlanned = 0x80000000 // SHTDN_REASON_FLAG_PLANNED | MAJOR_OTHER | MINOR_OTHER
)

// Sleep suspends the computer (SetSuspendState; wake events stay enabled so that wake
// timers still work).
func Sleep() error { return suspend(false) }

// Hibernate hibernates the computer (SetSuspendState with hibernation); it fails when
// hibernation is disabled (see CanHibernate).
func Hibernate() error {
	if !CanHibernate() {
		return fmt.Errorf("platform: hibernate: hibernation is disabled: %w", ErrPowerUnsupported)
	}
	return suspend(true)
}

func suspend(hibernate bool) error {
	if err := enableShutdownPrivilege(); err != nil {
		return err
	}
	var h uintptr
	if hibernate {
		h = 1
	}
	// SetSuspendState(bHibernate, bForce (ignored), bWakeupEventsDisabled = FALSE).
	if r, _, err := procSetSuspendState.Call(h, 0, 0); r == 0 {
		return fmt.Errorf("platform: suspend (hibernate %v): %w", hibernate, err)
	}
	return nil
}

// Shutdown starts a shutdown with power-off (InitiateShutdownW). Applications are
// asked to close, never forced: one that refuses keeps the computer on.
func Shutdown() error {
	if err := enableShutdownPrivilege(); err != nil {
		return err
	}
	r, _, _ := procInitiateShutdownW.Call(0, 0, 0, shutdownPoweroff, shutdownReasonPlanned)
	if r != 0 {
		return fmt.Errorf("platform: shut down: %w", windows.Errno(r))
	}
	return nil
}

// CanHibernate reports whether hibernation is available (IsPwrHibernateAllowed).
func CanHibernate() bool {
	if procIsPwrHibernateAllowed.Find() != nil {
		return false
	}
	r, _, _ := procIsPwrHibernateAllowed.Call()
	return r != 0
}

// enableShutdownPrivilege enables SeShutdownPrivilege in the process token, which
// suspending and shutting down require (interactive users hold it, disabled).
func enableShutdownPrivilege() error {
	var tok windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &tok); err != nil {
		return fmt.Errorf("platform: open process token: %w", err)
	}
	defer tok.Close()
	luid, err := shutdownPrivilege()
	if err != nil {
		return err
	}
	tp := windows.Tokenprivileges{PrivilegeCount: 1}
	tp.Privileges[0] = windows.LUIDAndAttributes{Luid: luid, Attributes: windows.SE_PRIVILEGE_ENABLED}
	// Called directly: a success with ERROR_NOT_ALL_ASSIGNED means the user lacks it.
	r, _, errno := procAdjustTokenPrivileges.Call(uintptr(tok), 0, uintptr(unsafe.Pointer(&tp)), 0, 0, 0)
	if r == 0 {
		return fmt.Errorf("platform: enable SeShutdownPrivilege: %w", errno)
	}
	if errors.Is(errno, windows.ERROR_NOT_ALL_ASSIGNED) {
		return fmt.Errorf("platform: this user may not shut down or suspend the computer: %w", errno)
	}
	return nil
}

// shutdownPrivilege looks up the LUID of SeShutdownPrivilege.
func shutdownPrivilege() (windows.LUID, error) {
	var luid windows.LUID
	name, err := windows.UTF16PtrFromString("SeShutdownPrivilege")
	if err != nil {
		return luid, err
	}
	if err := windows.LookupPrivilegeValue(nil, name, &luid); err != nil {
		return luid, fmt.Errorf("platform: look up SeShutdownPrivilege: %w", err)
	}
	return luid, nil
}
