//go:build !windows && !darwin

package host

import (
	"errors"
	"io/fs"
	"os/exec"
	"path/filepath"

	"connectrpc.com/connect"
)

// forbiddenNameChars cannot appear in a file name.
const forbiddenNameChars = "/"

// osRisk adds files with an execute permission bit, which are programs.
func osRisk(_ string, info fs.FileInfo) Risk {
	if info != nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
		return RiskProgram
	}
	return RiskNone
}

// xdgShell opens files and folders with xdg-open (freedesktop.org), without a shell.
// The Linux window is not built yet; this keeps the package portable.
type xdgShell struct{}

// NewShell returns the desktop's file handling.
func NewShell() Shell { return xdgShell{} }

func (xdgShell) Open(path string) error { return startDetached("xdg-open", path) }

// Reveal opens the file's folder; selecting the file needs the file manager's D-Bus
// interface, which comes with the Linux window.
func (xdgShell) Reveal(path string) error { return startDetached("xdg-open", filepath.Dir(path)) }

func (xdgShell) ShowFolder(dir string) error { return startDetached("xdg-open", dir) }

// CanOpenWith: Open with… is not offered yet (see OpenWith); Startup tells the page.
const CanOpenWith = false

// OpenWith is not available yet: choosing an application comes with the Linux window
// (DESKTOP-SPEC §8).
func (xdgShell) OpenWith(string) error {
	return connect.NewError(connect.CodeUnimplemented,
		errors.New("choosing an app to open a file with is not available on this system yet; use Open or Open folder"))
}

func startDetached(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// AllowForegroundHandoff does nothing here: no foreground lock to hand over.
func AllowForegroundHandoff() {}

// SystemPrefersDark is false: the desktop's theme is not read here yet.
func SystemPrefersDark() bool { return false }
