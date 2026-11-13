//go:build !windows && !darwin

package main

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
func osRisk(_ string, info fs.FileInfo) risk {
	if info != nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
		return riskProgram
	}
	return riskNone
}

// xdgShell opens files and folders with xdg-open (freedesktop.org), without a shell.
// The Linux window is not built yet (it needs cgo and WebKitGTK); this keeps the
// package portable.
type xdgShell struct{}

func newShell() Shell { return xdgShell{} }

func (xdgShell) Open(path string) error { return startDetached("xdg-open", path) }

// Reveal opens the file's folder; selecting the file needs the file manager's D-Bus
// interface, which comes with the Linux window.
func (xdgShell) Reveal(path string) error { return startDetached("xdg-open", filepath.Dir(path)) }

func (xdgShell) ShowFolder(dir string) error { return startDetached("xdg-open", dir) }

// canOpenWith: Open with… is not offered yet (see OpenWith); Startup tells the page.
const canOpenWith = false

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

func allowForegroundHandoff() {}

func prefersDark() bool { return false }

func webviewDataDir() string { return "" }
