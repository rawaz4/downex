//go:build windows

package host

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// relayCommand starts the relay without a console window: downex-nmhost is a console
// program, and the window that starts it is not, so Windows would open one for it.
func relayCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}
