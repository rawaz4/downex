//go:build windows

package tools

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// creationFlags: no console window (downexd is a GUI-subsystem program, so a console
// child would open one), and suspended until the child is in its job object, so that
// nothing it starts escapes the job.
const creationFlags = windows.CREATE_NO_WINDOW | windows.CREATE_SUSPENDED

// killAtExit: the job is closed (and what remains of the tree killed) as soon as the
// child exits; a job handle cannot name anything else.
const killAtExit = true

// jobTree is a child inside a job object with KILL_ON_JOB_CLOSE.
type jobTree struct {
	job    windows.Handle
	closed bool
}

// startTree starts cmd suspended, puts it into a new job object (at most maxProcesses
// active processes, memoryLimit bytes of committed memory when > 0, everything killed
// when the job is closed) and resumes it.
func startTree(cmd *exec.Cmd, memoryLimit int64) (procTree, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create a job object: %w", err)
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS
	info.BasicLimitInformation.ActiveProcessLimit = maxProcesses
	if memoryLimit > 0 {
		info.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_JOB_MEMORY
		info.JobMemoryLimit = uintptr(memoryLimit)
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return nil, fmt.Errorf("limit the job object: %w", err)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: creationFlags}
	if err := cmd.Start(); err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	pid := uint32(cmd.Process.Pid)
	// The suspended child cannot exit, and cmd.Process holds its handle, so pid still
	// names it.
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, pid)
	if err == nil {
		err = windows.AssignProcessToJobObject(job, h)
		windows.CloseHandle(h)
	}
	if err == nil {
		err = resumeProcess(pid)
	}
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		windows.CloseHandle(job)
		return nil, fmt.Errorf("start the child in its job object: %w", err)
	}
	return &jobTree{job: job}, nil
}

// resumeProcess resumes the threads of the suspended process pid (its main thread).
func resumeProcess(pid uint32) error {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snap)
	te := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	resumed := 0
	for err = windows.Thread32First(snap, &te); err == nil; err = windows.Thread32Next(snap, &te) {
		if te.OwnerProcessID != pid {
			continue
		}
		th, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, te.ThreadID)
		if err != nil {
			return err
		}
		_, err = windows.ResumeThread(th)
		windows.CloseHandle(th)
		if err != nil {
			return err
		}
		resumed++
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return err
	}
	if resumed == 0 {
		return errors.New("the child has no thread to resume")
	}
	return nil
}

func (t *jobTree) kill() { _ = windows.TerminateJobObject(t.job, 1) }

func (t *jobTree) release(bool) {
	if t.closed {
		return
	}
	t.closed = true
	_ = windows.TerminateJobObject(t.job, 1)
	_ = windows.CloseHandle(t.job)
}
