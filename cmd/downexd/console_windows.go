//go:build windows

package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"

	"downex/internal/platform"
)

// Consoles on Windows (docs/DESKTOP-SPEC.md §5). Windows gives a console program that
// is started like a program the user opens (the sign-in entry, a shortcut, a
// double-click) a console window of its own: it stays open as long as the program
// runs, and closing it stops the program. Releases therefore build downexd as a
// GUI-subsystem program (-H windowsgui, scripts/build-windows.ps1), which never gets a
// console, but then also has no standard output or error when it is run from a
// terminal. setupConsole adapts either build before anything is written.

var (
	kernel32                  = windows.NewLazySystemDLL("kernel32.dll")
	procGetConsoleProcessList = kernel32.NewProc("GetConsoleProcessList")
	procAttachConsole         = kernel32.NewProc("AttachConsole")
)

// attachParentProcess is ATTACH_PARENT_PROCESS, (DWORD)-1.
const attachParentProcess = uintptr(^uint32(0))

// consoleAction is what setupConsole does for the way downexd was started.
type consoleAction int

const (
	// consoleKeep changes nothing.
	consoleKeep consoleAction = iota
	// consoleHandOver starts a detached copy (platform.StartDetached: no console) with
	// the same arguments and exits; the console window closes with this process.
	consoleHandOver
	// consoleAttach attaches to the console of the parent (the terminal it was run
	// from) for the standard output and error this process lacks.
	consoleAttach
)

// chooseConsole decides for options o (parsed reports valid flags that run the
// daemon, not --help or --version), with consoleProcs processes attached to this
// process's console (0: it has none) and stdioMissing when standard output or error
// is missing:
//   - --background in a console of its own (a console build started at sign-in):
//     hand over, so that no window stays open. Relative --db or --log-dir paths would
//     change their meaning in the copy (it runs in the Windows directory), so with
//     them it keeps the console;
//   - --foreground, --help, --version or invalid flags without a console and without
//     standard handles (a GUI build run from a terminal): attach to the terminal, so
//     that the output appears there. A plain start keeps no tie to the terminal,
//     whose closing would stop the daemon.
func chooseConsole(o options, parsed bool, consoleProcs int, stdioMissing bool) consoleAction {
	relative := func(p string) bool { return p != "" && !filepath.IsAbs(p) }
	switch {
	case o.background:
		if parsed && consoleProcs == 1 && !relative(o.db) && !relative(o.logDir) {
			return consoleHandOver
		}
	case consoleProcs == 0 && stdioMissing && (!parsed || o.foreground):
		return consoleAttach
	}
	return consoleKeep
}

// setupConsole prepares the console of a downexd started with args, before anything
// is written, and returns the standard output and error for run. handedOver reports
// that a detached copy took over: this process must exit at once.
func setupConsole(args []string) (stdout, stderr io.Writer, handedOver bool) {
	stdout, stderr = os.Stdout, os.Stderr
	o, parsed, _ := parseFlags(args, io.Discard, io.Discard)
	outMissing, errMissing := stdHandleMissing(windows.STD_OUTPUT_HANDLE), stdHandleMissing(windows.STD_ERROR_HANDLE)
	switch chooseConsole(o, parsed, consoleProcesses(), outMissing || errMissing) {
	case consoleHandOver:
		exe, err := selfPath()
		if err == nil {
			err = platform.StartDetached(exe, args...)
		}
		if err == nil {
			return nil, nil, true
		}
		fmt.Fprintf(stderr, "downexd: cannot continue without a console window, so this one stays open: %v\n", err)
	case consoleAttach:
		if out := attachParentConsole(); out != nil {
			// The runtime writes panics to the standard handles: set them too.
			if outMissing {
				stdout = out
				_ = windows.SetStdHandle(windows.STD_OUTPUT_HANDLE, windows.Handle(out.Fd()))
			}
			if errMissing {
				stderr = out
				_ = windows.SetStdHandle(windows.STD_ERROR_HANDLE, windows.Handle(out.Fd()))
			}
		}
	}
	return stdout, stderr, false
}

// consoleProcesses returns how many processes are attached to this process's
// console, 0 when it has none.
func consoleProcesses() int {
	var pids [2]uint32
	n, _, _ := procGetConsoleProcessList.Call(uintptr(unsafe.Pointer(&pids[0])), uintptr(len(pids)))
	return int(n)
}

// stdHandleMissing reports whether the standard handle id is unset, as it is for a
// GUI-subsystem program that was given none.
func stdHandleMissing(id uint32) bool {
	h, err := windows.GetStdHandle(id)
	return err != nil || h == 0 || h == windows.InvalidHandle
}

// attachParentConsole attaches to the console of the parent process and opens its
// output; nil when the parent has none.
func attachParentConsole() *os.File {
	if r, _, _ := procAttachConsole.Call(attachParentProcess); r == 0 {
		return nil
	}
	f, err := os.OpenFile("CONOUT$", os.O_RDWR, 0)
	if err != nil {
		return nil
	}
	return f
}
