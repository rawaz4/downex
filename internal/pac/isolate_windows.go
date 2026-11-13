package pac

import (
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// createNoWindow is CREATE_NO_WINDOW: a console build of the helper gets no console
// window when the daemon has none.
const createNoWindow = 0x08000000

// hideHelperWindow starts the helper without a window.
func hideHelperWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}

// limitHelperProcess puts the helper in a job object whose processes may commit at
// most limit bytes. A larger allocation then fails within the job (the helper ends)
// instead of reaching the system's commit limit, which would make Windows grow the
// paging file and fail allocations of other programs meanwhile. Without a job (an
// enclosing job that does not allow it) the heap watchdog still applies.
func limitHelperProcess(limit uint64) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return
	}
	// The handle stays open for the life of the process (no KILL_ON_JOB_CLOSE: closing
	// it would end nothing anyway).
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_PROCESS_MEMORY
	info.ProcessMemoryLimit = uintptr(limit)
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return
	}
	if err := windows.AssignProcessToJobObject(job, windows.CurrentProcess()); err != nil {
		_ = windows.CloseHandle(job)
	}
}
