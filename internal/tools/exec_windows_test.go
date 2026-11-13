//go:build windows

package tools

import (
	"testing"

	"golang.org/x/sys/windows"
)

// TestCreationFlags: children never open a console window (downexd is a GUI program)
// and start suspended, so that they are in their job object before they run.
func TestCreationFlags(t *testing.T) {
	if creationFlags&windows.CREATE_NO_WINDOW == 0 {
		t.Error("CREATE_NO_WINDOW is not set")
	}
	if creationFlags&windows.CREATE_SUSPENDED == 0 {
		t.Error("CREATE_SUSPENDED is not set")
	}
	if creationFlags&(windows.CREATE_NEW_CONSOLE|windows.DETACHED_PROCESS) != 0 {
		t.Error("a console flag is set")
	}
}

// processAlive reports whether the process pid is still running.
func processAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	ev, err := windows.WaitForSingleObject(h, 0)
	return err == nil && ev == uint32(windows.WAIT_TIMEOUT)
}
