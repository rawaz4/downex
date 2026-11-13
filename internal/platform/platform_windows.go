//go:build windows

package platform

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Preallocate sets the length of f to size bytes without writing zeros. Bytes in
// [0, min(old length, size)) keep their contents; bytes beyond size are discarded, as
// with f.Truncate. f must be open for writing.
//
// When f's volume supports sparse files (NTFS, ReFS) f is first marked sparse
// (FSCTL_SET_SPARSE), so that the new range takes no disk space and a write at any
// offset does not make the file system zero-fill the gap before it. On other volumes
// (FAT32, exFAT), or when the file system rejects the request, the file is only
// extended. Marking an existing file sparse never frees its allocated data.
func Preallocate(f *os.File, size int64) error {
	return preallocate(f, size, true)
}

// preallocate implements Preallocate; sparse = false takes the path of a volume without
// sparse-file support, so that tests can exercise it on NTFS.
func preallocate(f *os.File, size int64, sparse bool) error {
	if err := checkSize(f, size); err != nil {
		return err
	}
	if sparse && size > 0 {
		if err := control(f, setSparse); err != nil {
			return fmt.Errorf("platform: preallocate %s: %w", f.Name(), err)
		}
	}
	if err := f.Truncate(size); err != nil {
		return fmt.Errorf("platform: preallocate: %w", err)
	}
	return nil
}

// setSparse marks the open file h sparse when its volume supports sparse files and it is
// not sparse already. A volume whose capabilities cannot be queried, or a file system
// that does not implement the request, leaves the file as it is: extending a file that
// is not sparse is always correct, only slower to fill out of order.
func setSparse(fd uintptr) error {
	h := windows.Handle(fd)
	var flags uint32
	if err := windows.GetVolumeInformationByHandle(h, nil, 0, nil, nil, &flags, nil, 0); err != nil {
		return nil //nolint:nilerr // capabilities unknown: extend without the sparse flag
	}
	if flags&windows.FILE_SUPPORTS_SPARSE_FILES == 0 {
		return nil
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return fmt.Errorf("query file attributes: %w", err)
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_SPARSE_FILE != 0 {
		return nil
	}
	// With no input buffer FSCTL_SET_SPARSE sets the sparse flag.
	var n uint32
	if err := windows.DeviceIoControl(h, windows.FSCTL_SET_SPARSE, nil, 0, nil, 0, &n, nil); err != nil && !unsupported(err) {
		return fmt.Errorf("mark file sparse: %w", err)
	}
	return nil
}

// unsupported reports errors with which a file system declines a request it does not
// implement.
func unsupported(err error) bool {
	return errors.Is(err, windows.ERROR_INVALID_FUNCTION) ||
		errors.Is(err, windows.ERROR_NOT_SUPPORTED) ||
		errors.Is(err, windows.ERROR_INVALID_PARAMETER)
}

// detachFlags are the creation flags of every detached process: no console
// (DETACHED_PROCESS, CREATE_NO_WINDOW) and its own process group
// (CREATE_NEW_PROCESS_GROUP, so the caller's Ctrl+C does not reach it).
const detachFlags = windows.CREATE_NO_WINDOW | windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP

// StartDetached starts exe with args as a background process that outlives the caller and
// returns as soon as the process has started; it never waits for it. The process gets no
// console (DETACHED_PROCESS, CREATE_NO_WINDOW), a hidden window, its own process group
// (CREATE_NEW_PROCESS_GROUP, so the caller's Ctrl+C does not reach it), and NUL as
// standard input, output and error. Those three NUL handles are the only handles it
// inherits: the standard library passes an explicit inheritance list to CreateProcess.
// It inherits the caller's environment, but runs in the Windows directory rather than
// the caller's working directory, so that a long-lived process never keeps a folder
// (or a removable drive) of the caller in use.
//
// When the caller runs inside a job object (an SSH session, a CI step, some terminals),
// the process breaks away from it (CREATE_BREAKAWAY_FROM_JOB), so that closing the job
// does not kill it. A job that does not permit breakaway gets the process as a member,
// as before.
func StartDetached(exe string, args ...string) error {
	dir, err := windows.GetSystemWindowsDirectory()
	if err != nil {
		return fmt.Errorf("platform: locate the Windows directory: %w", err)
	}
	err = startDetached(exe, args, dir, detachFlags|windows.CREATE_BREAKAWAY_FROM_JOB)
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		// The caller's job does not allow breakaway.
		err = startDetached(exe, args, dir, detachFlags)
	}
	if err != nil {
		return fmt.Errorf("platform: start %s: %w", exe, err)
	}
	return nil
}

