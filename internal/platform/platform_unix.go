//go:build linux || darwin

package platform

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// StartDetached starts exe with args as a background process that outlives the caller and
// returns as soon as the process has started; it never waits for it. The process runs in
// a new session (setsid), so it has no controlling terminal and does not receive the
// terminal's hangup or job-control signals, with /dev/null as standard input, output and
// error, and "/" as its working directory, so that it never keeps the caller's directory
// (or its mount) busy. It inherits the caller's environment.
//
// No other descriptor is inherited. The standard library opens its own files
// close-on-exec, but the caller may itself have inherited descriptors without that flag
// (a make jobserver pipe, a test harness's status descriptor); StartDetached first marks
// every descriptor above standard error close-on-exec in the calling process
// (closeOnExecAbove), so a later exec by the caller does not pass them on either.
//
// A goroutine reaps the process when it exits, so that it never lingers as a zombie of a
// long-lived caller (a zombie still answers kill(pid, 0) as if it were alive).
func StartDetached(exe string, args ...string) error {
	if err := closeOnExecAbove(2); err != nil {
		return fmt.Errorf("platform: start %s: %w", exe, err)
	}
	cmd := exec.Command(exe, args...)
	cmd.Dir = "/"
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("platform: start %s: %w", exe, err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// IsSparse reports whether the file at path has fewer bytes allocated than its length,
// that is, whether it has holes. It is meant for tests and diagnostics: compressing or
// inline-data file systems can make a file without holes look sparse.
func IsSparse(path string) (bool, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return false, fmt.Errorf("platform: %w", err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return false, fmt.Errorf("platform: stat %s: no block count", path)
	}
	return int64(st.Blocks)*512 < fi.Size(), nil // st_blocks counts 512-byte units
}
