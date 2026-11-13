//go:build windows

package platform

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// appFlags are the creation flags of StartApp: no console, its own process group.
const appFlags = windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP

// StartApp starts the desktop application exe with args detached from the caller, like
// StartDetached, but with a normal (shown) window: StartDetached hides the first window
// a process shows, which suits the daemon but would leave a window application
// invisible. It returns as soon as the process has started. The process inherits the
// caller's environment, runs in the Windows directory and gets NUL as standard input,
// output and error.
func StartApp(exe string, args ...string) error {
	dir, err := windows.GetSystemWindowsDirectory()
	if err != nil {
		return fmt.Errorf("platform: locate the Windows directory: %w", err)
	}
	err = startApp(exe, args, dir, appFlags|windows.CREATE_BREAKAWAY_FROM_JOB)
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		err = startApp(exe, args, dir, appFlags)
	}
	if err != nil {
		return fmt.Errorf("platform: start %s: %w", exe, err)
	}
	return nil
}

func startApp(exe string, args []string, dir string, flags uint32) error {
	cmd := exec.Command(exe, args...)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags}
	if err := cmd.Start(); err != nil {
		return err
	}
	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("release: %w", err)
	}
	return nil
}