// startDetached starts exe in dir with the given creation flags and releases it.
func startDetached(exe string, args []string, dir string, flags uint32) error {
	cmd := exec.Command(exe, args...)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: flags}
	if err := cmd.Start(); err != nil {
		return err
	}
	// Release closes the process handle; Windows keeps no zombie to reap.
	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("release: %w", err)
	}
	return nil
}

// Elevated reports whether this process runs with more rights than the user's normal
// processes (docs/DAEMON-SPEC.md section 1: no elevation): an elevated administrator
// token of a user who also has a filtered one (TokenElevationTypeFull, a "Run as
// administrator" process), or the System integrity level of a service. A process of a
// user without a split token (an administrator with UAC turned off, whose processes all
// run alike) is not elevated in this sense: there is no less privileged process of that
// user to protect it from.
func Elevated() (bool, error) {
	tok := windows.GetCurrentProcessToken()
	var typ, n uint32
	if err := windows.GetTokenInformation(tok, windows.TokenElevationType, (*byte)(unsafe.Pointer(&typ)),
		uint32(unsafe.Sizeof(typ)), &n); err != nil {
		return false, fmt.Errorf("platform: query token elevation: %w", err)
	}
	level, err := integrityLevel(tok)
	if err != nil {
		return false, err
	}
	return elevated(typ, level), nil
}

// Token elevation type and integrity level used by Elevated.
const (
	tokenElevationTypeFull = 2      // TokenElevationTypeFull
	integritySystem        = 0x4000 // SECURITY_MANDATORY_SYSTEM_RID
)

// elevated decides Elevated from the token's elevation type and integrity level.
func elevated(elevationType, level uint32) bool {
	return elevationType == tokenElevationTypeFull || level >= integritySystem
}

// integrityLevel returns the mandatory integrity level (label RID) of token t.
func integrityLevel(t windows.Token) (uint32, error) {
	n := uint32(64)
	for {
		buf := make([]byte, n)
		err := windows.GetTokenInformation(t, windows.TokenIntegrityLevel, &buf[0], n, &n)
		if errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) {
			continue
		}
		if err != nil {
			return 0, fmt.Errorf("platform: query integrity level: %w", err)
		}
		// The label SID (S-1-16-<rid>) is read from its binary layout (revision,
		// sub-authority count, 6-byte authority, 32-bit sub-authorities): the x/sys
		// accessors turn syscall results into pointers, which the race detector's
		// pointer checks reject for SIDs in Go memory.
		sid := (*windows.Tokenmandatorylabel)(unsafe.Pointer(&buf[0])).Label.Sid
		if sid == nil || !sid.IsValid() {
			return 0, errors.New("platform: token has no integrity level")
		}
		p := unsafe.Pointer(sid)
		count := int(*(*uint8)(unsafe.Add(p, 1)))
		if count == 0 {
			return 0, errors.New("platform: token has no integrity level")
		}
		return *(*uint32)(unsafe.Add(p, 8+4*(count-1))), nil
	}
}

// DownloadsDir returns the user's Downloads folder (FOLDERID_Downloads, which honours
// folder redirection), creating it if it is missing.
func DownloadsDir() (string, error) {
	dir, err := windows.KnownFolderPath(windows.FOLDERID_Downloads, windows.KF_FLAG_CREATE)
	if err != nil {
		return "", fmt.Errorf("platform: locate the Downloads folder: %w", err)
	}
	return ensureDir(dir, 0o755)
}

// LogDir returns, creating it if needed, the directory for daemon logs:
// %LOCALAPPDATA%\Downex\logs. When LOCALAPPDATA is unset or not absolute the known
// folder FOLDERID_LocalAppData is used.
func LogDir() (string, error) {
	base := os.Getenv("LOCALAPPDATA")
	if !filepath.IsAbs(base) {
		var err error
		if base, err = windows.KnownFolderPath(windows.FOLDERID_LocalAppData, windows.KF_FLAG_DEFAULT); err != nil {
			return "", fmt.Errorf("platform: locate the local application data folder: %w", err)
		}
	}
	return ensureDir(filepath.Join(base, "Downex", "logs"), 0o700)
}

// IsSparse reports whether the file at path carries the sparse attribute
// (FILE_ATTRIBUTE_SPARSE_FILE). It is meant for tests and diagnostics.
func IsSparse(path string) (bool, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return false, fmt.Errorf("platform: %w", err)
	}
	attrs, ok := fi.Sys().(*syscall.Win32FileAttributeData)
	if !ok {
		return false, fmt.Errorf("platform: stat %s: no file attributes", path)
	}
	return attrs.FileAttributes&windows.FILE_ATTRIBUTE_SPARSE_FILE != 0, nil
}
