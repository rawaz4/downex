//go:build windows || linux || darwin

package platform

import "os"

// control runs fn with f's OS handle (a file descriptor, or a HANDLE on Windows) while
// the file is guaranteed to stay open. Unlike f.Fd it leaves the file's I/O mode alone.
func control(f *os.File, fn func(fd uintptr) error) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var ferr error
	if err := rc.Control(func(fd uintptr) { ferr = fn(fd) }); err != nil {
		return err
	}
	return ferr
}
