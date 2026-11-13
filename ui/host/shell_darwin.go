//go:build darwin

package host

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"connectrpc.com/connect"

	"downex/internal/client"
)

// forbiddenNameChars cannot appear in a file name. (A ":" can: the Finder shows it as "/".)
const forbiddenNameChars = "/"

// CanOpenWith: macOS has no system dialog that lets a program have the user choose an app
// for a file, so Open with… is not offered there yet (MACOS-SPEC §5): Startup tells the
// page, which hides the button.
const CanOpenWith = false

// osRisk adds the bundles, scripts, installers and locations that macOS runs or follows
// when they are opened, and files with an execute permission bit (macRisk).
func osRisk(ext string, info fs.FileInfo) Risk { return macRisk(ext, info) }

const (
	// openTool hands files and folders to Launch Services, which opens them with their
	// application or in the Finder. Its absolute path: no search of $PATH.
	openTool = "/usr/bin/open"
	// openWait bounds the wait for openTool, which returns once Launch Services has
	// handed the file over (an app that is slow to start is not waited for longer).
	openWait = 10 * time.Second
	// openStderrMax bounds what is kept of openTool's error output.
	openStderrMax = 4 << 10
)

// macShell opens files with /usr/bin/open and shows them in the Finder with "open -R".
// No command line is ever built for a shell.
type macShell struct{}

// NewShell returns the desktop's file handling.
func NewShell() Shell { return macShell{} }

// Open opens file with the application that Launch Services chooses for its type.
func (macShell) Open(file string) error { return runOpen(file, file) }

// Reveal shows a Finder window on file's folder with file selected; it never opens file.
func (macShell) Reveal(file string) error { return runOpen(file, "-R", file) }

// ShowFolder opens a Finder window on dir. A folder that macOS may take for a bundle,
// which opening could run (macOpensAsItem), is selected in its parent instead.
func (macShell) ShowFolder(dir string) error {
	if macOpensAsItem(dir) {
		return runOpen(dir, "-R", dir)
	}
	return runOpen(dir, dir)
}

// OpenWith is not available on macOS yet (MACOS-SPEC §5; the page hides it: CanOpenWith).
func (macShell) OpenWith(string) error {
	return connect.NewError(connect.CodeUnimplemented,
		errors.New("choosing an app to open a file with is not available on macOS yet; use Open or Open folder"))
}

// runOpen runs openTool with args for target, an absolute path: open never takes it for
// an option ("-…") or a URL ("scheme:…"). It reports open's failure (no app for the type,
// a missing file) when open ends within openWait, and success when it is still running
// then; a goroutine reaps it.
func runOpen(target string, args ...string) error {
	if !filepath.IsAbs(target) {
		return fmt.Errorf("not an absolute path: %q", target)
	}
	cmd := exec.Command(openTool, args...)
	stderr := &headBuffer{max: openStderrMax}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(openWait)
	defer timer.Stop()
	select {
	case err := <-done:
		if err != nil {
			// Wait has returned: nothing writes to stderr any more.
			return openToolFailure(target, stderr.Bytes(), err)
		}
		return nil
	case <-timer.C:
		return nil
	}
}

// AllowForegroundHandoff lets the Downex windows that this process opens or hands over
// to take the focus from it. From macOS 14 on activation is cooperative: an app that is
// not active is activated only when the active app has yielded to it. A window is the
// active app when the user opens a progress window from it (StartProgressWindow), so it
// yields to Downex.app and its progress app (handoffBundles, YieldActivation), and the new
// window's request to be activated is granted. A second start that hands its arguments
// over (main) is not active yet and has nothing to yield: AppKit has not started, and
// nothing happens; nor before macOS 14, where apps activate themselves.
func AllowForegroundHandoff() {
	if l, err := client.FindLayout(os.Executable); err == nil {
		YieldActivation(handoffBundles(l)...)
	}
}

// YieldActivation lets the apps of bundles (app bundle paths) take the focus from this one
// (macOS 14+: NSApplication yieldActivationToApplicationWithBundleIdentifier:, on the main
// thread). This package has no AppKit (it builds without cgo): the macOS window backend
// sets it once, before its first window; until then it does nothing.
var YieldActivation = func(bundles ...string) {}

// SystemPrefersDark reports the system's dark appearance, for the window's background
// before it paints.
func SystemPrefersDark() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/bin/defaults", "read", "-g", "AppleInterfaceStyle").Output()
	return err == nil && interfaceStyleIsDark(out)
}
